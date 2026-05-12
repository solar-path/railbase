package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/fsnotify/fsnotify"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/realtime"
)

// Load reads every *.js file under HooksDir, parses them, executes
// each in a fresh goja VM with the `appBinding` API installed, and
// atomically swaps the resulting Registry into place.
//
// Idempotent: calling twice replaces the registry. Errors in one
// file don't poison the rest — we log + skip and continue. (A
// completely broken file shouldn't take the whole hooks dir offline.)
func (r *Runtime) Load(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if _, err := os.Stat(r.hooksDir); errors.Is(err, os.ErrNotExist) {
		// Hooks dir doesn't exist yet — load an empty registry.
		// Operator can mkdir + drop files later and a watcher reload
		// will pick them up. NOT an error: greenfield deployments
		// shouldn't have to mkdir hooks/ to boot.
		empty := &Registry{handlers: map[string]map[Event][]*registeredHandler{}}
		r.registry.Store(empty)
		return nil
	}

	files, err := r.discover()
	if err != nil {
		return err
	}

	r.vmMu.Lock()
	defer r.vmMu.Unlock()

	// Build a fresh registry. We use a single VM per Load() — one VM
	// owns all parsed Callables, and the dispatcher uses the same VM
	// per handler. Pool-of-VMs is a v1.2.x optimisation.
	vm := applyStackCap(goja.New(), r.maxCallStackSize)
	reg := &Registry{handlers: map[string]map[Event][]*registeredHandler{}}

	// Install appBinding surface. It's a JS object whose methods are
	// goja-callable Go closures. Each onRecord* method returns a
	// builder object with a bindFunc method — the PB-compatible
	// "hook handle" shape.
	appBinding := vm.NewObject()
	for _, ev := range []struct {
		name  string
		event Event
	}{
		{"onRecordBeforeCreate", EventRecordBeforeCreate},
		{"onRecordAfterCreate", EventRecordAfterCreate},
		{"onRecordBeforeUpdate", EventRecordBeforeUpdate},
		{"onRecordAfterUpdate", EventRecordAfterUpdate},
		{"onRecordBeforeDelete", EventRecordBeforeDelete},
		{"onRecordAfterDelete", EventRecordAfterDelete},
	} {
		ev := ev // capture
		_ = appBinding.Set(ev.name, func(call goja.FunctionCall) goja.Value {
			coll := "*"
			if len(call.Arguments) > 0 {
				coll = call.Arguments[0].String()
			}
			builder := vm.NewObject()
			_ = builder.Set("bindFunc", func(bcall goja.FunctionCall) goja.Value {
				if len(bcall.Arguments) == 0 {
					return goja.Undefined()
				}
				fn, ok := goja.AssertFunction(bcall.Arguments[0])
				if !ok {
					return goja.Undefined()
				}
				if _, ok := reg.handlers[coll]; !ok {
					reg.handlers[coll] = map[Event][]*registeredHandler{}
				}
				reg.handlers[coll][ev.event] = append(reg.handlers[coll][ev.event],
					&registeredHandler{
						fn:       fn,
						source:   "(current load)",
						loadedVM: vm,
					})
				return goja.Undefined()
			})
			return builder
		})
	}
	// $app.realtime() — returns a small JS object exposing
	// `.publish(event)`. Used by hook authors to emit custom realtime
	// events (e.g. "after a derived table refresh, ping connected
	// clients to reload their dashboard"). Wired against r.bus; when
	// the runtime was constructed without a bus the publish call is a
	// silent no-op so tests + dev environments without realtime
	// configured don't crash hook scripts.
	_ = appBinding.Set("realtime", func(_ goja.FunctionCall) goja.Value {
		return installRealtimeBinding(vm, r.bus)
	})
	// $app.routerAdd(method, path, handler) — register a custom HTTP
	// endpoint served BEFORE the built-in router. Handler receives an
	// `e` object exposing request fields (method/url/path/query/body/
	// headers + pathParam(name)) and response writers (json/text/html/
	// status/header). Routes are collected here and assembled into a
	// fresh chi.Mux at the end of Load(); the runtime's
	// atomic.Pointer[*chi.Mux] swap is what the app-level middleware
	// reads on every request.
	//
	// Route ordering across multiple .js files is alphabetical-by-file
	// then declaration-order (same as on* handlers). Conflicts (same
	// METHOD + PATH twice) keep the LAST registration — chi.Mux's own
	// idempotent behaviour. Operators numbering files `01_foo.js`
	// `02_bar.js` get deterministic precedence.
	routerRoutes := newRouterRouteSet()
	_ = appBinding.Set("routerAdd", func(call goja.FunctionCall) goja.Value {
		if err := routerRoutes.register(vm, call); err != nil {
			panic(vm.NewGoError(fmt.Errorf("$app.routerAdd: %w", err)))
		}
		return goja.Undefined()
	})
	// $app.cronAdd(name, expr, handler) — register a JS function to run
	// on a 5-field cron schedule. Sibling to routerAdd: per-Load collector
	// drives an atomic-swap on the runtime, the cron ticker (started by
	// app.go) reads the pointer on every minute boundary and fires
	// matching handlers under the watchdog. Names are dedup-keys —
	// re-registering the same name within a load replaces the prior
	// entry; across loads, the new load wholly replaces the previous
	// snapshot.
	cronJobs := newCronSet()
	_ = appBinding.Set("cronAdd", func(call goja.FunctionCall) goja.Value {
		if err := cronJobs.register(vm, call); err != nil {
			panic(vm.NewGoError(fmt.Errorf("$app.cronAdd: %w", err)))
		}
		return goja.Undefined()
	})
	// $app.onRequest(handler) — fires SYNCHRONOUSLY before every
	// incoming HTTP request, ahead of the auth / tenant / rbac
	// middleware stack. Handlers can mutate request headers, abort
	// with a response (e.abort(status, body)), or simply observe.
	// Per-Load collector → atomic-swap on finalize → middleware reads
	// the pointer on every request. The /_/* admin UI assets path
	// bypasses dispatch entirely (perf — hooks would add overhead on
	// every static asset fetch).
	requestHandlers := newRequestHandlerSet()
	_ = appBinding.Set("onRequest", func(call goja.FunctionCall) goja.Value {
		if err := requestHandlers.register(vm, call); err != nil {
			panic(vm.NewGoError(fmt.Errorf("$app.onRequest: %w", err)))
		}
		return goja.Undefined()
	})
	_ = vm.Set("$app", appBinding)
	// Tiny console for hook authors — `console.log` ≈ slog.Info.
	console := vm.NewObject()
	_ = console.Set("log", func(call goja.FunctionCall) goja.Value {
		parts := make([]any, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, a.Export())
		}
		r.log.Info("hook console", "args", parts)
		return goja.Undefined()
	})
	_ = console.Set("error", func(call goja.FunctionCall) goja.Value {
		parts := make([]any, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, a.Export())
		}
		r.log.Error("hook console", "args", parts)
		return goja.Undefined()
	})
	_ = vm.Set("console", console)
	// $export — document-generation primitives. Pure functions: they
	// take values + options, return ArrayBuffer of the generated bytes.
	// No filesystem / HTTP side-effects; operators compose those via
	// Go-side primitives. See internal/export for the underlying
	// XLSX / PDF / Markdown→PDF writers.
	installExportBinding(vm)

	// Execute each file with a per-file watchdog so a hung top-level
	// `while (true) {}` can't lock up boot.
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			r.log.Warn("hook: read file failed", "path", f, "err", err)
			continue
		}
		// Re-stamp the source for handlers registered by THIS file.
		// We pre-snapshot the per-collection-per-event lists, run the
		// file, then walk the diff and tag new entries.
		before := snapshotRegistry(reg)
		if err := runWithTimeout(ctx, vm, r.timeout, string(body), f); err != nil {
			r.log.Warn("hook: file failed to load", "path", f, "err", err)
			continue
		}
		stampNewHandlers(reg, before, f)
		r.log.Info("hook: file loaded", "path", f)
	}

	r.registry.Store(reg)
	r.primaryVM = vm

	// Materialise the routerAdd registry into a chi.Mux. An empty
	// route set → nil pointer (the middleware short-circuits cheaply).
	// Building a fresh Mux per Load() is the simplest way to honour
	// re-registration / removal semantics: a deleted .js file drops its
	// routes on the next reload without us tracking individual handles.
	if mux := routerRoutes.buildMux(r); mux != nil {
		r.router.Store(mux)
	} else {
		r.router.Store(nil)
	}

	// Same pattern for $app.cronAdd registrations. Swap is atomic so
	// in-flight cron ticks see a consistent set. Tracking startTime via
	// the freshly-finalized snapshot resets last-fire bookkeeping so a
	// "just removed and re-added" entry can't re-fire for a tick it
	// already serviced under the old snapshot.
	if cs := cronJobs.finalize(r); cs != nil {
		r.crons.Store(cs)
	} else {
		r.crons.Store(nil)
	}

	// $app.onRequest chain — atomic swap so the next incoming request
	// picks up the fresh handlers. In-flight requests keep running
	// against the old chain (the snap pointer is captured at request
	// entry); when they release, the old set is garbage.
	if rs := requestHandlers.finalize(); rs != nil {
		r.onRequest.Store(rs)
	} else {
		r.onRequest.Store(nil)
	}
	return nil
}

