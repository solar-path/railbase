package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the minimal pgx subset Store depends on.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Store persists jobs + cron rows. Goroutine-safe (no in-memory state).
type Store struct {
	q Querier
}

// NewStore constructs the store atop any Querier (pool or tx).
func NewStore(q Querier) *Store { return &Store{q: q} }

// Enqueue inserts a new job row. Returns the assigned ID + created_at.
// Payload is marshalled to JSONB; pass any JSON-encodable Go value or
// raw json.RawMessage. nil payload is stored as `{}`.
func (s *Store) Enqueue(ctx context.Context, kind string, payload any, opts EnqueueOptions) (*Job, error) {
	if kind == "" {
		return nil, errors.New("jobs: kind required")
	}
	if opts.Queue == "" {
		opts.Queue = "default"
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.RunAfter.IsZero() {
		opts.RunAfter = time.Now().UTC()
	}
	if opts.Delay > 0 {
		opts.RunAfter = opts.RunAfter.Add(opts.Delay)
	}

	body, err := encodePayload(payload)
	if err != nil {
		return nil, fmt.Errorf("jobs: marshal payload: %w", err)
	}

	j := &Job{
		ID:          uuid.Must(uuid.NewV7()),
		Queue:       opts.Queue,
		Kind:        kind,
		Payload:     body,
		Status:      StatusPending,
		MaxAttempts: opts.MaxAttempts,
		RunAfter:    opts.RunAfter,
	}
	err = s.q.QueryRow(ctx, `
		INSERT INTO _jobs (id, queue, kind, payload, max_attempts, run_after)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		j.ID, j.Queue, j.Kind, j.Payload, j.MaxAttempts, j.RunAfter,
	).Scan(&j.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("jobs: insert: %w", err)
	}
	return j, nil
}

// Claim atomically pulls one due job from `queue` for the named
// worker. Returns (nil, nil) when nothing is due — caller waits or
// re-polls. Uses SELECT … FOR UPDATE SKIP LOCKED so multiple workers
// don't fight over the same row.
//
// lockTTL governs how long the worker's claim is honoured before
// stuck-job recovery (v1.4.x) considers reclaiming it. The lock is
// best-effort — workers MUST also detect ctx cancellation.
func (s *Store) Claim(ctx context.Context, queue, workerID string, lockTTL time.Duration) (*Job, error) {
	if workerID == "" {
		return nil, errors.New("jobs: workerID required")
	}
	if lockTTL <= 0 {
		lockTTL = 5 * time.Minute
	}
	row := s.q.QueryRow(ctx, `
		WITH next AS (
		    SELECT id FROM _jobs
		    WHERE status = 'pending'
		      AND queue = $1
		      AND run_after <= now()
		    ORDER BY run_after
		    FOR UPDATE SKIP LOCKED
		    LIMIT 1
		)
		UPDATE _jobs j
		   SET status = 'running',
		       attempts = j.attempts + 1,
		       locked_by = $2,
		       locked_until = now() + $3::interval,
		       started_at = now()
		  FROM next
		 WHERE j.id = next.id
		RETURNING j.id, j.queue, j.kind, j.payload, j.status,
		          j.attempts, j.max_attempts, j.last_error,
		          j.run_after, j.locked_by, j.locked_until,
		          j.created_at, j.started_at, j.completed_at, j.cron_id`,
		queue, workerID, fmt.Sprintf("%d seconds", int(lockTTL.Seconds())),
	)
	j, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

// Complete marks a successfully-run job. Idempotent — re-completing a
// completed row is a no-op (RowsAffected=0).
func (s *Store) Complete(ctx context.Context, id uuid.UUID) error {
	_, err := s.q.Exec(ctx, `
		UPDATE _jobs
		   SET status = 'completed',
		       completed_at = now(),
		       locked_by = NULL,
		       locked_until = NULL,
		       last_error = NULL
		 WHERE id = $1 AND status = 'running'`, id)
	return err
}

// Fail records an attempt failure. When attempts < max_attempts the
// row goes back to pending with a backoff'd run_after; otherwise it's
// terminally failed.
func (s *Store) Fail(ctx context.Context, id uuid.UUID, attempts, maxAttempts int, errStr string) error {
	if attempts >= maxAttempts {
		_, err := s.q.Exec(ctx, `
			UPDATE _jobs
			   SET status = 'failed',
			       completed_at = now(),
			       locked_by = NULL,
			       locked_until = NULL,
			       last_error = $1
			 WHERE id = $2 AND status = 'running'`, errStr, id)
		return err
	}
	backoff := nextBackoff(attempts)
	_, err := s.q.Exec(ctx, `
		UPDATE _jobs
		   SET status = 'pending',
		       run_after = now() + $1::interval,
		       locked_by = NULL,
		       locked_until = NULL,
		       last_error = $2
		 WHERE id = $3 AND status = 'running'`,
		fmt.Sprintf("%d seconds", int(backoff.Seconds())), errStr, id)
	return err
}

// Recover finds running jobs whose locked_until has elapsed and
// returns them to pending status so other workers can pick them up.
// Returns the count recovered. Crashed/killed workers leave rows
// stuck in running indefinitely without this sweep.
//
// Called periodically by the scheduler loop (every Cron tick).
// Attempts are NOT decremented — the recovered row still counts as
// "tried"; on next claim attempts will increment again, and once
// max_attempts is hit the job terminally fails. This is the right
// shape: a worker that died on a job MIGHT have started bad side-
// effects, so we shouldn't blindly retry forever.
func (s *Store) Recover(ctx context.Context) (int64, error) {
	tag, err := s.q.Exec(ctx, `
		UPDATE _jobs
		   SET status = 'pending',
		       locked_by = NULL,
		       locked_until = NULL,
		       last_error = COALESCE(last_error, '') ||
		                    CASE WHEN last_error IS NOT NULL AND last_error <> ''
		                         THEN ' | ' ELSE '' END ||
		                    'recovered from stuck running state at ' || now()::text
		 WHERE status = 'running'
		   AND locked_until IS NOT NULL
		   AND locked_until < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RunNow forces a pending job to be eligible immediately by setting
// run_after = now(). For "skip the backoff and try again" admin
// actions. No-op on rows in other states.
func (s *Store) RunNow(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx, `
		UPDATE _jobs
		   SET run_after = now()
		 WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Reset moves a failed/cancelled job back to pending with attempts
// zeroed — operator-driven "try this again from scratch" path.
func (s *Store) Reset(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx, `
		UPDATE _jobs
		   SET status = 'pending',
		       attempts = 0,
		       last_error = NULL,
		       run_after = now(),
		       locked_by = NULL,
		       locked_until = NULL,
		       started_at = NULL,
		       completed_at = NULL
		 WHERE id = $1 AND status IN ('failed','cancelled')`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Cancel marks a pending or running job as cancelled. Idempotent.
// Returns true when a row was updated (caller can audit-log).
func (s *Store) Cancel(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx, `
		UPDATE _jobs
		   SET status = 'cancelled',
		       completed_at = now(),
		       locked_by = NULL,
		       locked_until = NULL
		 WHERE id = $1 AND status IN ('pending','running')`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Get returns the current row state for id.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Job, error) {
	row := s.q.QueryRow(ctx, `
		SELECT id, queue, kind, payload, status, attempts, max_attempts,
		       last_error, run_after, locked_by, locked_until,
		       created_at, started_at, completed_at, cron_id
		FROM _jobs WHERE id = $1`, id)
	return scanJob(row)
}

// List returns the last N jobs with optional status filter (empty = all).
// Ordered newest-first; admin UI uses this for the queue panel.
func (s *Store) List(ctx context.Context, status Status, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		rows pgx.Rows
		err  error
	)
	if status == "" {
		rows, err = s.q.Query(ctx, `
			SELECT id, queue, kind, payload, status, attempts, max_attempts,
			       last_error, run_after, locked_by, locked_until,
			       created_at, started_at, completed_at, cron_id
			FROM _jobs
			ORDER BY created_at DESC
			LIMIT $1`, limit)
	} else {
		rows, err = s.q.Query(ctx, `
			SELECT id, queue, kind, payload, status, attempts, max_attempts,
			       last_error, run_after, locked_by, locked_until,
			       created_at, started_at, completed_at, cron_id
			FROM _jobs
			WHERE status = $1
			ORDER BY created_at DESC
			LIMIT $2`, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListFiltered is like List but also accepts a case-insensitive
// substring filter on `kind`. Added in v1.7.7 for the admin UI's
// Jobs queue browser; the CLI still uses the plain List signature so
// we keep this as a separate method instead of overloading the
// existing one.
//
// Both filters are independent: empty status = any status, empty kind
// = any kind. Ordered newest-first; capped at limit (default 100, max
// 500 same as List).
func (s *Store) ListFiltered(ctx context.Context, status Status, kind string, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Build the WHERE clause dynamically so the path is one SQL
	// statement regardless of which filters are set.
	clauses := []string{}
	args := []any{}
	if status != "" {
		args = append(args, status)
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	}
	if kind != "" {
		args = append(args, "%"+kind+"%")
		clauses = append(clauses, fmt.Sprintf("kind ILIKE $%d", len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + joinClauses(clauses)
	}
	args = append(args, limit)
	q := `SELECT id, queue, kind, payload, status, attempts, max_attempts,
	             last_error, run_after, locked_by, locked_until,
	             created_at, started_at, completed_at, cron_id
	      FROM _jobs` + where + ` ORDER BY created_at DESC LIMIT $` + fmt.Sprintf("%d", len(args))
	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Count returns the total number of jobs matching the same status +
// kind filters as ListFiltered. Used by the admin endpoint for the
// pagination "totalItems" header.
func (s *Store) Count(ctx context.Context, status Status, kind string) (int64, error) {
	clauses := []string{}
	args := []any{}
	if status != "" {
		args = append(args, status)
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	}
	if kind != "" {
		args = append(args, "%"+kind+"%")
		clauses = append(clauses, fmt.Sprintf("kind ILIKE $%d", len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + joinClauses(clauses)
	}
	q := `SELECT count(*) FROM _jobs` + where
	var c int64
	if err := s.q.QueryRow(ctx, q, args...).Scan(&c); err != nil {
		return 0, err
	}
	return c, nil
}

// joinClauses concatenates WHERE-clause fragments with " AND ". A tiny
// helper to avoid importing strings just for one Join call in this
// file (encodePayload below also stays std-light).
func joinClauses(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += " AND " + parts[i]
	}
	return out
}

// scanJob decodes one row whose SELECT list matches Get's signature.
// Nullable columns use pgtype-friendly intermediates because pgx v5
// can't scan NULL directly into typed *string / *time.Time.
func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var lastErr, lockedBy *string
	var lockedUntil, startedAt, completedAt *time.Time
	if err := row.Scan(&j.ID, &j.Queue, &j.Kind, &j.Payload, &j.Status,
		&j.Attempts, &j.MaxAttempts, &lastErr, &j.RunAfter,
		&lockedBy, &lockedUntil, &j.CreatedAt, &startedAt,
		&completedAt, &j.CronID); err != nil {
		return nil, err
	}
	if lastErr != nil {
		j.LastError = *lastErr
	}
	j.LockedBy = lockedBy
	j.LockedUntil = lockedUntil
	j.StartedAt = startedAt
	j.CompletedAt = completedAt
	return &j, nil
}

// encodePayload normalises any payload to []byte JSON.
func encodePayload(p any) ([]byte, error) {
	if p == nil {
		return []byte("{}"), nil
	}
	if raw, ok := p.(json.RawMessage); ok {
		if len(raw) == 0 {
			return []byte("{}"), nil
		}
		return raw, nil
	}
	if b, ok := p.([]byte); ok {
		if len(b) == 0 {
			return []byte("{}"), nil
		}
		return b, nil
	}
	return json.Marshal(p)
}
