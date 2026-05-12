// Package i18n is the server-side internationalisation layer
// (§3.9.3 / docs/22).
//
// Three concepts:
//
//   - Locale — BCP-47 tag (e.g. "en", "ru", "pt-BR"). Parsed cheaply
//     into language + region.
//   - Catalog — map[Locale]Bundle where Bundle is the flat
//     key→template map for that locale.
//   - Negotiator — picks the best supported locale for a request
//     given the Accept-Language header.
//
// Lookup is a three-step fallback:
//   1. The requested locale's bundle.
//   2. The base language (e.g. "pt" for "pt-BR").
//   3. The default fallback locale (operator-configured; usually "en").
//
// Template interpolation: `{name}` placeholders are replaced with
// param values. No HTML escaping (handlers / templates do that
// downstream). Missing params render as `{name}` so the gap is
// visible.
//
// Pluralization: minimal English-grade rule ("one" / "other"); full
// CLDR plural categories deferred to v1.5.x once we have real
// content driving the requirement.
//
// What's deliberately NOT in this milestone:
//   - ICU MessageFormat parser (`{count, plural, =0 {none} ...}`).
//   - Schema `.Translatable()` + `_translations` table.
//   - Per-tenant overrides.
//   - Hot-reload via fsnotify (manual Reload() works; auto-watch v1.5.x).
//   - JS hooks `$t()` bindings (waits for §3.4 hooks API extension).
//   - Date/number/currency formatting (browser Intl handles client-side).
package i18n

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Locale is a normalised BCP-47 tag: lowercased language, optional
// uppercase region. Tags are stored canonically so map lookups are
// stable across input variations ("EN-us" → "en-US").
type Locale string

// Canonical normalises raw input to language[-REGION]. Empty input
// returns empty (no default applied here — Negotiator owns defaults).
func Canonical(raw string) Locale {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Split on '-' or '_' (some platforms emit underscores).
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return ""
	}
	lang := strings.ToLower(parts[0])
	if len(parts) == 1 {
		return Locale(lang)
	}
	region := strings.ToUpper(parts[1])
	return Locale(lang + "-" + region)
}

// Base returns the language portion of a locale. "pt-BR" → "pt".
func (l Locale) Base() Locale {
	s := string(l)
	if i := strings.IndexByte(s, '-'); i >= 0 {
		return Locale(s[:i])
	}
	return l
}

// Dir reports the text direction for a locale. Returns "rtl" for
// Arabic, Hebrew, Persian, Urdu (the four major RTL scripts in
// production use); "ltr" for everything else.
func (l Locale) Dir() string {
	switch l.Base() {
	case "ar", "he", "fa", "ur":
		return "rtl"
	}
	return "ltr"
}

// --- Bundle ---

// Bundle is the flat key→template map for one locale.
type Bundle map[string]string

// Get returns (template, true) if key is present; else ("", false).
func (b Bundle) Get(key string) (string, bool) {
	if b == nil {
		return "", false
	}
	v, ok := b[key]
	return v, ok
}

// --- Catalog ---

// Catalog holds bundles indexed by locale. Goroutine-safe via internal
// RWMutex; reads are lock-free under contention via a snapshot pointer.
type Catalog struct {
	defaultLocale Locale
	supported     []Locale

	mu      sync.RWMutex
	bundles map[Locale]Bundle
}

// NewCatalog constructs an empty catalog. defaultLocale is what
// Negotiator falls back to when nothing in Accept-Language matches.
// supported is the canonical list of locales operators want to
// announce as available; Negotiator restricts matches to this set.
func NewCatalog(defaultLocale Locale, supported []Locale) *Catalog {
	c := &Catalog{
		defaultLocale: Canonical(string(defaultLocale)),
		supported:     make([]Locale, 0, len(supported)),
		bundles:       make(map[Locale]Bundle),
	}
	for _, l := range supported {
		c.supported = append(c.supported, Canonical(string(l)))
	}
	if c.defaultLocale == "" {
		c.defaultLocale = "en"
	}
	return c
}

// DefaultLocale reports the configured fallback.
func (c *Catalog) DefaultLocale() Locale { return c.defaultLocale }

// Supported reports the announced locale list (canonical).
func (c *Catalog) Supported() []Locale {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Locale, len(c.supported))
	copy(out, c.supported)
	return out
}

// SetBundle installs a bundle for a locale. Overwrites any existing
// entry for that locale.
func (c *Catalog) SetBundle(l Locale, b Bundle) {
	l = Canonical(string(l))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bundles[l] = b
}

// Bundle returns the bundle for a locale, or nil when absent.
func (c *Catalog) Bundle(l Locale) Bundle {
	l = Canonical(string(l))
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bundles[l]
}

