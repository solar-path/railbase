//go:build embed_pg

// cleanup_exports builtin: end-to-end against real Postgres + tmpfs.
// Spins up the embedded server, applies sys migrations, seeds rows
// in _exports with concrete on-disk files, invokes the handler, and
// asserts both the row set + filesystem converged correctly.
//
// Run:
//   go test -tags embed_pg -race -run TestCleanupExports ./internal/jobs/...

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

func TestCleanupExports(t *testing.T) {
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

	exportsDir := filepath.Join(dataDir, "exports")
	if err := os.MkdirAll(exportsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// writeFile creates a small file on disk and returns its absolute path + size.
	writeFile := func(name, content string) (string, int64) {
		p := filepath.Join(exportsDir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p, int64(len(content))
	}

	// Seed fixtures. Each row's `id` mirrors the schema CHECK (UUID PK).
	type fixture struct {
		id          uuid.UUID
		status      string
		format      string
		filePath    string // empty == NULL
		fileSize    int64
		expiresPast bool // true → expires_at = now() - 1h, false → see expiresNull
		expiresNull bool // overrides expiresPast: NULL expires_at
		fileOnDisk  bool // true → create the file
	}
	mk := func(f fixture) fixture {
		f.id = uuid.New()
		return f
	}

	expiredCompletedPath, expiredCompletedSize := writeFile("expired-completed.xlsx", "EXPIRED-DATA-1")
	expiredFailedPath, expiredFailedSize := writeFile("expired-failed.pdf", "OOPS")
	expiredCancelledPath, expiredCancelledSize := writeFile("expired-cancelled.xlsx", "CANCEL")
	freshCompletedPath, freshCompletedSize := writeFile("fresh-completed.xlsx", "STILL-VALID")
	runningPath, runningSize := writeFile("running.xlsx", "IN-PROGRESS")
	missingFilePath := filepath.Join(exportsDir, "ghost.xlsx") // deliberately not created
	const missingFileSize int64 = 999

	fixtures := []fixture{
		mk(fixture{status: "completed", format: "xlsx", filePath: expiredCompletedPath, fileSize: expiredCompletedSize, expiresPast: true, fileOnDisk: true}),
		mk(fixture{status: "failed", format: "pdf", filePath: expiredFailedPath, fileSize: expiredFailedSize, expiresPast: true, fileOnDisk: true}),
		mk(fixture{status: "cancelled", format: "xlsx", filePath: expiredCancelledPath, fileSize: expiredCancelledSize, expiresPast: true, fileOnDisk: true}),
		mk(fixture{status: "completed", format: "xlsx", filePath: missingFilePath, fileSize: missingFileSize, expiresPast: true, fileOnDisk: false}),
		mk(fixture{status: "completed", format: "xlsx", filePath: freshCompletedPath, fileSize: freshCompletedSize, expiresPast: false, fileOnDisk: true}),
		mk(fixture{status: "running", format: "xlsx", filePath: runningPath, fileSize: runningSize, expiresPast: true, fileOnDisk: true}),
		mk(fixture{status: "pending", format: "pdf", expiresPast: true}),
		mk(fixture{status: "completed", format: "xlsx", expiresNull: true}),
	}
	for _, f := range fixtures {
		var expiresAt any
		switch {
		case f.expiresNull:
			expiresAt = nil
		case f.expiresPast:
			expiresAt = time.Now().Add(-1 * time.Hour)
		default:
			expiresAt = time.Now().Add(24 * time.Hour)
		}
		var filePath any
		if f.filePath != "" {
			filePath = f.filePath
		}
		var fileSize any
		if f.fileSize != 0 {
			fileSize = f.fileSize
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO _exports (id, format, collection, status, file_path, file_size, expires_at)
			VALUES ($1, $2, 'widgets', $3, $4, $5, $6)`,
			f.id, f.format, f.status, filePath, fileSize, expiresAt)
		if err != nil {
			t.Fatalf("seed %s/%s: %v", f.status, f.id, err)
		}
	}

	reg := NewRegistry(log)
	RegisterBuiltins(reg, pool, log)
	h := reg.Lookup("cleanup_exports")
	if h == nil {
		t.Fatal("cleanup_exports not registered")
	}

	if err := h(ctx, &Job{}); err != nil {
		t.Fatalf("cleanup_exports: %v", err)
	}

	// --- Assertions: rows ---

	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM _exports`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	// Survivors: fresh completed, running, pending, NULL-expires completed = 4.
	if remaining != 4 {
		t.Errorf("rows remaining: got %d want 4", remaining)
	}

	// The four expired completed/failed/cancelled rows must be gone.
	for i, f := range fixtures[:4] {
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM _exports WHERE id = $1`, f.id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Errorf("expired fixture[%d] (%s) still present", i, f.status)
		}
	}
	// The four survivor rows must remain.
	for i, f := range fixtures[4:] {
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM _exports WHERE id = $1`, f.id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Errorf("survivor fixture[%d] (%s, expiresNull=%v, expiresPast=%v) was deleted",
				i+4, f.status, f.expiresNull, f.expiresPast)
		}
	}

	// --- Assertions: files on disk ---

	mustBeMissing := []string{expiredCompletedPath, expiredFailedPath, expiredCancelledPath}
	for _, p := range mustBeMissing {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s removed; stat err=%v", p, err)
		}
	}
	mustStillExist := []string{freshCompletedPath, runningPath}
	for _, p := range mustStillExist {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to remain; stat err=%v", p, err)
		}
	}

	// --- Idempotence: running again with nothing eligible is a no-op. ---
	if err := h(ctx, &Job{}); err != nil {
		t.Fatalf("cleanup_exports idempotent run: %v", err)
	}
	var afterIdempotent int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM _exports`).Scan(&afterIdempotent); err != nil {
		t.Fatal(err)
	}
	if afterIdempotent != 4 {
		t.Errorf("after idempotent run: got %d want 4", afterIdempotent)
	}

	t.Logf("cleanup_exports: 4 rows deleted, 3 files removed, 1 missing file tolerated, 4 rows preserved")
}

// TestCleanupExportsDefaultSchedule pins the schedule entry so a
// future careless edit can't silently drop it.
func TestCleanupExportsDefaultSchedule(t *testing.T) {
	var found *DefaultSchedule
	for i, s := range DefaultSchedules() {
		if s.Kind == "cleanup_exports" {
			found = &DefaultSchedules()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("cleanup_exports missing from DefaultSchedules()")
	}
	if found.Name != "cleanup_exports" {
		t.Errorf("name: got %q want cleanup_exports", found.Name)
	}
	if found.Expression != "0 4 * * *" {
		t.Errorf("expression: got %q want \"0 4 * * *\"", found.Expression)
	}
}

// TestCleanupAuditArchiveDefaultSchedule pins the v1.7.13 audit-archive
// cron entry so a future careless edit can't silently drop it.
func TestCleanupAuditArchiveDefaultSchedule(t *testing.T) {
	var found *DefaultSchedule
	for i, s := range DefaultSchedules() {
		if s.Kind == "cleanup_audit_archive" {
			found = &DefaultSchedules()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("cleanup_audit_archive missing from DefaultSchedules()")
	}
	if found.Name != "cleanup_audit_archive" {
		t.Errorf("name: got %q want cleanup_audit_archive", found.Name)
	}
	if found.Expression != "30 4 * * *" {
		t.Errorf("expression: got %q want \"30 4 * * *\"", found.Expression)
	}
}
