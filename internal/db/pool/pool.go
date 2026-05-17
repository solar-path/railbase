// Package pool wraps pgxpool with the defaults Railbase enforces:
// PG version check on startup, sensible pool sizing, and a minimal
// surface so the rest of the codebase doesn't import pgx directly.
package pool

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MinPostgresMajor is the minimum supported PostgreSQL major version.
// Bumped together with feature usage in docs/02-architecture.md.
const MinPostgresMajor = 14

// Defaults from docs/03-data-layer.md "Connection management".
// Applied when the corresponding Config field is the zero value.
const (
	defaultMinConns          int32         = 1
	defaultMaxConnLifetime   time.Duration = 1 * time.Hour
	defaultMaxConnIdleTime   time.Duration = 30 * time.Minute
	defaultHealthCheckPeriod time.Duration = 1 * time.Minute
	// FEEDBACK loadtest #3 — see Config.StatementTimeout doc.
	defaultStatementTimeout  time.Duration = 30 * time.Second
)

// defaultMaxConns returns max(4, GOMAXPROCS*2). docs/03 spec.
func defaultMaxConns() int32 {
	n := int32(runtime.GOMAXPROCS(0)) * 2
	if n < 4 {
		n = 4
	}
	return n
}

// Config carries the pool tunables resolved from CLI/env/yaml.
// Zero values fall back to the docs/03 defaults applied by New.
type Config struct {
	DSN               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration

	// StatementTimeout — FEEDBACK loadtest #3 — server-side ceiling
	// on individual statement runtime, applied at session-init via
	// `SET statement_timeout` in AfterConnect. Default 30s. Zero
	// disables (matches Postgres default of "no limit"). A single
	// slow LIST query at 5s × 300 concurrent requests no longer
	// blocks the pool — connections clear in bounded time.
	//
	// Tighter per-route timeouts can still be set via `SET LOCAL` in
	// the handler tx.
	StatementTimeout time.Duration
}

// withDefaults fills in zero-valued fields from the docs/03 spec.
// Returned by value so callers can log the resolved values.
func (c Config) withDefaults() Config {
	if c.MaxConns <= 0 {
		c.MaxConns = defaultMaxConns()
	}
	if c.MinConns <= 0 {
		c.MinConns = defaultMinConns
	}
	if c.MaxConnLifetime <= 0 {
		c.MaxConnLifetime = defaultMaxConnLifetime
	}
	if c.MaxConnIdleTime <= 0 {
		c.MaxConnIdleTime = defaultMaxConnIdleTime
	}
	if c.HealthCheckPeriod <= 0 {
		c.HealthCheckPeriod = defaultHealthCheckPeriod
	}
	// StatementTimeout: zero → default 30s; negative → "disable"
	// sentinel (env RAILBASE_DB_STATEMENT_TIMEOUT=off threads -1 here).
	// We convert negative to zero before pgx receives it so the
	// AfterConnect hook below skips applying the SET entirely.
	if c.StatementTimeout == 0 {
		c.StatementTimeout = defaultStatementTimeout
	}
	if c.StatementTimeout < 0 {
		c.StatementTimeout = 0
	}
	return c
}

// Pool is the project-wide handle to PostgreSQL.
// Embeds *pgxpool.Pool so callers can use the underlying API directly
// where the wrapper hasn't grown a method yet.
type Pool struct {
	*pgxpool.Pool
	log *slog.Logger
}

// New parses the DSN, opens the pool, verifies server version and
// returns a ready-to-use Pool. The caller is responsible for Close().
func New(ctx context.Context, cfg Config, log *slog.Logger) (*Pool, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("pool: DSN is required")
	}

	cfg = cfg.withDefaults()

	pgxCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pool: parse DSN: %w", err)
	}
	pgxCfg.MaxConns = cfg.MaxConns
	pgxCfg.MinConns = cfg.MinConns
	pgxCfg.MaxConnLifetime = cfg.MaxConnLifetime
	pgxCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	pgxCfg.HealthCheckPeriod = cfg.HealthCheckPeriod

	// FEEDBACK loadtest #3 — apply statement_timeout in AfterConnect
	// so every connection in the pool comes out of the gate with a
	// bounded query ceiling. Bypasses are still possible via
	// `SET LOCAL statement_timeout = 0` inside a tx (used by long-
	// running maintenance jobs).
	if cfg.StatementTimeout > 0 {
		timeoutMS := int64(cfg.StatementTimeout / time.Millisecond)
		afterConnect := pgxCfg.AfterConnect
		pgxCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			if afterConnect != nil {
				if err := afterConnect(ctx, conn); err != nil {
					return err
				}
			}
			_, err := conn.Exec(ctx,
				fmt.Sprintf("SET statement_timeout = %d", timeoutMS))
			return err
		}
	}

	p, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		return nil, fmt.Errorf("pool: create: %w", err)
	}

	if err := verifyVersion(ctx, p); err != nil {
		p.Close()
		return nil, err
	}

	log.Info("postgres pool ready",
		"max_conns", pgxCfg.MaxConns,
		"min_conns", pgxCfg.MinConns,
		"max_conn_lifetime", pgxCfg.MaxConnLifetime,
		"max_conn_idle_time", pgxCfg.MaxConnIdleTime,
		"health_check_period", pgxCfg.HealthCheckPeriod,
		"statement_timeout", cfg.StatementTimeout,
	)
	return &Pool{Pool: p, log: log}, nil
}

// Ping issues a single round-trip to verify connectivity.
// Used by /readyz.
func (p *Pool) Ping(ctx context.Context) error {
	return p.Pool.Ping(ctx)
}

// verifyVersion fails fast if the server is older than MinPostgresMajor.
// We check at pool init rather than on each query to keep request paths clean.
func verifyVersion(ctx context.Context, p *pgxpool.Pool) error {
	var versionNum int
	if err := p.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&versionNum); err != nil {
		return fmt.Errorf("pool: query server version: %w", err)
	}
	major := versionNum / 10000
	if major < MinPostgresMajor {
		return fmt.Errorf("pool: postgres %d is unsupported, minimum required is %d", major, MinPostgresMajor)
	}
	return nil
}
