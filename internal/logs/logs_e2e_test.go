//go:build embed_pg

// v1.7.6 — full-stack logs e2e against embedded Postgres.
//
// One test function (shared embedded-PG + pool) with numbered
// sub-assertions to avoid paying the ~25s PG-extraction cost per
// case. Each sub-block TRUNCATEs `_logs` to start clean.
//
// Asserts:
//
//  1. Sink.Handle → background flush → row appears in `_logs`
//  2. Multi (stdout + sink): both branches see the record
//  3. Sink.Close() drains pending records before returning
//  4. Store.List filters by level (>= ranking)
//  5. Store.List filters by Since window
//  6. Store.List filters by RequestID
//  7. Store.List filters by Search (ILIKE substring)
//  8. Store.Count matches List+filter cardinality
//  9. Attrs JSONB round-trips through encode + decode

package logs

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

// freshSink builds a new Sink against the shared pool and returns it
// plus a slog.Logger wrapping it. Each sub-test gets its own Sink so
// flusher state doesn't leak between cases.
func freshSink(t *testing.T, pool *pgxpool.Pool, cfg Config) (*Sink, *slog.Logger) {
	t.Helper()
	sink := NewSink(pool, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(ctx)
	})
	return sink, slog.New(sink)
}

// truncate clears the `_logs` table so the next sub-test starts clean.
func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `TRUNCATE _logs`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// waitForLogs polls the store until `want` rows appear or deadline
// elapses. Returns the latest snapshot.
func waitForLogs(t *testing.T, store *Store, want int, deadline time.Duration) []*Record {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		recs, err := store.List(context.Background(), ListFilter{Limit: 1000})
		if err == nil && len(recs) >= want {
			return recs
		}
		time.Sleep(50 * time.Millisecond)
	}
	recs, _ := store.List(context.Background(), ListFilter{Limit: 1000})
	t.Fatalf("waitForLogs: got %d, want %d after %v", len(recs), want, deadline)
	return recs
}

