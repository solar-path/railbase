package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CronRow is the in-memory shape of a `_cron` schedule.
type CronRow struct {
	ID         uuid.UUID
	Name       string
	Expression string
	Kind       string
	Payload    []byte
	Enabled    bool
	LastRunAt  *time.Time
	NextRunAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CronStore is the persistence layer for cron schedules. Lives on
// the same Querier as Store but is split out so package consumers
// can wire only one or the other.
type CronStore struct {
	q Querier
}

// NewCronStore constructs a CronStore.
func NewCronStore(q Querier) *CronStore { return &CronStore{q: q} }

// Upsert creates or updates a schedule by name. Returns the row.
// The Schedule expression is validated up front — invalid expressions
// reject before touching the DB.
func (s *CronStore) Upsert(ctx context.Context, name, expr, kind string, payload any) (*CronRow, error) {
	sch, err := ParseCron(expr)
	if err != nil {
		return nil, err
	}
	body, err := encodePayload(payload)
	if err != nil {
		return nil, fmt.Errorf("cron: marshal payload: %w", err)
	}
	next := sch.Next(time.Now().UTC())
	row := s.q.QueryRow(ctx, `
		INSERT INTO _cron (id, name, expression, kind, payload, enabled, next_run_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, TRUE, $5)
		ON CONFLICT (name) DO UPDATE SET
		    expression = EXCLUDED.expression,
		    kind       = EXCLUDED.kind,
		    payload    = EXCLUDED.payload,
		    next_run_at = EXCLUDED.next_run_at,
		    updated_at = now()
		RETURNING id, name, expression, kind, payload, enabled,
		          last_run_at, next_run_at, created_at, updated_at`,
		name, expr, kind, body, next,
	)
	return scanCronRow(row)
}

// List returns all cron rows. Caller-paginated; admin UI keeps the
// total to a few hundred — no LIMIT here.
func (s *CronStore) List(ctx context.Context) ([]*CronRow, error) {
	rows, err := s.q.Query(ctx, `
		SELECT id, name, expression, kind, payload, enabled,
		       last_run_at, next_run_at, created_at, updated_at
		FROM _cron ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CronRow{}
	for rows.Next() {
		r, err := scanCronRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes a schedule by name. Idempotent.
func (s *CronStore) Delete(ctx context.Context, name string) error {
	_, err := s.q.Exec(ctx, `DELETE FROM _cron WHERE name = $1`, name)
	return err
}

// RunNow materialises a job for the named schedule immediately without
// advancing next_run_at — operator action for "trigger this once
// right now, but the next scheduled run still happens on time."
// Returns (jobID, true, nil) on success; (uuid.Nil, false, nil) when
// the schedule is missing or disabled.
func (s *CronStore) RunNow(ctx context.Context, name string) (uuid.UUID, bool, error) {
	row := s.q.QueryRow(ctx, `
		WITH src AS (
		    SELECT id, kind, payload FROM _cron
		    WHERE name = $1 AND enabled = TRUE
		)
		INSERT INTO _jobs (id, queue, kind, payload, max_attempts, run_after, cron_id)
		SELECT gen_random_uuid(), 'default', src.kind, src.payload, 5, now(), src.id
		FROM src
		RETURNING id`, name)
	var jobID uuid.UUID
	if err := row.Scan(&jobID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return jobID, true, nil
}

// Get returns the schedule by name. ErrNotFound when missing.
func (s *CronStore) Get(ctx context.Context, name string) (*CronRow, error) {
	row := s.q.QueryRow(ctx, `
		SELECT id, name, expression, kind, payload, enabled,
		       last_run_at, next_run_at, created_at, updated_at
		FROM _cron WHERE name = $1`, name)
	return scanCronRow(row)
}

// SetEnabled toggles a schedule. Pauses next_run_at advancement so
// re-enabling doesn't immediately flood-fire the missed slots.
func (s *CronStore) SetEnabled(ctx context.Context, name string, enabled bool) error {
	_, err := s.q.Exec(ctx,
		`UPDATE _cron SET enabled = $1, updated_at = now() WHERE name = $2`,
		enabled, name)
	return err
}

// MaterialiseDue finds all enabled schedules whose next_run_at <= now,
// inserts one `_jobs` row each (kind/payload from the schedule), and
// advances next_run_at. Returns the count materialised.
//
// Skips schedules whose computed Next falls in the past (e.g. a
// schedule disabled for hours and re-enabled) by advancing past now
// without backfilling — operators wanting backfill add their own
// "catch-up" job kind.
func (c *Cron) MaterialiseDue(ctx context.Context) (int, error) {
	rows, err := c.store.q.Query(ctx, `
		SELECT id, name, expression, kind, payload, enabled,
		       last_run_at, next_run_at, created_at, updated_at
		FROM _cron
		WHERE enabled = TRUE AND next_run_at IS NOT NULL AND next_run_at <= now()
		FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var due []*CronRow
	for rows.Next() {
		r, err := scanCronRow(rows)
		if err != nil {
			return 0, err
		}
		due = append(due, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	now := time.Now().UTC()
	for _, r := range due {
		sch, perr := ParseCron(r.Expression)
		if perr != nil {
			c.log.Warn("cron: bad expression, skipping", "name", r.Name, "expr", r.Expression, "err", perr)
			continue
		}
		// Materialise one job row stamped with cron_id.
		body := r.Payload
		if len(body) == 0 {
			body = []byte("{}")
		}
		if _, err := c.store.q.Exec(ctx, `
			INSERT INTO _jobs (id, queue, kind, payload, max_attempts, run_after, cron_id)
			VALUES (gen_random_uuid(), 'default', $1, $2, 5, now(), $3)`,
			r.Kind, body, r.ID); err != nil {
			c.log.Warn("cron: enqueue from schedule failed", "name", r.Name, "err", err)
			continue
		}
		next := sch.Next(now)
		if _, err := c.store.q.Exec(ctx, `
			UPDATE _cron
			   SET last_run_at = now(),
			       next_run_at = $1,
			       updated_at = now()
			 WHERE id = $2`, next, r.ID); err != nil {
			c.log.Warn("cron: advance next_run_at failed", "name", r.Name, "err", err)
			continue
		}
		count++
	}
	return count, nil
}

// Cron is the scheduler loop. One per process.
type Cron struct {
	store    *CronStore
	jobStore *Store // optional — when set, periodically Recover()'s stuck jobs
	log      *slog.Logger
	tick     time.Duration
}

// NewCron constructs a Cron scheduler.
func NewCron(store *CronStore, log *slog.Logger) *Cron {
	if log == nil {
		log = slog.Default()
	}
	return &Cron{store: store, log: log, tick: 15 * time.Second}
}

// WithRecover wires the jobs Store into the scheduler so each tick
// also sweeps stuck running rows back to pending. Nil-safe — call
// before Start, or skip if you don't want recovery.
func (c *Cron) WithRecover(s *Store) *Cron {
	c.jobStore = s
	return c
}

// SetTick adjusts the polling interval. Default 15s (cron precision
// is 1 minute, polling more frequently doesn't help).
func (c *Cron) SetTick(d time.Duration) {
	if d > 0 {
		c.tick = d
	}
}

// Start runs the scheduler loop. Blocks until ctx cancelled.
//
// Each tick does two things:
//   1. Materialise due cron rows into _jobs rows.
//   2. Recover stuck jobs (status=running with expired locked_until)
//      back to pending — only when WithRecover() wired the jobs Store.
//
// Both are best-effort; transient errors logged but the loop continues.
func (c *Cron) Start(ctx context.Context) {
	ticker := time.NewTicker(c.tick)
	defer ticker.Stop()
	c.tickOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			c.log.Info("cron: scheduler stopped")
			return
		case <-ticker.C:
			c.tickOnce(ctx)
		}
	}
}

// tickOnce performs one cycle of the scheduler loop. Exposed so tests
// can drive the scheduler synchronously without spawning Start.
func (c *Cron) tickOnce(ctx context.Context) {
	if _, err := c.MaterialiseDue(ctx); err != nil && ctx.Err() == nil {
		c.log.Warn("cron: materialise tick", "err", err)
	}
	if c.jobStore != nil {
		if n, err := c.jobStore.Recover(ctx); err != nil && ctx.Err() == nil {
			c.log.Warn("cron: recover sweep", "err", err)
		} else if n > 0 {
			c.log.Info("jobs: recovered stuck rows", "count", n)
		}
	}
}

func scanCronRow(row pgx.Row) (*CronRow, error) {
	var r CronRow
	if err := row.Scan(&r.ID, &r.Name, &r.Expression, &r.Kind, &r.Payload,
		&r.Enabled, &r.LastRunAt, &r.NextRunAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("cron: not found")
		}
		return nil, err
	}
	return &r, nil
}

// ScheduledPayload helper for the common case of using map literals.
func ScheduledPayload(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}
