package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/railbase/railbase/internal/config"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/pool"
	"github.com/railbase/railbase/internal/logger"
)

// runtimeContext captures everything a migrate subcommand needs to
// talk to Postgres: a live pool, a logger, and the original config.
// cleanup must be invoked on exit (LIFO) so embedded postgres gets
// stopped before the pool closes.
type runtimeContext struct {
	cfg     config.Config
	log     *slog.Logger
	pool    *pool.Pool
	cleanup func()
}

// openRuntime is the shared pool-setup path. It mirrors what
// pkg/railbase/app.go does at serve-time so the migrate commands
// inherit the same `--embed-postgres` semantics.
func openRuntime(ctx context.Context) (*runtimeContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	log := logger.New(cfg.LogLevel, cfg.LogFormat, os.Stdout)

	var stops []func()
	cleanup := func() {
		// LIFO — pool first, then embedded-postgres subprocess.
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
	}

	dsn := cfg.DSN
	if cfg.EmbedPostgres {
		embedDSN, stopEmbed, err := embedded.Start(ctx, embedded.Config{
			DataDir:    cfg.DataDir,
			Production: cfg.ProductionMode,
			Log:        log,
		})
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("embedded postgres: %w", err)
		}
		dsn = embedDSN
		stops = append(stops, func() {
			if err := stopEmbed(); err != nil {
				log.Error("embedded postgres stop", "err", err)
			}
		})
	}

	// FEEDBACK loadtest #4 — CLI pool now reads the same RAILBASE_DB_*
	// env vars the serving process does. Was: bare pool.Config{DSN}
	// defaulted to max_conns=8 (GOMAXPROCS*2 minimum 4) which silently
	// limited bulk-load workloads even when operator set
	// RAILBASE_DB_MAX_CONNS=300.
	p, err := pool.New(ctx, pool.Config{
		DSN:              dsn,
		MaxConns:         cfg.DBMaxConns,
		MinConns:         cfg.DBMinConns,
		MaxConnLifetime:  cfg.DBMaxConnLifetime,
		MaxConnIdleTime:  cfg.DBMaxConnIdleTime,
		StatementTimeout: cfg.DBStatementTimeout,
	}, log)
	if err != nil {
		cleanup()
		return nil, err
	}
	stops = append(stops, p.Close)

	return &runtimeContext{cfg: cfg, log: log, pool: p, cleanup: cleanup}, nil
}