func TestLogs_E2E(t *testing.T) {
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

	// === [1] Sink.Handle → background flush → row appears ===
	t.Run("flush persists row", func(t *testing.T) {
		truncate(t, pool)
		_, logger := freshSink(t, pool, Config{FlushInterval: 100 * time.Millisecond})
		logger.Info("hello-e2e", "tenant", "acme", "count", 7)
		recs := waitForLogs(t, store, 1, 3*time.Second)
		if recs[0].Message != "hello-e2e" {
			t.Fatalf("message=%q", recs[0].Message)
		}
		if recs[0].Level != "info" {
			t.Fatalf("level=%q want info", recs[0].Level)
		}
		if got, _ := recs[0].Attrs["tenant"].(string); got != "acme" {
			t.Fatalf("attrs.tenant=%v want acme", recs[0].Attrs["tenant"])
		}
	})

	// === [2] Multi fans out to stdout AND sink ===
	t.Run("multi fans out", func(t *testing.T) {
		truncate(t, pool)
		var buf bytes.Buffer
		stdoutH := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
		sink := NewSink(pool, Config{FlushInterval: 100 * time.Millisecond})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = sink.Close(ctx)
		})
		logger := slog.New(NewMulti(stdoutH, sink))
		logger.Info("multi-line")
		recs := waitForLogs(t, store, 1, 3*time.Second)
		if recs[0].Message != "multi-line" {
			t.Fatalf("sink got %q", recs[0].Message)
		}
		if !strings.Contains(buf.String(), "multi-line") {
			t.Fatalf("stdout buf missing message; got %q", buf.String())
		}
	})

	// === [3] Close drains pending records ===
	t.Run("close drains", func(t *testing.T) {
		truncate(t, pool)
		// Hour-long flush so only Close can trigger persistence.
		sink := NewSink(pool, Config{FlushInterval: 1 * time.Hour})
		logger := slog.New(sink)
		logger.Info("first")
		logger.Info("second")
		logger.Info("third")
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		recs, _ := store.List(context.Background(), ListFilter{Limit: 100})
		if len(recs) != 3 {
			t.Fatalf("Close didn't drain: got %d, want 3", len(recs))
		}
	})

	// === [4] Filter by level (>= ranking) ===
	t.Run("filter by level", func(t *testing.T) {
		truncate(t, pool)
		sink, logger := freshSink(t, pool, Config{FlushInterval: 50 * time.Millisecond, MinLevel: slog.LevelDebug})
		logger.Debug("debugmsg")
		logger.Info("infomsg")
		logger.Warn("warnmsg")
		logger.Error("errmsg")
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		recs, err := store.List(context.Background(), ListFilter{Level: "warn", Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 2 {
			t.Fatalf("level>=warn got %d, want 2", len(recs))
		}
		for _, r := range recs {
			if r.Level != "warn" && r.Level != "error" {
				t.Fatalf("unexpected level: %q", r.Level)
			}
		}
	})

	// === [5] Filter by Since/Until window ===
	t.Run("filter by since", func(t *testing.T) {
		truncate(t, pool)
		sink, logger := freshSink(t, pool, Config{FlushInterval: 50 * time.Millisecond})
		logger.Info("a")
		time.Sleep(50 * time.Millisecond)
		mid := time.Now()
		time.Sleep(50 * time.Millisecond)
		logger.Info("b")
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		recs, err := store.List(context.Background(), ListFilter{Since: mid, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1 || recs[0].Message != "b" {
			t.Fatalf("Since filter wrong: %+v", recs)
		}
	})

	// === [6] Filter by RequestID ===
	t.Run("filter by request_id", func(t *testing.T) {
		truncate(t, pool)
		sink, logger := freshSink(t, pool, Config{FlushInterval: 50 * time.Millisecond})
		ctxA := WithRequestID(context.Background(), "rid-A")
		ctxB := WithRequestID(context.Background(), "rid-B")
		logger.InfoContext(ctxA, "from-A")
		logger.InfoContext(ctxB, "from-B")
		logger.Info("no-rid")
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		recs, _ := store.List(context.Background(), ListFilter{RequestID: "rid-A", Limit: 10})
		if len(recs) != 1 || recs[0].Message != "from-A" {
			t.Fatalf("RequestID filter wrong: %+v", recs)
		}
	})

	// === [7] Filter by Search (ILIKE substring, case-insensitive) ===
	t.Run("filter by search", func(t *testing.T) {
		truncate(t, pool)
		sink, logger := freshSink(t, pool, Config{FlushInterval: 50 * time.Millisecond})
		logger.Info("payment processed successfully")
		logger.Info("user logged in")
		logger.Info("payment failed: insufficient funds")
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		recs, _ := store.List(context.Background(), ListFilter{Search: "PAYMENT", Limit: 10})
		if len(recs) != 2 {
			t.Fatalf("Search ILIKE got %d, want 2", len(recs))
		}
	})

	// === [8] Count matches List+filter cardinality ===
	t.Run("count matches list", func(t *testing.T) {
		truncate(t, pool)
		sink, logger := freshSink(t, pool, Config{FlushInterval: 50 * time.Millisecond})
		for i := 0; i < 7; i++ {
			logger.Info("burst")
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		n, err := store.Count(context.Background(), ListFilter{})
		if err != nil {
			t.Fatalf("Count: %v", err)
		}
		if n != 7 {
			t.Fatalf("Count=%d want 7", n)
		}
	})

	// === [9] Attrs JSONB round-trip preserves typed values ===
	t.Run("attrs round trip", func(t *testing.T) {
		truncate(t, pool)
		sink, logger := freshSink(t, pool, Config{FlushInterval: 50 * time.Millisecond})
		uid := uuid.New()
		logger.Info("structured",
			"user_id", uid.String(),
			"amount", 42.5,
			"ok", true,
		)
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		recs, _ := store.List(context.Background(), ListFilter{Limit: 10})
		if len(recs) != 1 {
			t.Fatalf("rows=%d want 1", len(recs))
		}
		a := recs[0].Attrs
		if got, _ := a["user_id"].(string); got != uid.String() {
			t.Fatalf("attrs.user_id=%v", a["user_id"])
		}
		if got, _ := a["amount"].(float64); got != 42.5 {
			t.Fatalf("attrs.amount=%v", a["amount"])
		}
		if got, _ := a["ok"].(bool); got != true {
			t.Fatalf("attrs.ok=%v", a["ok"])
		}
	})
}
