package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// corsNextOK is a sentinel handler that lets tests assert "the
// middleware let the request through" vs "short-circuited". Named
// distinctly from antibot_test.go's nextOK type to avoid collision.
func corsNextOK() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reached-Handler", "1")
		w.WriteHeader(http.StatusOK)
	})
}

// pin wraps a static CORSLive snapshot for the unit tests below.
func pin(origins []string, creds bool) CORSLive {
	return StaticCORSLive{AllowedOrigins: origins, AllowCredentials: creds}
}

func TestCORS_InertWhenNoAllowList(t *testing.T) {
	mw := CORS(CORSOptions{}, pin(nil, false))(corsNextOK())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://attacker.example")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("inert middleware should pass through; got status %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("inert middleware leaked Access-Control-Allow-Origin")
	}
	if rec.Header().Get("X-Reached-Handler") != "1" {
		t.Error("inert middleware blocked the handler")
	}
}

func TestCORS_AllowedOriginGetsHeader(t *testing.T) {
	mw := CORS(CORSOptions{}, pin([]string{"https://app.example.com"}, true))(corsNextOK())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q; want exact match echo", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q; want true", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q; want Origin", got)
	}
}

func TestCORS_DisallowedOriginGetsNoHeader(t *testing.T) {
	mw := CORS(CORSOptions{}, pin([]string{"https://app.example.com"}, false))(corsNextOK())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://attacker.example")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("foreign origin must NOT receive Allow-Origin header")
	}
	if rec.Header().Get("X-Reached-Handler") != "1" {
		t.Error("non-preflight should still reach handler even when origin disallowed")
	}
}

func TestCORS_PreflightAllowed(t *testing.T) {
	mw := CORS(CORSOptions{}, pin([]string{"https://app.example.com"}, true))(corsNextOK())
	req := httptest.NewRequest("OPTIONS", "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight: status %d; want 204", rec.Code)
	}
	if rec.Header().Get("X-Reached-Handler") == "1" {
		t.Error("preflight must short-circuit, not reach the real handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("preflight Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("preflight Allow-Methods missing POST: %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got == "" {
		t.Error("preflight Max-Age unset")
	}
}

func TestCORS_PreflightDisallowedHasNoHeaders(t *testing.T) {
	mw := CORS(CORSOptions{}, pin([]string{"https://app.example.com"}, false))(corsNextOK())
	req := httptest.NewRequest("OPTIONS", "/x", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight: status %d; want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("disallowed preflight leaked Allow-Origin header")
	}
	if rec.Header().Get("Access-Control-Allow-Methods") != "" {
		t.Error("disallowed preflight leaked Allow-Methods header")
	}
}

func TestCORS_WildcardWithCredentialsRefused(t *testing.T) {
	// "*" + AllowCredentials is a known footgun (browsers refuse it).
	// Middleware should fall back to inert behaviour, not silently
	// emit "*" with credentials.
	mw := CORS(CORSOptions{}, pin([]string{"*"}, true))(corsNextOK())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error(`"*" + credentials should refuse to emit Allow-Origin`)
	}
}

func TestCORS_WildcardWithoutCredentials(t *testing.T) {
	mw := CORS(CORSOptions{}, pin([]string{"*"}, false))(corsNextOK())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://anything.example")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf(`wildcard Allow-Origin = %q; want "*"`, got)
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("wildcard must NOT set Allow-Credentials")
	}
}

func TestCORS_OriginNotReflectedOnMismatch(t *testing.T) {
	// Regression: confirm we never echo back a non-allow-listed Origin.
	// The classic CORS mistake is `Allow-Origin: <whatever Origin came in>`.
	mw := CORS(CORSOptions{}, pin([]string{"https://app.example.com"}, false))(corsNextOK())
	for _, evil := range []string{
		"https://app.example.com.attacker.example",
		"https://attacker.example/app.example.com",
		"null",
		"https://APP.example.com", // case-sensitive — origins are case-sensitive per RFC
	} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Origin", evil)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Origin %q: leaked Allow-Origin = %q", evil, got)
		}
	}
}

// Liveness regression — the entire point of the runtimeconfig
// migration. A request issued against an empty live-snapshot must
// be inert; flipping the snapshot to non-empty must make the next
// request emit headers, with no middleware re-construction in
// between.
func TestCORS_LiveSnapshotReadOnEveryRequest(t *testing.T) {
	live := &mutableCORSLive{}
	mw := CORS(CORSOptions{}, live)(corsNextOK())

	// 1. Empty snapshot → inert.
	req1 := httptest.NewRequest("GET", "/x", nil)
	req1.Header.Set("Origin", "https://app.example.com")
	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, req1)
	if rec1.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("step 1: empty live should be inert; got header %q",
			rec1.Header().Get("Access-Control-Allow-Origin"))
	}

	// 2. Operator saves a new allowed origin via the admin UI.
	live.SetOrigins([]string{"https://app.example.com"})

	// 3. Same middleware instance, next request → header now emitted.
	req2 := httptest.NewRequest("GET", "/x", nil)
	req2.Header.Set("Origin", "https://app.example.com")
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("step 3: live update not picked up; Allow-Origin = %q", got)
	}

	// 4. Operator removes the origin again → next request inert again.
	live.SetOrigins(nil)
	req3 := httptest.NewRequest("GET", "/x", nil)
	req3.Header.Set("Origin", "https://app.example.com")
	rec3 := httptest.NewRecorder()
	mw.ServeHTTP(rec3, req3)
	if rec3.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("step 4: removal not picked up; header still present")
	}
}

// mutableCORSLive is the test-side counterpart to runtimeconfig.Config
// — atomically-swappable origin slice. We don't use runtimeconfig
// here to keep the security package free of the dependency.
type mutableCORSLive struct {
	origins atomic.Pointer[[]string]
	creds   atomic.Bool
}

func (m *mutableCORSLive) CORSAllowedOrigins() []string {
	p := m.origins.Load()
	if p == nil {
		return nil
	}
	return *p
}
func (m *mutableCORSLive) CORSAllowCredentials() bool { return m.creds.Load() }
func (m *mutableCORSLive) SetOrigins(o []string)      { m.origins.Store(&o) }
