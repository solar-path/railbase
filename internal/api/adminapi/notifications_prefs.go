package adminapi

// v1.7.35 §3.9.1 — admin endpoints for editing per-user notification
// preferences + user settings (quiet hours / digest mode). Closes the
// v1.5.3 "admin-side preferences editor deferred" note.
//
// Three endpoints, all gated by RequireAdmin in adminapi.Mount:
//
//	GET  /api/_admin/notifications/users
//	     — paginated list of distinct user_ids that have at least one
//	       row in `_notification_preferences` OR `_notification_user_settings`.
//	       Each row carries `user_id`, best-effort `email`, and the
//	       resolving auth-collection name (`collection`) — looked up by
//	       walking every registered auth collection in turn.
//
//	GET  /api/_admin/notifications/users/{user_id}/prefs
//	     — returns BOTH the prefs[] (kind × channel × enabled × frequency)
//	       and the per-user settings (quiet hours + digest). 404 when
//	       neither table has a row for the target user.
//
//	PUT  /api/_admin/notifications/users/{user_id}/prefs
//	     — accepts the same shape: UPSERT each prefs row + UPSERT the
//	       settings row. Emits a `notifications.admin_prefs_changed`
//	       audit event when d.Audit is wired.
//
// Why a separate file from notifications.go (the read-only cross-user
// log): the existing notifications.go is a query/listing surface only;
// this slice adds write semantics + a different domain entity (the
// user's posture, not their delivered notifications). Keeping them in
// distinct files keeps the file-level diff for v1.7.35 readable and
// matches the webhooks.go / cache.go split discipline elsewhere.
//
// Frequency field: the `_notification_preferences` table schema today
// only carries `enabled` (CHECK(channel IN ('inapp','email','push'))).
// The brief mentions a `frequency` column — we accept the field on the
// wire shape for forward-compat (the user-facing endpoint is on the
// roadmap to grow it) but ignore it on write and always return "" on
// read. If/when the column lands, the wire shape stays stable.
//
// Pool comes from adminapi.Deps. Tests pass a nil pool and exercise
// the short-circuit envelope to verify the param-parse paths.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/railbase/railbase/internal/audit"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/notifications"
	"github.com/railbase/railbase/internal/schema/registry"
)

// mountNotificationsPrefs registers the three admin prefs routes onto
// the parent /api/_admin group. Called from adminapi.Mount inside the
// RequireAdmin block. Nil-guards on d.Pool inside each handler so the
// route shape is always present, even before the pool is wired.
func (d *Deps) mountNotificationsPrefs(r chi.Router) {
	r.Get("/notifications/users", d.notificationsPrefsUsersHandler)
	r.Get("/notifications/users/{user_id}/prefs", d.notificationsPrefsGetHandler)
	r.Put("/notifications/users/{user_id}/prefs", d.notificationsPrefsPutHandler)
	// v1.7.36 — "send digest preview" button on the prefs editor.
	// Synthesises a sample digest email for a given user (using their
	// currently queued `_notification_deferred` rows with reason='digest'
	// or, if none, three fake notifications so the preview is still
	// useful) and sends it to a specified recipient — default: the
	// admin's own email from PrincipalFrom so the operator doesn't spam
	// the user just to eyeball the layout.
	r.Post("/notifications/users/{user_id}/digest-preview", d.notificationsDigestPreviewHandler)
	// v1.7.38 — "reset to defaults" path on the prefs editor. PUT
	// UPSERTs both tables; if an operator wants to wipe a user's
	// prefs + settings (e.g. user complains about settings drift,
	// operator wants a clean slate) they previously had to null-out
	// every field manually. DELETE drops both rows in a single
	// round-trip + emits an audit event for the rollback trail.
	r.Delete("/notifications/users/{user_id}/prefs", d.notificationsPrefsDeleteHandler)
}

