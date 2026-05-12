package mailer

// EventStore is the persistence layer behind the `_email_events` table
// (§3.1.4 — sent/failed in core; bounced/opened/clicked/complained from
// plugins). It's a thin DB shim — the publish-on-eventbus path
// (events.go) and this persistent shadow ride side by side:
//
//   * Eventbus subscribers are in-process observers (telemetry, JS
//     hooks). They run synchronously w.r.t. SendDirect and disappear
//     on process restart.
//
//   * EventStore is the durable record. Survives restart, queryable
//     from CLI / admin / SQL, scoped to the same per-recipient grain
//     so bounce-parser plugins can INSERT into the same table
//     without schema gymnastics.
//
// The two paths are intentionally independent: a hook can mutate the
// outbound message (before_send) without the EventStore even knowing,
// and the EventStore writes regardless of whether anyone subscribed.
// That keeps the "did the email leave?" answer in ONE place — the
// database — instead of forcing operators to also wire a subscriber
// just to get persistence.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the minimal pgx surface EventStore depends on. Both
// *pgxpool.Pool and pgx.Tx satisfy it. We define our own (rather than
// importing internal/jobs.Querier) so the mailer package stays free of
// cross-package imports beyond eventbus — keeps the dep arrow clean.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// EmailEvent is one row in _email_events. The struct is also the
// list-API return shape — no separate DTO since the writer columns and
// the reader columns are the same.
//
// Recipient is a single address: callers fan To/CC/BCC into N
// EmailEvent values, one per recipient. That matches the operator
// question "did alice@ get her reset email?" — Alice's row stands on
// its own without inner-joining anything.
type EmailEvent struct {
	ID           uuid.UUID      `json:"id"`
	OccurredAt   time.Time      `json:"occurred_at"`
	Event        string         `json:"event"`        // sent|failed|bounced|opened|clicked|complained
	Driver       string         `json:"driver"`       // smtp|console|ses|...
	MessageID    string         `json:"message_id,omitempty"`
	Recipient    string         `json:"recipient"`
	Subject      string         `json:"subject,omitempty"`
	Template     string         `json:"template,omitempty"`     // empty for SendDirect
	BounceType   string         `json:"bounce_type,omitempty"`  // plugin-populated
	ErrorCode    string         `json:"error_code,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// EventStore writes mailer event rows. Goroutine-safe: every method
// runs one statement against the underlying Querier, which handles
// concurrency internally for both *pgxpool.Pool and pgx.Tx.
type EventStore struct {
	q Querier
}

// NewEventStore wraps a Querier. Hold the pool for process lifetime —
// EventStore carries no state of its own.
func NewEventStore(q Querier) *EventStore { return &EventStore{q: q} }

// Write inserts one event row. Caller is responsible for fanning
// multi-recipient sends into one Write call per recipient — see
// mailer.go's after-Send loop for the canonical pattern.
//
// Validation is light because the table's CHECK constraints catch
// bad enum values. We only enforce the obvious required fields
// (Event, Driver, Recipient) so a misuse fails fast with a clear
// caller-side error instead of waiting for a PG round-trip.
func (s *EventStore) Write(ctx context.Context, ev EmailEvent) error {
	if ev.Event == "" {
		return fmt.Errorf("mailer/eventstore: Event required")
	}
	if ev.Driver == "" {
		return fmt.Errorf("mailer/eventstore: Driver required")
	}
	if ev.Recipient == "" {
		return fmt.Errorf("mailer/eventstore: Recipient required")
	}

	var metaJSON []byte
	if len(ev.Metadata) > 0 {
		b, err := json.Marshal(ev.Metadata)
		if err != nil {
			return fmt.Errorf("mailer/eventstore: marshal metadata: %w", err)
		}
		metaJSON = b
	}

	// Coerce empty optional strings into SQL NULL so the operator-facing
	// query `WHERE message_id IS NULL` works as expected. Without this,
	// empty strings would land as zero-length TEXT and the NULL check
	// would silently miss them.
	_, err := s.q.Exec(ctx, `
		INSERT INTO _email_events
		    (event, driver, message_id, recipient, subject, template,
		     bounce_type, error_code, error_message, metadata)
		VALUES
		    ($1, $2, NULLIF($3,''), $4, NULLIF($5,''), NULLIF($6,''),
		     NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), $10)`,
		ev.Event, ev.Driver, ev.MessageID, ev.Recipient, ev.Subject, ev.Template,
		ev.BounceType, ev.ErrorCode, ev.ErrorMessage, metaJSON,
	)
	if err != nil {
		return fmt.Errorf("mailer/eventstore: insert: %w", err)
	}
	return nil
}

// ListRecent returns events newest-first, capped at limit. limit<=0 or
// >1000 collapses to 100 — bounds match other Stores in the codebase
// (webhooks.ListDeliveries) for operator-muscle-memory consistency.
func (s *EventStore) ListRecent(ctx context.Context, limit int) ([]EmailEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.q.Query(ctx, `
		SELECT id, occurred_at, event, driver,
		       COALESCE(message_id, ''), recipient,
		       COALESCE(subject, ''), COALESCE(template, ''),
		       COALESCE(bounce_type, ''), COALESCE(error_code, ''),
		       COALESCE(error_message, ''), metadata
		FROM _email_events
		ORDER BY occurred_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("mailer/eventstore: list recent: %w", err)
	}
	defer rows.Close()
	return scanEmailEvents(rows)
}

