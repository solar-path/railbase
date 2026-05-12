//go:build embed_pg

// orphan_reaper builtin: end-to-end against real Postgres + tmpfs.
//
// Two-directional sweep (§3.6.13):
//   1. DB orphans — `_files` rows whose owner record is gone.
//   2. FS orphans — on-disk blobs no `_files.storage_key` references.
//
// Run:
//   go test -tags embed_pg -race -count=1 -timeout 180s -run TestOrphanReaper ./internal/jobs/...

package jobs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

// ownerTable is a stand-in collection table the test creates so the
// reaper's table-existence + anti-join paths both have something
// real to query. v1 `_files.collection` references a user-defined
// collection table; tests use a minimal `id UUID PRIMARY KEY`-only
// table because the reaper only cares about `id` for owner-presence.
const ownerTable = "widgets"

// orphanReaperHarness bundles the per-test scaffolding. Each test
// gets its own embedded PG + tempdir so they can run in parallel
// without stepping on each other.
type orphanReaperHarness struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	filesDir  string
	handler   Handler
	log       *slog.Logger
}

// newOrphanReaperHarness spins up embedded PG, applies sys migrations,
// creates the owner stub table, and registers the reaper. Defers the
// teardown via t.Cleanup so tests don't have to manage it manually.
func newOrphanReaperHarness(t *testing.T) *orphanReaperHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	dataDir := t.TempDir()
	filesDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	t.Cleanup(func() { _ = stopPG() })

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Owner stub table. The reaper joins `_files` against this on
	// (id = record_id); a missing row → DB orphan.
	if _, err := pool.Exec(ctx, `CREATE TABLE `+ownerTable+` (id UUID PRIMARY KEY)`); err != nil {
		t.Fatalf("create owner table: %v", err)
	}

	reg := NewRegistry(log)
	RegisterFileBuiltins(reg, pool, filesDir, log)
	h := reg.Lookup("orphan_reaper")
	if h == nil {
		t.Fatal("orphan_reaper not registered")
	}

	return &orphanReaperHarness{
		ctx:      ctx,
		pool:     pool,
		filesDir: filesDir,
		handler:  h,
		log:      log,
	}
}

// seedOwner inserts an owner row in the stub table and returns its id.
func (h *orphanReaperHarness) seedOwner(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := h.pool.Exec(h.ctx, `INSERT INTO `+ownerTable+` (id) VALUES ($1)`, id); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	return id
}

