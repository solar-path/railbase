// quiet_digest.go — v1.7.34 quiet hours + digest delivery paths.
//
// Decision points layered into Send (see notifications.go):
//
//   1. In-app row always inserted (so the bell view stays consistent).
//   2. For email/push channels, the user's UserSettings row is consulted:
//      - If quiet hours active at Now() in their tz → side-effect
//        deferred (`_notification_deferred` with reason='quiet_hours',
//        flush_after = end-of-window).
//      - Else if digest_mode != 'off' AND priority < urgent → deferred
//        with reason='digest', flush_after = next_digest_time().
//      - Else → side-effect fires inline as before.
//
//   3. The cron builtin `flush_deferred_notifications` (registered via
//      jobs.RegisterNotificationBuiltins) walks past-due rows on a
//      */5 cadence:
//      - quiet_hours rows: replay through Send with a sentinel that
//        bypasses the deferral check (otherwise we'd loop).
//      - digest rows: group by user, build ONE digest email
//        summarising N notifications, send via mailer.SendTemplate
//        with the "digest_summary" template, mark all included
//        notifications as `digested_at = now()` so they don't get
//        re-batched on the next flush tick.
//
// Precedence rule when a user has BOTH quiet hours AND a digest:
//   quiet hours wins. Rationale: the user has explicitly said "don't
//   disturb me right now" — sending a digest during quiet hours would
//   violate that contract. The deferred-as-quiet row will flush AFTER
//   the window expires, at which point a fresh Send decision is made
//   (likely landing in the digest bucket on the next pass).
//
// Urgent priority bypass: a notification with Priority == "urgent"
// skips BOTH quiet-hours and digest gating. Urgent already exists as
// a CHECK-constrained enum value in `_notifications.priority` (see
// migration 0017). Anything else (low/normal/high) is digest-eligible.

package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// UserSettings is one row in `_notification_user_settings`. NULL
// times collapse to a zero time.Time on the Go side (caller checks
// IsZero before applying the window).
type UserSettings struct {
	UserID          uuid.UUID
	QuietHoursStart time.Time // zero = disabled
	QuietHoursEnd   time.Time // zero = disabled
	QuietHoursTZ    string    // "" = UTC
	DigestMode      string    // "off"|"daily"|"weekly"
	DigestHour      int       // 0..23
	DigestDOW       int       // 0..6 (0=Sunday)
	DigestTZ        string    // "" = falls back to QuietHoursTZ, then UTC
	UpdatedAt       time.Time
}

// DeferredRow is one entry in `_notification_deferred`.
type DeferredRow struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	NotificationID uuid.UUID
	DeferredAt     time.Time
	FlushAfter     time.Time
	Reason         string // "quiet_hours"|"digest"
}

// GetUserSettings returns the per-user notification config. Missing
// row is NOT an error — returns a zero-value UserSettings (which
// withinQuietHours / digest helpers correctly read as "disabled").
func (s *Store) GetUserSettings(ctx context.Context, userID uuid.UUID) (UserSettings, error) {
	out := UserSettings{UserID: userID, DigestMode: "off", DigestHour: 8, DigestDOW: 1}
	// pgx returns "no rows" on QueryRow.Scan; we treat that as "use defaults".
	// TIME columns scan into pgtype-friendly intermediaries; we accept
	// them as time.Time via Postgres's "epoch + time-of-day" round-trip.
	var (
		qhStart, qhEnd                       *time.Time
		qhTZ, digestTZ                       *string
		digestMode                           string
		digestHour, digestDOW                int16
		updatedAt                            time.Time
	)
	err := s.q.QueryRow(ctx, `
		SELECT quiet_hours_start, quiet_hours_end, quiet_hours_tz,
		       digest_mode, digest_hour, digest_dow, digest_tz, updated_at
		FROM _notification_user_settings
		WHERE user_id = $1`, userID).Scan(
		&qhStart, &qhEnd, &qhTZ,
		&digestMode, &digestHour, &digestDOW, &digestTZ, &updatedAt,
	)
	if err != nil {
		// Treat ALL errors as "no row" — defaults. This includes
		// pgx.ErrNoRows. A real wiring failure would surface on the
		// subsequent Insert anyway.
		return out, nil
	}
	if qhStart != nil {
		out.QuietHoursStart = *qhStart
	}
	if qhEnd != nil {
		out.QuietHoursEnd = *qhEnd
	}
	if qhTZ != nil {
		out.QuietHoursTZ = *qhTZ
	}
	out.DigestMode = digestMode
	out.DigestHour = int(digestHour)
	out.DigestDOW = int(digestDOW)
	if digestTZ != nil {
		out.DigestTZ = *digestTZ
	}
	out.UpdatedAt = updatedAt
	return out, nil
}

