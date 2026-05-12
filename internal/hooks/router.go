package hooks

// v1.7.17 — `$app.routerAdd(method, path, handler)` JS hook binding.
// Closes docs/17 #60. Lets hook authors register custom HTTP endpoints
// alongside the built-in CRUD/auth routes, with the same hot-reload
// guarantee the on*-event hooks already have.
//
// Architecture:
//
//   1. During Load(), each `$app.routerAdd(...)` call appends to a
//      per-Load routerRouteSet (in declaration order). VMs are stable
//      across the load — the captured goja.Callable references stay
//      bindable to the same VM.
//   2. After all .js files run, routerRouteSet.buildMux() constructs a
//      fresh chi.Mux registering one Go handler per route. The handler
//      bridges http.ResponseWriter + *http.Request into a JS `e`
//      object (request fields + response writers + pathParam) and
//      invokes the captured Callable under the per-handler watchdog.
//   3. runtime.router (atomic.Pointer[*chi.Mux]) is swapped to the
//      new mux. Old mux is garbage when no in-flight request still
//      holds it.
//   4. app.go installs `runtime.RouterMiddleware()` BEFORE the rest of
//      the router. On each request the middleware calls chi.Mux.Match
//      against the active mux; a match dispatches there, otherwise
//      next.ServeHTTP fires.
//
// JS handler contract:
//
//   $app.routerAdd("GET", "/api/hello/{name}", (e) => {
//       const name = e.pathParam("name");
//       e.json(200, {message: "Hello " + name});
//   });
//
// `e` exposes:
//
//   - e.method (string)  e.path (string)  e.url (string)
//   - e.query   — Map<string, string[]> shape (multi-value-aware)
//   - e.headers — Map<string, string> (first header value per key)
//   - e.body    — request body as a string (read once into a buffer)
//   - e.json()  — parsed JSON body (object/array/etc.) or null on parse
//   - e.pathParam(name) — chi URL parameter, "" when absent
//   - e.json(status, body)   — content-type application/json
//   - e.text(status, body)   — content-type text/plain; charset=utf-8
//   - e.html(status, body)   — content-type text/html; charset=utf-8
//   - e.status(code)         — set status only (call before body writes)
//   - e.header(name, value)  — set response header
//
// Errors thrown from the handler surface as 500 with `{error: {code:
// "hook_error", message}}`. Watchdog kicks in at the same per-handler
// timeout as the on* dispatchers (default 5s).
//
// What's NOT in this slice:
//
//   - JSON body validation / schema hooks — hook authors do their own.
//   - Middleware chain on the JS side — chi handles auth via the global
//     middleware stack; hooks see the request after that stack.
//   - File upload helpers — defer to v1.x; multipart bodies arrive as
//     raw e.body for now (operators wanting multipart can read it
//     directly via $http when that binding lands).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/go-chi/chi/v5"
)

// routerRouteSet is the per-Load collector of $app.routerAdd registrations.
// One per loader.Load() call.
type routerRouteSet struct {
	routes []routerRoute
}

func newRouterRouteSet() *routerRouteSet { return &routerRouteSet{} }

// routerRoute is one (method, path, handler) tuple captured during JS
// evaluation. fn binds to the VM that ran the registration; rt.runOne
// switches back to that VM at dispatch.
type routerRoute struct {
	method string
	path   string
	fn     goja.Callable
	vm     *goja.Runtime
}

// register validates the call arguments and appends one route. Bad
// inputs surface as JS Error via the caller's panic(vm.NewGoError(...)).
func (rs *routerRouteSet) register(vm *goja.Runtime, call goja.FunctionCall) error {
	if len(call.Arguments) < 3 {
		return fmt.Errorf("expected (method, path, handler), got %d argument(s)", len(call.Arguments))
	}
	method := strings.ToUpper(strings.TrimSpace(call.Arguments[0].String()))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
	default:
		return fmt.Errorf("method must be GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS (got %q)", method)
	}
	path := strings.TrimSpace(call.Arguments[1].String())
	if path == "" || path[0] != '/' {
		return fmt.Errorf("path must be a non-empty string starting with `/` (got %q)", path)
	}
	fn, ok := goja.AssertFunction(call.Arguments[2])
	if !ok {
		return fmt.Errorf("handler must be a function")
	}
	rs.routes = append(rs.routes, routerRoute{method: method, path: path, fn: fn, vm: vm})
	return nil
}

