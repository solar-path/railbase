// Package logs persists application slog.Records to the `_logs`
// table for admin-UI browsing.
//
// Architecture:
//
//	slog.Logger ──► Multi-Handler ──► stdout/JSON  (always on; stderr-shaped)
//	                              └──► logs.Sink   (off by default; on in prod)
//
// The Sink implements slog.Handler. On Handle() it formats the
// record into a buffered batch and returns immediately — the hot
// path never blocks on DB I/O. A background flusher goroutine
// drains the batch every FlushInterval (default 2s) or when the
// batch reaches BatchSize (default 100), whichever comes first.
//
// On buffer overflow (Capacity, default 10_000): the oldest pending
// entry is dropped and an internal `dropped` counter increments.
// We intentionally don't block the producer — if the DB is down,
// the application keeps running and operators see the gap in the
// admin log view rather than a frozen server.
//
// Retention: rows past `logs.retention_days` are swept by the
// `cleanup_logs` cron builtin. Default 14 days.

package logs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
)

// Defaults — overridable via Config.
const (
	DefaultBatchSize     = 100
	DefaultFlushInterval = 2 * time.Second
	DefaultCapacity      = 10_000
	DefaultRetentionDays = 14
)

// Config bundles Sink tuning knobs.
type Config struct {
	// BatchSize is the per-flush insert batch. 100 is sized to fit
	// inside a single 64KB INSERT statement even with verbose attrs.
	BatchSize int
	// FlushInterval is the max time a record waits in the buffer
	// before being persisted. Trade-off: lower = fresher admin view
	// but more DB round-trips; higher = better batching but a crash
	// loses up to N seconds of in-memory logs.
	FlushInterval time.Duration
	// Capacity is the in-memory ring-buffer cap. When the producer
	// outpaces the flusher (e.g. DB hiccup), the oldest pending
	// records are dropped + a counter increments.
	Capacity int
	// MinLevel filters records BEFORE they enter the buffer. Default
	// is slog.LevelInfo (set by NewSink when zero-valued — caller
	// can opt into Debug for full firehose during incidents).
	MinLevel slog.Level
}

// Stats exposes Sink counters for the admin UI / metrics tab. All
// values are atomic snapshots — safe to read without holding a lock.
type Stats struct {
	Buffered uint64 // currently in the in-memory buffer
	Written  uint64 // successfully persisted total
	Dropped  uint64 // dropped on overflow (oldest evicted)
	Errors   uint64 // DB insert failures
}

// Sink is the slog.Handler that persists records. Goroutine-safe.
// Stop the flusher with Close — call on shutdown.
type Sink struct {
	pool *pgxpool.Pool
	cfg  Config

	mu       sync.Mutex
	buffer   []entry
	cap      int
	written  atomic.Uint64
	dropped  atomic.Uint64
	errors   atomic.Uint64
	wake     chan struct{}
	stopCh   chan struct{}
	stopped  atomic.Bool
	stopOnce sync.Once
	flusher  sync.WaitGroup
}

// entry is the materialised slog.Record awaiting persistence.
// Closing over the original record would pin its source-file
// information; we copy what we need.
type entry struct {
	id        uuid.UUID
	level     string
	message   string
	attrs     []byte // JSON-encoded once at enqueue time
	source    string
	requestID string
	userID    *uuid.UUID
	createdAt time.Time
}

// NewSink constructs and starts the Sink. Always call Close() on
// shutdown to flush any pending entries.
func NewSink(pool *pgxpool.Pool, cfg Config) *Sink {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultFlushInterval
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.MinLevel == 0 && cfg.MinLevel != slog.LevelDebug {
		// slog.LevelInfo is the zero value of slog.Level — disambiguate
		// "operator didn't set it" from "operator set Debug" by treating
		// zero as Info.
		cfg.MinLevel = slog.LevelInfo
	}
	s := &Sink{
		pool:   pool,
		cfg:    cfg,
		cap:    cfg.Capacity,
		buffer: make([]entry, 0, cfg.BatchSize*2),
		wake:   make(chan struct{}, 1),
		stopCh: make(chan struct{}),
	}
	s.flusher.Add(1)
	go s.flushLoop()
	return s
}

// Enabled implements slog.Handler. Records below MinLevel are
// short-circuited — no JSON marshal, no buffer growth.
func (s *Sink) Enabled(_ context.Context, level slog.Level) bool {
	if s.stopped.Load() {
		return false
	}
	return level >= s.cfg.MinLevel
}

// Handle implements slog.Handler. Materialises the record into the
// buffer + returns immediately. Returns nil even on overflow (drop)
// because slog's contract is "best-effort" — propagating an error
// would surface in every caller's chain.
func (s *Sink) Handle(ctx context.Context, r slog.Record) error {
	if !s.Enabled(ctx, r.Level) {
		return nil
	}
	e := entry{
		id: uuid.Must(uuid.NewV7()),
		// Lowercase canonical so the SQL `level` filter (which lower-
		// cases its bind value) matches without `LOWER(level)` wrapping
		// the index column. PB also stores lowercase by convention, so
		// admin-UI level badges render the same shape across backends.
		level:     strings.ToLower(r.Level.String()),
		message:   r.Message,
		createdAt: r.Time.UTC(),
	}
	if ctx != nil {
		if p := authmw.PrincipalFrom(ctx); p.Authenticated() {
			uid := p.UserID
			e.userID = &uid
		}
		if rid, ok := ctx.Value(reqIDKey{}).(string); ok && rid != "" {
			e.requestID = rid
		}
	}
	// Encode attrs once at enqueue so the flusher's hot loop is
	// pure SQL. r.Attrs may walk multiple groups — flatten to a
	// single map for JSONB.
	attrs := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Resolve().Any()
		return true
	})
	if r.PC != 0 {
		// Skip source frames — slog already encodes the call site
		// in stdout output; the DB doesn't need to bloat with it.
	}
	if len(attrs) > 0 {
		b, err := json.Marshal(attrs)
		if err == nil {
			e.attrs = b
		}
	}
	s.enqueue(e)
	return nil
}

