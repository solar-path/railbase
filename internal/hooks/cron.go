package hooks

// v1.7.18 — `$app.cronAdd(name, expression, handler)` JS hook binding.
// Closes docs/17 #61. Sibling to v1.7.17's routerAdd: lets hook authors
// register scheduled background tasks from JavaScript.
//
// Architecture:
//
//   1. During Load(), each $app.cronAdd(...) call appends to a per-Load
//      cronSet. Same-named jobs within the load overwrite each other
//      (later wins) so an operator can re-register without leaks.
//   2. After all .js files run, cronSet.finalize() builds the snapshot
//      that the runtime swaps into r.crons atomically.
//   3. app.go calls Runtime.StartCronLoop(ctx) once at boot. The loop
//      ticks every minute (aligned to wall-clock minute boundaries),
//      reads the current snapshot, and fires every handler whose
//      schedule matches the tick. Stop() cancels the goroutine.
//   4. Each handler runs under the same per-handler watchdog as
//      record-event hooks (DefaultTimeout = 5s). Throws are logged but
//      DO NOT abort the loop — a flaky job mustn't take down peers.
//
// Why in-memory instead of persisted to `_cron`:
//
//   - Hot-reload semantics are cleanest: deleting a .js file drops its
//     crons on the next reload, no orphan _cron rows operators have to
//     clean up.
//   - Hook crons are inherently per-process (the VM lives in this
//     process). Cross-replica coordination is the v1.4.0 _jobs queue's
//     job, not this binding's.
//   - Operator-managed crons (via CLI / settings) keep their existing
//     `_cron` table; the two systems coexist without overlap.
//
// JS API:
//
//   $app.cronAdd("nightly-cleanup", "0 4 * * *", () => {
//       console.log("running nightly cleanup");
//       // ... custom logic ...
//   });

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"github.com/railbase/railbase/internal/jobs"
)

// cronSet collects $app.cronAdd registrations during one Load() and
// produces the immutable snapshot the cron loop reads.
type cronSet struct {
	mu      sync.Mutex
	entries map[string]*cronEntry // keyed by name; later registration wins
	order   []string              // insertion order (for deterministic iteration)
}

// cronEntry is one registered job. fn binds to the VM that ran the
// registration; serveTick switches back to that VM at dispatch.
type cronEntry struct {
	name     string
	expr     string
	schedule *jobs.Schedule
	fn       goja.Callable
	vm       *goja.Runtime
	// lastFired guards against double-firing the same minute (e.g. if
	// the loop's tick alignment drifts by < 1s and we re-enter a
	// matched minute). Set after a successful dispatch.
	lastFired time.Time
}

func newCronSet() *cronSet {
	return &cronSet{entries: map[string]*cronEntry{}}
}

// register validates the JS call arguments and inserts (or replaces)
// the named entry. Returns a structured error the caller surfaces as
// a JS thrown Error.
func (cs *cronSet) register(vm *goja.Runtime, call goja.FunctionCall) error {
	if len(call.Arguments) < 3 {
		return fmt.Errorf("expected (name, expression, handler), got %d argument(s)", len(call.Arguments))
	}
	name := strings.TrimSpace(call.Arguments[0].String())
	if name == "" {
		return fmt.Errorf("name must be a non-empty string")
	}
	expr := strings.TrimSpace(call.Arguments[1].String())
	if expr == "" {
		return fmt.Errorf("expression must be a non-empty string (5-field cron)")
	}
	schedule, err := jobs.ParseCron(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	fn, ok := goja.AssertFunction(call.Arguments[2])
	if !ok {
		return fmt.Errorf("handler must be a function")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, dup := cs.entries[name]; !dup {
		cs.order = append(cs.order, name)
	}
	cs.entries[name] = &cronEntry{
		name:     name,
		expr:     expr,
		schedule: schedule,
		fn:       fn,
		vm:       vm,
	}
	return nil
}

// finalize produces the immutable snapshot the runtime swaps into
// r.crons. Returns nil for an empty set so the loop short-circuits.
// The returned *cronSet is read-only after this call — no further
// registrations should land on it.
func (cs *cronSet) finalize(_ *Runtime) *cronSet {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.entries) == 0 {
		return nil
	}
	return cs
}

// each iterates entries in insertion order (deterministic dispatch
// when multiple crons match the same minute).
func (cs *cronSet) each(fn func(*cronEntry)) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for _, name := range cs.order {
		if e, ok := cs.entries[name]; ok {
			fn(e)
		}
	}
}

