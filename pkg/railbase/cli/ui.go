package cli

// `railbase ui` — operator-facing surface over the embedded
// shadcn-on-Preact component registry.
//
// Why a CLI when /api/_ui/* already serves the same source: the CLI
// shells out without an HTTP round-trip, so an operator can scaffold a
// new frontend offline. It also handles the multi-file copy
// atomically (init.css + cn.ts + _primitives/* + selected components +
// every transitive local sibling) which is awkward to do with curl.
//
// Subcommands:
//   railbase ui list                    — show available components
//   railbase ui peers                   — print `npm install ...` for the kit
//   railbase ui init [--out DIR]        — scaffold styles.css + cn.ts + _primitives/
//   railbase ui add NAME...             — copy specific components (resolves deps)
//   railbase ui add --all               — copy everything

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	adminui "github.com/railbase/railbase/admin"
	"github.com/railbase/railbase/internal/api/uiapi"
	"github.com/spf13/cobra"
)

// newUICmd assembles the `ui` command tree. We register the embed FS
// on construction (not lazily inside each Run) because cobra calls
// these in PreRun order and the registry's sync.Once would otherwise
// fire against a nil FS if the user typed `railbase ui add` before
// any other command warmed the registry.
func newUICmd() *cobra.Command {
	uiapi.SetFS(adminui.UIKit())

	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Share Railbase's shadcn-on-Preact component kit with frontend apps",
		Long: "The Railbase binary embeds a Preact 10 port of shadcn/ui. " +
			"This subcommand copies the source files into a downstream " +
			"frontend project the same way the shadcn CLI works against " +
			"shadcn.com — except the source is local, so it works offline.",
	}
	cmd.AddCommand(
		newUIListCmd(),
		newUIPeersCmd(),
		newUIInitCmd(),
		newUIAddCmd(),
	)
	return cmd
}

func newUIListCmd() *cobra.Command {
	var withPeers bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available components",
		RunE: func(_ *cobra.Command, _ []string) error {
			m := uiapi.Snapshot()
			for _, c := range m.Components {
				if withPeers && len(c.Peers) > 0 {
					fmt.Printf("%-20s  peers: %s\n", c.Name, strings.Join(c.Peers, ", "))
				} else {
					fmt.Println(c.Name)
				}
			}
			fmt.Fprintf(os.Stderr, "\n%d components, %d primitives\n",
				len(m.Components), len(m.Primitives))
			return nil
		},
	}
	cmd.Flags().BoolVar(&withPeers, "with-peers", false,
		"print peer-dep list alongside each component name")
	return cmd
}

func newUIPeersCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "peers",
		Short: "Print the npm install line for every peer dep the kit uses",
		RunE: func(_ *cobra.Command, _ []string) error {
			peers := uiapi.Snapshot().Peers
			if jsonOut {
				fmt.Println("[\"" + strings.Join(peers, "\",\"") + "\"]")
				return nil
			}
			fmt.Println("npm install " + strings.Join(peers, " "))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON array instead of npm line")
	return cmd
}