// seedFileRow inserts a `_files` row with the given owner id. Returns
// the file id + the on-disk storage path the reaper would touch.
func (h *orphanReaperHarness) seedFileRow(t *testing.T, ownerID uuid.UUID, contents string) (uuid.UUID, string) {
	t.Helper()
	fileID := uuid.New()
	// Synthesise a sha256-like key so storage_key has the canonical
	// shape (<aa>/<full>/<filename>) the FSDriver would have written.
	// We don't actually hash the contents — the reaper only cares
	// about storage_key as a relative path. Strip dashes from the
	// UUID and pad to 64 hex chars so the result LOOKS like a
	// real sha256 digest.
	rawID := uuid.New().String()
	hexish := ""
	for i := 0; i < len(rawID); i++ {
		if rawID[i] != '-' {
			hexish += string(rawID[i])
		}
	}
	for len(hexish) < 64 {
		hexish += "0"
	}
	digest := hexish[:64]
	storageKey := digest[:2] + "/" + digest + "/file.txt"
	full := filepath.Join(h.filesDir, filepath.FromSlash(storageKey))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir blob: %v", err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	// sha256 column is BYTEA NOT NULL — feed a placeholder.
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO _files
		    (id, collection, record_id, field, filename, mime, size, sha256, storage_key)
		VALUES ($1, $2, $3, 'attachment', 'file.txt', 'text/plain', $4, $5, $6)`,
		fileID, ownerTable, ownerID, int64(len(contents)), []byte(digest)[:32], storageKey,
	); err != nil {
		t.Fatalf("insert _files: %v", err)
	}
	return fileID, full
}

// countFiles returns the count of `_files` rows with the given id.
func (h *orphanReaperHarness) countFiles(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM _files WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count _files: %v", err)
	}
	return n
}

// TestOrphanReaper_DBOrphan_Deleted: a `_files` row pointing at a
// missing owner is purged + its on-disk blob removed.
func TestOrphanReaper_DBOrphan_Deleted(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newOrphanReaperHarness(t)

	// Owner exists momentarily — seed file row, then hard-delete the
	// owner row to simulate a CASCADE-bypass. Records hard-deleted
	// via raw SQL / a migration may leave _files orphans behind.
	ownerID := h.seedOwner(t)
	fileID, blobPath := h.seedFileRow(t, ownerID, "DB-ORPHAN-PAYLOAD")
	if _, err := h.pool.Exec(h.ctx, `DELETE FROM `+ownerTable+` WHERE id = $1`, ownerID); err != nil {
		t.Fatalf("delete owner: %v", err)
	}

	if err := h.handler(h.ctx, &Job{}); err != nil {
		t.Fatalf("orphan_reaper: %v", err)
	}

	if n := h.countFiles(t, fileID); n != 0 {
		t.Errorf("_files row remaining: got %d, want 0", n)
	}
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Errorf("expected blob removed; stat err=%v", err)
	}
}

// TestOrphanReaper_LiveFile_Preserved: an owned `_files` row + its
// blob both survive a sweep.
func TestOrphanReaper_LiveFile_Preserved(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newOrphanReaperHarness(t)

	ownerID := h.seedOwner(t)
	fileID, blobPath := h.seedFileRow(t, ownerID, "LIVE-PAYLOAD")

	if err := h.handler(h.ctx, &Job{}); err != nil {
		t.Fatalf("orphan_reaper: %v", err)
	}

	if n := h.countFiles(t, fileID); n != 1 {
		t.Errorf("_files row: got %d, want 1 (preserved)", n)
	}
	if _, err := os.Stat(blobPath); err != nil {
		t.Errorf("expected blob preserved; stat err=%v", err)
	}
}

// TestOrphanReaper_FSOrphan_Deleted: a stray on-disk file with NO
// `_files` row gets removed.
func TestOrphanReaper_FSOrphan_Deleted(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newOrphanReaperHarness(t)

	// Stage 1: a live (owned) row so the reaper has SOMETHING in
	// the storage_key set — defends against a future code-path that
	// short-circuits the FS walk when validPaths is empty.
	ownerID := h.seedOwner(t)
	_, livePath := h.seedFileRow(t, ownerID, "LIVE-WITH-STRAY")

	// Stage 2: stray file at a plausible FSDriver-shaped path but
	// not referenced anywhere in `_files`. Simulates an aborted
	// multipart upload that wrote the blob then died before the
	// metadata row landed.
	strayDir := filepath.Join(h.filesDir, "de", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err := os.MkdirAll(strayDir, 0o755); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}
	strayPath := filepath.Join(strayDir, "stray.bin")
	if err := os.WriteFile(strayPath, []byte("ABORTED-UPLOAD"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	if err := h.handler(h.ctx, &Job{}); err != nil {
		t.Fatalf("orphan_reaper: %v", err)
	}

	if _, err := os.Stat(strayPath); !os.IsNotExist(err) {
		t.Errorf("expected stray removed; stat err=%v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Errorf("expected live blob preserved; stat err=%v", err)
	}
}

// TestOrphanReaper_EmptyState_NoOp: empty `_files` + empty filesDir
// → handler is a no-op, returns nil.
func TestOrphanReaper_EmptyState_NoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newOrphanReaperHarness(t)
	if err := h.handler(h.ctx, &Job{}); err != nil {
		t.Fatalf("orphan_reaper empty state: %v", err)
	}
	// _files is empty; the filesDir is a fresh tempdir with nothing
	// in it. Both invariants must hold post-sweep.
	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM _files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("_files count after empty no-op: got %d, want 0", n)
	}
}

// TestOrphanReaper_DefaultSchedule pins the weekly cron row so a
// future careless edit can't silently drop or re-time it.
func TestOrphanReaper_DefaultSchedule(t *testing.T) {
	var found *DefaultSchedule
	for i, s := range DefaultSchedules() {
		if s.Kind == "orphan_reaper" {
			found = &DefaultSchedules()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("orphan_reaper missing from DefaultSchedules()")
	}
	if found.Name != "orphan_reaper" {
		t.Errorf("schedule Name: got %q, want %q", found.Name, "orphan_reaper")
	}
	if found.Expression != "0 5 * * 0" {
		t.Errorf("schedule Expression: got %q, want weekly Sunday 05:00", found.Expression)
	}
}
