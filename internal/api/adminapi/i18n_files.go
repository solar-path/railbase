package adminapi

// v1.7.20 §3.11 — admin endpoints backing the Translations editor screen.
// Closes one of the remaining §3.11 admin-UI screens ("Translations
// editor (coverage %, missing keys)").
//
// The i18n core (internal/i18n) ships embedded en + ru bundles in the
// binary and lets operators install per-locale overrides via
// `<I18nDir>/<locale>.json` files at runtime (catalog.LoadDir). This
// admin surface lets operators read + write those override files from
// the UI without SSH'ing into the box.
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /i18n/locales              list embedded + overrides + coverage stats
//	GET    /i18n/files/{locale}       return embedded + override bundles
//	PUT    /i18n/files/{locale}       write the override file (entries map)
//	DELETE /i18n/files/{locale}       remove the override file
//
// The `{locale}` URL param must match a BCP-47 subset regex
// (^[a-z]{2,3}(-[A-Z]{2})?$) — same as the v1.5.5 i18n core. Any other
// shape is rejected as 400 `bad_request` so traversal attempts like
// `../etc/passwd` or `/etc/passwd` never reach the filesystem layer.
//
// `I18nDir` wired from app.go in v1.7.21+. Until then the field is
// empty in production and every handler short-circuits with a 503
// `unavailable` envelope; tests inject a t.TempDir() via Deps directly.
//
// We deliberately read the embedded reference bundle (en.json) directly
// from `internal/i18n/embed.FS` rather than importing the runtime
// catalog. This keeps the admin surface decoupled from the live
// negotiator state — the UI shows what's on disk + what shipped in the
// binary; the runtime catalog is reloaded out-of-band when the
// operator restarts.

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	i18nembed "github.com/railbase/railbase/internal/i18n/embed"
)

// localeRegex is the BCP-47 subset accepted by every endpoint. Mirrors
// the spec literally: lowercase 2-3 letter language (ISO 639-1/2T),
// optional uppercase 2-letter region (ISO 3166-1 alpha-2). Rejects
// uppercase language tags ("EN"), lowercase regions ("en-us"), and
// any payload with path separators — the regex IS the entire path-
// safety check for this surface (no `..` slips possible).
var localeRegex = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

// referenceLocale is the canonical key universe — every key in
// `internal/i18n/embed/en.json` defines a translation slot. Coverage
// stats are computed against this set.
const referenceLocale = "en"

// mountI18nFiles registers the four translations-editor routes on r.
// Always registered: handlers return 503 when I18nDir is empty so the
// UI can detect "not configured" without a missing-route 404 — same
// pattern as mountHooksFiles.
func (d *Deps) mountI18nFiles(r chi.Router) {
	r.Get("/i18n/locales", d.i18nLocalesHandler)
	r.Get("/i18n/files/{locale}", d.i18nFileGetHandler)
	r.Put("/i18n/files/{locale}", d.i18nFilePutHandler)
	r.Delete("/i18n/files/{locale}", d.i18nFileDeleteHandler)
}

// i18nCoverage is the per-locale coverage row in the GET /i18n/locales
// envelope. `MissingKeys` is the alphabetically-sorted list of keys in
// the reference bundle that are absent from this locale's effective
// bundle (override if present, else embedded if present, else empty).
type i18nCoverage struct {
	TotalKeys   int      `json:"total_keys"`
	Translated  int      `json:"translated"`
	MissingKeys []string `json:"missing_keys"`
}

// i18nLocalesResponse is the wire shape for GET /i18n/locales.
type i18nLocalesResponse struct {
	Default   string                  `json:"default"`
	Supported []string                `json:"supported"`
	Embedded  []string                `json:"embedded"`
	Overrides []string                `json:"overrides"`
	Coverage  map[string]i18nCoverage `json:"coverage"`
}

