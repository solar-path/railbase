package hooks

import (
	"errors"
	"testing"

	"github.com/dop251/goja"
)

// TestApplyStackCap_Defaults asserts the default-cap path uses
// DefaultMaxCallStackSize. Tighter than goja's 200 default; documents
// the v1.x partial closure of the "memory limit deferred" deferral.
func TestApplyStackCap_Defaults(t *testing.T) {
	vm := applyStackCap(goja.New(), 0)
	// Recurse 1000 times — must overflow the 128 cap → StackOverflowError.
	_, err := vm.RunString(`
		function f(n) { return f(n+1); }
		f(0);
	`)
	if err == nil {
		t.Fatal("expected stack overflow, got nil")
	}
	var soe *goja.StackOverflowError
	if !errors.As(err, &soe) {
		t.Errorf("error not *goja.StackOverflowError: %T %v", err, err)
	}
}

// TestApplyStackCap_OperatorOverride asserts callers can lift the cap.
func TestApplyStackCap_OperatorOverride(t *testing.T) {
	vm := applyStackCap(goja.New(), 16)
	_, err := vm.RunString(`
		function f(n) { return f(n+1); }
		f(0);
	`)
	if err == nil {
		t.Fatal("expected stack overflow with cap=16, got nil")
	}
	var soe *goja.StackOverflowError
	if !errors.As(err, &soe) {
		t.Errorf("error not *goja.StackOverflowError: %T %v", err, err)
	}
}

// TestApplyStackCap_ShallowOK asserts legitimate non-recursive code
// works at the default. Regression guard against accidentally setting
// the cap too low (e.g. <16 would break our own VM bindings).
func TestApplyStackCap_ShallowOK(t *testing.T) {
	vm := applyStackCap(goja.New(), 0)
	v, err := vm.RunString(`
		function fib(n) { if (n < 2) return n; return fib(n-1) + fib(n-2); }
		fib(10);
	`)
	if err != nil {
		t.Fatalf("fib(10) failed: %v", err)
	}
	if v.ToInteger() != 55 {
		t.Errorf("fib(10) = %d, want 55", v.ToInteger())
	}
}

// TestApplyStackCap_Disabled documents the -1 escape hatch. Operators
// with extreme DSLs can disable the cap; relying on it is NOT
// recommended but the surface exists for completeness.
func TestApplyStackCap_Disabled(t *testing.T) {
	vm := applyStackCap(goja.New(), -1)
	// 200-deep recursion succeeds because cap is effectively off.
	// Don't go too deep — Go's own goroutine stack (8KB default)
	// limits us; 200 frames is well within that.
	v, err := vm.RunString(`
		function f(n) { if (n <= 0) return "done"; return f(n-1); }
		f(200);
	`)
	if err != nil {
		t.Fatalf("recursion at -1 (disabled) failed: %v", err)
	}
	if v.String() != "done" {
		t.Errorf("got %q, want done", v.String())
	}
}
