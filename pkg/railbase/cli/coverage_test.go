package cli

// v1.7.28d — `railbase coverage` parser + renderer tests.
//
// We test the pure parse/render layer with inline fixture strings,
// plus a single cobra-level smoke test for the missing-file error
// path. The command itself doesn't shell out to `go test`, so unlike
// test_test.go we can drive it end-to-end safely.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tiny but representative coverprofile. Two files; one block in
// each file is hit, one isn't. Per-file totals should be:
//
//	a.go: 3 stmts / 2 covered = 66.7%
//	b.go: 2 stmts / 0 covered = 0%
//
// The hit/miss split per file matters for the asserts.
const fixtureGoCoverprofile = `mode: set
github.com/example/pkg/a.go:1.1,3.2 2 1
github.com/example/pkg/a.go:5.1,5.10 1 0
github.com/example/pkg/b.go:1.1,2.5 2 0
`

// A c8 fixture with two files. Each file has 3 statements; the first
// hits 2/3, the second hits 0/3. We use json.RawMessage for the map
// values, so any non-empty placeholder is fine in statementMap.
const fixtureC8JSON = `{
  "/repo/src/foo.ts": {
    "path": "/repo/src/foo.ts",
    "statementMap": {"0": {}, "1": {}, "2": {}},
    "s": {"0": 1, "1": 2, "2": 0}
  },
  "/repo/src/bar.ts": {
    "statementMap": {"0": {}, "1": {}, "2": {}},
    "s": {"0": 0, "1": 0, "2": 0}
  }
}`

