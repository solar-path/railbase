package testapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/hooks"
)

// MockHookRuntime is the JS-side hook-test harness — closes §3.12.8
// ("`mockApp().fireHook()` deferred") from plan.md. Operators writing
// JS hooks in `pb_data/hooks/` can verify behaviour from Go tests
// WITHOUT spinning up a full TestApp / embedded Postgres / REST mux.
//
// Wire it like this in a unit test:
//
//	src := `$app.onRecordBeforeCreate("posts").bindFunc(e => {
//	    if (!e.record.title) throw new Error("title required")
//	    e.next()
//	})`
//	rt := testapp.NewMockHookRuntime(t).
//	    WithHook("posts.js", src)
//	ev, err := rt.FireHook(t.Context(), "posts",
//	    hooks.EventRecordBeforeCreate, map[string]any{"title": ""})
//	if err == nil { t.Fatal("expected throw on empty title") }
//
// The helper writes inline JS to a t.TempDir() so the existing
// hooks.NewRuntime loader path drives the test — no new public surface
// in `internal/hooks` needed. The Runtime is torn down via t.Cleanup.
//
// Limitations (vs the full TestApp): no $app.realtime/$app.routerAdd/
// $app.cronAdd bindings get a real bus or HTTP mux (they'll be silent
// no-ops). For end-to-end tests use `testapp.New(t, ...)` instead;
// MockHookRuntime is for FAST per-hook unit tests.
type MockHookRuntime struct {
	t       testing.TB
	tmpDir  string
	rt      *hooks.Runtime
	started bool
}

// NewMockHookRuntime sets up a tempdir-backed hook runtime. Returns
// the harness so chains like
// `NewMockHookRuntime(t).WithHook(...).FireHook(...)` work.
//
// Hooks aren't loaded yet — call WithHook one or more times, then
// FireHook to lazily Start() + Dispatch.
func NewMockHookRuntime(t testing.TB) *MockHookRuntime {
	t.Helper()
	tmp := t.TempDir()
	rt, err := hooks.NewRuntime(hooks.Options{
		HooksDir: tmp,
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("hook mock: NewRuntime: %v", err)
	}
	if rt == nil {
		// HooksDir="" path returns nil — shouldn't happen because we
		// just passed a non-empty tmp. Defensive.
		t.Fatal("hook mock: NewRuntime returned nil unexpectedly")
	}
	return &MockHookRuntime{t: t, tmpDir: tmp, rt: rt}
}

// WithHook writes inline JS to <tempdir>/<filename>; filename must
// end in `.js`. Multiple calls accumulate — call this once per logical
// hook file you want loaded.
//
// Loading happens lazily on the first FireHook call (Start triggers
// the full load + parse + appBinding wiring).
func (m *MockHookRuntime) WithHook(filename, source string) *MockHookRuntime {
	m.t.Helper()
	if m.started {
		m.t.Fatalf("hook mock: WithHook called after FireHook; add all hooks first")
	}
	if filepath.Ext(filename) != ".js" {
		m.t.Fatalf("hook mock: filename %q must end in .js", filename)
	}
	full := filepath.Join(m.tmpDir, filename)
	if err := os.WriteFile(full, []byte(source), 0o644); err != nil {
		m.t.Fatalf("hook mock: write %s: %v", filename, err)
	}
	return m
}

// FireHook lazy-loads the runtime registry on first call, then
// synthesises a dispatch for (collection, event, record). The record
// map may be mutated by Before-hooks; callers inspect the returned
// event's underlying record via Dispatch's normal contract.
//
// Returns the same (event, error) pair as hooks.Runtime.Dispatch:
//   - On clean completion: (event, nil)
//   - On a handler throw: (event, error w/ hook details) — caller
//     uses errors.As or just checks err != nil for reject-tests.
//
// We intentionally do NOT call StartWatcher — fsnotify isn't useful
// for unit tests (the source is fixed once WithHook returns; nothing
// to watch for) and skipping it avoids the inotify-fd cost on Linux
// where parallel test packages can blow through the per-process fd
// limit.
func (m *MockHookRuntime) FireHook(ctx context.Context, collection string, event hooks.Event, record map[string]any) (*hooks.RecordEvent, error) {
	m.t.Helper()
	if !m.started {
		// Load() parses every .js file in HooksDir, populates the
		// atomic-pointer registry. No watcher; the test exits before
		// any file would change anyway.
		if err := m.rt.Load(ctx); err != nil {
			m.t.Fatalf("hook mock: Load: %v", err)
		}
		m.started = true
	}
	return m.rt.Dispatch(ctx, collection, event, record)
}

// Runtime exposes the underlying hooks.Runtime for tests that need to
// poke at it directly (e.g. assert HasHandlers, count registered
// handlers, etc.). Most callers won't need this.
func (m *MockHookRuntime) Runtime() *hooks.Runtime {
	return m.rt
}
