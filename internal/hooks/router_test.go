package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeRouterRuntime spins a hooks runtime, evaluates the supplied
// .js source, and returns the runtime so callers can wire its
// RouterMiddleware into an http test fixture. Mirrors makeRuntime
// but skips the on*-event boilerplate the routerAdd tests don't
// care about.
func makeRouterRuntime(t *testing.T, script string) *Runtime {
	t.Helper()
	r := makeRuntime(t, map[string]string{"routes.js": script})
	return r
}

// callRoute drives one HTTP request through the runtime's router
// middleware. The fallback handler returns 404 so test asserts can
// distinguish "hook served it" from "fell through".
func callRoute(t *testing.T, rt *Runtime, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	mw := rt.RouterMiddleware()
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "fallback 404", http.StatusNotFound)
	})
	req := httptest.NewRequest(method, path, body)
	rec := httptest.NewRecorder()
	mw(fallback).ServeHTTP(rec, req)
	return rec
}

func TestRouterAdd_GetJSON(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/hello", (e) => {
    e.json(200, {message: "hi"});
});
`)
	rec := callRoute(t, rt, "GET", "/hello", nil)
	if rec.Code != 200 {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: want application/json, got %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got["message"] != "hi" {
		t.Errorf("message: want hi, got %v", got["message"])
	}
}

func TestRouterAdd_PostReadsBody(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("POST", "/echo", (e) => {
    const body = e.jsonBody();
    e.json(201, {received: body});
});
`)
	rec := callRoute(t, rt, "POST", "/echo", strings.NewReader(`{"hello":"world"}`))
	if rec.Code != 201 {
		t.Fatalf("status: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Received map[string]any `json:"received"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got.Received["hello"] != "world" {
		t.Errorf("received body lost: %v", got.Received)
	}
}

func TestRouterAdd_PathParam(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/hello/{name}", (e) => {
    const name = e.pathParam("name");
    e.json(200, {greeting: "hello " + name});
});
`)
	rec := callRoute(t, rt, "GET", "/hello/alice", nil)
	if rec.Code != 200 {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["greeting"] != "hello alice" {
		t.Errorf("greeting: want 'hello alice', got %q", got["greeting"])
	}
}

func TestRouterAdd_QueryAndHeaders(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/probe", (e) => {
    e.json(200, {
        q: e.query.foo,
        h: e.headers["X-Probe"]
    });
});
`)
	mw := rt.RouterMiddleware()
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "fallback", http.StatusNotFound)
	})
	req := httptest.NewRequest("GET", "/probe?foo=bar&foo=baz", nil)
	req.Header.Set("X-Probe", "ping")
	rec := httptest.NewRecorder()
	mw(fallback).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Q []string `json:"q"`
		H string   `json:"h"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(got.Q) != 2 || got.Q[0] != "bar" || got.Q[1] != "baz" {
		t.Errorf("multi-value query lost: %v", got.Q)
	}
	if got.H != "ping" {
		t.Errorf("header lost: %v", got.H)
	}
}

func TestRouterAdd_NoMatchFallsThrough(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/hello", (e) => { e.json(200, {ok: true}); });
`)
	// Method matches, path doesn't.
	rec := callRoute(t, rt, "GET", "/goodbye", nil)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "fallback") {
		t.Errorf("unmatched path should fall through to fallback, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Path matches, method doesn't (chi treats this as no-match too in
	// our middleware path).
	rec = callRoute(t, rt, "POST", "/hello", strings.NewReader(`{}`))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "fallback") {
		t.Errorf("unmatched method should fall through to fallback, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterAdd_NoRoutesIsZeroCost(t *testing.T) {
	// A runtime with no .js files at all (i.e. no routerAdd calls)
	// must leave the middleware as a pass-through — the router pointer
	// is nil.
	r := makeRuntime(t, nil)
	rec := callRoute(t, r, "GET", "/anything", nil)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "fallback") {
		t.Errorf("empty-route runtime should fall through, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterAdd_StatusOnlyDefaultsTo204(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("DELETE", "/thing/{id}", (e) => {
    // No body, no e.status — should default to 204.
});
`)
	rec := callRoute(t, rt, "DELETE", "/thing/abc", nil)
	if rec.Code != 204 {
		t.Errorf("no-body handler should default to 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 must have no body, got %s", rec.Body.String())
	}
}

func TestRouterAdd_TextAndHTML(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/t", (e) => { e.text(200, "hello text"); });
$app.routerAdd("GET", "/h", (e) => { e.html(200, "<b>hi</b>"); });
`)
	rec := callRoute(t, rt, "GET", "/t", nil)
	if rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("text ct wrong: %q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "hello text" {
		t.Errorf("text body: %q", rec.Body.String())
	}
	rec = callRoute(t, rt, "GET", "/h", nil)
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("html ct wrong: %q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "<b>hi</b>" {
		t.Errorf("html body: %q", rec.Body.String())
	}
}

