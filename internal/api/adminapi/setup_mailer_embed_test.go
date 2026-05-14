//go:build embed_pg

package adminapi

// E2E tests for the mailer-save persistence path and the bootstrap
// welcome-email enqueue behaviour. Piggybacks on the shared
// emEventsPool TestMain.
//
// Run:
//
//	go test -race -count=1 -tags embed_pg ./internal/api/adminapi/... -run TestSetupMailerEmbed
//	go test -race -count=1 -tags embed_pg ./internal/api/adminapi/... -run TestBootstrap

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

// TestSetupMailerEmbed_Save_ClearsLegacySkip — saving a mailer config
// clears any legacy mailer.setup_skipped_at flag. The v0.9 wizard no
// longer writes that flag, but a pre-v0.9 install may carry it; a
// successful save should not leave the install reporting "skipped".
func TestSetupMailerEmbed_Save_ClearsLegacySkip(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupMailer(r)

	// Simulate a pre-v0.9 install that has the legacy skip flag set.
	ctx := context.Background()
	_ = d.Settings.Set(ctx, "mailer.setup_skipped_at", "2026-05-13T10:00:00Z")
	skipped, _, _ := d.Settings.GetString(ctx, "mailer.setup_skipped_at")
	if skipped == "" {
		t.Fatalf("skipped_at not set — test fixture broken")
	}

	// Now save a config.
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

	// Legacy skip flag should now be gone.
	skippedAfter, ok, _ := d.Settings.GetString(ctx, "mailer.setup_skipped_at")
	if ok && skippedAfter != "" {
		t.Errorf("skipped_at: want cleared after successful save, got %q", skippedAfter)
	}
}

// TestBootstrap_SucceedsWithoutMailerFlags — v0.9 regression. The
// mailer gate (mailerGateError) used to return 412 when neither
// mailer.configured_at nor mailer.setup_skipped_at was set; bootstrap
// would refuse. After the v0.9 IA simplification (mailer config moved
// to Settings, wizard reduced to DB + admin), admin creation no longer
// depends on these flags — POST /_bootstrap succeeds and the welcome
// email is enqueued because no skip flag is set.
func TestBootstrap_SucceedsWithoutMailerFlags(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)
	emEventsPool.Exec(context.Background(), `DELETE FROM _admins`)
	emEventsPool.Exec(context.Background(), `DELETE FROM _jobs`)

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

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (gate removed in v0.9), got %d body=%s",
			rec.Code, rec.Body.String())
	}
	count, _ := d.Admins.Count(context.Background())
	if count != 1 {
		t.Errorf("admins count: want 1 after successful bootstrap, got %d", count)
	}
}

// TestBootstrap_EnqueuesWelcomeWhenMailerConfigured — when the mailer
// is configured and no skip flag is present, bootstrap-create succeeds
// and the admin-welcome email job is enqueued.
func TestBootstrap_EnqueuesWelcomeWhenMailerConfigured(t *testing.T) {
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

// TestBootstrap_SuppressesWelcomeWhenLegacySkipSet — a pre-v0.9 install
// may still carry the legacy mailer.setup_skipped_at flag. Bootstrap
// still succeeds, but enqueueAdminEmails honors the flag and suppresses
// the welcome email — skip means "don't send to anyone".
func TestBootstrap_SuppressesWelcomeWhenLegacySkipSet(t *testing.T) {
	d := newSetupMailerDeps(t)
	clearMailerSettings(t, d)
	emEventsPool.Exec(context.Background(), `DELETE FROM _admins`)
	emEventsPool.Exec(context.Background(), `DELETE FROM _jobs`)

	d.Admins = admins.NewStore(emEventsPool)
	d.Sessions = admins.NewSessionStore(emEventsPool, fakeKey())

	_ = d.Settings.Set(context.Background(), "mailer.setup_skipped_at", "2026-05-13T10:00:00Z")

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
		t.Fatalf("status: want 200 (bootstrap never gated on mailer), got %d body=%s",
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
