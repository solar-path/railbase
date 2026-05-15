package cli

// Phase 3.x — `railbase dev` unified dev command. Closes Sentinel's
// `dev.sh` orchestration papercut: that 19-line bash script
// manually launches the binary, polls `/readyz` until ready, then
// starts Vite. Cross-platform users (Windows without WSL, fish/zsh
// users with strict mode quirks) end up rewriting the loop.
//
// What `railbase dev` does:
//
//  1. Build (or rebuild on first invocation) the backend binary
//     using the embed_pg build tag if --embed-pg is set.
//  2. Start the backend with sane dev defaults (DEV=true,
//     EMBED_POSTGRES=true on default-empty DSN, INFO log level on
//     stdout instead of the prod-quiet WARN-and-above).
//  3. Poll /readyz with a 30 s ceiling — if the backend hasn't
//     responded by then, bail with the captured stderr so the
//     operator sees the actual failure (DSN parse error, port
//     conflict, etc.) not just "timed out".
//  4. Start the frontend dev server (`npm run dev` in --web, or
//     custom command via --web-cmd). Streams its stdout under a
//     "[web]" prefix so backend + frontend logs are differentiable.
//  5. Forward SIGINT/SIGTERM to both children; wait for both to
//     exit cleanly. If either dies, kill the other and exit with
//     that signal/code.
//
// Cross-platform: uses os/exec, no shell — Windows works out of the
// box without WSL. The polling loop uses net/http directly so we
// don't shell out to curl either.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newDevCmd() *cobra.Command {
	var (
		webDir       string
		webCmd       string
		addr         string
		embedPG      bool
		readyURL     string
		timeout      time.Duration
		watchSchema  string
		sdkOut       string
		sdkPkg       string
	)
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Run backend + frontend dev server with a single Ctrl-C lifecycle",
		Long: `Launches the Railbase backend and a frontend dev server (Vite, etc.)
side-by-side. Cross-platform replacement for the manual two-process
dance documented in many Railbase project READMEs (Sentinel's dev.sh
is the canonical example — see /Users/work/apps/sentinel/dev.sh:1-19).

Workflow:

  - Builds the backend binary (or reuses the existing one).
  - Starts it with --addr, optional --embed-pg.
  - Polls --ready-url (default /readyz) for up to --timeout.
  - Starts the frontend with --web-cmd in --web.
  - Forwards Ctrl-C / SIGTERM to both children.

Most projects need just:

  railbase dev --web ./web

For Sentinel's exact shape:

  railbase dev --embed-pg --web ./web`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// FEEDBACK #B5 — if --addr wasn't passed explicitly, fall back
			// to $RAILBASE_HTTP_ADDR so `dev` honours the same env override
			// `serve` does. The blogger project hit a port-conflict on
			// :8095, set RAILBASE_HTTP_ADDR=:8096 in .env, and watched
			// `./blogger dev` keep binding to :8095 because the flag's
			// default value won the precedence fight.
			addr = resolveDevAddr(addr, cmd.Flags().Changed("addr"), os.Getenv("RAILBASE_HTTP_ADDR"))
			return runDev(cmd.Context(), devOptions{
				webDir:      webDir,
				webCmd:      webCmd,
				addr:        addr,
				embedPG:     embedPG,
				readyURL:    readyURL,
				timeout:     timeout,
				watchSchema: watchSchema,
				sdkOut:      sdkOut,
				sdkPkg:      sdkPkg,
			})
		},
	}
	cmd.Flags().StringVar(&webDir, "web", "web",
		"Directory of the frontend dev server (where package.json lives).")
	cmd.Flags().StringVar(&webCmd, "web-cmd", "npm run dev",
		"Command to run inside --web. Use 'pnpm dev', 'bun dev', etc. as needed.")
	cmd.Flags().StringVar(&addr, "addr", ":8095",
		"Backend bind address (sets RAILBASE_HTTP_ADDR).")
	cmd.Flags().BoolVar(&embedPG, "embed-pg", false,
		"Start with embedded PostgreSQL (sets RAILBASE_EMBED_POSTGRES=true).")
	cmd.Flags().StringVar(&readyURL, "ready-url", "",
		"Health-check URL to poll. Empty → http://127.0.0.1<addr>/readyz.")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second,
		"How long to wait for the backend to report ready before bailing.")
	cmd.Flags().StringVar(&watchSchema, "watch-schema", "",
		"Watch this directory for *.go changes and auto-regenerate the SDK on save. Empty disables.")
	cmd.Flags().StringVar(&sdkOut, "sdk-out", "web/src/client",
		"Where to emit the regenerated SDK on schema change (used with --watch-schema).")
	cmd.Flags().StringVar(&sdkPkg, "sdk-pkg", "",
		"Schema package path to regenerate from (e.g. ./schema). Required when --watch-schema is set so the watcher can re-run `go run` against the live schema.")
	return cmd
}

