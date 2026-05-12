// Package config loads runtime configuration from env vars and flags.
//
// Precedence (highest wins):
//  1. CLI flags
//  2. Environment variables (RAILBASE_*)
//  3. Defaults baked into Default().
//
// Settings that need to be mutable at runtime live in the `_settings`
// collection (see docs/14-observability.md), not here. This struct
// holds only boot-time, file-system-level configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/db/embedded"
)

// Config is the resolved boot configuration.
type Config struct {
	// HTTPAddr is the bind address for the API server, e.g. ":8090".
	HTTPAddr string

	// DataDir is where uploaded files, hooks scaffolding, and the
	// embedded-postgres data directory (when --embed-postgres) live.
	// Mirrors PocketBase's pb_data/ convention.
	DataDir string

	// HooksDir is where pb_hooks/*.pb.js files live.
	HooksDir string

	// PublicDir serves static assets at /; empty disables.
	PublicDir string

	// DSN is the PostgreSQL connection string. Required unless EmbedPostgres
	// is true, in which case it is filled in by the embedded-postgres
	// subprocess after it binds a free port.
	//
	// Format: "postgres://user:password@host:port/dbname?sslmode=..."
	DSN string

	// EmbedPostgres, when true, spawns an embedded postgres subprocess
	// (fergusstrange/embedded-postgres) on startup and points DSN at it.
	// Refused when ProductionMode is true — embedded-postgres is dev-only.
	EmbedPostgres bool

	// ProductionMode disables dev-only features (embedded-postgres,
	// hot-reload watchers, verbose error pages). Set via RAILBASE_PROD=true.
	ProductionMode bool

	// PBCompat controls /api/* URL aliases:
	//   "strict" — only PB-shape URLs
	//   "native" — only Railbase-shape URLs
	//   "both"   — both (default)
	PBCompat string

	// ShutdownGrace bounds how long graceful shutdown waits before SIGKILL.
	ShutdownGrace time.Duration

	// LogLevel is one of: debug, info, warn, error.
	LogLevel string

	// LogFormat is "json" (default in production) or "text".
	LogFormat string

	// DevMode toggles pretty logging, hot-reload watchers, and the
	// embedded admin UI dev proxy.
	DevMode bool

	// SetupMode is the first-run fallback: no DSN provided, no
	// persisted `.dsn` file, embedded postgres not compiled in
	// (production binary), not production. Boot still completes —
	// we run a minimal HTTP server that serves the admin SPA + the
	// `/api/_admin/_setup/*` wizard endpoints + a 503 stub for every
	// other route, so the operator can configure the real database
	// from a browser instead of from the shell.
	//
	// Auto-detected by Load() — operators don't set this directly.
	SetupMode bool
}

// Default returns the baseline configuration with no env/flag overlay.
func Default() Config {
	return Config{
		HTTPAddr:       ":8090",
		DataDir:        "./pb_data",
		HooksDir:       "./pb_hooks",
		PublicDir:      "",
		DSN:            "",
		EmbedPostgres:  false,
		ProductionMode: false,
		PBCompat:       "both",
		ShutdownGrace:  15 * time.Second,
		LogLevel:       "info",
		LogFormat:      "json",
		DevMode:        false,
	}
}

