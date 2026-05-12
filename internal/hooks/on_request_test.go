package hooks

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// makeOnRequestRuntime spins a hooks runtime loaded with the supplied
// JS script (single onRequest registration block) and returns it ready
// to wire its NewOnRequestMiddleware. Mirrors makeRouterRuntime.
func makeOnRequestRuntime(t *testing.T, script string) *Runtime {
	t.Helper()
	return makeRuntime(t, map[string]string{"on_request.js": script})
}

// runRequest drives one HTTP request through the runtime's onRequest
// middleware. The downstream handler is provided by the caller so
// individual tests can assert downstream behaviour (headers seen,
// "wasn't called", etc.).
func runRequest(t *testing.T, rt *Runtime, method, path string, downstream http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	mw := rt.NewOnRequestMiddleware()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	mw(downstream).ServeHTTP(rec, req)
	return rec
}

func TestOnRequest_Fires(t *testing.T) {
	rt := makeOnRequestRuntime(t, `
$app.onRequest((e) => {
    e.response.header("X-Hooked", "1");
    e.next();
});
`)
	var downstreamCalled int32
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&downstreamCalled, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	})
	rec := runRequest(t, rt, "GET", "/some/path", downstream)
	if rec.Code != 200 {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&downstreamCalled) != 1 {
		t.Errorf("downstream not called: hook should have called e.next()")
	}
	// response headers staged with e.response.header only flush on abort;
	// the hook ran but didn't abort, so the header IS NOT sent.
	// (This is a documented design choice — the dispatcher comment in
	// on_request.go explains the rationale.)
	if rec.Header().Get("X-Hooked") == "1" {
		t.Errorf("staged response header leaked on non-abort path")
	}
}

func TestOnRequest_HeaderMutation(t *testing.T) {
	rt := makeOnRequestRuntime(t, `
$app.onRequest((e) => {
    e.request.header("X-Test", "yes");
    e.next();
});
`)
	var seen string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Test")
		w.WriteHeader(200)
	})
	rec := runRequest(t, rt, "GET", "/", downstream)
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if seen != "yes" {
		t.Errorf("downstream did not see mutated header: got %q", seen)
	}
}

func TestOnRequest_AbortShortCircuits(t *testing.T) {
	rt := makeOnRequestRuntime(t, `
$app.onRequest((e) => {
    e.abort(403, "nope");
});
`)
	var downstreamCalled int32
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&downstreamCalled, 1)
	})
	rec := runRequest(t, rt, "GET", "/", downstream)
	if rec.Code != 403 {
		t.Fatalf("status: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "nope" {
		t.Errorf("body: want 'nope', got %q", rec.Body.String())
	}
	if atomic.LoadInt32(&downstreamCalled) != 0 {
		t.Errorf("downstream should NOT have been called after abort")
	}
}

func TestOnRequest_AbortObjectBodyJSON(t *testing.T) {
	rt := makeOnRequestRuntime(t, `
$app.onRequest((e) => {
    e.abort(429, {error: "rate-limited", retryAfter: 60});
});
`)
	rec := runRequest(t, rt, "GET", "/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("downstream should not run")
	}))
	if rec.Code != 429 {
		t.Fatalf("status: want 429, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: want application/json, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "rate-limited") {
		t.Errorf("body should contain JSON: %s", rec.Body.String())
	}
}

func TestOnRequest_ChainOrder(t *testing.T) {
	// Two handlers, A registered first then B. A.next() → B runs →
	// B.next() → downstream runs. Each handler stamps its own header.
	rt := makeRuntime(t, map[string]string{
		"01_a.js": `
$app.onRequest((e) => {
    e.request.header("X-Chain", "A");
    e.next();
});
`,
		"02_b.js": `
$app.onRequest((e) => {
    // Concatenate so order is observable from downstream.
    const cur = e.request.header("X-Chain");
    e.request.header("X-Chain", cur + ",B");
    e.next();
});
`,
	})
	var seen string
	downstream := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Chain")
	})
	runRequest(t, rt, "GET", "/", downstream)
	if seen != "A,B" {
		t.Errorf("chain order: want A,B got %q", seen)
	}
}

func TestOnRequest_AbortStopsChain(t *testing.T) {
	// Handler A aborts; handler B must NOT run.
	rt := makeRuntime(t, map[string]string{
		"01_a.js": `$app.onRequest((e) => { e.abort(401, "halt"); });`,
		"02_b.js": `$app.onRequest((e) => { e.request.header("X-B-Ran", "yes"); e.next(); });`,
	})
	var seenB string
	downstream := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenB = r.Header.Get("X-B-Ran")
	})
	rec := runRequest(t, rt, "GET", "/", downstream)
	if rec.Code != 401 {
		t.Fatalf("status: %d", rec.Code)
	}
	if seenB != "" {
		t.Errorf("B should not have run after A aborted, got %q", seenB)
	}
}

