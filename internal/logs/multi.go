package logs

// Multi-handler fan-out: every slog.Record goes to BOTH stdout and
// the DB Sink. Uses slog's idiomatic "wrap multiple handlers" shape
// rather than reinventing dispatch — keeps composition with other
// downstream handlers (e.g. OTLP plugin) straightforward.

import (
	"context"
	"log/slog"
)

// Multi is a slog.Handler that fans out Handle calls to N children.
// Each child decides independently whether to accept via Enabled().
// Useful for "always write to stdout + optionally to DB" wiring.
type Multi struct {
	handlers []slog.Handler
}

// NewMulti wraps the given handlers. Nil entries silently dropped
// so callers can pass `[h1, sinkOrNil, h3]` without branching.
func NewMulti(handlers ...slog.Handler) *Multi {
	out := make([]slog.Handler, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			out = append(out, h)
		}
	}
	return &Multi{handlers: out}
}

// Enabled returns true if ANY child is enabled at the given level —
// short-circuit avoids the materialisation cost when nobody wants
// the record.
func (m *Multi) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches to every enabled child. Errors from children
// are silently absorbed — slog's contract is best-effort, and one
// flaky handler shouldn't make the whole logger return errors.
func (m *Multi) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r)
		}
	}
	return nil
}

// WithAttrs returns a Multi whose children have all received the
// new attrs. This is how `logger.With("k", v)` propagates structured
// fields through the fan-out.
func (m *Multi) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &Multi{handlers: out}
}

// WithGroup likewise propagates the new group.
func (m *Multi) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(name)
	}
	return &Multi{handlers: out}
}
