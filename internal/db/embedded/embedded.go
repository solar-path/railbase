// Package embedded hosts the optional fergusstrange/embedded-postgres
// integration behind the `embed_pg` build tag.
//
// Why a separate package (instead of inline in pkg/railbase or
// internal/db/pool):
//   - Layer 1 belongs to internal/db/* (see docs/02-architecture.md
//     "Структура пакетов").
//   - The fergusstrange/embedded-postgres dependency is downloaded as
//     extra runtime data and is dev-only. It must NOT leak into the
//     pkg/railbase/ public-API import graph.
//   - Build-tag swapping (`embed_pg` vs default) is scoped here. The
//     rest of the codebase calls a stable Start function and remains
//     unaware of the runtime split.
//
// Usage:
//
//	dsn, stop, err := embedded.Start(ctx, embedded.Config{
//	    DataDir:    cfg.DataDir,
//	    Production: cfg.ProductionMode,
//	    Log:        log,
//	})
//	if err != nil { ... }
//	defer stop()
package embedded

import (
	"context"
	"log/slog"
)

// Config is what Start needs from the caller.
type Config struct {
	// DataDir is where the embedded postgres binary writes its data
	// (typically <pb_data>/postgres/). Must be writable.
	DataDir string

	// Production, when true, makes Start refuse to run regardless of
	// build tag. Defence-in-depth complement to config.Validate.
	Production bool

	// Log receives both Railbase-level events and the postgres
	// subprocess stdout/stderr (each line one record).
	Log *slog.Logger
}

// StopFunc gracefully terminates the embedded postgres subprocess.
// Always-non-nil if Start returned a nil error.
type StopFunc func() error

// Start launches embedded postgres and returns a usable DSN.
//
// The actual implementation is selected at compile time:
//   - default build:    returns ErrNotCompiledIn
//   - -tags embed_pg:   spawns subprocess via fergusstrange/embedded-postgres
//
// See start_enabled.go and start_disabled.go.
func Start(ctx context.Context, cfg Config) (dsn string, stop StopFunc, err error) {
	return start(ctx, cfg)
}
