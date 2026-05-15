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
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// defaultEmbedPort — the preferred port. Kept stable so the dev DSN
// embedded in the user's .env / IDE config doesn't churn between
// restarts. FEEDBACK #5 — two parallel Railbase projects on the
// same machine collide on this port; we now probe + persist a
// per-project alternate when it's already held.
const defaultEmbedPort = 54329

// chooseEmbedPort returns the port the embedded postgres should
// bind. Order of preference:
//
//  1. Previously-chosen port persisted in <dataDir>/postgres/.port —
//     re-used across restarts so connection strings the user pasted
//     into their IDE / .env stay valid.
//  2. defaultEmbedPort (54329) if it's currently free.
//  3. First free port in [54330, 54429] — bounded scan so a system
//     under heavy port pressure fails loudly instead of looping.
//
// The chosen port is persisted on disk so step (1) catches it next
// time. FEEDBACK #5.
func chooseEmbedPort(dataDir string) (int, error) {
	return chooseEmbedPortWithEnv(dataDir, os.Getenv("RAILBASE_EMBED_PG_PORT"))
}

// chooseEmbedPortWithEnv is the testable body of chooseEmbedPort.
// envPort, if non-empty and a valid TCP port number, short-circuits
// the sticky/default/scan logic — the operator has explicitly chosen
// a port and the choice should be honoured even if it collides.
//
// FEEDBACK #B4 — the blogger project ran sentinel on the default
// :54329 and had no way to force a different port for blogger's
// own embedded-pg. Now `RAILBASE_EMBED_PG_PORT=54331 ./blogger dev`
// works without code changes.
func chooseEmbedPortWithEnv(dataDir, envPort string) (int, error) {
	// Explicit env override wins. We still validate the value is a
	// plausible TCP port — `RAILBASE_EMBED_PG_PORT=abc` shouldn't
	// crash the boot loop with a cryptic error from net.Listen.
	if envPort != "" {
		p, err := strconv.Atoi(strings.TrimSpace(envPort))
		if err != nil || p < 1 || p > 65535 {
			return 0, fmt.Errorf("embedded postgres: RAILBASE_EMBED_PG_PORT=%q is not a valid TCP port (1..65535)", envPort)
		}
		// Persist so subsequent boots without the env var stay sticky
		// on the same port (consistent with the choice-persistence
		// behaviour for the default path).
		_ = writePortFile(filepath.Join(dataDir, "postgres", ".port"), p)
		return p, nil
	}

	portFile := filepath.Join(dataDir, "postgres", ".port")
	// Re-use sticky choice when free. Operators running two
	// projects benefit from getting the SAME alt-port every boot.
	if data, err := os.ReadFile(portFile); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && p > 0 {
			if portFree(p) {
				return p, nil
			}
			// Sticky port now busy — fall through to fresh probe.
		}
	}
	// Default first.
	if portFree(defaultEmbedPort) {
		_ = writePortFile(portFile, defaultEmbedPort)
		return defaultEmbedPort, nil
	}
	// Scan a bounded window. 100 ports is generous for "another
	// Railbase project + maybe a database tool occupying the
	// neighbourhood"; if all 100 are taken something else is wrong.
	for p := defaultEmbedPort + 1; p < defaultEmbedPort+100; p++ {
		if portFree(p) {
			_ = writePortFile(portFile, p)
			return p, nil
		}
	}
	return 0, fmt.Errorf("embedded postgres: no free port in [%d, %d]; close other postgres instances, set RAILBASE_EMBED_PG_PORT to an explicit value, or set RAILBASE_DSN",
		defaultEmbedPort, defaultEmbedPort+99)
}

// portFree returns true iff TCP `p` is bindable on localhost right
// now. Races are possible — a concurrent process could grab the
// port in the microseconds between this check and the actual bind
// — but in practice the embedded-postgres library's start path
// retries on bind failure, so a race produces a clear startup
// error instead of silent breakage.
func portFree(p int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", p)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func writePortFile(path string, port int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(port)), 0o644)
}

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
	port, err := chooseEmbedPort(cfg.DataDir)
	if err != nil {
		return "", nil, err
	}
	cfg.Log.Info("starting embedded postgres",
		"runtime_path", runtimePath,
		"data_path", dataPath,
		"port", port,
	)

	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Database("railbase").
			Username("railbase").
			Password("railbase").
			Port(uint32(port)).
			RuntimePath(runtimePath).
			DataPath(dataPath).
			Logger(newSlogWriter(cfg.Log, slog.LevelDebug)),
	)
	if err := pg.Start(); err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}

	dsn := fmt.Sprintf(
		"postgres://railbase:railbase@127.0.0.1:%d/railbase?sslmode=disable",
		port,
	)
	return dsn, pg.Stop, nil
}

// Available reports whether this binary was built with `-tags embed_pg`.
// Returns true in the embed_pg build. See start_disabled.go for the
// default-build counterpart.
func Available() bool { return true }
