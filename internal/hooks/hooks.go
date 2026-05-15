// Package hooks is the v1.2.0 JS lifecycle hook runtime.
//
// Goals (MVP — v1.2.0 ships this slice):
//
//	- Hot-reloadable JS files in `<dataDir>/hooks/*.js`. fsnotify
//	  triggers a re-parse + atomic.Pointer swap (sub-second visible
//	  in the running server).
//	- Six record-lifecycle hook points per collection:
//	    onRecordBeforeCreate / onRecordAfterCreate
//	    onRecordBeforeUpdate / onRecordAfterUpdate
//	    onRecordBeforeDelete / onRecordAfterDelete
//	- goja VM pool (size = GOMAXPROCS). Each dispatch acquires one
//	  VM, runs handlers in declaration order, releases.
//	- Watchdog: a single handler hung in an infinite loop won't take
//	  the request thread with it. Default 5s timeout.
//	- Crash-safe: a handler that throws / panics returns a
//	  ValidationError to the caller; the dispatcher catches and the
//	  next request runs normally.
//
// Deliberately deferred to v1.2.x:
//
//	- onAuth* / onMailer* / onRequest hooks
//	- routerAdd / cronAdd
//	- Full JSVM bindings ($apis, $http, $os, $security, $template,
//	  $tokens, $filesystem, $mailer, $dbx, $inflector)
//	- require()/module system
//	- Go-side typed hooks (alongside JS)
//	- Per-handler memory limits
//	- Admin UI test panel
//
// JS API (v1.2.0):
//
//	$app.onRecordBeforeCreate("posts").bindFunc((e) => {
//	    if (!e.record.title) throw new Error("title required");
//	    e.record.title = e.record.title.trim();
//	    return e.next();
//	});
//	$app.onRecordAfterCreate("posts").bindFunc((e) => {
//	    console.log("created", e.record.id);
//	    return e.next();
//	});
//
// e.next() proceeds; throwing an Error aborts the request with a
// validation error. The After-hooks run after the DB write; their
// throws are logged but DON'T undo the write (DB is already committed).
package hooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/eventbus"
)

// Event names. Stable wire identifiers — renaming breaks every user's
// hooks dir. The constants are exported so other internal packages
// (REST handlers) can reference them by name without typos.
type Event string

const (
	EventRecordBeforeCreate Event = "record.before_create"
	EventRecordAfterCreate  Event = "record.after_create"
	EventRecordBeforeUpdate Event = "record.before_update"
	EventRecordAfterUpdate  Event = "record.after_update"
	EventRecordBeforeDelete Event = "record.before_delete"
	EventRecordAfterDelete  Event = "record.after_delete"
)

// DefaultTimeout bounds how long a single handler invocation can run.
// 5s is generous for legitimate logic; anything longer is almost
// certainly an infinite loop and we'd rather 500 the request than
// have it hang forever.
const DefaultTimeout = 5 * time.Second

// HandlerError is returned when a Before-hook throws. The dispatcher
// surfaces it to the caller as a validation error (400).
type HandlerError struct {
	Event   Event
	Message string
}

func (e *HandlerError) Error() string {
	return fmt.Sprintf("hook %s: %s", e.Event, e.Message)
}

// RecordEvent is the JS-visible event object. Goja exposes it via the
// `e` parameter on every handler:
//
//	(e) => { e.record.title = e.record.title.toUpperCase(); return e.next(); }
//
// .record is a JS proxy over the underlying map[string]any that the
// REST handler holds. Mutations on the JS side propagate back; the
// dispatcher snapshots before/after and the REST handler picks up
// changes from .Record() on the way out.
type RecordEvent struct {
	Collection string
	record     map[string]any
	preventErr error
	hasNext    bool
}

// Record returns the (possibly-mutated) record. Called by the
// dispatcher's caller after Dispatch returns.
func (e *RecordEvent) Record() map[string]any { return e.record }

