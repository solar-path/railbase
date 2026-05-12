// Package logger constructs the application slog logger from config.
//
// Two output formats are supported:
//   - "json" (default): structured one-record-per-line for production /
//     container log shipping
//   - "text": human-friendly, primarily for local `railbase serve` development
//
// Levels follow slog: debug, info, warn, error.
//
// Date-rotated file output: when Options.FileDir is non-empty, a
// secondary writer cuts a new file at local midnight under
// `<FileDir>/railbase-YYYY-MM-DD.log` and the slog fan-out reaches BOTH
// stdout (subject to TerminalLevel) and the file (subject to FileLevel).
// Operators get the full debug trail on disk + a quiet terminal.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Options bundles every knob the package supports. The legacy
// three-argument form is preserved via New() for backward compatibility
// with the older call sites (CLI utilities + tests).
type Options struct {
	// Level is the threshold for the stdout terminal handler.
	// One of: debug, info, warn, error. Defaults to info.
	Level string

	// Format selects the stdout encoding: "json" (default) or "text".
	Format string

	// Out is the stdout writer. Defaults to os.Stdout when nil.
	Out io.Writer

	// FileDir, when non-empty, enables date-rotated file logging under
	// the directory. Each Write hits both stdout and the file; the
	// file always uses JSON encoding so it's grep + jq friendly even
	// when the terminal is rendering text format.
	FileDir string

	// FileLevel is the threshold for the file handler. Defaults to
	// debug (capture everything) — file logs are post-mortem material,
	// so we don't want to discover after the fact that we filtered
	// out the line we'd needed. Override only if the file is becoming
	// unmanageably large in a noisy dev loop.
	FileLevel string

	// FileRetention is how long to keep daily log files before the
	// rotating writer purges them. Zero (the default) disables
	// purging — let the operator manage retention themselves.
	FileRetention time.Duration
}

// New returns a slog.Logger wired to the legacy three-argument signature.
// Callers wanting file rotation / split levels use NewWithOptions.
//
// Unknown levels fall back to info; unknown formats fall back to json.
// We intentionally do not error here — config.Validate already enforces
// the allowed set, and the logger must never be the thing that prevents
// the binary from starting.
func New(level, format string, w io.Writer) *slog.Logger {
	return slog.New(NewHandler(level, format, w))
}

// NewWithOptions returns the configured logger plus an optional Close
// func. Close is non-nil iff file logging is active; callers should
// defer it so the open file flushes on shutdown. Safe to call repeatedly.
func NewWithOptions(opts Options) (*slog.Logger, func() error, error) {
	stdoutHandler := NewHandler(opts.Level, opts.Format, opts.Out)
	if opts.FileDir == "" {
		return slog.New(stdoutHandler), func() error { return nil }, nil
	}
	writer, err := newDateRotatingWriter(opts.FileDir, opts.FileRetention)
	if err != nil {
		// File logging requested but couldn't be wired — fall back
		// to stdout-only WITH a logged warning, not a hard failure.
		// Logger must never block boot.
		logger := slog.New(stdoutHandler)
		logger.Warn("file logger could not be wired; continuing with stdout-only",
			"dir", opts.FileDir, "err", err)
		return logger, func() error { return nil }, nil
	}
	fileLevel := opts.FileLevel
	if fileLevel == "" {
		fileLevel = "debug"
	}
	// File handler always emits JSON — file logs are programmatically
	// consumed (jq, grep, Splunk, log shipping) much more often than
	// they're eyeballed; the text format would be a footgun.
	fileHandler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: parseLevel(fileLevel),
	})
	combined := multiHandler{stdoutHandler, fileHandler}
	return slog.New(combined), writer.Close, nil
}

// multiHandler fans every record out to N handlers. Cheaper than the
// `slog.NewLogLogger` two-step wrap and avoids the per-handler enabled-
// gate cost when most callers are JSON-encoded.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range m {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
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
