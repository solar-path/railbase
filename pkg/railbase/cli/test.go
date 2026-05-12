package cli

// v1.7.21 — `railbase test` CLI (docs/23 §3.12.1).
//
// Thin wrapper over `go test` that composes flags Railbase users
// reach for often:
//
//   - --integration / --embed-pg compose into a single -tags= argv
//     without the operator manually concatenating
//   - --coverage defaults the -coverprofile path to coverage.out
//   - help text points at the testapp package so users discover the
//     fixtures harness without reading the design doc
//
// Watch mode (fsnotify) and combined Go+JS coverage from the §3.12.1
// design are deferred to v1.x — see progress.md.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// testFlags holds the parsed flag state. Kept as a struct so the
// pure argv builder can be unit-tested without round-tripping through
// cobra. RunE populates it from cobra flags then hands it off.
type testFlags struct {
	short       bool
	race        bool
	coverage    bool
	coverageOut string
	only        string
	timeout     string
	tags        string
	verbose     bool
	integration bool
	embedPG     bool
	packages    []string
}

// buildTestArgv composes the argv (sans leading "go") for `go test`.
//
// Order of -tags composition: user --tags first, then --integration,
// then --embed_pg. Order isn't load-bearing for `go test` (it parses
// the list), but a deterministic order keeps tests simpler.
func buildTestArgv(f testFlags) []string {
	argv := []string{"test"}

	if f.verbose {
		argv = append(argv, "-v")
	}
	if f.short {
		argv = append(argv, "-short")
	}
	if f.race {
		argv = append(argv, "-race")
	}
	if f.coverage {
		out := f.coverageOut
		if out == "" {
			out = "coverage.out"
		}
		argv = append(argv, "-coverprofile="+out)
	}
	if f.only != "" {
		argv = append(argv, "-run="+f.only)
	}
	if f.timeout != "" {
		argv = append(argv, "-timeout="+f.timeout)
	}

	// Compose -tags. Split user --tags on "," so we can dedupe and
	// re-join after appending the convenience flags.
	var tags []string
	if f.tags != "" {
		for _, t := range strings.Split(f.tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}
	if f.integration {
		tags = append(tags, "integration")
	}
	if f.embedPG {
		tags = append(tags, "embed_pg")
	}
	if len(tags) > 0 {
		argv = append(argv, "-tags="+strings.Join(tags, ","))
	}

	// Packages — default ./... if the operator didn't specify any.
	if len(f.packages) == 0 {
		argv = append(argv, "./...")
	} else {
		argv = append(argv, f.packages...)
	}
	return argv
}

func newTestCmd() *cobra.Command {
	var f testFlags
	cmd := &cobra.Command{
		Use:   "test [packages...]",
		Short: "Run Go tests with Railbase-flavoured flag defaults",
		Long: `Run Go tests with Railbase-flavoured flag defaults.

This is a thin wrapper over ` + "`go test`" + ` that composes the flags Railbase
users reach for most often. Anything you can do with ` + "`go test`" + ` you can
still do directly; this command exists for ergonomics, not capability.

Highlights:
  - --integration and --embed-pg compose into a single -tags= argv,
    so you don't have to remember to concatenate them.
  - --coverage defaults the -coverprofile path to coverage.out.
  - Default package set is ./..., so a bare ` + "`railbase test`" + ` runs the
    whole module.

For first-class test fixtures (embedded Postgres, schema bootstrap,
HTTP actor builders) import pkg/railbase/testapp — its package doc
walks through the harness. Tests that use testapp need the embed_pg
build tag, which --embed-pg sets for you.

Examples:
  railbase test
  railbase test ./internal/api/...
  railbase test --only TestPosts
  railbase test --coverage
  railbase test --integration --embed-pg
  railbase test --race --timeout 60s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.packages = args
			argv := buildTestArgv(f)

			c := exec.CommandContext(cmd.Context(), "go", argv...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			c.Stdin = os.Stdin
			if err := c.Run(); err != nil {
				// Bubble up `go test`'s non-zero exit so CI sees red.
				// cobra's SilenceUsage is on at root, so we won't
				// spam the test failure with a usage dump.
				if ee, ok := err.(*exec.ExitError); ok {
					return fmt.Errorf("go test exited with status %d", ee.ExitCode())
				}
				return fmt.Errorf("go test: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&f.short, "short", false, "passes -short to go test")
	cmd.Flags().BoolVar(&f.race, "race", false, "passes -race to go test")
	cmd.Flags().BoolVar(&f.coverage, "coverage", false, "passes -coverprofile=<path> to go test (default path coverage.out)")
	cmd.Flags().StringVar(&f.coverageOut, "coverage-out", "", "custom coverage file path (implies --coverage)")
	cmd.Flags().StringVar(&f.only, "only", "", "regex of test names to run (passes -run=<pattern>)")
	cmd.Flags().StringVar(&f.timeout, "timeout", "", "passes -timeout=<duration> to go test")
	cmd.Flags().StringVar(&f.tags, "tags", "", "comma-separated build tags (passes -tags=<list>)")
	cmd.Flags().BoolVar(&f.verbose, "verbose", false, "passes -v to go test")
	cmd.Flags().BoolVar(&f.integration, "integration", false, "adds 'integration' to the build tag list")
	cmd.Flags().BoolVar(&f.embedPG, "embed-pg", false, "adds 'embed_pg' to the build tag list (required by pkg/railbase/testapp)")

	// A bare --coverage-out without --coverage should still emit
	// the coverprofile — flip the boolean for the user.
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if f.coverageOut != "" {
			f.coverage = true
		}
		return nil
	}
	return cmd
}