func TestOnRequest_WatchdogKills(t *testing.T) {
	// A `while(true){}` loop must be killed by goja.Interrupt within
	// the per-handler timeout. We construct a runtime with a SHORT
	// (100ms) explicit timeout so the test finishes quickly even on
	// slow CI hosts. The watchdog must fire before the 2s test
	// deadline below.
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `
$app.onRequest((e) => {
    while (true) {}
});
`
	if err := os.WriteFile(filepath.Join(hooksDir, "slow.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  100 * time.Millisecond, // tighter than DefaultOnRequestTimeout
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("downstream should not run when handler hangs")
	})
	start := time.Now()
	rec := runRequest(t, rt, "GET", "/", downstream)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("watchdog didn't fire promptly: %v", elapsed)
	}
	// A timed-out handler surfaces as 500 hook_error.
	if rec.Code != 500 {
		t.Errorf("hung handler should produce 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOnRequest_HotReloadAtomicSwap(t *testing.T) {
	rt := makeOnRequestRuntime(t, `
$app.onRequest((e) => {
    e.request.header("X-Version", "v1");
    e.next();
});
`)
	// Initial: header stamped "v1".
	var seen string
	downstream := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Version")
	})
	runRequest(t, rt, "GET", "/", downstream)
	if seen != "v1" {
		t.Fatalf("initial: want v1, got %q", seen)
	}

	// Rewrite the .js source + call Load() to simulate the fsnotify
	// debounce-driven reload path. Atomic-swap of the onRequest
	// pointer means the next request picks up the new handler.
	hookFile := filepath.Join(rt.hooksDir, "on_request.js")
	newSrc := `
$app.onRequest((e) => {
    e.request.header("X-Version", "v2");
    e.next();
});
`
	if err := os.WriteFile(hookFile, []byte(newSrc), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := rt.Load(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	seen = ""
	runRequest(t, rt, "GET", "/", downstream)
	if seen != "v2" {
		t.Errorf("after hot-reload: want v2, got %q", seen)
	}
}

func TestOnRequest_NoHandlersBypass(t *testing.T) {
	// A runtime with no $app.onRequest calls registered — the
	// middleware must be a pass-through. We assert downstream runs
	// AND that HasOnRequestHandlers reports false.
	r := makeRuntime(t, nil)
	if r.HasOnRequestHandlers() {
		t.Errorf("fresh runtime should have no onRequest handlers")
	}
	var called int32
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(204)
	})
	rec := runRequest(t, r, "GET", "/anything", downstream)
	if rec.Code != 204 {
		t.Errorf("status: want 204 (downstream), got %d", rec.Code)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("downstream should fire when no handlers registered")
	}
}

func TestOnRequest_AdminUIBypass(t *testing.T) {
	// Requests to `/_/*` (the embedded admin UI assets path) must
	// bypass the dispatcher entirely. We register a handler that
	// would abort with 403; the admin path should NOT trigger it.
	rt := makeOnRequestRuntime(t, `
$app.onRequest((e) => { e.abort(403, "blocked"); });
`)
	// Non-admin path → hook runs → 403.
	rec := runRequest(t, rt, "GET", "/api/foo", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("downstream should not run for non-admin path")
	}))
	if rec.Code != 403 {
		t.Errorf("non-admin: want 403, got %d", rec.Code)
	}
	// Admin path → bypass → downstream serves.
	var hit int32
	rec = runRequest(t, rt, "GET", "/_/assets/main.js", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(200)
	}))
	if rec.Code != 200 || atomic.LoadInt32(&hit) != 1 {
		t.Errorf("admin path should bypass hook: status=%d hit=%d", rec.Code, atomic.LoadInt32(&hit))
	}
}

func TestOnRequest_ThrowSurfacesAs500(t *testing.T) {
	rt := makeOnRequestRuntime(t, `
$app.onRequest((e) => { throw new Error("boom"); });
`)
	rec := runRequest(t, rt, "GET", "/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("downstream should not run after throw")
	}))
	if rec.Code != 500 {
		t.Fatalf("status: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("error message should mention thrown reason: %s", rec.Body.String())
	}
}

func TestOnRequest_NilRuntimeMiddleware(t *testing.T) {
	// A nil *Runtime must yield a pass-through middleware. Mirrors
	// RouterMiddleware's nil-safety contract.
	var r *Runtime
	var called int32
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(200)
	})
	mw := r.NewOnRequestMiddleware()
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	mw(downstream).ServeHTTP(rec, req)
	if rec.Code != 200 || atomic.LoadInt32(&called) != 1 {
		t.Errorf("nil runtime middleware should pass-through: status=%d called=%d",
			rec.Code, atomic.LoadInt32(&called))
	}
}
