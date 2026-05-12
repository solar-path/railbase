// go_hooks.go — Go-side typed lifecycle hooks (§3.4.10).
//
// Closes the §3.4.10 plan.md slice. The JS hook runtime (hooks.go,
// loader.go) ships PB-compatible scripting for operators who edit
// `<dataDir>/hooks/*.js` and reload on save. THIS file is the
// compile-time sibling: Go programs that embed `pkg/railbase` get a
// type-safe, allocation-cheap way to register lifecycle handlers
// without involving goja at all.
//
// Why both surfaces:
//
//   - JS hooks: optimised for operators. Edit a file, save, hot-reload —
//     no recompile, no Go knowledge. The price is goja marshalling on
//     every event (~50µs/event) and a watchdog.
//   - Go hooks: optimised for embedders. A team that builds a custom
//     binary on top of `pkg/railbase` registers handlers in their
//     `main()` and pays only a slice-walk + function-call per dispatch
//     (~50ns/event). No JS, no marshalling, no hot reload — the binary
//     itself is the unit of deployment.
//
// Both fire on every event in defined order: Go hooks first (compile-time
// wired, runs synchronously), then JS hooks (runtime-loaded, runs through
// goja). Together they let operators OVERRIDE compiled-in behaviour from
// JS — a Go hook that adds a default value can be re-mutated by a JS
// hook before the DB write.
//
// Behaviour contract (mirrors what the slice spec demands):
//
//   - Register-then-fire: registration is mutex-guarded but happens at
//     boot. No hot-reload — Go hooks are compile-time wired.
//   - Per-collection AND wildcard ("" = all collections) handlers. The
//     wildcard bucket fires FIRST, then per-collection, both in
//     registration order. Rationale: wildcards typically install
//     cross-cutting concerns (audit, tracing); collection-specific
//     handlers should see whatever the wildcards left behind, and may
//     override those mutations.
//   - SYNC dispatch for Before hooks. Return ErrReject (or any error)
//     to short-circuit + propagate to the caller as a 400 (REST
//     handlers wrap our error in CodeValidation).
//   - ASYNC dispatch for After hooks. Fire-and-forget into a goroutine.
//     A panicking handler is recovered + logged; the rest of the chain
//     still runs. The DB write has already committed by the time
//     After-hooks fire.
//
// Thread safety: registration mutates an internal slice under a mutex.
// Dispatch takes a read-lock copy of the slice header so a concurrent
// register doesn't tear under the iterator. Registrations that arrive
// AFTER a dispatch starts won't fire for that dispatch — they take
// effect on the next one. That's the same contract the JS dispatcher
// offers, just with deterministic Go semantics instead of goja's atomic
// pointer swap.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// Action labels the CRUD verb the event represents. Stable strings —
// embedders that inspect ev.Action shouldn't have to import an enum
// type to compare.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// Principal is the minimal authenticated-identity view exposed to Go
// hooks. Mirrors internal/auth/middleware.Principal without an import
// dependency — hooks is a leaf package; REST handlers translate at
// the dispatch site (see Runtime.Dispatch).
//
// nil → unauthenticated request.
type Principal struct {
	UserID         string // uuid.UUID.String(); empty when unauthenticated
	CollectionName string // the auth collection (e.g. "_users", "_superusers")
}

// GoRecordEvent is what Go handlers receive. It mirrors the JS-side
// GoRecordEvent shape but with typed fields — handlers read Record as
// a map[string]any (same as the JS proxy backing), Auth as a Principal,
// Action as one of the three string constants.
//
// Mutations to Record propagate back to the dispatcher caller for Before
// hooks. After hooks may mutate freely (the DB write has happened); the
// mutations are visible to subsequent After handlers but not persisted.
type GoRecordEvent struct {
	Collection string
	Record     map[string]any
	Auth       *Principal // nil → unauthenticated
	Action     string     // ActionCreate / ActionUpdate / ActionDelete
}

// HookContext is what Go handlers receive alongside the event. Keeping
// the context separate (rather than stuffing it onto GoRecordEvent) lets
// the event struct stay pure data — handlers that want to spawn child
// goroutines, set timeouts, or call external APIs reach through Ctx.
type HookContext struct {
	Ctx context.Context
}