// TestParseGoCoverProfile_Valid — sample profile string, assert
// per-file totals are correct and ordering is deterministic.
func TestParseGoCoverProfile_Valid(t *testing.T) {
	rows, err := parseGoCoverProfile(strings.NewReader(fixtureGoCoverprofile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 files, got %d (%+v)", len(rows), rows)
	}
	// Sorted alphabetically — a.go first.
	if !strings.HasSuffix(rows[0].File, "a.go") {
		t.Errorf("rows[0].File = %q, want suffix a.go", rows[0].File)
	}
	if rows[0].Statements != 3 || rows[0].Covered != 2 {
		t.Errorf("a.go: stmts=%d covered=%d, want 3/2", rows[0].Statements, rows[0].Covered)
	}
	if rows[0].Percent < 66.6 || rows[0].Percent > 66.7 {
		t.Errorf("a.go percent = %.2f, want ~66.67", rows[0].Percent)
	}
	if rows[1].Statements != 2 || rows[1].Covered != 0 {
		t.Errorf("b.go: stmts=%d covered=%d, want 2/0", rows[1].Statements, rows[1].Covered)
	}
}

// TestParseGoCoverProfile_EmptyFile — empty input is not an error.
// `go test` produces an empty file when no tests ran.
func TestParseGoCoverProfile_EmptyFile(t *testing.T) {
	rows, err := parseGoCoverProfile(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("want 0 rows, got %d", len(rows))
	}
}

// TestParseGoCoverProfile_Malformed — a line missing fields surfaces
// as an error with the line number.
func TestParseGoCoverProfile_Malformed(t *testing.T) {
	bad := "mode: set\nbroken-line-no-colon\n"
	_, err := parseGoCoverProfile(strings.NewReader(bad))
	if err == nil {
		t.Fatal("expected error for malformed line")
	}
}

// TestParseC8JSON_Valid — sample c8 JSON, assert per-file totals.
func TestParseC8JSON_Valid(t *testing.T) {
	rows, err := parseC8JSON(strings.NewReader(fixtureC8JSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 files, got %d", len(rows))
	}
	// Sorted alphabetically — bar.ts comes before foo.ts.
	var foo, bar fileCoverage
	for _, r := range rows {
		if strings.HasSuffix(r.File, "foo.ts") {
			foo = r
		}
		if strings.HasSuffix(r.File, "bar.ts") {
			bar = r
		}
	}
	if foo.Statements != 3 || foo.Covered != 2 {
		t.Errorf("foo.ts: stmts=%d covered=%d, want 3/2", foo.Statements, foo.Covered)
	}
	if bar.Statements != 3 || bar.Covered != 0 {
		t.Errorf("bar.ts: stmts=%d covered=%d, want 3/0", bar.Statements, bar.Covered)
	}
}

// TestParseC8JSON_MalformedJSON — bad JSON returns an error.
func TestParseC8JSON_MalformedJSON(t *testing.T) {
	_, err := parseC8JSON(strings.NewReader("{not valid json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// TestRenderHTML_BothSides — both Go + JS inputs render with both
// totals present and the "no data" placeholder absent.
func TestRenderHTML_BothSides(t *testing.T) {
	goRows, _ := parseGoCoverProfile(strings.NewReader(fixtureGoCoverprofile))
	jsRows, _ := parseC8JSON(strings.NewReader(fixtureC8JSON))
	rep := coverageReport{
		Go: goRows, GoTotal: totalsOf(goRows),
		JS: jsRows, JSTotal: totalsOf(jsRows),
	}
	var buf bytes.Buffer
	if err := renderHTML(&buf, rep); err != nil {
		t.Fatalf("renderHTML: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Go", "JavaScript", "TOTAL",
		"a.go", "b.go", "foo.ts", "bar.ts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(out, "No Go coverprofile provided") {
		t.Error("HTML unexpectedly shows the 'no Go' empty state")
	}
	if strings.Contains(out, "No Vitest c8 JSON provided") {
		t.Error("HTML unexpectedly shows the 'no JS' empty state")
	}
}

// TestRenderHTML_GoOnly — Go-only input renders the Go side and
// shows the JS empty state instead of crashing.
func TestRenderHTML_GoOnly(t *testing.T) {
	goRows, _ := parseGoCoverProfile(strings.NewReader(fixtureGoCoverprofile))
	rep := coverageReport{Go: goRows, GoTotal: totalsOf(goRows)}
	var buf bytes.Buffer
	if err := renderHTML(&buf, rep); err != nil {
		t.Fatalf("renderHTML: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "a.go") {
		t.Error("expected Go table to be rendered")
	}
	if !strings.Contains(out, "No Vitest c8 JSON provided") {
		t.Error("expected JS empty state placeholder")
	}
}

// TestRenderHTML_JSOnly — symmetric to the Go-only case.
func TestRenderHTML_JSOnly(t *testing.T) {
	jsRows, _ := parseC8JSON(strings.NewReader(fixtureC8JSON))
	rep := coverageReport{JS: jsRows, JSTotal: totalsOf(jsRows)}
	var buf bytes.Buffer
	if err := renderHTML(&buf, rep); err != nil {
		t.Fatalf("renderHTML: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "foo.ts") {
		t.Error("expected JS table to be rendered")
	}
	if !strings.Contains(out, "No Go coverprofile provided") {
		t.Error("expected Go empty state placeholder")
	}
}

// TestRenderHTML_NoInputs_ReturnsError — both sides empty errors out.
func TestRenderHTML_NoInputs_ReturnsError(t *testing.T) {
	var buf bytes.Buffer
	err := renderHTML(&buf, coverageReport{})
	if err == nil {
		t.Fatal("expected error when both sides empty")
	}
	if !strings.Contains(err.Error(), "no coverage data") {
		t.Errorf("error msg = %q, want substring 'no coverage data'", err.Error())
	}
}

// TestCommand_FilesNotFound_FriendlyError — invoking the cobra
// command with explicitly-bad paths gives a clear error (not a stack
// trace). We rely on Changed() detection: explicit flag → missing
// file is fatal.
func TestCommand_FilesNotFound_FriendlyError(t *testing.T) {
	tmp := t.TempDir()
	cmd := newCoverageCmd()
	cmd.SetArgs([]string{
		"--go", filepath.Join(tmp, "does-not-exist.out"),
		"--js", filepath.Join(tmp, "also-missing.json"),
		"--out", filepath.Join(tmp, "coverage.html"),
	})
	// Capture stderr-shaped output so we don't pollute test logs.
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing files")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		t.Errorf("error msg = %q, want substring 'not found'", msg)
	}
	// No stack trace — cobra error messages are single-line.
	if strings.Contains(msg, "goroutine") || strings.Contains(msg, ".go:") {
		t.Errorf("error message looks like a stack trace: %q", msg)
	}
}

// TestCommand_DefaultsMissing_StillFriendly — when both default paths
// are absent (CWD has no coverage files), the error names both paths
// and suggests --go / --js.
func TestCommand_DefaultsMissing_StillFriendly(t *testing.T) {
	tmp := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(cwd)

	cmd := newCoverageCmd()
	cmd.SetArgs([]string{"--out", filepath.Join(tmp, "out.html")})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both default inputs missing")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no coverage inputs found") {
		t.Errorf("error msg = %q, want 'no coverage inputs found'", msg)
	}
}

// TestCommand_EndToEnd_WritesHTML — happy path: write a real go
// coverprofile to disk, invoke the command, check the HTML output
// landed on disk and contains both tables.
func TestCommand_EndToEnd_WritesHTML(t *testing.T) {
	tmp := t.TempDir()
	goPath := filepath.Join(tmp, "cov.out")
	jsPath := filepath.Join(tmp, "c8.json")
	outPath := filepath.Join(tmp, "report.html")
	if err := os.WriteFile(goPath, []byte(fixtureGoCoverprofile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsPath, []byte(fixtureC8JSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newCoverageCmd()
	cmd.SetArgs([]string{"--go", goPath, "--js", jsPath, "--out", outPath})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	html, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	s := string(html)
	for _, want := range []string{"<!DOCTYPE html>", "a.go", "foo.ts", "TOTAL"} {
		if !strings.Contains(s, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}