// i18nFileResponse is the wire shape for GET /i18n/files/{locale}. The
// `Override` field is null when no override file exists on disk; the UI
// uses that to flip between "edit" and "create new" modes.
type i18nFileResponse struct {
	Locale   string            `json:"locale"`
	Embedded map[string]string `json:"embedded"`
	Override map[string]string `json:"override"`
}

// i18nFilePutRequest is the wire shape for PUT bodies. Empty `Entries`
// (zero-length map OR omitted) is treated as a delete request — the
// admin UI's "clear all" button uses this to drop the override file.
type i18nFilePutRequest struct {
	Entries map[string]string `json:"entries"`
}

// i18nLocalesHandler — GET /api/_admin/i18n/locales.
//
// Aggregates three sources: the embedded bundles compiled into the
// binary, the .json files in I18nDir, and the computed coverage stats.
//
// `total_keys` is fixed across all locales — it's the size of the
// reference (en) bundle's key universe. `translated` counts keys with
// a non-empty value in the locale's effective bundle (override if
// present, else embedded). `missing_keys` is the reference keys absent
// from the effective bundle, alphabetically sorted.
func (d *Deps) i18nLocalesHandler(w http.ResponseWriter, r *http.Request) {
	if d.I18nDir == "" {
		writeI18nUnavailable(w)
		return
	}

	embedded, refKeys, err := loadEmbeddedBundles()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load embedded bundles"))
		return
	}

	overrides, overrideBundles, err := loadOverrideBundles(d.I18nDir)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load override bundles"))
		return
	}

	// Build the supported list: union of embedded + overrides, sorted.
	// We dedupe via a set first because the same locale can appear in
	// both (e.g. an `en` override on top of the embedded `en`).
	supportedSet := make(map[string]struct{}, len(embedded)+len(overrides))
	for l := range embedded {
		supportedSet[l] = struct{}{}
	}
	for _, l := range overrides {
		supportedSet[l] = struct{}{}
	}
	supported := make([]string, 0, len(supportedSet))
	for l := range supportedSet {
		supported = append(supported, l)
	}
	sort.Strings(supported)

	// Coverage rows: compute against the reference key universe. For
	// each supported locale, the "effective bundle" is the override if
	// present, else the embedded bundle if present. We don't merge the
	// two — operators editing the override see exactly the keys they
	// shipped, and the embedded values render as a hint in the UI.
	coverage := make(map[string]i18nCoverage, len(supported))
	for _, l := range supported {
		var effective map[string]string
		if b, ok := overrideBundles[l]; ok {
			effective = b
		} else if b, ok := embedded[l]; ok {
			effective = b
		}
		coverage[l] = computeCoverage(refKeys, effective)
	}

	resp := i18nLocalesResponse{
		Default:   referenceLocale,
		Supported: supported,
		Embedded:  embedded.locales(),
		Overrides: overrides,
		Coverage:  coverage,
	}
	if resp.Embedded == nil {
		resp.Embedded = []string{}
	}
	if resp.Overrides == nil {
		resp.Overrides = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// i18nFileGetHandler — GET /api/_admin/i18n/files/{locale}.
//
// Returns the embedded bundle (may be empty when the locale wasn't
// shipped in the binary) and the override bundle (null when no file
// exists on disk). The UI overlays the two: the override is the
// editable copy; the embedded is the gray hint shown under each
// missing-from-override row.
func (d *Deps) i18nFileGetHandler(w http.ResponseWriter, r *http.Request) {
	if d.I18nDir == "" {
		writeI18nUnavailable(w)
		return
	}
	locale, ok := resolveI18nLocale(w, r)
	if !ok {
		return
	}

	embedded := readEmbeddedBundle(locale)

	override, err := readOverrideBundle(d.I18nDir, locale)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "read override bundle"))
		return
	}

	if embedded == nil {
		embedded = map[string]string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(i18nFileResponse{
		Locale:   locale,
		Embedded: embedded,
		Override: override, // nil → JSON null, by design
	})
}

