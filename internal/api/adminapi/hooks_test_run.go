package adminapi

// v1.7.20 §3.4.11 — Admin UI hook test panel.
//
// One route:
//
//	POST /api/_admin/hooks/test-run     fire a hook handler against a
//	                                    synthetic record + capture output
//
// Wire-shape (request):
//
//	{
//	  "event":      "BeforeCreate" | "AfterCreate" | "BeforeUpdate" |
//	                "AfterUpdate"  | "BeforeDelete" | "AfterDelete",
//	  "collection": "posts" | "",         // "" → wildcard handlers ("*")
//	  "record":     {"title": "x", ...},  // synthetic record JSON
//	  "principal":  {"id": "...uuid...", "collection": "users"} // optional
//	}
//
// Response:
//
//	{
//	  "outcome":         "ok" | "rejected" | "error",
//	  "console":         ["...", ...],         // captured console.log/error
//	  "modified_record": {"title": "x", ...},  // post-hook record state
//	  "duration_ms":     12,
//	  "error":           "..."                 // when outcome != ok
//	}
//
// Outcome semantics:
//
//   - `ok`        — handler ran to completion. modified_record reflects
//                   any Before-hook mutations.
//   - `rejected`  — handler threw (or `e.next()` aborted via throw).
//                   `error` carries the JS-side message.
//   - `error`     — runtime error: watchdog killed the loop, load
//                   failure, internal panic, etc. `error` carries the
//                   Go-side detail.
//
// Design call: NO DB side effects.
//
// We build a fresh isolated `hooks.Runtime` per test-run request and
// load the operator's `pb_hooks/*.js` against it. That runtime is
// constructed via the same `hooks.NewRuntime` path the production
// runtime uses, but Options.Bus is nil (so `$app.realtime().publish()`
// silently no-ops — same contract as the testapp.MockHookRuntime
// harness in pkg/railbase/testapp/hookmock.go) and there's no `$app.dao`
// binding to begin with (it lands in v1.2.x; the v1.2.0 loader doesn't
// install it). If a hook calls a future `$app.dao.save()` it will throw
// `TypeError: $app.dao is undefined` — captured as console.error and
// surfaced as outcome=rejected, which is exactly the operator feedback
// we want: "your handler tried to write, the test panel said no".
//
// Console capture: the runtime's `console.log` / `console.error` route
// through Options.Log. We plug in a slog handler that appends one line
// per Record to a buffer, so every print made by the operator's hook
// (and by the loader for load-failure warnings) is captured into the
// response payload.
//
// Watchdog: 5s ceiling. We rely on the in-runtime watchdog already wired
// in internal/hooks (DefaultTimeout = 5s; we pass the same value
// explicitly so the failure mode is predictable across reloader bumps).
// On timeout the runtime returns a `*HandlerError` whose Message
// mentions "timeout after 5s" — we promote that to outcome=error with
// duration_ms < 6000 (it can briefly overshoot the 5s by the time the
// `Interrupt` propagates through goja's bytecode dispatcher; we'd
// rather report the true wall-clock than synthesize a cleaner number).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/audit"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/hooks"
)

// hookTestRunTimeout caps the runtime's per-handler watchdog AND the
// outer dispatch context. Two seconds is plenty for legitimate hook
// logic; we'd rather a "hung loop" test surface fast than make the
// operator wait 5 s on every accidental `while(true){}`.
//
// 5 s is the spec ceiling — the in-runtime watchdog defaults to that —
// but the loader-side script execution can briefly overshoot because
// goja's Interrupt only fires at the next bytecode boundary. We bound
// the OUTER deadline at 6 s so the handler can still report a
// duration_ms reading on the timeout case.
const (
	hookTestRunHandlerTimeout = 2 * time.Second
	hookTestRunOuterCeiling   = 6 * time.Second
)

// hookTestRunRequest is the wire-decoded request body. We use lowercase-
// snake_case for the principal sub-object to match the rest of the
// admin API; the top-level keys (`event`, `collection`, `record`,
// `principal`) follow the spec literally.
type hookTestRunRequest struct {
	Event      string                  `json:"event"`
	Collection string                  `json:"collection"`
	Record     map[string]any          `json:"record"`
	Principal  *hookTestRunPrincipalIn `json:"principal,omitempty"`
}

