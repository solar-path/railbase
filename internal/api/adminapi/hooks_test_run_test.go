package adminapi

// Tests for the v1.7.20 §3.4.11 admin hooks test-run endpoint.
//
// Default-tag tests only — the test-run path is pure filesystem +
// goja, no DB. Mirrors hooks_files_test.go's tempdir-driven approach:
// seed a `.js` file into Deps.HooksDir, fire the endpoint, assert the
// response envelope. No shared TestMain needed because the embed_pg
// suite from email_events_test.go doesn't gate these.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newHooksTestRunRouter mounts the test-run endpoint on a fresh chi
// router. Mirrors newHooksRouter in hooks_files_test.go. We DON'T wrap
// in RequireAdmin here — individual tests do that when they need to
// verify the auth gate, and skip it otherwise so they can hit the
// handler directly.
func newHooksTestRunRouter(d *Deps) chi.Router {
	r := chi.NewRouter()
	d.mountHooksTestRun(r)
	return r
}

// writeHookFile is the test-side equivalent of seedHooksDir: drop one
// .js file into dir at the given relative path.
func writeHookFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// decodeTestRunResponse pulls the JSON envelope into a strongly-typed
// shape so the assertions don't repeat the same struct literal.
type testRunResp struct {
	Outcome        string         `json:"outcome"`
	Console        []string       `json:"console"`
	ModifiedRecord map[string]any `json:"modified_record"`
	DurationMS     int64          `json:"duration_ms"`
	Error          string         `json:"error"`
}

func decodeTestRunResponse(t *testing.T, body *bytes.Buffer) testRunResp {
	t.Helper()
	var resp testRunResp
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body.String())
	}
	return resp
}