// buildMux constructs a chi.Mux registering each captured route. Returns
// nil when no routes were registered so the middleware can short-circuit.
// The owning *Runtime is captured by reference for the per-request
// dispatch (so we get its watchdog, VM lock, and logger).
func (rs *routerRouteSet) buildMux(rt *Runtime) *chi.Mux {
	if len(rs.routes) == 0 {
		return nil
	}
	mux := chi.NewRouter()
	for _, rr := range rs.routes {
		rr := rr // capture
		mux.MethodFunc(rr.method, rr.path, func(w http.ResponseWriter, r *http.Request) {
			rt.serveRoute(w, r, rr)
		})
	}
	return mux
}

// RouterMiddleware returns a chi-compatible middleware that dispatches
// incoming requests to the runtime's $app.routerAdd registry when the
// request matches one. No match → next.ServeHTTP fires (and the rest of
// the chi tree runs normally). When no routes are registered (router
// pointer is nil), the middleware is a zero-cost pass-through.
//
// IMPORTANT: install this BEFORE the rest of the route registrations so
// hook routes take precedence over generic CRUD. Wiring after the CRUD
// surface would let a /api/collections/{name}/records match first.
func (r *Runtime) RouterMiddleware() func(http.Handler) http.Handler {
	if r == nil {
		// Nil runtime → no-op middleware (safe to wire even when hooks
		// are disabled, mirrors the nil-safe pattern on Dispatch).
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			mux := r.router.Load()
			if mux == nil {
				next.ServeHTTP(w, req)
				return
			}
			// chi.Mux.Match needs a fresh RouteContext to record path
			// params; if we don't reset it, parent-router context bleeds
			// in and confuses the match.
			rctx := chi.NewRouteContext()
			if !mux.Match(rctx, req.Method, req.URL.Path) {
				next.ServeHTTP(w, req)
				return
			}
			// Match succeeded — let the mux handle dispatch (which
			// populates the real RouteContext for the chosen handler
			// and runs serveRoute below).
			mux.ServeHTTP(w, req)
		})
	}
}

// serveRoute is the bridge between net/http and goja: read the request,
// build a JS `e` object, invoke the handler under the watchdog, copy
// the buffered response back to the wire.
//
// Response writes are buffered into an *http.ResponseRecorder-shaped
// captureWriter — handlers can call e.json/text/html exactly once (the
// last call wins). This sidesteps the foot-gun where a JS handler
// writes a status code AFTER writing the body (net/http would silently
// 200 the body, then ignore the WriteHeader).
func (rt *Runtime) serveRoute(w http.ResponseWriter, req *http.Request, rr routerRoute) {
	// Buffer request body so e.body and e.json() can both read it
	// without consuming.
	bodyBytes, _ := io.ReadAll(io.LimitReader(req.Body, 8<<20)) // 8 MiB safety cap
	_ = req.Body.Close()

	// Capture response state in-memory; flush at the end.
	cap := &captureWriter{header: http.Header{}, status: 0, body: &bytes.Buffer{}}

	rt.vmMu.Lock()
	defer rt.vmMu.Unlock()
	vm := rr.vm

	// Build the `e` object the handler sees. We expose:
	//   request: method/path/url/query/headers/body + json()
	//   response: status/header/json/text/html
	//   pathParam(name)
	e := vm.NewObject()
	_ = e.Set("method", req.Method)
	_ = e.Set("path", req.URL.Path)
	_ = e.Set("url", req.URL.String())
	_ = e.Set("body", string(bodyBytes))

	// query → JS object whose values are arrays (preserves duplicate
	// keys without making the common single-value case awkward).
	q := vm.NewObject()
	for k, vs := range req.URL.Query() {
		_ = q.Set(k, append([]string(nil), vs...))
	}
	_ = e.Set("query", q)

	hd := vm.NewObject()
	for k := range req.Header {
		_ = hd.Set(k, req.Header.Get(k))
	}
	_ = e.Set("headers", hd)

	// pathParam(name) — chi URL param lookup. chi populates the route
	// context on dispatch; here we surface whichever value chi stored.
	_ = e.Set("pathParam", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		name := call.Arguments[0].String()
		return vm.ToValue(chi.URLParam(req, name))
	})

	// status(code) — sets status code only; subsequent json/text/html
	// override it. Useful for "204 No Content" responses.
	_ = e.Set("status", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		cap.status = int(call.Arguments[0].ToInteger())
		return goja.Undefined()
	})

	// header(name, value) — sets a response header.
	_ = e.Set("header", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			return goja.Undefined()
		}
		cap.header.Set(call.Arguments[0].String(), call.Arguments[1].String())
		return goja.Undefined()
	})

	// json(status, body) — set content-type + serialise + flush.
	_ = e.Set("json", buildBodyWriter(vm, cap, "application/json", func(v goja.Value) ([]byte, error) {
		return json.Marshal(v.Export())
	}))
	// text(status, body) — set content-type + write string.
	_ = e.Set("text", buildBodyWriter(vm, cap, "text/plain; charset=utf-8", func(v goja.Value) ([]byte, error) {
		return []byte(v.String()), nil
	}))
	// html(status, body) — set content-type + write string.
	_ = e.Set("html", buildBodyWriter(vm, cap, "text/html; charset=utf-8", func(v goja.Value) ([]byte, error) {
		return []byte(v.String()), nil
	}))

	// jsonBody() — parsed request body. `e.json(status, body)` is the
	// RESPONSE writer (matches PB c.json shape); read access lives under
	// a distinct name so authors don't collide the two. Returns null on
	// empty body or parse error so callers can branch with `if (body)`.
	_ = e.Set("jsonBody", func(_ goja.FunctionCall) goja.Value {
		if len(bodyBytes) == 0 {
			return goja.Null()
		}
		var parsed any
		if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
			return goja.Null()
		}
		return vm.ToValue(parsed)
	})

	// Watchdog. Same pattern as the on* dispatcher in hooks.go.
	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-time.After(rt.timeout):
			vm.Interrupt(fmt.Errorf("$app.routerAdd %s %s: timeout after %s",
				rr.method, rr.path, rt.timeout))
		case <-doneCh:
		}
	}()
	vm.ClearInterrupt()

	var thrown error
	func() {
		defer func() {
			if recv := recover(); recv != nil {
				thrown = fmt.Errorf("hook panicked: %v", recv)
			}
		}()
		if _, err := rr.fn(goja.Undefined(), e); err != nil {
			thrown = err
		}
	}()

	if thrown != nil {
		rt.log.Warn("hook: routerAdd handler threw",
			"method", rr.method, "path", rr.path, "err", thrown)
		writeHandlerError(w, thrown)
		return
	}

	// Flush captured response. If the handler didn't call json/text/
	// html OR status, default to 204 No Content — operators get a
	// visible signal that the handler ran but produced nothing.
	flushCapture(w, cap)
}

