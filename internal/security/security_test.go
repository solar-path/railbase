package security

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Headers ---

func TestHeaders_DefaultEmitsExpectedHeaders(t *testing.T) {
	mw := Headers(DefaultHeadersOptions())
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	expect := map[string]string{
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "no-referrer",
	}
	for k, v := range expect {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s: got %q, want %q", k, got, v)
		}
	}
}

func TestHeaders_EmptyOptionsAreOmitted(t *testing.T) {
	mw := Headers(HeadersOptions{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS leaked when option empty")
	}
}

// --- IPFilterRules ---

func TestIPFilterRules_AllowOnlyMode(t *testing.T) {
	r, err := NewIPFilterRules([]string{"10.0.0.0/24"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Allowed(net.ParseIP("10.0.0.5")) {
		t.Error("10.0.0.5 should be allowed")
	}
	if r.Allowed(net.ParseIP("10.0.1.5")) {
		t.Error("10.0.1.5 should be denied (outside allow CIDR)")
	}
}

func TestIPFilterRules_DenyOnlyMode(t *testing.T) {
	r, _ := NewIPFilterRules(nil, []string{"10.0.0.0/8"})
	if r.Allowed(net.ParseIP("10.5.5.5")) {
		t.Error("10.5.5.5 should be denied")
	}
	if !r.Allowed(net.ParseIP("192.168.0.1")) {
		t.Error("outside-deny should pass")
	}
}

func TestIPFilterRules_DenyWinsOnOverlap(t *testing.T) {
	r, _ := NewIPFilterRules([]string{"10.0.0.0/8"}, []string{"10.0.0.5/32"})
	if !r.Allowed(net.ParseIP("10.0.0.1")) {
		t.Error("10.0.0.1 should be allowed (in allow CIDR, not in deny)")
	}
	if r.Allowed(net.ParseIP("10.0.0.5")) {
		t.Error("10.0.0.5 should be denied (deny wins over allow)")
	}
}

func TestIPFilterRules_BareIPPromoted(t *testing.T) {
	// Bare IPs without /N should become /32 (v4) or /128 (v6).
	r, err := NewIPFilterRules([]string{"203.0.113.5"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Allowed(net.ParseIP("203.0.113.5")) {
		t.Error("bare-IP allow failed")
	}
	if r.Allowed(net.ParseIP("203.0.113.6")) {
		t.Error("only the literal IP should match for /32")
	}
}

func TestIPFilterRules_IPv6(t *testing.T) {
	r, err := NewIPFilterRules([]string{"fc00::/7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Allowed(net.ParseIP("fc00:1:2::3")) {
		t.Error("fc00:1:2::3 should match fc00::/7")
	}
	if r.Allowed(net.ParseIP("2001:db8::1")) {
		t.Error("2001:db8::1 should not match fc00::/7")
	}
}

func TestIPFilterRules_RejectsBadCIDR(t *testing.T) {
	if _, err := NewIPFilterRules([]string{"bogus"}, nil); err == nil {
		t.Error("expected error for bogus CIDR")
	}
}

func TestIPFilterRules_EmptyAllowsAll(t *testing.T) {
	r, _ := NewIPFilterRules(nil, nil)
	if !r.IsEmpty() {
		t.Error("nil/nil should be empty")
	}
	if !r.Allowed(net.ParseIP("1.2.3.4")) {
		t.Error("empty filter should allow all")
	}
}

// --- IPFilter middleware ---

func TestIPFilterMiddleware_RemoteAddr(t *testing.T) {
	f, err := NewIPFilter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Update(nil, []string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	h := f.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	// Denied: RemoteAddr in 10/8.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.1.1:12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("denied IP: got %d, want 403", rec.Code)
	}

	// Allowed: RemoteAddr outside deny.
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.1:12345"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("allowed IP: got %d, want 200", rec.Code)
	}
}

func TestIPFilterMiddleware_XFFOnlyWithTrustedProxy(t *testing.T) {
	// Without trustedProxies, X-Forwarded-For is IGNORED.
	f, _ := NewIPFilter(nil)
	_ = f.Update(nil, []string{"203.0.113.0/24"})
	h := f.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.1.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.5") // would-be denied
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Error("without trusted proxies XFF should be ignored; got 403")
	}

	// With trustedProxies including 10/8, XFF is HONOURED.
	f2, _ := NewIPFilter([]string{"10.0.0.0/8"})
	_ = f2.Update(nil, []string{"203.0.113.0/24"})
	h2 := f2.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "10.1.1.1:12345"
	req2.Header.Set("X-Forwarded-For", "203.0.113.5")
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("trusted proxy + XFF in deny: got %d, want 403", rec2.Code)
	}
}

func TestIPFilterMiddleware_EmptyRulesPassthrough(t *testing.T) {
	f, _ := NewIPFilter(nil)
	h := f.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.1.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Errorf("empty rules should pass through: %d %q", rec.Code, rec.Body.String())
	}
}

func TestIPFilterMiddleware_LiveUpdate(t *testing.T) {
	f, _ := NewIPFilter(nil)
	h := f.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	// Initially passes.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.1.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("initial pass-through")
	}

	// Update to deny — next request blocks.
	if err := f.Update(nil, []string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("after live update: got %d, want 403", rec.Code)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x"); got != "x" {
		t.Errorf("got %q, want x", got)
	}
	if got := firstNonEmpty("", "y", "x"); got != "y" {
		t.Errorf("got %q, want y", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// Sanity: parseCIDR error messages list every malformed entry once.
func TestNewIPFilterRules_ReportsMultipleErrors(t *testing.T) {
	_, err := NewIPFilterRules([]string{"bad1", "10.0.0.0/8"}, []string{"bad2"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bad1") || !strings.Contains(msg, "bad2") {
		t.Errorf("expected both bad entries in error: %s", msg)
	}
}
