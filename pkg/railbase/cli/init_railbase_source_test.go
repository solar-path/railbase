// Regression tests for FEEDBACK #12 — `railbase init --railbase-source
// <path>` used to accept any path silently and write the replace
// directive into the user's go.mod, even when the path didn't exist
// or didn't contain a railbase checkout. The embedder hit a confusing
// `go mod tidy` failure later.
//
// validateRailbaseSource fails fast with a message that names the
// actual problem (no directory / no go.mod / wrong module).
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateRailbaseSource_OK — a real railbase-shaped go.mod
// passes. The check is content-based, not directory-name based, so a
// renamed checkout still works.
func TestValidateRailbaseSource_OK(t *testing.T) {
	dir := t.TempDir()
	body := `module github.com/railbase/railbase

go 1.26
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateRailbaseSource(dir); err != nil {
		t.Errorf("expected nil error for valid railbase checkout, got: %v", err)
	}
}

// TestValidateRailbaseSource_OK_Quoted — go.mod's module line is
// sometimes quoted (`module "github.com/..."`). Tolerate that shape.
func TestValidateRailbaseSource_OK_Quoted(t *testing.T) {
	dir := t.TempDir()
	body := `module "github.com/railbase/railbase"

go 1.26
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateRailbaseSource(dir); err != nil {
		t.Errorf("quoted module line should also pass, got: %v", err)
	}
}

// TestValidateRailbaseSource_NotADirectory — passing a file path
// (e.g. somebody auto-completed to go.mod itself) is a clear user
// error and should be reported as such.
func TestValidateRailbaseSource_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(file, []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateRailbaseSource(file)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected 'not a directory' error, got: %v", err)
	}
}

// TestValidateRailbaseSource_DoesNotExist — the shopper-classic
// scenario: typo in the path. Must name the bad path and the actual
// problem ("does not exist"), not a generic stat error.
func TestValidateRailbaseSource_DoesNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	err := validateRailbaseSource(missing)
	if err == nil {
		t.Fatalf("expected error for missing path")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error must say 'does not exist', got: %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error must quote the bad path %q, got: %v", missing, err)
	}
}

// TestValidateRailbaseSource_NoGoMod — directory exists but no
// go.mod. This is the exact case from the feedback: shopper passed
// pwd, which had no go.mod, and the scaffold wrote a broken replace.
func TestValidateRailbaseSource_NoGoMod(t *testing.T) {
	dir := t.TempDir()
	err := validateRailbaseSource(dir)
	if err == nil {
		t.Fatalf("expected error for directory without go.mod")
	}
	if !strings.Contains(err.Error(), "no go.mod") {
		t.Errorf("error must say 'no go.mod', got: %v", err)
	}
	// The fix-it text must mention how to recover (omit the flag).
	if !strings.Contains(err.Error(), "omit the flag") {
		t.Errorf("error should point at the recovery path, got: %v", err)
	}
}

// TestValidateRailbaseSource_WrongModule — directory has a go.mod
// but for a different module (sibling Go project mistake). Must
// reject with a clear message naming the expected module path.
func TestValidateRailbaseSource_WrongModule(t *testing.T) {
	dir := t.TempDir()
	body := `module github.com/example/somethingelse

go 1.26
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateRailbaseSource(dir)
	if err == nil {
		t.Fatalf("expected error for wrong-module go.mod")
	}
	if !strings.Contains(err.Error(), "not the railbase module") {
		t.Errorf("error must say 'not the railbase module', got: %v", err)
	}
	if !strings.Contains(err.Error(), railbaseModulePath) {
		t.Errorf("error must name the expected module path %q, got: %v", railbaseModulePath, err)
	}
}