// captureWriter is an in-memory ResponseWriter analogue. Goja handlers
// build their response into it via json/text/html/header/status; we
// flush to the real writer once the handler returns (or threw).
type captureWriter struct {
	header http.Header
	status int
	body   *bytes.Buffer
}

// buildBodyWriter returns a goja-callable that sets status code,
// content-type, and writes a serialised body. Shared by json/text/html.
func buildBodyWriter(vm *goja.Runtime, cap *captureWriter, contentType string, serialise func(goja.Value) ([]byte, error)) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			return goja.Undefined()
		}
		// Single-arg form: `e.text("body")` defaults status to 200.
		// Two-arg form: `e.text(200, "body")` is the PB-compat shape.
		var status int = 200
		var bodyArg goja.Value
		if len(call.Arguments) == 1 {
			bodyArg = call.Arguments[0]
		} else {
			status = int(call.Arguments[0].ToInteger())
			bodyArg = call.Arguments[1]
		}
		body, err := serialise(bodyArg)
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("response serialise: %w", err)))
		}
		cap.status = status
		if cap.header.Get("Content-Type") == "" {
			cap.header.Set("Content-Type", contentType)
		}
		cap.body.Reset()
		cap.body.Write(body)
		return goja.Undefined()
	}
}

// flushCapture writes captured response state out to the real writer.
// Default status is 204 when the handler set nothing (no status, no
// body, no header) — distinct signal vs. a defaulted 200 OK.
func flushCapture(w http.ResponseWriter, cap *captureWriter) {
	for k, vs := range cap.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	status := cap.status
	hasBody := cap.body.Len() > 0
	switch {
	case status == 0 && hasBody:
		status = 200
	case status == 0:
		status = 204
	}
	w.WriteHeader(status)
	if hasBody {
		_, _ = w.Write(cap.body.Bytes())
	}
}

// writeHandlerError surfaces a thrown JS error as a structured 500. We
// don't leak the full stack to the client — operators inspect the slog
// "hook: routerAdd handler threw" entry for that. The JSON shape mirrors
// the rest of the API's error envelope so SDK consumers can branch on
// `body.error.code`.
func writeHandlerError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	msg := err.Error()
	// Keep the message under 200 chars in case the throw was a giant
	// stack — operators read the log for the full version.
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "hook_error",
			"message": msg,
		},
	})
	_, _ = w.Write(body)
}
