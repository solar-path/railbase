//go:build embed_pg

package migrate

// v1.7.42 — boot-time foreign-database invariant. Verifies that:
//
//  1. Apply against a fresh DB → succeeds (bootstrap path).
//  2. Apply against the SAME DB a second time → succeeds (marker
//     present, treated as existing Railbase).
//  3. Apply against a DB that has a non-Railbase table but no
//     `_migrations` marker → returns ErrForeignDatabase.
//  4. With AllowForeignDatabase=true, the same scenario succeeds
//     (operator escape hatch).
//
// We spin up embedded postgres in TestMain and share one pool across
// all four tests; each test runs in its OWN logical DB (CREATE DATABASE
// off the admin connection) so they don't interfere via the shared
// `public` schema.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
)

var (
	sharedBaseDSN string
	sharedAdminP  *pgxpool.Pool
	sharedCtx     context.Context
)

func TestMain(m *testing.M) {
	// Same os.Exit-defers leak fix pattern as v1.7.35d notifications +
	// mailer: wrap m.Run() in a helper so the embedded-pg cleanup
	// defers actually fire before the process exits.
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir, err := os.MkdirTemp("", "railbase-migrate-foreign-db-*")
	if err != nil {
		panic("migrate test: tempdir: " + err.Error())
	}
	defer os.RemoveAll(dataDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		panic("migrate test: embedded pg: " + err.Error())
	}
	defer func() { _ = stopPG() }()

	// Admin pool stays on the base DB so we can CREATE/DROP per-test.
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("migrate test: pgxpool: " + err.Error())
	}
	defer admin.Close()

	sharedBaseDSN = dsn
	sharedAdminP = admin
	sharedCtx = ctx
	return m.Run()
}

// freshDB creates a uniquely-named database and returns a DSN pointing
// at it. The caller is responsible for cleanup via t.Cleanup (we
// register the drop here on behalf of the test).
func freshDB(t *testing.T, name string) string {
	t.Helper()
	dbName := "rb_migrate_test_" + name + "_" + randomSuffix()
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := sharedAdminP.Exec(sharedCtx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("create database %s: %v", dbName, err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) terminates any leftover sessions from the test's
		// own per-test pool so the DROP doesn't block.
		_, _ = sharedAdminP.Exec(context.Background(),
			"DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
	})

	// Build a DSN that points at the new database. The base DSN's
	// query string (host=/tmp, sslmode=disable) is preserved; only
	// the path component changes.
	return swapDB(t, sharedBaseDSN, dbName)
}

// swapDB rewrites the database path component of dsn.
func swapDB(t *testing.T, dsn, newDB string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.Database = newDB
	// Rebuild a URL-form DSN. pgx exposes the connection config but
	// not a serializer; for our tests the base URL form preserves the
	// socket host via query string, so we slice/rebuild manually.
	// Simpler: replace the trailing `/<old>?` chunk.
	return replaceDBPath(dsn, newDB)
}

// replaceDBPath swaps the /<db>? chunk in a postgres:// URL. The
// embedded driver's DSN always has a trailing query string, so the `?`
// anchor is reliable.
func replaceDBPath(dsn, newDB string) string {
	q := strings.Index(dsn, "?")
	if q < 0 {
		// No query string: replace from the last `/` to end.
		slash := strings.LastIndex(dsn, "/")
		if slash < 0 {
			return dsn // can't rewrite, return as-is and let the test fail at connect
		}
		return dsn[:slash+1] + newDB
	}
	slash := strings.LastIndex(dsn[:q], "/")
	if slash < 0 {
		return dsn
	}
	return dsn[:slash+1] + newDB + dsn[q:]
}

// randomSuffix yields a short unique suffix for DB names. Same approach
// as setup_db_embed_test.go::randomSuffix — nanosecond-derived hex.
func randomSuffix() string {
	ns := time.Now().UnixNano()
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[7-i] = hex[ns&0xf]
		ns >>= 4
	}
	return string(out)
}