// RecordHook is the function signature every Go hook implements. Return
// values:
//
//   - nil           → continue chain (Before hooks proceed; After hooks
//                     are silently fire-and-forget so the return value
//                     is logged but ignored).
//   - ErrReject     → reject the operation. Short-circuits the chain;
//                     subsequent handlers (Go + JS) do NOT fire. REST
//                     handlers translate to 400 with the error message.
//   - other error   → identical to ErrReject for Before hooks (we log +
//                     reject). For After hooks the error is logged and
//                     the chain continues — the DB write has committed
//                     and we can't undo it.
type RecordHook func(*HookContext, *GoRecordEvent) error

// ErrReject is the sentinel handlers return to reject an operation
// without dressing it up as a particular validation error. Callers can
// either return ErrReject directly OR return a custom error — both
// produce a 400 at the REST layer, but custom errors give a clearer
// message in the response body.
//
// Tested via errors.Is so wrapped values still match.
var ErrReject = errors.New("hook rejected the operation")

// GoHooks is the registry + dispatcher for Go-side hooks. One per
// process; the Runtime holds one (lazy-init or via Options).
//
// Zero value is NOT safe — always construct via NewGoHooks so the log
// field is non-nil. (We could fall back to slog.Default(), but
// surfacing the dep at construction time keeps test isolation tight.)
type GoHooks struct {
	mu  sync.RWMutex
	log *slog.Logger

	// handlers is keyed by (Event, collection). Empty collection ("")
	// is the wildcard bucket; per-collection buckets layer on top.
	// Slice order is REGISTRATION order — the dispatch walk preserves
	// it. Append-only; we don't expose a deregister API in v1.2.x
	// because Go hooks come from compile-time wiring and removing one
	// at runtime is unusual + risks subtle ordering bugs.
	handlers map[Event]map[string][]RecordHook
}

// NewGoHooks constructs an empty registry. Caller registers via
// On* methods; FireBefore* / FireAfter* drive the dispatch.
func NewGoHooks() *GoHooks {
	return &GoHooks{
		log:      slog.Default(),
		handlers: map[Event]map[string][]RecordHook{},
	}
}

// SetLogger replaces the slog handle used for warning logs (panicking
// After hooks, non-ErrReject Before errors). Called by the Runtime
// owner so all hooks-package logs flow through one logger.
func (g *GoHooks) SetLogger(log *slog.Logger) {
	if g == nil || log == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.log = log
}

// register adds h to the (event, collection) bucket. Internal — the
// On* methods are the public surface; keeping the loop in one place
// avoids divergent error handling across the six variants.
func (g *GoHooks) register(event Event, collection string, h RecordHook) {
	if g == nil || h == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.handlers[event] == nil {
		g.handlers[event] = map[string][]RecordHook{}
	}
	g.handlers[event][collection] = append(g.handlers[event][collection], h)
}

// OnRecordBeforeCreate registers h to fire on every create attempt for
// the given collection. Pass "" to register a wildcard handler that
// fires for ALL collections.
func (g *GoHooks) OnRecordBeforeCreate(collection string, h RecordHook) {
	g.register(EventRecordBeforeCreate, collection, h)
}

// OnRecordAfterCreate registers h to fire after a successful create.
// Fire-and-forget — see RecordHook docs.
func (g *GoHooks) OnRecordAfterCreate(collection string, h RecordHook) {
	g.register(EventRecordAfterCreate, collection, h)
}

// OnRecordBeforeUpdate registers h to fire on every update attempt.
// "" → wildcard.
func (g *GoHooks) OnRecordBeforeUpdate(collection string, h RecordHook) {
	g.register(EventRecordBeforeUpdate, collection, h)
}

// OnRecordAfterUpdate registers h to fire after a successful update.
// Fire-and-forget.
func (g *GoHooks) OnRecordAfterUpdate(collection string, h RecordHook) {
	g.register(EventRecordAfterUpdate, collection, h)
}

