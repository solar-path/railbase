package hooks

// v1.7.38 — `$app.onRequest(handler)` JS hook binding. Closes the
// last open item in docs/17 §3.4.5 (the hook-dispatcher track):
// sibling to onRecord*, onAuth*, onMailer*, routerAdd, cronAdd —
// fires SYNCHRONOUSLY before every incoming HTTP request, ahead of
// the auth / tenant / rbac middleware stack.
//
// Architecture:
//
//   1. During Load(), each $app.onRequest(fn) call appends to a
//      per-Load requestHandlerSet (preserving declaration order).
//      VMs are stable across the load — captured Callables stay
//      bindable to the registering VM.
//   2. After all .js files run, requestHandlerSet.finalize() produces
//      the immutable snapshot the runtime swaps into r.onRequest
//      atomically. Hot-reload semantics: the old chain stays in use
//      for in-flight requests, the new chain catches the next one.
//   3. app.go installs `runtime.NewOnRequestMiddleware()` AHEAD of
//      i18n / csrf / auth / tenant — operators can mutate headers,
//      short-circuit unauthorised traffic, or augment ctx values
//      before any of that downstream stack reads them.
//   4. Each handler runs under the per-handler watchdog (default 500ms
//      — tighter than the on*-event default 5s because this fires on
//      EVERY request and we'd rather 500 than hang the front door).
//   5. The /_/* admin UI assets path skips the dispatcher entirely
//      (those URLs fire many sub-requests for static assets — hook
//      overhead would compound visibly on every page load).
//
// JS API:
//
//   $app.onRequest((e) => {
//       // e.request — method/path/url/query/headers/body
//       // e.request.header(name, value) — mutate request headers
//       // e.response.header(name, value) — set response headers
//       // e.abort(status, body) — short-circuit with a response
//       // e.next() — proceed to the next handler (or downstream)
//   });
//
// Chain semantics (express-style):
//
//   handler A → handler B → downstream handler chain
//                  ↑
//   each handler MUST call e.next() to continue OR e.abort() to halt.
//   Calling neither implicitly continues (more forgiving — matches
//   the on*-event next() pattern where the flag is a hint, not a hard
//   gate). Multiple .next() calls within one handler are idempotent;
//   abort() takes precedence (an aborted request stops the chain even
//   if a peer also called next()).
//
// Design call on next() vs CPS:
//
//   We use a FLAG-based chain (not true continuation-passing style).
//   Each handler runs to completion, then the dispatcher inspects the
//   ev.aborted / ev.hasNext flags to decide whether to continue. This
//   matches the existing RecordEvent.next() pattern in hooks.go and
//   keeps the VM lock semantics simple (one handler at a time, no
//   nested re-entry into goja). True CPS — where .next() recursively
//   invokes the rest of the chain from inside the handler — would
//   require either (a) nested vm.Lock acquisition (deadlock unless
//   we switch to a reentrant lock) or (b) a per-request VM pool. Both
//   are valid v1.8.x optimisations; v1.7.38 keeps the surface tight.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// DefaultOnRequestTimeout bounds how long a single onRequest handler
// can run. Tighter than DefaultTimeout (5s) because this fires on every
// request — a slow handler delays every page load, not just the rare
// CRUD write. 500ms catches legitimate sync work (header inspection,
// quick allow/deny checks) while killing runaway loops promptly.
const DefaultOnRequestTimeout = 500 * time.Millisecond

// adminUIPathPrefix is the prefix that addresses the embedded admin UI
// assets. We skip the onRequest dispatcher for these paths because the
// admin SPA fires many sub-requests per page (HTML, JS bundles, fonts,
// images) and hook overhead would compound visibly. Operators wanting
// to gate the admin UI itself should do so via Go-side middleware.
const adminUIPathPrefix = "/_/"

// requestHandlerSet collects $app.onRequest registrations during one
// Load() and produces the immutable snapshot the middleware reads.
// Sibling to routerRouteSet / cronSet.
type requestHandlerSet struct {
	mu       sync.Mutex
	handlers []*requestHandler
}

// requestHandler is one registered $app.onRequest callback. fn binds
// to the VM that ran the registration; serveRequest switches back to
// that VM at dispatch.
type requestHandler struct {
	fn     goja.Callable
	vm     *goja.Runtime
	source string // file path, for log/error messages
}

func newRequestHandlerSet() *requestHandlerSet {
	return &requestHandlerSet{}
}

// register validates the call arguments and appends one handler.
// Returns a structured error the caller surfaces as a JS thrown
// Error via panic(vm.NewGoError(...)).
func (rs *requestHandlerSet) register(vm *goja.Runtime, call goja.FunctionCall) error {
	if len(call.Arguments) < 1 {
		return fmt.Errorf("expected (handler), got %d argument(s)", len(call.Arguments))
	}
	fn, ok := goja.AssertFunction(call.Arguments[0])
	if !ok {
		return fmt.Errorf("handler must be a function")
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.handlers = append(rs.handlers, &requestHandler{fn: fn, vm: vm})
	return nil
}

// finalize returns the snapshot the runtime swaps in. Returns nil for
// an empty set so the middleware short-circuits at zero cost.
func (rs *requestHandlerSet) finalize() *requestHandlerSet {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.handlers) == 0 {
		return nil
	}
	return rs
}

