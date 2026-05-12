package security

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestAntiBot constructs an AntiBot wired to a discard logger so
// noisy event lines don't pollute `go test` output. Tests that want
// to assert on a logged event use newCapturingAntiBot instead.
func newTestAntiBot(cfg AntiBotConfig) *AntiBot {
	return NewAntiBot(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// nextOK is a tiny "next" handler that records whether the chain
// reached it and writes a 200 with body "ok". Used to distinguish
// pass-through from short-circuit in honeypot/UA tests.
type nextOK struct{ called bool }

func (n *nextOK) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	n.called = true
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func TestAntiBot_Disabled_PassThrough(t *testing.T) {
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = false
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	// Even an obviously-scripted UA on an auth path passes when
	// Enabled is false — the middleware is a pure no-op.
	req := httptest.NewRequest("GET", "/api/auth/sign-in", nil)
	req.Header.Set("User-Agent", "curl/8.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !next.called {
		t.Fatal("next handler not called when Enabled=false")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

func TestAntiBot_HoneypotEmpty_PassThrough(t *testing.T) {
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	// POST a form with NONE of the honeypot fields set → must pass.
	body := strings.NewReader("email=a@b.com&password=secret")
	req := httptest.NewRequest("POST", "/api/collections/users/records", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !next.called {
		t.Fatal("honeypot-empty form must pass through")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

func TestAntiBot_HoneypotPresent_200OK_BenignBody(t *testing.T) {
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	// "website" is in the default honeypot list. A bot would
	// dutifully fill it in; we 200 + `{}` and DON'T forward.
	body := strings.NewReader("email=bot@x.com&website=https://spam.example")
	req := httptest.NewRequest("POST", "/api/collections/users/records", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if next.called {
		t.Fatal("next handler should NOT be called when honeypot fires")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (bot bait)", rec.Code)
	}
	if got := rec.Body.String(); got != "{}" {
		t.Errorf("body: got %q, want %q", got, "{}")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want application/json", ct)
	}
}

func TestAntiBot_BadUA_ForbiddenOnAuthPath(t *testing.T) {
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	// curl/ UA on /api/auth/sign-in → 403.
	req := httptest.NewRequest("POST", "/api/auth/sign-in", nil)
	req.Header.Set("User-Agent", "curl/8.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if next.called {
		t.Fatal("UA-blocked request must NOT reach next handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rec.Code)
	}
	// Body should NOT leak detail about WHY.
	if strings.Contains(rec.Body.String(), "ua") ||
		strings.Contains(rec.Body.String(), "user") {
		t.Errorf("body leaks UA-rejection detail: %q", rec.Body.String())
	}
}

func TestAntiBot_BadUA_PassThroughOnPublicPath(t *testing.T) {
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	// curl/ UA on /api/collections/posts/records (NOT under
	// UAEnforcePaths) → pass. The UA check is intentionally
	// scoped to enumeration-vulnerable endpoints only.
	req := httptest.NewRequest("GET", "/api/collections/posts/records", nil)
	req.Header.Set("User-Agent", "curl/8.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !next.called {
		t.Fatal("UA check fired on non-auth path; expected pass-through")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

func TestAntiBot_LargeBody_413(t *testing.T) {
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	// 2 MiB body — exceeds honeypotBodyCap (1 MiB). MaxBytesReader
	// trips ParseForm; we short-circuit with 413 instead of
	// letting downstream OOM on the read.
	huge := strings.Repeat("a", 2<<20)
	body := strings.NewReader("email=x@y.com&junk=" + huge)
	req := httptest.NewRequest("POST", "/api/collections/users/records", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if next.called {
		t.Fatal("oversized body must short-circuit before next handler")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", rec.Code)
	}
}

func TestAntiBot_UpdateConfig_TakesEffect(t *testing.T) {
	// Start with a config that does NOT block "weirdbot/1.0".
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	cfg.RejectUAs = []string{"curl/"} // narrow list
	a := newTestAntiBot(cfg)

	next1 := &nextOK{}
	h := a.Middleware(next1)
	req := httptest.NewRequest("POST", "/api/auth/sign-in", nil)
	req.Header.Set("User-Agent", "weirdbot/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !next1.called {
		t.Fatal("weirdbot/1.0 blocked under initial config (want pass)")
	}

	// Hot-swap config to ALSO reject weirdbot.
	updated := DefaultAntiBotConfig()
	updated.Enabled = true
	updated.RejectUAs = []string{"curl/", "weirdbot"}
	a.UpdateConfig(updated)

	// Same request, fresh next handler — must now be 403'd.
	next2 := &nextOK{}
	h2 := a.Middleware(next2)
	req2 := httptest.NewRequest("POST", "/api/auth/sign-in", nil)
	req2.Header.Set("User-Agent", "weirdbot/1.0")
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if next2.called {
		t.Fatal("post-update: next handler called for weirdbot (want 403)")
	}
	if rec2.Code != http.StatusForbidden {
		t.Errorf("post-update status: got %d, want 403", rec2.Code)
	}
}

func TestAntiBot_EmptyUA_OnAuthPath_Blocked(t *testing.T) {
	// Empty UA on /api/auth/* is itself suspicious — legitimate
	// browsers and SDKs all send a UA.
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	req := httptest.NewRequest("POST", "/api/auth/sign-in", nil)
	// No User-Agent header set.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if next.called {
		t.Fatal("empty UA on auth path reached next handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rec.Code)
	}
}

func TestAntiBot_HoneypotJSONBody_PassThrough(t *testing.T) {
	// JSON bodies are out-of-scope for honeypot (SDK-driven,
	// no HTML form a bot would scrape). Even if the JSON contains
	// a key matching a honeypot name, we pass through.
	cfg := DefaultAntiBotConfig()
	cfg.Enabled = true
	a := newTestAntiBot(cfg)
	next := &nextOK{}
	h := a.Middleware(next)

	body := strings.NewReader(`{"email":"a@b.com","website":"https://x.example"}`)
	req := httptest.NewRequest("POST", "/api/collections/users/records", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !next.called {
		t.Fatal("JSON body wrongly tripped honeypot")
	}
}

// --- ParseStringList ---

func TestParseStringList_JSON(t *testing.T) {
	got, err := ParseStringList(`["a","b","c"]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("got %v, want [a b c]", got)
	}
}

func TestParseStringList_CSV(t *testing.T) {
	got, err := ParseStringList(" a, b ,c ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != "b" {
		t.Errorf("got %v, want [a b c]", got)
	}
}

func TestParseStringList_Empty(t *testing.T) {
	got, err := ParseStringList("")
	if err != nil || got != nil {
		t.Errorf("empty: got (%v, %v), want (nil, nil)", got, err)
	}
}