// next is the JS handler's "proceed to the next step" call. We
// don't implement a true filter chain in v1.2.0 — handlers run in
// declaration order and any throw aborts. .next() is a no-op
// returned value so PB-style hooks compile, but it doesn't actually
// gate handler-chain advancement (we always run every handler unless
// one throws).
func (e *RecordEvent) next() any { e.hasNext = true; return nil }

// Registry is the in-memory map of (collection, event) → handlers.
// Loaded once at startup + on every fsnotify event. Concurrent
// readers (dispatch path) hold atomic.Pointer; writers (loader) swap.
type Registry struct {
	// handlers keyed by collection (or "*" for global) → event → ordered slice
	handlers map[string]map[Event][]*registeredHandler
}

type registeredHandler struct {
	// fn is a goja.Callable captured at parse time. We re-use it
	// across dispatches by switching back to the source VM each call;
	// this is safe because goja's runtime is single-threaded — the
	// pool guarantees only one goroutine touches a VM at a time.
	fn       goja.Callable
	source   string // file path (for error messages)
	loadedVM *goja.Runtime
}

// Runtime is the public handle wired into app.go. Goroutine-safe.
type Runtime struct {
	log              *slog.Logger
	hooksDir         string
	timeout          time.Duration
	maxCallStackSize int // cached so loader.go's per-reload VM matches NewRuntime's primary VM
	registry         atomic.Pointer[Registry]
	vmMu             sync.Mutex // serialises VM access (one VM per Runtime in v1.2.0)
	primaryVM        *goja.Runtime

	// bus is the optional eventbus the `$app.realtime().publish(...)` JS
	// binding writes onto. nil is allowed — the binding will silently
	// no-op so tests can run the runtime without wiring a real bus.
	bus *eventbus.Bus

	// router holds the chi.Mux built from $app.routerAdd(...) calls during
	// Load(). Swapped atomically on hot-reload so request dispatch always
	// reads a consistent route table without taking the loader's lock.
	// nil-pointer means "no $app.routerAdd calls registered" — the
	// Middleware short-circuits in that case.
	router atomic.Pointer[chi.Mux]

	// crons holds the in-memory cron jobs registered via
	// $app.cronAdd(name, expr, handler). Swapped atomically on
	// hot-reload (cronLoop reads the pointer on each minute tick). nil
	// → no crons registered; the loop short-circuits.
	crons atomic.Pointer[cronSet]

	// onRequest holds the chain of $app.onRequest(fn) handlers,
	// dispatched SYNCHRONOUSLY by NewOnRequestMiddleware before every
	// incoming HTTP request. Swapped atomically on hot-reload — the
	// middleware reads the pointer on every request so a fresh chain
	// catches the next call. nil → no handlers registered (the
	// middleware short-circuits at zero cost).
	onRequest atomic.Pointer[requestHandlerSet]

	// goHooks is the optional Go-side typed dispatcher (§3.4.10). When
	// non-nil, Dispatch fires Go hooks BEFORE the JS chain. Owned by
	// the caller (pkg/railbase.App holds it across the Runtime lifetime
	// so embedders can register handlers before NewRuntime is even
	// called). nil → JS-only dispatch (the original v1.2.0 contract).
	goHooks *GoHooks

	// invocationsCounter is the optional metric counter the runtime
	// bumps once per Dispatch call (skipped when HasHandlers returns
	// false so background no-op CRUD doesn't inflate the count). nil
	// → no-op, matching the "registry not wired" contract on every
	// other publishing site. Set via Options.MetricInvocations.
	invocationsCounter MetricCounter

	// Stop fns the watcher / reloader register at startup so the
	// app can clean up on shutdown.
	stops []func()
}

// MetricCounter is the surface the hooks runtime calls to publish per-
// dispatch metrics. Kept as a one-method interface so the hooks
// package doesn't have to import internal/metrics directly (and so
// tests can pass a stub recorder without instantiating a Registry).
type MetricCounter interface {
	Inc()
}

