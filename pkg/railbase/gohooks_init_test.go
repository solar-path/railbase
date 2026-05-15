// Internal-package regression test for FEEDBACK #16 (GoHooks lazy-init
// + Runtime nil-capture). Lives in `package railbase` (NOT
// `railbase_test`) so it can read the unexported `a.goHooks` field
// directly. Same-package test files are a Go convention for asserting
// invariants the public surface doesn't expose.

package railbase

import (
	"testing"
)

// TestEnsureGoHooks_IdempotentAndStable proves the post-FEEDBACK-#16
// invariant: by the time Run() builds the hooks Runtime, a.goHooks is
// non-nil. Run() invokes ensureGoHooks() before constructing the
// Runtime; this test exercises the same helper directly so the fix
// has unit coverage without requiring a live Postgres / hooks Runtime.
//
// Background: the original bug was a value-capture trap —
//
//	hooksRT, _ := hooks.NewRuntime(hooks.Options{
//	    GoHooks: a.goHooks,  // ← captures nil if not initialised yet
//	})
//
// If a.goHooks was nil at that line, the Runtime forever saw nil.
// Embedders who only called app.GoHooks() inside their OnBeforeServe
// callback (which fires AFTER Runtime construction) would have their
// Go-side hooks silently dropped — POST returned 200, audit row
// appeared, the user-collection OnRecordAfterCreate hook never fired.
//
// The fix: ensureGoHooks() runs at the START of Run(), BEFORE any
// Runtime is built. This test asserts:
//
//  1. A fresh App has goHooks == nil (lazy default preserved for
//     embedders who never touch hooks — keeps the dispatcher on its
//     pre-v0.4 hot path).
//  2. After ensureGoHooks(), goHooks is non-nil.
//  3. Calling ensureGoHooks() a second time keeps the same pointer
//     (idempotent — never clobbers handlers registered before Run).
//  4. The public GoHooks() accessor returns the same pointer as the
//     internal field — so a registry obtained pre-Run via GoHooks()
//     is identity-equal to whatever Run() hands to the Runtime.
func TestEnsureGoHooks_IdempotentAndStable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DSN = "postgres://test:test@localhost:5432/test?sslmode=disable"
	cfg.DataDir = t.TempDir()
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// (1) Fresh App: lazy default — goHooks starts nil. If this ever
	// fails, somebody added eager-init to New() and silently changed
	// the dispatcher hot-path semantics — that's a separate decision
	// worth surfacing, not papering over by removing this assert.
	if app.goHooks != nil {
		t.Fatalf("fresh App.goHooks should be nil (lazy); got %p", app.goHooks)
	}

	// (2) After ensureGoHooks() — non-nil.
	app.ensureGoHooks()
	if app.goHooks == nil {
		t.Fatal("ensureGoHooks() did not populate a.goHooks")
	}
	first := app.goHooks

	// (3) Idempotent — second call keeps the same pointer.
	app.ensureGoHooks()
	if app.goHooks != first {
		t.Errorf("ensureGoHooks() clobbered the registry on second call: %p → %p", first, app.goHooks)
	}

	// (4) Public GoHooks() returns the same pointer Run() captures.
	// This is the cornerstone of the fix: a registry obtained via
	// the public accessor (anywhere, anytime — before Run, inside
	// OnBeforeServe, after Run) refers to the SAME object the
	// hooks Runtime got at construction time.
	pub := app.GoHooks()
	if pub != app.goHooks {
		t.Errorf("GoHooks() returned a different pointer than the internal field: %p vs %p", pub, app.goHooks)
	}
}

// TestGoHooks_PreRunRegistration_SurvivesRunInit is the OnBeforeServe-
// scenario half of the regression. We can't fully exercise Run()
// without Postgres, but we CAN prove that:
//
//   - A hook registered on app.GoHooks() at any point pre-Run …
//   - … is still present on app.GoHooks() after ensureGoHooks() runs
//     (Run's pre-Runtime guard).
//
// If a future refactor reverts to clobbering the registry inside
// Run(), this test catches it.
func TestGoHooks_PreRunRegistration_SurvivesRunInit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DSN = "postgres://test:test@localhost:5432/test?sslmode=disable"
	cfg.DataDir = t.TempDir()
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Register a hook via the public surface — embedder-style.
	registry := app.GoHooks()
	registry.OnRecordBeforeCreate("posts", nil) // nil handler is fine for the identity check

	// Simulate what Run() does at its start — must NOT discard the
	// pointer the embedder already captured.
	app.ensureGoHooks()

	if app.GoHooks() != registry {
		t.Fatal("ensureGoHooks() inside Run() would have clobbered an embedder's pre-Run registry — the FEEDBACK #16 fix regressed")
	}
}
