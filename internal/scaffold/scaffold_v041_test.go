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

// TestInit_AuthStarter_OverlaysBasic — proves the auth-starter
// template walks basic FIRST (inherits go.mod, schema, webembed) and
// THEN overlays its own files (web/, package.json, src/pages, ...).
// Without the chain walk, the user would lose the basic files and
// the project wouldn't compile.
func TestInit_AuthStarter_OverlaysBasic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	_, err := Init(Options{
		ProjectDir:      dir,
		Template:        TemplateAuthStarter,
		RailbaseVersion: "v0.4.3",
	})
	if err != nil {
		t.Fatalf("Init auth-starter: %v", err)
	}
	// Files from BASIC must be present.
	for _, p := range []string{
		"go.mod",
		"railbase.yaml",
		"webembed/embed.go",
		"webembed/web-dist/README.txt",
		"cmd/demo/main.go",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("auth-starter scaffold missing basic-template file %q: %v", p, err)
		}
	}
	// Files from auth-starter overlay must be present.
	for _, p := range []string{
		"web/package.json",
		"web/vite.config.ts",
		"web/index.html",
		"web/tsconfig.json",
		"web/src/main.tsx",
		"web/src/app.tsx",
		"web/src/api.ts",
		"web/src/auth.ts",
		"web/src/lib/ui.tsx",
		"web/src/pages/login.tsx",
		"web/src/pages/account.tsx",
		"web/src/pages/profile.tsx",
		"web/src/pages/security.tsx",
		"web/src/pages/appearance.tsx",
		"web/src/_generated/README.txt",
		"web/.gitignore",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("auth-starter overlay missing %q: %v", p, err)
		}
	}
}

// TestInit_AuthStarter_TemplatesRenderProjectName — the .tmpl files
// in auth-starter use {{.ProjectName}}; the scaffold must run them
// through text/template not just copy verbatim.
func TestInit_AuthStarter_TemplatesRenderProjectName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myapp")
	if _, err := Init(Options{
		ProjectDir:      dir,
		Template:        TemplateAuthStarter,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pkg, err := os.ReadFile(filepath.Join(dir, "web/package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	body := string(pkg)
	if !strings.Contains(body, `"name": "myapp-web"`) {
		t.Errorf("package.json template didn't expand ProjectName:\n%s", body)
	}
	idx, err := os.ReadFile(filepath.Join(dir, "web/index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(idx), "<title>myapp</title>") {
		t.Errorf("index.html didn't expand ProjectName: %s", idx)
	}
}

// TestInit_AuthStarter_PagesReferenceGeneratedSDK — guarantees the
// pages talk to the SDK via the generated module path, not by
// duplicating an API client. Catches a regression where someone
// pastes a hard-coded fetch() back into the pages.
func TestInit_AuthStarter_PagesReferenceGeneratedSDK(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := Init(Options{
		ProjectDir:      dir,
		Template:        TemplateAuthStarter,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	api, err := os.ReadFile(filepath.Join(dir, "web/src/api.ts"))
	if err != nil {
		t.Fatalf("read api.ts: %v", err)
	}
	body := string(api)
	if !strings.Contains(body, `from "./_generated/index.js"`) {
		t.Errorf("api.ts should import from generated SDK, got:\n%s", body)
	}
	if !strings.Contains(body, "createRailbaseClient") {
		t.Errorf("api.ts missing createRailbaseClient call:\n%s", body)
	}
	// security.tsx uses rb.account.* — proves the page lands on the
	// v0.4.3 account namespace.
	sec, _ := os.ReadFile(filepath.Join(dir, "web/src/pages/security.tsx"))
	for _, want := range []string{
		"rb.account.changePassword",
		"rb.account.listSessions",
		"rb.account.revokeSession",
		"rb.account.revokeOtherSessions",
		"rb.account.twoFAStatus",
	} {
		if !strings.Contains(string(sec), want) {
			t.Errorf("security.tsx missing %q\n%s", want, sec)
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

// TestInit_Fullstack_OverlayChain — fullstack inherits from auth-starter
// (which inherits from basic) AND adds its own public/private pages.
// Verifies the embed walk picks up every layer.
func TestInit_Fullstack_OverlayChain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	_, err := Init(Options{
		ProjectDir:      dir,
		Template:        TemplateFullstack,
		RailbaseVersion: "v0.4.3",
	})
	if err != nil {
		t.Fatalf("Init fullstack: %v", err)
	}
	// Basic layer.
	for _, p := range []string{"go.mod", "railbase.yaml", "cmd/demo/main.go"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("[basic] missing %q: %v", p, err)
		}
	}
	// Auth-starter layer.
	for _, p := range []string{
		"web/package.json",
		"web/src/api.ts",
		"web/src/auth.ts",
		"web/src/pages/login.tsx",
		"web/src/pages/account.tsx",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("[auth-starter] missing %q: %v", p, err)
		}
	}
	// Fullstack layer — overlay's own files.
	for _, p := range []string{
		"web/src/app.tsx", // OVERRIDDEN by fullstack (router with public+private)
		"web/src/layouts/public.tsx",
		"web/src/layouts/private.tsx",
		"web/src/pages/public/landing.tsx",
		"web/src/pages/public/pricing.tsx",
		"web/src/pages/public/contact.tsx",
		"web/src/pages/public/docs.tsx",
		"web/src/pages/private/dashboard.tsx",
		"web/src/pages/private/tenants.tsx",
		"web/src/pages/private/tenant_settings.tsx",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("[fullstack] missing %q: %v", p, err)
		}
	}
	// Sanity: the FULLSTACK app.tsx must have replaced the auth-starter
	// version (last-write-wins). The fullstack one imports the layouts.
	app, err := os.ReadFile(filepath.Join(dir, "web/src/app.tsx"))
	if err != nil {
		t.Fatalf("read app.tsx: %v", err)
	}
	if !strings.Contains(string(app), "./layouts/public.js") {
		t.Errorf("fullstack app.tsx didn't override auth-starter app.tsx:\n%s", app)
	}
}

// TestInit_Fullstack_UIExtras — fullstack ships an extended UI kit
// alongside the auth-starter one. The 5 extra primitives (Badge,
// Alert, Spinner, EmptyState, Tabs, Table) are emitted on top of
// the 5 from auth-starter (Button, Input, Label, Card, Section)
// for 10 total — matches the "ui-kit subset" promised in docs.
func TestInit_Fullstack_UIExtras(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := Init(Options{
		ProjectDir:      dir,
		Template:        TemplateFullstack,
		RailbaseVersion: "v0.4.3",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Original 5 primitives (from auth-starter).
	ui, err := os.ReadFile(filepath.Join(dir, "web/src/lib/ui.tsx"))
	if err != nil {
		t.Fatalf("read ui.tsx: %v", err)
	}
	for _, want := range []string{"export function Button", "export function Input", "export function Label", "export function Card", "export function Section"} {
		if !strings.Contains(string(ui), want) {
			t.Errorf("ui.tsx missing %q", want)
		}
	}
	// New 5 (from fullstack overlay).
	extra, err := os.ReadFile(filepath.Join(dir, "web/src/lib/ui_extras.tsx"))
	if err != nil {
		t.Fatalf("read ui_extras.tsx: %v", err)
	}
	for _, want := range []string{
		"export function Badge",
		"export function Alert",
		"export function Spinner",
		"export function EmptyState",
		"export function Tabs",
		"export const Table",
	} {
		if !strings.Contains(string(extra), want) {
			t.Errorf("ui_extras.tsx missing %q", want)
		}
	}
}