func TestRouterAdd_CustomHeader(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/h", (e) => {
    e.header("X-Custom", "value-1");
    e.json(200, {ok: true});
});
`)
	rec := callRoute(t, rt, "GET", "/h", nil)
	if got := rec.Header().Get("X-Custom"); got != "value-1" {
		t.Errorf("X-Custom header: got %q", got)
	}
}

func TestRouterAdd_ThrowSurfacesAs500(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/bang", (e) => {
    throw new Error("kaboom");
});
`)
	rec := callRoute(t, rt, "GET", "/bang", nil)
	if rec.Code != 500 {
		t.Fatalf("status: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "hook_error" {
		t.Errorf("error.code: want hook_error, got %q", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "kaboom") {
		t.Errorf("message should mention thrown reason: %q", env.Error.Message)
	}
}

func TestRouterAdd_ValidationErrors(t *testing.T) {
	// Each of these scripts has a bad routerAdd call. Load() shouldn't
	// fail outright (we wrap individual file failures with a log warn),
	// so we exercise the registration error by checking that the route
	// table ends up empty.
	cases := []struct {
		name   string
		script string
	}{
		{"missing args", `$app.routerAdd("GET");`},
		{"bad method", `$app.routerAdd("FROBNICATE", "/foo", () => {});`},
		{"bad path", `$app.routerAdd("GET", "no-leading-slash", () => {});`},
		{"non-function handler", `$app.routerAdd("GET", "/foo", "not a function");`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := makeRouterRuntime(t, tc.script)
			// Bad register → no route persisted → request falls through.
			rec := callRoute(t, rt, "GET", "/foo", nil)
			if rec.Code != 404 {
				t.Errorf("bad routerAdd should leave route un-registered, got %d body=%s",
					rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRouterAdd_HotReloadReplaces(t *testing.T) {
	rt := makeRouterRuntime(t, `
$app.routerAdd("GET", "/v1", (e) => { e.json(200, {v: 1}); });
`)
	// Initial state: /v1 OK, /v2 404.
	if rec := callRoute(t, rt, "GET", "/v1", nil); rec.Code != 200 {
		t.Fatalf("v1: want 200, got %d", rec.Code)
	}
	if rec := callRoute(t, rt, "GET", "/v2", nil); rec.Code != 404 {
		t.Fatalf("v2: want 404 before reload, got %d", rec.Code)
	}

	// Reload with the SAME hooks dir but a new file body (atomic
	// registry swap). Replace the file then call Load() again.
	hookFile := filepath.Join(rt.hooksDir, "routes.js")
	newSrc := `$app.routerAdd("GET", "/v2", (e) => { e.json(200, {v: 2}); });`
	if err := os.WriteFile(hookFile, []byte(newSrc), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := rt.Load(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// /v1 should now 404 (removed) and /v2 should serve.
	if rec := callRoute(t, rt, "GET", "/v1", nil); rec.Code != 404 {
		t.Errorf("v1 after reload: want 404 (removed), got %d", rec.Code)
	}
	if rec := callRoute(t, rt, "GET", "/v2", nil); rec.Code != 200 {
		t.Errorf("v2 after reload: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}
