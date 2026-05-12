package hooks

// v1.7.22 — docs/17 #174 hook dispatch concurrency / throughput.
//
// The dispatcher serialises VM access through r.vmMu (v1.2.0 ships
// a single goja VM per Runtime — per-CPU pool is a v1.2.x polish).
// These benchmarks characterise:
//
//   1. The "no handlers" fast path (HasHandlers gate) — what every
//      record write pays when hooks are empty. Must be ~nanoseconds,
//      else the gate is broken.
//   2. The single-handler slow path — JS invocation cost, measured
//      against a trivial handler. Sets the floor.
//   3. Concurrent dispatch under 100 goroutines — exposes deadlocks
//      (would hang the bench) and measures contention on vmMu.
//
// Run:
//
//	go test -bench=. -benchmem -run=^$ -benchtime=2s ./internal/hooks/...

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newBenchRuntime spins up a Runtime against a tempdir with the supplied
// JS files. Cleanup via b.Cleanup so b.Run sub-bench can reuse one
// runtime across the iteration loop.
func newBenchRuntime(b *testing.B, jsFiles map[string]string) *Runtime {
	b.Helper()
	dir := b.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		b.Fatal(err)
	}
	for name, body := range jsFiles {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	r, err := NewRuntime(Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  2 * time.Second,
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := r.Load(context.Background()); err != nil {
		b.Fatalf("load: %v", err)
	}
	return r
}

// BenchmarkDispatch_NoHandlers exercises the HasHandlers fast-path.
// This is what EVERY record write costs when no hooks are registered.
// Should be < 1µs.
func BenchmarkDispatch_NoHandlers(b *testing.B) {
	r := newBenchRuntime(b, nil)
	ctx := context.Background()
	rec := map[string]any{"id": "x", "title": "hi"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Dispatch(ctx, "posts", EventRecordBeforeCreate, rec); err != nil {
			b.Fatalf("dispatch: %v", err)
		}
	}
}

// BenchmarkDispatch_SingleHandler measures one round-trip into goja
// for a trivial handler. Per-iteration latency = JS invocation cost
// (object construction + script eval + return-value marshalling).
func BenchmarkDispatch_SingleHandler(b *testing.B) {
	r := newBenchRuntime(b, map[string]string{
		"trivial.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.touched = true;
    return e.next();
});
`,
	})
	ctx := context.Background()

	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := map[string]any{"id": "x", "title": "hi"}
		t0 := time.Now()
		if _, err := r.Dispatch(ctx, "posts", EventRecordBeforeCreate, rec); err != nil {
			b.Fatalf("dispatch: %v", err)
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
	}
}

// BenchmarkDispatch_Concurrent_100 is the docs/17 #174 acceptance gate:
// 100 goroutines firing Dispatch in parallel must not deadlock + must
// progress at a reasonable rate. We use a fixed total-event count and
// divide it across goroutines so b.N stays meaningful as "total
// dispatches".
//
// The runtime serialises through vmMu — the throughput here is the
// upper bound on hook-driven write throughput on a single Runtime
// instance.
func BenchmarkDispatch_Concurrent_100(b *testing.B) { benchConcurrent(b, 100) }

// BenchmarkDispatch_Concurrent_10 — moderate parallelism. Useful baseline
// for the contention curve between 1 goroutine (the SingleHandler bench
// above) and 100 (the gate).
func BenchmarkDispatch_Concurrent_10(b *testing.B) { benchConcurrent(b, 10) }

func benchConcurrent(b *testing.B, goroutines int) {
	r := newBenchRuntime(b, map[string]string{
		"trivial.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.touched = true;
    return e.next();
});
`,
	})
	ctx := context.Background()

	// Each goroutine drives `perG` dispatches. Total dispatches = b.N.
	perG := b.N / goroutines
	if perG == 0 {
		perG = 1
	}
	var completed atomic.Int64

	b.ResetTimer()
	t0 := time.Now()

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := map[string]any{"id": "x", "title": "hi"}
			for i := 0; i < perG; i++ {
				if _, err := r.Dispatch(ctx, "posts", EventRecordBeforeCreate, rec); err != nil {
					b.Errorf("dispatch: %v", err)
					return
				}
				completed.Add(1)
			}
		}()
	}
	wg.Wait()
	wall := time.Since(t0)
	b.StopTimer()

	total := completed.Load()
	if total == 0 {
		b.Fatal("zero dispatches completed")
	}
	dispatchesPerSec := float64(total) / wall.Seconds()
	b.ReportMetric(dispatchesPerSec, "dispatches_per_sec")
	b.ReportMetric(float64(goroutines), "goroutines")
	// Wall ÷ total = mean per-dispatch latency under contention.
	meanPerOp := wall / time.Duration(total)
	b.ReportMetric(float64(meanPerOp.Microseconds()), "mean_µs_per_dispatch")
}

// TestDispatch_Concurrent_NoDeadlock is the docs/17 #174 correctness
// gate. 100 goroutines × 50 dispatches each = 5000 total. Must finish
// in < 30s (the bench takes ~1s on M2; the test budget is generous).
// Guards against silent regressions that would lock the dispatcher.
//
// NOT a Benchmark so it runs in the normal sweep.
func TestDispatch_Concurrent_NoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent-dispatch test in -short mode")
	}
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	_ = os.MkdirAll(hooksDir, 0o755)
	_ = os.WriteFile(filepath.Join(hooksDir, "trivial.js"), []byte(`
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.touched = true;
    return e.next();
});
`), 0o644)
	r, err := NewRuntime(Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	const goroutines = 100
	const perG = 50

	done := make(chan struct{})
	var completed atomic.Int64
	go func() {
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perG; i++ {
					rec := map[string]any{"id": "x", "title": "hi"}
					if _, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec); err != nil {
						t.Errorf("dispatch: %v", err)
						return
					}
					completed.Add(1)
				}
			}()
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if got := completed.Load(); got != goroutines*perG {
			t.Errorf("completed = %d, want %d", got, goroutines*perG)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("deadlock: only %d/%d dispatches completed after 30s",
			completed.Load(), goroutines*perG)
	}
}
