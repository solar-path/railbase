// Package jobs is the v1.4.0 background-work runner.
//
// Two layers:
//
//	1. Job queue. Handlers register under string kinds; callers
//	   Enqueue(kind, payload) and a worker pool claims rows via
//	   SELECT … FOR UPDATE SKIP LOCKED. Retries are bounded by
//	   max_attempts с exponential backoff (30s → 1h).
//
//	2. Cron. Persisted schedules in `_cron` produce one job-row per
//	   tick. The scheduler loop wakes once per minute, finds rows
//	   whose next_run_at has elapsed, materialises them into `_jobs`,
//	   and advances next_run_at.
//
// Wire-up:
//
//	store := jobs.NewStore(pool)
//	reg := jobs.NewRegistry(log)
//	reg.Register("cleanup_sessions", cleanupSessionsHandler)
//	runner := jobs.NewRunner(store, reg, log, jobs.RunnerOptions{Workers: 4})
//	go runner.Start(ctx)
//	cron := jobs.NewCron(store, log)
//	go cron.Start(ctx)
//
// Out of scope v1.4.0:
//   - Stuck-job recovery (lock-expired sweep) — v1.4.1 once we
//     have telemetry to see the rate.
//   - Per-queue worker pools (different concurrency by kind) —
//     v1.4.x.
//   - Admin UI panel — v1.4.x.
//   - LISTEN/NOTIFY tickler так worker reacts < 1s instead of polling
//     every 500ms — wire into existing PGBridge in v1.4.x.
//
// Hand-rolled cron parser (~150 LOC) keeps us off robfig/cron — same
// algorithmic surface, no transitive deps.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Status is the lifecycle state of a job row. Strings mirror the
// CHECK constraint in migration 0015.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// ErrPermanent is the sentinel a handler returns (wrapped) to signal
// "do not retry me — bug, malformed payload, or operator-mistake".
// The runner treats it the same as exhausted-attempts: terminal
// `failed` status, no backoff scheduled.
//
// Use via errors.Join or fmt.Errorf with %w:
//
//	return fmt.Errorf("bad payload: %w", jobs.ErrPermanent)
//
// Pre-v1.7.31 the only permanent-fail path was "unknown kind" (handled
// inside runner.process). Now builtins like send_email_async and
// scheduled_backup can flag malformed payloads as permanent rather
// than wasting backoff cycles on doomed retries.
var ErrPermanent = errors.New("jobs: permanent failure")

// Job is the in-memory representation of one `_jobs` row.
type Job struct {
	ID          uuid.UUID
	Queue       string
	Kind        string
	Payload     []byte // raw JSON
	Status      Status
	Attempts    int
	MaxAttempts int
	LastError   string
	RunAfter    time.Time
	LockedBy    *string
	LockedUntil *time.Time
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	CronID      *uuid.UUID
}

// EnqueueOptions tune one Enqueue call.
type EnqueueOptions struct {
	Queue       string        // default "default"
	RunAfter    time.Time     // zero = NOW()
	MaxAttempts int           // default 5
	Delay       time.Duration // additive on top of RunAfter
}

// Handler processes one job. Return nil on success; any error is
// captured as last_error and the job is retried (or marked failed
// when attempts == max_attempts).
//
// Handlers should respect ctx — when the runner is stopping it
// cancels in-flight ctxs and waits up to a grace window before
// abandoning the worker.
type Handler func(ctx context.Context, j *Job) error

// Registry maps kind → handler. Goroutine-safe; Register may be
// called during boot (typical) or at runtime (rare).
type Registry struct {
	log *slog.Logger
	mu  sync.RWMutex
	m   map[string]Handler
}

// NewRegistry constructs an empty registry. `log` may be nil; uses
// slog.Default in that case.
func NewRegistry(log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{log: log, m: map[string]Handler{}}
}

// Register binds kind → handler. Overwriting is allowed (test pattern);
// production callers should register once at boot.
func (r *Registry) Register(kind string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[kind] = h
}

// Lookup returns the handler for kind, or nil if unregistered.
func (r *Registry) Lookup(kind string) Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[kind]
}

// Kinds reports all registered kinds. Admin / CLI uses this for
// "is my cron expr pointing at a real handler?".
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	return out
}

// ErrUnknownKind is the sentinel for "no handler registered for this
// kind." The worker marks the job permanently failed (no retry — the
// problem won't fix itself).
var ErrUnknownKind = errors.New("jobs: unknown kind")

// nextBackoff computes the delay until the next attempt for a job
// whose current attempt count is `attempts` (already incremented).
//
// Curve: 30s, 1min, 2min, 4min, 8min, capped at 1h. Standard
// "binary exponential" with a soft ceiling — gives the failing
// dependency room to recover without flooding logs forever.
func nextBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	const base = 30 * time.Second
	const cap = 1 * time.Hour
	// (1 << (attempts-1)) overflows past ~62 but max_attempts caps
	// way before that. Bound anyway as defence.
	if attempts > 12 {
		return cap
	}
	d := base * time.Duration(1<<(attempts-1))
	if d > cap {
		d = cap
	}
	return d
}