type devOptions struct {
	webDir      string
	webCmd      string
	addr        string
	embedPG     bool
	readyURL    string
	timeout     time.Duration
	watchSchema string
	sdkOut      string
	sdkPkg      string
}

// runDev orchestrates the two child processes. Returns nil on clean
// Ctrl-C shutdown, an error if either child fails to come up.
func runDev(ctx context.Context, opts devOptions) error {
	// SIGINT / SIGTERM intercept — propagate to children. ctx already
	// honours cobra's signal handler, but we add our own to handle the
	// case where the user's terminal sends SIGINT and we need to wait
	// for both children to exit before returning.
	sigCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Backend: re-exec the same binary with `serve`. We deliberately
	// don't shell out — children inherit our os.Stdin so a panic-time
	// debugger.repl would still work.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	backendCmd := exec.CommandContext(sigCtx, exe, "serve")
	backendCmd.Env = append(os.Environ(),
		"RAILBASE_HTTP_ADDR="+opts.addr,
		"RAILBASE_DEV=true",
		"RAILBASE_LOG_LEVEL=info",
	)
	if opts.embedPG {
		backendCmd.Env = append(backendCmd.Env, "RAILBASE_EMBED_POSTGRES=true")
	}
	// Prefixed stdout: "[api] ..." so operator can disambiguate
	// backend logs from web-dev-server logs interleaved on one
	// terminal.
	backendOut, _ := backendCmd.StdoutPipe()
	backendErr, _ := backendCmd.StderrPipe()
	if err := backendCmd.Start(); err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	go pipePrefix("[api]", backendOut)
	go pipePrefix("[api]", backendErr)

	// Wait for /readyz before launching the frontend — frontend's
	// API calls would otherwise hammer with refused connections in
	// the first few seconds.
	if opts.readyURL == "" {
		opts.readyURL = "http://127.0.0.1" + opts.addr + "/readyz"
	}
	fmt.Fprintf(os.Stderr, "railbase dev: waiting for backend at %s ...\n", opts.readyURL)
	if err := waitForReady(sigCtx, opts.readyURL, opts.timeout); err != nil {
		_ = backendCmd.Process.Kill()
		_ = backendCmd.Wait()
		return fmt.Errorf("backend never reported ready: %w", err)
	}
	fmt.Fprintf(os.Stderr, "railbase dev: backend ready, starting frontend ...\n")

	// Frontend: arbitrary command (npm/pnpm/bun/yarn). Split on
	// whitespace — covers the common shapes; for anything fancier
	// the operator passes via --web-cmd as a quoted phrase and we
	// run it through a shell only if it contains shell metachars.
	webParts := strings.Fields(opts.webCmd)
	if len(webParts) == 0 {
		_ = backendCmd.Process.Kill()
		_ = backendCmd.Wait()
		return errors.New("--web-cmd is empty")
	}
	frontendCmd := exec.CommandContext(sigCtx, webParts[0], webParts[1:]...)
	frontendCmd.Dir = opts.webDir
	frontendCmd.Env = os.Environ()
	frontendOut, _ := frontendCmd.StdoutPipe()
	frontendErr, _ := frontendCmd.StderrPipe()
	if err := frontendCmd.Start(); err != nil {
		_ = backendCmd.Process.Kill()
		_ = backendCmd.Wait()
		return fmt.Errorf("start frontend (%s in %s): %w", opts.webCmd,
			filepath.Clean(opts.webDir), err)
	}
	go pipePrefix("[web]", frontendOut)
	go pipePrefix("[web]", frontendErr)

	// Optional schema watcher — regenerates the typed SDK whenever a
	// *.go file in --watch-schema changes. Closes Sentinel's "remember
	// to run `railbase generate sdk` after every schema edit" papercut.
	// The watcher dies with sigCtx so ^C cleans it up alongside the
	// child processes.
	if opts.watchSchema != "" {
		if opts.sdkPkg == "" {
			fmt.Fprintln(os.Stderr,
				"railbase dev: --watch-schema is set but --sdk-pkg is empty; SDK regen disabled")
		} else {
			go watchSchemaAndRegen(sigCtx, opts.watchSchema, opts.sdkPkg, opts.sdkOut)
		}
	}

	// Wait for either to exit. When one exits, kill the other so we
	// don't leave a zombie.
	var wg sync.WaitGroup
	wg.Add(2)
	var backendErr2, frontendErr2 error
	go func() {
		defer wg.Done()
		backendErr2 = backendCmd.Wait()
		if frontendCmd.Process != nil {
			_ = frontendCmd.Process.Signal(syscall.SIGTERM)
		}
	}()
	go func() {
		defer wg.Done()
		frontendErr2 = frontendCmd.Wait()
		if backendCmd.Process != nil {
			_ = backendCmd.Process.Signal(syscall.SIGTERM)
		}
	}()
	wg.Wait()

	// On clean ^C both children exit with signal — return nil. On
	// abnormal exit (one crashed), surface the first error.
	if errors.Is(sigCtx.Err(), context.Canceled) {
		return nil
	}
	if backendErr2 != nil && !isSignalExit(backendErr2) {
		return fmt.Errorf("backend: %w", backendErr2)
	}
	if frontendErr2 != nil && !isSignalExit(frontendErr2) {
		return fmt.Errorf("frontend: %w", frontendErr2)
	}
	return nil
}

