// Package hooks re-exports the Go-side hook types from
// internal/hooks so userland projects can write handler functions
// without importing internal/* (which Go forbids across module
// boundaries).
//
// v0.4.1 — added to close Sentinel FEEDBACK.md #2:
//
//	// before:
//	app.GoHooks().OnRecordBeforeCreate("tasks", func(...) error { ... })
//	                                            ^^^ types were in internal —
//	                                            handler couldn't even be written
//
//	// after:
//	import "github.com/railbase/railbase/pkg/railbase/hooks"
//	app.GoHooks().OnRecordBeforeCreate("tasks",
//	    func(c *hooks.Context, ev *hooks.RecordEvent) error {
//	        if ev.Record["project"] == "" {
//	            return hooks.ErrReject
//	        }
//	        return nil
//	    })
//
// All re-exports are Go type aliases — they share identity with the
// internal types, so `*GoHooks.OnRecordBeforeCreate(coll, hook)`
// accepts a hook declared against the public alias unchanged.
package hooks

import (
	"github.com/railbase/railbase/internal/hooks"
)

// RecordEvent mirrors internal/hooks.GoRecordEvent. The rename to
// drop the `Go` prefix is intentional — userland already imports
// from `railbase/hooks`, the prefix would be redundant.
type RecordEvent = hooks.GoRecordEvent

// Context is the per-call context handlers receive. Use `c.Ctx` for
// cancellation / timeouts / outbound calls that need request scope.
type Context = hooks.HookContext

// RecordHook is the function signature every record handler matches.
// Returns nil to continue, ErrReject to refuse the operation, or any
// other error to refuse + surface the message at the REST layer.
type RecordHook = hooks.RecordHook

// Principal is the optional auth principal attached to a record
// event. Nil on anonymous requests.
type Principal = hooks.Principal

// Event names — string constants mirroring the dispatcher's events.
// Useful for hooks that want to switch on event type instead of
// registering separate handlers per before/after pair.
const (
	EventRecordBeforeCreate = hooks.EventRecordBeforeCreate
	EventRecordAfterCreate  = hooks.EventRecordAfterCreate
	EventRecordBeforeUpdate = hooks.EventRecordBeforeUpdate
	EventRecordAfterUpdate  = hooks.EventRecordAfterUpdate
	EventRecordBeforeDelete = hooks.EventRecordBeforeDelete
	EventRecordAfterDelete  = hooks.EventRecordAfterDelete
)

// Action constants matching RecordEvent.Action.
const (
	ActionCreate = hooks.ActionCreate
	ActionUpdate = hooks.ActionUpdate
	ActionDelete = hooks.ActionDelete
)

// ErrReject is the canonical "refuse this operation" sentinel.
// Returning it from a Before hook produces a 400 at the REST layer.
// Use a wrapped fmt.Errorf for a custom user-visible message.
var ErrReject = hooks.ErrReject

// ErrHookDepthExceeded is returned by Dispatch when the recursive
// hook chain exceeds MaxHookDepth (default 5). Surfaces a 500 with
// the offending chain at the REST layer.
var ErrHookDepthExceeded = hooks.ErrHookDepthExceeded

// Registry is the GoHooks registry handle returned by
// `App.GoHooks()`. Re-exported so userland code can declare local
// variables of this type (e.g. when refactoring hook-registration
// into a helper function that takes `*hooks.Registry`).
//
// Go's "you can call exported methods on unexported types" rule
// means hook registration works WITHOUT this alias too — Sentinel
// can write `app.GoHooks().OnRecordBeforeCreate(...)` and the
// compiler is happy. The alias matters only when you want to pass
// the registry around as a typed argument:
//
//	func wireHooks(r *hooks.Registry) { r.OnRecordBeforeCreate(...) }
type Registry = hooks.GoHooks
