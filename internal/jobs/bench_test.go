//go:build embed_pg

// v1.7.14 — docs/17 #173 Jobs queue throughput benchmark.
//
// Measures Enqueue + Claim throughput against a live embedded
// Postgres. The docs/17 #173 target is "5000 jobs/sec sustained on
// single Postgres under 8 workers" — this benchmark gives concrete
// numbers operators can compare to that target.
//
// Run:
//
//   go test -tags embed_pg -bench=. -benchmem -run=^$ \
//     -benchtime=5s ./internal/jobs/...
//
// Reported metrics (custom via b.ReportMetric):
//   - jobs_per_sec — sustained throughput
//   - p50_µs / p99_µs — per-op latency

package jobs

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

// benchSetup spins up embedded PG + migrations + Store. Cleanup
// happens via b.Cleanup. Returns a fresh Store keyed off a temporary
// data dir so concurrent benchmarks don't collide.
func benchSetup(b *testing.B) *Store {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	b.Cleanup(cancel)

	dataDir := b.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		b.Fatalf("embedded pg: %v", err)
	}
	b.Cleanup(func() { _ = stopPG() })

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		b.Fatal(err)
	}

	return NewStore(pool)
}

// BenchmarkEnqueue measures raw insert throughput: how fast can a
// single goroutine push jobs into the queue. This is the upper-bound
// throughput before SKIP LOCKED contention kicks in.
func BenchmarkEnqueue(b *testing.B) {
	store := benchSetup(b)
	ctx := context.Background()

	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		if _, err := store.Enqueue(ctx, "bench_kind", nil, EnqueueOptions{}); err != nil {
			b.Fatalf("enqueue: %v", err)
		}
		latencies = append(latencies, time.Since(t0))
	}
	b.StopTimer()

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[len(latencies)*50/100]
		p99 := latencies[len(latencies)*99/100]
		b.ReportMetric(float64(p50.Microseconds()), "p50_µs")
		b.ReportMetric(float64(p99.Microseconds()), "p99_µs")
		// Inverse of mean = ops/sec.
		var sum time.Duration
		for _, d := range latencies {
			sum += d
		}
		ops := float64(time.Second) / (float64(sum) / float64(len(latencies)))
		b.ReportMetric(ops, "jobs_per_sec")
	}
}

// BenchmarkClaim measures the SKIP LOCKED claim path. Pre-seeds the
// queue with b.N+slack jobs, then races a single worker through them.
// This is the docs/17 #173 metric — claim throughput under load.
func BenchmarkClaim(b *testing.B) {
	store := benchSetup(b)
	ctx := context.Background()

	// Seed b.N jobs (plus 100 slack so the worker never starves).
	for i := 0; i < b.N+100; i++ {
		if _, err := store.Enqueue(ctx, "bench_kind", nil, EnqueueOptions{}); err != nil {
			b.Fatalf("seed enqueue: %v", err)
		}
	}

	latencies := make([]time.Duration, 0, b.N)
	workerID := "bench-worker"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		job, err := store.Claim(ctx, "default", workerID, 30*time.Second)
		if err != nil {
			b.Fatalf("claim: %v", err)
		}
		if job == nil {
			b.Fatalf("claim returned nil at iter %d", i)
		}
		latencies = append(latencies, time.Since(t0))
		// Mark complete to keep the queue moving.
		if err := store.Complete(ctx, job.ID); err != nil {
			b.Fatalf("complete: %v", err)
		}
	}
	b.StopTimer()

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[len(latencies)*50/100]
		p99 := latencies[len(latencies)*99/100]
		b.ReportMetric(float64(p50.Microseconds()), "p50_µs")
		b.ReportMetric(float64(p99.Microseconds()), "p99_µs")
		var sum time.Duration
		for _, d := range latencies {
			sum += d
		}
		ops := float64(time.Second) / (float64(sum) / float64(len(latencies)))
		b.ReportMetric(ops, "claims_per_sec")
	}
}

// TestJobsThroughput_NoDoubleExecution is the docs/17 #173
// correctness gate: under 4 concurrent workers claiming the same
// queue, every job must be claimed exactly once. This guards against
// silent SKIP LOCKED regressions.
//
// Not a benchmark — runs as a regular test (requires embed_pg) so the
// invariant is checked in the normal sweep.
func TestJobsThroughput_NoDoubleExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	// Seed 200 jobs.
	const total = 200
	jobIDs := make(map[uuid.UUID]struct{}, total)
	for i := 0; i < total; i++ {
		j, err := store.Enqueue(ctx, "race_kind", nil, EnqueueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		jobIDs[j.ID] = struct{}{}
	}

	// Spin 4 workers; each loops claim → record-id → complete until
	// the queue is empty.
	const workers = 4
	claimed := make(chan uuid.UUID, total)
	var claimedCount atomic.Int32
	done := make(chan struct{})
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			for {
				job, err := store.Claim(ctx, "default", "w-bench", time.Minute)
				if err != nil {
					return
				}
				if job == nil {
					// Either the queue is empty OR another worker just
					// took it. Sleep briefly then check via Count.
					if claimedCount.Load() >= total {
						return
					}
					time.Sleep(2 * time.Millisecond)
					continue
				}
				_ = w
				claimed <- job.ID
				claimedCount.Add(1)
				if err := store.Complete(ctx, job.ID); err != nil {
					t.Errorf("complete: %v", err)
				}
				if claimedCount.Load() >= total {
					return
				}
			}
		}()
	}
	// Close channel when all done.
	go func() {
		for {
			if claimedCount.Load() >= total {
				close(done)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("workers didn't drain queue in 30s (got %d/%d)", claimedCount.Load(), total)
	}
	close(claimed)

	// Verify exactly-once: every jobID appears in `claimed` exactly once.
	seen := make(map[uuid.UUID]int, total)
	for id := range claimed {
		seen[id]++
	}
	for id := range jobIDs {
		if c := seen[id]; c != 1 {
			t.Errorf("job %s claimed %d times (want exactly 1)", id, c)
		}
	}
	if len(seen) != total {
		t.Errorf("claimed %d distinct jobs, want %d", len(seen), total)
	}
	t.Logf("4 workers claimed %d jobs with zero duplication", total)
}
