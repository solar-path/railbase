package cli

import (
	"github.com/railbase/railbase/internal/config"
	"github.com/railbase/railbase/pkg/railbase"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the Railbase HTTP server",
		Long: `serve starts the HTTP server, applies system migrations,
and mounts CRUD + admin routes.

Zero-config dev mode (no env vars): boots an embedded Postgres in
./pb_data and serves on :8095. The admin UI is at http://localhost:8095/_/.

Production: set RAILBASE_DSN=postgres://... and RAILBASE_PROD=true.

Port choice: :8095 is IANA-unassigned and has no default daemon on
Linux, Windows, or macOS — including macOS AirPlay Receiver which
squats :5000 + :7000. Picked adjacent to PocketBase's :8090 so
migrating operators find it on muscle memory.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			app, err := railbase.New(cfg)
			if err != nil {
				return err
			}
			// v0.4.1 — ExecuteWith callback. Runs AFTER New (so the
			// app exists + cfg is finalised) but BEFORE Run, which
			// is the only window where OnBeforeServe registration
			// has any effect. Nil-safe — bare `railbase serve`
			// (via Execute()) leaves appSetup nil and skips.
			if appSetup != nil {
				appSetup(app)
			}
			return app.Run(cmd.Context())
		},
	}
}