// i18nFilePutHandler — PUT /api/_admin/i18n/files/{locale}.
//
// Writes the entries map to `<I18nDir>/<locale>.json` (pretty-printed,
// 2-space indent, sorted keys for stable diffs). Empty entries (zero-
// length map OR omitted field) is a synonym for DELETE — the admin UI's
// "clear all" button uses that.
//
// We marshal with sorted keys (json.Marshal sorts map[string]string by
// default in Go) and indent for diffability: operators commit these
// files to git and a stable order minimises noise across saves.
func (d *Deps) i18nFilePutHandler(w http.ResponseWriter, r *http.Request) {
	if d.I18nDir == "" {
		writeI18nUnavailable(w)
		return
	}
	locale, ok := resolveI18nLocale(w, r)
	if !ok {
		return
	}
	var req i18nFilePutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Empty body is fine for the delete-synonym path — treat as
		// "no entries".
		if !errors.Is(err, io.EOF) {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
			return
		}
	}
	abs := filepath.Join(d.I18nDir, locale+".json")
	if err := os.MkdirAll(d.I18nDir, 0o755); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create i18n dir"))
		return
	}

	// Empty entries → delete the override (synonym for DELETE).
	if len(req.Entries) == 0 {
		err := os.Remove(abs)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "remove override file"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Pretty-print with 2-space indent + sorted keys. json.Marshal
	// emits map[string]string with keys in sorted order by default
	// (since Go 1.12), so we don't need a manual sort here.
	body, err := json.MarshalIndent(req.Entries, "", "  ")
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "marshal entries"))
		return
	}

	// Atomic-ish write: temp file in the same dir + rename, so a
	// concurrent reload doesn't observe a half-written file.
	tmp, err := os.CreateTemp(d.I18nDir, ".rb-i18n-*.tmp")
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create temp file"))
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "write temp file"))
		return
	}
	// Trailing newline for unix-friendliness — operators editing the
	// file in vim won't see a "no newline at end of file" diff.
	if _, err := tmp.Write([]byte("\n")); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "write temp file newline"))
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "close temp file"))
		return
	}
	if err := os.Rename(tmpPath, abs); err != nil {
		_ = os.Remove(tmpPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rename temp file"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"locale":  locale,
		"entries": req.Entries,
	})
}

// i18nFileDeleteHandler — DELETE /api/_admin/i18n/files/{locale}.
//
// Removes the override file. 204 on success, 404 if the file didn't
// exist (mirrors hooks_files.go — the UI should reflect that the
// target wasn't there).
func (d *Deps) i18nFileDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if d.I18nDir == "" {
		writeI18nUnavailable(w)
		return
	}
	locale, ok := resolveI18nLocale(w, r)
	if !ok {
		return
	}
	abs := filepath.Join(d.I18nDir, locale+".json")
	if err := os.Remove(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "override file not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete override file"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// resolveI18nLocale extracts the {locale} URL param and validates it
// against localeRegex. Returns (locale, true) on success; on failure
// writes a 400 `bad_request` envelope and returns ok=false. This is
// the only path-safety check the i18n surface needs: the regex pins
// the shape to a BCP-47 subset, which by construction can't contain
// `..` or `/` segments.
func resolveI18nLocale(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "locale")
	if raw == "" {
		writeBadRequest(w, "locale is required")
		return "", false
	}
	if !localeRegex.MatchString(raw) {
		writeBadRequest(w, "invalid locale (expected BCP-47 like \"en\" or \"pt-BR\")")
		return "", false
	}
	return raw, true
}

// bundleSet is the embedded bundle index: locale → key/value map. We
// load this once per request from the //go:embed FS — it's tiny (< 100
// keys × 2 locales today) and the read is in-process so the cost is
// dominated by JSON parsing, which is still well under a millisecond.
type bundleSet map[string]map[string]string

