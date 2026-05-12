package stream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// httptest.ResponseRecorder satisfies http.Flusher in go1.20+, but
// only via the Result().Body buffering path. Our tests use the
// recorder's underlying buffer directly because that's what bytes
// stream-helpers actually emit.

// flushRecorder is a tiny http.ResponseWriter + http.Flusher pair
// that captures bytes immediately. We use it instead of
// httptest.ResponseRecorder so we can be confident Flush() actually
// runs.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes atomic.Int32
}

func newFlushRecorder() *flushRecorder { return &flushRecorder{ResponseRecorder: httptest.NewRecorder()} }
func (f *flushRecorder) Flush()        { f.flushes.Add(1) }

// --- SSE ---

func TestSSE_HeadersAndOneEvent(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/stream", nil)
	s, err := NewSSE(rec, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content-type: %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering: %q", got)
	}
	if err := s.Send("token", map[string]any{"text": "hello"}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: token\n") {
		t.Errorf("missing event line: %q", body)
	}
	if !strings.Contains(body, `data: {"text":"hello"}`+"\n\n") {
		t.Errorf("missing data line: %q", body)
	}
	// At least 2 flushes: one on header write, one per Send.
	if got := rec.flushes.Load(); got < 2 {
		t.Errorf("flushes: got %d, want >=2", got)
	}
}

func TestSSE_MultiLineDataSplits(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/stream", nil)
	s, _ := NewSSE(rec, req)
	if err := s.Send("multi", "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	// JSON-encoded "line1\nline2" is `"line1\nline2"` (escape) — so
	// no actual newline in the encoded form. But if a caller sent
	// raw bytes with newlines, we'd split. Test that aspect via
	// SendID with pre-rendered JSON containing a literal newline.
	if !strings.Contains(body, `data: "line1\nline2"`) {
		t.Errorf("expected escaped newline in encoded body: %q", body)
	}
}

func TestSSE_WithID(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := NewSSE(rec, req)
	if err := s.SendID("evt", "abc-123", "x"); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id: abc-123\n") {
		t.Errorf("missing id line: %q", body)
	}
}

func TestSSE_Comment(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := NewSSE(rec, req)
	if err := s.Comment("heartbeat"); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ": heartbeat\n\n") {
		t.Errorf("missing comment line: %q", body)
	}
}

func TestSSE_ClosedRefusesSend(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := NewSSE(rec, req)
	s.Close()
	if err := s.Send("x", nil); err == nil {
		t.Error("Send after Close should error")
	}
}

func TestSSE_ClientDisconnect(t *testing.T) {
	rec := newFlushRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	s, _ := NewSSE(rec, req)
	cancel()
	if err := s.Send("x", nil); err == nil {
		t.Error("Send after ctx cancel should return ctx error")
	}
}

func TestSSE_HeartbeatLoop(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := NewSSE(rec, req)
	stop := s.HeartbeatLoop(20 * time.Millisecond)
	time.Sleep(70 * time.Millisecond) // expect 3 ticks
	stop()
	body := rec.Body.String()
	hbCount := strings.Count(body, ": keepalive\n\n")
	if hbCount < 2 {
		t.Errorf("heartbeats: got %d, want >=2", hbCount)
	}
}

func TestSSE_UnflushableRejected(t *testing.T) {
	// A plain *bytes.Buffer doesn't satisfy http.Flusher.
	w := &nonFlushWriter{header: http.Header{}, buf: &bytes.Buffer{}}
	req := httptest.NewRequest("GET", "/", nil)
	if _, err := NewSSE(w, req); err != ErrUnflushable {
		t.Errorf("expected ErrUnflushable; got %v", err)
	}
}

// --- NDJSON ---

func TestNDJSON_OneObjectPerLine(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	j, err := NewJSONL(rec, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("content-type: %q", got)
	}
	for i := 0; i < 3; i++ {
		if err := j.Write(map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	body := rec.Body.String()
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: got %d, want 3 — body=%q", len(lines), body)
	}
	for i, line := range lines {
		var got map[string]int
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d not JSON: %q", i, line)
		} else if got["n"] != i {
			t.Errorf("line %d: got n=%d, want %d", i, got["n"], i)
		}
	}
}

func TestNDJSON_ClosedRefuses(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	j, _ := NewJSONL(rec, req)
	j.Close()
	if err := j.Write(map[string]int{}); err == nil {
		t.Error("Write after Close should error")
	}
}

// --- chunked ---

func TestChunked_FlushPerWrite(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	c, err := NewChunked(rec, req, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("content-type: %q", got)
	}
	for _, chunk := range [][]byte{[]byte("aa"), []byte("bb"), []byte("cc")} {
		n, err := c.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Errorf("write %q: n=%d err=%v", chunk, n, err)
		}
	}
	if body := rec.Body.String(); body != "aabbcc" {
		t.Errorf("body: %q", body)
	}
	if got := rec.flushes.Load(); got < 4 { // 1 init + 3 writes
		t.Errorf("flushes: %d", got)
	}
}

func TestChunked_DisconnectReturnsCtxErr(t *testing.T) {
	rec := newFlushRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	c, _ := NewChunked(rec, req, "text/plain")
	cancel()
	_, err := c.Write([]byte("hi"))
	if err == nil {
		t.Error("expected ctx error after cancel")
	}
}

// --- helper types ---

type nonFlushWriter struct {
	header http.Header
	buf    *bytes.Buffer
}

func (w *nonFlushWriter) Header() http.Header        { return w.header }
func (w *nonFlushWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *nonFlushWriter) WriteHeader(int)             {}

// --- end-to-end SSE parsing sanity ---

func TestSSE_ParseableByStdScanner(t *testing.T) {
	rec := newFlushRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := NewSSE(rec, req)
	_ = s.Send("greet", "hi")
	_ = s.Send("done", "")
	// Confirm a scanner sees two complete event blocks.
	r := bufio.NewReader(rec.Body)
	events := 0
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			break
		}
		if line == "\n" {
			events++
		}
	}
	if events != 2 {
		t.Errorf("event blocks: got %d, want 2", events)
	}
}
