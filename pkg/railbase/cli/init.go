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
		"scaffold template (v0.2: only `basic`)")
	cmd.Flags().StringVar(&railbaseSource, "railbase-source", "",
		"absolute path to local railbase checkout; emits a `replace` directive in go.mod (or set env RAILBASE_LOCAL_PATH)")
	return cmd
}
