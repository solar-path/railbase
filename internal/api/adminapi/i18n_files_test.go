package adminapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newI18nRouter mounts the translations-editor surface on a fresh chi
// router using the supplied Deps. Centralised so each test can pin a
// different I18nDir + share the same dispatch shape — mirrors
// newHooksRouter.
func newI18nRouter(d *Deps) chi.Router {
	r := chi.NewRouter()
	d.mountI18nFiles(r)
	return r
}

// writeOverrideFile is a tiny helper that drops a JSON bundle into
// I18nDir under the given locale. Used by tests that need to pre-seed
// the directory before calling the handler.
func writeOverrideFile(t *testing.T, dir, locale string, entries map[string]string) {
	t.Helper()
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, locale+".json"), body, 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}

// decodeI18nJSON is a typed JSON-decode helper that fails the test on a
// parse error. Centralises the boilerplate so each test reads cleanly.
func decodeI18nJSON(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode: %v body=%s", err, string(body))
	}
}

// TestI18nLocales_ListsEmbeddedAndOverrides — the listing endpoint
// returns the union of embedded + override locales (sorted) plus
// coverage stats for each. We seed an `fr` override (partial coverage)
// + a full `ru` override; the embedded set (en, ru) is read straight
// from internal/i18n/embed.FS.
func TestI18nLocales_ListsEmbeddedAndOverrides(t *testing.T) {
	dir := t.TempDir()
	// Partial `fr` override — covers only 1 key.
	writeOverrideFile(t, dir, "fr", map[string]string{
		"auth.signin.title": "Connexion",
	})
	// Full-ish `ru` override (mirroring the embedded ru.json shape).
	writeOverrideFile(t, dir, "ru", map[string]string{
		"auth.signin.title": "Вход",
	})

	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/i18n/locales", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got i18nLocalesResponse
	decodeI18nJSON(t, rec.Body.Bytes(), &got)

	if got.Default != "en" {
		t.Errorf("default: want en, got %q", got.Default)
	}
	// Embedded should at minimum include "en" and "ru" (both shipped
	// with the binary). We don't assert exact equality because future
	// commits may add more embedded bundles.
	embeddedSet := map[string]bool{}
	for _, l := range got.Embedded {
		embeddedSet[l] = true
	}
	if !embeddedSet["en"] || !embeddedSet["ru"] {
		t.Errorf("embedded: want en+ru, got %v", got.Embedded)
	}
	// Overrides: exactly the two files we wrote, sorted.
	wantOverrides := []string{"fr", "ru"}
	if len(got.Overrides) != 2 || got.Overrides[0] != wantOverrides[0] || got.Overrides[1] != wantOverrides[1] {
		t.Errorf("overrides: want %v, got %v", wantOverrides, got.Overrides)
	}
	// Supported: union, sorted. Must include en, fr, ru.
	supportedSet := map[string]bool{}
	for _, l := range got.Supported {
		supportedSet[l] = true
	}
	for _, want := range []string{"en", "fr", "ru"} {
		if !supportedSet[want] {
			t.Errorf("supported: missing %q, got %v", want, got.Supported)
		}
	}
	// Sortedness check: supported should be alphabetical.
	if !sort.StringsAreSorted(got.Supported) {
		t.Errorf("supported not sorted: %v", got.Supported)
	}
}

// TestI18nLocales_CoverageStats — coverage rows compute total_keys
// against the embedded en bundle (the reference) and missing_keys as
// the alphabetical complement. A partial override should surface a
// non-empty missing_keys list; the embedded `en` reference should
// have zero missing.
func TestI18nLocales_CoverageStats(t *testing.T) {
	dir := t.TempDir()
	// Override `fr` with one known key + one unknown (the unknown is
	// not in the reference set, so it doesn't increase total_keys —
	// the reference key universe is fixed at en.json's shape).
	writeOverrideFile(t, dir, "fr", map[string]string{
		"auth.signin.title": "Connexion",
		"custom.app.key":    "Application",
	})

	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/i18n/locales", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got i18nLocalesResponse
	decodeI18nJSON(t, rec.Body.Bytes(), &got)

	// en coverage: 100% (it IS the reference). Verify zero missing.
	enCov, ok := got.Coverage["en"]
	if !ok {
		t.Fatalf("coverage.en missing")
	}
	if enCov.TotalKeys == 0 {
		t.Errorf("coverage.en.total_keys: want >0, got 0")
	}
	if len(enCov.MissingKeys) != 0 {
		t.Errorf("coverage.en.missing_keys: want empty, got %v", enCov.MissingKeys)
	}
	if enCov.Translated != enCov.TotalKeys {
		t.Errorf("coverage.en.translated: want %d, got %d", enCov.TotalKeys, enCov.Translated)
	}

	// fr coverage: only 1 reference key covered, rest missing.
	frCov, ok := got.Coverage["fr"]
	if !ok {
		t.Fatalf("coverage.fr missing")
	}
	if frCov.TotalKeys != enCov.TotalKeys {
		t.Errorf("coverage.fr.total_keys: want %d (matches reference), got %d", enCov.TotalKeys, frCov.TotalKeys)
	}
	if frCov.Translated != 1 {
		t.Errorf("coverage.fr.translated: want 1 (auth.signin.title), got %d", frCov.Translated)
	}
	if len(frCov.MissingKeys) != frCov.TotalKeys-1 {
		t.Errorf("coverage.fr.missing_keys: want %d, got %d", frCov.TotalKeys-1, len(frCov.MissingKeys))
	}
	// missing_keys must be sorted alphabetically.
	if !sort.StringsAreSorted(frCov.MissingKeys) {
		t.Errorf("coverage.fr.missing_keys not sorted: %v", frCov.MissingKeys)
	}
}

