package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSRF_GetIsSafe(t *testing.T) {
	mw := CSRF(CSRFOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	// GET with session cookie but no CSRF header → still 200.
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "railbase_session", Value: "x"})
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("GET: got %d, want 200", rec.Code)
	}
	// Should also issue a token cookie for the client to use.
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected CSRF cookie to be issued on GET")
	}
}

func TestCSRF_PostWithoutHeaderRejected(t *testing.T) {
	mw := CSRF(CSRFOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/anything", nil)
	req.AddCookie(&http.Cookie{Name: "railbase_session", Value: "sess"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "the-token"})
	// NO X-CSRF-Token header.
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST without header: got %d, want 403", rec.Code)
	}
}

func TestCSRF_PostWithMismatchedHeaderRejected(t *testing.T) {
	mw := CSRF(CSRFOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)
	req.AddCookie(&http.Cookie{Name: "railbase_session", Value: "sess"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "real-token"})
	req.Header.Set(CSRFHeaderName, "wrong-token")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("mismatched: got %d, want 403", rec.Code)
	}
}

func TestCSRF_PostWithMatchingHeaderAccepted(t *testing.T) {
	mw := CSRF(CSRFOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)
	req.AddCookie(&http.Cookie{Name: "railbase_session", Value: "sess"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "matching-token"})
	req.Header.Set(CSRFHeaderName, "matching-token")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("matching: got %d, want 200", rec.Code)
	}
}

func TestCSRF_BearerAuthBypasses(t *testing.T) {
	mw := CSRF(CSRFOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer abc123")
	// No CSRF cookie or header — but Bearer means we skip.
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("bearer-authed POST should bypass CSRF; got %d", rec.Code)
	}
}

func TestCSRF_NoSessionCookieBypasses(t *testing.T) {
	mw := CSRF(CSRFOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/sign-in", nil)
	// No session cookie → nothing privileged to protect (e.g. login).
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("unauthenticated POST should bypass CSRF; got %d", rec.Code)
	}
}

func TestCSRF_SkipHook(t *testing.T) {
	mw := CSRF(CSRFOptions{Skip: func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/webhooks/")
	}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	// Cookie-authed POST to /webhooks/* — should be skipped despite
	// missing header.
	req := httptest.NewRequest("POST", "/webhooks/incoming", nil)
	req.AddCookie(&http.Cookie{Name: "railbase_session", Value: "sess"})
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("Skip hook should bypass CSRF; got %d", rec.Code)
	}
}

func TestCSRF_LazyIssuesCookieOnMissing(t *testing.T) {
	mw := CSRF(CSRFOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	// First request: no CSRF cookie. The middleware should issue one
	// in the response. Cookie-authed POST without header → 403 (this
	// request is unprotected) but the client now has a token for the
	// NEXT one.
	req := httptest.NewRequest("POST", "/", nil)
	req.AddCookie(&http.Cookie{Name: "railbase_session", Value: "sess"})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("first cookie-authed POST without token: got %d, want 403", rec.Code)
	}
	issued := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName && c.Value != "" {
			issued = true
		}
	}
	if !issued {
		t.Error("expected CSRF cookie to be issued even on 403 path so client can recover")
	}
}

func TestCSRF_TokenHandlerJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/csrf-token", nil)
	TokenHandler(CSRFOptions{})(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: %q", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, `{"token":"`) {
		t.Errorf("body: %q", body)
	}
}

func TestCSRF_IssueTokenReturnsValue(t *testing.T) {
	rec := httptest.NewRecorder()
	tok := IssueToken(rec, CSRFOptions{Secure: true})
	if tok == "" {
		t.Fatal("expected non-empty token")
	}
	// Confirm a cookie was set with that value.
	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			found = c
		}
	}
	if found == nil || found.Value != tok {
		t.Errorf("cookie not set or mismatched value")
	}
	if !found.Secure {
		t.Error("Secure flag not applied")
	}
}

func TestCSRF_ConstantTimeCompare(t *testing.T) {
	if ctEq("abc", "abcd") {
		t.Error("different lengths should not match")
	}
	if !ctEq("abc", "abc") {
		t.Error("same strings should match")
	}
	if ctEq("abc", "abd") {
		t.Error("different content should not match")
	}
}
