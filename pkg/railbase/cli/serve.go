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
./pb_data and serves on :8090. The admin UI is at http://localhost:8090/_/.

Production: set RAILBASE_DSN=postgres://... and RAILBASE_PROD=true.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			app, err := railbase.New(cfg)
			if err != nil {
				return err
			}
			return app.Run(cmd.Context())
		},
	}
}