// TestI18nFileGet_Existing — happy path: an override file exists, GET
// returns both the embedded bundle (for the same locale) AND the
// override map. The UI overlays them in the editor.
func TestI18nFileGet_Existing(t *testing.T) {
	dir := t.TempDir()
	writeOverrideFile(t, dir, "ru", map[string]string{
		"auth.signin.title": "Override RU",
	})

	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/i18n/files/ru", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got i18nFileResponse
	decodeI18nJSON(t, rec.Body.Bytes(), &got)
	if got.Locale != "ru" {
		t.Errorf("locale: want ru, got %q", got.Locale)
	}
	if got.Override == nil {
		t.Fatalf("override: want non-nil (we wrote the file)")
	}
	if got.Override["auth.signin.title"] != "Override RU" {
		t.Errorf("override.auth.signin.title: want %q, got %q", "Override RU", got.Override["auth.signin.title"])
	}
	if len(got.Embedded) == 0 {
		t.Errorf("embedded: want non-empty for `ru` (shipped in binary), got 0 keys")
	}
}

// TestI18nFileGet_NoOverride — when no override file exists, override
// is JSON null but embedded is still returned. The UI uses this to
// seed a new override from the embedded reference values.
func TestI18nFileGet_NoOverride(t *testing.T) {
	dir := t.TempDir()

	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)
	// `fr` is NOT shipped in the embedded set today (only en + ru),
	// but we use `en` to exercise the embedded-present path while
	// having no override.
	req := httptest.NewRequest(http.MethodGet, "/i18n/files/en", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// We need to detect the JSON-null override explicitly. The decoded
	// struct field will be nil; verify by re-parsing the raw body.
	var raw map[string]json.RawMessage
	decodeI18nJSON(t, rec.Body.Bytes(), &raw)
	if string(raw["override"]) != "null" {
		t.Errorf("override raw: want \"null\", got %s", string(raw["override"]))
	}
	if string(raw["embedded"]) == "{}" {
		t.Errorf("embedded raw: want non-empty for `en`, got %s", string(raw["embedded"]))
	}
}

// TestI18nFilePut_CreatesNewFile — PUT to a locale that has no
// override yet creates the file. The content should round-trip
// through a follow-up GET.
func TestI18nFilePut_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)

	entries := map[string]string{
		"auth.signin.title": "Iniciar sesión",
		"auth.email.label":  "Correo electrónico",
	}
	body, _ := json.Marshal(map[string]any{"entries": entries})
	req := httptest.NewRequest(http.MethodPut, "/i18n/files/es", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// On-disk verification — the file should exist + parse back to the
	// same map.
	abs := filepath.Join(dir, "es.json")
	disk, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	var back map[string]string
	if err := json.Unmarshal(disk, &back); err != nil {
		t.Fatalf("parse written file: %v body=%s", err, string(disk))
	}
	for k, v := range entries {
		if back[k] != v {
			t.Errorf("on-disk entries[%q]: want %q, got %q", k, v, back[k])
		}
	}

	// Round-trip via GET — the override field should match exactly.
	getReq := httptest.NewRequest(http.MethodGet, "/i18n/files/es", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get after put status: want 200, got %d", getRec.Code)
	}
	var roundtrip i18nFileResponse
	decodeI18nJSON(t, getRec.Body.Bytes(), &roundtrip)
	if len(roundtrip.Override) != len(entries) {
		t.Errorf("round-trip override len: want %d, got %d", len(entries), len(roundtrip.Override))
	}
}

// TestI18nFilePut_EmptyEntriesRemovesFile — PUT with empty `entries`
// (or missing field) is a synonym for DELETE. Used by the admin UI's
// "clear all" button to drop the override.
func TestI18nFilePut_EmptyEntriesRemovesFile(t *testing.T) {
	dir := t.TempDir()
	writeOverrideFile(t, dir, "fr", map[string]string{"k": "v"})

	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)

	body, _ := json.Marshal(map[string]any{"entries": map[string]string{}})
	req := httptest.NewRequest(http.MethodPut, "/i18n/files/fr", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d body=%s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(filepath.Join(dir, "fr.json")); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err=%v", err)
	}
}