// prefsUserRow is one entry in the user-list response. `email` is
// best-effort: when no auth collection contains the id, both fields
// are empty strings (the UI degrades to showing the truncated UUID).
type prefsUserRow struct {
	UserID     uuid.UUID `json:"user_id"`
	Email      string    `json:"email"`
	Collection string    `json:"collection"`
	HasPrefs   bool      `json:"has_prefs"`
	HasSettings bool     `json:"has_settings"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// notificationsPrefsUsersHandler — GET /api/_admin/notifications/users.
//
// Query params:
//
//	page     1-indexed, default 1
//	perPage  default 50, max 200
//	q        substring filter on the resolved email (case-insensitive).
//	         The DB query is unfiltered; the filter happens AFTER email
//	         resolution because emails live in N different auth tables.
//	         When q is empty we skip the per-row resolve work and only
//	         resolve the visible page — keeps p50 cheap.
//
// Pagination is page-based on the union of distinct user_ids ordered
// by the most-recently-updated row across both tables (newest first).
// The DB query uses a UNION so a user with ONLY a settings row OR
// ONLY a prefs row still appears.
func (d *Deps) notificationsPrefsUsersHandler(w http.ResponseWriter, r *http.Request) {
	const defaultPerPage = 50
	const maxPerPage = 200

	perPage := parseIntParam(r, "perPage", defaultPerPage)
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	page := parseIntParam(r, "page", 1)
	if page < 1 {
		page = 1
	}

	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "notifications prefs not configured"))
		return
	}

	emailFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	// Materialise the union of (user_id, max(updated_at), source flags).
	// One round-trip; serves both the listing and the total count.
	rows, err := pool.Query(r.Context(), `
		WITH u AS (
			SELECT user_id, MAX(updated_at) AS updated_at,
			       TRUE AS has_prefs, FALSE AS has_settings
			FROM _notification_preferences
			GROUP BY user_id
			UNION ALL
			SELECT user_id, updated_at,
			       FALSE AS has_prefs, TRUE AS has_settings
			FROM _notification_user_settings
		)
		SELECT user_id,
		       MAX(updated_at) AS updated_at,
		       bool_or(has_prefs) AS has_prefs,
		       bool_or(has_settings) AS has_settings
		FROM u
		GROUP BY user_id
		ORDER BY updated_at DESC, user_id ASC`)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list prefs users"))
		return
	}
	defer rows.Close()

	all := make([]prefsUserRow, 0, 64)
	for rows.Next() {
		var rec prefsUserRow
		if err := rows.Scan(&rec.UserID, &rec.UpdatedAt, &rec.HasPrefs, &rec.HasSettings); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan prefs user"))
			return
		}
		all = append(all, rec)
	}
	if err := rows.Err(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "iterate prefs users"))
		return
	}

	// Resolve emails. When a q filter is active we resolve every row
	// and then slice; when not, we resolve only the visible window so
	// large user counts don't trigger N auth-table lookups per request.
	resolver := newEmailResolver(pool)
	var filtered []prefsUserRow
	if emailFilter != "" {
		for i := range all {
			if email, col, ok := resolver.Lookup(r.Context(), all[i].UserID); ok {
				all[i].Email = email
				all[i].Collection = col
			}
			if strings.Contains(strings.ToLower(all[i].Email), emailFilter) {
				filtered = append(filtered, all[i])
			}
		}
	} else {
		filtered = all
	}

	total := int64(len(filtered))
	start := (page - 1) * perPage
	end := start + perPage
	if start > len(filtered) {
		filtered = nil
	} else {
		if end > len(filtered) {
			end = len(filtered)
		}
		filtered = filtered[start:end]
	}

	// When no q filter is active we resolved nothing above — do it now
	// for the visible window.
	if emailFilter == "" {
		for i := range filtered {
			if email, col, ok := resolver.Lookup(r.Context(), filtered[i].UserID); ok {
				filtered[i].Email = email
				filtered[i].Collection = col
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": total,
		"items":      filtered,
	})
}

// prefRow is one entry in the prefs[] array of the GET/PUT payload.
// Channel + enabled mirror the store's Preference struct verbatim;
// `frequency` is a forward-compat placeholder (always "" today — the
// `_notification_preferences` schema has no such column).
type prefRow struct {
	Kind      string                `json:"kind"`
	Channel   notifications.Channel `json:"channel"`
	Enabled   bool                  `json:"enabled"`
	Frequency string                `json:"frequency"`
}

// settingsBody mirrors notifications.UserSettings as a wire-friendly
// shape. Quiet-hours times are emitted as "HH:MM" / "HH:MM:SS"
// strings (TIME columns project that way on the wire) so the React
// time-input renderer roundtrips cleanly. Empty string = unset.
type settingsBody struct {
	QuietHoursStart string `json:"quiet_hours_start"`
	QuietHoursEnd   string `json:"quiet_hours_end"`
	QuietHoursTZ    string `json:"quiet_hours_tz"`
	DigestMode      string `json:"digest_mode"`
	DigestHour      int    `json:"digest_hour"`
	DigestDOW       int    `json:"digest_dow"`
	DigestTZ        string `json:"digest_tz"`
}

// prefsEnvelope is the GET / PUT shape — prefs[] and settings live
// in one envelope so the editor can submit both in one round-trip.
type prefsEnvelope struct {
	UserID   uuid.UUID    `json:"user_id"`
	Email    string       `json:"email,omitempty"`
	Prefs    []prefRow    `json:"prefs"`
	Settings settingsBody `json:"settings"`
}

// notificationsPrefsGetHandler — GET /api/_admin/notifications/users/{user_id}/prefs.
//
// Returns the full envelope (prefs[] + settings) for the target user.
// 404 only when neither table has a row for them; an empty prefs[] +
// default settings (digest_mode=off) is a legitimate 200 result for a
// user who exists in an auth collection but has never touched their
// notification posture.
func (d *Deps) notificationsPrefsGetHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := parsePrefsUserID(w, r)
	if !ok {
		return
	}
	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "notifications prefs not configured"))
		return
	}

	store := notifications.NewStore(pool)
	prefs, err := store.ListPreferences(r.Context(), userID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list prefs"))
		return
	}
	settings, err := store.GetUserSettings(r.Context(), userID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "get user settings"))
		return
	}

	// 404 only when neither side has a row. ListPreferences returns an
	// empty slice (not error) on no rows. GetUserSettings always returns
	// a UserSettings struct — defaults when the row is absent — so we
	// detect "absent" via the zero updated_at + empty digest_mode after
	// the defaulting overlay. Cross-check with a direct EXISTS probe:
	hasSettings, err := userHasSettingsRow(r.Context(), pool, userID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "probe settings"))
		return
	}
	if len(prefs) == 0 && !hasSettings {
		// 404 — but be helpful: maybe the user exists in an auth
		// collection. Try to surface their email so the operator knows
		// they typed a valid id and the prefs are simply empty.
		resolver := newEmailResolver(pool)
		if email, _, ok := resolver.Lookup(r.Context(), userID); ok {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "user %s exists (%s) but has no notification prefs or settings yet", userID, email))
			return
		}
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "user has no notification prefs or settings"))
		return
	}

	out := prefsEnvelope{
		UserID:   userID,
		Prefs:    make([]prefRow, 0, len(prefs)),
		Settings: settingsFromStore(settings),
	}
	resolver := newEmailResolver(pool)
	if email, _, ok := resolver.Lookup(r.Context(), userID); ok {
		out.Email = email
	}
	for _, p := range prefs {
		out.Prefs = append(out.Prefs, prefRow{
			Kind:    p.Kind,
			Channel: p.Channel,
			Enabled: p.Enabled,
			// Frequency intentionally empty — see file header note.
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// notificationsPrefsPutHandler — PUT /api/_admin/notifications/users/{user_id}/prefs.
//
// Accepts the same envelope shape: UPSERTs each row in prefs[] +
// UPSERTs the settings row. Idempotent. Per-channel validation
// matches the user-facing endpoint (inapp|email|push). Per-mode
// validation matches the store-level SetUserSettings (off|daily|weekly).
//
// Audit event `notifications.admin_prefs_changed` is emitted with the
// before-and-after envelope diff (the full new envelope as After;
// the loaded prefs as Before). The audit Writer redacts; the
// before/after blobs are safe to dump verbatim.
func (d *Deps) notificationsPrefsPutHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := parsePrefsUserID(w, r)
	if !ok {
		return
	}
	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "notifications prefs not configured"))
		return
	}

	var body prefsEnvelope
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}

	// Validate the prefs[] payload first; reject the whole request on
	// the first malformed entry rather than partially applying.
	for i, p := range body.Prefs {
		if strings.TrimSpace(p.Kind) == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "prefs[%d].kind is required", i))
			return
		}
		if p.Channel != notifications.ChannelInApp &&
			p.Channel != notifications.ChannelEmail &&
			p.Channel != notifications.ChannelPush {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "prefs[%d].channel must be inapp|email|push", i))
			return
		}
	}

	// Snapshot pre-change state for the audit row. Best-effort: a
	// fetch failure logs + continues; we don't want a transient DB
	// blip on the SELECT to block the PUT.
	store := notifications.NewStore(pool)
	beforePrefs, _ := store.ListPreferences(r.Context(), userID)
	beforeSettings, _ := store.GetUserSettings(r.Context(), userID)

	// Apply prefs first, settings second. If settings validation fails
	// (invalid tz, etc.) the prefs UPSERT survives — partial application
	// is preferable to "either everything or nothing" here because the
	// admin can re-submit the settings half independently in a follow-
	// up PUT. Tests pin both branches.
	for _, p := range body.Prefs {
		if err := store.SetPreference(r.Context(), userID, p.Kind, p.Channel, p.Enabled); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "set preference"))
			return
		}
	}
	us, err := settingsToStore(userID, body.Settings)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if err := store.SetUserSettings(r.Context(), us); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "set user settings"))
		return
	}

	// Audit. Nil-guarded — tests bypass.
	if d.Audit != nil {
		p := AdminPrincipalFrom(r.Context())
		_, _ = d.Audit.Write(r.Context(), audit.Event{
			UserID:         p.AdminID,
			UserCollection: "_admins",
			Event:          "notifications.admin_prefs_changed",
			Outcome:        audit.OutcomeSuccess,
			Before: map[string]any{
				"target_user_id": userID.String(),
				"prefs":          beforePrefs,
				"settings":       settingsFromStore(beforeSettings),
			},
			After: map[string]any{
				"target_user_id": userID.String(),
				"prefs":          body.Prefs,
				"settings":       body.Settings,
			},
			IP:        clientIP(r),
			UserAgent: r.Header.Get("User-Agent"),
		})
	}

	// Re-read so the response carries the canonical post-update state.
	out := prefsEnvelope{
		UserID:   userID,
		Settings: settingsFromStore(us),
		Prefs:    make([]prefRow, 0, len(body.Prefs)),
	}
	updated, err := store.ListPreferences(r.Context(), userID)
	if err == nil {
		for _, p := range updated {
			out.Prefs = append(out.Prefs, prefRow{
				Kind:    p.Kind,
				Channel: p.Channel,
				Enabled: p.Enabled,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// parsePrefsUserID extracts the {user_id} URL param and writes a typed
// 400 envelope on malformed input. Returns ok=false so the caller can
// short-circuit. Mirrors parseWebhookID's shape in webhooks.go.
func parsePrefsUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "user_id")
	id, err := uuid.Parse(raw)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "user_id must be a valid UUID"))
		return uuid.Nil, false
	}
	return id, true
}

// settingsFromStore converts the notifications.UserSettings struct to
// the wire shape. TIME values stringify as "HH:MM:SS"; empty when zero.
func settingsFromStore(us notifications.UserSettings) settingsBody {
	out := settingsBody{
		QuietHoursTZ: us.QuietHoursTZ,
		DigestMode:   us.DigestMode,
		DigestHour:   us.DigestHour,
		DigestDOW:    us.DigestDOW,
		DigestTZ:     us.DigestTZ,
	}
	if !us.QuietHoursStart.IsZero() {
		out.QuietHoursStart = us.QuietHoursStart.Format("15:04:05")
	}
	if !us.QuietHoursEnd.IsZero() {
		out.QuietHoursEnd = us.QuietHoursEnd.Format("15:04:05")
	}
	if out.DigestMode == "" {
		out.DigestMode = "off"
	}
	return out
}

// settingsToStore parses the wire shape back into notifications.UserSettings.
// Returns a validation error for malformed time strings; tz / digest
// validation is delegated to the store's own SetUserSettings (which
// runs after this).
func settingsToStore(userID uuid.UUID, s settingsBody) (notifications.UserSettings, error) {
	us := notifications.UserSettings{
		UserID:       userID,
		QuietHoursTZ: strings.TrimSpace(s.QuietHoursTZ),
		DigestMode:   strings.TrimSpace(s.DigestMode),
		DigestHour:   s.DigestHour,
		DigestDOW:    s.DigestDOW,
		DigestTZ:     strings.TrimSpace(s.DigestTZ),
	}
	if v := strings.TrimSpace(s.QuietHoursStart); v != "" {
		t, err := parseClockTime(v)
		if err != nil {
			return us, fmt.Errorf("quiet_hours_start: %w", err)
		}
		us.QuietHoursStart = t
	}
	if v := strings.TrimSpace(s.QuietHoursEnd); v != "" {
		t, err := parseClockTime(v)
		if err != nil {
			return us, fmt.Errorf("quiet_hours_end: %w", err)
		}
		us.QuietHoursEnd = t
	}
	if us.DigestMode == "" {
		us.DigestMode = "off"
	}
	return us, nil
}

// parseClockTime accepts "HH:MM" or "HH:MM:SS" — both shapes that the
// HTML <input type="time"> can emit. Returns a time.Time on the epoch
// day; the store-side TIME column projects to time-of-day only on the
// way out (date discarded).
func parseClockTime(s string) (time.Time, error) {
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("must be HH:MM or HH:MM:SS, got %q", s)
}

// userHasSettingsRow probes whether `_notification_user_settings` has
// a row for the given user. Cheap EXISTS query — used by the GET
// handler to distinguish "no row at all" (404) from "row exists with
// default-shaped values" (200, returned as defaults).
func userHasSettingsRow(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID uuid.UUID) (bool, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT 1 FROM _notification_user_settings WHERE user_id = $1`, userID).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// emailResolver walks the registered auth collections in turn to find
// the email belonging to a user_id. Cached per-request so the bulk
// list endpoint doesn't repeat lookups for the same id (rare, but
// cheap insurance). One round-trip per (auth-collection, user) — the
// first hit wins.
//
// Auth-collection ordering: registry.All returns alphabetical order,
// so resolution is deterministic across requests.
type emailResolver struct {
	pool  notifications.Querier
	cache map[uuid.UUID]emailHit
}

