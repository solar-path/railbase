// Package notifications is the unified user-notification system
// (§3.9.1 / docs/20).
//
// One Send call:
//   - Resolves which channels (inapp / email / push) the user has
//     opted into for the given kind. Missing preferences → channel
//     default (currently "enabled" for inapp + email; "enabled" for
//     push but that channel is deferred to the railbase-push plugin).
//   - Persists the notification row in _notifications (so the UI
//     can fetch it later — even if the user is offline at send time).
//   - Publishes a realtime event on `notification.<user_id>` so the
//     UI sees it immediately when the user IS online (SSE subscriber).
//   - If email channel is on, kicks off mailer.SendTemplate (mailer
//     handles its own retries / rate limits).
//
// Email integration is opt-in: pass a non-nil Mailer to NewService.
// Without it, email channel is silently skipped (the row is still
// persisted; just no email leaves).
//
// What's deliberately NOT in this milestone:
//   - Push channel (FCM/APNs) — railbase-push plugin.
//   - Per-tenant template overrides — needs the mailer template
//     loader extension; v1.5.x.
//   - Quiet hours — needs timezone-aware priority bucketing.
//   - Digest / aggregation ("5 new comments") — operator-side cron.
//   - JS hooks bindings ($notifications.send) — §3.4 hooks epic.
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/railbase/railbase/internal/eventbus"
)

// Channel names. Matches the CHECK constraint in migration 0017.
type Channel string

const (
	ChannelInApp Channel = "inapp"
	ChannelEmail Channel = "email"
	ChannelPush  Channel = "push"
)

// Priority levels. Matches the CHECK constraint.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// TopicNotification is the bus topic. Subscribers receive
// `Event{Topic: "notification", Payload: Notification{...}}`. UI
// fans this out to per-user SSE streams.
const TopicNotification = "notification"