// hookTestRunPrincipalIn is decoded but not (yet) wired into the
// dispatch path — v1.2.0 hooks don't have an `e.auth` binding. We
// accept the field for forward compatibility; the audit event records
// the (id, collection) pair so the operator can correlate test-runs
// across users.
type hookTestRunPrincipalIn struct {
	ID         string `json:"id"`
	Collection string `json:"collection"`
}

// hookTestRunResponse is the wire-encoded response body. `Console` is
// always an array — even for the empty case — so the UI doesn't need
// to null-check. `ModifiedRecord` carries the post-hook state for
// Before-hooks; After-hooks return whatever the handler left in
// `e.record` (typically the same as the input).
type hookTestRunResponse struct {
	Outcome        string         `json:"outcome"`
	Console        []string       `json:"console"`
	ModifiedRecord map[string]any `json:"modified_record"`
	DurationMS     int64          `json:"duration_ms"`
	Error          string         `json:"error,omitempty"`
}

// hookTestRunEvents maps the wire-shape event name to the canonical
// hooks.Event identifier. Listed exhaustively so a typo in the request
// body surfaces as a clear 400 rather than a silent miss.
var hookTestRunEvents = map[string]hooks.Event{
	"BeforeCreate": hooks.EventRecordBeforeCreate,
	"AfterCreate":  hooks.EventRecordAfterCreate,
	"BeforeUpdate": hooks.EventRecordBeforeUpdate,
	"AfterUpdate":  hooks.EventRecordAfterUpdate,
	"BeforeDelete": hooks.EventRecordBeforeDelete,
	"AfterDelete":  hooks.EventRecordAfterDelete,
}

// mountHooksTestRun registers POST /hooks/test-run on r. Always
// registered: like the sibling hooks-files surface, the handler itself
// 503s when HooksDir is empty so the UI can render a typed "not
// configured" hint without a missing-route 404.
func (d *Deps) mountHooksTestRun(r chi.Router) {
	r.Post("/hooks/test-run", d.hooksTestRunHandler)
}

// hooksTestRunHandler — POST /api/_admin/hooks/test-run.
//
// Pipeline:
//  1. Decode body + validate event name.
//  2. 503 when HooksDir is empty.
//  3. Build a captured-console slog handler + fresh hooks.Runtime
//     pointed at HooksDir (same Options as production, but with our
//     log + nil Bus).
//  4. Load all .js files; any load failure is logged into the capture
//     buffer (the loader already routes load warnings through r.log)
//     but doesn't fail the request — the operator might be testing a
//     file that's known to fail at load.
//  5. Dispatch the requested event against a deep-copy of the input
//     record (so the request body's map stays untouched for the audit
//     write).
//  6. Compose the response. modified_record is the dispatched event's
//     final record state.
//  7. Audit `hooks.test_run` with outcome + event name.
func (d *Deps) hooksTestRunHandler(w http.ResponseWriter, r *http.Request) {
	if d.HooksDir == "" {
		writeHooksUnavailable(w)
		return
	}

	var req hookTestRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	event, ok := hookTestRunEvents[req.Event]
	if !ok {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"event must be one of BeforeCreate/AfterCreate/BeforeUpdate/AfterUpdate/BeforeDelete/AfterDelete (got %q)",
			req.Event))
		return
	}
	if req.Record == nil {
		req.Record = map[string]any{}
	}

	// Console capture buffer. The slog handler appends one line per
	// Record; we drain it after Dispatch into the response payload.
	cap := newHookConsoleCapture()
	captureLog := slog.New(cap)

	rt, err := hooks.NewRuntime(hooks.Options{
		HooksDir: d.HooksDir,
		Timeout:  hookTestRunHandlerTimeout,
		Log:      captureLog,
		Bus:      nil, // realtime publish is a silent no-op in the test panel
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "build hooks runtime"))
		return
	}
	if rt == nil {
		// HooksDir empty after the guard above shouldn't happen — defensive.
		writeHooksUnavailable(w)
		return
	}

	loadCtx, cancelLoad := context.WithTimeout(r.Context(), hookTestRunOuterCeiling)
	defer cancelLoad()
	if err := rt.Load(loadCtx); err != nil {
		// Load failures are surfaced as outcome=error rather than 500:
		// a syntax error in one .js file is operator-visible feedback,
		// not an internal server fault.
		resp := hookTestRunResponse{
			Outcome:        "error",
			Console:        cap.drain(),
			ModifiedRecord: cloneRecord(req.Record),
			DurationMS:     0,
			Error:          err.Error(),
		}
		d.writeHookTestRunAudit(r, req, resp.Outcome, resp.Error)
		writeHookTestRunJSON(w, resp)
		return
	}

	dispatchCtx, cancelDispatch := context.WithTimeout(r.Context(), hookTestRunOuterCeiling)
	defer cancelDispatch()

	// Dispatch against a deep-copy of the input. The runtime mutates
	// the map in-place; we want the audit record to stamp the BEFORE
	// state if that's ever useful, and we want the response's
	// modified_record to be unambiguously the AFTER state.
	working := cloneRecord(req.Record)

	collection := req.Collection // "" → wildcard handlers won't match
	// per-collection, but the loader registers "*"-keyed handlers and
	// HasHandlers also checks "*", so passing "" forwards through to
	// just the wildcard set — exactly the desired behaviour for
	// "fire any wildcard handler against this synthetic event".

	start := time.Now()
	ev, dispatchErr := rt.Dispatch(dispatchCtx, collection, event, working)
	duration := time.Since(start)

	resp := hookTestRunResponse{
		Console:    cap.drain(),
		DurationMS: duration.Milliseconds(),
	}
	if ev != nil {
		resp.ModifiedRecord = ev.Record()
	} else {
		// Dispatch never returns a nil event in v1.2.0, but the
		// signature allows it; clone the working copy to keep the
		// response stable.
		resp.ModifiedRecord = working
	}

	switch {
	case dispatchErr == nil:
		resp.Outcome = "ok"
	case isHookTestRunTimeout(dispatchErr):
		resp.Outcome = "error"
		resp.Error = "watchdog killed: " + dispatchErr.Error()
	default:
		// A *HandlerError or a wrapped goja exception both surface here.
		// We classify everything non-timeout as "rejected" — the handler
		// threw, the request would be 400'd in production.
		resp.Outcome = "rejected"
		var he *hooks.HandlerError
		if errors.As(dispatchErr, &he) {
			resp.Error = he.Message
		} else {
			resp.Error = dispatchErr.Error()
		}
	}

	d.writeHookTestRunAudit(r, req, resp.Outcome, resp.Error)
	writeHookTestRunJSON(w, resp)
}

