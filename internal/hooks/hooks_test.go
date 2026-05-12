package hooks

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// makeRuntime spins up a hooks runtime against a temp dir, returning
// both the runtime and the hooks dir so tests can drop .js files.
func makeRuntime(t *testing.T, jsFiles map[string]string) *Runtime {
	t.Helper()
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range jsFiles {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRuntime(Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return r
}

func TestNewRuntime_NoDir(t *testing.T) {
	r, err := NewRuntime(Options{HooksDir: ""})
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Errorf("empty HooksDir should yield nil runtime")
	}
}

func TestHasHandlers_Empty(t *testing.T) {
	r := makeRuntime(t, nil)
	if r.HasHandlers("posts", EventRecordBeforeCreate) {
		t.Errorf("empty registry should report no handlers")
	}
}

func TestHasHandlers_AfterLoad(t *testing.T) {
	r := makeRuntime(t, map[string]string{
		"posts.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.title = "modified";
    return e.next();
});
`,
	})
	if !r.HasHandlers("posts", EventRecordBeforeCreate) {
		t.Errorf("expected posts handler after load")
	}
	if r.HasHandlers("posts", EventRecordAfterCreate) {
		t.Errorf("after-create not registered, shouldn't match")
	}
	if r.HasHandlers("other", EventRecordBeforeCreate) {
		t.Errorf("other-collection shouldn't match")
	}
}

func TestDispatch_MutatesRecord(t *testing.T) {
	r := makeRuntime(t, map[string]string{
		"normalize.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.title = e.record.title.trim().toUpperCase();
    e.record.normalized = true;
    return e.next();
});
`,
	})
	rec := map[string]any{"title": "  hello world  "}
	evt, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec)
	if err != nil {
		t.Fatal(err)
	}
	got := evt.Record()
	if got["title"] != "HELLO WORLD" {
		t.Errorf("title = %v, want HELLO WORLD", got["title"])
	}
	if got["normalized"] != true {
		t.Errorf("normalized = %v, want true", got["normalized"])
	}
}

func TestDispatch_ThrowAborts(t *testing.T) {
	r := makeRuntime(t, map[string]string{
		"validate.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    if (!e.record.title) throw new Error("title required");
    return e.next();
});
`,
	})
	rec := map[string]any{"body": "no title"}
	_, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec)
	if err == nil {
		t.Fatalf("expected error for missing title")
	}
	if !strings.Contains(err.Error(), "title required") {
		t.Errorf("error doesn't mention the thrown message: %v", err)
	}
	var he *HandlerError
	if !errAs(err, &he) {
		t.Errorf("expected *HandlerError, got %T", err)
	}
}

func TestDispatch_MultipleHandlersInOrder(t *testing.T) {
	r := makeRuntime(t, map[string]string{
		"a.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.steps = (e.record.steps || []).concat(["a"]);
    return e.next();
});
`,
		"b.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.steps = (e.record.steps || []).concat(["b"]);
    return e.next();
});
`,
	})
	rec := map[string]any{}
	evt, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := evt.Record()["steps"].([]any)
	if len(steps) != 2 || steps[0] != "a" || steps[1] != "b" {
		t.Errorf("expected [a, b], got %v", steps)
	}
}

func TestDispatch_AfterHookThrowIgnored(t *testing.T) {
	r := makeRuntime(t, map[string]string{
		"after.js": `
$app.onRecordAfterCreate("posts").bindFunc((e) => {
    throw new Error("after blew up");
});
`,
	})
	rec := map[string]any{"id": "abc"}
	// After-hook throws are logged but not propagated.
	evt, err := r.Dispatch(context.Background(), "posts", EventRecordAfterCreate, rec)
	if err != nil {
		t.Errorf("after-hook throw should not propagate: %v", err)
	}
	if evt.Record()["id"] != "abc" {
		t.Errorf("record should survive after-hook throw: %v", evt.Record())
	}
}

func TestDispatch_GlobalCollection(t *testing.T) {
	r := makeRuntime(t, map[string]string{
		"audit.js": `
$app.onRecordBeforeCreate("*").bindFunc((e) => {
    e.record.audit_tag = e.collection;
    return e.next();
});
`,
	})
	rec := map[string]any{"x": 1}
	evt, _ := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec)
	if evt.Record()["audit_tag"] != "posts" {
		t.Errorf("global handler should fire for any collection: %v", evt.Record())
	}
}

func TestDispatch_Timeout(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	_ = os.MkdirAll(hooksDir, 0o755)
	_ = os.WriteFile(filepath.Join(hooksDir, "slow.js"), []byte(`
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    while (true) {}  // infinite loop — watchdog should fire
});
`), 0o644)
	r, _ := NewRuntime(Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  200 * time.Millisecond,
	})
	_ = r.Load(context.Background())

	start := time.Now()
	_, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, map[string]any{})
	elapsed := time.Since(start)
	if err == nil {
		t.Errorf("expected timeout error")
	}
	if elapsed > 1*time.Second {
		t.Errorf("watchdog didn't fire promptly: %v", elapsed)
	}
}

func TestDispatch_NilRuntime(t *testing.T) {
	var r *Runtime
	rec := map[string]any{"x": 1}
	evt, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec)
	if err != nil {
		t.Errorf("nil runtime should be a no-op: %v", err)
	}
	if evt.Record()["x"] != 1 {
		t.Errorf("nil-dispatch should pass through record: %v", evt.Record())
	}
}

func TestLoad_SkipsBrokenFile(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	_ = os.MkdirAll(hooksDir, 0o755)
	// One broken file, one good file. Good one should still load.
	_ = os.WriteFile(filepath.Join(hooksDir, "0_broken.js"), []byte(`syntax !!! error @@@`), 0o644)
	_ = os.WriteFile(filepath.Join(hooksDir, "1_good.js"), []byte(`