// Notification is one row in _notifications.
type Notification struct {
	ID        uuid.UUID      `json:"id"`
	UserID    uuid.UUID      `json:"user_id"`
	TenantID *uuid.UUID      `json:"tenant_id,omitempty"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Priority  Priority       `json:"priority"`
	ReadAt    *time.Time     `json:"read_at,omitempty"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Preference is one row in _notification_preferences.
type Preference struct {
	UserID    uuid.UUID `json:"user_id"`
	Kind      string    `json:"kind"`
	Channel   Channel   `json:"channel"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Querier is the minimal pgx surface the store depends on.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is the persistent storage layer for notifications.
type Store struct {
	q Querier
}

// NewStore wraps a Querier (pool or tx).
func NewStore(q Querier) *Store { return &Store{q: q} }

// --- Notification CRUD ---

// SendInput is what Service.Send accepts. Channels is optional —
// when nil, all channels enabled by preference are used.
type SendInput struct {
	UserID    uuid.UUID
	TenantID  *uuid.UUID
	Kind      string
	Title     string
	Body      string
	Data      map[string]any
	Priority  Priority
	ExpiresAt *time.Time

	// Channels caps which channels to deliver on. nil = honour
	// preferences; non-empty restricts to the intersection.
	Channels []Channel
}

// Insert writes one notification row. Used by Service.Send;
// operators can call this directly to bypass channel resolution.
func (s *Store) Insert(ctx context.Context, n *Notification) error {
	if n.ID == uuid.Nil {
		n.ID = uuid.Must(uuid.NewV7())
	}
	if n.Priority == "" {
		n.Priority = PriorityNormal
	}
	dataJSON, err := json.Marshal(or(n.Data, map[string]any{}))
	if err != nil {
		return fmt.Errorf("notifications: marshal data: %w", err)
	}
	err = s.q.QueryRow(ctx, `
		INSERT INTO _notifications (id, user_id, tenant_id, kind, title, body, data, priority, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at`,
		n.ID, n.UserID, n.TenantID, n.Kind, n.Title, n.Body, dataJSON, n.Priority, n.ExpiresAt,
	).Scan(&n.CreatedAt)
	if err != nil {
		return fmt.Errorf("notifications: insert: %w", err)
	}
	return nil
}

// List returns recent notifications for one user. unreadOnly filters
// to read_at IS NULL. Newest first. limit caps result rows.
func (s *Store) List(ctx context.Context, userID uuid.UUID, unreadOnly bool, limit int) ([]*Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id, user_id, tenant_id, kind, title, body, data, priority, read_at, expires_at, created_at
	      FROM _notifications WHERE user_id = $1`
	args := []any{userID}
	if unreadOnly {
		q += " AND read_at IS NULL"
	}
	q += " ORDER BY created_at DESC LIMIT $2"
	args = append(args, limit)
	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("notifications: list: %w", err)
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListAllFilter is the cross-user filter for the admin "Notifications"
// screen (§3.11 / docs/17). Unlike List (which is hard-scoped to one
// user), ListAll walks `_notifications` across every user — this is
// the operator's "what's been delivered" view and is gated by
// RequireAdmin at the HTTP layer.
//
// Channel is included for forward-compatibility: today every row in
// `_notifications` is an in-app delivery (other channels are
// side-effects with no per-row audit table yet). The filter is
// honoured against a synthesised constant "inapp" so non-inapp values
// just return zero rows — the UI shape stays stable when v1.6+ adds
// per-channel delivery tracking.
type ListAllFilter struct {
	Kind       string
	Channel    Channel
	UserID     *uuid.UUID
	UnreadOnly bool
	Since      time.Time
	Until      time.Time
	Limit      int
}

// ListAll returns notifications across every user, newest first.
// Admin-only — the user-facing surface uses List (per-user). Limit
// defaults to 50, capped at 500 (matches the per-user List bounds).
func (s *Store) ListAll(ctx context.Context, f ListAllFilter) ([]*Notification, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	// Channel filter: today every row in `_notifications` is an inapp
	// delivery. A request for any other channel returns zero rows
	// without hitting the DB; "inapp" or empty falls through.
	if f.Channel != "" && f.Channel != ChannelInApp {
		return nil, nil
	}
	q := `SELECT id, user_id, tenant_id, kind, title, body, data, priority, read_at, expires_at, created_at
	      FROM _notifications WHERE 1=1`
	args := []any{}
	idx := 1
	if f.Kind != "" {
		q += fmt.Sprintf(" AND kind = $%d", idx)
		args = append(args, f.Kind)
		idx++
	}
	if f.UserID != nil {
		q += fmt.Sprintf(" AND user_id = $%d", idx)
		args = append(args, *f.UserID)
		idx++
	}
	if f.UnreadOnly {
		q += " AND read_at IS NULL"
	}
	if !f.Since.IsZero() {
		q += fmt.Sprintf(" AND created_at >= $%d", idx)
		args = append(args, f.Since)
		idx++
	}
	if !f.Until.IsZero() {
		q += fmt.Sprintf(" AND created_at <= $%d", idx)
		args = append(args, f.Until)
		idx++
	}
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", idx)
	args = append(args, f.Limit)
	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("notifications: list all: %w", err)
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CountAll returns the total row count matching the same filter set
// as ListAll. Used to drive page-based pagination totals on the admin
// surface. Limit is ignored.
func (s *Store) CountAll(ctx context.Context, f ListAllFilter) (int64, error) {
	if f.Channel != "" && f.Channel != ChannelInApp {
		return 0, nil
	}
	q := `SELECT COUNT(*) FROM _notifications WHERE 1=1`
	args := []any{}
	idx := 1
	if f.Kind != "" {
		q += fmt.Sprintf(" AND kind = $%d", idx)
		args = append(args, f.Kind)
		idx++
	}
	if f.UserID != nil {
		q += fmt.Sprintf(" AND user_id = $%d", idx)
		args = append(args, *f.UserID)
		idx++
	}
	if f.UnreadOnly {
		q += " AND read_at IS NULL"
	}
	if !f.Since.IsZero() {
		q += fmt.Sprintf(" AND created_at >= $%d", idx)
		args = append(args, f.Since)
		idx++
	}
	if !f.Until.IsZero() {
		q += fmt.Sprintf(" AND created_at <= $%d", idx)
		args = append(args, f.Until)
		idx++
	}
	var n int64
	if err := s.q.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("notifications: count all: %w", err)
	}
	return n, nil
}

// StatsByKind reports the distribution of notification rows by kind
// across every user. Powers the admin Notifications screen header
// pill row. Returns rows ordered by count DESC.
type KindCount struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

// StatsByKind returns the (kind, count) pairs across `_notifications`.
func (s *Store) StatsByKind(ctx context.Context) ([]KindCount, error) {
	rows, err := s.q.Query(ctx, `
		SELECT kind, COUNT(*) AS n
		FROM _notifications
		GROUP BY kind
		ORDER BY n DESC, kind ASC`)
	if err != nil {
		return nil, fmt.Errorf("notifications: stats by kind: %w", err)
	}
	defer rows.Close()
	var out []KindCount
	for rows.Next() {
		var k KindCount
		if err := rows.Scan(&k.Kind, &k.Count); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// TotalCount returns the total + unread counts across every user.
// Single round-trip (uses FILTER) to keep the stats endpoint cheap.
func (s *Store) TotalCount(ctx context.Context) (total int64, unread int64, err error) {
	err = s.q.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE read_at IS NULL)
		FROM _notifications`).Scan(&total, &unread)
	if err != nil {
		return 0, 0, fmt.Errorf("notifications: total count: %w", err)
	}
	return total, unread, nil
}

// MarkRead transitions a single notification to read. Idempotent.
// Returns false if no row matched user_id + id (so callers can return
// 404 to forged ids).
func (s *Store) MarkRead(ctx context.Context, userID, id uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx, `
		UPDATE _notifications
		SET read_at = COALESCE(read_at, now())
		WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return false, fmt.Errorf("notifications: mark read: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkAllRead transitions every unread row for user → read.
// Returns the number of rows touched.
func (s *Store) MarkAllRead(ctx context.Context, userID uuid.UUID) (int, error) {
	tag, err := s.q.Exec(ctx, `
		UPDATE _notifications
		SET read_at = now()
		WHERE user_id = $1 AND read_at IS NULL`, userID)
	if err != nil {
		return 0, fmt.Errorf("notifications: mark all read: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Delete removes one notification row. Idempotent.
func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx, `
		DELETE FROM _notifications WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return false, fmt.Errorf("notifications: delete: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// UnreadCount is the cheap "show a badge" query.
func (s *Store) UnreadCount(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `
		SELECT COUNT(*) FROM _notifications WHERE user_id = $1 AND read_at IS NULL`,
		userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("notifications: unread count: %w", err)
	}
	return n, nil
}

// --- Preferences CRUD ---

// ListPreferences returns every preference row for one user. Missing
// entries fall back to channel defaults in code (see ChannelEnabled).
func (s *Store) ListPreferences(ctx context.Context, userID uuid.UUID) ([]*Preference, error) {
	rows, err := s.q.Query(ctx, `
		SELECT user_id, kind, channel, enabled, updated_at
		FROM _notification_preferences
		WHERE user_id = $1
		ORDER BY kind, channel`, userID)
	if err != nil {
		return nil, fmt.Errorf("notifications: list prefs: %w", err)
	}
	defer rows.Close()
	var out []*Preference
	for rows.Next() {
		p := &Preference{}
		if err := rows.Scan(&p.UserID, &p.Kind, &p.Channel, &p.Enabled, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteAllPreferences drops every row in `_notification_preferences`
// for a user. Returns the count of deleted rows. Used by the admin
// "reset to defaults" surface (v1.7.38) — after this call,
// ChannelEnabled() falls back to the per-channel default policy
// for any subsequent send.
//
// Idempotent: zero rows is a valid no-op result.
func (s *Store) DeleteAllPreferences(ctx context.Context, userID uuid.UUID) (int, error) {
	tag, err := s.q.Exec(ctx,
		`DELETE FROM _notification_preferences WHERE user_id = $1`,
		userID)
	if err != nil {
		return 0, fmt.Errorf("notifications: delete prefs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// SetPreference upserts one (user, kind, channel) preference.
func (s *Store) SetPreference(ctx context.Context, userID uuid.UUID, kind string, channel Channel, enabled bool) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO _notification_preferences (user_id, kind, channel, enabled, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, kind, channel)
		DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
		userID, kind, channel, enabled)
	if err != nil {
		return fmt.Errorf("notifications: set pref: %w", err)
	}
	return nil
}

// ChannelEnabled reports whether a (user, kind, channel) tuple is
// enabled. Falls back to channel default when the preference row is
// absent. The default policy: inapp + email default ENABLED;
// push defaults ENABLED but is silently no-op without plugin.
func (s *Store) ChannelEnabled(ctx context.Context, userID uuid.UUID, kind string, channel Channel) (bool, error) {
	var enabled bool
	err := s.q.QueryRow(ctx, `
		SELECT enabled FROM _notification_preferences
		WHERE user_id = $1 AND kind = $2 AND channel = $3`,
		userID, kind, channel).Scan(&enabled)
	if err == nil {
		return enabled, nil
	}
	// Row absent → default. We treat pgx.ErrNoRows (any error) as
	// "use default". The error variable type is intentionally not
	// imported to keep the package zero-dep on pgx semantics.
	return channelDefault(channel), nil
}

func channelDefault(c Channel) bool {
	switch c {
	case ChannelInApp, ChannelEmail, ChannelPush:
		return true
	default:
		return false
	}
}

// --- Service: high-level Send ---

// Mailer is the minimal interface we need from internal/mailer. We
// take it as an interface so notifications doesn't import the
// concrete package — keeps the dep graph clean and tests pluggable.
type Mailer interface {
	SendTemplate(ctx context.Context, to string, template string, data map[string]any) error
}

// LookupEmail resolves a user's email address. Operators supply a
// closure that reads from their users-collection. nil = email channel
// is skipped (logs a warning).
type LookupEmail func(ctx context.Context, userID uuid.UUID) (string, error)

// Service is the high-level facade. Wire one per process.
type Service struct {
	Store      *Store
	Bus        *eventbus.Bus
	Mailer     Mailer
	GetEmail   LookupEmail
	Log        *slog.Logger
}

// Send delivers a notification to one user across all applicable
// channels. The in-app channel is the source-of-truth row in
// _notifications; other channels are best-effort side-effects
// (failures logged, not surfaced to the caller). Returns the row id.
//
// v1.7.34 quiet-hours / digest gating: AFTER in-app insert, before
// emitting any email/push side-effect, the per-user UserSettings row
// is consulted (see quiet_digest.go). If quiet hours are active the
// side-effect is deferred to a row in `_notification_deferred`; if a
// digest mode is set and the notification isn't `urgent`, the side-
// effect is deferred to the next digest cycle. The in-app row is
// ALWAYS persisted + published — the bell view stays consistent.
func (s *Service) Send(ctx context.Context, in SendInput) (uuid.UUID, error) {
	return s.sendInternal(ctx, in, false)
}

// sendInternal is the engine behind Send. bypassDeferral=true means
// the call is replaying a previously-deferred quiet-hours row — we
// skip the gating step (otherwise we'd loop the same row through
// indefinitely if the user's window happens to be misconfigured).
func (s *Service) sendInternal(ctx context.Context, in SendInput, bypassDeferral bool) (uuid.UUID, error) {
	if in.UserID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("notifications: user_id required")
	}
	if in.Kind == "" {
		return uuid.Nil, fmt.Errorf("notifications: kind required")
	}
	if in.Title == "" {
		return uuid.Nil, fmt.Errorf("notifications: title required")
	}
	n := &Notification{
		UserID:    in.UserID,
		TenantID:  in.TenantID,
		Kind:      in.Kind,
		Title:     in.Title,
		Body:      in.Body,
		Data:      in.Data,
		Priority:  in.Priority,
		ExpiresAt: in.ExpiresAt,
	}

	// Resolve channels. Empty = all defaults; non-nil = use as-is
	// but still filter by preference (operator hint vs. user opt-out).
	channels := in.Channels
	if len(channels) == 0 {
		channels = []Channel{ChannelInApp, ChannelEmail, ChannelPush}
	}
	enabled := make(map[Channel]bool, len(channels))
	for _, c := range channels {
		on, err := s.Store.ChannelEnabled(ctx, in.UserID, in.Kind, c)
		if err != nil {
			return uuid.Nil, err
		}
		enabled[c] = on
	}

	// In-app channel = always persist the row IF inapp is enabled.
	// (When a user opts out of in-app for one kind they really mean
	// "don't show me this in the bell list" — we honour by skipping
	// the row entirely. Operators wanting "send anyway, just don't
	// publish" can call Store.Insert directly.)
	if enabled[ChannelInApp] {
		if err := s.Store.Insert(ctx, n); err != nil {
			return uuid.Nil, err
		}
		// Publish realtime so an online UI sees it immediately.
		if s.Bus != nil {
			s.Bus.Publish(eventbus.Event{
				Topic:   TopicNotification,
				Payload: *n,
			})
		}
	}

	// Email channel — best-effort side-effect.
	//
	// v1.7.34: gate on quiet hours + digest. Both paths require an
	// in-app row to point at (the deferred row FKs _notifications.id),
	// so we only enter the gating branch when an in-app row was actually
	// inserted above. Without an in-app row there's nothing to defer.
	wantEmail := enabled[ChannelEmail] && s.Mailer != nil && s.GetEmail != nil
	if wantEmail && n.ID != uuid.Nil && !bypassDeferral {
		us, err := s.Store.GetUserSettings(ctx, in.UserID)
		if err != nil {
			if s.Log != nil {
				s.Log.Warn("notifications: load user settings failed", "user", in.UserID, "err", err)
			}
		} else {
			now := time.Now().UTC()
			// Quiet hours wins over digest (see quiet_digest.go docs).
			within, endOfWindow := withinQuietHours(now, us.QuietHoursTZ, us.QuietHoursStart, us.QuietHoursEnd)
			if within {
				deferErr := s.Store.InsertDeferred(ctx, &DeferredRow{
					UserID:         in.UserID,
					NotificationID: n.ID,
					FlushAfter:     endOfWindow.UTC(),
					Reason:         "quiet_hours",
				})
				if deferErr != nil && s.Log != nil {
					s.Log.Warn("notifications: defer (quiet_hours) failed", "user", in.UserID, "err", deferErr)
				}
				wantEmail = false
			} else if us.DigestMode != "off" && us.DigestMode != "" && in.Priority != PriorityUrgent {
				tz := us.DigestTZ
				if tz == "" {
					tz = us.QuietHoursTZ
				}
				next := nextDigestTime(now, us.DigestMode, us.DigestHour, us.DigestDOW, tz)
				if !next.IsZero() {
					deferErr := s.Store.InsertDeferred(ctx, &DeferredRow{
						UserID:         in.UserID,
						NotificationID: n.ID,
						FlushAfter:     next.UTC(),
						Reason:         "digest",
					})
					if deferErr != nil && s.Log != nil {
						s.Log.Warn("notifications: defer (digest) failed", "user", in.UserID, "err", deferErr)
					}
					wantEmail = false
				}
			}
		}
	}

	if wantEmail {
		email, err := s.GetEmail(ctx, in.UserID)
		switch {
		case err != nil:
			if s.Log != nil {
				s.Log.Warn("notifications: lookup email failed", "user", in.UserID, "err", err)
			}
		case email == "":
			// User has no email on file — silently skip.
		default:
			tmpl := "notification_" + in.Kind // operators add their own .md
			data := map[string]any{
				"title":  in.Title,
				"body":   in.Body,
				"data":   in.Data,
				"kind":   in.Kind,
				"userId": in.UserID.String(),
			}
			if err := s.Mailer.SendTemplate(ctx, email, tmpl, data); err != nil {
				if s.Log != nil {
					s.Log.Warn("notifications: email send failed", "user", in.UserID, "kind", in.Kind, "err", err)
				}
			}
		}
	}

	// Push channel: deferred to plugin. No-op here.

	return n.ID, nil
}

// --- helpers ---

func scanNotification(row interface {
	Scan(dest ...any) error
}) (*Notification, error) {
	n := &Notification{}
	var dataJSON []byte
	if err := row.Scan(&n.ID, &n.UserID, &n.TenantID, &n.Kind, &n.Title, &n.Body, &dataJSON, &n.Priority, &n.ReadAt, &n.ExpiresAt, &n.CreatedAt); err != nil {
		return nil, err
	}
	if len(dataJSON) > 0 && string(dataJSON) != "{}" && string(dataJSON) != "null" {
		if err := json.Unmarshal(dataJSON, &n.Data); err != nil {
			return nil, fmt.Errorf("notifications: unmarshal data: %w", err)
		}
	}
	return n, nil
}

func or[T any](a, b T) T {
	if any(a) == nil {
		return b
	}
	return a
}