// runWithTimeout executes a goja script with a watchdog. Same shape
// as the per-handler watchdog in hooks.go but applied at script-load
// time (so a runaway top-level loop doesn't hang boot).
func runWithTimeout(ctx context.Context, vm *goja.Runtime, timeout time.Duration, src, path string) error {
	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-time.After(timeout):
			vm.Interrupt(fmt.Errorf("load %s: timeout after %s", path, timeout))
		case <-doneCh:
		}
	}()
	vm.ClearInterrupt()
	_, err := vm.RunString(src)
	return err
}

func (r *Runtime) discover() ([]string, error) {
	var out []string
	err := filepath.WalkDir(r.hooksDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip a top-level "node_modules" if someone vendors deps
			// (no module system yet, but anticipating).
			if path != r.hooksDir && d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".js") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Deterministic load order: alphabetical. Operators numbering
	// files `01_setup.js` / `02_extras.js` see predictable behaviour.
	sort.Strings(out)
	return out, nil
}

// snapshotRegistry captures the count of handlers per (coll, event)
// so stampNewHandlers can identify just-registered entries.
type regSnapshot map[string]map[Event]int

func snapshotRegistry(reg *Registry) regSnapshot {
	snap := regSnapshot{}
	for coll, byEvent := range reg.handlers {
		snap[coll] = map[Event]int{}
		for ev, hs := range byEvent {
			snap[coll][ev] = len(hs)
		}
	}
	return snap
}