// ListByRecipient returns the event history for a single address,
// newest-first. Operator workflow: "alice complained that her invite
// email never arrived" → drill in on alice@.
func (s *EventStore) ListByRecipient(ctx context.Context, recipient string, limit int) ([]EmailEvent, error) {
	if recipient == "" {
		return nil, fmt.Errorf("mailer/eventstore: recipient required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.q.Query(ctx, `
		SELECT id, occurred_at, event, driver,
		       COALESCE(message_id, ''), recipient,
		       COALESCE(subject, ''), COALESCE(template, ''),
		       COALESCE(bounce_type, ''), COALESCE(error_code, ''),
		       COALESCE(error_message, ''), metadata
		FROM _email_events
		WHERE recipient = $1
		ORDER BY occurred_at DESC
		LIMIT $2`, recipient, limit)
	if err != nil {
		return nil, fmt.Errorf("mailer/eventstore: list by recipient: %w", err)
	}
	defer rows.Close()
	return scanEmailEvents(rows)
}

// ListFilter is the admin-browser-shaped predicate set on List / Count.
// Every field is optional; the zero value lists everything newest-first.
//
// Recipient is a case-insensitive substring match (so the admin filter
// bar handles "alice" → matches "alice@example.com"); every other text
// filter is an exact match because they enumerate over small CHECK-
// constrained domains where substring search would just be noise.
type ListFilter struct {
	Recipient  string    // ILIKE %s%
	Event      string    // exact
	Template   string    // exact
	BounceType string    // exact
	Since      time.Time // occurred_at >= since
	Until      time.Time // occurred_at <= until
	Limit      int       // default 100, max 1000; >1000 → 1000
	Offset     int       // 0-indexed offset for page-based pagination
}

// List returns rows matching f, sorted occurred_at DESC. Page-based
// pagination is via Offset+Limit so the admin endpoint can compose
// page/perPage on top.
func (s *EventStore) List(ctx context.Context, f ListFilter) ([]EmailEvent, error) {
	clauses, args := f.buildWhere()
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 1000 {
		f.Limit = 1000
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	args = append(args, f.Limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, occurred_at, event, driver,
		       COALESCE(message_id, ''), recipient,
		       COALESCE(subject, ''), COALESCE(template, ''),
		       COALESCE(bounce_type, ''), COALESCE(error_code, ''),
		       COALESCE(error_message, ''), metadata
		FROM _email_events%s
		ORDER BY occurred_at DESC
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))
	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("mailer/eventstore: list: %w", err)
	}
	defer rows.Close()
	return scanEmailEvents(rows)
}

// Count returns the total row count matching f. Limit / Offset are
// ignored — the admin endpoint uses Count to render "X of Y" headers
// independent of the current page window.
func (s *EventStore) Count(ctx context.Context, f ListFilter) (int64, error) {
	clauses, args := f.buildWhere()
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	q := "SELECT count(*) FROM _email_events" + where
	var n int64
	if err := s.q.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("mailer/eventstore: count: %w", err)
	}
	return n, nil
}

// buildWhere converts f into a pair of parallel slices: the SQL clauses
// (already $-substituted in declaration order) and the args slice.
// Shared by List + Count so a divergence between the two queries is
// structurally impossible.
func (f ListFilter) buildWhere() ([]string, []any) {
	var (
		clauses []string
		args    []any
	)
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Recipient != "" {
		clauses = append(clauses, "recipient ILIKE "+addArg("%"+f.Recipient+"%"))
	}
	if f.Event != "" {
		clauses = append(clauses, "event = "+addArg(f.Event))
	}
	if f.Template != "" {
		clauses = append(clauses, "template = "+addArg(f.Template))
	}
	if f.BounceType != "" {
		clauses = append(clauses, "bounce_type = "+addArg(f.BounceType))
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "occurred_at >= "+addArg(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "occurred_at <= "+addArg(f.Until))
	}
	return clauses, args
}

// scanEmailEvents materialises pgx.Rows into the result slice. Shared
// by both list methods — the column list is identical.
func scanEmailEvents(rows pgx.Rows) ([]EmailEvent, error) {
	var out []EmailEvent
	for rows.Next() {
		var ev EmailEvent
		var metaJSON []byte
		if err := rows.Scan(
			&ev.ID, &ev.OccurredAt, &ev.Event, &ev.Driver,
			&ev.MessageID, &ev.Recipient, &ev.Subject, &ev.Template,
			&ev.BounceType, &ev.ErrorCode, &ev.ErrorMessage, &metaJSON,
		); err != nil {
			return nil, fmt.Errorf("mailer/eventstore: scan: %w", err)
		}
		if len(metaJSON) > 0 && string(metaJSON) != "null" {
			if err := json.Unmarshal(metaJSON, &ev.Metadata); err != nil {
				return nil, fmt.Errorf("mailer/eventstore: unmarshal metadata: %w", err)
			}
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// recordSendOutcome fans a Message into per-recipient EventStore writes
// after the driver returns. Called from SendDirect when m.events is
// non-nil; nil events keeps zero-cost behaviour. Errors from the
// EventStore are logged but never surfaced to the caller — losing an
// audit row is operator-visible (slog) but shouldn't fail an already-
// successful send. The SQL is a single INSERT per recipient; a typical
// invite/reset fans to 1 address, so the cost is negligible.
func (m *Mailer) recordSendOutcome(ctx context.Context, msg Message, sendErr error, template string) {
	if m.events == nil {
		return
	}
	event := "sent"
	var errCode, errMsg string
	if sendErr != nil {
		event = "failed"
		errMsg = sendErr.Error()
	}
	messageID := ""
	if msg.Headers != nil {
		messageID = msg.Headers["Message-ID"]
	}
	driverName := m.driver.Name()
	for _, addr := range flattenRecipients(msg) {
		ev := EmailEvent{
			Event:        event,
			Driver:       driverName,
			MessageID:    messageID,
			Recipient:    addr,
			Subject:      msg.Subject,
			Template:     template,
			ErrorCode:    errCode,
			ErrorMessage: errMsg,
		}
		if err := m.events.Write(ctx, ev); err != nil {
			m.log.Warn("mailer: event store write failed",
				"recipient", addr, "event", event, "err", err)
		}
	}
}
