// Package stream ships HTTP streaming-response helpers (§3.9.8).
//
// Three writers:
//
//   - SSEWriter   — Server-Sent Events (text/event-stream), suitable
//                   for AI completion streams, progress bars, log tails.
//   - JSONLWriter — newline-delimited JSON (application/x-ndjson),
//                   suitable for bulk export streams that downstream
//                   tools (jq, pandas) can consume incrementally.
//   - ChunkedWriter — raw chunked transfer, for binary or pre-formatted
//                     bodies (e.g. PDF generation in pieces).
//
// All three:
//   - Flush after every send so the client sees bytes immediately
//     (HTTP/1.1 + chunked OR HTTP/2 frames; works on both).
//   - Wire X-Accel-Buffering: no so Nginx / Cloudflare don't buffer.
//   - Disable Content-Length so the body is open-ended.
//   - Tie completion to the request Context — when the client
//     disconnects, ctx fires; handlers stop generating immediately
//     (critical for LLM cost control: don't pay for tokens nobody
//     receives).
//
// This package never imports realtime/* — the realtime broker has its
// own SSE handler tied to subscriptions. Both can coexist; this one
// is for ad-hoc handler streaming where the broker is overkill.
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrUnflushable is returned by writer constructors when the
// http.ResponseWriter doesn't implement http.Flusher. Modern Go
// servers always satisfy it, but middleware that wraps Writer with a
// non-flushing impl will hit this.
var ErrUnflushable = errors.New("stream: ResponseWriter does not implement http.Flusher")

// --- SSE ---

// SSEWriter wraps an http.ResponseWriter for sending Server-Sent
// Events. Construct via NewSSE; the headers are written immediately
// so the client starts the stream.
//
// Format reminder (one event):
//
//	event: <type>     (optional; default omitted)
//	id: <id>          (optional; supports SSE replay)
//	data: <payload>   (required; newlines split into multiple data:)
//	<blank line>
//
// Spec: https://html.spec.whatwg.org/multipage/server-sent-events.html
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
	mu      sync.Mutex
	closed  bool
}

// NewSSE wires the SSE headers + content type and returns a writer.
// Heartbeat behaviour is up to the caller — start a goroutine with a
// ticker that calls Comment("ping") every 25s if you need it.
func NewSSE(w http.ResponseWriter, r *http.Request) (*SSEWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, ErrUnflushable
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Nginx / Cloudflare otherwise buffer SSE responses, defeating
	// the stream-as-you-go pattern.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &SSEWriter{w: w, flusher: f, ctx: r.Context()}, nil
}

// Send emits one event. `event` is the event name (may be empty for
// the default "message" event); `data` is marshalled to JSON. Newlines
// in the rendered JSON are turned into multiple `data:` lines per
// SSE spec.
//
// Returns the context error if the client disconnected.
func (s *SSEWriter) Send(event string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("stream: marshal sse data: %w", err)
	}
	return s.sendRaw(event, "", body)
}

// SendID emits an event with the optional id field (for SSE replay).
func (s *SSEWriter) SendID(event, id string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("stream: marshal sse data: %w", err)
	}
	return s.sendRaw(event, id, body)
}

// Comment writes a `: <text>` heartbeat. Browsers ignore the line;
// proxies see traffic and don't time the connection out.
func (s *SSEWriter) Comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("stream: writer closed")
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(s.w, ": %s\n\n", text)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) sendRaw(event, id string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("stream: writer closed")
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	var sb strings.Builder
	if event != "" {
		sb.WriteString("event: ")
		sb.WriteString(event)
		sb.WriteByte('\n')
	}
	if id != "" {
		sb.WriteString("id: ")
		sb.WriteString(id)
		sb.WriteByte('\n')
	}
	// SSE spec: each newline in data splits into a new `data:` line.
	for _, line := range strings.Split(string(body), "\n") {
		sb.WriteString("data: ")
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteByte('\n') // blank line terminates the event
	if _, err := s.w.Write([]byte(sb.String())); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Close marks the writer as no longer accepting Send calls. Idempotent.
// Does NOT close the underlying connection — the http server does that
// when the handler returns.
func (s *SSEWriter) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// Context returns the request's context. Handlers should select on
// Context().Done() to stop generating tokens when the client leaves.
func (s *SSEWriter) Context() context.Context { return s.ctx }

// HeartbeatLoop starts a goroutine that sends `: keepalive` comments
// at the given interval. Returns a stop func — it BLOCKS until the
// goroutine has exited, so the caller's subsequent reads of the
// response body are race-free. Typical interval: 25 seconds (under
// most proxy timeouts).
func (s *SSEWriter) HeartbeatLoop(interval time.Duration) (stop func()) {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-s.ctx.Done():
				return
			case <-t.C:
				_ = s.Comment("keepalive")
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

// --- NDJSON / JSON Lines ---

// JSONLWriter writes newline-delimited JSON to the response body, one
// object per call to Write. Suitable for export streams that
// downstream tooling consumes incrementally.
type JSONLWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
	mu      sync.Mutex
	enc     *json.Encoder
	closed  bool
}

// NewJSONL wires the NDJSON headers and returns a writer. Each call
// to Write emits one line.
func NewJSONL(w http.ResponseWriter, r *http.Request) (*JSONLWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, ErrUnflushable
	}
	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	enc := json.NewEncoder(w)
	// Encoder writes a trailing newline by default, which is exactly
	// what we want for NDJSON.
	return &JSONLWriter{w: w, flusher: f, ctx: r.Context(), enc: enc}, nil
}

// Write emits one JSON object followed by a newline + flush. Returns
// the context error if the client disconnected.
func (j *JSONLWriter) Write(v any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("stream: writer closed")
	}
	if err := j.ctx.Err(); err != nil {
		return err
	}
	if err := j.enc.Encode(v); err != nil {
		return err
	}
	j.flusher.Flush()
	return nil
}

// Close marks the writer terminated. Idempotent.
func (j *JSONLWriter) Close() {
	j.mu.Lock()
	j.closed = true
	j.mu.Unlock()
}

// Context returns the request's context.
func (j *JSONLWriter) Context() context.Context { return j.ctx }

// --- chunked binary ---

// ChunkedWriter wraps an http.ResponseWriter for unbounded binary
// streams (PDFs assembled piece-by-piece, exports, etc). Each Write
// flushes immediately.
type ChunkedWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
}

// NewChunked starts a chunked response with the given Content-Type.
// Sets Transfer-Encoding: chunked semantics by NOT writing a
// Content-Length (Go's http server does the rest).
func NewChunked(w http.ResponseWriter, r *http.Request, contentType string) (*ChunkedWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, ErrUnflushable
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &ChunkedWriter{w: w, flusher: f, ctx: r.Context()}, nil
}

// Write emits one chunk and flushes. Returns the context error if
// the client disconnected mid-stream.
func (c *ChunkedWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.w.Write(p)
	if err != nil {
		return n, err
	}
	c.flusher.Flush()
	return n, nil
}

// Context returns the request's context.
func (c *ChunkedWriter) Context() context.Context { return c.ctx }