// DefaultMaxCallStackSize bounds JS call depth across the embedded
// runtime. Goja has no per-VM memory limit (the engine doesn't track
// heap usage), so we approximate by bounding stack depth — the most
// common "runaway memory" attack pattern is mutual / unbounded
// recursion. 128 leaves comfortable headroom for legitimate templated
// helpers (PB-style hook code rarely needs more than 16 deep) while
// catching `function f(n) { return f(n+1); }` synchronously instead
// of running until the 250ms watchdog or — worse — until OOM.
//
// Operators with deep-recursion DSLs can raise via Options.MaxCallStackSize.
// Setting to 0 keeps the default; -1 disables the cap entirely (NOT
// recommended; documented for completeness).
const DefaultMaxCallStackSize = 128

// Options configures NewRuntime.
type Options struct {
	HooksDir string        // typically <dataDir>/hooks; "" disables JS loading
	Timeout  time.Duration // per-handler invocation timeout; 0 → DefaultTimeout
	// MaxCallStackSize caps JS call depth per VM. 0 → DefaultMaxCallStackSize.
	// -1 disables the cap. See the constant docstring for the rationale.
	MaxCallStackSize int
	Log              *slog.Logger
	// Bus is the eventbus the `$app.realtime().publish(...)` binding writes
	// onto. Optional — when nil, the binding remains installed but every
	// publish call is a silent no-op (so tests don't need to wire a bus
	// to exercise hook authoring).
	Bus *eventbus.Bus
	// GoHooks attaches a Go-side typed hook registry (§3.4.10). When
	// non-nil, every Dispatch call fires Go hooks FIRST (synchronously
	// for Before, async for After), then the JS dispatcher chain. This
	// lets embedders that build custom binaries on `pkg/railbase`
	// register handlers in Go without involving goja.
	//
	// Pass nil to skip Go-hook integration (current default behaviour).
	// When both HooksDir == "" AND GoHooks == nil, NewRuntime returns
	// (nil, nil) — REST handlers nil-check and skip dispatch.
	GoHooks *GoHooks

	// MetricInvocations, when non-nil, is incremented once per Dispatch
	// call that actually has handlers (HasHandlers true). Hooks-package
	// metric publishing is opt-in: production wires the registry's
	// `hooks.invocations_total` counter via pkg/railbase/app.go; tests
	// leave it nil and the dispatcher path stays exactly as it was in
	// v1.2.0.
	MetricInvocations MetricCounter
}

