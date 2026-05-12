package adminapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newHooksRouter mounts the hooks-files surface on a fresh chi router
// using the supplied Deps. Centralised so each test can pin a different
// HooksDir + share the same dispatch shape.
func newHooksRouter(d *Deps) chi.Router {
	r := chi.NewRouter()
	d.mountHooksFiles(r)
	return r
}

// seedHooksDir writes three .js files into the tempdir, including one
// nested under a sub-folder, so the list test exercises the recursive
// walk + sort ordering simultaneously.
func seedHooksDir(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write seed file %s: %v", rel, err)
		}
	}
}

// TestHooksFilesList_SortedRecursive — three seeded files, one nested,
// one with .txt extension that must be filtered out. The list should
// return exactly two .js entries in path-sorted order.
func TestHooksFilesList_SortedRecursive(t *testing.T) {
	dir := t.TempDir()
	seedHooksDir(t, dir, map[string]string{
		"zeta.js":          "// last alphabetically\n",
		"alpha.js":         "// first\n",
		"sub/middle.js":    "// nested\n",
		"ignored.txt":      "not a hook\n",
		".hidden/skip.js":  "// hidden dir should be skipped\n",
	})

	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/hooks/files", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			Path     string `json:"path"`
			Size     int64  `json:"size"`
			Modified string `json:"modified"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(got.Items) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(got.Items), got.Items)
	}
	want := []string{"alpha.js", "sub/middle.js", "zeta.js"}
	for i, w := range want {
		if got.Items[i].Path != w {
			t.Errorf("items[%d].path: want %q, got %q", i, w, got.Items[i].Path)
		}
		if got.Items[i].Size <= 0 {
			t.Errorf("items[%d].size: want >0, got %d", i, got.Items[i].Size)
		}
		if got.Items[i].Modified == "" {
			t.Errorf("items[%d].modified: want non-empty RFC3339", i)
		}
	}
}

// TestHooksFilesList_EmptyDir — an empty HooksDir returns the JSON
// envelope with `items: []`, not null. Operators rendering the editor
// against a fresh checkout should see "no hooks yet" not "schema error".
func TestHooksFilesList_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/hooks/files", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Verify the empty array shape — items must be `[]`, not `null`.
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body should carry items:[] envelope, got %s", rec.Body.String())
	}
}

// TestHooksFilesGet_ExistingFile — happy-path read. Returns 200 with
// content + size + modified populated.
func TestHooksFilesGet_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	content := "// $app.onRecordBeforeCreate('posts', (e) => {})\n"
	seedHooksDir(t, dir, map[string]string{"on_post_create.js": content})

	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/hooks/files/on_post_create.js", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Size    int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got.Path != "on_post_create.js" {
		t.Errorf("path: want on_post_create.js, got %q", got.Path)
	}
	if got.Content != content {
		t.Errorf("content mismatch: want %q, got %q", content, got.Content)
	}
	if got.Size != int64(len(content)) {
		t.Errorf("size: want %d, got %d", len(content), got.Size)
	}
}

// TestHooksFilesGet_MissingFile — non-existent path returns 404 with
// the typed `not_found` code envelope (not a generic 500 / panic).
func TestHooksFilesGet_MissingFile(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/hooks/files/does_not_exist.js", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "not_found" {
		t.Errorf("error.code: want not_found, got %q", env.Error.Code)
	}
}

// TestHooksFilesPut_NewFile — PUT to a path that doesn't exist yet
// creates the file (and any missing parent directories) and returns
// 200 with the file's size + modified time.
func TestHooksFilesPut_NewFile(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)

	body, _ := json.Marshal(map[string]string{"content": "// brand new\n"})
	req := httptest.NewRequest(http.MethodPut, "/hooks/files/new/handler.js", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Verify on disk.
	wrote, err := os.ReadFile(filepath.Join(dir, "new", "handler.js"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(wrote) != "// brand new\n" {
		t.Errorf("on-disk content: want %q, got %q", "// brand new\n", string(wrote))
	}
}

// TestHooksFilesPut_UpdateExisting — PUT to an existing path replaces
// the content + bumps mtime. Verifies the temp-file + rename dance
// doesn't leave a `.rb-hook-*.tmp` behind.
func TestHooksFilesPut_UpdateExisting(t *testing.T) {
	dir := t.TempDir()
	seedHooksDir(t, dir, map[string]string{"existing.js": "// v1\n"})

	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)

	body, _ := json.Marshal(map[string]string{"content": "// v2 updated\n"})
	req := httptest.NewRequest(http.MethodPut, "/hooks/files/existing.js", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	wrote, err := os.ReadFile(filepath.Join(dir, "existing.js"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(wrote) != "// v2 updated\n" {
		t.Errorf("content: want %q, got %q", "// v2 updated\n", string(wrote))
	}
	// Ensure no temp file lingered.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".rb-hook-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

// TestHooksFilesDelete_RemovesFile — happy-path delete returns 204 and
// the file is gone from disk.
func TestHooksFilesDelete_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	seedHooksDir(t, dir, map[string]string{"tobegone.js": "// rip\n"})

	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)
	req := httptest.NewRequest(http.MethodDelete, "/hooks/files/tobegone.js", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "tobegone.js")); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err=%v", err)
	}
}

// TestHooksFilesDelete_Missing — delete-of-nothing returns 404 (the
// surface intentionally does NOT treat this as idempotent; the UI
// should reflect that the operator's target wasn't there).
func TestHooksFilesDelete_Missing(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)
	req := httptest.NewRequest(http.MethodDelete, "/hooks/files/nope.js", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
}

// TestHooksFilesPathTraversal_Rejected exercises every variant of the
// traversal guard. Each path must come back as a 400 `bad_request`
// without touching anything outside HooksDir.
func TestHooksFilesPathTraversal_Rejected(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)

	// Build the malicious path list. Some need url-encoding because chi
	// won't accept literal `../` segments through its glob param —
	// percent-encoding routes them through the handler so our defence
	// runs.
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"plain dotdot", http.MethodGet, "/hooks/files/" + url.PathEscape("../etc/passwd")},
		{"nested dotdot", http.MethodGet, "/hooks/files/" + url.PathEscape("sub/../../etc/passwd")},
		{"absolute path", http.MethodGet, "/hooks/files/" + url.PathEscape("/etc/passwd")},
		{"dotdot put", http.MethodPut, "/hooks/files/" + url.PathEscape("../escape.js")},
		{"dotdot delete", http.MethodDelete, "/hooks/files/" + url.PathEscape("../etc/passwd")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.method == http.MethodPut {
				body, _ = json.Marshal(map[string]string{"content": "// pwn\n"})
			}
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: want 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v body=%s", err, rec.Body.String())
			}
			if env.Error.Code != "bad_request" {
				t.Errorf("error.code: want bad_request, got %q", env.Error.Code)
			}
			if !strings.Contains(env.Error.Message, "hooks") && !strings.Contains(env.Error.Message, "path") {
				t.Errorf("error.message should mention hooks/path, got %q", env.Error.Message)
			}
		})
	}
}

// TestHooksFilesUnavailable_EmptyHooksDir — when Deps.HooksDir is "",
// every route surfaces 503 `unavailable` so the admin UI can render
// the "hooks dir not configured" hint instead of a generic 5xx.
func TestHooksFilesUnavailable_EmptyHooksDir(t *testing.T) {
	d := &Deps{HooksDir: ""}
	r := newHooksRouter(d)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/hooks/files"},
		{http.MethodGet, "/hooks/files/x.js"},
		{http.MethodPut, "/hooks/files/x.js"},
		{http.MethodDelete, "/hooks/files/x.js"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader([]byte(`{"content":""}`)))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status: want 503, got %d body=%s", rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v body=%s", err, rec.Body.String())
			}
			if env.Error.Code != "unavailable" {
				t.Errorf("error.code: want unavailable, got %q", env.Error.Code)
			}
		})
	}
}

// TestHooksFilesPut_RejectsNonJS — PUT to a path that doesn't end in
// `.js` is rejected as a 400 validation error. The surface is hooks-only
// by design (see handler comment).
func TestHooksFilesPut_RejectsNonJS(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}
	r := newHooksRouter(d)

	body, _ := json.Marshal(map[string]string{"content": "anything"})
	req := httptest.NewRequest(http.MethodPut, "/hooks/files/oops.txt", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
