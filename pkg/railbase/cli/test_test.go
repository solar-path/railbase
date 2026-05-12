package cli

// v1.7.21 — `railbase test` CLI argv builder tests (docs/23 §3.12.1).
//
// We test the pure buildTestArgv shape rather than exec-ing `go test`
// from inside a unit test (recursive testing is a tar pit). Each case
// asserts the relevant substring(s) appear in the constructed argv —
// order between optional flags is not load-bearing for `go test`.

import (
	"strings"
	"testing"
)

// joined returns argv joined with single spaces, for substring asserts.
func joined(argv []string) string {
	return strings.Join(argv, " ")
}

// argvContains is a strings.Contains shorthand for argv-as-string asserts.
func argvContains(t *testing.T, argv []string, want string) {
	t.Helper()
	s := joined(argv)
	if !strings.Contains(s, want) {
		t.Errorf("argv missing %q\n  got: %s", want, s)
	}
}

// argEquals checks for an exact token in argv (not a substring) —
// useful where -short could otherwise be matched inside a longer flag.
func argEquals(t *testing.T, argv []string, want string) {
	t.Helper()
	for _, a := range argv {
		if a == want {
			return
		}
	}
	t.Errorf("argv missing token %q\n  got: %s", want, joined(argv))
}

// TestBuildTestArgv_Defaults: bare invocation yields `test ./...`.
func TestBuildTestArgv_Defaults(t *testing.T) {
	argv := buildTestArgv(testFlags{})
	if len(argv) < 2 {
		t.Fatalf("expected at least 2 argv entries, got %v", argv)
	}
	if argv[0] != "test" {
		t.Errorf("argv[0] = %q, want %q", argv[0], "test")
	}
	argEquals(t, argv, "./...")
	// No spurious flags on the default path.
	for _, a := range argv {
		if strings.HasPrefix(a, "-") {
			t.Errorf("unexpected flag in default argv: %s", a)
		}
	}
}

// TestBuildTestArgv_AllFlags: every flag set → every corresponding
// go-test flag is present.
func TestBuildTestArgv_AllFlags(t *testing.T) {
	argv := buildTestArgv(testFlags{
		short:       true,
		race:        true,
		coverage:    true,
		coverageOut: "custom.out",
		only:        "TestPosts",
		timeout:     "60s",
		tags:        "foo",
		verbose:     true,
	})
	argEquals(t, argv, "-short")
	argEquals(t, argv, "-race")
	argEquals(t, argv, "-v")
	argvContains(t, argv, "-coverprofile=custom.out")
	argvContains(t, argv, "-run=TestPosts")
	argvContains(t, argv, "-timeout=60s")
	argvContains(t, argv, "-tags=foo")
	argEquals(t, argv, "./...")
}

// TestBuildTestArgv_TagComposition: --integration + --embed-pg fold
// into the user --tags list as a single -tags=<csv> argv.
func TestBuildTestArgv_TagComposition(t *testing.T) {
	argv := buildTestArgv(testFlags{
		tags:        "foo",
		integration: true,
		embedPG:     true,
	})

	// Should be exactly one -tags= flag in argv.
	var tagsCount int
	var tagsArg string
	for _, a := range argv {
		if strings.HasPrefix(a, "-tags=") {
			tagsCount++
			tagsArg = a
		}
	}
	if tagsCount != 1 {
		t.Fatalf("expected exactly 1 -tags= entry, got %d (%v)", tagsCount, argv)
	}

	// Order-tolerant: every required tag is present in the csv.
	for _, want := range []string{"foo", "integration", "embed_pg"} {
		if !strings.Contains(tagsArg, want) {
			t.Errorf("-tags= missing %q (got %s)", want, tagsArg)
		}
	}
}

// TestBuildTestArgv_TagComposition_IntegrationOnly: bare --integration
// (no --tags) still produces -tags=integration.
func TestBuildTestArgv_TagComposition_IntegrationOnly(t *testing.T) {
	argv := buildTestArgv(testFlags{integration: true})
	argvContains(t, argv, "-tags=integration")
}

// TestBuildTestArgv_CoverageDefault: --coverage without --coverage-out
// defaults to coverage.out.
func TestBuildTestArgv_CoverageDefault(t *testing.T) {
	argv := buildTestArgv(testFlags{coverage: true})
	argvContains(t, argv, "-coverprofile=coverage.out")
}

// TestBuildTestArgv_CoverageCustom: --coverage-out picks the path.
func TestBuildTestArgv_CoverageCustom(t *testing.T) {
	argv := buildTestArgv(testFlags{coverage: true, coverageOut: "custom.out"})
	argvContains(t, argv, "-coverprofile=custom.out")
	// And no fallback to coverage.out leaked through.
	for _, a := range argv {
		if a == "-coverprofile=coverage.out" {
			t.Errorf("unexpected default coverage path in argv: %v", argv)
		}
	}
}

// TestBuildTestArgv_PackagesPassthrough: positional package paths
// replace the ./... default.
func TestBuildTestArgv_PackagesPassthrough(t *testing.T) {
	argv := buildTestArgv(testFlags{packages: []string{"./internal/...", "./pkg/foo"}})
	argEquals(t, argv, "./internal/...")
	argEquals(t, argv, "./pkg/foo")
	// ./... shouldn't still be there.
	for _, a := range argv {
		if a == "./..." {
			t.Errorf("default ./... should not appear when packages given: %v", argv)
		}
	}
}

// TestBuildTestArgv_TagsCommaListPreserved: a user passing multiple
// comma-separated tags survives the dedupe/rejoin pass.
func TestBuildTestArgv_TagsCommaListPreserved(t *testing.T) {
	argv := buildTestArgv(testFlags{tags: "foo,bar", integration: true})
	var tagsArg string
	for _, a := range argv {
		if strings.HasPrefix(a, "-tags=") {
			tagsArg = a
		}
	}
	for _, want := range []string{"foo", "bar", "integration"} {
		if !strings.Contains(tagsArg, want) {
			t.Errorf("-tags= missing %q (got %s)", want, tagsArg)
		}
	}
}