// locales returns the bundle locales as a sorted slice. Wraps the
// map iteration so the GET /i18n/locales response has stable ordering
// across requests.
func (b bundleSet) locales() []string {
	out := make([]string, 0, len(b))
	for l := range b {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// loadEmbeddedBundles reads every .json file in the embedded FS and
// returns the resulting bundle set + the reference key universe (the
// set of keys in en.json). Errors only on malformed JSON — missing
// files surface as an empty bundle.
func loadEmbeddedBundles() (bundleSet, map[string]struct{}, error) {
	out := make(bundleSet)
	entries, err := fs.ReadDir(i18nembed.FS, ".")
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		if !localeRegex.MatchString(locale) {
			continue
		}
		data, err := fs.ReadFile(i18nembed.FS, e.Name())
		if err != nil {
			return nil, nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, nil, err
		}
		out[locale] = m
	}
	// Reference key universe = every key in en.json. If en.json is
	// missing (shouldn't happen — it's shipped in the binary) we fall
	// back to an empty set so coverage stats degrade gracefully.
	refKeys := make(map[string]struct{})
	if ref, ok := out[referenceLocale]; ok {
		for k := range ref {
			refKeys[k] = struct{}{}
		}
	}
	return out, refKeys, nil
}

// readEmbeddedBundle returns the embedded bundle for a single locale,
// or nil when no such file ships in the binary. Used by the per-file
// GET handler — the listing endpoint uses loadEmbeddedBundles for the
// full set.
func readEmbeddedBundle(locale string) map[string]string {
	data, err := fs.ReadFile(i18nembed.FS, locale+".json")
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// loadOverrideBundles reads every <locale>.json file in I18nDir,
// validates the filename against localeRegex, and returns the sorted
// locale list + the parsed bundle map. Missing dir is not an error
// (returns empty slice + empty map) — operators bootstrapping the
// system see "no overrides yet" not an error envelope.
func loadOverrideBundles(dir string) ([]string, map[string]map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []string{}, map[string]map[string]string{}, nil
		}
		return nil, nil, err
	}
	var locales []string
	bundles := make(map[string]map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		locale := strings.TrimSuffix(name, ".json")
		if !localeRegex.MatchString(locale) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			// Skip malformed files rather than 500ing — the listing
			// should still render the well-formed siblings. Operators
			// see the missing locale in the embedded-only column and
			// can diff the broken file out-of-band.
			continue
		}
		locales = append(locales, locale)
		bundles[locale] = m
	}
	sort.Strings(locales)
	return locales, bundles, nil
}

// readOverrideBundle returns the parsed override bundle for a single
// locale, or (nil, nil) when the file doesn't exist. JSON parse errors
// surface as a real error so the per-file GET can return 500 instead
// of silently swallowing — the listing endpoint's skip-malformed
// policy doesn't apply here because the operator explicitly asked for
// this file.
func readOverrideBundle(dir, locale string) (map[string]string, error) {
	abs := filepath.Join(dir, locale+".json")
	data, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		// `null` body parses successfully but leaves m nil. Treat as
		// empty so the UI doesn't get a JSON null back where it
		// expects an object.
		m = map[string]string{}
	}
	return m, nil
}

// computeCoverage returns the per-locale coverage row. `translated`
// counts reference keys that have a non-empty string in the effective
// bundle; `missing_keys` is the alphabetically-sorted complement.
func computeCoverage(refKeys map[string]struct{}, effective map[string]string) i18nCoverage {
	total := len(refKeys)
	missing := make([]string, 0)
	translated := 0
	for k := range refKeys {
		v, ok := effective[k]
		if ok && v != "" {
			translated++
		} else {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return i18nCoverage{
		TotalKeys:   total,
		Translated:  translated,
		MissingKeys: missing,
	}
}

// writeI18nUnavailable emits the 503 `unavailable` envelope used when
// I18nDir is empty. Mirrors writeHooksUnavailable — the admin UI
// detects this status to render a "Set RAILBASE_I18N_DIR" hint.
func writeI18nUnavailable(w http.ResponseWriter) {
	rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "i18n overrides dir not configured"))
}