type emailHit struct {
	email      string
	collection string
	ok         bool
}

func newEmailResolver(pool notifications.Querier) *emailResolver {
	return &emailResolver{pool: pool, cache: map[uuid.UUID]emailHit{}}
}

// Lookup returns the first (email, collection) hit for userID, or
// ok=false when no auth collection contains the id. Safe to call
// concurrently from a single goroutine; not safe from multiple.
func (er *emailResolver) Lookup(ctx context.Context, userID uuid.UUID) (string, string, bool) {
	if hit, found := er.cache[userID]; found {
		return hit.email, hit.collection, hit.ok
	}
	hit := emailHit{}
	for _, col := range registry.All() {
		spec := col.Spec()
		if !spec.Auth {
			continue
		}
		// Identifier safety: registry collection names are validated by
		// the schema builder to match a strict regex (see
		// internal/schema/builder/validate.go), so direct interpolation
		// is safe — quoteIdent is a no-op for the same reason in
		// internal/schema/gen/sql.go.
		var email string
		err := er.pool.QueryRow(ctx,
			fmt.Sprintf(`SELECT email FROM %s WHERE id = $1`, spec.Name),
			userID,
		).Scan(&email)
		if err == nil {
			hit = emailHit{email: email, collection: spec.Name, ok: true}
			break
		}
		// Any error (no rows, missing column, broken table) → try the
		// next collection. The resolver is best-effort by design — a
		// missing email never breaks the prefs editor.
	}
	er.cache[userID] = hit
	return hit.email, hit.collection, hit.ok
}