// OnRecordBeforeDelete registers h to fire on every delete attempt.
// "" → wildcard. The event Record map carries `{"id": <id>}` only —
// pre-delete state is not fetched (would cost an extra SELECT). Handlers
// that need pre-delete state should query for it themselves.
func (g *GoHooks) OnRecordBeforeDelete(collection string, h RecordHook) {
	g.register(EventRecordBeforeDelete, collection, h)
}

// OnRecordAfterDelete registers h to fire after a successful delete.
// Fire-and-forget. Event Record carries `{"id": <id>}` only.
func (g *GoHooks) OnRecordAfterDelete(collection string, h RecordHook) {
	g.register(EventRecordAfterDelete, collection, h)
}

// snapshot returns the wildcard + per-collection handler slices for
// (event, collection) under a read-lock. Returned slices are safe to
// iterate without holding the lock — slice headers are copied so a
// concurrent register can't tear the iterator.
//
// Ordering: wildcards FIRST, then per-collection. Each bucket preserves
// registration order. The spec's "wildcard runs BEFORE per-collection"
// is enforced here.
func (g *GoHooks) snapshot(event Event, collection string) []RecordHook {
	g.mu.RLock()
	defer g.mu.RUnlock()
	byColl, ok := g.handlers[event]
	if !ok {
		return nil
	}
	wild := byColl[""]
	specific := byColl[collection]
	if collection == "" {
		// A FireXxx call without a collection (unusual but possible)
		// fires wildcards only. Avoid double-counting "" handlers.
		out := make([]RecordHook, len(wild))
		copy(out, wild)
		return out
	}
	out := make([]RecordHook, 0, len(wild)+len(specific))
	out = append(out, wild...)
	out = append(out, specific...)
	return out
}

// HasHandlers reports whether ANY handler (wildcard or per-collection)
// is registered for (event, collection). The Runtime uses this to skip
// event-object allocation when nothing's wired.
func (g *GoHooks) HasHandlers(event Event, collection string) bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	byColl, ok := g.handlers[event]
	if !ok {
		return false
	}
	if len(byColl[""]) > 0 {
		return true
	}
	return len(byColl[collection]) > 0
}

// fireBefore is the synchronous dispatch path used by all three
// FireBefore* methods. Walks handlers in (wildcard, per-collection) ×
// (registration) order. The first error short-circuits + propagates.
// ErrReject and other errors are equivalent at the protocol level
// (both produce a 400 at the REST layer), but ErrReject is the
// documented sentinel for "intentional rejection without a specific
// message".
//
// On non-ErrReject errors we ALSO log at warn level so operators
// debugging a misbehaving handler see something in the journal even if
// the response body is opaque.
func (g *GoHooks) fireBefore(ctx context.Context, event Event, ev *GoRecordEvent) error {
	if g == nil {
		return nil
	}
	handlers := g.snapshot(event, ev.Collection)
	if len(handlers) == 0 {
		return nil
	}
	hctx := &HookContext{Ctx: ctx}
	for _, h := range handlers {
		err := g.runOneBefore(hctx, ev, h, event)
		if err != nil {
			return err
		}
	}
	return nil
}

// runOneBefore invokes one Before handler with panic-recovery. A
// panicking handler shouldn't take down the request — we recover,
// log, and translate to a generic error. ErrReject is returned
// verbatim so callers can errors.Is(err, ErrReject) to differentiate
// intentional rejection from a bug.
func (g *GoHooks) runOneBefore(hctx *HookContext, ev *GoRecordEvent, h RecordHook, event Event) (returnedErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			g.log.Warn("go hook: before-handler panicked",
				"event", event, "collection", ev.Collection,
				"panic", rec)
			returnedErr = fmt.Errorf("hook panicked: %v", rec)
		}
	}()
	if err := h(hctx, ev); err != nil {
		if !errors.Is(err, ErrReject) {
			g.log.Warn("go hook: before-handler rejected",
				"event", event, "collection", ev.Collection, "err", err)
		}
		return err
	}
	return nil
}

