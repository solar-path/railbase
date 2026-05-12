package logs

// v1.7.6 — unit tests for the Sink that don't require a DB.
// Backed by a stub pool: we test enqueue/overflow/MinLevel/Close
// semantics; the actual COPY-FROM batch is covered in the embed_pg
// e2e (sink_e2e_test.go).

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// nilPoolSink builds a Sink with a nil pool. Safe as long as the test
// only exercises code paths that don't call s.pool — Enabled / Handle
// / enqueue / Stats / Close-without-flush.
//
// We override flushInterval to a very large value so the timer doesn't
// fire during the test, and override insertBatch indirectly by closing
// before flush could happen.
func nilPoolSink(t *testing.T, cfg Config) *Sink {
	t.Helper()
	cfg.FlushInterval = time.Hour // suppress timer-fire
	s := NewSink(nil, cfg)
	t.Cleanup(func() {
		// Don't drain — pool is nil. Stop the goroutine cleanly.
		// Close attempts flush; flush with empty buffer is a no-op,
		// so we drain the buffer here first by stealing it.
		s.mu.Lock()
		s.buffer = s.buffer[:0]
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s
}

func TestSink_Enabled_MinLevel(t *testing.T) {
	s := nilPoolSink(t, Config{MinLevel: slog.LevelWarn, Capacity: 10})
	if s.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Info should be disabled when MinLevel=Warn")
	}
	if !s.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("Warn should be enabled when MinLevel=Warn")
	}
	if !s.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Error should be enabled when MinLevel=Warn")
	}
}

func TestSink_Enabled_ZeroValueDefaultsToInfo(t *testing.T) {
	s := nilPoolSink(t, Config{}) // MinLevel left zero
	if s.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Debug accepted when MinLevel was zero (should default to Info)")
	}
	if !s.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Info rejected when MinLevel was zero (should default to Info)")
	}
}

func TestSink_Handle_BuffersEntry(t *testing.T) {
	s := nilPoolSink(t, Config{Capacity: 10, BatchSize: 100})
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	rec.AddAttrs(slog.String("k", "v"))
	if err := s.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := s.Stats().Buffered; got != 1 {
		t.Fatalf("Buffered=%d, want 1", got)
	}
}

func TestSink_Handle_BelowMinLevel_NotBuffered(t *testing.T) {
	s := nilPoolSink(t, Config{MinLevel: slog.LevelWarn, Capacity: 10, BatchSize: 100})
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "skipme", 0)
	_ = s.Handle(context.Background(), rec)
	if got := s.Stats().Buffered; got != 0 {
		t.Fatalf("Buffered=%d, want 0 (Info below MinLevel=Warn)", got)
	}
}

func TestSink_Overflow_DropsOldest(t *testing.T) {
	s := nilPoolSink(t, Config{Capacity: 3, BatchSize: 100})
	for i := 0; i < 5; i++ {
		rec := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
		_ = s.Handle(context.Background(), rec)
	}
	st := s.Stats()
	if st.Buffered != 3 {
		t.Fatalf("Buffered=%d, want 3 (capped)", st.Buffered)
	}
	if st.Dropped != 2 {
		t.Fatalf("Dropped=%d, want 2 (over-capacity producer)", st.Dropped)
	}
}

func TestSink_WithAttrs_ReturnsSelf(t *testing.T) {
	s := nilPoolSink(t, Config{Capacity: 10})
	// Sink intentionally doesn't pre-bind attrs (the entry materialises
	// its own at Handle time), so WithAttrs/WithGroup return the same
	// sink. Document the contract.
	if got := s.WithAttrs(nil); got != s {
		t.Fatal("WithAttrs returned a different handler")
	}
	if got := s.WithGroup("x"); got != s {
		t.Fatal("WithGroup returned a different handler")
	}
}

func TestSink_Close_IsIdempotent(t *testing.T) {
	cfg := Config{Capacity: 5, BatchSize: 100, FlushInterval: time.Hour}
	s := NewSink(nil, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// First close must complete promptly (empty buffer).
	if err := s.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close is a no-op.
	if err := s.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// After close, Enabled should return false (records past shutdown
	// shouldn't accumulate in a dead buffer).
	if s.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Enabled returned true after Close")
	}
}

func TestSink_WithRequestID_RoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "rid-abc")
	rid, ok := ctx.Value(reqIDKey{}).(string)
	if !ok || rid != "rid-abc" {
		t.Fatalf("WithRequestID: got (%q, %v), want (rid-abc, true)", rid, ok)
	}
}

func TestSink_Stats_InitialZeroes(t *testing.T) {
	s := nilPoolSink(t, Config{Capacity: 10})
	st := s.Stats()
	if st.Buffered != 0 || st.Written != 0 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("Stats not zero on fresh Sink: %+v", st)
	}
}
