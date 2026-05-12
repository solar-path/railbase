package adminapi

// Handler-shape tests for the v1.7.7 §3.11 backups admin surface.
//
// These tests deliberately avoid spinning up embed_pg — the List
// handler is pure filesystem and the Create handler's database side
// is exercised exhaustively by internal/backup/backup_e2e_test.go.
// What we pin here is the HTTP envelope: empty-dir → 200 with
// items:[], non-empty → sorted newest-first, nil pool on POST →
// 503 envelope (not a panic).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withDataDir points dataDirFromEnv at a temp directory for the
// duration of one test. The CLI's loadConfigOnly resolves the env
// to an absolute path, so we set it to the already-absolute t.TempDir
// to keep the paths stable across the test.
func withDataDir(t *testing.T, dir string) {
	t.Helper()
	prev, hadPrev := os.LookupEnv("RAILBASE_DATA_DIR")
	if err := os.Setenv("RAILBASE_DATA_DIR", dir); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("RAILBASE_DATA_DIR", prev)
		} else {
			_ = os.Unsetenv("RAILBASE_DATA_DIR")
		}
	})
}

// TestBackupsListHandlerEmptyDir — a fresh deploy has no
// <DataDir>/backups/ directory at all. The handler must respond with
// the empty envelope, not 404; the UI distinguishes "no backups yet"
// from "endpoint broken" via that contract.
func TestBackupsListHandlerEmptyDir(t *testing.T) {
	dir := t.TempDir()
	withDataDir(t, dir)
	// Note: deliberately do NOT mkdir <dir>/backups — we want the
	// IsNotExist branch.

	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/backups", nil)
	rec := httptest.NewRecorder()
	d.backupsListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []backupItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items: want [], got %d entries: %s", len(resp.Items), rec.Body.String())
	}
	// Pin the JSON shape too — `items` MUST serialise as [] not null
	// so the frontend's .map() doesn't crash on first load.
	if !contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("items must serialise as [] (got %s)", rec.Body.String())
	}
}

// TestBackupsListHandlerSortedNewestFirst — populate the directory
// with three archives at staggered mtimes, plus a non-tar.gz file
// that must be filtered out, plus a directory entry that must also
// be skipped. Verify the response is sorted strictly newest-first.
func TestBackupsListHandlerSortedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	backupsDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	withDataDir(t, dir)

	now := time.Now()
	files := []struct {
		name string
		mtime time.Time
	}{
		{"backup-20260109-120000.tar.gz", now.Add(-3 * time.Hour)},
		{"backup-20260110-120000.tar.gz", now.Add(-1 * time.Hour)},
		{"backup-20260111-120000.tar.gz", now.Add(-2 * time.Hour)},
		// These two must be filtered out:
		{"README.txt", now},
		{"backup-old.tar", now},
	}
	for _, f := range files {
		path := filepath.Join(backupsDir, f.name)
		if err := os.WriteFile(path, []byte("dummy"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, f.mtime, f.mtime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
	// And a subdirectory that must be skipped.
	if err := os.Mkdir(filepath.Join(backupsDir, "subdir.tar.gz"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/backups", nil)
	rec := httptest.NewRecorder()
	d.backupsListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []backupItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items count: want 3, got %d (%s)", len(resp.Items), rec.Body.String())
	}
	// Newest first → -1h then -2h then -3h.
	wantOrder := []string{
		"backup-20260110-120000.tar.gz",
		"backup-20260111-120000.tar.gz",
		"backup-20260109-120000.tar.gz",
	}
	for i, want := range wantOrder {
		if resp.Items[i].Name != want {
			t.Errorf("items[%d].name: want %q, got %q", i, want, resp.Items[i].Name)
		}
		// Path must be relative to DataDir (no absolute leakage).
		wantPath := "backups/" + want
		if resp.Items[i].Path != wantPath {
			t.Errorf("items[%d].path: want %q, got %q", i, wantPath, resp.Items[i].Path)
		}
		if resp.Items[i].SizeBytes != int64(len("dummy")) {
			t.Errorf("items[%d].size_bytes: want %d, got %d",
				i, len("dummy"), resp.Items[i].SizeBytes)
		}
	}
}

// TestBackupsCreateHandlerNilPool — a Deps with no pool (e.g. a
// test harness that omits the DB) must return a typed unavailable
// envelope, not panic. Mirrors jobs.go's defensive check.
func TestBackupsCreateHandlerNilPool(t *testing.T) {
	withDataDir(t, t.TempDir())
	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/_admin/backups", nil)
	rec := httptest.NewRecorder()
	d.backupsCreateHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "unavailable" {
		t.Fatalf("error.code: want unavailable, got %q (body=%s)",
			env.Error.Code, rec.Body.String())
	}
}

// contains is a tiny substring helper so the test doesn't pull in
// strings just for one assertion.
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