$app.onRecordBeforeCreate("posts").bindFunc((e) => { e.record.ok = true; return e.next(); });
`), 0o644)
	r, _ := NewRuntime(Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  1 * time.Second,
	})
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.HasHandlers("posts", EventRecordBeforeCreate) {
		t.Errorf("good file should load despite broken peer")
	}
}

func TestLoad_AlphabeticalOrder(t *testing.T) {
	r := makeRuntime(t, map[string]string{
		"02_second.js": `$app.onRecordBeforeCreate("x").bindFunc((e) => { e.record.s = (e.record.s||"")+"2"; return e.next(); });`,
		"01_first.js":  `$app.onRecordBeforeCreate("x").bindFunc((e) => { e.record.s = (e.record.s||"")+"1"; return e.next(); });`,
	})
	evt, _ := r.Dispatch(context.Background(), "x", EventRecordBeforeCreate, map[string]any{})
	if evt.Record()["s"] != "12" {
		t.Errorf("alphabetical load order broken: got %v, want 12", evt.Record()["s"])
	}
}

// runExportHook drops one hook file that calls $export.* and stores
// the resulting bytes in e.record.out, dispatches, and returns the
// captured byte slice. Returns nil bytes + the dispatch error if the
// handler threw. Helper for all the round-trip $export tests.
func runExportHook(t *testing.T, script string) ([]byte, error) {
	t.Helper()
	r := makeRuntime(t, map[string]string{
		"export.js": `
$app.onRecordBeforeCreate("docs").bindFunc((e) => {
` + script + `
    return e.next();
});
`,
	})
	evt, err := r.Dispatch(context.Background(), "docs", EventRecordBeforeCreate, map[string]any{})
	if err != nil {
		return nil, err
	}
	out := evt.Record()["out"]
	if out == nil {
		return nil, nil
	}
	if ab, ok := out.(goja.ArrayBuffer); ok {
		return ab.Bytes(), nil
	}
	if b, ok := out.([]byte); ok {
		return b, nil
	}
	t.Fatalf("e.record.out is %T, expected ArrayBuffer/[]byte", out)
	return nil, nil
}

func TestExport_XLSX_RoundTrip(t *testing.T) {
	bytes, err := runExportHook(t, `
    e.record.out = $export.xlsx(
        [["a", 1], ["b", 2]],
        {columns: ["k", "v"], sheet: "Sheet1"}
    );`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(bytes) < 4 {
		t.Fatalf("xlsx bytes too short: %d", len(bytes))
	}
	// XLSX is a zip file — PK\x03\x04 magic header.
	if bytes[0] != 0x50 || bytes[1] != 0x4B || bytes[2] != 0x03 || bytes[3] != 0x04 {
		t.Errorf("xlsx magic header mismatch: % x", bytes[:4])
	}
}

func TestExport_PDF_RoundTrip(t *testing.T) {
	bytes, err := runExportHook(t, `
    e.record.out = $export.pdf(
        [["alpha", 10]],
        {columns: ["name", "qty"], title: "Q1 Report"}
    );`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(bytes) < 5 {
		t.Fatalf("pdf bytes too short: %d", len(bytes))
	}
	if string(bytes[:5]) != "%PDF-" {
		t.Errorf("pdf magic header mismatch: %q", string(bytes[:8]))
	}
}

func TestExport_PDFFromMarkdown(t *testing.T) {
	bytes, err := runExportHook(t, `
    e.record.out = $export.pdfFromMarkdown("# Title\nBody", {tenant: "acme"});`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(bytes) < 5 {
		t.Fatalf("pdf bytes too short: %d", len(bytes))
	}
	if string(bytes[:5]) != "%PDF-" {
		t.Errorf("pdf magic header mismatch: %q", string(bytes[:8]))
	}
}

func TestExport_XLSX_RejectsBadOpts(t *testing.T) {
	_, err := runExportHook(t, `
    e.record.out = $export.xlsx([["a"]], {columns: []});`)
	if err == nil {
		t.Fatal("expected error for empty columns")
	}
	if !strings.Contains(err.Error(), "columns") {
		t.Errorf("error should mention 'columns': %v", err)
	}
}

func TestExport_XLSX_RejectsNonArrayRows(t *testing.T) {
	_, err := runExportHook(t, `
    e.record.out = $export.xlsx("not an array", {columns: ["k"]});`)
	if err == nil {
		t.Fatal("expected error for non-array rows")
	}
	if !strings.Contains(err.Error(), "rows") && !strings.Contains(err.Error(), "array") {
		t.Errorf("error should mention rows/array: %v", err)
	}
}

// errAs is a tiny errors.As shim that doesn't pull in `errors` as
// a named import (which would shadow the rerr alias in some files).
func errAs(err error, target any) bool {
	if err == nil {
		return false
	}
	t, ok := target.(**HandlerError)
	if !ok {
		return false
	}
	if he, ok := err.(*HandlerError); ok {
		*t = he
		return true
	}
	return false
}
