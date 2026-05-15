// Package cli is the cobra command set shared by the bare `railbase`
// binary and the project binary that `railbase init` scaffolds for
// the user.
//
// The split exists because both binaries want the same `serve`,
// `migrate diff/up/down/status`, and `version` surface — only the
// `init` command is exclusive to the bare binary (you don't init
// a project from inside another project).
//
// Calling Execute from main() is all a binary needs to do; cobra
// reads os.Args, dispatches, and exits. ExecuteWithInit additionally
// wires up the init command for the bare-binary case.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/railbase/railbase/pkg/railbase"
	"github.com/spf13/cobra"
)

// railbaseAppAlias re-exposes pkg/railbase.App under a local name so
// the cli package can publish `cli.App` as a re-export without an
// import cycle. The blank identifier on the package use ensures Go
// keeps the import even when no other line in this file dereferences
// it — the type alias below is the user-facing surface.
type railbaseAppAlias = railbase.App

// Execute runs the CLI without the `init` subcommand. Use from a
// user project binary.
func Execute() {
	run(newRoot(false))
}

// ExecuteWith is Execute + a setup callback that lets the user
// register custom routes / hooks / static mounts on the App BEFORE
// it starts serving traffic. The CLI handles config loading + App
// construction; the callback receives the live *App.
//
// Canonical userland shape:
//
//	package main
//
//	import (
//	    _ "myapp/schema"
//	    "myapp/cpm"
//	    "github.com/railbase/railbase/pkg/railbase"
//	    "github.com/railbase/railbase/pkg/railbase/cli"
//	)
//
//	func main() {
//	    cli.ExecuteWith(func(app *railbase.App) {
//	        app.OnBeforeServe(func(r chi.Router) {
//	            r.Get("/api/cpm/{projectId}", cpm.Compute(app.Pool()))
//	        })
//	    })
//	}
//
// The callback fires for `serve` AND `dev`; other subcommands
// (migrate, generate, audit verify, …) skip it — they don't run the
// HTTP server, so route hooks would be no-ops. v0.4.1 — closes
// Sentinel FEEDBACK.md #1, where Execute() was the only public entry
// point and there was no hook for custom-route registration.
func ExecuteWith(setup func(app *App)) {
	appSetup = setup
	run(newRoot(false))
}

// ExecuteWithInit runs the CLI including `init`. The bare railbase
// binary uses this; user project binaries should not.
func ExecuteWithInit() {
	run(newRoot(true))
}

// App is a re-export of `pkg/railbase.App` for the ExecuteWith
// callback signature. Lets userland binaries register hooks without
// importing pkg/railbase explicitly (the `cli` import already covers
// what they need).
type App = railbaseAppAlias

// appSetup is the registered callback (or nil). Set by ExecuteWith;
// invoked from the `serve`/`dev` runner after App construction.
var appSetup func(*App)

func run(root *cobra.Command) {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := root.ExecuteContext(ctx); err != nil {
		// SilenceErrors is on, so cobra hasn't printed anything.
		// Treat ctx-cancel as a clean exit so SIGTERM doesn't return
		// non-zero from a graceful shutdown.
		if errors.Is(err, context.Canceled) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRoot(includeInit bool) *cobra.Command {
	// Use the actual binary basename — for the user's `./mydemo`
	// help text, cobra shows "mydemo serve" instead of "railbase
	// serve" which would be misleading.
	use := "railbase"
	if exe, err := os.Executable(); err == nil {
		use = filepath.Base(exe)
	}

	root := &cobra.Command{
		Use:           use,
		Short:         "Railbase: PocketBase-class Go backend on PostgreSQL",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       buildinfo.String(),
	}
	root.AddCommand(
		newServeCmd(),
		// v3.x — unified dev command (backend + frontend in one
		// ^C lifecycle). Closes the Sentinel-style dev.sh
		// orchestration papercut. See cli/dev.go.
		newDevCmd(),
		// v0.4.2 — single-binary build orchestrator (SPA + Go binary).
		// Symmetric to `dev`, but for the production output path.
		// Closes Sentinel FEEDBACK.md G3. See cli/build.go.
		newBuildCmd(),
		newVersionCmd(),
		newMigrateCmd(),
		newAdminCmd(),
		newTenantCmd(),
		newConfigCmd(),
		newAuditCmd(),
		newGenerateCmd(),
		newMailerCmd(),
		newAuthCmd(),
		// v1.7.51 — SCIM 2.0 bearer credentials for inbound provisioning
		// from external IdPs (Okta / Azure AD / OneLogin / Auth0).
		// Sibling to `railbase auth token` (v1.7.3) — separate store +
		// separate token prefix (`rbsm_`) so the routes can disambiguate.
		newSCIMCmd(),
		newRoleCmd(),
		newJobsCmd(),
		newCronCmd(),
		newWebhooksCmd(),
		// v1.6.6 — XLSX / PDF export from the terminal. Operates on
		// the local DB, RBAC bypassed (operator surface).
		newExportCmd(),
		// v1.7.7 — DB dump/restore via pure-Go pgx COPY (no pg_dump
		// dep). Single-binary contract preserved.
		newBackupCmd(),
		// v1.7.8 — PocketBase schema → Railbase Go-code translator.
		// Closes the last §3.13 PB-compat item before v1 verification.
		newImportCmd(),
		// v1.7.21 — `railbase test` CLI (docs/23 §3.12.1). Thin
		// wrapper over `go test` with Railbase-flavoured flag
		// composition (--integration / --embed-pg → -tags=).
		newTestCmd(),
		// v1.7.28d — `railbase coverage` (docs/23 §3.12.7). Merges
		// a Go coverprofile and a Vitest c8 JSON into a single
		// self-contained HTML report. Closes the last §3.12 item.
		newCoverageCmd(),
		// v1.7.40 — `railbase ui` (Preact + shadcn component registry).
		// Surfaces the embedded UI kit so downstream frontend apps
		// can lift components into their own tree without an npm
		// publish step; mirrors shadcn's "copy, don't install" model
		// against an in-binary source-of-truth.
		newUICmd(),
	)
	if includeInit {
		root.AddCommand(newInitCmd())
	}
	return root
}
