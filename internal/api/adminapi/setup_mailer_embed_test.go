//go:build embed_pg

package adminapi

// E2E tests for v1.7.43 — covers the mailer-save persistence path,
// the bootstrap mailer-gate (412 PreconditionFailed when neither
// configured nor skipped), and the retry sweeper. Piggybacks on the
// shared emEventsPool TestMain.
//
// Run:
//
//	go test -race -count=1 -tags embed_pg ./internal/api/adminapi/... -run TestSetupMailerEmbed
//	go test -race -count=1 -tags embed_pg ./internal/api/adminapi/... -run TestBootstrapMailerGate

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/settings"
)

// fakeKey returns a fixed 32-byte secret.Key for test use. The session
// store hashes session tokens with this key; tests don't need a real
// production-grade rotation, just a stable value.
func fakeKey() secret.Key {
	var k secret.Key
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

// newSetupMailerDeps builds a Deps with Settings + Pool wired off the
// shared embed-PG pool. Other handler-specific fields are left nil —
// the setup-mailer + bootstrap paths only need these two.
func newSetupMailerDeps(t *testing.T) *Deps {
	t.Helper()
	if emEventsPool == nil {
		t.Skip("shared embed_pg pool not wired")
	}
	mgr := settings.New(settings.Options{Pool: emEventsPool})
	return &Deps{
		Pool:     emEventsPool,
		Settings: mgr,
	}
}

// clearMailerSettings wipes any prior state from a previous test
// case. Tests in this file share the same _settings table because
// they share the embed-PG pool.
func clearMailerSettings(t *testing.T, d *Deps) {
	t.Helper()
	ctx := context.Background()
	keys := []string{
		"mailer.configured_at",
		"mailer.setup_skipped_at",
		"mailer.setup_skipped_reason",
		"mailer.driver",
		"mailer.from",
		"mailer.smtp.host",
		"mailer.smtp.port",
		"mailer.smtp.tls",
	}
	for _, k := range keys {
		_ = d.Settings.Delete(ctx, k)
	}
}

// TestSetupMailerEmbed_Skip_PersistsFlags — POST /_setup/mailer-skip
// with a non-empty reason writes mailer.setup_skipped_at AND
// mailer.setup_skipped_reason to _settings. The status endpoint
// reflects both fields on the next read.
func TestSetupMailerEmbed_Skip_PersistsFlags(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupMailer(r)

	// Skip request.
	body, _ := json.Marshal(setupMailerSkipBody{
		Reason: "SMTP credentials still pending from infra team",
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/mailer-skip",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("skip: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Read status.
	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/mailer-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", statusRec.Code)
	}
	var status setupMailerStatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.SkippedAt == "" {
		t.Errorf("skipped_at: want populated after skip, got empty")
	}
	if status.SkippedReason != "SMTP credentials still pending from infra team" {
		t.Errorf("skipped_reason: want exact match, got %q", status.SkippedReason)
	}
}

// TestSetupMailerEmbed_Save_PersistsSMTP — POST /_setup/mailer-save
// with a valid SMTP body writes every key. We don't probe-test here
// (probe requires a reachable SMTP server); we just verify the
// persistence path is wired correctly.
func TestSetupMailerEmbed_Save_PersistsSMTP(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupMailer(r)

	body, _ := json.Marshal(setupMailerBody{
		Driver:      "smtp",
		FromAddress: "railbase@example.com",
		SMTPHost:    "smtp.example.com",
		SMTPPort:    587,
		SMTPUser:    "user",
		SMTPPass:    "pass",
		TLS:         "starttls",
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/mailer-save",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Verify every key landed in _settings.
	ctx := context.Background()
	configuredAt, ok, _ := d.Settings.GetString(ctx, "mailer.configured_at")
	if !ok || configuredAt == "" {
		t.Errorf("mailer.configured_at: want populated, got %q ok=%v", configuredAt, ok)
	}
	driver, _, _ := d.Settings.GetString(ctx, "mailer.driver")
	if driver != "smtp" {
		t.Errorf("mailer.driver: want 'smtp', got %q", driver)
	}
	host, _, _ := d.Settings.GetString(ctx, "mailer.smtp.host")
	if host != "smtp.example.com" {
		t.Errorf("mailer.smtp.host: want match, got %q", host)
	}
	port, _, _ := d.Settings.GetInt(ctx, "mailer.smtp.port")
	if port != 587 {
		t.Errorf("mailer.smtp.port: want 587, got %d", port)
	}
}

// TestSetupMailerEmbed_Save_ClearsSkip — saving a mailer config after
// a prior skip clears the skipped_at flag (and reason) — operator
// intent has reversed.
func TestSetupMailerEmbed_Save_ClearsSkip(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupMailer(r)

	// Skip first.
	skipBody, _ := json.Marshal(setupMailerSkipBody{Reason: "initial"})
	skipReq := httptest.NewRequest(http.MethodPost, "/_setup/mailer-skip",
		bytes.NewReader(skipBody))
	r.ServeHTTP(httptest.NewRecorder(), skipReq)

	// Confirm skip is set.
	skipped, _, _ := d.Settings.GetString(context.Background(), "mailer.setup_skipped_at")
	if skipped == "" {
		t.Fatalf("skipped_at not set after skip — test fixture broken")
	}

	// Now save.
	saveBody, _ := json.Marshal(setupMailerBody{
		Driver:      "console",
		FromAddress: "x@y.com",
	})
	saveReq := httptest.NewRequest(http.MethodPost, "/_setup/mailer-save",
		bytes.NewReader(saveBody))
	saveRec := httptest.NewRecorder()
	r.ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save: want 200, got %d body=%s", saveRec.Code, saveRec.Body.String())
	}

	// Skip flag should now be gone.
	skippedAfter, ok, _ := d.Settings.GetString(context.Background(), "mailer.setup_skipped_at")
	if ok && skippedAfter != "" {
		t.Errorf("skipped_at: want cleared after successful save, got %q", skippedAfter)
	}
}

// TestBootstrapMailerGate_RefusesWhenNeitherSet — POST /_bootstrap
// when there's no mailer.configured_at AND no mailer.setup_skipped_at
// returns 412 PreconditionFailed. The gate fires BEFORE the email/
// password validation.
func TestBootstrapMailerGate_RefusesWhenNeitherSet(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)
	// Clear any admins from prior tests so the bootstrap-count check
	// reports 0 and we get to the mailer gate.
	emEventsPool.Exec(context.Background(), `DELETE FROM _admins`)

	d.Admins = admins.NewStore(emEventsPool)
	d.Sessions = admins.NewSessionStore(emEventsPool, fakeKey())

	r := chi.NewRouter()
	r.Get("/_bootstrap", d.bootstrapProbeHandler)
	r.Post("/_bootstrap", d.bootstrapCreateHandler)

	body, _ := json.Marshal(map[string]string{
		"email":    "first@example.com",
		"password": "ValidPass123!",
	})
	req := httptest.NewRequest(http.MethodPost, "/_bootstrap", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status: want 412 PreconditionFailed, got %d body=%s",
			rec.Code, rec.Body.String())
	}
	// Verify the admin was NOT created (gate fires pre-INSERT).
	count, _ := d.Admins.Count(context.Background())
	if count != 0 {
		t.Errorf("admins count: want 0 (no admin should have been inserted), got %d", count)
	}
}

// TestBootstrapMailerGate_AcceptsWhenConfigured — once
// mailer.configured_at is set, the gate lets bootstrap-create through
// (and the admin actually lands).
func TestBootstrapMailerGate_AcceptsWhenConfigured(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)
	emEventsPool.Exec(context.Background(), `DELETE FROM _admins`)
	emEventsPool.Exec(context.Background(), `DELETE FROM _jobs`) // clean any prior welcome jobs

	d.Admins = admins.NewStore(emEventsPool)
	d.Sessions = admins.NewSessionStore(emEventsPool, fakeKey())

	// Configure mailer (sets mailer.configured_at).
	_ = d.Settings.Set(context.Background(), "mailer.configured_at", "2026-05-13T10:00:00Z")
	_ = d.Settings.Set(context.Background(), "mailer.driver", "console")
	_ = d.Settings.Set(context.Background(), "mailer.from", "railbase@example.com")

	// v1.7.47+: bootstrap now also requires the auth-methods step to be
	// configured OR skipped (parallel gate to mailer). This test is
	// asserting the MAILER gate path, so stamp auth as configured to
	// pass the orthogonal auth gate.
	_ = d.Settings.Set(context.Background(), "auth.configured_at", "2026-05-13T10:00:00Z")

	r := chi.NewRouter()
	r.Get("/_bootstrap", d.bootstrapProbeHandler)
	r.Post("/_bootstrap", d.bootstrapCreateHandler)

	body, _ := json.Marshal(map[string]string{
		"email":    "first@example.com",
		"password": "ValidPass123!",
	})
	req := httptest.NewRequest(http.MethodPost, "/_bootstrap", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (mailer configured), got %d body=%s",
			rec.Code, rec.Body.String())
	}
	count, _ := d.Admins.Count(context.Background())
	if count != 1 {
		t.Errorf("admins count: want 1 after successful bootstrap, got %d", count)
	}

	// Welcome email job should have been enqueued.
	var jobCount int
	emEventsPool.QueryRow(context.Background(), `
		SELECT count(*) FROM _jobs
		 WHERE kind = 'send_email_async'
		   AND payload->>'template' = 'admin_welcome'
	`).Scan(&jobCount)
	if jobCount != 1 {
		t.Errorf("welcome email job count: want 1 after bootstrap, got %d", jobCount)
	}
}

