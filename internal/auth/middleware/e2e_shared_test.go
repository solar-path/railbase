//go:build embed_pg

// v1.7.37 — shared embedded-PG TestMain for the auth/middleware
// e2e tests. Before this file, both `apitoken_e2e_test.go` (1 test)
// and `query_token_e2e_test.go` (5 tests) booted their OWN embedded
// Postgres on the fixed port 54329 — 6 cold PG starts totalling
// ~270s, comfortably overflowing the default 240s -timeout. Plus
// they would collide on port 54329 if go test ever ran them in
// parallel (it doesn't, by default — but the timing was already
// fragile and re-running on a busy machine surfaced spurious
// "port already listening" errors).
//
// Now: one PG boot per test PROCESS via TestMain; both files share
// the same `sharedPool`. Per-test row isolation is preserved by
// (a) fresh user/session/api-token UUIDs per test and (b) any
// table-truncate helpers each test can layer on if they need to
// observe row counts in isolation. The `runTests(m)` wrapper
// follows the v1.7.35d pattern — `os.Exit` bypasses defers in its
// own frame, so wrapping `m.Run()` is required for the stopPG +
// pool.Close + RemoveAll cleanups to actually fire.

package middleware

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

var (
	sharedPool *pgxpool.Pool
	sharedLog  *slog.Logger
	sharedCtx  context.Context
)

func TestMain(m *testing.M) {
	// Wrap in runTests so the deferred stopPG / pool.Close /
	// RemoveAll actually fire before os.Exit. See v1.7.35d for the
	// rationale — os.Exit bypasses defers in its own frame, so
	// without this layering the embedded postgres leaks past the
	// test run and binds port 54329 forever.
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir, err := os.MkdirTemp("", "railbase-authmw-shared-pg-*")
	if err != nil {
		panic("authmw tests: tempdir: " + err.Error())
	}
	defer os.RemoveAll(dataDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		panic("authmw tests: embedded pg: " + err.Error())
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("authmw tests: pgxpool: " + err.Error())
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		panic("authmw tests: migrate: " + err.Error())
	}

	sharedPool = pool
	sharedLog = log
	sharedCtx = ctx

	return m.Run()
}