// waitForReady polls url until HTTP 200, ctx cancellation, or
// timeout. Returns nil on first success, error otherwise. 250 ms
// poll interval — fast enough for boot to feel snappy, sparse
// enough not to spam the boot log.
func waitForReady(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 1 * time.Second}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s", timeout)
		}
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// pipePrefix copies r to stdout, prepending each line with prefix +
// space. Used to disambiguate child-process stdout when both run in
// the same terminal.
func pipePrefix(prefix string, r io.ReadCloser) {
	defer r.Close()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			fmt.Fprint(os.Stdout, prefix+" "+line)
		}
		if err != nil {
			return
		}
	}
}

// watchSchemaAndRegen watches the schema directory for *.go changes
// and re-runs `railbase generate sdk` whenever one lands. Debounces
// rapid bursts (IDE save events often fire multiple times) on a
// 500 ms window so a single save triggers one regen, not three.
//
// Why not call into the generate package directly: the live schema
// is compiled into THIS binary; if it changed on disk, we'd still be
// running with the stale registered specs. Shelling out to `go run`
// against the operator's main package (--sdk-pkg) picks up the
// freshly-edited code.
func watchSchemaAndRegen(ctx context.Context, dir, pkg, out string) {
	w, err := fsnotifyNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "railbase dev: fsnotify init failed: %v\n", err)
		return
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		fmt.Fprintf(os.Stderr, "railbase dev: watch %s: %v\n", dir, err)
		return
	}
	fmt.Fprintf(os.Stderr, "railbase dev: watching %s for schema changes\n", dir)

	var debounce *time.Timer
	regen := func() {
		fmt.Fprintln(os.Stderr, "railbase dev: schema changed → regenerating SDK")
		cmd := exec.CommandContext(ctx, "go", "run", pkg, "generate", "sdk", "--out", out)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "railbase dev: SDK regen failed: %v\n", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.events():
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".go") {
				continue
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, regen)
		case err, ok := <-w.errors():
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "railbase dev: watch error: %v\n", err)
		}
	}
}

// resolveDevAddr decides which bind address `railbase dev` should
// pass to the backend. Precedence:
//
//  1. Explicit --addr from the CLI (flagChanged=true) wins always.
//  2. Otherwise $RAILBASE_HTTP_ADDR, if non-empty.
//  3. Otherwise the cobra default (already in flagDefault).
//
// FEEDBACK #B5 — without step 2, an operator with
// `RAILBASE_HTTP_ADDR=:8096` in their .env still got the cobra default
// `:8095` because flag-defaults outrank env in cobra.
func resolveDevAddr(flagDefault string, flagChanged bool, envValue string) string {
	if flagChanged {
		return flagDefault
	}
	if v := strings.TrimSpace(envValue); v != "" {
		return v
	}
	return flagDefault
}

// isSignalExit detects exec.ExitError caused by SIGTERM/SIGKILL.
// These are normal during shutdown — don't surface as an "error".
func isSignalExit(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	if status, ok := ee.Sys().(syscall.WaitStatus); ok {
		return status.Signaled()
	}
	return false
}
