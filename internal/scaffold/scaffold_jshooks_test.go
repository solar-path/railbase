// Regression test for FEEDBACK #1 — the scaffold's pb_hooks/example.pb.js
// used to ship with a `// v0.2: not yet executed` comment and a stale
// global-function API (`onRecordCreate("posts", (e) => {...})`). The
// goja runtime had been shipping for releases — embedders read the
// comment, assumed JS hooks were a no-op, and migrated their logic to
// Go (a shopper-project pain point).
//
// This test loads the scaffold's example file into the real hooks
// runtime and asserts:
//   - it parses without error
//   - the `posts` BeforeCreate handler is registered
//   - dispatching to it fires the trim+validate logic from the example
//
// If the scaffold ever regresses to a non-executing placeholder, this
// test fails loudly.
package scaffold

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/hooks"
)

// TestInit_PBHooksExample_ParsesAndRuns proves the file shipped under
// pb_hooks/example.pb.js actually loads + executes in the same goja
// runtime production uses.
func TestInit_PBHooksExample_ParsesAndRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	hooksDir := filepath.Join(dir, "pb_hooks")
	body, err := os.ReadFile(filepath.Join(hooksDir, "example.pb.js"))
	if err != nil {
		t.Fatalf("read example.pb.js: %v", err)
	}
	// Sanity: the stale comment must NOT survive. If someone re-adds
	// the "v0.2: not yet executed" placeholder, fail before the runtime
	// load even tries.
	if strings.Contains(string(body), "not yet executed") {
		t.Errorf("scaffold example.pb.js still claims `not yet executed`:\n%s", body)
	}

	// Build the same runtime the server uses, point it at the scaffold
	// hooks dir, and load.
	r, err := hooks.NewRuntime(hooks.Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if r == nil {
		t.Fatalf("Runtime should be non-nil when HooksDir is set")
	}
	if err := r.Load(context.Background()); err != nil {
		t.Fatalf("Load scaffold example.pb.js: %v", err)
	}

	// The example registers $app.onRecordBeforeCreate("posts") AND
	// onRecordAfterCreate("posts"). Both must be visible to the
	// dispatcher.
	if !r.HasHandlers("posts", hooks.EventRecordBeforeCreate) {
		t.Errorf("scaffold example didn't register BeforeCreate(posts)")
	}
	if !r.HasHandlers("posts", hooks.EventRecordAfterCreate) {
		t.Errorf("scaffold example didn't register AfterCreate(posts)")
	}

	// Dispatch: the Before handler trims the title and rejects empty.
	rec := map[string]any{"title": "  hello world  "}
	evt, err := r.Dispatch(context.Background(), "posts", hooks.EventRecordBeforeCreate, rec)
	if err != nil {
		t.Fatalf("Dispatch BeforeCreate: %v", err)
	}
	if got := evt.Record()["title"]; got != "hello world" {
		t.Errorf("Before-hook should have trimmed title, got %q", got)
	}

	// Empty title must be rejected (the example throws).
	_, err = r.Dispatch(context.Background(), "posts", hooks.EventRecordBeforeCreate, map[string]any{"title": "   "})
	if err == nil {
		t.Errorf("Before-hook should have rejected empty title, got nil error")
	}
}

// TestInit_PBHooksExample_UsesCurrentAPI — the example uses the v1.2.0
// `$app.onRecordBeforeCreate("col").bindFunc(...)` shape, not the
// legacy global `onRecordCreate("col", ...)` shape. The legacy form
// silently no-ops under the current runtime.
func TestInit_PBHooksExample_UsesCurrentAPI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "pb_hooks", "example.pb.js"))
	if err != nil {
		t.Fatalf("read example.pb.js: %v", err)
	}
	src := string(body)
	if !strings.Contains(src, "$app.onRecordBeforeCreate") {
		t.Errorf("example must use the current $app.onRecord* API:\n%s", src)
	}
	if !strings.Contains(src, ".bindFunc(") {
		t.Errorf("example must use .bindFunc() to attach handlers:\n%s", src)
	}
	// The legacy global form must NOT appear — it would silently no-op.
	for _, bad := range []string{
		`onRecordCreate("`,
		`onRecordUpdate("`,
		`onRecordDelete("`,
	} {
		if strings.Contains(src, bad) {
			t.Errorf("example uses legacy global form %q — will silently no-op:\n%s", bad, src)
		}
	}
}