// TestBootstrapMailerGate_AcceptsWhenSkipped — explicit skip is the
// other gate-passing condition. No welcome email enqueued when
// skipped — skip means "don't send to anyone".
func TestBootstrapMailerGate_AcceptsWhenSkipped(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)
	emEventsPool.Exec(context.Background(), `DELETE FROM _admins`)
	emEventsPool.Exec(context.Background(), `DELETE FROM _jobs`)

	d.Admins = admins.NewStore(emEventsPool)
	d.Sessions = admins.NewSessionStore(emEventsPool, fakeKey())

	_ = d.Settings.Set(context.Background(), "mailer.setup_skipped_at", "2026-05-13T10:00:00Z")
	_ = d.Settings.Set(context.Background(), "mailer.setup_skipped_reason", "deferred")
	// v1.7.47+: pass the parallel auth-methods gate by recording a skip.
	_ = d.Settings.Set(context.Background(), "auth.setup_skipped_at", "2026-05-13T10:00:00Z")
	_ = d.Settings.Set(context.Background(), "auth.setup_skipped_reason", "deferred")

	r := chi.NewRouter()
	r.Get("/_bootstrap", d.bootstrapProbeHandler)
	r.Post("/_bootstrap", d.bootstrapCreateHandler)

	body, _ := json.Marshal(map[string]string{
		"email":    "first@example.com",
		"password": "ValidPass123!",
	})
	req := httptest.NewRequest(http.MethodPost, "/_bootstrap", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (mailer skipped), got %d body=%s",
			rec.Code, rec.Body.String())
	}
	count, _ := d.Admins.Count(context.Background())
	if count != 1 {
		t.Errorf("admins count: want 1, got %d", count)
	}

	// NO welcome email should be enqueued when skipped.
	var jobCount int
	emEventsPool.QueryRow(context.Background(), `
		SELECT count(*) FROM _jobs
		 WHERE kind = 'send_email_async'
		   AND payload->>'template' = 'admin_welcome'
	`).Scan(&jobCount)
	if jobCount != 0 {
		t.Errorf("welcome job count when mailer skipped: want 0, got %d", jobCount)
	}
}
