// v1.7.36b — `?token=` query-param fallback for raw EventSource
// clients in strict (PB-compat) mode. The browser EventSource API
// cannot set headers, so the PB JS SDK passes the session JWT via
// the URL. These tests cover the gating + precedence of the new
// fallback without needing a live session store: the extractor
// stops at the token-string level and the lookup-via-DB path is
// covered by the embed_pg e2e tests elsewhere.

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railbase/railbase/internal/compat"
)

// strictReq builds a GET with the active compat mode stamped into
// ctx — same wiring the compat resolver middleware performs at the
// chi-router level in production.
func strictReq(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(compat.With(r.Context(), compat.ModeStrict))
}

func nativeReq(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(compat.With(r.Context(), compat.ModeNative))
}

func TestExtractToken_QueryFallback_AcceptedInStrictGET(t *testing.T) {
	opts := options{queryParam: "token"}
	r := strictReq(http.MethodGet, "/api/realtime?token=session-jwt-abc")
	got, ok := extractTokenWithOpts(r, opts)
	if !ok || got != "session-jwt-abc" {
		t.Errorf("strict+GET+?token=: got=%q ok=%v, want session-jwt-abc/true", got, ok)
	}
}

func TestExtractToken_QueryFallback_IgnoredInNativeMode(t *testing.T) {
	opts := options{queryParam: "token"}
	r := nativeReq(http.MethodGet, "/api/realtime?token=session-jwt-abc")
	if _, ok := extractTokenWithOpts(r, opts); ok {
		t.Errorf("native mode must NOT honour ?token= — query-auth is strict-only")
	}
}

func TestExtractToken_QueryFallback_IgnoredOnPOST(t *testing.T) {
	opts := options{queryParam: "token"}
	r := strictReq(http.MethodPost, "/api/realtime?token=session-jwt-abc")
	if _, ok := extractTokenWithOpts(r, opts); ok {
		t.Errorf("POST must NOT honour ?token= — CSRF / Referer-leak risk")
	}
}

func TestExtractToken_QueryFallback_BearerWinsWhenBothPresent(t *testing.T) {
	opts := options{queryParam: "token"}
	r := strictReq(http.MethodGet, "/api/realtime?token=query-tok")
	r.Header.Set("Authorization", "Bearer header-tok")
	got, ok := extractTokenWithOpts(r, opts)
	if !ok {
		t.Fatalf("expected ok with both present")
	}
	if got != "header-tok" {
		t.Errorf("Bearer header must beat ?token= fallback: got %q, want header-tok", got)
	}
}

func TestExtractToken_QueryFallback_CookieBeatsQuery(t *testing.T) {
	// Cookie auth represents the admin UI's authenticated state. If
	// somebody happens to forward a `?token=` from a link, the
	// cookie still wins — same precedence as Bearer.
	opts := options{queryParam: "token"}
	r := strictReq(http.MethodGet, "/api/realtime?token=query-tok")
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "cookie-tok"})
	got, ok := extractTokenWithOpts(r, opts)
	if !ok || got != "cookie-tok" {
		t.Errorf("cookie must beat ?token= fallback: got=%q ok=%v", got, ok)
	}
}

func TestExtractToken_QueryFallback_EmptyValueIgnored(t *testing.T) {
	opts := options{queryParam: "token"}
	r := strictReq(http.MethodGet, "/api/realtime?token=")
	if _, ok := extractTokenWithOpts(r, opts); ok {
		t.Errorf("empty ?token= must NOT activate the fallback")
	}
}

func TestExtractToken_QueryFallback_WhitespaceTrimmed(t *testing.T) {
	// strings.TrimSpace mirrors the Bearer-header treatment so a
	// stray space in a URL builder doesn't produce a token that
	// fails session lookup with a confusing error.
	opts := options{queryParam: "token"}
	r := strictReq(http.MethodGet, "/api/realtime?token=%20tok-with-spaces%20")
	got, ok := extractTokenWithOpts(r, opts)
	if !ok || got != "tok-with-spaces" {
		t.Errorf("whitespace trim: got=%q ok=%v, want tok-with-spaces/true", got, ok)
	}
}

func TestExtractToken_QueryFallback_DisabledWhenOptionEmpty(t *testing.T) {
	// Regression guard: zero-value options behave like the old
	// extractToken — no query-param surface even in strict+GET.
	r := strictReq(http.MethodGet, "/api/realtime?token=session-jwt-abc")
	if _, ok := extractTokenWithOpts(r, options{}); ok {
		t.Errorf("default options must not honour ?token= — opt-in only")
	}
}

func TestExtractToken_QueryFallback_StrictDefaultsWhenCtxUnset(t *testing.T) {
	// compat.From returns ModeStrict when no mode is stamped — the
	// "safe default" documented in package compat. Confirm the
	// extractor honours that for symmetry: a request that bypassed
	// the compat middleware still benefits from query-auth in GET.
	// (Production wires compat.Middleware before authmw, so this is
	// a defence-in-depth check.)
	opts := options{queryParam: "token"}
	r := httptest.NewRequest(http.MethodGet, "/api/realtime?token=session-jwt-abc", nil)
	got, ok := extractTokenWithOpts(r, opts)
	if !ok || got != "session-jwt-abc" {
		t.Errorf("unset compat ctx (defaults to strict): got=%q ok=%v", got, ok)
	}
}

func TestExtractToken_QueryFallback_BothModeIgnored(t *testing.T) {
	// `both` mode runs PB + native side-by-side; the query-auth
	// surface is strict-only, so `both` does NOT activate it. Native
	// clients hitting /v1/* paths can always set headers; PB clients
	// hitting /api/* paths in `both` mode are presumed to use header
	// auth too (they have a real SDK in front of them).
	opts := options{queryParam: "token"}
	r := httptest.NewRequest(http.MethodGet, "/api/realtime?token=session-jwt-abc", nil)
	r = r.WithContext(compat.With(r.Context(), compat.ModeBoth))
	if _, ok := extractTokenWithOpts(r, opts); ok {
		t.Errorf("compat mode `both` must NOT honour ?token= — strict-only surface")
	}
}

// === Middleware-level integration tests ===
//
// These exercise the full NewWithAPI(..., WithQueryParamFallback)
// pipeline including the compat-mode ctx flow. They DO NOT cover
// the session.Store lookup path (that requires a live DB; see the
// embed_pg e2e suite). The assertion target is "does the middleware
// honour or reject the ?token= surface" — i.e. is the token
// extracted from the URL? We piggy-back on a never-matching session
// store: the lookup fails → the request remains anonymous, which
// is the SAME observable outcome as "no token extracted". The
// distinguishing test is the strict-GET case: the lookup IS
// attempted, which we observe via an unrelated assertion on the
// next handler.
//
// To make the path observable WITHOUT a DB, we pass a nil session
// store and confirm the middleware's panic-free handling of the
// "extracted but lookup fails" branch. That's not feasible — the
// store is dereferenced unconditionally. Instead we keep the
// middleware-flow tests in the embed_pg e2e file and limit unit
// coverage to the extractor.