// WithAttrs / WithGroup implement slog.Handler. We don't pre-bind
// attrs into the Sink — each entry materialises its own attrs at
// Handle time — so these return s unchanged. Multi-handler wrapper
// applies WithAttrs to its other branch (stdout) so structured
// fields still appear there.
func (s *Sink) WithAttrs(_ []slog.Attr) slog.Handler { return s }
func (s *Sink) WithGroup(_ string) slog.Handler      { return s }

// enqueue pushes an entry. Drops the oldest on overflow.
func (s *Sink) enqueue(e entry) {
	s.mu.Lock()
	if len(s.buffer) >= s.cap {
		// Drop oldest. Slice front so the buffer remains a queue.
		copy(s.buffer, s.buffer[1:])
		s.buffer = s.buffer[:len(s.buffer)-1]
		s.dropped.Add(1)
	}
	s.buffer = append(s.buffer, e)
	batched := len(s.buffer) >= s.cfg.BatchSize
	s.mu.Unlock()
	if batched {
		// Non-blocking wake — flusher loop runs even if we miss.
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// flushLoop drains the buffer on a timer + on demand.
func (s *Sink) flushLoop() {
	defer s.flusher.Done()
	t := time.NewTicker(s.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			s.flush() // final drain
			return
		case <-t.C:
			s.flush()
		case <-s.wake:
			s.flush()
		}
	}
}

// flush persists the current batch. Holds the lock briefly to swap
// buffers, then writes outside the lock so producers aren't blocked
// on the DB.
func (s *Sink) flush() {
	s.mu.Lock()
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.buffer
	s.buffer = make([]entry, 0, s.cfg.BatchSize*2)
	s.mu.Unlock()

	// Build the INSERT. Pgx accepts batch inserts via CopyFrom — but
	// our batches are bounded (≤BatchSize) and we want simple SQL,
	// so a multi-row VALUES literal works fine. Up to 100 rows per
	// flush stays well under Postgres parameter limits (32767 max).
	if err := s.insertBatch(context.Background(), batch); err != nil {
		s.errors.Add(1)
	} else {
		s.written.Add(uint64(len(batch)))
	}
}

func (s *Sink) insertBatch(ctx context.Context, batch []entry) error {
	if len(batch) == 0 {
		return nil
	}
	// Use COPY FROM via batch.SendBatch — keeps the protocol cost
	// to one round-trip regardless of batch size.
	type queryArg struct {
		id        uuid.UUID
		level     string
		message   string
		attrs     []byte
		source    string
		requestID *string
		userID    *uuid.UUID
		createdAt time.Time
	}
	rows := make([][]any, 0, len(batch))
	for _, e := range batch {
		var rid *string
		if e.requestID != "" {
			r := e.requestID
			rid = &r
		}
		attrs := e.attrs
		if attrs == nil {
			attrs = []byte("{}")
		}
		var src *string
		if e.source != "" {
			ss := e.source
			src = &ss
		}
		rows = append(rows, []any{
			e.id, e.level, e.message, attrs, src, rid, e.userID, e.createdAt,
		})
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	src := &copyFromRows{rows: rows, idx: -1}
	_, err = conn.Conn().CopyFrom(ctx,
		[]string{"_logs"},
		[]string{"id", "level", "message", "attrs", "source", "request_id", "user_id", "created"},
		src,
	)
	return err
}

// copyFromRows adapts a pre-materialised slice into pgx.CopyFromSource
// without pulling in a third-party shim. Stateful iterator.
type copyFromRows struct {
	rows [][]any
	idx  int
}

func (c *copyFromRows) Next() bool {
	c.idx++
	return c.idx < len(c.rows)
}

func (c *copyFromRows) Values() ([]any, error) { return c.rows[c.idx], nil }
func (c *copyFromRows) Err() error              { return nil }

// Close stops the flusher + drains pending entries. Idempotent.
// Returns nil after flush even if the final write fails (operator
// gets the gap in the log view, not a shutdown error).
func (s *Sink) Close(ctx context.Context) error {
	if s.stopped.Swap(true) {
		return nil
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	done := make(chan struct{})
	go func() {
		s.flusher.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return errors.New("logs: close timed out before flush")
	}
	return nil
}

// Stats returns a snapshot of the counters.
func (s *Sink) Stats() Stats {
	s.mu.Lock()
	buf := uint64(len(s.buffer))
	s.mu.Unlock()
	return Stats{
		Buffered: buf,
		Written:  s.written.Load(),
		Dropped:  s.dropped.Load(),
		Errors:   s.errors.Load(),
	}
}

// reqIDKey duplicates the chi request-id context-key shape so we
// can read it without importing chi (and creating an import cycle).
// chi's middleware stamps the key under a sentinel value; we mirror
// it. If the upstream definition changes we'd need to refactor.
type reqIDKey struct{}

// WithRequestID stamps rid onto ctx — used by the audit/log hook
// wiring (and exposed here so tests can inject without chi).
func WithRequestID(ctx context.Context, rid string) context.Context {
	return context.WithValue(ctx, reqIDKey{}, rid)
}