// Load resolves config from environment variables, layered on top of
// the defaults. CLI flag overlay is applied by callers after Load.
func Load() (Config, error) {
	c := Default()

	if v := os.Getenv("RAILBASE_HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("RAILBASE_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("RAILBASE_HOOKS_DIR"); v != "" {
		c.HooksDir = v
	}
	if v := os.Getenv("RAILBASE_PUBLIC_DIR"); v != "" {
		c.PublicDir = v
	}
	if v := os.Getenv("RAILBASE_DSN"); v != "" {
		c.DSN = v
	}
	if v := os.Getenv("RAILBASE_EMBED_POSTGRES"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("RAILBASE_EMBED_POSTGRES: %w", err)
		}
		c.EmbedPostgres = b
	}
	if v := os.Getenv("RAILBASE_PROD"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("RAILBASE_PROD: %w", err)
		}
		c.ProductionMode = b
	}
	if v := os.Getenv("RAILBASE_PB_COMPAT"); v != "" {
		c.PBCompat = v
	}
	if v := os.Getenv("RAILBASE_SHUTDOWN_GRACE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("RAILBASE_SHUTDOWN_GRACE: %w", err)
		}
		c.ShutdownGrace = d
	}
	if v := os.Getenv("RAILBASE_LOG_LEVEL"); v != "" {
		c.LogLevel = strings.ToLower(v)
	}
	if v := os.Getenv("RAILBASE_LOG_FORMAT"); v != "" {
		c.LogFormat = strings.ToLower(v)
	}
	if v := os.Getenv("RAILBASE_DEV"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("RAILBASE_DEV: %w", err)
		}
		c.DevMode = b
	}

	// v1.7.39: consult the persisted DSN file BEFORE the zero-config
	// embedded-fallback policy below. The admin setup wizard writes
	// <DataDir>/.dsn after the operator picks a real PostgreSQL on
	// first run; on the NEXT boot we pick it up here and bypass
	// embedded entirely. Env-level RAILBASE_DSN still wins (it was
	// loaded just above), so an operator with both falls into the
	// env path — matching the spec's "env vars override persisted
	// config" expectation.
	if c.DSN == "" {
		if persisted := readPersistedDSN(c.DataDir); persisted != "" {
			c.DSN = persisted
		}
	}

	// Zero-config UX policy with no explicit DSN AND not in production:
	//
	//   - If embedded postgres IS compiled in (-tags embed_pg, dev/demo
	//     build via `make build-embed`) → auto-enable EmbedPostgres so
	//     the first `./railbase serve` on a fresh machine just works.
	//
	//   - If embedded postgres is NOT compiled in (release binaries
	//     from `bin/dist/` / GitHub Releases / goreleaser output) →
	//     SetupMode. App.Run() poignts the operator at the first-run
	//     wizard (v1.7.39) so they pick a real Postgres in the browser
	//     instead of getting a hard boot error like prior versions did.
	//
	// Hard-coding a default DSN like `postgres://$USER@/railbase?host=/tmp`
	// was considered and rejected: Railbase is a universal backend, not
	// a machine-local tool, so db name + auth identity are operator-owned.
	// `DetectLocalPostgresSockets()` is still exported for the wizard to
	// list candidates without the boot process committing to one.
	if c.DSN == "" && !c.EmbedPostgres && !c.ProductionMode {
		if embedded.Available() {
			c.EmbedPostgres = true
		} else {
			c.SetupMode = true
		}
	}

	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// LocalPostgresSocket describes a Unix-domain socket where a
// PostgreSQL server is listening. The setup wizard renders these
// as picker options so the operator chooses the connection
// shape — db name + auth user + ssl mode — rather than the boot
// process guessing on their behalf.
type LocalPostgresSocket struct {
	// Dir is the directory containing the socket file. Pass this
	// to libpq via `host=<dir>` to use the socket.
	Dir string

	// Path is the full socket path (`<Dir>/.s.PGSQL.5432`).
	// Operators see this in the wizard UI for confidence ("yes,
	// that's my Homebrew install").
	Path string

	// Distro is a best-effort label for the wizard: "homebrew",
	// "system", "unknown". Purely cosmetic — never branched on.
	Distro string
}