// TestHookTestRun_BeforeCreate_RunsHandler covers the happy path:
// a BeforeCreate handler logs to console + mutates the record's title.
// We assert:
//   - HTTP 200
//   - outcome=ok, no error
//   - console captures the log line in source order
//   - modified_record reflects the in-handler mutation
//
// The hook source uses wildcard collection ("*") so the test doesn't
// need to register a real collection; the dispatcher matches "posts"
// against the wildcard handler set the same way as in production.
func TestHookTestRun_BeforeCreate_RunsHandler(t *testing.T) {
	dir := t.TempDir()
	writeHookFile(t, dir, "before_create.js", `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
  console.log("running before create for", e.collection);
  e.record.title = "MUTATED: " + e.record.title;
  return e.next();
});
`)

	d := &Deps{HooksDir: dir}
	r := newHooksTestRunRouter(d)

	body, _ := json.Marshal(map[string]any{
		"event":      "BeforeCreate",
		"collection": "posts",
		"record":     map[string]any{"title": "hello"},
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/test-run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeTestRunResponse(t, rec.Body)
	if resp.Outcome != "ok" {
		t.Fatalf("outcome: want ok, got %q (error=%q console=%v)",
			resp.Outcome, resp.Error, resp.Console)
	}
	if resp.Error != "" {
		t.Errorf("error: want empty, got %q", resp.Error)
	}
	gotTitle, _ := resp.ModifiedRecord["title"].(string)
	if gotTitle != "MUTATED: hello" {
		t.Errorf("modified_record.title: want %q, got %q (full=%v)",
			"MUTATED: hello", gotTitle, resp.ModifiedRecord)
	}
	// At least one console line should mention the source-side log.
	// We don't pin the exact format because the renderer joins varargs
	// with spaces; checking for the substring is enough.
	found := false
	for _, ln := range resp.Console {
		if bytes.Contains([]byte(ln), []byte("running before create for")) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("console: want a line mentioning 'running before create for', got %v",
			resp.Console)
	}
	if resp.DurationMS < 0 {
		t.Errorf("duration_ms: want >=0, got %d", resp.DurationMS)
	}
}

// TestHookTestRun_RejectsViaThrow covers the "Before hook throws" case:
// the handler raises an Error; we expect outcome=rejected with the
// thrown message surfaced in `error`. This is the same shape the
// production REST handler would translate into a 400 validation error.
func TestHookTestRun_RejectsViaThrow(t *testing.T) {
	dir := t.TempDir()
	writeHookFile(t, dir, "throws.js", `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
  throw new Error("title is too short");
});
`)

	d := &Deps{HooksDir: dir}
	r := newHooksTestRunRouter(d)

	body, _ := json.Marshal(map[string]any{
		"event":      "BeforeCreate",
		"collection": "posts",
		"record":     map[string]any{"title": "x"},
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/test-run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (outcome in-band), got %d body=%s",
			rec.Code, rec.Body.String())
	}
	resp := decodeTestRunResponse(t, rec.Body)
	if resp.Outcome != "rejected" {
		t.Fatalf("outcome: want rejected, got %q (error=%q)", resp.Outcome, resp.Error)
	}
	if !bytes.Contains([]byte(resp.Error), []byte("title is too short")) {
		t.Errorf("error: should mention the thrown message, got %q", resp.Error)
	}
}

// TestHookTestRun_WatchdogKills covers the infinite-loop path. The
// handler enters a `while(true){}` that the runtime's per-handler
// watchdog interrupts after the configured timeout (2 s in the test
// path). We assert outcome=error AND duration_ms < 6000 — the outer
// ceiling is 6 s, so anything above means the watchdog DIDN'T fire
// and the test would hang to the test framework's timeout instead.
//
// We mark t.Parallel() not set: this test consumes a CPU for the
// busy-loop duration; running serially is intentional.
func TestHookTestRun_WatchdogKills(t *testing.T) {
	dir := t.TempDir()
	writeHookFile(t, dir, "hang.js", `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
  while (true) { /* spin */ }
});
`)

	d := &Deps{HooksDir: dir}
	r := newHooksTestRunRouter(d)

	body, _ := json.Marshal(map[string]any{
		"event":      "BeforeCreate",
		"collection": "posts",
		"record":     map[string]any{},
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/test-run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeTestRunResponse(t, rec.Body)
	if resp.Outcome != "error" {
		t.Fatalf("outcome: want error, got %q (error=%q)", resp.Outcome, resp.Error)
	}
	if resp.DurationMS >= 6000 {
		t.Errorf("duration_ms: want < 6000ms (watchdog cap), got %d", resp.DurationMS)
	}
	if !bytes.Contains([]byte(resp.Error), []byte("watchdog")) &&
		!bytes.Contains([]byte(resp.Error), []byte("timeout")) {
		t.Errorf("error: should mention watchdog/timeout, got %q", resp.Error)
	}
}

// TestHookTestRun_UnknownEvent_400 covers the event-name validation.
// An unknown wire string surfaces as a 400 with code=validation
// BEFORE the handler builds the runtime — saves work + gives the UI
// an obvious error to render.
func TestHookTestRun_UnknownEvent_400(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}
	r := newHooksTestRunRouter(d)

	body, _ := json.Marshal(map[string]any{
		"event":      "Unknown",
		"collection": "posts",
		"record":     map[string]any{},
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/test-run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "validation" {
		t.Errorf("error.code: want validation, got %q", env.Error.Code)
	}
	if !bytes.Contains([]byte(env.Error.Message), []byte("BeforeCreate")) {
		t.Errorf("error.message: should list the valid event names, got %q",
			env.Error.Message)
	}
}

// TestHookTestRun_RequireAdmin_401 mirrors TestPutPrefs_Unauthenticated:
// a sub-router with RequireAdmin wraps the mount, so an
// AdminPrincipal-less context returns 401. We can't call adminapi.Mount
// without the admins + session machinery, so we replicate the wrapping
// pattern here.
func TestHookTestRun_RequireAdmin_401(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{HooksDir: dir}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		d.mountHooksTestRun(r)
	})

	body, _ := json.Marshal(map[string]any{
		"event":      "BeforeCreate",
		"collection": "posts",
		"record":     map[string]any{},
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/test-run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "unauthorized" {
		t.Errorf("error.code: want unauthorized, got %q", env.Error.Code)
	}
}

// TestHookTestRun_Unavailable — bonus regression guard mirroring the
// hooks_files surface: empty HooksDir → 503. Belt-and-braces for the
// same reason TestHooksFilesUnavailable_EmptyHooksDir exists.
func TestHookTestRun_Unavailable(t *testing.T) {
	d := &Deps{HooksDir: ""}
	r := newHooksTestRunRouter(d)

	body, _ := json.Marshal(map[string]any{
		"event":      "BeforeCreate",
		"collection": "posts",
		"record":     map[string]any{},
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/test-run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
}
