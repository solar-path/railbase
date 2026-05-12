package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railbase/railbase/internal/security"
)

// TestServer_SecurityHeadersWired confirms that passing
// SecurityHeaders to server.New results in those headers landing on
// every response, including 404s. Caught a regression once where the
// middleware was registered AFTER the /healthz handler — only the
// route paths inherited it.
func TestServer_SecurityHeadersWired(t *testing.T) {
	opts := security.DefaultHeadersOptions()
	s := New(Config{
		Addr:            ":0",
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Build:           "test",
		SecurityHeaders: &opts,
		Probes: Probes{
			Live:  func(context.Context) error { return nil },
			Ready: func(context.Context) error { return nil },
		},
	})

	cases := []struct {
		name string
		path string
		want int
	}{
		{"healthz emits headers", "/healthz", 200},
		{"unknown emits headers", "/does-not-exist", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			if rec.Code != tc.want {
				t.Errorf("status: got %d, want %d", rec.Code, tc.want)
			}
			if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options: got %q, want DENY", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options: got %q, want nosniff", got)
			}
		})
	}
}

// TestServer_IPFilterWired confirms denied IPs return 403 BEFORE
// reaching the probe handler — the IP filter sits high in the chain.
func TestServer_IPFilterWired(t *testing.T) {
	f, err := security.NewIPFilter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Update(nil, []string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{
		Addr:     ":0",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Build:    "test",
		IPFilter: f,
		Probes: Probes{
			Live:  func(context.Context) error { return nil },
			Ready: func(context.Context) error { return nil },
		},
	})

	// Denied IP — even on /healthz.
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "10.1.1.1:1"
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("denied IP on /healthz: got %d, want 403", rec.Code)
	}

	// Allowed IP — passes through.
	req = httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "203.0.113.5:1"
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("allowed IP on /healthz: got %d, want 200", rec.Code)
	}
}

// TestServer_NoSecurityWhenNil confirms that nil SecurityHeaders +
// nil IPFilter = pass-through (dev default). Caught a regression
// where a typed-nil interface bug made the middleware always-on.
func TestServer_NoSecurityWhenNil(t *testing.T) {
	s := New(Config{
		Addr:  ":0",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Build: "test",
		Probes: Probes{
			Live:  func(context.Context) error { return nil },
			Ready: func(context.Context) error { return nil },
		},
	})
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "10.1.1.1:1" // would-be-denied under deny rules
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("nil security: got %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options leaked when SecurityHeaders nil: %q", got)
	}
}
