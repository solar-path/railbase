// Package webhooks ships outbound HTTP webhooks (§3.9.2 / docs/21).
//
// Architecture in one breath:
//
//   - The REST layer publishes RecordEvent on the eventbus on every
//     create/update/delete.
//   - Dispatcher subscribes to that bus, fans out to every active
//     webhook whose `events` list matches, and enqueues one job per
//     match (kind="webhook_deliver").
//   - The job handler (DeliveryHandler) does the HTTP POST with HMAC
//     signing, records the attempt in _webhook_deliveries, retries on
//     transient failure via the existing jobs framework's exp-backoff.
//
// Why ride on the jobs queue instead of a dedicated worker pool:
//   - We already have exp-backoff + retry budget + lock-expired sweep.
//   - One uniform "where did my background work go?" admin surface.
//   - Crashes don't drop deliveries: pending rows survive restart.
//
// What's deliberately NOT in this milestone:
//   - JS hooks bindings ($webhooks.dispatch, onWebhookBefore/After).
//     Wait for §3.4 hooks epic to extend its $api surface.
//   - Per-payload filter expressions ("amount > 100"). Add in v1.5.1.
//   - Per-tenant separation. v1.5.1 — needs tenant.WithSiteScope reasoning.
//   - Admin UI screens. §3.11 epic.
//   - Rate limiting / per-host concurrency. v1.5.x once we have data.
package webhooks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the minimal pgx surface the store depends on. Both
// *pgxpool.Pool and pgx.Tx satisfy it, so callers can run inside a
// transaction when they need atomicity (rare — typical flow is pool).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Webhook is one configured outbound destination.
type Webhook struct {
	ID          uuid.UUID         `json:"id"`
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	SecretB64   string            `json:"-"` // never serialised
	Events      []string          `json:"events"`
	Active      bool              `json:"active"`
	MaxAttempts int               `json:"max_attempts"`
	TimeoutMS   int               `json:"timeout_ms"`
	Headers     map[string]string `json:"headers,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Delivery captures one attempt to ship one event to one webhook.
type Delivery struct {
	ID            uuid.UUID  `json:"id"`
	WebhookID     uuid.UUID  `json:"webhook_id"`
	Event         string     `json:"event"`
	Payload       []byte     `json:"-"` // returned as json.RawMessage by API
	Attempt       int        `json:"attempt"`
	SupersededBy  *uuid.UUID `json:"superseded_by,omitempty"`
	Status        string     `json:"status"` // pending|success|retry|dead
	ResponseCode  *int       `json:"response_code,omitempty"`
	ResponseBody  string     `json:"response_body,omitempty"`
	ErrorMsg      string     `json:"error_msg,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// Store is the persistent storage layer for webhooks + deliveries.
type Store struct {
	q Querier
}

// NewStore wraps a Querier. Hold the pool for process lifetime.
func NewStore(q Querier) *Store { return &Store{q: q} }

// --- _webhooks CRUD ---

// CreateInput carries the fields a Create call accepts. Optional fields
// fall back to defaults: Active=true, MaxAttempts=5, TimeoutMS=30000,
// secret auto-generated when SecretB64 is empty.
type CreateInput struct {
	Name        string
	URL         string
	SecretB64   string
	Events      []string
	Active      *bool
	MaxAttempts int
	TimeoutMS   int
	Headers     map[string]string
}

// Create inserts a new webhook. Returns the row including a freshly
// generated secret if the caller didn't provide one.
func (s *Store) Create(ctx context.Context, in CreateInput) (*Webhook, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("webhooks: name required")
	}
	if in.URL == "" {
		return nil, fmt.Errorf("webhooks: url required")
	}
	if in.SecretB64 == "" {
		s, err := GenerateSecret(32)
		if err != nil {
			return nil, fmt.Errorf("webhooks: generate secret: %w", err)
		}
		in.SecretB64 = s
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 5
	}
	if in.TimeoutMS == 0 {
		in.TimeoutMS = 30000
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}

	eventsJSON, err := json.Marshal(in.Events)
	if err != nil {
		return nil, fmt.Errorf("webhooks: marshal events: %w", err)
	}
	headersJSON, err := json.Marshal(or(in.Headers, map[string]string{}))
	if err != nil {
		return nil, fmt.Errorf("webhooks: marshal headers: %w", err)
	}

	w := &Webhook{
		ID:          uuid.Must(uuid.NewV7()),
		Name:        in.Name,
		URL:         in.URL,
		SecretB64:   in.SecretB64,
		Events:      in.Events,
		Active:      active,
		MaxAttempts: in.MaxAttempts,
		TimeoutMS:   in.TimeoutMS,
		Headers:     in.Headers,
	}
	err = s.q.QueryRow(ctx, `
		INSERT INTO _webhooks (id, name, url, secret_b64, events, active, max_attempts, timeout_ms, headers)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at, updated_at`,
		w.ID, w.Name, w.URL, w.SecretB64, eventsJSON, w.Active, w.MaxAttempts, w.TimeoutMS, headersJSON,
	).Scan(&w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("webhooks: insert: %w", err)
	}
	return w, nil
}