// readPersistedDSN reads <dataDir>/.dsn (written by the v1.7.39 admin
// setup wizard) and returns the trimmed DSN string. Returns "" on
// ANY error: file absent, unreadable, empty, all map to "no
// persisted DSN" so Load() falls through to the zero-config embedded
// path. No panic, no log — silent failure is intentional so a
// permissions glitch on .dsn doesn't brick boot.
func readPersistedDSN(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dataDir, ".dsn"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// DetectLocalPostgresSockets returns Unix-domain PostgreSQL sockets
// found at well-known locations. The setup wizard uses this to
// offer the operator pre-filled options instead of forcing them
// to remember the path. Returns an empty slice on machines with
// no detected PG.
//
// Why socket-only (not localhost:5432)? Two reasons:
//
//  1. Stat-on-socket is a presence check. A TCP open-port probe on
//     5432 is ambiguous: that port might be your home gateway, a
//     development tunnel, an unrelated server. Sockets named
//     `.s.PGSQL.<port>` are vanishingly unlikely to be anything but
//     PostgreSQL.
//  2. Socket auth is typically `peer` (Linux) or `trust` (macOS brew),
//     so no password roundtrip is needed for the local-PG case the
//     wizard surfaces as "use my local server".
//
// The well-known paths cover the three common Postgres distros:
//   - Homebrew on macOS  → /tmp/.s.PGSQL.5432
//   - Debian/Ubuntu      → /var/run/postgresql/.s.PGSQL.5432
//   - Fedora/RHEL        → /var/run/postgresql/.s.PGSQL.5432 (same)
//
// Boot itself does NOT use this — see Load(). It's a pure
// observation the setup wizard reads off `/_/setup/probe-db`.
func DetectLocalPostgresSockets() []LocalPostgresSocket {
	candidates := []struct {
		dir    string
		distro string
	}{
		{"/tmp", "homebrew"},          // Homebrew on macOS
		{"/var/run/postgresql", "system"}, // Debian/Ubuntu, Fedora/RHEL
	}
	var out []LocalPostgresSocket
	for _, c := range candidates {
		sock := filepath.Join(c.dir, ".s.PGSQL.5432")
		info, err := os.Stat(sock)
		if err != nil {
			continue
		}
		// Socket file should be a Unix socket. Skip stale regular
		// files that happen to share the name.
		if info.Mode()&os.ModeSocket == 0 {
			continue
		}
		out = append(out, LocalPostgresSocket{Dir: c.dir, Path: sock, Distro: c.distro})
	}
	return out
}

// Validate checks invariants that hold across all entry points.
func (c Config) Validate() error {
	switch c.PBCompat {
	case "strict", "native", "both":
	default:
		return fmt.Errorf("invalid pb-compat %q: want strict|native|both", c.PBCompat)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log-level %q", c.LogLevel)
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("invalid log-format %q", c.LogFormat)
	}
	if c.HTTPAddr == "" {
		return fmt.Errorf("http-addr must not be empty")
	}
	if c.ShutdownGrace <= 0 {
		return fmt.Errorf("shutdown-grace must be positive")
	}

	// Postgres-only baseline: either explicit DSN OR embedded opt-in
	// OR setup-mode fallback. Load() auto-flips EmbedPostgres=true when
	// embed_pg is compiled in, otherwise SetupMode=true so the first-run
	// wizard can drive db configuration from the browser. Reaching this
	// state with all three false means: caller skipped Load(), OR was in
	// production with no DSN (both fail).
	if c.DSN == "" && !c.EmbedPostgres && !c.SetupMode {
		if c.ProductionMode {
			return fmt.Errorf("RAILBASE_DSN required in production (RAILBASE_PROD=true). Refusing to fall back to embedded postgres or setup mode")
		}
		return fmt.Errorf("RAILBASE_DSN required (or use Load() which picks embedded/setup-mode in dev)")
	}
	if c.EmbedPostgres && c.ProductionMode {
		return fmt.Errorf("embed-postgres is dev-only; refused with RAILBASE_PROD=true. Provide RAILBASE_DSN to a managed Postgres instead")
	}
	if c.DSN != "" && !strings.HasPrefix(c.DSN, "postgres://") && !strings.HasPrefix(c.DSN, "postgresql://") {
		return fmt.Errorf("RAILBASE_DSN must start with postgres:// or postgresql:// (Postgres is the only supported backend)")
	}
	return nil
}