// newUIInitCmd writes the foundation files (styles.css, cn.ts, every
// _primitives/* file) into the target tree. Subsequent `ui add` calls
// assume init has already run.
func newUIInitCmd() *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold styles.css + cn.ts + _primitives/ into a frontend project",
		RunE: func(_ *cobra.Command, _ []string) error {
			m := uiapi.Snapshot()
			root := outDir
			if root == "" {
				root = "."
			}

			// 1) src/styles.css — only if absent. Operators usually own
			// their global CSS; we never clobber.
			stylesPath := filepath.Join(root, "src", "styles.css")
			if err := writeIfAbsent(stylesPath, m.Styles); err != nil {
				return err
			}

			// 2) src/lib/ui/{cn,icons,theme,index}.{ts,tsx}.
			//    The whole kit-base set ships in one pass; without
			//    icons.tsx a half the components fail to compile.
			for _, base := range m.KitBase {
				dst := filepath.Join(root, base.File)
				if err := writeOverwrite(dst, base.Source); err != nil {
					return err
				}
			}

			// 3) src/lib/ui/_primitives/*.
			for _, p := range m.Primitives {
				body, ok := uiapi.LookupPrimitive(p.Name)
				if !ok {
					continue
				}
				dst := filepath.Join(root, p.File) // p.File = src/lib/ui/_primitives/<name>.tsx
				if err := writeOverwrite(dst, body.Source); err != nil {
					return err
				}
			}

			// 4) Onboarding hints to stderr (so a `> log.txt` redirect
			// captures the file list, not the prose).
			fmt.Fprintf(os.Stderr, "\nFoundation written under %s/. Peers to install:\n  npm install %s\n",
				root, strings.Join(m.Peers, " "))
			fmt.Fprintf(os.Stderr, "\nNext: railbase ui add button card input  (or --all)\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "", "target project root (defaults to current directory)")
	return cmd
}

// newUIAddCmd resolves transitive local dependencies and copies the
// chosen components. With --all, ignores the positional name args and
// emits every component.
func newUIAddCmd() *cobra.Command {
	var (
		outDir string
		all    bool
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "add NAME...",
		Short: "Copy a component (and its local deps) into a frontend project",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				return nil
			}
			if len(args) == 0 {
				return fmt.Errorf("at least one component name required (or use --all)")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			m := uiapi.Snapshot()
			root := outDir
			if root == "" {
				root = "."
			}
			want := map[string]struct{}{}
			if all {
				for _, c := range m.Components {
					want[c.Name] = struct{}{}
				}
			} else {
				for _, a := range args {
					want[a] = struct{}{}
				}
			}

			// Resolve transitive locals — Local[] on each Component
			// references sibling .ui.tsx files; walk until the set is
			// closed. Bounded by the registry size so no infinite-loop
			// risk even if someone introduces a cycle (won't compile
			// either way).
			resolved := map[string]uiapi.Component{}
			toVisit := keys(want)
			for len(toVisit) > 0 {
				name := toVisit[0]
				toVisit = toVisit[1:]
				if _, done := resolved[name]; done {
					continue
				}
				c, ok := uiapi.LookupComponent(name)
				if !ok {
					return fmt.Errorf("unknown component %q (try `railbase ui list`)", name)
				}
				resolved[name] = c
				for _, dep := range c.Local {
					toVisit = append(toVisit, dep)
				}
			}

			// Stable copy order for readable output.
			names := keys(resolved)
			sort.Strings(names)

			// We require styles.css + cn.ts + _primitives/ to already
			// be present. If cn.ts is missing, hint the operator —
			// `ui init` is the right command to run first.
			cnPath := filepath.Join(root, "src", "lib", "ui", "cn.ts")
			if _, err := os.Stat(cnPath); err != nil {
				return fmt.Errorf("%s missing — run `railbase ui init --out %s` first", cnPath, root)
			}

			peerSet := map[string]struct{}{}
			for _, name := range names {
				c := resolved[name]
				dst := filepath.Join(root, c.File)
				if !force {
					if _, err := os.Stat(dst); err == nil {
						fmt.Fprintf(os.Stderr, "skip %s (exists; --force to overwrite)\n", dst)
						continue
					}
				}
				if err := writeOverwrite(dst, c.Source); err != nil {
					return err
				}
				for _, p := range c.Peers {
					peerSet[p] = struct{}{}
				}
			}

			if len(peerSet) > 0 {
				fmt.Fprintf(os.Stderr, "\nPeer deps for the chosen set:\n  npm install %s\n",
					strings.Join(sortedKeys(peerSet), " "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "", "target project root (defaults to current directory)")
	cmd.Flags().BoolVar(&all, "all", false, "add every available component")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return cmd
}

// writeIfAbsent writes file with content unless it already exists.
// Creates intermediate dirs. The "absent" rule guards against
// stomping on a project's own globals.
func writeIfAbsent(p, content string) error {
	if _, err := os.Stat(p); err == nil {
		fmt.Fprintf(os.Stderr, "skip %s (exists)\n", p)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", p)
	return nil
}

// writeOverwrite always writes. Used for files we own (cn.ts,
// _primitives/*, every .ui.tsx the operator explicitly asked for).
func writeOverwrite(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", p)
	return nil
}

// keys returns the keys of any string-keyed map. Generic so the
// resolve loop can extract names from both `map[string]struct{}` (the
// want-set) and `map[string]uiapi.Component` (the resolved-set)
// without bespoke helpers.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// sortedKeys mirrors the uiapi helper — kept here to avoid exporting
// it across package boundaries for a 4-line helper.
func sortedKeys(m map[string]struct{}) []string {
	out := keys(m)
	sort.Strings(out)
	return out
}