// poolFor opens a per-test pool against the given DSN and registers
// the close as cleanup. We don't share a pool across tests because
// each test runs in its own DB and the connection string differs.
func poolFor(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(sharedCtx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// newRunner builds a Runner for the given pool with a discard logger.
func newRunner(p *pgxpool.Pool, allowForeign bool) *Runner {
	return &Runner{
		Pool:                 p,
		Log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
		AllowForeignDatabase: allowForeign,
	}
}

// dummyMigration is the smallest valid migration for the boot-invariant
// tests: a single SQL statement that doesn't touch the marker table.
// The bootstrap path inserts `_migrations` on its own.
func dummyMigration() Migration {
	sql := `CREATE TABLE _railbase_test_dummy (id BIGINT PRIMARY KEY)`
	return Migration{
		Version: 1,
		Name:    "dummy",
		SQL:     sql,
		Hash:    "0000000000000000000000000000000000000000000000000000000000000001",
	}
}

// TestForeignDB_FreshDB_BootstrapSucceeds — empty DB, no marker, no
// other tables. Apply must succeed and write the marker.
func TestForeignDB_FreshDB_BootstrapSucceeds(t *testing.T) {
	dsn := freshDB(t, "fresh")
	p := poolFor(t, dsn)
	r := newRunner(p, false)

	if err := r.Apply(sharedCtx, []Migration{dummyMigration()}); err != nil {
		t.Fatalf("Apply on fresh DB: want nil, got %v", err)
	}

	// Marker should now exist.
	var present bool
	if err := p.QueryRow(sharedCtx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_tables
		  WHERE schemaname = 'public' AND tablename = '_migrations'
		)
	`).Scan(&present); err != nil {
		t.Fatalf("query marker: %v", err)
	}
	if !present {
		t.Errorf("_migrations marker: want present after first Apply, got absent")
	}
}

// TestForeignDB_ExistingRailbase_BootstrapSucceeds — after the first
// Apply, a SECOND Apply against the same DB should also succeed (the
// marker is now present and the precheck waves it through).
func TestForeignDB_ExistingRailbase_BootstrapSucceeds(t *testing.T) {
	dsn := freshDB(t, "existing")
	p := poolFor(t, dsn)
	r := newRunner(p, false)

	if err := r.Apply(sharedCtx, []Migration{dummyMigration()}); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// Re-applying the same migration set: nothing to do, but the
	// precheck must still wave it through.
	if err := r.Apply(sharedCtx, []Migration{dummyMigration()}); err != nil {
		t.Fatalf("second Apply on existing-Railbase DB: want nil, got %v", err)
	}
}

// TestForeignDB_ForeignTables_BootstrapRefused — DB has tables but no
// `_migrations` marker. Apply must return ErrForeignDatabase WITHOUT
// creating the marker.
func TestForeignDB_ForeignTables_BootstrapRefused(t *testing.T) {
	dsn := freshDB(t, "foreign")
	p := poolFor(t, dsn)

	// Seed a foreign-app table directly on the pool.
	if _, err := p.Exec(sharedCtx, `CREATE TABLE foreign_app_users (id SERIAL PRIMARY KEY)`); err != nil {
		t.Fatalf("create foreign table: %v", err)
	}

	r := newRunner(p, false)
	err := r.Apply(sharedCtx, []Migration{dummyMigration()})
	if err == nil {
		t.Fatalf("Apply on foreign DB: want ErrForeignDatabase, got nil")
	}
	if !errors.Is(err, ErrForeignDatabase) {
		t.Fatalf("Apply error: want ErrForeignDatabase, got %v", err)
	}

	// Marker must NOT have been written — precheck fires BEFORE bootstrap.
	var present bool
	if err := p.QueryRow(sharedCtx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_tables
		  WHERE schemaname = 'public' AND tablename = '_migrations'
		)
	`).Scan(&present); err != nil {
		t.Fatalf("query marker: %v", err)
	}
	if present {
		t.Errorf("_migrations marker: want absent after refused Apply, got present")
	}
}

// TestForeignDB_ForeignTables_AllowOverride — same setup but with
// AllowForeignDatabase=true (the RAILBASE_FORCE_INIT escape hatch).
// Apply must succeed and write the marker alongside the foreign table.
func TestForeignDB_ForeignTables_AllowOverride(t *testing.T) {
	dsn := freshDB(t, "override")
	p := poolFor(t, dsn)

	if _, err := p.Exec(sharedCtx, `CREATE TABLE foreign_app_users (id SERIAL PRIMARY KEY)`); err != nil {
		t.Fatalf("create foreign table: %v", err)
	}

	r := newRunner(p, true) // AllowForeignDatabase=true
	if err := r.Apply(sharedCtx, []Migration{dummyMigration()}); err != nil {
		t.Fatalf("Apply with override: want nil, got %v", err)
	}

	// Marker should now exist next to the foreign table.
	var present bool
	if err := p.QueryRow(sharedCtx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_tables
		  WHERE schemaname = 'public' AND tablename = '_migrations'
		)
	`).Scan(&present); err != nil {
		t.Fatalf("query marker: %v", err)
	}
	if !present {
		t.Errorf("_migrations marker: want present after override Apply, got absent")
	}
}
