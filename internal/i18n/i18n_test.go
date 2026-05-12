package i18n

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	embeddedi18n "github.com/railbase/railbase/internal/i18n/embed"
)

// --- Canonical ---

func TestCanonical(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"en", "en"},
		{"EN", "en"},
		{"en-US", "en-US"},
		{"EN-us", "en-US"},
		{"en_GB", "en-GB"},
		{"pt-BR", "pt-BR"},
		{"  fr  ", "fr"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Canonical(c.in); string(got) != c.want {
			t.Errorf("Canonical(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- Base / Dir ---

func TestBase(t *testing.T) {
	if Locale("pt-BR").Base() != "pt" {
		t.Error("pt-BR base")
	}
	if Locale("en").Base() != "en" {
		t.Error("en base unchanged")
	}
}

func TestDir(t *testing.T) {
	cases := map[Locale]string{
		"en":    "ltr",
		"ru":    "ltr",
		"ar":    "rtl",
		"ar-SA": "rtl",
		"he":    "rtl",
		"fa":    "rtl",
		"ur":    "rtl",
	}
	for l, want := range cases {
		if got := l.Dir(); got != want {
			t.Errorf("Dir(%q) = %q, want %q", l, got, want)
		}
	}
}

// --- T / interpolation ---

func TestT_ExactMatch(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	c.SetBundle("en", Bundle{"hi": "Hello {name}"})
	got := c.T("en", "hi", map[string]any{"name": "Ada"})
	if got != "Hello Ada" {
		t.Errorf("got %q", got)
	}
}

func TestT_BaseLanguageFallback(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "pt"})
	c.SetBundle("pt", Bundle{"hi": "Olá"})
	if got := c.T("pt-BR", "hi", nil); got != "Olá" {
		t.Errorf("pt-BR should fall back to pt; got %q", got)
	}
}

func TestT_DefaultLocaleFallback(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	c.SetBundle("en", Bundle{"hi": "Hello"})
	c.SetBundle("ru", Bundle{})
	if got := c.T("ru", "hi", nil); got != "Hello" {
		t.Errorf("ru missing key should fall back to en; got %q", got)
	}
}

func TestT_MissingEverywhereReturnsKey(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	c.SetBundle("en", Bundle{})
	if got := c.T("en", "nope", nil); got != "nope" {
		t.Errorf("missing key should echo; got %q", got)
	}
}

func TestT_InterpolatesMultipleParams(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	c.SetBundle("en", Bundle{"tpl": "{a} + {b} = {c}"})
	got := c.T("en", "tpl", map[string]any{"a": 1, "b": 2, "c": 3})
	if got != "1 + 2 = 3" {
		t.Errorf("got %q", got)
	}
}

func TestT_MissingParamRendersLiteral(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	c.SetBundle("en", Bundle{"tpl": "Hi {name}, your code is {code}"})
	got := c.T("en", "tpl", map[string]any{"name": "Ada"})
	if !strings.Contains(got, "{code}") {
		t.Errorf("missing param should render literally; got %q", got)
	}
}

func TestT_MalformedTemplateNoCrash(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	c.SetBundle("en", Bundle{"tpl": "broken {name without close"})
	if got := c.T("en", "tpl", map[string]any{"name": "x"}); got == "" {
		t.Error("expected best-effort output, not empty")
	}
}

// --- Plural ---

func TestPlural(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	c.SetBundle("en", Bundle{
		"comments.one":   "1 comment",
		"comments.other": "{count} comments",
	})
	cases := []struct {
		count int
		want  string
	}{
		{1, "1 comment"},
		{0, "0 comments"},
		{5, "5 comments"},
	}
	for _, tc := range cases {
		got := c.Plural("en", "comments", tc.count, map[string]any{"count": tc.count})
		if got != tc.want {
			t.Errorf("count=%d got %q want %q", tc.count, got, tc.want)
		}
	}
}

// --- Negotiate ---

func TestNegotiate_ExactMatch(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru", "fr"})
	if got := c.Negotiate("ru,en;q=0.5"); got != "ru" {
		t.Errorf("got %q, want ru", got)
	}
}

func TestNegotiate_BaseLanguage(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "pt"})
	if got := c.Negotiate("pt-BR,en;q=0.5"); got != "pt" {
		t.Errorf("got %q, want pt (base of pt-BR)", got)
	}
}

func TestNegotiate_InverseBase(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "pt-BR"})
	if got := c.Negotiate("pt,en;q=0.5"); got != "pt-BR" {
		t.Errorf("got %q, want pt-BR (matched on base)", got)
	}
}

func TestNegotiate_QualityOrdering(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru", "fr"})
	// fr has lower q than ru; ru wins.
	if got := c.Negotiate("fr;q=0.3, ru;q=0.9, en;q=0.1"); got != "ru" {
		t.Errorf("got %q, want ru (highest q)", got)
	}
}

func TestNegotiate_DefaultFallback(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	if got := c.Negotiate("zh,ko;q=0.5"); got != "en" {
		t.Errorf("got %q, want en (no match → default)", got)
	}
}

