package testapp

import (
	"context"
	"testing"

	"github.com/railbase/railbase/internal/hooks"
)

// TestMockHookRuntime_BeforeCreate_Mutates verifies the JS hook test
// harness — operators write tests against their hook code without
// spinning up a full TestApp / embedded PG / REST mux.
func TestMockHookRuntime_BeforeCreate_Mutates(t *testing.T) {
	src := `$app.onRecordBeforeCreate("posts").bindFunc(e => {
		e.record.title = e.record.title + "!"
		e.next()
	})`
	rt := NewMockHookRuntime(t).WithHook("posts.js", src)

	rec := map[string]any{"title": "hello"}
	ev, err := rt.FireHook(context.Background(), "posts",
		hooks.EventRecordBeforeCreate, rec)
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if ev == nil {
		t.Fatal("got nil event")
	}
	// Before-hook mutation propagates back via the SAME record map.
	if got, _ := rec["title"].(string); got != "hello!" {
		t.Errorf("title = %q, want %q", got, "hello!")
	}
}

// TestMockHookRuntime_RejectViaError exercises the "hook throws to
// reject" path — operators can validate their reject-logic.
func TestMockHookRuntime_RejectViaError(t *testing.T) {
	src := `$app.onRecordBeforeCreate("posts").bindFunc(e => {
		if (!e.record.title) {
			throw new Error("title required")
		}
		e.next()
	})`
	rt := NewMockHookRuntime(t).WithHook("posts.js", src)

	_, err := rt.FireHook(context.Background(), "posts",
		hooks.EventRecordBeforeCreate, map[string]any{"title": ""})
	if err == nil {
		t.Fatal("expected hook to reject empty title")
	}
}

// TestMockHookRuntime_AfterHookFires verifies After-hooks run + their
// throws don't propagate as errors (After is fire-and-forget per
// hooks.Dispatch's documented contract).
func TestMockHookRuntime_AfterHookFires(t *testing.T) {
	// After-hook side effect: we can't easily prove it ran from inside
	// JS without a Go-callable side channel. Instead, prove that a
	// THROWING after-hook does NOT propagate the error to the caller —
	// that's the documented contract and our test doubles as a
	// regression guard if someone "fixes" After-hook error handling
	// to be like Before.
	src := `$app.onRecordAfterCreate("posts").bindFunc(e => {
		throw new Error("this throw should be swallowed")
	})`
	rt := NewMockHookRuntime(t).WithHook("after.js", src)

	_, err := rt.FireHook(context.Background(), "posts",
		hooks.EventRecordAfterCreate, map[string]any{"id": "1"})
	if err != nil {
		t.Fatalf("After-hook throw should NOT propagate, got: %v", err)
	}
}

// TestMockHookRuntime_MultipleHooks loads two files at once + asserts
// both register handlers. Models the typical pb_data/hooks/ layout
// where operators split hooks across files.
func TestMockHookRuntime_MultipleHooks(t *testing.T) {
	rt := NewMockHookRuntime(t).
		WithHook("a.js", `$app.onRecordBeforeCreate("posts").bindFunc(e => { e.next() })`).
		WithHook("b.js", `$app.onRecordBeforeUpdate("posts").bindFunc(e => { e.next() })`)

	// Lazy-load via the first FireHook.
	_, err := rt.FireHook(context.Background(), "posts",
		hooks.EventRecordBeforeCreate, map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("create fire: %v", err)
	}
	if !rt.Runtime().HasHandlers("posts", hooks.EventRecordBeforeCreate) {
		t.Errorf("expected handler registered for posts:before_create")
	}
	if !rt.Runtime().HasHandlers("posts", hooks.EventRecordBeforeUpdate) {
		t.Errorf("expected handler registered for posts:before_update")
	}
}

// TestMockHookRuntime_NoHandlers_DispatchIsNoop guards the fast path
// where Dispatch finds zero handlers (HasHandlers returns false).
// Common case for tests that exercise unrelated collections.
func TestMockHookRuntime_NoHandlers_DispatchIsNoop(t *testing.T) {
	rt := NewMockHookRuntime(t).
		WithHook("only_posts.js", `$app.onRecordBeforeCreate("posts").bindFunc(e => { e.next() })`)

	// Dispatch a DIFFERENT collection — no matching handler → noop.
	rec := map[string]any{"id": "1"}
	ev, err := rt.FireHook(context.Background(), "comments",
		hooks.EventRecordBeforeCreate, rec)
	if err != nil {
		t.Fatalf("noop dispatch returned err: %v", err)
	}
	if ev == nil {
		t.Fatal("noop dispatch returned nil event")
	}
}
