package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RunnerOptions tune the worker pool. Zero values pick sane defaults.
type RunnerOptions struct {
	// Workers is the number of concurrent goroutines pulling jobs.
	// Default GOMAXPROCS — but in practice 4 is plenty for typical
	// CRUD workloads (jobs are IO-bound).
	Workers int

	// Queue specifies which queue this Runner consumes. Default
	// "default". Multi-queue deployments spawn multiple Runners.
	Queue string

	// PollInterval is the sleep between empty-claim cycles. Default
	// 500ms — low enough to feel snappy, high enough not to hammer
	// the DB with empty SELECTs.
	PollInterval time.Duration

	// LockTTL bounds the cooperative lock on a claimed row.
	// Default 5min — long enough for any reasonable handler.
	LockTTL time.Duration

	// HandlerTimeout caps any single handler invocation. Default 1min.
	HandlerTimeout time.Duration
}

// Runner is the worker pool. One Runner pulls jobs off one queue.
type Runner struct {
	store *Store
	reg   *Registry
	log   *slog.Logger
	opts  RunnerOptions

	// Generated once at boot; used as locked_by so admin can correlate.
	workerID string

	mu      sync.Mutex
	stopped bool
}

// NewRunner constructs a Runner. Caller invokes Start (blocks until
// ctx is cancelled).
func NewRunner(store *Store, reg *Registry, log *slog.Logger, opts RunnerOptions) *Runner {
	if log == nil {
		log = slog.Default()
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Queue == "" {
		opts.Queue = "default"
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 500 * time.Millisecond
	}
	if opts.LockTTL <= 0 {
		opts.LockTTL = 5 * time.Minute
	}
	if opts.HandlerTimeout <= 0 {
		opts.HandlerTimeout = 1 * time.Minute
	}
	return &Runner{
		store:    store,
		reg:      reg,
		log:      log,
		opts:     opts,
		workerID: fmt.Sprintf("worker-%s", uuid.NewString()[:8]),
	}
}

// Start fires up the worker pool. Blocks until ctx is cancelled,
// then waits for in-flight handlers to either return or exhaust the
// HandlerTimeout. Safe to call once per Runner.
func (r *Runner) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < r.opts.Workers; i++ {
		wg.Add(1)
		go func(workerIdx int) {
			defer wg.Done()
			r.loop(ctx, workerIdx)
		}(i)
	}
	wg.Wait()
	r.log.Info("jobs: runner stopped", "queue", r.opts.Queue)
}

// loop is one worker's claim → process → repeat cycle.
func (r *Runner) loop(ctx context.Context, idx int) {
	workerName := fmt.Sprintf("%s.%d", r.workerID, idx)
	ticker := time.NewTicker(r.opts.PollInterval)
	defer ticker.Stop()

	for {
		// Try to claim immediately, but bail out if the ctx is
		// cancelled between iterations.
		select {
		case <-ctx.Done():
			return
		default:
		}

		j, err := r.store.Claim(ctx, r.opts.Queue, workerName, r.opts.LockTTL)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.log.Warn("jobs: claim error", "err", err, "worker", workerName)
			// Short backoff so we don't spin on persistent DB failures.
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.opts.PollInterval):
				continue
			}
		}
		if j == nil {
			// Nothing due — sleep and try again.
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		r.process(ctx, j, workerName)
	}
}

// process runs one job through its handler with timeout + panic
// recovery, then records the outcome.
func (r *Runner) process(parent context.Context, j *Job, worker string) {
	h := r.reg.Lookup(j.Kind)
	if h == nil {
		errStr := "unknown kind: " + j.Kind
		r.log.Warn("jobs: no handler", "kind", j.Kind, "id", j.ID)
		// Force permanent fail by passing maxAttempts so retry path
		// doesn't reset.
		_ = r.store.Fail(parent, j.ID, j.MaxAttempts, j.MaxAttempts, errStr)
		return
	}

	ctx, cancel := context.WithTimeout(parent, r.opts.HandlerTimeout)
	defer cancel()

	t0 := time.Now()
	err := safeRun(ctx, h, j)
	took := time.Since(t0)

	if err == nil {
		if cerr := r.store.Complete(parent, j.ID); cerr != nil {
			r.log.Error("jobs: complete failed", "id", j.ID, "err", cerr)
		}
		r.log.Info("jobs: completed", "kind", j.Kind, "id", j.ID, "took_ms", took.Milliseconds(), "worker", worker)
		return
	}

	// v1.7.31 — ErrPermanent short-circuits retry: pass maxAttempts as
	// both args so Fail treats this as terminal regardless of how many
	// attempts have actually occurred. Catches malformed payloads in
	// builtins like send_email_async / scheduled_backup that retrying
	// can't help.
	if errors.Is(err, ErrPermanent) {
		if ferr := r.store.Fail(parent, j.ID, j.MaxAttempts, j.MaxAttempts, err.Error()); ferr != nil {
			r.log.Error("jobs: fail-record failed", "id", j.ID, "err", ferr)
		}
		r.log.Warn("jobs: permanent failure",
			"kind", j.Kind, "id", j.ID, "err", err.Error(), "worker", worker)
		return
	}
	if ferr := r.store.Fail(parent, j.ID, j.Attempts, j.MaxAttempts, err.Error()); ferr != nil {
		r.log.Error("jobs: fail-record failed", "id", j.ID, "err", ferr)
	}
	r.log.Warn("jobs: handler error",
		"kind", j.Kind, "id", j.ID, "attempts", j.Attempts,
		"max", j.MaxAttempts, "err", err.Error(), "worker", worker)
}

// safeRun invokes h with panic recovery so one buggy handler doesn't
// kill the worker.
func safeRun(ctx context.Context, h Handler, j *Job) (err error) {
	defer func() {
		if rv := recover(); rv != nil {
			err = fmt.Errorf("panic: %v\n%s", rv, debug.Stack())
		}
	}()
	return h(ctx, j)
}
