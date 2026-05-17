package cli

import (
	"github.com/railbase/railbase/internal/config"
	"github.com/railbase/railbase/pkg/railbase"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	// FEEDBACK loadtest #11 — convenience flags layered on top of env
	// vars. Flag value > env value > default. Embedders running ad-hoc
	// `railbase serve --addr :8195 --data-dir /tmp/x` no longer need
	// to export env vars for one-off runs.
	var (
		addr     string
		dataDir  string
		dsn      string
		logLevel string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Railbase HTTP server",
		Long: `serve starts the HTTP server, applies system migrations,
and mounts CRUD + admin routes.

Zero-config dev mode (no env vars): boots an embedded Postgres in
./pb_data and serves on :8095. The admin UI is at http://localhost:8095/_/.

Production: set RAILBASE_DSN=postgres://... and RAILBASE_PROD=true.

Convenience flags (FEEDBACK loadtest #11) override env values for ad-hoc
runs:
  railbase serve --addr :8195 --data-dir /tmp/run1 --log-level debug

Port choice: :8095 is IANA-unassigned and has no default daemon on
Linux, Windows, or macOS — including macOS AirPlay Receiver which
squats :5000 + :7000. Picked adjacent to PocketBase's :8090 so
migrating operators find it on muscle memory.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// Apply convenience flags AFTER config.Load so flags
			// override env, which in turn overrode defaults.
			if cmd.Flags().Changed("addr") {
				cfg.HTTPAddr = addr
			}
			if cmd.Flags().Changed("data-dir") {
				cfg.DataDir = dataDir
			}
			if cmd.Flags().Changed("dsn") {
				cfg.DSN = dsn
			}
			if cmd.Flags().Changed("log-level") {
				cfg.LogLevel = logLevel
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
	cmd.Flags().StringVar(&addr, "addr", "",
		"Bind address override (e.g. :8195). Default: $RAILBASE_HTTP_ADDR or :8095.")
	cmd.Flags().StringVar(&dataDir, "data-dir", "",
		"Data directory override. Default: $RAILBASE_DATA_DIR or ./pb_data.")
	cmd.Flags().StringVar(&dsn, "dsn", "",
		"Postgres DSN override. Default: $RAILBASE_DSN or embedded PG.")
	cmd.Flags().StringVar(&logLevel, "log-level", "",
		"Log level (debug, info, warn, error). Default: $RAILBASE_LOG_LEVEL or info.")
	return cmd
}