// stampNewHandlers patches the .source field of handlers added since
// the snapshot was taken. Operators see meaningful file paths in
// error messages.
func stampNewHandlers(reg *Registry, before regSnapshot, source string) {
	for coll, byEvent := range reg.handlers {
		for ev, hs := range byEvent {
			start := before[coll][ev] // zero when (coll, ev) is new
			for i := start; i < len(hs); i++ {
				hs[i].source = source
			}
		}
	}
}

// --- watcher ---

// StartWatcher spins up an fsnotify-driven reloader. Returns a stop
// closure the caller MUST defer.
//
// Debouncing: filesystem operations (especially editor saves like
// vim's "write -> tmp -> rename") can fire multiple events in tight
// sequence. We collapse anything within 150ms into one reload.
func (r *Runtime) StartWatcher(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if _, err := os.Stat(r.hooksDir); errors.Is(err, os.ErrNotExist) {
		// Create the dir so fsnotify can watch it — and operators see
		// it as a hint to drop files in.
		if err := os.MkdirAll(r.hooksDir, 0o755); err != nil {
			return fmt.Errorf("hooks: mkdir %s: %w", r.hooksDir, err)
		}
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("hooks: fsnotify: %w", err)
	}
	// Watch the root dir + every subdirectory (depth-1+).
	if err := w.Add(r.hooksDir); err != nil {
		_ = w.Close()
		return fmt.Errorf("hooks: watch %s: %w", r.hooksDir, err)
	}
	stop := make(chan struct{})
	go r.watchLoop(ctx, w, stop)
	r.stops = append(r.stops, func() {
		close(stop)
		_ = w.Close()
	})
	return nil
}

func (r *Runtime) watchLoop(ctx context.Context, w *fsnotify.Watcher, stop chan struct{}) {
	var reload *time.Timer
	const debounce = 150 * time.Millisecond
	doReload := func() {
		if err := r.Load(ctx); err != nil {
			r.log.Error("hook: reload failed", "err", err)
			return
		}
		r.log.Info("hook: registry reloaded")
	}
	for {
		select {
		case <-stop:
			if reload != nil {
				reload.Stop()
			}
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(strings.ToLower(ev.Name), ".js") {
				continue
			}
			if reload != nil {
				reload.Stop()
			}
			reload = time.AfterFunc(debounce, doReload)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			r.log.Warn("hook: watcher error", "err", err)
		}
	}
}

