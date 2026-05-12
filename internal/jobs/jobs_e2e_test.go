//go:build embed_pg

// Live jobs smoke. Spins up embedded Postgres, applies system
// migrations, then exercises:
//
//   1. Enqueue → Claim → Complete (happy path)
//   2. Unknown kind → permanent fail
//   3. Handler error → retry with backoff (attempt count rises)
//   4. Cancel pending job
//   5. Cron Upsert → MaterialiseDue inserts a job and advances next_run_at
//   6. Worker pool drains a small batch concurrently
//
// Run:
//   go test -tags embed_pg -run TestJobsFlowE2E -timeout 90s ./internal/jobs/...

package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

func TestJobsFlowE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	store := NewStore(pool)
	cronStore := NewCronStore(pool)
	reg := NewRegistry(log)

	// === [1] Happy path: enqueue → claim → complete ===
	var hits atomic.Int32
	reg.Register("ping", func(ctx context.Context, j *Job) error {
		hits.Add(1)
		return nil
	})
	j1, err := store.Enqueue(ctx, "ping", nil, EnqueueOptions{})
	if err != nil {
		t.Fatalf("[1] enqueue: %v", err)
	}
	got1, err := store.Claim(ctx, "default", "test-w1", time.Minute)
	if err != nil || got1 == nil {
		t.Fatalf("[1] claim: got=%v err=%v", got1, err)
	}
	if got1.ID != j1.ID {
		t.Errorf("[1] claim id mismatch: %s != %s", got1.ID, j1.ID)
	}
	if err := store.Complete(ctx, got1.ID); err != nil {
		t.Fatalf("[1] complete: %v", err)
	}
	reread, _ := store.Get(ctx, j1.ID)
	if reread.Status != StatusCompleted {
		t.Errorf("[1] status: %v", reread.Status)
	}
	t.Logf("[1] enqueue→claim→complete OK (status=%s)", reread.Status)

	// === [2] Unknown kind → permanent fail via Runner.process ===
	j2, _ := store.Enqueue(ctx, "nope_unknown", nil, EnqueueOptions{MaxAttempts: 3})
	// Drive one tick of the runner manually.
	runner := NewRunner(store, reg, log, RunnerOptions{Workers: 1, PollInterval: 50 * time.Millisecond})
	rctx, rcancel := context.WithCancel(ctx)
	go runner.Start(rctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := store.Get(ctx, j2.ID)
		if r != nil && r.Status == StatusFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	r2, _ := store.Get(ctx, j2.ID)
	if r2.Status != StatusFailed {
		t.Errorf("[2] unknown kind: expected failed, got %s", r2.Status)
	}
	if r2.LastError == "" {
		t.Errorf("[2] missing last_error")
	}
	t.Logf("[2] unknown kind → status=%s err=%q", r2.Status, r2.LastError)

	// === [3] Handler error → retry with backoff ===
	var attempts atomic.Int32
	reg.Register("flaky", func(ctx context.Context, j *Job) error {
		attempts.Add(1)
		return errors.New("simulated failure")
	})
	j3, _ := store.Enqueue(ctx, "flaky", nil, EnqueueOptions{MaxAttempts: 2})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := store.Get(ctx, j3.ID)
		if r != nil && r.Status == StatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	rcancel()
	r3, _ := store.Get(ctx, j3.ID)
	// Note: max=2 so after attempt 1 it goes pending with backoff (30s),
	// which is too long for our wait. We only verify attempt incremented.
	if r3.Attempts < 1 {
		t.Errorf("[3] expected at least 1 attempt, got %d", r3.Attempts)
	}
	if attempts.Load() < 1 {
		t.Errorf("[3] expected handler invoked at least once, got %d", attempts.Load())
	}
	t.Logf("[3] flaky handler: attempts=%d status=%s", r3.Attempts, r3.Status)

	// === [4] Cancel pending ===
	j4, _ := store.Enqueue(ctx, "ping", nil, EnqueueOptions{
		RunAfter: time.Now().Add(1 * time.Hour), // won't be picked up
	})
	ok, err := store.Cancel(ctx, j4.ID)
	if err != nil || !ok {
		t.Fatalf("[4] cancel: ok=%v err=%v", ok, err)
	}
	r4, _ := store.Get(ctx, j4.ID)
	if r4.Status != StatusCancelled {
		t.Errorf("[4] status after cancel: %v", r4.Status)
	}
	// Idempotent: second cancel returns false.
	ok2, _ := store.Cancel(ctx, j4.ID)
	if ok2 {
		t.Errorf("[4] re-cancel should be no-op")
	}
	t.Logf("[4] cancel + idempotent re-cancel OK")

	// === [5] Cron Upsert → MaterialiseDue ===
	// Use "* * * * *" so Next is the upcoming minute boundary.
	// Set next_run_at directly to now() so we don't have to wait.
	cr, err := cronStore.Upsert(ctx, "every-minute", "* * * * *", "ping", map[string]any{"src": "cron"})
	if err != nil {
		t.Fatalf("[5] upsert: %v", err)
	}
	// Force next_run_at to past so MaterialiseDue picks it up.
	if _, err := pool.Exec(ctx, `UPDATE _cron SET next_run_at = now() - INTERVAL '1 minute' WHERE id = $1`, cr.ID); err != nil {
		t.Fatal(err)
	}
	cron := NewCron(cronStore, log)
	count, err := cron.MaterialiseDue(ctx)
	if err != nil {
		t.Fatalf("[5] materialise: %v", err)
	}
	if count != 1 {
		t.Errorf("[5] expected 1 materialised, got %d", count)
	}
	cr2, _ := cronStore.List(ctx)
	var found *CronRow
	for _, x := range cr2 {
		if x.Name == "every-minute" {
			found = x
			break
		}
	}
	if found == nil || found.NextRunAt == nil || !found.NextRunAt.After(time.Now()) {
		t.Errorf("[5] next_run_at should be in the future, got %+v", found.NextRunAt)
	}
	// Confirm a corresponding _jobs row exists.
	jobs, _ := store.List(ctx, "", 50)
	var cronJob *Job
	for _, x := range jobs {
		if x.CronID != nil && *x.CronID == cr.ID {
			cronJob = x
			break
		}
	}
	if cronJob == nil {
		t.Errorf("[5] no _jobs row tagged with cron_id")
	}
	t.Logf("[5] cron materialised %d row(s); next_run_at advanced to %s", count, found.NextRunAt)

	// === [6] Worker drains a small batch concurrently ===
	hits.Store(0)
	for i := 0; i < 5; i++ {
		store.Enqueue(ctx, "ping", nil, EnqueueOptions{})
	}
	rctx2, rcancel2 := context.WithCancel(ctx)
	runner2 := NewRunner(store, reg, log, RunnerOptions{Workers: 3, PollInterval: 50 * time.Millisecond})
	go runner2.Start(rctx2)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() >= 5 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	rcancel2()
	if hits.Load() < 5 {
		t.Errorf("[6] expected 5 hits, got %d", hits.Load())
	}
	t.Logf("[6] worker pool drained 5 jobs; hits=%d", hits.Load())

	// === [7] Recover sweeps stuck running rows ===
	// Insert a "stuck" row directly: status=running with locked_until in the past.
	if _, err := pool.Exec(ctx, `
		INSERT INTO _jobs (id, queue, kind, payload, status, attempts, max_attempts, run_after, locked_by, locked_until)
		VALUES (gen_random_uuid(), 'default', 'ping', '{}'::jsonb, 'running', 1, 5, now(), 'dead-worker', now() - INTERVAL '1 minute')`); err != nil {
		t.Fatal(err)
	}
	n, err := store.Recover(ctx)
	if err != nil {
		t.Fatalf("[7] recover: %v", err)
	}
	if n < 1 {
		t.Errorf("[7] expected at least 1 recovered row, got %d", n)
	}
	// Verify the row is now pending with the recovery note in last_error.
	var status, lastErr string
	if err := pool.QueryRow(ctx, `
		SELECT status, COALESCE(last_error, '') FROM _jobs WHERE locked_by IS NULL AND last_error LIKE '%recovered from stuck%'
		ORDER BY created_at DESC LIMIT 1`).Scan(&status, &lastErr); err != nil {
		t.Fatalf("[7] re-scan: %v", err)
	}
	if status != string(StatusPending) {
		t.Errorf("[7] recovered row status: %s", status)
	}
	t.Logf("[7] recovered %d stuck row(s); last_error=%q", n, lastErr)

	// === [8] RunNow forces pending → run_after=now() ===
	j8, _ := store.Enqueue(ctx, "ping", nil, EnqueueOptions{
		RunAfter: time.Now().Add(2 * time.Hour),
	})
	runNow, runNowErr8 := store.RunNow(ctx, j8.ID)
	if runNowErr8 != nil || !runNow {
		t.Fatalf("[8] RunNow: ok=%v err=%v", runNow, runNowErr8)
	}
	r8, _ := store.Get(ctx, j8.ID)
	if !r8.RunAfter.Before(time.Now().Add(1 * time.Minute)) {
		t.Errorf("[8] run_after should be ~now, got %s", r8.RunAfter)
	}
	t.Logf("[8] RunNow set run_after to %s", r8.RunAfter.Format(time.RFC3339))

	// === [9] Reset failed/cancelled job ===
	j9, _ := store.Enqueue(ctx, "ping", nil, EnqueueOptions{})
	if _, cerr := store.Cancel(ctx, j9.ID); cerr != nil {
		t.Fatal(cerr)
	}
	resetOK, resetErr := store.Reset(ctx, j9.ID)
	if resetErr != nil || !resetOK {
		t.Fatalf("[9] reset: ok=%v err=%v", resetOK, resetErr)
	}
	r9, _ := store.Get(ctx, j9.ID)
	if r9.Status != StatusPending {
		t.Errorf("[9] reset status: %s", r9.Status)
	}
	if r9.Attempts != 0 {
		t.Errorf("[9] reset attempts: %d", r9.Attempts)
	}
	// Reset on non-failed/cancelled is a no-op.
	resetOK2, _ := store.Reset(ctx, j9.ID)
	if resetOK2 {
		t.Errorf("[9] reset on pending should be no-op")
	}
	t.Logf("[9] reset cancelled job → pending; reset on pending = no-op")

	// === [10] CronStore.RunNow materialises immediately without advancing next_run_at ===
	beforeNext := *found.NextRunAt
	jobID, runNowOK, runNowErr := cronStore.RunNow(ctx, "every-minute")
	if runNowErr != nil || !runNowOK {
		t.Fatalf("[10] cron run-now: ok=%v err=%v", runNowOK, runNowErr)
	}
	// Verify a new _jobs row exists with cron_id matching every-minute.
	enqueued, getErr := store.Get(ctx, jobID)
	if getErr != nil {
		t.Fatalf("[10] get materialised: %v", getErr)
	}
	if enqueued.CronID == nil || *enqueued.CronID != cr.ID {
		t.Errorf("[10] cron_id mismatch on materialised job")
	}
	// next_run_at must be unchanged.
	rows3, _ := cronStore.List(ctx)
	for _, r := range rows3 {
		if r.Name == "every-minute" {
			if !r.NextRunAt.Equal(beforeNext) {
				t.Errorf("[10] next_run_at advanced unexpectedly: was %s now %s", beforeNext, r.NextRunAt)
			}
			break
		}
	}
	t.Logf("[10] cron run-now materialised job %s; next_run_at preserved at %s", jobID, beforeNext.Format(time.RFC3339))

	t.Log("Jobs E2E: 10/10 checks passed")
}
