package adminapi

// Pure-unit tests for the v1.7.43 setup-mailer handlers.
//
// These tests don't need embedded PG — the path under test is body
// validation + driver construction + hint mapping. The /mailer-save
// path that actually writes to _settings is covered by the embed_pg
// counterpart in setup_mailer_embed_test.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newSetupMailerRouter(d *Deps) chi.Router {
	r := chi.NewRouter()
	d.mountSetupMailer(r)
	return r
}

// TestSetupMailer_Status_NilSettings — when Deps.Settings is nil
// (setup-mode boot), the handler returns a clean status payload with
// mailer_required=true so the wizard renders the form.
func TestSetupMailer_Status_NilSettings(t *testing.T) {
	d := &Deps{}
	r := newSetupMailerRouter(d)
	req := httptest.NewRequest(http.MethodGet, "/_setup/mailer-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	var resp setupMailerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.MailerRequired {
		t.Errorf("mailer_required: want true (default invariant), got false")
	}
	if resp.ConfiguredAt != "" {
		t.Errorf("configured_at: want empty in fresh state, got %q", resp.ConfiguredAt)
	}
}

// TestSetupMailer_Probe_BadDriver — body validation catches unknown
// driver values before the probe attempts to construct anything.
func TestSetupMailer_Probe_BadDriver(t *testing.T) {
	d := &Deps{}
	r := newSetupMailerRouter(d)
	body, _ := json.Marshal(setupMailerBody{
		Driver:      "carrier-pigeon",
		FromAddress: "x@y.com",
		ProbeTo:     "a@b.com",
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/mailer-probe",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 for unknown driver, got %d body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "driver must be one of") {
		t.Errorf("body: want 'driver must be one of' hint, got %s",
			rec.Body.String())
	}
}

// TestSetupMailer_Probe_SMTPRequiresHost — driver=smtp without host
// is a 400 even though port and from_address are well-formed.
func TestSetupMailer_Probe_SMTPRequiresHost(t *testing.T) {
	d := &Deps{}
	r := newSetupMailerRouter(d)
	body, _ := json.Marshal(setupMailerBody{
		Driver:      "smtp",
		SMTPPort:    587,
		FromAddress: "x@y.com",
		ProbeTo:     "a@b.com",
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/mailer-probe",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "smtp_host is required") {
		t.Errorf("body: want host-required hint, got %s", rec.Body.String())
	}
}

// TestSetupMailer_Probe_RequiresProbeTo — probe path with all SMTP
// fields but no probe_to is a 400 (we always want a destination for
// the test send).
func TestSetupMailer_Probe_RequiresProbeTo(t *testing.T) {
	d := &Deps{}
	r := newSetupMailerRouter(d)
	body, _ := json.Marshal(setupMailerBody{
		Driver:      "smtp",
		SMTPHost:    "smtp.example.com",
		SMTPPort:    587,
		FromAddress: "x@y.com",
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/mailer-probe",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "probe_to is required") {
		t.Errorf("body: want probe_to-required hint, got %s", rec.Body.String())
	}
}

// TestSetupMailer_Probe_ConsoleDriver — driver=console with a valid
// shape returns ok=true. Console "send" writes to a discard sink so
// we can verify the wiring without polluting test output.
func TestSetupMailer_Probe_ConsoleDriver(t *testing.T) {
	d := &Deps{}
	r := newSetupMailerRouter(d)
	body, _ := json.Marshal(setupMailerBody{
		Driver:      "console",
		FromAddress: "x@y.com",
		ProbeTo:     "a@b.com",
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/mailer-probe",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupMailerProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("ok: want true for console driver, got false (err=%s)", resp.Error)
	}
	if resp.Driver != "console" {
		t.Errorf("driver: want 'console', got %q", resp.Driver)
	}
}

// TestSetupMailer_Probe_SMTPUnreachable — pointing at a port we know
// nothing listens on returns ok=false with a connection-refused hint.
func TestSetupMailer_Probe_SMTPUnreachable(t *testing.T) {
	d := &Deps{}
	r := newSetupMailerRouter(d)
	body, _ := json.Marshal(setupMailerBody{
		Driver:      "smtp",
		SMTPHost:    "127.0.0.1",
		SMTPPort:    1, // ICMP-only port; nothing listens
		FromAddress: "x@y.com",
		ProbeTo:     "a@b.com",
		TLS:         "off",
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/mailer-probe",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 with ok=false payload, got %d", rec.Code)
	}
	var resp setupMailerProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Errorf("ok: want false for unreachable host, got true")
	}
	if resp.Error == "" {
		t.Errorf("error: want populated, got empty")
	}
}

// TestSetupMailer_HintMapping_CommonErrors — the hint mapper covers
// the failure modes most likely to bite operators.
func TestSetupMailer_HintMapping_CommonErrors(t *testing.T) {
	cases := []struct {
		errMsg   string
		wantHint string
	}{
		{"dial tcp: lookup smtp.invalid: no such host", "couldn't be resolved"},
		{"dial tcp 127.0.0.1:1: connect: connection refused", "Nothing is listening"},
		{"535 5.7.8 Authentication failed", "authentication failed"},
		{"tls: handshake failure", "TLS handshake failed"},
		{"context deadline exceeded", "Connection timed out"},
		{"550 5.7.1 Sender address rejected: not owned by user", "Server rejected the From address"},
		{"some other random transport error", "See the error message above"},
	}
	for _, tc := range cases {
		got := mailerProbeHint(tc.errMsg)
		if !strings.Contains(strings.ToLower(got), strings.ToLower(tc.wantHint)) {
			t.Errorf("mailerProbeHint(%q):\n  got:  %q\n  want substring: %q",
				tc.errMsg, got, tc.wantHint)
		}
	}
}