// StartCronLoop kicks off the background goroutine that fires
// registered $app.cronAdd jobs on minute boundaries. Caller MUST defer
// Stop() on the runtime to shut it down cleanly. Safe to call on a nil
// Runtime (no-op) and idempotent — a second call cancels the first
// loop and starts a fresh one. ctx cancellation also stops the loop.
//
// Tick alignment: we sleep until the next wall-clock minute boundary
// before the first tick. This matches the docs/10 §1 contract that
// cron expressions fire on minute-precision wall-clock times.
//
// Drift handling: when the system suspends/resumes (laptop sleep), the
// first wake-up tick will fire every schedule that matched a minute
// during the sleep IF the minute matches the current wall-clock minute.
// We don't try to "catch up" missed firings — that's the v1.4.0 jobs
// queue's job (operator stores work there if catch-up matters).
func (r *Runtime) StartCronLoop(ctx context.Context) {
	if r == nil {
		return
	}
	cronCtx, cancel := context.WithCancel(ctx)
	go r.cronLoop(cronCtx)
	r.stops = append(r.stops, cancel)
}

// cronLoop is the long-running goroutine. Tick once per minute aligned
// to the wall-clock boundary; on each tick, iterate the current
// snapshot, fire matching handlers, advance lastFired bookkeeping.
func (r *Runtime) cronLoop(ctx context.Context) {
	for {
		// Sleep until the next minute boundary (+1ms padding so we
		// don't race time.Now() against the boundary).
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute).Add(time.Millisecond)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}

		snap := r.crons.Load()
		if snap == nil {
			continue
		}
		tickMinute := time.Now().Truncate(time.Minute)
		snap.each(func(e *cronEntry) {
			if !e.schedule.Matches(tickMinute) {
				return
			}
			// Dedup re-entry on the exact same minute (loop drift).
			if e.lastFired.Equal(tickMinute) {
				return
			}
			e.lastFired = tickMinute
			r.fireCron(ctx, e)
		})
	}
}

// fireCron invokes one cron handler under the per-handler watchdog.
// Mirror of the on*-event dispatcher's runOne — same VM lock, same
// timeout pattern, same recover-on-panic story.
func (r *Runtime) fireCron(_ context.Context, e *cronEntry) {
	r.vmMu.Lock()
	defer r.vmMu.Unlock()
	vm := e.vm

	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-time.After(r.timeout):
			vm.Interrupt(fmt.Errorf("$app.cronAdd %q: timeout after %s", e.name, r.timeout))
		case <-doneCh:
		}
	}()
	vm.ClearInterrupt()

	var thrown error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				thrown = fmt.Errorf("hook panicked: %v", rec)
			}
		}()
		// Pass an empty `e` object — cron handlers don't have a
		// request to bridge. Future v1.x might attach a `lastFiredAt`
		// or `nextFireAt` field; v1.7.18 keeps the surface tight so
		// operators don't pin shape early.
		evObj := vm.NewObject()
		_ = evObj.Set("name", e.name)
		_ = evObj.Set("expression", e.expr)
		if _, err := e.fn(goja.Undefined(), evObj); err != nil {
			thrown = err
		}
	}()

	if thrown != nil {
		// Log + continue — one bad job mustn't take down the loop.
		var jsErr *goja.Exception
		msg := thrown.Error()
		if errors.As(thrown, &jsErr) {
			msg = jsErr.Value().String()
		}
		r.log.Warn("hook: cronAdd handler threw",
			"name", e.name, "expression", e.expr, "err", msg)
		return
	}
	r.log.Info("hook: cronAdd handler fired", "name", e.name, "expression", e.expr)
}
