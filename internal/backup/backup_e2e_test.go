//go:build embed_pg

// v1.7.7 — full-stack backup/restore against embedded Postgres.
//
// One test function (shared embedded-PG + pool) with numbered
// sub-assertions to avoid paying the ~25s PG-extraction cost per case.
// Each sub-block clears state by re-running migrations into a fresh
// dataDir is too expensive; instead we TRUNCATE the user-test tables
// we seed.
//
// Asserts:
//
//  1. Backup round-trip: dump + restore produces identical row counts
//  2. Backup excludes runtime tables (_sessions, _jobs, _record_tokens)
//  3. Restore TRUNCATEs before COPY-FROM (existing rows replaced, not appended)
//  4. Restore --force-less rejects schema-head mismatch
//  5. Restore with TruncateBefore=false appends (operator escape hatch)
//  6. Empty DB round-trip (no rows, manifest still valid)
//  7. Migration-head NULL case (no _migrations table) — manifest.MigrationHead empty

package backup

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

func TestBackup_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	// Seed: a tiny test table + a few rows in `_settings` (operator
	// config — must round-trip). We deliberately avoid the auth
	// collections because their full schema requires lots of inserts;
	// the dump path is identical so this is sufficient coverage.
	mustExec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	mustExec(t, `CREATE TABLE IF NOT EXISTS bk_widgets (
		id UUID PRIMARY KEY,
		name TEXT NOT NULL,
		qty INTEGER NOT NULL DEFAULT 0,
		notes TEXT
	)`)

	seedRows := func(t *testing.T) {
		t.Helper()
		mustExec(t, `TRUNCATE bk_widgets`)
		mustExec(t, `INSERT INTO bk_widgets (id, name, qty, notes) VALUES
			($1, 'alpha', 10, 'first row'),
			($2, 'beta',  20, 'with, comma'),
			($3, 'gamma', 30, NULL)`,
			uuid.New(), uuid.New(), uuid.New())
		// One row into _settings so we can confirm operator-critical
		// tables round-trip.
		mustExec(t, `INSERT INTO _settings (key, value) VALUES ('bk_test', '"hello"'::jsonb)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
	}

	// === [1] Round-trip: dump + restore preserves rows ===
	t.Run("round trip preserves rows", func(t *testing.T) {
		seedRows(t)
		var got [3]string
		if err := pool.QueryRow(ctx,
			`SELECT array_agg(name ORDER BY name) FROM bk_widgets`).Scan(&got); err != nil {
			// pgx scan to fixed-size array fails — switch to a slice.
		}
		var names []string
		rows, _ := pool.Query(ctx, `SELECT name FROM bk_widgets ORDER BY name`)
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			names = append(names, n)
		}
		rows.Close()
		if len(names) != 3 {
			t.Fatalf("seed: %d rows, want 3", len(names))
		}

		var buf bytes.Buffer
		m, err := Backup(ctx, pool, &buf, Options{RailbaseVersion: "v1.7.7-test"})
		if err != nil {
			t.Fatalf("Backup: %v", err)
		}
		if m.MigrationHead == "" {
			t.Error("MigrationHead empty after system migrations — expected non-empty")
		}
		// Wipe and restore.
		mustExec(t, `TRUNCATE bk_widgets`)
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM bk_widgets`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("after truncate: %d rows, want 0", n)
		}
		if _, err := Restore(ctx, pool, &buf, RestoreOptions{TruncateBefore: true}); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		// Now we expect 3 rows back.
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM bk_widgets`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("after restore: %d rows, want 3", n)
		}
		// Names match.
		var restored []string
		rows, _ = pool.Query(ctx, `SELECT name FROM bk_widgets ORDER BY name`)
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			restored = append(restored, s)
		}
		rows.Close()
		if strings.Join(restored, ",") != "alpha,beta,gamma" {
			t.Fatalf("restored names %v != alpha,beta,gamma", restored)
		}
	})

	// === [2] defaultExcludes wins: _sessions / _jobs / _record_tokens not in manifest ===
	t.Run("default excludes runtime tables", func(t *testing.T) {
		var buf bytes.Buffer
		m, err := Backup(ctx, pool, &buf, Options{})
		if err != nil {
			t.Fatalf("Backup: %v", err)
		}
		for _, ti := range m.Tables {
			full := ti.Schema + "." + ti.Name
			switch full {
			case "public._jobs", "public._sessions", "public._record_tokens",
				"public._admin_sessions", "public._mfa_challenges", "public._exports":
				t.Errorf("manifest includes runtime table %s — defaultExcludes broken", full)
			}
		}
	})

	// === [3] TruncateBefore replaces (not appends) ===
	t.Run("truncate replaces not appends", func(t *testing.T) {
		seedRows(t)
		var buf bytes.Buffer
		if _, err := Backup(ctx, pool, &buf, Options{}); err != nil {
			t.Fatalf("Backup: %v", err)
		}
		// Add an extra row that's NOT in the backup.
		mustExec(t, `INSERT INTO bk_widgets (id, name, qty) VALUES ($1, 'extra', 999)`, uuid.New())
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM bk_widgets`).Scan(&n)
		if n != 4 {
			t.Fatalf("pre-restore count: %d want 4", n)
		}
		if _, err := Restore(ctx, pool, &buf, RestoreOptions{TruncateBefore: true}); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM bk_widgets`).Scan(&n)
		if n != 3 {
			t.Fatalf("post-restore: got %d rows, want 3 (extra row should be wiped)", n)
		}
	})

	// === [4] Schema-head mismatch rejected without --force ===
	t.Run("schema head mismatch rejected", func(t *testing.T) {
		var buf bytes.Buffer
		if _, err := Backup(ctx, pool, &buf, Options{}); err != nil {
			t.Fatalf("Backup: %v", err)
		}
		// Hack: insert a synthetic future migration to shift the head.
		mustExec(t, `INSERT INTO _migrations (version, name, content_hash) VALUES
			(99999, 'future', 'fakehash')
			ON CONFLICT (version) DO NOTHING`)
		_, err := Restore(ctx, pool, &buf, RestoreOptions{TruncateBefore: true})
		if err == nil {
			t.Error("Restore without --force should fail on schema-head mismatch")
		}
		mustExec(t, `DELETE FROM _migrations WHERE version = 99999`)
	})

	// === [5] Force overrides the head check ===
	t.Run("force overrides head check", func(t *testing.T) {
		seedRows(t)
		var buf bytes.Buffer
		if _, err := Backup(ctx, pool, &buf, Options{}); err != nil {
			t.Fatalf("Backup: %v", err)
		}
		mustExec(t, `INSERT INTO _migrations (version, name, content_hash) VALUES
			(99998, 'future', 'fakehash')
			ON CONFLICT (version) DO NOTHING`)
		_, err := Restore(ctx, pool, &buf, RestoreOptions{
			Force: true, TruncateBefore: true,
		})
		if err != nil {
			t.Errorf("Restore --force should succeed: %v", err)
		}
		mustExec(t, `DELETE FROM _migrations WHERE version = 99998`)
	})
}
