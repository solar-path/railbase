//go:build embed_pg

// v1.7.22 — docs/17 #171 DB write throughput benchmark.
//
// Measures sustained INSERT throughput against a live embedded
// Postgres. The docs/17 #171 target is "10k writes/sec sustained on
// single Postgres" — these benchmarks give concrete numbers operators
// can compare to that target on their hardware.
//
// Three modes:
//
//   1. Serial INSERT — single goroutine, baseline.
//   2. Concurrent INSERT — 8 goroutines, the realistic pool-saturation
//      throughput. Matches `pgxpool` defaults (4× cores typical).
//   3. Batch (COPY FROM) INSERT — the upper-bound throughput, what
//      `railbase import data` (v1.7.19) achieves.
//
// Goes DIRECTLY to the pgxpool — no HTTP layer, no rule evaluation, no
// audit. The point of this benchmark is to characterise the DB layer
// in isolation; HTTP-layer overhead is measured by hand via `wrk`.
//
// Run:
//
//	go test -tags embed_pg -bench='^BenchmarkThroughput_' -benchmem \
//	    -run=^$ -benchtime=3s ./internal/api/rest/...

package rest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

// thBenchSetup brings up embedded PG + sys migrations + one trivial
// collection (bench_items with a text column). Cleanup via b.Cleanup.
// Reused by every BenchmarkThroughput_* function.
//
// One PG per benchmark function is acceptable — `go test -bench` runs
// them sequentially. The cold-boot cost (~12-25s) is amortised inside
// the bench harness's setup/teardown not the timed section.
func thBenchSetup(b *testing.B) *pgxpool.Pool {
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
		b.Fatalf("sys migrations: %v", err)
	}

	registry.Reset()
	b.Cleanup(func() { registry.Reset() })
	bench := schemabuilder.NewCollection("bench_items").
		Field("title", schemabuilder.NewText().Required()).
		Field("body", schemabuilder.NewText())
	registry.Register(bench)
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(bench.Spec())); err != nil {
		b.Fatalf("create bench_items: %v", err)
	}

	return pool
}

// BenchmarkThroughput_Insert_Serial — single-goroutine INSERT into
// bench_items via INSERT ... VALUES. Measures the per-statement
// round-trip cost (parse + plan + write + commit + reply).
func BenchmarkThroughput_Insert_Serial(b *testing.B) {
	pool := thBenchSetup(b)
	ctx := context.Background()

	b.ResetTimer()
	t0 := time.Now()
	for i := 0; i < b.N; i++ {
		_, err := pool.Exec(ctx,
			`INSERT INTO bench_items (title, body) VALUES ($1, $2)`,
			fmt.Sprintf("title-%d", i),
			"body content body content body content")
		if err != nil {
			b.Fatalf("insert %d: %v", i, err)
		}
	}
	wall := time.Since(t0)
	b.StopTimer()

	rate := float64(b.N) / wall.Seconds()
	b.ReportMetric(rate, "rows_per_sec")
	b.ReportMetric(float64(wall.Microseconds())/float64(b.N), "mean_µs_per_row")
}

// BenchmarkThroughput_Insert_Concurrent_8 — 8 goroutines pounding the
// pool. Each goroutine does `b.N/8` inserts. This is the realistic
// production-shape throughput (matches default pgxpool size).
func BenchmarkThroughput_Insert_Concurrent_8(b *testing.B) {
	benchConcurrentInsert(b, 8)
}

// BenchmarkThroughput_Insert_Concurrent_32 — pool-saturation curve.
// Useful for spotting per-connection contention; production pool is
// 25 conns by default so 32 goroutines tests the limit.
func BenchmarkThroughput_Insert_Concurrent_32(b *testing.B) {
	benchConcurrentInsert(b, 32)
}

func benchConcurrentInsert(b *testing.B, goroutines int) {
	pool := thBenchSetup(b)
	ctx := context.Background()

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
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_, err := pool.Exec(ctx,
					`INSERT INTO bench_items (title, body) VALUES ($1, $2)`,
					fmt.Sprintf("g%d-i%d", g, i),
					"body content body content body content")
				if err != nil {
					b.Errorf("insert: %v", err)
					return
				}
				completed.Add(1)
			}
		}(g)
	}
	wg.Wait()
	wall := time.Since(t0)
	b.StopTimer()

	n := completed.Load()
	if n == 0 {
		b.Fatal("zero rows inserted")
	}
	rate := float64(n) / wall.Seconds()
	b.ReportMetric(rate, "rows_per_sec")
	b.ReportMetric(float64(goroutines), "goroutines")
	b.ReportMetric(float64(wall.Microseconds())/float64(n), "mean_µs_per_row")
}

// BenchmarkThroughput_CopyFrom — bulk-load via COPY FROM STDIN. The
// upper bound on insert throughput; what `railbase import data` (v1.7.19)
// uses for CSV imports. Per-row latency is meaningless here (the entire
// batch is one round trip); rows_per_sec is the headline metric.
func BenchmarkThroughput_CopyFrom(b *testing.B) {
	pool := thBenchSetup(b)
	ctx := context.Background()

	// Build a CSV-flavoured TSV body once. Each iteration runs ONE COPY
	// FROM with `rowsPerBatch` rows so the per-iteration cost is non-
	// negligible at small b.N.
	const rowsPerBatch = 1000
	var sb strings.Builder
	for r := 0; r < rowsPerBatch; r++ {
		// COPY tsv: title<TAB>body<NL>
		fmt.Fprintf(&sb, "title-%d\tbody content body content body content\n", r)
	}
	body := sb.String()

	b.ResetTimer()
	t0 := time.Now()
	for i := 0; i < b.N; i++ {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			b.Fatal(err)
		}
		_, err = conn.Conn().PgConn().CopyFrom(ctx,
			strings.NewReader(body),
			`COPY bench_items (title, body) FROM STDIN`)
		conn.Release()
		if err != nil {
			b.Fatalf("copy: %v", err)
		}
	}
	wall := time.Since(t0)
	b.StopTimer()

	totalRows := int64(b.N) * rowsPerBatch
	rate := float64(totalRows) / wall.Seconds()
	b.ReportMetric(rate, "rows_per_sec")
	b.ReportMetric(float64(rowsPerBatch), "rows_per_batch")
}
