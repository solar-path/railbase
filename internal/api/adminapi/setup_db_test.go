package adminapi

// Handler-shape tests for the v1.7.39 first-run "Database configuration"
// wizard endpoints (setup_db.go).
//
// These tests deliberately avoid spinning up embed_pg for the detect /
// validation paths — those handlers are pure filesystem + struct
// validation. The probe + save-db tests that do need a live PG share
// the same emEventsPool TestMain wired in email_events_test.go (build
// tag embed_pg), so the cost is amortised across the rest of the
// adminapi suite.
//
// Run:
//
//	go test -race -count=1 ./internal/api/adminapi/... -run TestSetupDB
//	go test -race -count=1 -tags embed_pg ./internal/api/adminapi/... -run TestSetupDB

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newSetupRouter builds a fresh chi.Router with the three setup
// endpoints mounted. Public — no RequireAdmin guard — matching the
// production wiring documented in setup_db.go's mountSetupDB.
func newSetupRouter(d *Deps) chi.Router {
	r := chi.NewRouter()
	d.mountSetupDB(r)
	return r
}

// withSetupDataDir points dataDirFromEnv at a temp directory for one
// test. Mirrors withDataDir in backups_test.go; duplicated rather than
// shared because the symbol there is already exported into this
// package via its non-_test.go suffix.
func withSetupDataDir(t *testing.T, dir string) {
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

// TestSetupDB_Detect_NoLocal_ReturnsEmptyList — the handler MUST
// serialise `sockets` as `[]` not `null` when no local PG is
// detected, so the React side's .map() works without a defensive
// guard. We can't easily force DetectLocalPostgresSockets() to return
// empty on a dev machine that happens to have a local PG, so we
// assert the JSON shape only (the slice is initialised to []
// explicitly inside the handler).
func TestSetupDB_Detect_NoLocal_ReturnsEmptyList(t *testing.T) {
	dir := t.TempDir()
	withSetupDataDir(t, dir)

	d := &Deps{}
	r := newSetupRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/_setup/detect", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	// `sockets` MUST serialise as a JSON array, never null — pin the
	// raw substring so a future regression that drops the make([], 0)
	// init in the handler is caught here, not in the frontend at
	// runtime.
	if !contains(rec.Body.String(), `"sockets":[`) {
		t.Fatalf("sockets key must serialise as JSON array; got %s", rec.Body.String())
	}
}

// TestSetupDB_Detect_ShapeIsCorrect pins the response envelope keys.
// Skipped paths around live local-PG presence: when sockets ARE
// present we also verify the per-entry shape; when none, we still
// assert the top-level keys are there.
func TestSetupDB_Detect_ShapeIsCorrect(t *testing.T) {
	dir := t.TempDir()
	withSetupDataDir(t, dir)

	d := &Deps{}
	r := newSetupRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/_setup/detect", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupDetectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}

	// current_mode: with no .dsn under DataDir, must be one of the
	// non-configured states — "embedded" when embed_pg is compiled in
	// (dev/demo build), or "setup" when not (release binary first
	// boot). Tests run without the embed_pg tag by default, so we
	// expect "setup" here; the embed_pg matrix re-runs the suite with
	// the tag and exercises the "embedded" branch.
	if resp.CurrentMode != "embedded" && resp.CurrentMode != "setup" {
		t.Errorf("current_mode: want embedded or setup with no .dsn, got %q", resp.CurrentMode)
	}
	// configured must mirror "external" only — neither embedded nor
	// setup count as "configured".
	if resp.Configured {
		t.Errorf("configured: want false when current_mode=%q, got true", resp.CurrentMode)
	}
	// suggested_username is os.Getenv("USER"); we just assert it's a
	// string (could be empty in CI sandboxes that strip env).
	_ = resp.SuggestedUsername

	// Per-entry shape, if any sockets surfaced. On CI runners we
	// usually get none; on a dev box with a Homebrew PG we get one
	// or two.
	for i, s := range resp.Sockets {
		if s.Dir == "" || s.Path == "" {
			t.Errorf("sockets[%d]: empty fields %+v", i, s)
		}
		if !strings.HasSuffix(s.Path, ".s.PGSQL.5432") {
			t.Errorf("sockets[%d].path: want suffix .s.PGSQL.5432, got %q", i, s.Path)
		}
	}
}

// TestSetupDB_Detect_ExternalModeWhenDSNFileExists — when a .dsn file
// is present under DataDir, the detect endpoint MUST report
// current_mode=external + configured=true. That's the "wizard is
// being re-visited after a successful save" path.
func TestSetupDB_Detect_ExternalModeWhenDSNFileExists(t *testing.T) {
	dir := t.TempDir()
	withSetupDataDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".dsn"),
		[]byte("postgres://u@h/d?sslmode=disable\n"), 0o600); err != nil {
		t.Fatalf("seed .dsn: %v", err)
	}

	d := &Deps{}
	r := newSetupRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/_setup/detect", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupDetectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.CurrentMode != "external" {
		t.Errorf("current_mode: want external with .dsn present, got %q", resp.CurrentMode)
	}
	if !resp.Configured {
		t.Errorf("configured: want true, got false")
	}
}