// fireAfter is the asynchronous dispatch path. Each handler runs in
// its own goroutine — the spec says "fire-and-forget"; we honour that
// by NOT awaiting completion. A panicking handler is recovered + logged
// (panics in user goroutines crash the process by default, which would
// take a perfectly valid HTTP response down with it).
//
// Why per-handler goroutines instead of one goroutine running all
// handlers: a slow handler shouldn't delay subsequent After handlers
// (they're already detached from the request lifecycle; we have nothing
// to gain by serialising them). The fan-out is bounded by len(handlers),
// which embedders control at registration time.
//
// We snapshot the slice under the lock + iterate without holding it, so
// concurrent registrations don't tear the walk.
func (g *GoHooks) fireAfter(ctx context.Context, event Event, ev *GoRecordEvent) {
	if g == nil {
		return
	}
	handlers := g.snapshot(event, ev.Collection)
	if len(handlers) == 0 {
		return
	}
	hctx := &HookContext{Ctx: ctx}
	for _, h := range handlers {
		h := h // capture per iteration
		go g.runOneAfter(hctx, ev, h, event)
	}
}

// runOneAfter invokes one After handler with panic-recovery. Errors
// (including ErrReject — meaningless for an After hook but valid Go)
// are logged at warn level. The DB write has already committed; we
// can't undo it from here.
func (g *GoHooks) runOneAfter(hctx *HookContext, ev *GoRecordEvent, h RecordHook, event Event) {
	defer func() {
		if rec := recover(); rec != nil {
			g.log.Warn("go hook: after-handler panicked (recovered)",
				"event", event, "collection", ev.Collection,
				"panic", rec)
		}
	}()
	if err := h(hctx, ev); err != nil {
		g.log.Warn("go hook: after-handler returned error (ignored)",
			"event", event, "collection", ev.Collection, "err", err)
	}
}

// FireBeforeCreate synchronously dispatches every BeforeCreate handler
// registered for ev.Collection (wildcards first, then per-collection),
// in registration order. The first error short-circuits + is returned.
// ErrReject is the recommended sentinel for "reject"; any other error
// produces an equivalent 400 at the REST layer plus a warn log.
//
// Mutations to ev.Record propagate back to the caller — REST handlers
// re-read ev.Record after Fire returns and feed it into the INSERT.
func (g *GoHooks) FireBeforeCreate(ctx context.Context, ev *GoRecordEvent) error {
	if ev != nil {
		ev.Action = ActionCreate
	}
	return g.fireBefore(ctx, EventRecordBeforeCreate, ev)
}

// FireAfterCreate asynchronously dispatches every AfterCreate handler.
// Returns immediately; handlers run in detached goroutines (panic-safe).
func (g *GoHooks) FireAfterCreate(ctx context.Context, ev *GoRecordEvent) {
	if ev != nil {
		ev.Action = ActionCreate
	}
	g.fireAfter(ctx, EventRecordAfterCreate, ev)
}

// FireBeforeUpdate — see FireBeforeCreate. Mutations to ev.Record
// propagate back to the REST handler's UPDATE statement.
func (g *GoHooks) FireBeforeUpdate(ctx context.Context, ev *GoRecordEvent) error {
	if ev != nil {
		ev.Action = ActionUpdate
	}
	return g.fireBefore(ctx, EventRecordBeforeUpdate, ev)
}

// FireAfterUpdate — see FireAfterCreate.
func (g *GoHooks) FireAfterUpdate(ctx context.Context, ev *GoRecordEvent) {
	if ev != nil {
		ev.Action = ActionUpdate
	}
	g.fireAfter(ctx, EventRecordAfterUpdate, ev)
}

// FireBeforeDelete — see FireBeforeCreate. ev.Record typically carries
// `{"id": <id>}` only (REST handlers don't fetch pre-delete state).
func (g *GoHooks) FireBeforeDelete(ctx context.Context, ev *GoRecordEvent) error {
	if ev != nil {
		ev.Action = ActionDelete
	}
	return g.fireBefore(ctx, EventRecordBeforeDelete, ev)
}

// FireAfterDelete — see FireAfterCreate.
func (g *GoHooks) FireAfterDelete(ctx context.Context, ev *GoRecordEvent) {
	if ev != nil {
		ev.Action = ActionDelete
	}
	g.fireAfter(ctx, EventRecordAfterDelete, ev)
}