// SetUserSettings upserts the per-user notification config. TZ
// fields are validated via time.LoadLocation before write — a
// malformed tz returns an error and the row is NOT touched.
// DeleteUserSettings drops the `_notification_user_settings` row for
// a user. Returns true if a row was deleted, false if there was none.
// Subsequent GetUserSettings() falls back to defaults. Used by the
// admin "reset to defaults" surface (v1.7.38).
func (s *Store) DeleteUserSettings(ctx context.Context, userID uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx,
		`DELETE FROM _notification_user_settings WHERE user_id = $1`,
		userID)
	if err != nil {
		return false, fmt.Errorf("notifications: delete user_settings: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) SetUserSettings(ctx context.Context, us UserSettings) error {
	if us.UserID == uuid.Nil {
		return fmt.Errorf("notifications: user_id required")
	}
	if us.QuietHoursTZ != "" {
		if _, err := time.LoadLocation(us.QuietHoursTZ); err != nil {
			return fmt.Errorf("notifications: invalid quiet_hours_tz %q: %w", us.QuietHoursTZ, err)
		}
	}
	if us.DigestTZ != "" {
		if _, err := time.LoadLocation(us.DigestTZ); err != nil {
			return fmt.Errorf("notifications: invalid digest_tz %q: %w", us.DigestTZ, err)
		}
	}
	switch us.DigestMode {
	case "", "off", "daily", "weekly":
	default:
		return fmt.Errorf("notifications: invalid digest_mode %q", us.DigestMode)
	}
	if us.DigestMode == "" {
		us.DigestMode = "off"
	}
	if us.DigestHour < 0 || us.DigestHour > 23 {
		return fmt.Errorf("notifications: invalid digest_hour %d", us.DigestHour)
	}
	if us.DigestDOW < 0 || us.DigestDOW > 6 {
		return fmt.Errorf("notifications: invalid digest_dow %d", us.DigestDOW)
	}

	var qhStart, qhEnd *time.Time
	if !us.QuietHoursStart.IsZero() {
		qhStart = &us.QuietHoursStart
	}
	if !us.QuietHoursEnd.IsZero() {
		qhEnd = &us.QuietHoursEnd
	}
	var qhTZ, digestTZ *string
	if us.QuietHoursTZ != "" {
		qhTZ = &us.QuietHoursTZ
	}
	if us.DigestTZ != "" {
		digestTZ = &us.DigestTZ
	}
	_, err := s.q.Exec(ctx, `
		INSERT INTO _notification_user_settings
			(user_id, quiet_hours_start, quiet_hours_end, quiet_hours_tz,
			 digest_mode, digest_hour, digest_dow, digest_tz, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (user_id) DO UPDATE SET
			quiet_hours_start = EXCLUDED.quiet_hours_start,
			quiet_hours_end   = EXCLUDED.quiet_hours_end,
			quiet_hours_tz    = EXCLUDED.quiet_hours_tz,
			digest_mode       = EXCLUDED.digest_mode,
			digest_hour       = EXCLUDED.digest_hour,
			digest_dow        = EXCLUDED.digest_dow,
			digest_tz         = EXCLUDED.digest_tz,
			updated_at        = now()`,
		us.UserID, qhStart, qhEnd, qhTZ,
		us.DigestMode, int16(us.DigestHour), int16(us.DigestDOW), digestTZ,
	)
	if err != nil {
		return fmt.Errorf("notifications: set user settings: %w", err)
	}
	return nil
}

// InsertDeferred adds one row to `_notification_deferred`.
func (s *Store) InsertDeferred(ctx context.Context, d *DeferredRow) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.Must(uuid.NewV7())
	}
	if d.DeferredAt.IsZero() {
		d.DeferredAt = time.Now().UTC()
	}
	_, err := s.q.Exec(ctx, `
		INSERT INTO _notification_deferred
			(id, user_id, notification_id, deferred_at, flush_after, reason)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		d.ID, d.UserID, d.NotificationID, d.DeferredAt, d.FlushAfter, d.Reason)
	if err != nil {
		return fmt.Errorf("notifications: insert deferred: %w", err)
	}
	return nil
}

// ListDeferredDue returns deferred rows whose flush_after <= now.
// Newest-deferred first within reason so digest grouping has stable
// ordering. limit caps result size (callers iterate in batches).
func (s *Store) ListDeferredDue(ctx context.Context, now time.Time, limit int) ([]*DeferredRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.q.Query(ctx, `
		SELECT id, user_id, notification_id, deferred_at, flush_after, reason
		FROM _notification_deferred
		WHERE flush_after <= $1
		ORDER BY reason ASC, deferred_at ASC
		LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("notifications: list deferred: %w", err)
	}
	defer rows.Close()
	var out []*DeferredRow
	for rows.Next() {
		d := &DeferredRow{}
		if err := rows.Scan(&d.ID, &d.UserID, &d.NotificationID, &d.DeferredAt, &d.FlushAfter, &d.Reason); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDeferred removes deferred rows by id. Used after a flush
// flushes them either by direct re-send (quiet hours) or by bundling
// into a digest.
func (s *Store) DeleteDeferred(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.q.Exec(ctx, `DELETE FROM _notification_deferred WHERE id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("notifications: delete deferred: %w", err)
	}
	return nil
}

// LoadNotification reads one row by id. Used by the flush handler to
// hydrate digest content + replay quiet-hours sends.
func (s *Store) LoadNotification(ctx context.Context, id uuid.UUID) (*Notification, error) {
	row := s.q.QueryRow(ctx, `
		SELECT id, user_id, tenant_id, kind, title, body, data, priority,
		       read_at, expires_at, created_at
		FROM _notifications WHERE id = $1`, id)
	return scanNotification(row)
}

// MarkDigested stamps `digested_at = now()` on every notification id.
// Idempotent. Used by the digest flusher post-email to record that
// those rows have already been bundled.
func (s *Store) MarkDigested(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.q.Exec(ctx, `
		UPDATE _notifications SET digested_at = now()
		WHERE id = ANY($1) AND digested_at IS NULL`, ids)
	if err != nil {
		return fmt.Errorf("notifications: mark digested: %w", err)
	}
	return nil
}

// --- Pure-Go time math helpers ---

// withinQuietHours reports whether now falls within [start, end) in
// the user's tz. Wrap-around (e.g. 22:00 → 07:00) is supported. Also
// returns the wall-clock end-of-window in the SAME tz, projected to
// the nearest future occurrence (caller uses this as flush_after).
//
// When start.IsZero() or end.IsZero(), quiet hours are disabled and
// the function returns (false, time.Time{}) — caller short-circuits.
//
// tz "" is interpreted as UTC; an unparseable tz is also UTC (caller
// validates on Set, so this is defensive only).
func withinQuietHours(now time.Time, tz string, start, end time.Time) (bool, time.Time) {
	if start.IsZero() || end.IsZero() {
		return false, time.Time{}
	}
	loc := loadLocOrUTC(tz)
	local := now.In(loc)

	// Build today's start/end as wall-clock times in loc.
	todayStart := time.Date(local.Year(), local.Month(), local.Day(),
		start.Hour(), start.Minute(), start.Second(), 0, loc)
	todayEnd := time.Date(local.Year(), local.Month(), local.Day(),
		end.Hour(), end.Minute(), end.Second(), 0, loc)

	wraps := !todayEnd.After(todayStart)
	if !wraps {
		// Simple case: 09:00-17:00. Within iff start <= now < end.
		if !local.Before(todayStart) && local.Before(todayEnd) {
			return true, todayEnd
		}
		return false, time.Time{}
	}
	// Wrap case: 22:00-07:00. Either [start, midnight) OR [midnight, end).
	// Branch A: now is after today's start → window ends TOMORROW at end.
	if !local.Before(todayStart) {
		nextEnd := todayEnd.Add(24 * time.Hour)
		return true, nextEnd
	}
	// Branch B: now is before today's end (we're past midnight, before wake).
	if local.Before(todayEnd) {
		return true, todayEnd
	}
	return false, time.Time{}
}

// nextDigestTime returns the next time the user's digest should fire,
// in absolute time. For mode='daily': next occurrence of hour in tz.
// For mode='weekly': next occurrence of (dow, hour). For 'off' or
// unrecognised: returns the zero time (caller short-circuits).
func nextDigestTime(now time.Time, mode string, hour, dow int, tz string) time.Time {
	loc := loadLocOrUTC(tz)
	local := now.In(loc)
	switch mode {
	case "daily":
		today := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, loc)
		if today.After(local) {
			return today
		}
		return today.Add(24 * time.Hour)
	case "weekly":
		// Find the next day where weekday == dow at the given hour.
		// time.Weekday is 0=Sunday..6=Saturday — matches our convention.
		todayDOW := int(local.Weekday())
		daysAhead := (dow - todayDOW + 7) % 7
		candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, loc).
			AddDate(0, 0, daysAhead)
		if !candidate.After(local) {
			candidate = candidate.AddDate(0, 0, 7)
		}
		return candidate
	default:
		return time.Time{}
	}
}

func loadLocOrUTC(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// --- Digest mailer payload ---

// DigestItem is one row in a digest email.
type DigestItem struct {
	Title string         `json:"title"`
	Body  string         `json:"body,omitempty"`
	Kind  string         `json:"kind"`
	Data  map[string]any `json:"data,omitempty"`
}

// buildDigestData renders the template-context map for the digest
// email. Operators override the `digest_summary` template by adding
// a same-named .md file to their on-disk mailer overrides dir.
func buildDigestData(mode string, items []DigestItem) map[string]any {
	return map[string]any{
		"Mode":  mode,
		"Count": len(items),
		"Items": items,
	}
}

// dataAsMap pulls Notification.Data out as a plain map for digest
// rendering — keeps the buildDigestData side decoupled from JSON
// round-trips at the seam.
func dataAsMap(raw []byte) map[string]any {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// FlushDeferred drains past-due rows from `_notification_deferred`.
// Wired via jobs.RegisterNotificationBuiltins as the
// `flush_deferred_notifications` cron handler. Returns the count of
// deferred rows processed (digest or quiet-hours combined).
//
// Quiet-hours rows replay through Send with the deferral check
// bypassed (otherwise the just-expired window could re-defer them
// during edge-of-second drift). Digest rows are grouped by user,
// bundled into a single email via the `digest_summary` template, and
// then the underlying _notifications rows are stamped with
// digested_at so they don't get re-batched on the next pass.
//
// Best-effort: an error on one user's flush logs + continues rather
// than aborting the whole tick. The next */5 run picks up survivors.
func (s *Service) FlushDeferred(ctx context.Context) (int, error) {
	if s.Store == nil {
		return 0, nil
	}
	now := time.Now().UTC()
	due, err := s.Store.ListDeferredDue(ctx, now, 500)
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}

	// Partition by reason.
	var quiet []*DeferredRow
	digestByUser := make(map[uuid.UUID][]*DeferredRow)
	for _, d := range due {
		switch d.Reason {
		case "quiet_hours":
			quiet = append(quiet, d)
		case "digest":
			digestByUser[d.UserID] = append(digestByUser[d.UserID], d)
		}
	}

	processed := 0

	// --- Quiet-hours replay ---
	//
	// Re-hydrate each notification and re-call Send with bypassDeferral
	// so the side-effect fires. We don't re-insert the in-app row
	// (already there from the original Send); we just want the email/
	// push leg to run. The simplest path is a focused replay that
	// triggers ONLY the email side: the in-app branch in sendInternal
	// guards on "if enabled[ChannelInApp]" + Store.Insert, but we'd
	// double-insert. Instead, we open-code the email leg here.
	for _, d := range quiet {
		n, err := s.Store.LoadNotification(ctx, d.NotificationID)
		if err != nil {
			if s.Log != nil {
				s.Log.Warn("notifications: quiet flush load failed", "id", d.NotificationID, "err", err)
			}
			continue
		}
		if s.Mailer != nil && s.GetEmail != nil {
			email, eErr := s.GetEmail(ctx, n.UserID)
			if eErr == nil && email != "" {
				tmpl := "notification_" + n.Kind
				data := map[string]any{
					"title":  n.Title,
					"body":   n.Body,
					"data":   n.Data,
					"kind":   n.Kind,
					"userId": n.UserID.String(),
				}
				if sendErr := s.Mailer.SendTemplate(ctx, email, tmpl, data); sendErr != nil && s.Log != nil {
					s.Log.Warn("notifications: quiet flush email failed", "user", n.UserID, "err", sendErr)
				}
			}
		}
		if err := s.Store.DeleteDeferred(ctx, []uuid.UUID{d.ID}); err != nil && s.Log != nil {
			s.Log.Warn("notifications: quiet flush delete failed", "id", d.ID, "err", err)
		}
		processed++
	}

	// --- Digest assembly ---
	for userID, rows := range digestByUser {
		items := make([]DigestItem, 0, len(rows))
		notifIDs := make([]uuid.UUID, 0, len(rows))
		deferredIDs := make([]uuid.UUID, 0, len(rows))
		for _, d := range rows {
			n, err := s.Store.LoadNotification(ctx, d.NotificationID)
			if err != nil {
				if s.Log != nil {
					s.Log.Warn("notifications: digest load failed", "id", d.NotificationID, "err", err)
				}
				continue
			}
			items = append(items, DigestItem{
				Title: n.Title,
				Body:  n.Body,
				Kind:  n.Kind,
				Data:  n.Data,
			})
			notifIDs = append(notifIDs, n.ID)
			deferredIDs = append(deferredIDs, d.ID)
		}
		if len(items) == 0 {
			continue
		}
		// Discover the user's chosen mode for the subject line. Load
		// settings ONCE per user — the deferred rows themselves don't
		// carry mode (it's a user-level toggle).
		us, _ := s.Store.GetUserSettings(ctx, userID)
		mode := us.DigestMode
		if mode == "" || mode == "off" {
			// User toggled digest off after rows were enqueued. Flush
			// them as individual emails instead — best-effort fall-back.
			mode = "daily"
		}
		if s.Mailer != nil && s.GetEmail != nil {
			email, eErr := s.GetEmail(ctx, userID)
			if eErr == nil && email != "" {
				if sendErr := s.Mailer.SendTemplate(ctx, email, "digest_summary", buildDigestData(mode, items)); sendErr != nil && s.Log != nil {
					s.Log.Warn("notifications: digest email failed", "user", userID, "err", sendErr)
				}
			}
		}
		if err := s.Store.MarkDigested(ctx, notifIDs); err != nil && s.Log != nil {
			s.Log.Warn("notifications: mark digested failed", "user", userID, "err", err)
		}
		if err := s.Store.DeleteDeferred(ctx, deferredIDs); err != nil && s.Log != nil {
			s.Log.Warn("notifications: digest delete deferred failed", "user", userID, "err", err)
		}
		processed += len(rows)
	}

	return processed, nil
}