// T returns the rendered translation for key in the given locale.
// Lookup order:
//
//	1. requested locale (exact)
//	2. requested locale's base (e.g. "pt" for "pt-BR")
//	3. default locale
//
// Missing-everywhere → returns the key itself so the gap is visible
// in UI. Interpolation: `{name}` placeholders are replaced with
// params[name]; missing params render literally.
func (c *Catalog) T(locale Locale, key string, params map[string]any) string {
	locale = Canonical(string(locale))
	c.mu.RLock()
	tpl, ok := c.lookupLocked(locale, key)
	c.mu.RUnlock()
	if !ok {
		return key
	}
	if len(params) == 0 {
		return tpl
	}
	return interpolate(tpl, params)
}

// Plural picks "one" or "other" form for an English-grade rule. The
// catalog convention: keys are `<base>.one` and `<base>.other`. For
// non-English languages add the appropriate forms in the bundle; this
// helper only branches on count==1.
//
// Example bundle:
//
//	"comments.one":   "1 comment",
//	"comments.other": "{count} comments",
//
// Call:
//
//	t := cat.Plural(locale, "comments", count, map[string]any{"count": count})
func (c *Catalog) Plural(locale Locale, baseKey string, count int, params map[string]any) string {
	form := "other"
	if count == 1 {
		form = "one"
	}
	return c.T(locale, baseKey+"."+form, params)
}

func (c *Catalog) lookupLocked(locale Locale, key string) (string, bool) {
	if b, ok := c.bundles[locale]; ok {
		if v, ok := b.Get(key); ok {
			return v, true
		}
	}
	if base := locale.Base(); base != locale {
		if b, ok := c.bundles[base]; ok {
			if v, ok := b.Get(key); ok {
				return v, true
			}
		}
	}
	if c.defaultLocale != locale && c.defaultLocale != locale.Base() {
		if b, ok := c.bundles[c.defaultLocale]; ok {
			if v, ok := b.Get(key); ok {
				return v, true
			}
		}
	}
	return "", false
}

// --- Loader ---

// LoadDir reads every `<locale>.json` file in dir (flat key/value
// maps) and installs the resulting bundles in the catalog. Files
// whose name doesn't parse as a locale are skipped silently.
//
// Returns the list of locales that were loaded. Caller can compare
// against catalog.Supported() to see if any expected ones are missing.
func (c *Catalog) LoadDir(dir string) ([]Locale, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // optional dir, not an error
		}
		return nil, fmt.Errorf("i18n: read dir: %w", err)
	}
	var loaded []Locale
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw := strings.TrimSuffix(name, ".json")
		loc := Canonical(raw)
		if loc == "" {
			continue
		}
		// Cached read+parse — see internal/i18n/cache.go. On a warm
		// entry this is an in-process map lookup; on a cold one it
		// runs the original loadBundleFile body under singleflight.
		b, err := readBundleFileCached(filepath.Join(dir, name))
		if err != nil {
			return loaded, fmt.Errorf("i18n: load %s: %w", name, err)
		}
		c.SetBundle(loc, b)
		loaded = append(loaded, loc)
	}
	return loaded, nil
}

// LoadFS reads bundles from an embedded fs.FS rooted at dir. Same
// rules as LoadDir, but works against `go:embed`-ed assets so the
// railbase binary ships with default bundles built-in.
func (c *Catalog) LoadFS(fsys fs.FS, dir string) ([]Locale, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("i18n: read embedded dir %q: %w", dir, err)
	}
	var loaded []Locale
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw := strings.TrimSuffix(name, ".json")
		loc := Canonical(raw)
		if loc == "" {
			continue
		}
		// Cached read+parse — see internal/i18n/cache.go.
		b, err := readBundleFSCached(fsys, filepath.Join(dir, name))
		if err != nil {
			return loaded, fmt.Errorf("i18n: read embedded %s: %w", name, err)
		}
		c.SetBundle(loc, b)
		loaded = append(loaded, loc)
	}
	return loaded, nil
}

func loadBundleFile(path string) (Bundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return b, nil
}

// --- Negotiator ---

// Negotiate picks the best supported locale for an Accept-Language
// header value. Algorithm:
//
//   1. Parse the header into (tag, quality) pairs.
//   2. Sort by descending quality (stable).
//   3. For each entry, try exact match against supported, then base.
//   4. Fall back to default.
//
// Accept-Language wildcards ("*") match the first supported entry.
func (c *Catalog) Negotiate(acceptLanguage string) Locale {
	tags := parseAcceptLanguage(acceptLanguage)
	for _, t := range tags {
		if t.tag == "*" {
			if len(c.supported) > 0 {
				return c.supported[0]
			}
			continue
		}
		want := Canonical(t.tag)
		for _, s := range c.supported {
			if s == want {
				return s
			}
		}
		// Base-language fallback: "pt-BR" requested, "pt" supported.
		base := want.Base()
		for _, s := range c.supported {
			if s == base {
				return s
			}
		}
		// Inverse: "pt" requested, "pt-BR" supported → match if any
		// supported locale shares the base.
		for _, s := range c.supported {
			if s.Base() == want {
				return s
			}
		}
	}
	return c.defaultLocale
}