// GetByID loads one webhook by uuid.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*Webhook, error) {
	row := s.q.QueryRow(ctx, `SELECT id, name, url, secret_b64, events, active, max_attempts, timeout_ms, headers, created_at, updated_at FROM _webhooks WHERE id = $1`, id)
	return scanWebhook(row)
}

// GetByName resolves the human handle to a webhook row. CLI uses this.
func (s *Store) GetByName(ctx context.Context, name string) (*Webhook, error) {
	row := s.q.QueryRow(ctx, `SELECT id, name, url, secret_b64, events, active, max_attempts, timeout_ms, headers, created_at, updated_at FROM _webhooks WHERE name = $1`, name)
	return scanWebhook(row)
}

// List returns all webhooks, newest first.
func (s *Store) List(ctx context.Context) ([]*Webhook, error) {
	rows, err := s.q.Query(ctx, `SELECT id, name, url, secret_b64, events, active, max_attempts, timeout_ms, headers, created_at, updated_at FROM _webhooks ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("webhooks: list: %w", err)
	}
	defer rows.Close()
	var out []*Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListActiveMatching returns the active webhooks whose `events` array
// matches `topic`. Topic strings look like "record.created.posts". The
// matcher supports the literal pattern + one '*' wildcard per dotted
// segment: "record.*.posts" matches any verb on the posts collection;
// "record.*.*" matches every record event.
//
// This runs on the hot path (every record mutation), so we keep it
// allocation-light: parse-once-per-call, no regex.
func (s *Store) ListActiveMatching(ctx context.Context, topic string) ([]*Webhook, error) {
	all, err := s.listActive(ctx)
	if err != nil {
		return nil, err
	}
	var matched []*Webhook
	for _, w := range all {
		for _, p := range w.Events {
			if matchTopic(p, topic) {
				matched = append(matched, w)
				break
			}
		}
	}
	return matched, nil
}

func (s *Store) listActive(ctx context.Context) ([]*Webhook, error) {
	rows, err := s.q.Query(ctx, `SELECT id, name, url, secret_b64, events, active, max_attempts, timeout_ms, headers, created_at, updated_at FROM _webhooks WHERE active = TRUE`)
	if err != nil {
		return nil, fmt.Errorf("webhooks: list active: %w", err)
	}
	defer rows.Close()
	var out []*Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Delete removes the webhook + cascades deliveries. Returns false if
// no row matched.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM _webhooks WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("webhooks: delete: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetActive toggles a webhook on or off without deleting config.
func (s *Store) SetActive(ctx context.Context, id uuid.UUID, active bool) error {
	_, err := s.q.Exec(ctx, `UPDATE _webhooks SET active = $1, updated_at = now() WHERE id = $2`, active, id)
	if err != nil {
		return fmt.Errorf("webhooks: set active: %w", err)
	}
	return nil
}

// --- _webhook_deliveries ---

// InsertDelivery records a delivery row in `pending` status. The
// dispatcher inserts this BEFORE enqueuing the job so the admin UI
// always sees the attempt even if the job worker hasn't picked it up
// yet.
func (s *Store) InsertDelivery(ctx context.Context, webhookID uuid.UUID, event string, payload []byte, attempt int) (*Delivery, error) {
	d := &Delivery{
		ID:        uuid.Must(uuid.NewV7()),
		WebhookID: webhookID,
		Event:     event,
		Payload:   payload,
		Attempt:   attempt,
		Status:    "pending",
	}
	err := s.q.QueryRow(ctx, `
		INSERT INTO _webhook_deliveries (id, webhook_id, event, payload, attempt, status)
		VALUES ($1, $2, $3, $4, $5, 'pending')
		RETURNING created_at`,
		d.ID, d.WebhookID, d.Event, d.Payload, d.Attempt,
	).Scan(&d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("webhooks: insert delivery: %w", err)
	}
	return d, nil
}

// CompleteDelivery transitions a row from pending to success/retry/dead.
// Body is truncated to 1KB to bound storage.
func (s *Store) CompleteDelivery(ctx context.Context, id uuid.UUID, status string, code int, body string, errorMsg string) error {
	if len(body) > 1024 {
		body = body[:1024]
	}
	var codePtr *int
	if code != 0 {
		codePtr = &code
	}
	_, err := s.q.Exec(ctx, `
		UPDATE _webhook_deliveries
		SET status = $1, response_code = $2, response_body = $3, error_msg = $4, completed_at = now()
		WHERE id = $5`,
		status, codePtr, body, errorMsg, id,
	)
	if err != nil {
		return fmt.Errorf("webhooks: complete delivery: %w", err)
	}
	return nil
}

// ListDeliveries returns the most recent attempts for one webhook,
// optionally filtered by status. Limit caps result rows.
func (s *Store) ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit int) ([]*Delivery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.q.Query(ctx, `
		SELECT id, webhook_id, event, payload, attempt, superseded_by, status, response_code, COALESCE(response_body, ''), COALESCE(error_msg, ''), created_at, completed_at
		FROM _webhook_deliveries
		WHERE webhook_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, webhookID, limit)
	if err != nil {
		return nil, fmt.Errorf("webhooks: list deliveries: %w", err)
	}
	defer rows.Close()
	var out []*Delivery
	for rows.Next() {
		d := &Delivery{}
		var code *int
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.Event, &d.Payload, &d.Attempt, &d.SupersededBy, &d.Status, &code, &d.ResponseBody, &d.ErrorMsg, &d.CreatedAt, &d.CompletedAt); err != nil {
			return nil, err
		}
		d.ResponseCode = code
		out = append(out, d)
	}
	return out, rows.Err()
}

