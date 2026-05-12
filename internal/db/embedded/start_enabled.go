//go:build embed_pg

// Real implementation, only compiled with `-tags embed_pg`.
// Adds ~50 MB of downloaded postgres binaries on first run (cached
// under <DataDir>/postgres/). Refuses to run in production mode.

package embedded

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// embedPort is fixed for now to keep the dev DSN stable across restarts.
// A future revision can probe for a free port and write it back into config.
const embedPort = 54329

func start(_ context.Context, cfg Config) (string, StopFunc, error) {
	if cfg.Production {
		return "", nil, errors.New("embedded postgres refused in production mode")
	}
	if cfg.Log == nil {
		return "", nil, errors.New("embedded postgres: Log is required")
	}
	if cfg.DataDir == "" {
		return "", nil, errors.New("embedded postgres: DataDir is required")
	}

	// IMPORTANT: keep dataPath OUTSIDE runtimePath. The library wipes
	// runtimePath on every Start (embedded_postgres.go:90 RemoveAll),
	// and dataPath defaults to <runtimePath>/data — so a single
	// RuntimePath() setup loses data on every restart. We split:
	//   <DataDir>/postgres/runtime/  → binaries (nuked + re-extracted)
	//   <DataDir>/postgres/data/     → PG cluster data (persists)
	root := filepath.Join(cfg.DataDir, "postgres")
	runtimePath := filepath.Join(root, "runtime")
	dataPath := filepath.Join(root, "data")
	for _, p := range []string{root, runtimePath, dataPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return "", nil, fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	cfg.Log.Info("starting embedded postgres",
		"runtime_path", runtimePath,
		"data_path", dataPath,
		"port", embedPort,
	)

	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Database("railbase").
			Username("railbase").
			Password("railbase").
			Port(embedPort).
			RuntimePath(runtimePath).
			DataPath(dataPath).
			Logger(newSlogWriter(cfg.Log, slog.LevelDebug)),
	)
	if err := pg.Start(); err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}

	dsn := fmt.Sprintf(
		"postgres://railbase:railbase@127.0.0.1:%d/railbase?sslmode=disable",
		embedPort,
	)
	return dsn, pg.Stop, nil
}
