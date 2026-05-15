// Regression tests for FEEDBACK #6 / #27 — scaffold ships a Makefile
// whose dev targets default to `-tags embed_pg`, so the documented
// `RAILBASE_EMBED_POSTGRES=true` and `migrate diff` paths work
// immediately after `railbase init`. Without this, the embedder builds
// a tag-less binary and gets the cryptic
//
//   embedded postgres not compiled in: rebuild with -tags embed_pg
//
// on first `migrate diff` — a known shopper-project pain point.
package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInit_GeneratesMakefile — the scaffold drops a Makefile at the
// project root with build/dev/migrate-diff/migrate-up targets, and
// the dev targets carry `-tags embed_pg`.
func TestInit_GeneratesMakefile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	got := string(body)

	// Targets the shopper-project workflow expects.
	for _, want := range []string{
		"build:",
		"build-prod:",
		"dev:",
		"migrate-diff:",
		"migrate-up:",
		"clean:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Makefile missing target %q\nMakefile:\n%s", want, got)
		}
	}

	// The dev `build` target MUST default to embed_pg — that's the
	// whole point of FEEDBACK #6/#27.
	if !strings.Contains(got, "-tags $(TAGS_DEV)") && !strings.Contains(got, "-tags embed_pg") {
		t.Errorf("Makefile `build` target doesn't pass -tags embed_pg:\n%s", got)
	}
	if !strings.Contains(got, "TAGS_DEV ?= embed_pg") {
		t.Errorf("Makefile missing TAGS_DEV default — operators can't override per-build:\n%s", got)
	}

	// `build-prod` MUST NOT carry the embed_pg tag — production
	// binaries shouldn't bloat by ~50 MB.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "go build") &&
			strings.Contains(line, "BIN_PROD") &&
			strings.Contains(line, "embed_pg") {
			t.Errorf("build-prod must NOT carry embed_pg tag: %q", line)
		}
	}
}

// TestInit_MakefileRendersProjectName — Makefile.tmpl uses
// {{.ProjectName}} for the binary name + ./cmd/<name> path. Make sure
// text/template substitution actually fires.
func TestInit_MakefileRendersProjectName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myshop")
	if _, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"bin/myshop",        // BIN default
		"./cmd/myshop",      // go build target path
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Makefile didn't expand ProjectName to %q\n%s", want, got)
		}
	}
	// The literal template syntax must NOT leak through.
	if strings.Contains(got, "{{.ProjectName}}") {
		t.Errorf("Makefile contains unrendered {{.ProjectName}}:\n%s", got)
	}
}

// TestInit_Makefile_DevTargetsUseEmbedPGEnvar — `dev` and `migrate-*`
// targets must set RAILBASE_EMBED_POSTGRES=true, otherwise the embed_pg
// tag would compile in the support but the runtime wouldn't choose
// the embedded path. Both halves are needed.
func TestInit_Makefile_DevTargetsUseEmbedPGEnvar(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	got := string(body)

	// Count occurrences — dev + migrate-diff + migrate-up = 3 targets,
	// each must set the envar.
	if c := strings.Count(got, "RAILBASE_EMBED_POSTGRES=true"); c < 3 {
		t.Errorf("expected RAILBASE_EMBED_POSTGRES=true in at least 3 dev targets, got %d:\n%s", c, got)
	}
}
