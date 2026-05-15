package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/railbase/railbase/internal/scaffold"
	"github.com/spf13/cobra"
)

// railbaseModulePath is the canonical Go import path of the
// railbase module, embedded in scaffolded go.mod / main.go.
const railbaseModulePath = "github.com/railbase/railbase"

// newInitCmd returns the `init <name>` scaffold command. Only wired
// into the bare railbase binary; user project binaries shouldn't
// scaffold themselves into a sibling directory.
func newInitCmd() *cobra.Command {
	var (
		modulePath     string
		template       string
		railbaseSource string
	)
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Scaffold a new Railbase project",
		Long: `init creates a fresh Go module with a starter schema, hooks directory,
config file, and main.go that exposes the same CLI as the railbase binary
itself. Workflow:

  $ railbase init mydemo
  $ cd mydemo
  $ go mod tidy
  $ go build ./cmd/mydemo
  $ ./mydemo migrate diff initial_schema
  $ ./mydemo migrate up
  $ ./mydemo serve --embed-postgres

The resulting binary owns its schema; ` + "`railbase init`" + ` is one-shot.

Pre-release usage: until railbase publishes versioned modules, set
` + "`--railbase-source`" + ` (or env RAILBASE_LOCAL_PATH) to your local
railbase checkout so the scaffolded go.mod can ` + "`replace`" + ` the
github.com/railbase/railbase dependency.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			projectDir, err := filepath.Abs(name)
			if err != nil {
				return err
			}

			module := modulePath
			if module == "" {
				// `name` may be a path; the module path defaults to
				// the directory's basename.
				module = filepath.Base(projectDir)
			}

			tmpl := scaffold.TemplateBasic
			if template != "" {
				tmpl = scaffold.Template(template)
			}

			// Pre-release replace directive — env var as fallback so
			// you can point at a local railbase checkout without
			// passing the flag every time.
			if railbaseSource == "" {
				railbaseSource = os.Getenv("RAILBASE_LOCAL_PATH")
			}
			if railbaseSource != "" {
				if abs, err := filepath.Abs(railbaseSource); err == nil {
					railbaseSource = abs
				}
				// FEEDBACK #12 — validate the path looks like a Go module
				// checkout BEFORE we write a go.mod that references it.
				// Without this, the scaffold succeeds and the embedder
				// hits a confusing `go mod tidy` failure later. Failing
				// here points at the actual cause: bad --railbase-source.
				if err := validateRailbaseSource(railbaseSource); err != nil {
					return err
				}
			}

			written, err := scaffold.Init(scaffold.Options{
				ProjectDir:        projectDir,
				ModulePath:        module,
				Template:          tmpl,
				RailbaseVersion:   buildinfo.Tag,
				RailbaseLocalPath: railbaseSource,
			})
			if err != nil {
				return err
			}

			projectName := filepath.Base(projectDir)
			fmt.Fprintf(os.Stdout, "scaffolded %s (%d files)\n", projectDir, len(written))
			fmt.Fprintln(os.Stdout)
			fmt.Fprintln(os.Stdout, "next steps:")
			fmt.Fprintf(os.Stdout, "  cd %s\n", name)
			fmt.Fprintln(os.Stdout, "  go mod tidy")
			fmt.Fprintf(os.Stdout, "  go build ./cmd/%s\n", projectName)
			fmt.Fprintf(os.Stdout, "  ./%s migrate diff initial_schema\n", projectName)
			fmt.Fprintf(os.Stdout, "  ./%s migrate up\n", projectName)
			fmt.Fprintf(os.Stdout, "  ./%s serve --embed-postgres\n", projectName)
			return nil
		},
	}
	cmd.Flags().StringVar(&modulePath, "module", "",
		"Go module path (default: project basename)")
	cmd.Flags().StringVar(&template, "template", "basic",
		"scaffold template: `basic` | `auth-starter` | `fullstack`")
	cmd.Flags().StringVar(&railbaseSource, "railbase-source", "",
		"absolute path to local railbase checkout; emits a `replace` directive in go.mod (or set env RAILBASE_LOCAL_PATH)")
	return cmd
}

// validateRailbaseSource checks that path looks like a Go module
// checkout — exists, is a directory, and contains a `go.mod` whose
// `module` line names the railbase canonical import path. Returns a
// targeted error pointing at the actual fix instead of letting the
// embedder hit `go mod tidy: invalid replacement directive` later.
//
// FEEDBACK #12 — the shopper hit this exactly: passed --railbase-source
// pointing at a sibling directory that had no go.mod, the scaffold
// happily wrote the replace directive, then `go mod tidy` failed with
// a generic error that didn't name the bad path.
func validateRailbaseSource(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("--railbase-source %q: directory does not exist", path)
		}
		return fmt.Errorf("--railbase-source %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--railbase-source %q: not a directory", path)
	}
	gomodPath := filepath.Join(path, "go.mod")
	body, err := os.ReadFile(gomodPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"--railbase-source %q: no go.mod at this path. "+
					"Pass an absolute path to a checked-out copy of the railbase repo "+
					"(the directory containing the top-level go.mod), or omit the flag "+
					"to use the published module version.", path)
		}
		return fmt.Errorf("--railbase-source %q: read go.mod: %w", path, err)
	}
	// Quick sanity — first `module <path>` line should name the
	// canonical railbase module. Catches the case where someone
	// passes a sibling Go project by mistake.
	if !containsModuleLine(body, railbaseModulePath) {
		return fmt.Errorf(
			"--railbase-source %q: go.mod is not the railbase module (expected `module %s`). "+
				"Pass the path to a railbase checkout, not a sibling project.",
			path, railbaseModulePath)
	}
	return nil
}

// containsModuleLine returns true iff `body` contains a top-level
// `module <want>` declaration. Tolerant of leading whitespace and
// trailing comments but does NOT parse the full go.mod grammar — the
// quick prefix check is enough to catch the wrong-directory mistake.
func containsModuleLine(body []byte, want string) bool {
	for _, raw := range splitLines(body) {
		line := trimSpace(raw)
		if len(line) == 0 || line[0] == '/' {
			continue
		}
		const prefix = "module "
		if !hasPrefix(line, prefix) {
			continue
		}
		rest := trimSpace(line[len(prefix):])
		// Strip inline `//` comment, if any. Look for the `//` token
		// — single `/` characters are legal inside module paths
		// (`github.com/foo/bar`) and must be preserved.
		if i := indexDoubleSlash(rest); i >= 0 {
			rest = trimSpace(rest[:i])
		}
		// Tolerate quoted module paths.
		got := trimQuote(rest)
		return got == want
		// first module line wins — bail
	}
	return false
}

// Tiny string helpers — avoiding the strings package import keeps this
// file's dependency surface unchanged.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
func hasPrefix(b []byte, p string) bool {
	if len(b) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if b[i] != p[i] {
			return false
		}
	}
	return true
}
func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
func indexDoubleSlash(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '/' && b[i+1] == '/' {
			return i
		}
	}
	return -1
}
func trimQuote(b []byte) string {
	if len(b) >= 2 && (b[0] == '"' && b[len(b)-1] == '"') {
		return string(b[1 : len(b)-1])
	}
	return string(b)
}
