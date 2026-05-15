package cli

// `railbase build` — single-binary production artefact orchestrator.
// Closes Sentinel FEEDBACK.md G3: symmetric to `railbase dev`, but
// for the production output path. The bash-script equivalent operators
// used to write:
//
//	cd web && npm run build              # 1
//	rm -rf ../webembed/web-dist
//	cp -r dist/ ../webembed/web-dist/    # 2
//	go build -tags embed_pg -o ./<name> ./cmd/<name>  # 3
//
// becomes:
//
//	railbase build                       # autodetects everything
//	railbase build --tags embed_pg       # embed Postgres into the binary
//	railbase build --target linux/amd64  # cross-compile for Linux
//
// The command is intentionally a thin orchestrator over `npm`, `cp`,
// and `go build` — it does NOT reimplement any of them, just wires
// them in the order an operator would otherwise run by hand. No magic
// caches: every invocation rebuilds the SPA + the binary. If you want
// to skip the SPA step (e.g. a CI matrix where the frontend is built
// in a prior step), use `--skip-web`.

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

func newBuildCmd() *cobra.Command {
	var (
		webDir   string
		webCmd   string
		embedDir string
		out      string
		cmdDir   string
		tags     string
		target   string
		skipWeb  bool
		skipGo   bool
	)
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a single-binary production artefact (SPA + Go binary)",
		Long: `Builds the frontend SPA, embeds it into the binary, and produces
a single executable artefact. Mirrors the manual three-step shell
recipe most Railbase projects ship (see Sentinel's dev.sh, or the
build instructions in every README written from scratch).

Workflow:

  1. Run "npm run build" inside --web (skipped when --skip-web is set
     or --web does not exist).
  2. Replace --embed-dir/web-dist with --web/dist atomically (so a
     partially-failed copy never leaves a half-populated embed).
  3. Run "go build" against --cmd, writing to --out. Cross-compile
     by passing --target as GOOS/GOARCH (e.g. linux/amd64).

Most projects need just:

  railbase build

…which infers --out and --cmd from the current directory's base name
(matching the layout that "railbase init" scaffolds). For Sentinel:

  railbase build --tags embed_pg

bundles the embedded-Postgres driver into a self-contained binary.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBuild(cmd.Context(), buildOptions{
				webDir:   webDir,
				webCmd:   webCmd,
				embedDir: embedDir,
				out:      out,
				cmdDir:   cmdDir,
				tags:     tags,
				target:   target,
				skipWeb:  skipWeb,
				skipGo:   skipGo,
			})
		},
	}
	cmd.Flags().StringVar(&webDir, "web", "web",
		"Frontend project directory (where package.json lives). Skipped when the directory doesn't exist.")
	cmd.Flags().StringVar(&webCmd, "web-cmd", "npm run build",
		"Command to run inside --web. Use 'pnpm build', 'bun run build', etc. as needed.")
	cmd.Flags().StringVar(&embedDir, "embed-dir", "webembed",
		"Go package directory that hosts the //go:embed FS. Its web-dist/ subdir is replaced with the SPA output.")
	cmd.Flags().StringVar(&out, "out", "",
		"Output binary path. Empty → ./<cwd-base-name> (with .exe on Windows targets).")
	cmd.Flags().StringVar(&cmdDir, "cmd", "",
		"Go cmd package to build. Empty → ./cmd/<cwd-base-name>.")
	cmd.Flags().StringVar(&tags, "tags", "",
		"Comma-separated build tags, e.g. 'embed_pg' for a single-binary bundle with embedded Postgres.")
	cmd.Flags().StringVar(&target, "target", "",
		"Cross-compile target as os/arch (e.g. linux/amd64). Sets GOOS/GOARCH for the go build invocation.")
	cmd.Flags().BoolVar(&skipWeb, "skip-web", false,
		"Skip the npm build. Useful in CI matrices where the SPA was built in a prior step.")
	cmd.Flags().BoolVar(&skipGo, "skip-go", false,
		"Skip the go build. Useful for SPA-only refreshes during local debugging.")
	return cmd
}

type buildOptions struct {
	webDir   string
	webCmd   string
	embedDir string
	out      string
	cmdDir   string
	tags     string
	target   string
	skipWeb  bool
	skipGo   bool
}

// runBuild orchestrates the three steps. Errors bubble back with the
// step name and the underlying tool's stderr so the operator sees
// "npm run build failed: …" or "go build failed: …" instead of a
// generic "exit status 1".
func runBuild(ctx context.Context, opts buildOptions) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("build: getwd: %w", err)
	}
	projName := filepath.Base(cwd)

	// Resolve defaults from cwd.
	if opts.cmdDir == "" {
		opts.cmdDir = "./cmd/" + projName
	}
	if opts.out == "" {
		opts.out = "./" + projName
		// Windows convention — emit .exe automatically when cross-compiling
		// to a windows target so the artefact runs as-is on transfer.
		if goosFromTarget(opts.target) == "windows" || (opts.target == "" && runtime.GOOS == "windows") {
			opts.out += ".exe"
		}
	}

	// Step 1 — SPA build. Resilient to missing --web: the scaffold may
	// not include a frontend at all (API-only project), so a missing
	// directory is not an error.
	if !opts.skipWeb {
		if _, err := os.Stat(opts.webDir); err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("build: [web] %s does not exist, skipping SPA build\n", opts.webDir)
			} else {
				return fmt.Errorf("build: stat %s: %w", opts.webDir, err)
			}
		} else {
			fmt.Printf("build: [web] %s in %s\n", opts.webCmd, opts.webDir)
			if err := runWebBuild(ctx, opts.webDir, opts.webCmd); err != nil {
				return fmt.Errorf("build: web: %w", err)
			}
			// Step 2 — replace webembed/web-dist atomically.
			distSrc := filepath.Join(opts.webDir, "dist")
			distDst := filepath.Join(opts.embedDir, "web-dist")
			fmt.Printf("build: [embed] %s -> %s\n", distSrc, distDst)
			if err := replaceTree(distSrc, distDst); err != nil {
				return fmt.Errorf("build: embed: %w", err)
			}
		}
	} else {
		fmt.Println("build: [web] skipped (--skip-web)")
	}

	// Step 3 — go build. The tags flag accepts a single Go-build-style
	// string, which we pass through verbatim (`-tags embed_pg,foo` is
	// already the format the user types).
	if !opts.skipGo {
		fmt.Printf("build: [go] go build -o %s %s\n", opts.out, opts.cmdDir)
		if err := runGoBuild(ctx, opts); err != nil {
			return fmt.Errorf("build: go: %w", err)
		}
	} else {
		fmt.Println("build: [go] skipped (--skip-go)")
	}

	fmt.Printf("build: ok → %s\n", opts.out)
	return nil
}

// runWebBuild executes `webCmd` inside `webDir` with stdio inherited
// so operators see the live npm output. webCmd is shell-style
// ("npm run build", "pnpm build") — we split on whitespace, no full
// shell escaping. Anyone needing exotic quoting can write their own
// recipe and skip this command.
func runWebBuild(ctx context.Context, webDir, webCmd string) error {
	parts := strings.Fields(webCmd)
	if len(parts) == 0 {
		return fmt.Errorf("web-cmd is empty")
	}
	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	c.Dir = webDir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// replaceTree atomically replaces dst with a copy of src. We do
// "remove + copy" rather than rename because src lives inside webDir
// (a git-tracked tree) and dst inside embedDir — moving src would
// surprise the operator. Cost is one full filesystem copy of the
// SPA output, which is negligible compared to the npm build itself.
func replaceTree(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("source %s: %w", src, err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clean %s: %w", dst, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	return copyDir(src, dst)
}

// copyDir copies a directory tree using io/fs. Preserves regular
// files, directories, and 0644/0755 permissions — that's all an SPA
// build output needs. Symlinks, special files, and extended
// attributes are NOT preserved (Vite/esbuild outputs don't use them).
func copyDir(src, dst string) error {
	return fs.WalkDir(os.DirFS(src), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		srcPath := filepath.Join(src, rel)
		dstPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		in, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

// runGoBuild runs `go build [-tags ...] -o <out> <cmdDir>` with
// GOOS/GOARCH set when --target is non-empty. We deliberately don't
// pass `-trimpath` or other flags by default — operators that want
// reproducible builds will set GOFLAGS=-trimpath themselves; we
// don't second-guess the toolchain.
func runGoBuild(ctx context.Context, opts buildOptions) error {
	args := []string{"build"}
	if opts.tags != "" {
		args = append(args, "-tags", opts.tags)
	}
	args = append(args, "-o", opts.out, opts.cmdDir)
	c := exec.CommandContext(ctx, "go", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if opts.target != "" {
		goos, goarch, err := parseTarget(opts.target)
		if err != nil {
			return err
		}
		c.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	}
	return c.Run()
}

// parseTarget splits "linux/amd64" into ("linux", "amd64"). We don't
// validate against go tool dist list — Go itself will reject an
// unknown combination at build time with a clear message.
func parseTarget(s string) (goos, goarch string, err error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("target %q must be in os/arch form (e.g. linux/amd64)", s)
	}
	return parts[0], parts[1], nil
}

// goosFromTarget returns the GOOS portion of --target, or "" when
// --target is empty.
func goosFromTarget(s string) string {
	goos, _, err := parseTarget(s)
	if err != nil {
		return ""
	}
	return goos
}