type acceptLangTag struct {
	tag string
	q   float64
}

// parseAcceptLanguage parses the header per RFC 7231 §5.3.5. Quality
// defaults to 1.0 when q= is absent; invalid q-values are treated as
// 1.0 (lenient — the alternative is to drop the entry, which surprises
// operators when their malformed header silently disappears).
func parseAcceptLanguage(h string) []acceptLangTag {
	if h == "" {
		return nil
	}
	var out []acceptLangTag
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segs := strings.Split(part, ";")
		tag := strings.TrimSpace(segs[0])
		q := 1.0
		for _, s := range segs[1:] {
			s = strings.TrimSpace(s)
			if strings.HasPrefix(s, "q=") {
				var v float64
				if _, err := fmt.Sscanf(s[2:], "%f", &v); err == nil {
					q = v
				}
			}
		}
		out = append(out, acceptLangTag{tag: tag, q: q})
	}
	// Stable sort by descending q. Bubble-style — input is tiny (≤10).
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].q > out[i].q {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// --- context propagation ---

type ctxKey struct{}

// WithLocale stamps a locale into ctx. Middleware calls this after
// negotiation so handlers can pluck it back via FromContext.
func WithLocale(ctx context.Context, l Locale) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the locale stamped by WithLocale, or the empty
// locale when none is set. Handlers should treat empty as "use the
// catalog's default".
func FromContext(ctx context.Context) Locale {
	if v, ok := ctx.Value(ctxKey{}).(Locale); ok {
		return v
	}
	return ""
}

// T is the convenience handler-side helper. Resolves locale from ctx
// (or falls back to catalog default) and renders the key.
func T(ctx context.Context, c *Catalog, key string, params map[string]any) string {
	l := FromContext(ctx)
	if l == "" {
		l = c.DefaultLocale()
	}
	return c.T(l, key, params)
}

// --- translatable field helpers ---

// PickLocaleValue resolves the best string for a translatable JSONB
// field. `values` is a map[locale]string drawn from the column (e.g.
// `{"en":"Hello","ru":"Привет"}`); requested is the locale the request
// asked for. The lookup order mirrors Catalog.T:
//
//  1. exact match on requested locale            ("ru" → ru)
//  2. base language of requested locale          ("ru-RU" → ru)
//  3. catalog's DefaultLocale                    ("en" — config default)
//  4. first key in alphabetical order            (deterministic last resort)
//  5. empty string                               (truly empty input)
//
// Returns the picked value as-is — callers are responsible for any
// escaping / interpolation appropriate to their context.
func (c *Catalog) PickLocaleValue(requested Locale, values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	requested = Canonical(string(requested))
	if v, ok := values[string(requested)]; ok {
		return v
	}
	if base := requested.Base(); base != requested {
		if v, ok := values[string(base)]; ok {
			return v
		}
	}
	def := c.DefaultLocale()
	if def != "" && def != requested {
		if v, ok := values[string(def)]; ok {
			return v
		}
		if base := def.Base(); base != def {
			if v, ok := values[string(base)]; ok {
				return v
			}
		}
	}
	// Deterministic last resort: smallest key alphabetically. Sort
	// inline (n is usually < 10) so we don't pull in sort.Strings here.
	var pick string
	first := true
	for k := range values {
		if first || k < pick {
			pick = k
			first = false
		}
	}
	return values[pick]
}

// --- interpolation ---

// interpolate replaces `{name}` placeholders with params[name]. Plain
// string substitution — no HTML escaping (handlers downstream are
// responsible for context-aware encoding). Unknown placeholders render
// literally so the gap is visible.
func interpolate(tpl string, params map[string]any) string {
	if !strings.Contains(tpl, "{") {
		return tpl
	}
	var b strings.Builder
	b.Grow(len(tpl))
	i := 0
	for i < len(tpl) {
		j := strings.IndexByte(tpl[i:], '{')
		if j < 0 {
			b.WriteString(tpl[i:])
			break
		}
		b.WriteString(tpl[i : i+j])
		i += j
		k := strings.IndexByte(tpl[i:], '}')
		if k < 0 {
			b.WriteString(tpl[i:])
			break
		}
		name := tpl[i+1 : i+k]
		if v, ok := params[name]; ok {
			fmt.Fprintf(&b, "%v", v)
		} else {
			b.WriteString(tpl[i : i+k+1])
		}
		i += k + 1
	}
	return b.String()
}