// --- v1.7.36 digest preview ---
//
// POST /api/_admin/notifications/users/{user_id}/digest-preview
//
// Synthesises a sample digest email for a given user — using their
// currently queued `_notification_deferred` rows with reason='digest'
// when available, falling back to three fake "Sample notification"
// items when the user has nothing queued — and sends it through the
// `digest_preview` mailer template (a sibling to `digest_summary`
// with a `[Preview]` subject prefix baked into the frontmatter so it
// can never be confused with a real digest).
//
// Behaviours pinned by the test matrix:
//
//   - Unknown / malformed user_id surface as 400 (parsePrefsUserID)
//     or 404 (no rows in any auth collection).
//   - Up to 50 queued digest rows are bundled. More than 50 → only
//     the most-recent 50 land in the preview (a real digest cron
//     processes 500 at a time; the operator preview cap is tighter
//     because the preview is for eyeballing layout, not a stress test).
//   - Recipient defaults to the admin's own email looked up from the
//     `_admins` table by PrincipalFrom(ctx).AdminID. An explicit
//     `recipient` in the body wins — useful for cross-checking what
//     the user themselves would see without actually sending to them.
//
// Audit event `notifications.admin_digest_preview_sent` is emitted on
// success with the (target user_id, recipient, kind_count) tuple so
// the timeline shows who previewed what and to whom.

