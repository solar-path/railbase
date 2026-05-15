// Regression tests for the scaffold-side v0.4.1 fixes (Sentinel
// FEEDBACK.md #8 and #10).
//
// #8: scaffold wrote `git describe`-shaped version strings (e.g.
// `b2a9eb7-dirty`) verbatim into the user's go.mod `require` line.
// `go mod tidy` then rejected them — first thing a Sentinel-style
// integrator does after `railbase init <name>` is run `go mod tidy`,
// which immediately exploded.
//
// #10: scaffold's railbase.yaml.tmpl shipped with `addr: ":8090"`,
// the legacy PocketBase port. The binary's default is `:8095`. The
// mismatch silently bound the project to a different port than the
// docs claimed.
package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsValidGoModuleVersion proves the regex classifies every shape
// we care about, so callers know whether to keep the version as-is
// or substitute the well-known unknown pseudo
// `v0.0.0-00010101000000-000000000000`.
//
// FEEDBACK.md #8.
func TestIsValidGoModuleVersion(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
		why   string
	}{
		// Valid semver tags — every real release should pass.
		{"v0.4.1", true, "vanilla semver"},
		{"v1.0.0", true, "release tag"},
		{"v0.4.1-rc1", true, "prerelease tag"},
		{"v0.4.1+meta", true, "build metadata"},
		{"v0.4.1-rc1+meta", true, "prerelease + meta"},
		// Valid pseudo-versions — what `go get @master` produces.
		{"v0.0.0-20251201123456-abc123abc123", true, "pseudo-version"},

		// Invalid shapes — must be coerced to placeholder.
		{"", false, "empty"},
		{"v0.0.0-dev", false, "buildinfo fallback (recognised but go-mod-invalid)"},
		{"b2a9eb7-dirty", false, "git describe dirty short hash"},
		{"v0.4", false, "missing patch"},
		{"0.4.1", false, "missing leading v"},
		{"main", false, "branch name"},
		// NOTE: the regex is intentionally lenient on prerelease IDs
		// (see goVersionRE doc comment), so shapes like
		// `v0.0.0-shorttime-abc123abc123` pass as a prerelease tag —
		// Go itself accepts them. We don't try to be stricter than
		// `go mod tidy`.
	}
	for _, tc := range cases {
		got := isValidGoModuleVersion(tc.in)
		if got != tc.valid {
			t.Errorf("isValidGoModuleVersion(%q) = %v, want %v (%s)", tc.in, got, tc.valid, tc.why)
		}
	}
}

// TestInit_CoercesInvalidVersionToPlaceholder runs the actual scaffold
// against a `git describe` version string and asserts the generated
// go.mod contains the Go-module placeholder, not the raw input. This
// is the symptom Sentinel actually hit — proves the whole path, not
// just the regex.
func TestInit_CoercesInvalidVersionToPlaceholder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	_, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "b2a9eb7-dirty",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	body := string(gomod)
	if strings.Contains(body, "b2a9eb7-dirty") {
		t.Errorf("go.mod still contains the raw invalid version:\n%s", body)
	}
	if !strings.Contains(body, "v0.0.0-00010101000000-000000000000") {
		t.Errorf("go.mod missing placeholder pseudo-version:\n%s", body)
	}
}

// TestInit_KeepsValidVersionUnchanged proves a real semver tag (or
// pseudo-version) passes through untouched — the coercion path only
// fires for invalid inputs.
func TestInit_KeepsValidVersionUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	_, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.1",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	body := string(gomod)
	if !strings.Contains(body, "v0.4.1") {
		t.Errorf("go.mod missing v0.4.1: %s", body)
	}
	if strings.Contains(body, "v0.0.0-00010101000000-000000000000") {
		t.Errorf("valid version got coerced to placeholder anyway:\n%s", body)
	}
}

// TestInit_GeneratesWebembedPackage proves a fresh scaffold lays down
// the webembed/ Go package + a non-empty web-dist/ subtree so
// `go:embed all:web-dist` compiles immediately, without forcing the
// operator to write the boilerplate themselves.
//
// Closes Sentinel FEEDBACK.md G3 — Sentinel had to hand-write a
// 13-line webembed/embed.go to get a single-binary build. The
// scaffold now ships that file pre-populated.
func TestInit_GeneratesWebembedPackage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	_, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.2",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	// embed.go must exist and reference the embed FS pattern.
	body, err := os.ReadFile(filepath.Join(dir, "webembed", "embed.go"))
	if err != nil {
		t.Fatalf("read webembed/embed.go: %v", err)
	}
	for _, want := range []string{
		"package webembed",
		`//go:embed all:web-dist`,
		"func FS() fs.FS",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("webembed/embed.go missing %q\nbody:\n%s", want, body)
		}
	}
	// web-dist/ must be non-empty so the embed directive doesn't fail.
	if _, err := os.Stat(filepath.Join(dir, "webembed", "web-dist", "README.txt")); err != nil {
		t.Errorf("webembed/web-dist/README.txt missing — go:embed all:web-dist would fail on empty dir: %v", err)
	}
}

// TestInit_MainWiresWebembed proves the scaffolded main.go imports
// the webembed package and calls ServeStaticFS in the ExecuteWith
// callback. Without this wiring, the webembed package would compile
// but never serve — a silent regression.
func TestInit_MainWiresWebembed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	_, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.2",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	mainGo, err := os.ReadFile(filepath.Join(dir, "cmd", "demo", "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(mainGo)
	for _, want := range []string{
		`"demo/webembed"`,                    // import path uses module path
		`cli.ExecuteWith(func(app *railbase.App)`, // extension seam
		`webembed.FS()`,                      // the call we care about
		`app.ServeStaticFS("/"`,              // mounted at root
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scaffolded main.go missing %q\nbody:\n%s", want, body)
		}
	}
}

// TestInit_YamlTemplateDefaultsToCorrectPort proves the railbase.yaml
// the scaffold writes matches the binary's actual default port (:8095).
// A regression here would mean the operator runs `./mydemo serve`,
// reads from `:8090` in the yaml, and hits "connection refused"
// because the listener is on `:8095`.
//
// FEEDBACK.md #10.
func TestInit_YamlTemplateDefaultsToCorrectPort(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	_, err := Init(Options{
		ProjectDir:      dir,
		RailbaseVersion: "v0.4.1",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	yaml, err := os.ReadFile(filepath.Join(dir, "railbase.yaml"))
	if err != nil {
		t.Fatalf("read railbase.yaml: %v", err)
	}
	body := string(yaml)
	if !strings.Contains(body, `addr: ":8095"`) {
		t.Errorf("railbase.yaml missing `addr: \":8095\"`:\n%s", body)
	}
	// And the legacy port must NOT leak in as an active config line.
	// (`:8090` may legitimately appear in the comment block as
	// "PocketBase uses :8090" — assert it's NOT on a yaml addr key.)
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "addr:") && strings.Contains(trim, "8090") {
			t.Errorf("yaml has an active `addr:` line bound to :8090: %q", line)
		}
	}
}
