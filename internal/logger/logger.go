// Package logger constructs the application slog logger from config.
//
// Two output formats are supported:
//   - "json" (default): structured one-record-per-line for production /
//     container log shipping
//   - "text": human-friendly, primarily for local `railbase serve` development
//
// Levels follow slog: debug, info, warn, error.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New returns a slog.Logger wired to the requested level/format on w.
// If w is nil, logs are written to os.Stdout.
//
// Unknown levels fall back to info; unknown formats fall back to json.
// We intentionally do not error here — config.Validate already enforces
// the allowed set, and the logger must never be the thing that prevents
// the binary from starting.
func New(level, format string, w io.Writer) *slog.Logger {
	return slog.New(NewHandler(level, format, w))
}

// NewHandler returns the bare slog.Handler that New() wraps. Exposed so
// callers (e.g. app boot) can wrap it in a Multi-handler that fans out
// to the stdout/JSON handler AND to other Handlers (e.g. logs.Sink for
// admin-UI persistence) without re-implementing the level/format pick.
func NewHandler(level, format string, w io.Writer) slog.Handler {
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}
	switch strings.ToLower(format) {
	case "text":
		return slog.NewTextHandler(w, opts)
	default:
		return slog.NewJSONHandler(w, opts)
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