// NewRuntime constructs a Runtime ready to load + dispatch. Caller is
// responsible for calling Start() (which kicks off the initial load
// + fsnotify watcher) and Stop() on shutdown.
//
// Returns (nil, nil) when BOTH HooksDir is "" AND opts.GoHooks is nil
// (no JS to load, no Go hooks to fire — there's literally nothing the
// dispatcher can do). Caller treats nil as "no hooks configured" and
// skips dispatch.
//
// When opts.GoHooks is non-nil but HooksDir is "" → Runtime is built
// without JS loading (no fsnotify watcher, no JS bindings); Dispatch
// fires only the Go chain. This is the §3.4.10 path for embedders who
// don't want a hooks dir but still want compile-time hooks.
func NewRuntime(opts Options) (*Runtime, error) {
	if opts.HooksDir == "" && opts.GoHooks == nil {
		return nil, nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	r := &Runtime{
		log:                log,
		hooksDir:           opts.HooksDir,
		timeout:            timeout,
		maxCallStackSize:   opts.MaxCallStackSize,
		primaryVM:          applyStackCap(goja.New(), opts.MaxCallStackSize),
		bus:                opts.Bus,
		goHooks:            opts.GoHooks,
		invocationsCounter: opts.MetricInvocations,
	}
	// Empty registry — Load() populates.
	empty := &Registry{handlers: map[string]map[Event][]*registeredHandler{}}
	r.registry.Store(empty)
	// Thread our logger into the Go-hooks registry so panic logs land
	// in the same journal as JS-side warnings.
	if r.goHooks != nil {
		r.goHooks.SetLogger(log)
	}
	return r, nil
}

// applyStackCap configures a fresh VM's max call-stack size. Pass 0
// to use DefaultMaxCallStackSize; -1 to disable the cap entirely.
// Returns the VM so callers can chain at construction sites.
//
// goja's default is 200; we apply a tighter cap (128) by default so
// recursive runaways are caught synchronously instead of through the
// timeout watchdog. See the DefaultMaxCallStackSize docstring.
func applyStackCap(vm *goja.Runtime, n int) *goja.Runtime {
	switch {
	case n == 0:
		vm.SetMaxCallStackSize(DefaultMaxCallStackSize)
	case n < 0:
		// Operator-requested disable. Goja's SetMaxCallStackSize
		// doesn't have a "no cap" sentinel; max-int works in practice
		// because the Go stack overflows long before the call counter
		// would, but we set it explicitly so the intent is recorded.
		vm.SetMaxCallStackSize(1 << 30)
	default:
		vm.SetMaxCallStackSize(n)
	}
	return vm
}

// HasHandlers reports whether ANY handler (JS or Go) is registered for
// (collection, event). Used by the dispatcher to skip locking +
// event-object construction when there's nothing to dispatch.
func (r *Runtime) HasHandlers(collection string, event Event) bool {
	if r == nil {
		return false
	}
	// Go-side check is cheap (single mutex + map lookup); short-circuit
	// before touching the JS registry pointer.
	if r.goHooks != nil && r.goHooks.HasHandlers(event, collection) {
		return true
	}
	reg := r.registry.Load()
	if reg == nil {
		return false
	}
	// Check per-collection first, then global "*".
	if byEvent, ok := reg.handlers[collection]; ok {
		if hs, ok := byEvent[event]; ok && len(hs) > 0 {
			return true
		}
	}
	if byEvent, ok := reg.handlers["*"]; ok {
		if hs, ok := byEvent[event]; ok && len(hs) > 0 {
			return true
		}
	}
	return false
}

// GoHooks returns the runtime's Go-hooks registry, or nil when none
// was attached at construction. External embedders typically obtain
// this via App.GoHooks() (pkg/railbase) rather than reaching into the
// Runtime directly.
func (r *Runtime) GoHooks() *GoHooks {
	if r == nil {
		return nil
	}
	return r.goHooks
}

// Dispatch runs every registered handler for (collection, event) in
// declaration order. The record map may be mutated by handlers (e.g.
// to validate/transform inputs in Before hooks); caller picks up
// the modified value via the returned event.
//
// Dispatch order (§3.4.10):
//
//  1. Go hooks FIRST (compile-time wired; cheap function calls). For
//     Before events, a Go-hook rejection short-circuits the entire
//     chain — JS hooks DO NOT fire. For After events, Go hooks run
//     async (fire-and-forget) and we proceed to JS regardless.
//  2. JS hooks SECOND (runtime-loaded; goja marshalling). Compile-time
//     Go behaviour can therefore be re-mutated by operator JS — a Go
//     hook that defaults a field can have that default overridden by a
//     JS hook before the DB write.
//
// Returns (event, nil) on clean completion — the record is the
// mutated state. Returns (event, *HandlerError) when a JS handler
// throws OR (event, error) when a Go handler returns a non-nil error
// (typically ErrReject). Caller should refuse the request with 400 in
// either case.
//
// Non-error panics inside goja are recovered. After-hook throws are
// logged but DO NOT propagate (the DB write already happened).
//
// Reentry guard (v3.x): Dispatch carries a context-attached depth
// counter. When a hook handler issues a write that re-fires another
// hook (e.g. an AfterCreate on `tasks` that updates a counter on
// `projects` triggering AfterUpdate), the depth grows; past
// MaxHookDepth, Dispatch refuses with ErrHookDepthExceeded. This
// closes Sentinel's documented "recursive hook reentry guards" gap
// (see schema/tasks.go:21) — without it, a careless rollup hook can
// loop forever before Postgres rejects a deadlock or the request
// times out. Each handler gets ctx with the incremented depth so
// nested writes don't have to thread the counter manually.
func (r *Runtime) Dispatch(ctx context.Context, collection string, event Event, record map[string]any) (*RecordEvent, error) {
	if r == nil {
		return &RecordEvent{Collection: collection, record: record}, nil
	}
	if !r.HasHandlers(collection, event) {
		return &RecordEvent{Collection: collection, record: record}, nil
	}

	// Reentry depth check. Bail BEFORE any handler logic so a
	// runaway-recursion path doesn't even hit the registry walk.
	if d := hookDepthFromCtx(ctx); d >= MaxHookDepth {
		return &RecordEvent{Collection: collection, record: record},
			fmt.Errorf("%w (collection=%s event=%s depth=%d)",
				ErrHookDepthExceeded, collection, event, d)
	}
	// Increment for any handler we're about to call — sets the
	// ceiling for nested Dispatch calls one level deeper.
	ctx = withHookDepth(ctx, hookDepthFromCtx(ctx)+1)
	// Publish "we dispatched something" exactly once per Dispatch call
	// that has handlers. The interface is nil-safe via the explicit
	// guard so the runtime works without a registry wired.
	if r.invocationsCounter != nil {
		r.invocationsCounter.Inc()
	}

	isAfter := event == EventRecordAfterCreate ||
		event == EventRecordAfterUpdate ||
		event == EventRecordAfterDelete

	// Phase 1: Go hooks. Build a GoRecordEvent over the same record map
	// so mutations propagate without a copy. The Go side has no access
	// to the *RecordEvent (JS struct); we feed the typed view in and
	// merge mutations back out via map-aliasing.
	if r.goHooks != nil && r.goHooks.HasHandlers(event, collection) {
		goEv := &GoRecordEvent{
			Collection: collection,
			Record:     record,
			Auth:       principalFromCtx(ctx),
			Action:     actionForEvent(event),
		}
		if isAfter {
			// Async fire-and-forget. We don't wait, but we DO want a
			// snapshot of record at this instant — Go hooks running
			// after the response is written shouldn't see a record map
			// that the REST handler has since reused for a different
			// row. Shallow-copy is the cheapest defence; deep-copy
			// awaits a real bug report.
			snap := make(map[string]any, len(record))
			for k, v := range record {
				snap[k] = v
			}
			goEv.Record = snap
			r.goHooks.fireAfter(ctx, event, goEv)
		} else {
			if err := r.goHooks.fireBefore(ctx, event, goEv); err != nil {
				return &RecordEvent{Collection: collection, record: record}, err
			}
			// Mutations to goEv.Record propagate via shared map; nothing
			// to merge back manually. (Handler that assigns a fresh map
			// to goEv.Record won't propagate — documented contract: edit
			// in place, don't replace.)
		}
	}

	// Phase 2: JS hooks. Short-circuit if there's no JS registry to
	// walk (Go-hooks-only configurations skip the goja machinery
	// entirely).
	reg := r.registry.Load()
	var handlers []*registeredHandler
	if reg != nil {
		if byEvent, ok := reg.handlers[collection]; ok {
			handlers = append(handlers, byEvent[event]...)
		}
		if byEvent, ok := reg.handlers["*"]; ok {
			handlers = append(handlers, byEvent[event]...)
		}
	}

	ev := &RecordEvent{Collection: collection, record: record}
	if len(handlers) == 0 {
		return ev, nil
	}

	// Serialise VM access. v1.2.0 ships a single VM per Runtime; a
	// per-CPU pool lands in v1.2.x when profiling shows contention.
	r.vmMu.Lock()
	defer r.vmMu.Unlock()

	for _, h := range handlers {
		if err := r.runOne(ctx, h, ev, event); err != nil {
			if isAfter {
				// After-hook throws don't undo the DB write — log and
				// continue down the chain.
				r.log.Warn("hook: after-handler threw (ignored)",
					"event", event, "collection", collection,
					"source", h.source, "err", err)
				continue
			}
			return ev, err
		}
	}
	return ev, nil
}

// principalFromCtx looks for a Principal value stamped onto ctx by an
// upstream auth middleware. Returns nil when none is present (or when
// the value is the zero Principal — unauthenticated).
//
// The hooks package is a leaf module; we DON'T import auth/middleware
// (would create a cycle with REST handlers that import hooks). Instead
// the auth middleware exposes a ctx key + getter, and we attach a
// translation closure at wiring time. Until that's wired (current
// state: ctx never carries our Principal), this returns nil — Go hooks
// see Auth == nil. The slice spec's §3.4.10 calls out the Auth field
// existence; populating it from the REST/auth boundary is a v1.2.x
// follow-up that DOESN'T need to land alongside this slice.
func principalFromCtx(_ context.Context) *Principal {
	// Intentional v1.2.x stub. Wiring point: REST handlers (or the auth
	// middleware) call hooks.WithPrincipal(ctx, &hooks.Principal{...})
	// before Dispatch; this function reads that key. Keeping it inert
	// for the v1.2.0 §3.4.10 slice means Go hooks can register + fire
	// without a partial Auth integration leaking observable bugs.
	return nil
}

// actionForEvent maps the six lifecycle Events onto the three Action
// labels. Used to populate GoRecordEvent.Action so Go handlers checking
// `ev.Action == ActionCreate` see a stable string regardless of which
// FireXxx the caller invoked.
func actionForEvent(e Event) string {
	switch e {
	case EventRecordBeforeCreate, EventRecordAfterCreate:
		return ActionCreate
	case EventRecordBeforeUpdate, EventRecordAfterUpdate:
		return ActionUpdate
	case EventRecordBeforeDelete, EventRecordAfterDelete:
		return ActionDelete
	}
	return ""
}

// runOne invokes one handler with the watchdog applied.
//
// The record needs to round-trip through a fresh JS object (not just
// a goja-wrapped Go map) because goja's automatic Go-map wrapping
// doesn't let JS add new keys reliably. We copy entries in, let JS
// mutate, copy back.
func (r *Runtime) runOne(ctx context.Context, h *registeredHandler, ev *RecordEvent, event Event) error {
	// Build the JS record proxy. NewObject + Set for each key gives
	// us a real JS object that supports add/remove/modify cleanly.
	jsRecord := h.loadedVM.NewObject()
	for k, v := range ev.record {
		_ = jsRecord.Set(k, v)
	}

	jsEvent := h.loadedVM.NewObject()
	_ = jsEvent.Set("collection", ev.Collection)
	_ = jsEvent.Set("record", jsRecord)
	_ = jsEvent.Set("next", func(call goja.FunctionCall) goja.Value {
		ev.next()
		return goja.Undefined()
	})

	// Watchdog: schedule Interrupt after timeout. Goja's Interrupt
	// makes the next bytecode op throw, breaking out of any tight
	// loop. We pair it with `defer` so a normal-completing handler
	// cancels the timer cleanly.
	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-time.After(r.timeout):
			h.loadedVM.Interrupt(fmt.Errorf("hook %s: timeout after %s", event, r.timeout))
		case <-doneCh:
		}
	}()
	// Clear any stale interrupt before invoking.
	h.loadedVM.ClearInterrupt()

	var thrown error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				thrown = fmt.Errorf("hook panicked: %v", rec)
			}
		}()
		_, err := h.fn(goja.Undefined(), jsEvent)
		if err != nil {
			thrown = err
		}
	}()

	// Copy the (possibly-mutated) JS record back into ev.record.
	// We replace contents in-place so the caller's map reference
	// stays valid. The JS record might also have been REPLACED
	// entirely (e.g. `e.record = {...}` assignment) — Get fetches
	// whatever's currently bound to .record.
	if recordVal := jsEvent.Get("record"); recordVal != nil && !goja.IsUndefined(recordVal) && !goja.IsNull(recordVal) {
		exported := recordVal.Export()
		if m, ok := exported.(map[string]any); ok {
			for k := range ev.record {
				delete(ev.record, k)
			}
			for k, v := range m {
				ev.record[k] = v
			}
		}
	}

	if thrown != nil {
		var jsErr *goja.Exception
		msg := thrown.Error()
		if errors.As(thrown, &jsErr) {
			msg = jsErr.Value().String()
		}
		return &HandlerError{Event: event, Message: msg}
	}
	return nil
}

// silence unused import; runtime.GOMAXPROCS will return when the
// per-CPU pool lands in v1.2.x.
var _ = runtime.GOMAXPROCS