// installExportBinding wires the `$export` global into vm. Three
// methods: xlsx(rows, opts), pdf(rows, opts), pdfFromMarkdown(md, data?).
// All three return goja.ArrayBuffer values; argument-validation errors
// surface to JS as thrown Go errors via panic(vm.NewGoError(...)) —
// same pattern goja uses for native fn panics.
//
// Row data shape: rows is Array<Array<any>>; columns is string[]; we
// stringify every value via fmt.Sprint (the export writers do the
// same internally) and build a map[string]any keyed by column name
// per row, since the export writers consume map-shaped rows.
func installExportBinding(vm *goja.Runtime) {
	expo := vm.NewObject()
	_ = expo.Set("xlsx", func(call goja.FunctionCall) goja.Value {
		rows, columns, err := parseExportRowsOpts(call, "xlsx")
		if err != nil {
			panic(vm.NewGoError(err))
		}
		sheet := readStringField(call.Argument(1), "sheet")
		cols := make([]export.Column, len(columns))
		for i, c := range columns {
			cols[i] = export.Column{Key: c, Header: c}
		}
		w, err := export.NewXLSXWriter(sheet, cols)
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("$export.xlsx: %w", err)))
		}
		defer w.Discard()
		for _, row := range rows {
			if err := w.AppendRow(buildRowMap(columns, row)); err != nil {
				panic(vm.NewGoError(fmt.Errorf("$export.xlsx: %w", err)))
			}
		}
		var buf bytes.Buffer
		if err := w.Finish(&buf); err != nil {
			panic(vm.NewGoError(fmt.Errorf("$export.xlsx: %w", err)))
		}
		return vm.ToValue(vm.NewArrayBuffer(buf.Bytes()))
	})
	_ = expo.Set("pdf", func(call goja.FunctionCall) goja.Value {
		rows, columns, err := parseExportRowsOpts(call, "pdf")
		if err != nil {
			panic(vm.NewGoError(err))
		}
		cfg := export.PDFConfig{
			Title:  readStringField(call.Argument(1), "title"),
			Header: readStringField(call.Argument(1), "header"),
			Footer: readStringField(call.Argument(1), "footer"),
		}
		cols := make([]export.PDFColumn, len(columns))
		for i, c := range columns {
			cols[i] = export.PDFColumn{Key: c, Header: c}
		}
		w, err := export.NewPDFWriter(cfg, cols)
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("$export.pdf: %w", err)))
		}
		defer w.Discard()
		for _, row := range rows {
			if err := w.AppendRow(buildRowMap(columns, row)); err != nil {
				panic(vm.NewGoError(fmt.Errorf("$export.pdf: %w", err)))
			}
		}
		var buf bytes.Buffer
		if err := w.Finish(&buf); err != nil {
			panic(vm.NewGoError(fmt.Errorf("$export.pdf: %w", err)))
		}
		return vm.ToValue(vm.NewArrayBuffer(buf.Bytes()))
	})
	_ = expo.Set("pdfFromMarkdown", func(call goja.FunctionCall) goja.Value {
		mdArg := call.Argument(0)
		if mdArg == nil || goja.IsUndefined(mdArg) || goja.IsNull(mdArg) {
			panic(vm.NewGoError(fmt.Errorf("$export.pdfFromMarkdown: md must be a string")))
		}
		md := mdArg.String()
		var data map[string]any
		if dv := call.Argument(1); dv != nil && !goja.IsUndefined(dv) && !goja.IsNull(dv) {
			if m, ok := dv.Export().(map[string]any); ok {
				data = m
			}
			// Non-map data is silently ignored — current renderer treats
			// data as passthrough anyway (see RenderMarkdownToPDF godoc).
		}
		out, err := export.RenderMarkdownToPDF([]byte(md), data)
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("$export.pdfFromMarkdown: %w", err)))
		}
		return vm.ToValue(vm.NewArrayBuffer(out))
	})
	_ = vm.Set("$export", expo)
}

// parseExportRowsOpts pulls and validates the (rows, opts) argument
// pair shared by $export.xlsx + $export.pdf. Returns the rows as a
// slice of slices (each inner slice is one row's raw cell values) and
// the column-name strings from opts.columns.
func parseExportRowsOpts(call goja.FunctionCall, fn string) (rows [][]any, columns []string, err error) {
	rowsV := call.Argument(0)
	if rowsV == nil || goja.IsUndefined(rowsV) || goja.IsNull(rowsV) {
		return nil, nil, fmt.Errorf("$export.%s: rows must be an array of arrays", fn)
	}
	rowsRaw, ok := rowsV.Export().([]any)
	if !ok {
		return nil, nil, fmt.Errorf("$export.%s: rows must be an array of arrays", fn)
	}
	rows = make([][]any, 0, len(rowsRaw))
	for i, r := range rowsRaw {
		rowSlice, ok := r.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("$export.%s: rows[%d] must be an array", fn, i)
		}
		rows = append(rows, rowSlice)
	}

	optsV := call.Argument(1)
	if optsV == nil || goja.IsUndefined(optsV) || goja.IsNull(optsV) {
		return nil, nil, fmt.Errorf("$export.%s: opts.columns must be a non-empty string array", fn)
	}
	optsMap, ok := optsV.Export().(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("$export.%s: opts must be an object", fn)
	}
	colsRaw, ok := optsMap["columns"].([]any)
	if !ok || len(colsRaw) == 0 {
		return nil, nil, fmt.Errorf("$export.%s: opts.columns must be a non-empty string array", fn)
	}
	columns = make([]string, len(colsRaw))
	for i, c := range colsRaw {
		s, ok := c.(string)
		if !ok {
			return nil, nil, fmt.Errorf("$export.%s: opts.columns must be a non-empty string array", fn)
		}
		columns[i] = s
	}
	return rows, columns, nil
}