// writeHookTestRunJSON emits the response envelope. We always set
// Content-Type and 200 — even when outcome=error/rejected — because
// the request itself succeeded; the handler outcome is in-band.
func writeHookTestRunJSON(w http.ResponseWriter, resp hookTestRunResponse) {
	// Belt-and-braces: a nil Console must serialise to [], not null.
	if resp.Console == nil {
		resp.Console = []string{}
	}
	if resp.ModifiedRecord == nil {
		resp.ModifiedRecord = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// writeHookTestRunAudit records the test-run action. Mirrors the
// cache.cleared pattern: nil-guarded so test Deps without an Audit
// writer stay functional.
func (d *Deps) writeHookTestRunAudit(r *http.Request, req hookTestRunRequest, outcome, errMsg string) {
	if d == nil || d.Audit == nil {
		return
	}
	p := AdminPrincipalFrom(r.Context())
	before := map[string]any{
		"event":      req.Event,
		"collection": req.Collection,
		"outcome":    outcome,
	}
	if req.Principal != nil {
		before["principal_id"] = req.Principal.ID
		before["principal_collection"] = req.Principal.Collection
	}
	var au audit.Outcome
	switch outcome {
	case "ok":
		au = audit.OutcomeSuccess
	case "rejected":
		au = audit.OutcomeDenied
	default:
		au = audit.OutcomeError
	}
	_, _ = d.Audit.Write(r.Context(), audit.Event{
		UserID:         p.AdminID,
		UserCollection: "_admins",
		Event:          "hooks.test_run",
		Outcome:        au,
		Before:         before,
		ErrorCode:      errMsg,
		IP:             clientIP(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
}

// cloneRecord returns a shallow copy of m. The dispatcher swaps map
// entries in-place; cloning here keeps the request body's map
// reference clean for audit + response composition.
//
// We intentionally don't deep-clone nested objects: hook handlers
// mutating a nested slice/map will still mutate the working copy, but
// the top-level identity is distinct from the request body, which is
// the only invariant we need for the audit trail. The cost of a full
// deep clone (JSON round-trip) isn't worth it for a debug-flow endpoint.
func cloneRecord(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// isHookTestRunTimeout reports whether err is the runtime watchdog's
// timeout. The hooks package wraps it as a *HandlerError with a Message
// containing "timeout after"; we substring-match because the timeout
// message format isn't a public constant.
func isHookTestRunTimeout(err error) bool {
	var he *hooks.HandlerError
	if errors.As(err, &he) {
		return strings.Contains(he.Message, "timeout after")
	}
	return strings.Contains(err.Error(), "timeout after")
}

// hookConsoleCapture is a slog.Handler that appends each record's
// message + args to a string buffer. The hooks runtime routes console.log
// / console.error through r.log.Info / r.log.Error, both of which call
// Handler.Handle with a Record. We don't care about level here — every
// captured line is rendered into the JSON response as-is.
//
// Concurrency: goja runs hooks on the caller's goroutine, but the
// runtime's slog logger is shared across the dispatch path (load
// warnings can land on a different goroutine if the loader ever grows
// concurrent file parsing). A sync.Mutex keeps lines well-ordered.
type hookConsoleCapture struct {
	mu    sync.Mutex
	lines []string
}

func newHookConsoleCapture() *hookConsoleCapture {
	return &hookConsoleCapture{lines: make([]string, 0, 16)}
}

// Enabled implements slog.Handler. The hooks runtime writes at Info /
// Error / Warn; we accept all of them. (Debug is reserved for
// internal-only logger calls that aren't user-visible console output.)
func (h *hookConsoleCapture) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= slog.LevelInfo
}

// Handle implements slog.Handler. Formats the record as "<message>
// <key=value>..." into the line buffer. The hooks runtime's console.log
// always emits a "hook console" message with an `args` slog.Attr
// carrying the user's varargs; we render those specially so the line
// looks like the operator's `console.log("created", e.record.id)` call
// instead of `level=INFO msg="hook console" args=[created abc-...]`.
func (h *hookConsoleCapture) Handle(_ context.Context, rec slog.Record) error {
	var line strings.Builder
	// Detect the "hook console" sentinel + pull the `args` slice. This
	// is a tight coupling to the loader's logging shape, but it's
	// stable: changing the message there would require a test update
	// here, which is the right kind of pressure for keeping the contract
	// visible.
	if rec.Message == "hook console" {
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "args" {
				if arr, ok := a.Value.Any().([]any); ok {
					parts := make([]string, len(arr))
					for i, v := range arr {
						parts[i] = renderConsoleArg(v)
					}
					line.WriteString(strings.Join(parts, " "))
					return false
				}
			}
			return true
		})
	} else {
		// Loader warnings / other internal log lines: render plainly.
		line.WriteString("[")
		line.WriteString(rec.Level.String())
		line.WriteString("] ")
		line.WriteString(rec.Message)
		rec.Attrs(func(a slog.Attr) bool {
			line.WriteString(" ")
			line.WriteString(a.Key)
			line.WriteString("=")
			line.WriteString(renderConsoleArg(a.Value.Any()))
			return true
		})
	}
	h.mu.Lock()
	h.lines = append(h.lines, line.String())
	h.mu.Unlock()
	return nil
}

// WithAttrs / WithGroup are no-ops for our purposes — the runtime
// doesn't call them on the test-run path. Returning the same handler
// is the canonical noop pattern (mirrors slog.DiscardHandler).
func (h *hookConsoleCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *hookConsoleCapture) WithGroup(_ string) slog.Handler      { return h }

// drain returns + clears the captured lines. Safe to call concurrently
// with Handle, though in practice the dispatcher has returned by the
// time we call this.
func (h *hookConsoleCapture) drain() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.lines))
	copy(out, h.lines)
	h.lines = h.lines[:0]
	return out
}

// renderConsoleArg formats one console argument. Strings pass through
// unquoted; everything else gets fmt.Sprint'd. We use fmt.Sprint rather
// than fmt.Sprintf("%v",...) so map / slice rendering matches Go's
// default — operators will recognise the output shape.
func renderConsoleArg(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	// For maps / nested objects, marshal to JSON when feasible so the
	// console output is readable JS-shape (e.g. `{"title":"x"}` rather
	// than Go's `map[title:x]`).
	switch v.(type) {
	case map[string]any, []any:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(v)
}

// Compile-time check: hookConsoleCapture must satisfy slog.Handler.
var _ slog.Handler = (*hookConsoleCapture)(nil)
