package logs

// v1.7.6 — unit tests for Multi (slog.Handler fan-out).
//
// Asserts:
//
//  1. Multi.Enabled short-circuits to false when every child disables
//  2. Multi.Enabled returns true when ANY child accepts
//  3. Handle dispatches to enabled children
//  4. Handle skips children whose Enabled returns false
//  5. NewMulti drops nil entries silently (caller convenience)
//  6. WithAttrs propagates to every child
//  7. WithGroup propagates to every child

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// recordingHandler is a slog.Handler stub that counts calls + filters
// by level. Used by multi tests so we can observe dispatch behaviour
// without a real backing store.
type recordingHandler struct {
	min     slog.Level
	handles atomic.Int64
	attrs   atomic.Int64
	groups  atomic.Int64
	tag     string // identity for With* clone tracking
}

func (h *recordingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.min }
func (h *recordingHandler) Handle(_ context.Context, _ slog.Record) error {
	h.handles.Add(1)
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	h.attrs.Add(1)
	return h
}
func (h *recordingHandler) WithGroup(_ string) slog.Handler {
	h.groups.Add(1)
	return h
}

func TestMulti_EnabledAggregates(t *testing.T) {
	a := &recordingHandler{min: slog.LevelError}
	b := &recordingHandler{min: slog.LevelInfo}
	m := NewMulti(a, b)

	if !m.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Enabled(Info) = false; expected true because b accepts Info")
	}
	if !m.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Enabled(Error) = false; expected true")
	}
	// Both reject Debug.
	if m.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Enabled(Debug) = true; expected false")
	}
}

func TestMulti_HandleDispatches(t *testing.T) {
	a := &recordingHandler{min: slog.LevelInfo}
	b := &recordingHandler{min: slog.LevelInfo}
	m := NewMulti(a, b)
	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)
	_ = m.Handle(context.Background(), rec)
	if a.handles.Load() != 1 || b.handles.Load() != 1 {
		t.Fatalf("Handle dispatch: a=%d b=%d, want 1+1", a.handles.Load(), b.handles.Load())
	}
}

func TestMulti_HandleSkipsDisabled(t *testing.T) {
	loud := &recordingHandler{min: slog.LevelError}
	chatty := &recordingHandler{min: slog.LevelDebug}
	m := NewMulti(loud, chatty)
	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "info-only", 0)
	_ = m.Handle(context.Background(), rec)
	if loud.handles.Load() != 0 {
		t.Fatalf("loud (Error+) got info record: %d", loud.handles.Load())
	}
	if chatty.handles.Load() != 1 {
		t.Fatalf("chatty (Debug+) missed info record: %d", chatty.handles.Load())
	}
}

func TestMulti_NewMulti_DropsNil(t *testing.T) {
	a := &recordingHandler{min: slog.LevelInfo}
	m := NewMulti(a, nil, nil)
	if got := len(m.handlers); got != 1 {
		t.Fatalf("nil entries kept: len=%d, want 1", got)
	}
}

func TestMulti_NewMulti_AllNilIsEmpty(t *testing.T) {
	m := NewMulti(nil, nil)
	if len(m.handlers) != 0 {
		t.Fatal("nil-only ctor should produce zero handlers")
	}
	// Empty Multi: Enabled false, Handle no-op.
	if m.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("empty Multi reports Enabled=true")
	}
	if err := m.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelError, "noop", 0)); err != nil {
		t.Fatalf("Handle on empty Multi: %v", err)
	}
}

func TestMulti_WithAttrs_Propagates(t *testing.T) {
	a := &recordingHandler{min: slog.LevelInfo, tag: "A"}
	b := &recordingHandler{min: slog.LevelInfo, tag: "B"}
	m := NewMulti(a, b)
	_ = m.WithAttrs([]slog.Attr{slog.String("k", "v")})
	if a.attrs.Load() != 1 || b.attrs.Load() != 1 {
		t.Fatalf("WithAttrs propagate: a=%d b=%d", a.attrs.Load(), b.attrs.Load())
	}
}

func TestMulti_WithGroup_Propagates(t *testing.T) {
	a := &recordingHandler{min: slog.LevelInfo}
	b := &recordingHandler{min: slog.LevelInfo}
	m := NewMulti(a, b)
	_ = m.WithGroup("req")
	if a.groups.Load() != 1 || b.groups.Load() != 1 {
		t.Fatalf("WithGroup propagate: a=%d b=%d", a.groups.Load(), b.groups.Load())
	}
}