// TestSetupDB_ProbeDB_BadDSN_400 — malformed bodies hit the validation
// branch and return 400 with the typed error envelope. We exercise
// three failure shapes: empty body, missing driver, malformed
// external DSN.
func TestSetupDB_ProbeDB_BadDSN_400(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wantIn string // expected substring in error message
	}{
		{"empty body", ``, "empty body"},
		{"unknown driver", `{"driver":"sqlite"}`, "driver must be one of"},
		{"bad external DSN", `{"driver":"external","external_dsn":"http://foo"}`, "must start with postgres"},
		{"missing socket dir", `{"driver":"local-socket","username":"u"}`, "socket_dir is required"},
		{"missing username", `{"driver":"local-socket","socket_dir":"/tmp"}`, "username is required"},
	}

	d := &Deps{}
	r := newSetupRouter(d)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/_setup/probe-db",
				bytes.NewBufferString(tc.body))
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
			if !strings.Contains(env.Error.Message, tc.wantIn) {
				t.Errorf("error.message: want substring %q, got %q",
					tc.wantIn, env.Error.Message)
			}
		})
	}
}

// TestSetupDB_ProbeDB_ConnectionRefused_ReturnsErrorPayload — point at
// an unreachable host. The endpoint must respond 200 (it's not a
// validation error, the body was well-formed) with ok=false + a
// populated hint so the wizard can render the failure inline.
//
// We use 127.0.0.1:1 (privileged port nobody listens on, but kernel
// refuses fast — no DNS round-trip needed). The hint string is
// pattern-matched against the lower-cased connection-refused error.
func TestSetupDB_ProbeDB_ConnectionRefused_ReturnsErrorPayload(t *testing.T) {
	d := &Deps{}
	r := newSetupRouter(d)
	body := `{"driver":"external","external_dsn":"postgres://u:p@127.0.0.1:1/d?sslmode=disable"}`
	req := httptest.NewRequest(http.MethodPost, "/_setup/probe-db",
		bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.OK {
		t.Fatalf("ok: want false on unreachable host, got true; body=%s", rec.Body.String())
	}
	if resp.Error == "" {
		t.Errorf("error: want non-empty on failure, got empty; body=%s", rec.Body.String())
	}
	if resp.Hint == "" {
		t.Errorf("hint: want populated on failure, got empty; body=%s", rec.Body.String())
	}
}

// TestSetupDB_SaveDB_EmbeddedDriverShortCircuits — the embedded
// branch is intentionally a no-op (no .dsn written). Verify the
// response shape, restart_required=false, and that .dsn is NOT
// created.
func TestSetupDB_SaveDB_EmbeddedDriverShortCircuits(t *testing.T) {
	dir := t.TempDir()
	withSetupDataDir(t, dir)

	d := &Deps{}
	r := newSetupRouter(d)
	req := httptest.NewRequest(http.MethodPost, "/_setup/save-db",
		bytes.NewBufferString(`{"driver":"embedded"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupSaveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !resp.OK {
		t.Errorf("ok: want true, got false; body=%s", rec.Body.String())
	}
	if resp.RestartRequired {
		t.Errorf("restart_required: want false for embedded, got true")
	}
	if _, err := os.Stat(filepath.Join(dir, ".dsn")); !os.IsNotExist(err) {
		t.Errorf(".dsn file should not exist for embedded driver; stat err: %v", err)
	}
}

// TestSetupDB_buildSetupDSN_LocalSocketShape pins the DSN string for
// the local-socket driver against the canonical libpq URL form. The
// wizard frontend renders this string back to the operator so a
// stable shape avoids "wait why does my DSN have these escapes"
// confusion.
func TestSetupDB_buildSetupDSN_LocalSocketShape(t *testing.T) {
	body := setupDBBody{
		Driver:    "local-socket",
		SocketDir: "/tmp",
		Username:  "ali",
		Database:  "railbase",
		SSLMode:   "disable",
	}
	got, err := buildSetupDSN(body)
	if err != nil {
		t.Fatalf("buildSetupDSN: %v", err)
	}
	// Order-independent verification — url.Values.Encode() iterates
	// in alpha-sorted key order ("host" then "sslmode"), so the
	// canonical form is deterministic. We still assert against the
	// component pieces so a future query-key reordering only fails
	// the one assertion that matters.
	if !strings.HasPrefix(got, "postgres://ali@/railbase?") {
		t.Errorf("DSN prefix: want 'postgres://ali@/railbase?', got %q", got)
	}
	if !strings.Contains(got, "host=%2Ftmp") {
		t.Errorf("DSN should encode host=/tmp; got %q", got)
	}
	if !strings.Contains(got, "sslmode=disable") {
		t.Errorf("DSN should carry sslmode=disable; got %q", got)
	}
}

// TestSetupDB_buildSetupDSN_LocalSocket_WithPassword — password is
// optional for trust/peer auth; when supplied it must round-trip
// through the URL userinfo without leaking into the path.
func TestSetupDB_buildSetupDSN_LocalSocket_WithPassword(t *testing.T) {
	body := setupDBBody{
		Driver:    "local-socket",
		SocketDir: "/tmp",
		Username:  "ali",
		Password:  "s3cret",
		Database:  "railbase",
		SSLMode:   "disable",
	}
	got, err := buildSetupDSN(body)
	if err != nil {
		t.Fatalf("buildSetupDSN: %v", err)
	}
	if !strings.HasPrefix(got, "postgres://ali:s3cret@/railbase?") {
		t.Errorf("DSN: want password in userinfo, got %q", got)
	}
}

// TestSetupDB_buildSetupDSN_ExternalPassthrough — the external driver
// is a verbatim pass-through after a prefix sanity check. We don't
// mangle whitespace or query strings.
func TestSetupDB_buildSetupDSN_ExternalPassthrough(t *testing.T) {
	want := "postgres://u:p@db.internal:5432/app?sslmode=require"
	got, err := buildSetupDSN(setupDBBody{Driver: "external", ExternalDSN: want})
	if err != nil {
		t.Fatalf("buildSetupDSN: %v", err)
	}
	if got != want {
		t.Errorf("external DSN should pass through verbatim: want %q got %q", want, got)
	}
}

// TestSetupDB_setupProbeHint_MapsCommonErrors pins the substring →
// hint mapping for the most common failure modes. Documentation by
// test: if a future contributor renames a hint string, this test
// breaks loudly rather than silently shipping a less-helpful UI.
func TestSetupDB_setupProbeHint_MapsCommonErrors(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want string // substring of expected hint
	}{
		{"db missing", `pq: database "railbase" does not exist`, "Create the database if it doesn't exist"},
		{"auth failed", "password authentication failed for user ali", "Authentication failed"},
		{"refused", "dial tcp 127.0.0.1:5432: connect: connection refused", "No server is listening"},
		{"no host", "dial tcp: lookup foo.invalid: no such host", "Host or socket"},
		{"ssl", "tls: handshake failure", "TLS handshake"},
		{"timeout", "context deadline exceeded", "timed out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := setupProbeHint(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Errorf("hint for %q: want substring %q, got %q", tc.err, tc.want, got)
			}
		})
	}
}

// TestSetupDB_dsnWithDatabase rewrites the path of a DSN to flip the
// target db. Used by the CREATE DATABASE path to swap onto the
// `postgres` admin db with the same credentials.
func TestSetupDB_dsnWithDatabase(t *testing.T) {
	got, err := dsnWithDatabase("postgres://u:p@h:5432/origdb?sslmode=disable", "postgres")
	if err != nil {
		t.Fatalf("dsnWithDatabase: %v", err)
	}
	if !strings.HasPrefix(got, "postgres://u:p@h:5432/postgres?") {
		t.Errorf("rewritten DSN: want path=/postgres, got %q", got)
	}
	if !strings.Contains(got, "sslmode=disable") {
		t.Errorf("rewritten DSN: querystring lost; got %q", got)
	}
}

// TestSetupDB_readPersistedDSNFile — file I/O contract for the small
// helper. Absent / empty / whitespace-only all map to "".
func TestSetupDB_readPersistedDSNFile(t *testing.T) {
	dir := t.TempDir()

	// Absent.
	if got := readPersistedDSNFile(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("absent file: want \"\", got %q", got)
	}
	// Empty.
	emptyPath := filepath.Join(dir, "empty")
	_ = os.WriteFile(emptyPath, []byte(""), 0o600)
	if got := readPersistedDSNFile(emptyPath); got != "" {
		t.Errorf("empty file: want \"\", got %q", got)
	}
	// Whitespace only.
	wsPath := filepath.Join(dir, "ws")
	_ = os.WriteFile(wsPath, []byte("  \n\t  "), 0o600)
	if got := readPersistedDSNFile(wsPath); got != "" {
		t.Errorf("whitespace-only: want \"\", got %q", got)
	}
	// Real value, trimmed.
	realPath := filepath.Join(dir, "real")
	_ = os.WriteFile(realPath, []byte("  postgres://u@h/d\n"), 0o600)
	if got := readPersistedDSNFile(realPath); got != "postgres://u@h/d" {
		t.Errorf("real value: want trimmed DSN, got %q", got)
	}
}