// digestPreviewRequest is the inbound body. Empty body is fine — both
// fields are optional.
type digestPreviewRequest struct {
	Recipient string `json:"recipient"`
}

// digestPreviewResponse is the success envelope. `kind_count` reflects
// the number of distinct sample/queued items that landed in the email
// body — useful for the toast pill ("Sent to alice@example.com · 5 items").
type digestPreviewResponse struct {
	Sent      bool   `json:"sent"`
	Recipient string `json:"recipient"`
	KindCount int    `json:"kind_count"`
}

// notificationsDigestPreviewHandler — POST /api/_admin/notifications/
// users/{user_id}/digest-preview.
//
// Defensive ordering:
//   1. URL param parse (400 on malformed UUID).
//   2. Pool / mailer nil-guard (503 — keeps the rest of the admin
//      surface reachable on deployments where the mailer isn't wired).
//   3. Body decode is best-effort: empty body == defaults.
//   4. Recipient resolution — explicit body wins; else admin's email
//      from PrincipalFrom; else 400 (we won't guess).
//   5. Load up to 50 queued digest deferrals → fake fallback when zero.
//   6. SendTemplate via `digest_preview` (subject "[Preview] ...").
//   7. Audit + 200.
func (d *Deps) notificationsDigestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := parsePrefsUserID(w, r)
	if !ok {
		return
	}
	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "notifications prefs not configured"))
		return
	}
	if d.Mailer == nil {
		// 503 via CodeInternal: same posture as the other "missing
		// dependency" branches in this file. The operator sees a
		// typed envelope rather than a missing-route 404.
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "digest preview unavailable: mailer not configured"))
		return
	}

	// Body decode. Empty body is a no-op.
	var body digestPreviewRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
			return
		}
	}

	// 404 when the target user can't be located in any auth collection
	// AND has no queued digest rows / settings — gives the operator a
	// crisp signal "you typed a wrong id".
	resolver := newEmailResolver(pool)
	_, _, userExists := resolver.Lookup(r.Context(), userID)
	if !userExists {
		// Also accept the user when they have prefs/settings rows even
		// though no auth-collection email — the prefs editor itself
		// renders such users, so the preview should too.
		hasSettings, _ := userHasSettingsRow(r.Context(), pool, userID)
		store := notifications.NewStore(pool)
		prefs, _ := store.ListPreferences(r.Context(), userID)
		if !hasSettings && len(prefs) == 0 {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "user not found in any auth collection"))
			return
		}
	}

	// Recipient resolution. Body wins; else admin email.
	recipient := strings.TrimSpace(body.Recipient)
	if recipient == "" {
		p := AdminPrincipalFrom(r.Context())
		if p.AdminID == uuid.Nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "recipient required (no admin principal in context)"))
			return
		}
		if d.Admins == nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "admin store not configured"))
			return
		}
		admin, err := d.Admins.GetByID(r.Context(), p.AdminID)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load admin email"))
			return
		}
		recipient = admin.Email
	}
	if recipient == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "recipient resolved to empty"))
		return
	}

	// Load up to 50 queued digest deferrals + materialise the bundled
	// notification rows. We hand-roll the query rather than reaching for
	// Store.ListDeferredDue because that helper filters on flush_after
	// <= now — the preview should include EVERY queued row regardless of
	// its scheduled flush time.
	const previewCap = 50
	items, err := loadQueuedDigestItems(r.Context(), pool, userID, previewCap)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load queued digest items"))
		return
	}
	if len(items) == 0 {
		// Fake-data fallback so the preview is still useful for the
		// "no queued rows yet" common case.
		items = []notifications.DigestItem{
			{Kind: "system", Title: "Sample notification 1", Body: "This is a synthesised preview item. Real digests will list the user's actual queued notifications."},
			{Kind: "system", Title: "Sample notification 2", Body: "When the user has notifications queued under reason='digest', they appear here verbatim."},
			{Kind: "system", Title: "Sample notification 3", Body: "Preview emails are subject-prefixed with [Preview] so they're never confused with the real thing."},
		}
	}

	// Resolve a sensible Mode for the subject / body — falls back to
	// "daily" when the user hasn't configured one (matches the
	// FlushDeferred fallback in quiet_digest.go).
	store := notifications.NewStore(pool)
	us, _ := store.GetUserSettings(r.Context(), userID)
	mode := us.DigestMode
	if mode == "" || mode == "off" {
		mode = "daily"
	}

	data := map[string]any{
		"Mode":  mode,
		"Count": len(items),
		"Items": items,
	}
	if err := d.Mailer.SendTemplate(r.Context(), recipient, "digest_preview", data); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "send digest preview"))
		return
	}

	// Audit. Nil-guarded — tests bypass.
	if d.Audit != nil {
		p := AdminPrincipalFrom(r.Context())
		_, _ = d.Audit.Write(r.Context(), audit.Event{
			UserID:         p.AdminID,
			UserCollection: "_admins",
			Event:          "notifications.admin_digest_preview_sent",
			Outcome:        audit.OutcomeSuccess,
			After: map[string]any{
				"target_user_id": userID.String(),
				"recipient":      recipient,
				"kind_count":     len(items),
				"mode":           mode,
			},
			IP:        clientIP(r),
			UserAgent: r.Header.Get("User-Agent"),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(digestPreviewResponse{
		Sent:      true,
		Recipient: recipient,
		KindCount: len(items),
	})
}

// loadQueuedDigestItems pulls up to `limit` of the user's currently
// queued `_notification_deferred` rows with reason='digest' and
// hydrates the underlying notification rows into DigestItem shape.
//
// Newest-deferred-first (so the operator sees the same ordering they'd
// see in the real digest). We can afford to ignore flush_after here —
// the preview is "what would this user's NEXT digest look like" — so
// every queued row counts, even one freshly enqueued seconds ago.
func loadQueuedDigestItems(ctx context.Context, pool notifications.Querier, userID uuid.UUID, limit int) ([]notifications.DigestItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT n.kind, n.title, n.body, n.data
		FROM _notification_deferred d
		JOIN _notifications n ON n.id = d.notification_id
		WHERE d.user_id = $1 AND d.reason = 'digest'
		ORDER BY d.deferred_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notifications.DigestItem
	for rows.Next() {
		var (
			kind, title, body string
			dataJSON          []byte
		)
		if err := rows.Scan(&kind, &title, &body, &dataJSON); err != nil {
			return nil, err
		}
		item := notifications.DigestItem{Kind: kind, Title: title, Body: body}
		if len(dataJSON) > 0 && string(dataJSON) != "{}" && string(dataJSON) != "null" {
			var m map[string]any
			if err := json.Unmarshal(dataJSON, &m); err == nil {
				item.Data = m
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// notificationsPrefsDeleteHandler — DELETE /api/_admin/notifications/users/{user_id}/prefs.
//
// v1.7.38 — "reset to defaults". Drops all `_notification_preferences`
// rows AND the `_notification_user_settings` row for the user in one
// best-effort sequence. Idempotent: zero rows on either side is a
// valid 200 response. The follow-up `GetUserSettings` / `ChannelEnabled`
// calls then fall back to defaults (digest_mode=off; per-channel
// defaults policy on prefs).
//
// 404 ONLY when neither table has a row — mirrors the GET handler's
// "user has nothing here" detection so the operator gets a useful
// "nothing to delete" signal rather than a silent 200.
//
// Audit event: `notifications.admin_prefs_reset`, with the snapshot
// of what was deleted in the `before` slot.
func (d *Deps) notificationsPrefsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := parsePrefsUserID(w, r)
	if !ok {
		return
	}
	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "notifications prefs not configured"))
		return
	}
	store := notifications.NewStore(pool)

	// Snapshot for audit before we drop.
	beforePrefs, _ := store.ListPreferences(r.Context(), userID)
	beforeSettings, _ := store.GetUserSettings(r.Context(), userID)
	hasSettings, _ := userHasSettingsRow(r.Context(), pool, userID)

	if len(beforePrefs) == 0 && !hasSettings {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "user has no notification prefs or settings to reset"))
		return
	}

	prefsDeleted, err := store.DeleteAllPreferences(r.Context(), userID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete prefs"))
		return
	}
	settingsDeleted, err := store.DeleteUserSettings(r.Context(), userID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete user settings"))
		return
	}

	// Audit. Nil-guarded — tests bypass.
	if d.Audit != nil {
		p := AdminPrincipalFrom(r.Context())
		_, _ = d.Audit.Write(r.Context(), audit.Event{
			UserID:         p.AdminID,
			UserCollection: "_admins",
			Event:          "notifications.admin_prefs_reset",
			Outcome:        audit.OutcomeSuccess,
			Before: map[string]any{
				"target_user_id":   userID.String(),
				"prefs":            beforePrefs,
				"settings":         settingsFromStore(beforeSettings),
				"prefs_deleted":    prefsDeleted,
				"settings_deleted": settingsDeleted,
			},
			IP:        clientIP(r),
			UserAgent: r.Header.Get("User-Agent"),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"reset":            true,
		"prefs_deleted":    prefsDeleted,
		"settings_deleted": settingsDeleted,
	})
}