// TestI18nFileDelete_RemovesAndIs404OnSecondCall — first DELETE returns
// 204, second returns 404. Mirrors hooks_files delete semantics: NOT
// idempotent by design — the UI should reflect that the target wasn't
// there.
func TestI18nFileDelete_RemovesAndIs404OnSecondCall(t *testing.T) {
	dir := t.TempDir()
	writeOverrideFile(t, dir, "fr", map[string]string{"k": "v"})

	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)

	// First delete: 204.
	req := httptest.NewRequest(http.MethodDelete, "/i18n/files/fr", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first delete status: want 204, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Second delete: 404 with the typed `not_found` envelope.
	req2 := httptest.NewRequest(http.MethodDelete, "/i18n/files/fr", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("second delete status: want 404, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeI18nJSON(t, rec2.Body.Bytes(), &env)
	if env.Error.Code != "not_found" {
		t.Errorf("error.code: want not_found, got %q", env.Error.Code)
	}
}

// TestI18nInvalidLocale_Rejected — locales failing the BCP-47 regex
// (`/etc/passwd`, `EN`, `eng-US`, `..`, …) get a 400 `bad_request`
// envelope on every method. None of these can produce a filesystem
// traversal because the regex is the entire path-safety check.
func TestI18nInvalidLocale_Rejected(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		// Note: chi's path matcher treats `/` as a segment boundary, so
		// `/etc/passwd` as a literal won't even reach our handler — the
		// route doesn't match. We encode it instead so the param IS
		// `/etc/passwd` literally and our regex check runs.
		{"slash payload", http.MethodGet, "/i18n/files/" + url.PathEscape("/etc/passwd")},
		{"uppercase EN", http.MethodGet, "/i18n/files/EN"},
		{"too-long base", http.MethodGet, "/i18n/files/enus-US"},
		{"single letter", http.MethodGet, "/i18n/files/e"},
		{"dotdot", http.MethodGet, "/i18n/files/" + url.PathEscape("..")},
		{"mixed case region", http.MethodGet, "/i18n/files/en-us"},
		{"put with bad locale", http.MethodPut, "/i18n/files/EN"},
		{"delete with bad locale", http.MethodDelete, "/i18n/files/EN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.method == http.MethodPut {
				body, _ = json.Marshal(map[string]any{"entries": map[string]string{"k": "v"}})
			}
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: want 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decodeI18nJSON(t, rec.Body.Bytes(), &env)
			if env.Error.Code != "bad_request" {
				t.Errorf("error.code: want bad_request, got %q", env.Error.Code)
			}
		})
	}
}

// TestI18nUnavailable_EmptyI18nDir — when Deps.I18nDir is "", every
// route surfaces 503 `unavailable` so the admin UI can render the
// "Set RAILBASE_I18N_DIR" hint instead of a generic 5xx.
func TestI18nUnavailable_EmptyI18nDir(t *testing.T) {
	d := &Deps{I18nDir: ""}
	r := newI18nRouter(d)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/i18n/locales"},
		{http.MethodGet, "/i18n/files/en"},
		{http.MethodPut, "/i18n/files/en"},
		{http.MethodDelete, "/i18n/files/en"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader([]byte(`{"entries":{}}`)))
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
			decodeI18nJSON(t, rec.Body.Bytes(), &env)
			if env.Error.Code != "unavailable" {
				t.Errorf("error.code: want unavailable, got %q", env.Error.Code)
			}
		})
	}
}

// TestI18nFilePut_RoundtripPrettyPrinted — verify the on-disk file is
// 2-space-indented JSON with sorted keys + a trailing newline. Diff-
// friendliness matters because operators commit these files to git.
func TestI18nFilePut_RoundtripPrettyPrinted(t *testing.T) {
	dir := t.TempDir()
	d := &Deps{I18nDir: dir}
	r := newI18nRouter(d)

	// Intentionally unsorted input — Go's json.Marshal sorts
	// map[string]string keys at encode time so the on-disk order
	// should be alphabetical regardless.
	entries := map[string]string{
		"zeta":  "z",
		"alpha": "a",
		"mid":   "m",
	}
	body, _ := json.Marshal(map[string]any{"entries": entries})
	req := httptest.NewRequest(http.MethodPut, "/i18n/files/de", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}

	disk, err := os.ReadFile(filepath.Join(dir, "de.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Expected exact bytes — 2-space indent, sorted keys, trailing \n.
	want := "{\n  \"alpha\": \"a\",\n  \"mid\": \"m\",\n  \"zeta\": \"z\"\n}\n"
	if string(disk) != want {
		t.Errorf("on-disk shape mismatch:\nwant: %q\ngot:  %q", want, string(disk))
	}
}