// readStringField pulls one string-valued property from a goja Value
// (expected to be a JS object); returns "" when missing/undefined/null
// or non-string. Used for the optional opts fields (sheet, title,
// header, footer) — all default to empty when unset.
func readStringField(optsV goja.Value, key string) string {
	if optsV == nil || goja.IsUndefined(optsV) || goja.IsNull(optsV) {
		return ""
	}
	m, ok := optsV.Export().(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// buildRowMap converts a positional row (slice of raw cell values) into
// the column-keyed map the underlying export writers consume. nil /
// missing values land as "". Trailing values past len(columns) drop.
func buildRowMap(columns []string, row []any) map[string]any {
	m := make(map[string]any, len(columns))
	for i, col := range columns {
		if i < len(row) {
			v := row[i]
			if v == nil {
				m[col] = ""
			} else {
				m[col] = v
			}
		} else {
			m[col] = ""
		}
	}
	return m
}

// installRealtimeBinding builds the JS object returned by `$app.realtime()`.
// Single method: `.publish(event)` where event is an object shaped like:
//
//	{
//	  collection: "posts",     // required, non-empty string
//	  verb:       "create",    // required; one of "create" | "update" | "delete"
//	  id:         "abc-...",   // optional record id (string)
//	  record:     {...},       // optional record payload (object)
//	  tenantId:   "uuid-..."   // optional; restricts fan-out to that tenant
//	}
//
// Validation errors surface to JS as thrown Go errors via panic(vm.NewGoError),
// matching the $export binding's contract. When bus is nil the publish call
// returns undefined without doing anything — operators running hooks outside
// a wired server (unit tests, ad-hoc evaluations) don't trip over a missing
// dep.
func installRealtimeBinding(vm *goja.Runtime, bus *eventbus.Bus) goja.Value {
	rt := vm.NewObject()
	_ = rt.Set("publish", func(call goja.FunctionCall) goja.Value {
		evt, err := parseRealtimeEvent(call.Argument(0))
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("$app.realtime().publish: %w", err)))
		}
		if bus == nil {
			return goja.Undefined()
		}
		realtime.Publish(bus, evt)
		return goja.Undefined()
	})
	return rt
}

// parseRealtimeEvent translates a JS object into a realtime.RecordEvent.
// Enforces the wire contract: collection + verb required, verb is one of
// the three known verbs, record (if present) is an object. Returns a
// helpful error string the hook author can read in the thrown JS Error.
func parseRealtimeEvent(v goja.Value) (realtime.RecordEvent, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return realtime.RecordEvent{}, fmt.Errorf("event must be an object")
	}
	m, ok := v.Export().(map[string]any)
	if !ok {
		return realtime.RecordEvent{}, fmt.Errorf("event must be an object")
	}
	coll, _ := m["collection"].(string)
	if coll == "" {
		return realtime.RecordEvent{}, fmt.Errorf("event.collection is required (non-empty string)")
	}
	verb, _ := m["verb"].(string)
	switch realtime.Verb(verb) {
	case realtime.VerbCreate, realtime.VerbUpdate, realtime.VerbDelete:
	default:
		return realtime.RecordEvent{}, fmt.Errorf("event.verb must be one of \"create\", \"update\", \"delete\" (got %q)", verb)
	}
	id, _ := m["id"].(string)
	tenantID, _ := m["tenantId"].(string)
	var record map[string]any
	if rv, present := m["record"]; present && rv != nil {
		if rm, ok := rv.(map[string]any); ok {
			record = rm
		} else {
			return realtime.RecordEvent{}, fmt.Errorf("event.record must be an object")
		}
	}
	return realtime.RecordEvent{
		Collection: coll,
		Verb:       realtime.Verb(verb),
		ID:         id,
		Record:     record,
		TenantID:   tenantID,
	}, nil
}

// Stop tears down the watcher (and other resources). Idempotent.
func (r *Runtime) Stop() {
	if r == nil {
		return
	}
	for _, s := range r.stops {
		s()
	}
	r.stops = nil
}