// --- helpers ---

// scanWebhook accepts both pgx.Row and pgx.Rows (each has Scan).
func scanWebhook(row interface {
	Scan(dest ...any) error
}) (*Webhook, error) {
	w := &Webhook{}
	var eventsJSON, headersJSON []byte
	if err := row.Scan(&w.ID, &w.Name, &w.URL, &w.SecretB64, &eventsJSON, &w.Active, &w.MaxAttempts, &w.TimeoutMS, &headersJSON, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	if len(eventsJSON) > 0 {
		if err := json.Unmarshal(eventsJSON, &w.Events); err != nil {
			return nil, fmt.Errorf("webhooks: unmarshal events: %w", err)
		}
	}
	if len(headersJSON) > 0 && string(headersJSON) != "{}" && string(headersJSON) != "null" {
		if err := json.Unmarshal(headersJSON, &w.Headers); err != nil {
			return nil, fmt.Errorf("webhooks: unmarshal headers: %w", err)
		}
	}
	return w, nil
}

// matchTopic returns true when pattern matches topic. Both are dotted
// strings; '*' in pattern matches one whole segment. Examples:
//
//	matchTopic("record.created.posts", "record.created.posts") = true
//	matchTopic("record.*.posts",       "record.updated.posts") = true
//	matchTopic("record.*.*",           "record.deleted.tags")   = true
//	matchTopic("record.*.posts",       "record.updated.tags")   = false
func matchTopic(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	p := strings.Split(pattern, ".")
	t := strings.Split(topic, ".")
	if len(p) != len(t) {
		return false
	}
	for i := range p {
		if p[i] == "*" {
			continue
		}
		if p[i] != t[i] {
			return false
		}
	}
	return true
}

func or[T any](a, b T) T {
	if any(a) == nil {
		return b
	}
	return a
}

// DecodeSecret returns the raw HMAC key from the base64 stored form.
func DecodeSecret(b64 string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(b64)
}
