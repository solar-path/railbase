package adminapi

// Handler-shape tests for the v1.7.x §3.11 Mailer templates admin
// surface. These tests stay filesystem-only — no Pool, no Mailer
// instance, no SMTP — because the handlers' branches are 100%
// stat/read/render. The Mailer's own resolver chain is covered by
// internal/mailer/mailer_test.go.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/mailer"
)

// TestMailerTemplatesListHandlerNoOverrides — a fresh deploy with no
// `<DataDir>/email_templates/` directory should still return one
// entry per built-in kind, all with override_exists=false. Mirrors
// the backups handler's empty-dir contract.
func TestMailerTemplatesListHandlerNoOverrides(t *testing.T) {
	dir := t.TempDir()
	withDataDir(t, dir)

	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/mailer-templates", nil)
	rec := httptest.NewRecorder()
	d.mailerTemplatesListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Templates []mailerTemplateMeta `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	wantKinds := mailer.BuiltinKinds()
	if len(resp.Templates) != len(wantKinds) {
		t.Fatalf("templates count: want %d, got %d (%s)",
			len(wantKinds), len(resp.Templates), rec.Body.String())
	}
	for i, want := range wantKinds {
		if resp.Templates[i].Kind != want {
			t.Errorf("templates[%d].kind: want %q, got %q", i, want, resp.Templates[i].Kind)
		}
		if resp.Templates[i].OverrideExists {
			t.Errorf("templates[%d].override_exists: want false, got true", i)
		}
		if resp.Templates[i].OverrideSize != 0 {
			t.Errorf("templates[%d].override_size_bytes: want 0, got %d",
				i, resp.Templates[i].OverrideSize)
		}
		if resp.Templates[i].OverrideModified != nil {
			t.Errorf("templates[%d].override_modified: want nil, got %v",
				i, resp.Templates[i].OverrideModified)
		}
	}
}

// TestMailerTemplatesListHandlerOneOverride — a single override file
// on disk should flip override_exists for exactly that kind, with
// the correct size, while every other kind stays false.
func TestMailerTemplatesListHandlerOneOverride(t *testing.T) {
	dir := t.TempDir()
	overrideDir := filepath.Join(dir, "email_templates")
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	withDataDir(t, dir)

	// Pick a real built-in kind so the response actually flips.
	kinds := mailer.BuiltinKinds()
	if len(kinds) == 0 {
		t.Fatal("BuiltinKinds() returned empty — embed broken?")
	}
	overrideKind := kinds[0]
	body := "# Overridden\n\nHello {{ user.email }}.\n"
	overridePath := filepath.Join(overrideDir, overrideKind+".md")
	if err := os.WriteFile(overridePath, []byte(body), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/mailer-templates", nil)
	rec := httptest.NewRecorder()
	d.mailerTemplatesListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Templates []mailerTemplateMeta `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	for _, item := range resp.Templates {
		if item.Kind == overrideKind {
			if !item.OverrideExists {
				t.Errorf("%s.override_exists: want true, got false", item.Kind)
			}
			if item.OverrideSize != int64(len(body)) {
				t.Errorf("%s.override_size_bytes: want %d, got %d",
					item.Kind, len(body), item.OverrideSize)
			}
			if item.OverrideModified == nil {
				t.Errorf("%s.override_modified: want non-nil, got nil", item.Kind)
			}
		} else {
			if item.OverrideExists {
				t.Errorf("%s.override_exists: want false, got true", item.Kind)
			}
		}
	}
}

// TestMailerTemplatesViewHandlerUnknownKind — any kind not in the
// builtin set must return the typed not_found envelope, not 200 with
// an empty source. We route through a chi mux so the URL param
// binding mirrors production.
func TestMailerTemplatesViewHandlerUnknownKind(t *testing.T) {
	withDataDir(t, t.TempDir())

	d := &Deps{}
	r := chi.NewRouter()
	r.Get("/api/_admin/mailer-templates/{kind}", d.mailerTemplatesViewHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/_admin/mailer-templates/nope_bogus", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d (body=%s)", rec.Code, rec.Body.String())
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
	if env.Error.Code != "not_found" {
		t.Fatalf("error.code: want not_found, got %q (body=%s)",
			env.Error.Code, rec.Body.String())
	}
}

// TestMailerTemplatesViewHandlerBuiltin — when no override is on
// disk, the viewer must return the embedded built-in's source plus
// a non-empty html render. We pick the first builtin kind so the
// test stays robust against future kind additions.
func TestMailerTemplatesViewHandlerBuiltin(t *testing.T) {
	withDataDir(t, t.TempDir())

	kinds := mailer.BuiltinKinds()
	if len(kinds) == 0 {
		t.Fatal("BuiltinKinds() returned empty — embed broken?")
	}
	kind := kinds[0]
	builtin, ok := mailer.BuiltinSource(kind)
	if !ok {
		t.Fatalf("BuiltinSource(%s) not found", kind)
	}

	d := &Deps{}
	r := chi.NewRouter()
	r.Get("/api/_admin/mailer-templates/{kind}", d.mailerTemplatesViewHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/_admin/mailer-templates/"+kind, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp mailerTemplateView
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.Kind != kind {
		t.Errorf("kind: want %q, got %q", kind, resp.Kind)
	}
	if resp.Source != builtin {
		t.Errorf("source: want builtin (%d bytes), got %d bytes",
			len(builtin), len(resp.Source))
	}
	if resp.OverrideExists {
		t.Errorf("override_exists: want false, got true")
	}
	if resp.HTML == "" {
		t.Errorf("html: want non-empty, got empty")
	}
}

// TestMailerTemplatesViewHandlerOverride — when an override file is
// on disk, source must echo the file contents (not the builtin), and
// override_exists/size/modified must reflect the file's stat.
func TestMailerTemplatesViewHandlerOverride(t *testing.T) {
	dir := t.TempDir()
	overrideDir := filepath.Join(dir, "email_templates")
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	withDataDir(t, dir)

	kinds := mailer.BuiltinKinds()
	if len(kinds) == 0 {
		t.Fatal("BuiltinKinds() returned empty — embed broken?")
	}
	kind := kinds[0]
	body := "---\nsubject: Overridden subject\n---\n\n# Custom header\n\nHello {{ user.email }}.\n"
	overridePath := filepath.Join(overrideDir, kind+".md")
	if err := os.WriteFile(overridePath, []byte(body), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	d := &Deps{}
	r := chi.NewRouter()
	r.Get("/api/_admin/mailer-templates/{kind}", d.mailerTemplatesViewHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/_admin/mailer-templates/"+kind, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp mailerTemplateView
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.Source != body {
		t.Errorf("source mismatch:\nwant %q\ngot  %q", body, resp.Source)
	}
	if !resp.OverrideExists {
		t.Errorf("override_exists: want true, got false")
	}
	if resp.OverrideSize != int64(len(body)) {
		t.Errorf("override_size_bytes: want %d, got %d", len(body), resp.OverrideSize)
	}
	if resp.OverrideModified == nil {
		t.Errorf("override_modified: want non-nil, got nil")
	}
	// The HTML render should include the "Custom header" text from
	// the override body — proves we're rendering the file, not the
	// builtin.
	if !strings.Contains(resp.HTML, "Custom header") {
		t.Errorf("html: want to contain %q, got %q", "Custom header", resp.HTML)
	}
}