// NewOnRequestMiddleware returns a chi-compatible middleware that runs
// every registered $app.onRequest handler before the request flows to
// the rest of the chain. Handlers can mutate request headers, abort
// with a response, or simply observe — declaration-order chained.
//
// Zero-cost fast path: when no handlers are registered (atomic pointer
// is nil) AND the runtime itself is nil, the middleware is a
// pass-through. We check the pointer on EVERY request — it's a single
// atomic load — so hot-reload swaps land immediately.
//
// The /_/* admin UI assets path bypasses the dispatcher entirely.
func (r *Runtime) NewOnRequestMiddleware() func(http.Handler) http.Handler {
	if r == nil {
		// Nil runtime → no-op middleware (safe to wire even when hooks
		// are disabled, mirrors RouterMiddleware's nil-safe pattern).
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// Skip the admin UI assets path entirely — hot path.
			if strings.HasPrefix(req.URL.Path, adminUIPathPrefix) {
				next.ServeHTTP(w, req)
				return
			}
			snap := r.onRequest.Load()
			if snap == nil || len(snap.handlers) == 0 {
				next.ServeHTTP(w, req)
				return
			}
			r.dispatchOnRequest(w, req, next, snap)
		})
	}
}

// dispatchOnRequest runs the chain of registered onRequest handlers in
// declaration order. Each handler gets its own JS `e` object with
// request/response views and next/abort signals; the dispatcher
// inspects the flags after each handler to decide whether to continue.
//
// Aborted: write captured response, return — downstream NEVER runs.
// Otherwise (next or implicit-continue): proceed to the next handler.
// After the last handler: invoke next.ServeHTTP for the rest of the
// chi chain.
func (rt *Runtime) dispatchOnRequest(w http.ResponseWriter, req *http.Request, next http.Handler, snap *requestHandlerSet) {
	// Buffer body so a handler can call e.request.body() without
	// consuming the stream for downstream handlers / the eventual
	// REST handler. 8 MiB safety cap mirrors routerAdd's serveRoute.
	bodyBytes, _ := io.ReadAll(io.LimitReader(req.Body, 8<<20))
	_ = req.Body.Close()
	// Restore a fresh reader so downstream middleware reads the same
	// bytes. This costs one alloc per request — acceptable for the
	// observability + mutation primitives the hook provides.
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// We serialise VM access (one VM per Runtime in v1.7.38 — same as
	// the rest of the dispatchers). The lock is released between
	// handlers in the chain only if we explicitly drop it; we don't,
	// because the chain is short and locked-out time is bounded by
	// the per-handler watchdog.
	rt.vmMu.Lock()
	defer rt.vmMu.Unlock()

	for _, h := range snap.handlers {
		aborted, abortStatus, abortBody, abortHeaders, err := rt.runOneRequestHandler(h, req, bodyBytes)
		if err != nil {
			// Handler threw — log + 500. Don't continue the chain.
			rt.log.Warn("hook: onRequest handler threw",
				"source", h.source, "method", req.Method, "path", req.URL.Path, "err", err)
			writeHandlerError(w, err)
			return
		}
		if aborted {
			// Flush the abort response and stop. Downstream NEVER fires.
			for k, vs := range abortHeaders {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			if abortStatus == 0 {
				abortStatus = http.StatusForbidden
			}
			w.WriteHeader(abortStatus)
			if len(abortBody) > 0 {
				_, _ = w.Write(abortBody)
			}
			return
		}
	}
	// Whole chain ran without abort — proceed to the rest of the
	// middleware chain. The mutated request (header/value changes
	// applied by handlers) flows through.
	next.ServeHTTP(w, req)
}

// runOneRequestHandler invokes one $app.onRequest handler under the
// watchdog. Returns the abort flags (status/body/headers) captured by
// any e.abort(...) call, or err if the handler threw. The dispatcher
// caller flushes / propagates as appropriate.
//
// The handler's e.request.header(name, value) call mutates the actual
// req.Header — downstream middleware sees the change. Response-side
// header mutations go into a buffer that the dispatcher flushes only
// on abort (a continued chain doesn't write a response — downstream
// does).
func (rt *Runtime) runOneRequestHandler(h *requestHandler, req *http.Request, bodyBytes []byte) (aborted bool, abortStatus int, abortBody []byte, abortHeaders http.Header, err error) {
	vm := h.vm

	// Build the `e` object the handler sees.
	e := vm.NewObject()

	// State captured by abort/next calls. Closures below mutate.
	var (
		didAbort         bool
		capturedStatus   int
		capturedBody     []byte
		capturedHeaders  = http.Header{}
		capturedRespHdrs = http.Header{} // staged response headers (flushed only on abort)
	)

	// e.request — observability + header-mutation view of the request.
	request := vm.NewObject()
	_ = request.Set("method", req.Method)
	_ = request.Set("path", req.URL.Path)
	_ = request.Set("url", req.URL.String())
	_ = request.Set("body", string(bodyBytes))

	// e.request.query → JS object whose values are first-value strings
	// (the common case). Multi-value access via [0]/[1] indexing into
	// the underlying array — same shape as routerAdd's `query`.
	q := vm.NewObject()
	for k, vs := range req.URL.Query() {
		_ = q.Set(k, append([]string(nil), vs...))
	}
	_ = request.Set("query", q)

	// e.request.headers — read-only snapshot (first value per key).
	// For multi-value or mutation, authors call e.request.header(...).
	hd := vm.NewObject()
	for k := range req.Header {
		_ = hd.Set(k, req.Header.Get(k))
	}
	_ = request.Set("headers", hd)

	// e.request.header(name, value?) — getter when called with one
	// argument, setter with two. Setter mutates the live req.Header
	// so downstream middleware sees the change.
	_ = request.Set("header", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		name := call.Arguments[0].String()
		if len(call.Arguments) == 1 {
			return vm.ToValue(req.Header.Get(name))
		}
		val := call.Arguments[1].String()
		req.Header.Set(name, val)
		return goja.Undefined()
	})
	_ = e.Set("request", request)

	// e.response — staged response-header object. Headers set here
	// flush ONLY on abort; a continued chain hands the request to
	// downstream which writes its own response.
	response := vm.NewObject()
	_ = response.Set("header", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			return goja.Undefined()
		}
		capturedRespHdrs.Set(call.Arguments[0].String(), call.Arguments[1].String())
		return goja.Undefined()
	})
	_ = e.Set("response", response)

	// e.next() — signals the handler is done and the chain should
	// continue. Idempotent. Has no effect when paired with abort()
	// (abort wins).
	_ = e.Set("next", func(_ goja.FunctionCall) goja.Value {
		return goja.Undefined()
	})

	// e.abort(status, body?) — short-circuit with a response.
	// body can be a string OR an object (JSON-serialised). Headers
	// staged via e.response.header(...) flush alongside.
	_ = e.Set("abort", func(call goja.FunctionCall) goja.Value {
		didAbort = true
		if len(call.Arguments) > 0 {
			capturedStatus = int(call.Arguments[0].ToInteger())
		}
		if len(call.Arguments) > 1 {
			bodyArg := call.Arguments[1]
			// String → write as-is. Object → JSON-serialise + content-type.
			switch exported := bodyArg.Export().(type) {
			case string:
				capturedBody = []byte(exported)
				if capturedRespHdrs.Get("Content-Type") == "" {
					capturedRespHdrs.Set("Content-Type", "text/plain; charset=utf-8")
				}
			default:
				if b, jerr := json.Marshal(exported); jerr == nil {
					capturedBody = b
					if capturedRespHdrs.Get("Content-Type") == "" {
						capturedRespHdrs.Set("Content-Type", "application/json")
					}
				}
			}
		}
		// Merge captured response headers into the abort flush set.
		for k, vs := range capturedRespHdrs {
			for _, v := range vs {
				capturedHeaders.Add(k, v)
			}
		}
		return goja.Undefined()
	})

	// Watchdog — same pattern as routerAdd / cron / on*-event.
	doneCh := make(chan struct{})
	defer close(doneCh)
	timeout := rt.timeout
	// onRequest gets its own tighter default. If the operator passed an
	// explicit Timeout to NewRuntime, honour it for parity with the
	// other dispatchers; otherwise apply the per-request budget.
	if timeout > DefaultOnRequestTimeout {
		timeout = DefaultOnRequestTimeout
	}
	go func() {
		select {
		case <-time.After(timeout):
			vm.Interrupt(fmt.Errorf("$app.onRequest: timeout after %s", timeout))
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
		if _, ferr := h.fn(goja.Undefined(), e); ferr != nil {
			thrown = ferr
		}
	}()

	if thrown != nil {
		var jsErr *goja.Exception
		msg := thrown.Error()
		if errors.As(thrown, &jsErr) {
			msg = jsErr.Value().String()
		}
		return false, 0, nil, nil, fmt.Errorf("%s", msg)
	}
	return didAbort, capturedStatus, capturedBody, capturedHeaders, nil
}

// HasOnRequestHandlers reports whether any $app.onRequest handlers are
// registered. Useful for telemetry / smoke tests; the middleware
// itself does its own atomic-pointer check inline.
func (r *Runtime) HasOnRequestHandlers() bool {
	if r == nil {
		return false
	}
	snap := r.onRequest.Load()
	return snap != nil && len(snap.handlers) > 0
}

// suppress unused — context is reserved for a future per-request ctx
// integration (e.g. propagating req.Context() into goja for cancellation).
var _ = context.Background