func TestNegotiate_Wildcard(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	if got := c.Negotiate("*"); got != "en" {
		t.Errorf("wildcard should pick first supported: %q", got)
	}
}

func TestNegotiate_EmptyHeader(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	if got := c.Negotiate(""); got != "en" {
		t.Errorf("empty Accept-Language → default; got %q", got)
	}
}

// --- Context propagation ---

func TestContext(t *testing.T) {
	ctx := WithLocale(context.Background(), "ru")
	if FromContext(ctx) != "ru" {
		t.Error("locale not stamped")
	}
	if FromContext(context.Background()) != "" {
		t.Error("empty ctx should return empty locale")
	}
}

// --- LoadDir / embedded ---

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "en.json"), []byte(`{"greet":"Hi"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ru.json"), []byte(`{"greet":"Привет"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-locale.txt"), []byte(`ignored`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewCatalog("en", []Locale{"en", "ru"})
	loaded, err := c.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Errorf("loaded count: got %d, want 2 (skipped .txt)", len(loaded))
	}
	if got := c.T("ru", "greet", nil); got != "Привет" {
		t.Errorf("got %q", got)
	}
}

func TestLoadFS_Embedded(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	loaded, err := c.LoadFS(embeddedi18n.FS, ".")
	if err != nil {
		t.Fatalf("load embedded: %v", err)
	}
	if len(loaded) < 2 {
		t.Errorf("embedded loaded count: got %d, want >=2", len(loaded))
	}
	// One canonical built-in key should be present.
	if got := c.T("en", "errors.required", map[string]any{"field": "email"}); !strings.Contains(got, "email") {
		t.Errorf("embedded en bundle missing or wrong shape: %q", got)
	}
	if got := c.T("ru", "errors.required", map[string]any{"field": "email"}); !strings.Contains(got, "Поле") {
		t.Errorf("embedded ru bundle missing or wrong shape: %q", got)
	}
}

// --- Middleware ---

func TestMiddleware_StampsLocale(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	c.SetBundle("ru", Bundle{"x": "y"})
	var seen Locale
	mw := Middleware(c)
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Language", "ru,en;q=0.5")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "ru" {
		t.Errorf("got %q, want ru", seen)
	}
}

func TestMiddleware_LangQueryOverridesHeader(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	var seen Locale
	mw := Middleware(c)
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/?lang=ru", nil)
	req.Header.Set("Accept-Language", "fr")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "ru" {
		t.Errorf("got %q, want ru (query overrides)", seen)
	}
}

func TestMiddleware_UnsupportedLangFallsThrough(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	var seen Locale
	mw := Middleware(c)
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/?lang=zz", nil)
	req.Header.Set("Accept-Language", "ru")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "ru" {
		t.Errorf("unsupported lang should fall back to header negotiation; got %q", seen)
	}
}

// --- BundleHandler ---

func TestBundleHandler_ReturnsJSON(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	c.SetBundle("en", Bundle{"a": "A", "auth.x": "X"})
	h := BundleHandler(c)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/i18n/en", nil)
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	var got struct {
		Locale string         `json:"locale"`
		Dir    string         `json:"dir"`
		Keys   map[string]any `json:"keys"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Locale != "en" || got.Dir != "ltr" {
		t.Errorf("locale/dir: got %q/%q", got.Locale, got.Dir)
	}
	if got.Keys["a"] != "A" {
		t.Errorf("keys: %v", got.Keys)
	}
}

func TestBundleHandler_PrefixFilter(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	c.SetBundle("en", Bundle{"a": "A", "auth.x": "X", "auth.y": "Y"})
	h := BundleHandler(c)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/i18n/en?prefix=auth", nil)
	h(rec, req)
	var got struct {
		Keys map[string]any `json:"keys"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if _, ok := got.Keys["a"]; ok {
		t.Error("non-prefix key leaked")
	}
	if got.Keys["auth.x"] != "X" {
		t.Errorf("prefix filter missed key: %v", got.Keys)
	}
}

func TestBundleHandler_FallsBackOnMissingLocale(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	c.SetBundle("en", Bundle{"a": "A"})
	h := BundleHandler(c)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/i18n/fr", nil)
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	// Falls back to default (en) so SPA gets SOMETHING.
	if !strings.Contains(rec.Body.String(), `"a":"A"`) {
		t.Errorf("expected en fallback bundle: %s", rec.Body.String())
	}
}

// --- Top-level T helper ---

func TestTopLevelT(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	c.SetBundle("ru", Bundle{"hi": "Привет"})
	ctx := WithLocale(context.Background(), "ru")
	if got := T(ctx, c, "hi", nil); got != "Привет" {
		t.Errorf("got %q", got)
	}
	// No locale in ctx → default.
	c.SetBundle("en", Bundle{"hi": "Hello"})
	if got := T(context.Background(), c, "hi", nil); got != "Hello" {
		t.Errorf("default fallback: got %q", got)
	}
}
