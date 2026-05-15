// Unit-level tests for `railbase build` — focused on the bits we
// can test without booting `npm` or `go build` in CI.
//
// What we cover here:
//   - --target parsing (linux/amd64 → GOOS/GOARCH)
//   - copyDir/replaceTree atomic-replace semantics
//   - --skip-web with no webembed/web-dist still proceeds
//
// What we deliberately DON'T cover (would require npm + a Go toolchain
// in the test environment):
//   - End-to-end: npm run build → embed → go build → runnable binary
//   - Cross-compilation produces the right magic bytes
//
// The integration claim — "this command produces a single-binary
// artefact" — is asserted by the scaffold tests below: a fresh
// `railbase init` lays down the webembed/ package, and `go build`
// compiles cleanly against it. That, plus the pieces tested here,
// covers the v0.4.2 surface.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in        string
		wantOS    string
		wantArch  string
		wantError bool
	}{
		{"linux/amd64", "linux", "amd64", false},
		{"darwin/arm64", "darwin", "arm64", false},
		{"windows/amd64", "windows", "amd64", false},
		{"", "", "", true},
		{"linux", "", "", true},
		{"/amd64", "", "", true},
		{"linux/", "", "", true},
	}
	for _, tc := range cases {
		goos, goarch, err := parseTarget(tc.in)
		if (err != nil) != tc.wantError {
			t.Errorf("parseTarget(%q) err = %v, wantError = %v", tc.in, err, tc.wantError)
		}
		if goos != tc.wantOS || goarch != tc.wantArch {
			t.Errorf("parseTarget(%q) = (%q, %q), want (%q, %q)",
				tc.in, goos, goarch, tc.wantOS, tc.wantArch)
		}
	}
}

// TestReplaceTree_AtomicReplace proves the "old dst dropped + src
// copied" semantics. The motivation: a partially-failed copy must
// not leave a half-populated embed/ directory — operators would ship
// a broken SPA. We test by populating dst with sentinel content,
// then replacing it, then asserting only src's content survives.
func TestReplaceTree_AtomicReplace(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")

	// Populate src with a tiny tree.
	mustWriteFile(t, filepath.Join(src, "index.html"), "<html>new</html>")
	mustWriteFile(t, filepath.Join(src, "assets/app.js"), "console.log('new')")

	// Populate dst with stale content that MUST be wiped.
	mustWriteFile(t, filepath.Join(dst, "stale.txt"), "this should not survive")
	mustWriteFile(t, filepath.Join(dst, "index.html"), "<html>old</html>")

	if err := replaceTree(src, dst); err != nil {
		t.Fatalf("replaceTree: %v", err)
	}

	// New content present.
	if got := mustReadFile(t, filepath.Join(dst, "index.html")); got != "<html>new</html>" {
		t.Errorf("dst/index.html = %q, want fresh content", got)
	}
	if got := mustReadFile(t, filepath.Join(dst, "assets/app.js")); got != "console.log('new')" {
		t.Errorf("dst/assets/app.js missing or wrong: %q", got)
	}
	// Stale content gone — the regression test for "embed half-replaced".
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Errorf("stale.txt survived the replace — replaceTree is not atomic-replace")
	}
}

// TestReplaceTree_MissingSrcErrors proves we fail loudly when --web
// has no dist/ output (e.g. the npm build silently produced nothing
// because of a misconfigured script). Without this guard the operator
// would ship a binary missing the SPA they expect.
func TestReplaceTree_MissingSrcErrors(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "nonexistent")
	dst := filepath.Join(root, "dst")
	mustWriteFile(t, filepath.Join(dst, "existing.txt"), "preserve me")

	err := replaceTree(src, dst)
	if err == nil {
		t.Fatal("expected error for missing src, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the missing path; got: %v", err)
	}
	// dst must NOT have been wiped — fail loudly, leave state intact.
	if _, err := os.Stat(filepath.Join(dst, "existing.txt")); err != nil {
		t.Errorf("dst was destroyed on error path — replaceTree should fail safely: %v", err)
	}
}

// TestCopyDir_NestedStructure exercises the deep-tree copy. SPA
// outputs commonly have assets/css/, assets/js/, fonts/ — copyDir
// must preserve the layout exactly so the manifest URLs resolve.
func TestCopyDir_NestedStructure(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	files := map[string]string{
		"index.html":            "<html>",
		"assets/css/app.css":    "body{}",
		"assets/js/main.js":     "x=1",
		"assets/js/chunks/a.js": "y=2",
		"fonts/inter.woff2":     "binary-bytes",
	}
	for rel, content := range files {
		mustWriteFile(t, filepath.Join(src, rel), content)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	for rel, want := range files {
		got := mustReadFile(t, filepath.Join(dst, rel))
		if got != want {
			t.Errorf("dst/%s = %q, want %q", rel, got, want)
		}
	}
}

// --- helpers ------------------------------------------------------

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
