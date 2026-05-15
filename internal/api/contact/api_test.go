// Unit tests for the public POST /api/contact endpoint. No DB —
// the mailer is swapped for the console driver that captures sends
// in-process, and the rate-limiter is exercised directly.
package contact

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/mailer"
)

// captureDriver records every Send so tests can assert on what would
// have gone out without standing up an SMTP server.
type captureDriver struct {
	mu   sync.Mutex
	sent []mailer.Message
}

func (c *captureDriver) Name() string { return "capture" }
func (c *captureDriver) Send(ctx context.Context, msg mailer.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, msg)
	return nil
}
func (c *captureDriver) Close() error { return nil }
func (c *captureDriver) sentCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}
func (c *captureDriver) lastSent() mailer.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return mailer.Message{}
	}
	return c.sent[len(c.sent)-1]
}

func newTestServer(t *testing.T, recipient string) (*httptest.Server, *captureDriver) {
	t.Helper()
	drv := &captureDriver{}
	m := mailer.New(mailer.Options{
		Driver:      drv,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DefaultFrom: mailer.Address{Email: "no-reply@test"},
	})
	r := chi.NewRouter()
	Mount(r, &Deps{
		Mailer:         m,
		RecipientEmail: recipient,
		SiteName:       "TestSite",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return httptest.NewServer(r), drv
}

func post(t *testing.T, url string, payload any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(payload)
	resp, err := http.Post(url+"/api/contact", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestContact_HappyPath(t *testing.T) {
	srv, drv := newTestServer(t, "sales@test")
	defer srv.Close()
	code, body := post(t, srv.URL, map[string]string{
		"name":    "Alice",
		"email":   "alice@example.com",
		"subject": "Demo request",
		"message": "Tell me more.",
	})
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d %s", code, body)
	}
	if drv.sentCount() != 1 {
		t.Fatalf("expected 1 email, got %d", drv.sentCount())
	}
	m := drv.lastSent()
	if !strings.Contains(m.Subject, "[TestSite]") || !strings.Contains(m.Subject, "Alice") {
		t.Errorf("subject missing site/name: %q", m.Subject)
	}
	if len(m.To) != 1 || m.To[0].Email != "sales@test" {
		t.Errorf("to = %+v, want sales@test", m.To)
	}
	if m.ReplyTo.Email != "alice@example.com" {
		t.Errorf("reply-to = %+v, want alice@example.com", m.ReplyTo)
	}
	if !strings.Contains(m.Text, "Tell me more.") {
		t.Errorf("text missing message body: %q", m.Text)
	}
}

func TestContact_Validation(t *testing.T) {
	srv, drv := newTestServer(t, "sales@test")
	defer srv.Close()
	bad := []map[string]string{
		{"email": "x@y.z", "message": "hi"},                   // missing name
		{"name": "A", "message": "hi"},                        // missing email
		{"name": "A", "email": "notanemail", "message": "hi"}, // bad email
		{"name": "A", "email": "x@y.z"},                       // missing message
	}
	for i, in := range bad {
		code, body := post(t, srv.URL, in)
		if code/100 != 4 {
			t.Errorf("[%d] %+v: code=%d body=%s (want 4xx)", i, in, code, body)
		}
	}
	if drv.sentCount() != 0 {
		t.Errorf("validation failures must not send any email; sent=%d", drv.sentCount())
	}
}

func TestContact_Honeypot(t *testing.T) {
	srv, drv := newTestServer(t, "sales@test")
	defer srv.Close()
	code, body := post(t, srv.URL, map[string]string{
		"name":    "Botty",
		"email":   "bot@example.com",
		"message": "hi",
		"website": "http://spam.example",
	})
	if code != http.StatusAccepted {
		t.Errorf("honeypot path should still return 202 (bot blindness), got %d %s", code, body)
	}
	if drv.sentCount() != 0 {
		t.Errorf("honeypot triggered → no email expected; got %d", drv.sentCount())
	}
}

func TestContact_NotConfigured(t *testing.T) {
	// Mailer wired but recipient blank → 503.
	drv := &captureDriver{}
	m := mailer.New(mailer.Options{Driver: drv})
	r := chi.NewRouter()
	Mount(r, &Deps{Mailer: m, RecipientEmail: ""})
	srv := httptest.NewServer(r)
	defer srv.Close()
	code, body := post(t, srv.URL, map[string]string{
		"name": "Alice", "email": "alice@example.com", "message": "hi",
	})
	if code != http.StatusServiceUnavailable {
		t.Errorf("missing recipient: %d %s, want 503", code, body)
	}

	// Mailer nil → 503.
	r2 := chi.NewRouter()
	Mount(r2, &Deps{RecipientEmail: "sales@test"})
	srv2 := httptest.NewServer(r2)
	defer srv2.Close()
	code, body = post(t, srv2.URL, map[string]string{
		"name": "Alice", "email": "alice@example.com", "message": "hi",
	})
	if code != http.StatusServiceUnavailable {
		t.Errorf("missing mailer: %d %s, want 503", code, body)
	}
}

func TestContact_RateLimit(t *testing.T) {
	// Build a server with a tighter limiter for the test.
	drv := &captureDriver{}
	m := mailer.New(mailer.Options{Driver: drv})
	r := chi.NewRouter()
	d := &Deps{Mailer: m, RecipientEmail: "sales@test", SiteName: "S"}
	// Inline-build the handler with a small bucket so we don't have
	// to fire 6 requests just to exhaust the default.
	r.Post("/api/contact", d.submit(newLimiter(2, time.Minute)))
	srv := httptest.NewServer(r)
	defer srv.Close()

	in := map[string]string{
		"name": "Alice", "email": "alice@example.com", "message": "hi",
	}
	// First two succeed, third 429-equivalent (CodeRateLimit).
	for i := 0; i < 2; i++ {
		code, body := post(t, srv.URL, in)
		if code != http.StatusAccepted {
			t.Fatalf("[r%d] %d %s", i, code, body)
		}
	}
	code, body := post(t, srv.URL, in)
	if code/100 != 4 {
		t.Errorf("third submission should be rate-limited: %d %s", code, body)
	}
}

func TestLimiter_PerKey(t *testing.T) {
	l := newLimiter(2, 100*time.Millisecond)
	if !l.allow("ip-a") {
		t.Error("first ip-a should pass")
	}
	if !l.allow("ip-a") {
		t.Error("second ip-a should pass")
	}
	if l.allow("ip-a") {
		t.Error("third ip-a should be denied")
	}
	if !l.allow("ip-b") {
		t.Error("ip-b independent bucket should pass")
	}
	time.Sleep(120 * time.Millisecond)
	if !l.allow("ip-a") {
		t.Error("after window expiry, ip-a should pass again")
	}
}
