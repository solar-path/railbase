//go:build embed_pg

package adminapi

// v1.7.36 — E2E for the digest-preview admin endpoint.
//
// Closes the v1.7.36 "digest preview deferred" follow-up. Companion to
// the handler-shape tests in notifications_prefs_test.go (default-tag,
// nil-pool short-circuit) — these tests stand up a real Postgres via
// the shared TestMain pool defined in email_events_test.go so the
// `_notification_deferred` → `_notifications` join + the `_admins`
// recipient-lookup paths get exercised against the actual schema.
//
// Run:
//   go test -race -count=1 -tags embed_pg ./internal/api/adminapi/...

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/notifications"
)

// previewMailer captures every SendTemplate call the digest-preview
// endpoint emits. Mirrors captureMailer in
// internal/notifications/quiet_hours_test.go but local to this package
// so the e2e tests don't pull in a cross-package test helper.
type previewMailer struct {
	mu    sync.Mutex
	calls []previewCall
}

type previewCall struct {
	To       string
	Template string
	Data     map[string]any
}

func (m *previewMailer) SendTemplate(_ context.Context, to string, template string, data map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, previewCall{To: to, Template: template, Data: data})
	return nil
}

func (m *previewMailer) snapshot() []previewCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]previewCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// seedAdmin inserts one admin row with a deterministic password hash
// shape — we don't go through admins.Store.Create because that runs
// argon2id and we just need a row to hang an email off. Returns the
// id so the test can stamp it into an AdminPrincipal.
func seedAdmin(t *testing.T, email string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := emEventsPool.Exec(emEventsCtx, `
		INSERT INTO _admins (id, email, password_hash)
		VALUES ($1, $2, $3)`,
		id, email, "$argon2id$test-only-placeholder",
	); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	t.Cleanup(func() {
		_, _ = emEventsPool.Exec(emEventsCtx, `DELETE FROM _admins WHERE id = $1`, id)
	})
	return id
}

// seedDigestDeferral inserts one notification row + one matching
// deferred row with reason='digest' for the given user. Returns the
// notification id so the test can validate cleanup behaviour.
func seedDigestDeferral(t *testing.T, userID uuid.UUID, title, body string) uuid.UUID {
	t.Helper()
	notifID := uuid.Must(uuid.NewV7())
	if _, err := emEventsPool.Exec(emEventsCtx, `
		INSERT INTO _notifications (id, user_id, kind, title, body, data, priority)
		VALUES ($1, $2, 'digest_preview_test', $3, $4, '{}'::jsonb, 'normal')`,
		notifID, userID, title, body,
	); err != nil {
		t.Fatalf("seed notification: %v", err)
	}
	deferredID := uuid.Must(uuid.NewV7())
	if _, err := emEventsPool.Exec(emEventsCtx, `
		INSERT INTO _notification_deferred (id, user_id, notification_id, deferred_at, flush_after, reason)
		VALUES ($1, $2, $3, now(), now() + interval '1 hour', 'digest')`,
		deferredID, userID, notifID,
	); err != nil {
		t.Fatalf("seed deferred: %v", err)
	}
	t.Cleanup(func() {
		_, _ = emEventsPool.Exec(emEventsCtx, `DELETE FROM _notification_deferred WHERE id = $1`, deferredID)
		_, _ = emEventsPool.Exec(emEventsCtx, `DELETE FROM _notifications WHERE id = $1`, notifID)
	})
	return notifID
}

// seedUserSettings stamps a digest_mode row for a user so the preview
// handler resolves a sensible Mode for the email body. Without this
// the handler falls back to "daily" — covered by the fake-data test.
func seedUserSettings(t *testing.T, userID uuid.UUID, mode string) {
	t.Helper()
	if _, err := emEventsPool.Exec(emEventsCtx, `
		INSERT INTO _notification_user_settings (user_id, digest_mode, digest_hour, digest_dow)
		VALUES ($1, $2, 8, 1)
		ON CONFLICT (user_id) DO UPDATE SET digest_mode = EXCLUDED.digest_mode`,
		userID, mode,
	); err != nil {
		t.Fatalf("seed user settings: %v", err)
	}
	t.Cleanup(func() {
		_, _ = emEventsPool.Exec(emEventsCtx, `DELETE FROM _notification_user_settings WHERE user_id = $1`, userID)
	})
}

// callDigestPreview drives one POST against the handler with an
// AdminPrincipal stamped into ctx (so RequireAdmin would pass — though
// we bypass it here by calling the handler directly). Returns the
// decoded response envelope + status code.
func callDigestPreview(t *testing.T, d *Deps, adminID, userID uuid.UUID, body string) (int, digestPreviewResponse) {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/notifications/users/{user_id}/digest-preview", d.notificationsDigestPreviewHandler)

	var buf *bytes.Buffer
	if body != "" {
		buf = bytes.NewBufferString(body)
	} else {
		buf = &bytes.Buffer{}
	}
	req := httptest.NewRequest(http.MethodPost,
		"/notifications/users/"+userID.String()+"/digest-preview", buf)
	// Stamp the AdminPrincipal so the recipient-defaulting branch can
	// load the admin's email. Without this, the handler returns 400.
	req = req.WithContext(WithAdminPrincipal(req.Context(), AdminPrincipal{
		AdminID:   adminID,
		SessionID: uuid.Must(uuid.NewV7()),
	}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var resp digestPreviewResponse
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	}
	return rec.Code, resp
}

// TestDigestPreview_SendsEmail — happy path. Seed two queued digest
// deferrals for a freshly-created user, hit the endpoint with an
// explicit recipient, assert one SendTemplate call landed with the
// `digest_preview` template + the expected recipient + Mode + 2 items.
//
// The subject prefix `[Preview]` lives in the template frontmatter
// (digest_preview.md) rather than the handler — so this test asserts
// on the (template name, recipient, item count) tuple; rendering the
// frontmatter is a mailer.Templates concern covered elsewhere.
func TestDigestPreview_SendsEmail(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	if emEventsPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}

	adminID := seedAdmin(t, "admin-preview-sends@example.com")
	userID := uuid.Must(uuid.NewV7())
	seedUserSettings(t, userID, "daily")
	_ = seedDigestDeferral(t, userID, "Queued item A", "Body A")
	_ = seedDigestDeferral(t, userID, "Queued item B", "Body B")

	mailer := &previewMailer{}
	d := &Deps{
		Pool:   emEventsPool,
		Admins: admins.NewStore(emEventsPool),
		Mailer: mailer,
	}

	code, resp := callDigestPreview(t, d, adminID, userID,
		`{"recipient":"alice@example.com"}`)
	if code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", code)
	}
	if !resp.Sent {
		t.Errorf("response.sent: want true")
	}
	if resp.Recipient != "alice@example.com" {
		t.Errorf("response.recipient: want alice@example.com, got %q", resp.Recipient)
	}
	if resp.KindCount != 2 {
		t.Errorf("response.kind_count: want 2, got %d", resp.KindCount)
	}

	calls := mailer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("mailer calls: want 1, got %d", len(calls))
	}
	c := calls[0]
	if c.To != "alice@example.com" {
		t.Errorf("mailer.To: want alice@example.com, got %q", c.To)
	}
	if c.Template != "digest_preview" {
		t.Errorf("mailer.Template: want digest_preview, got %q", c.Template)
	}
	if mode, ok := c.Data["Mode"].(string); !ok || mode != "daily" {
		t.Errorf("data.Mode: want daily, got %v", c.Data["Mode"])
	}
	if count, ok := c.Data["Count"].(int); !ok || count != 2 {
		t.Errorf("data.Count: want 2, got %v", c.Data["Count"])
	}
	items, ok := c.Data["Items"].([]notifications.DigestItem)
	if !ok {
		t.Fatalf("data.Items: wrong type %T", c.Data["Items"])
	}
	if len(items) != 2 {
		t.Errorf("data.Items len: want 2, got %d", len(items))
	}
	// Queued rows are loaded newest-first; B was inserted second so it
	// should sort first.
	titles := []string{items[0].Title, items[1].Title}
	if !containsString(titles, "Queued item A") || !containsString(titles, "Queued item B") {
		t.Errorf("data.Items titles: want both queued items, got %v", titles)
	}
}

// TestDigestPreview_FakeDataFallback — user has zero queued digest
// rows; the handler synthesises three "Sample notification" items so
// the operator can still eyeball the layout. Pin the exact count + the
// title prefix so the synth path stays stable across refactors.
func TestDigestPreview_FakeDataFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	if emEventsPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}

	adminID := seedAdmin(t, "admin-preview-fake@example.com")
	userID := uuid.Must(uuid.NewV7())
	// Settings row gives the user a registered presence even though no
	// queued rows exist — without this the 404 branch fires (covered
	// by TestDigestPreview_UnknownUser_404 instead).
	seedUserSettings(t, userID, "weekly")

	mailer := &previewMailer{}
	d := &Deps{
		Pool:   emEventsPool,
		Admins: admins.NewStore(emEventsPool),
		Mailer: mailer,
	}

	code, resp := callDigestPreview(t, d, adminID, userID,
		`{"recipient":"fallback@example.com"}`)
	if code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", code)
	}
	if resp.KindCount != 3 {
		t.Errorf("kind_count: want 3 (synth fallback), got %d", resp.KindCount)
	}

	calls := mailer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("mailer calls: want 1, got %d", len(calls))
	}
	c := calls[0]
	if c.Template != "digest_preview" {
		t.Errorf("Template: want digest_preview, got %q", c.Template)
	}
	if mode, _ := c.Data["Mode"].(string); mode != "weekly" {
		t.Errorf("Mode: want weekly (from user settings), got %q", mode)
	}
	items, ok := c.Data["Items"].([]notifications.DigestItem)
	if !ok {
		t.Fatalf("Items: wrong type %T", c.Data["Items"])
	}
	if len(items) != 3 {
		t.Fatalf("synth items: want 3, got %d", len(items))
	}
	for i, it := range items {
		if !strings.HasPrefix(it.Title, "Sample notification") {
			t.Errorf("synth item[%d].Title: want 'Sample notification …', got %q", i, it.Title)
		}
		if it.Kind != "system" {
			t.Errorf("synth item[%d].Kind: want system, got %q", i, it.Kind)
		}
	}
}

// TestDigestPreview_UnknownUser_404 — malformed user_id surfaces as
// 400 from parsePrefsUserID; a well-formed but unknown user (no auth
// row + no settings/prefs) lands as 404 so the operator sees a typed
// "you typed the wrong id" envelope rather than a silent empty preview.
func TestDigestPreview_UnknownUser_404(t *testing.T) {
	if emEventsPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}

	adminID := seedAdmin(t, "admin-preview-404@example.com")
	mailer := &previewMailer{}
	d := &Deps{
		Pool:   emEventsPool,
		Admins: admins.NewStore(emEventsPool),
		Mailer: mailer,
	}

	t.Run("malformed user_id 400", func(t *testing.T) {
		r := chi.NewRouter()
		r.Post("/notifications/users/{user_id}/digest-preview", d.notificationsDigestPreviewHandler)
		req := httptest.NewRequest(http.MethodPost,
			"/notifications/users/not-a-uuid/digest-preview",
			bytes.NewBufferString(`{}`))
		req = req.WithContext(WithAdminPrincipal(req.Context(), AdminPrincipal{
			AdminID:   adminID,
			SessionID: uuid.Must(uuid.NewV7()),
		}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: want 400, got %d body=%s", rec.Code, rec.Body.String())
		}
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if env.Error.Code != "validation" {
			t.Errorf("error.code: want validation, got %q", env.Error.Code)
		}
	})

	t.Run("well-formed but unknown user 404", func(t *testing.T) {
		// Fresh random uuid that exists nowhere — neither auth tables
		// (none registered in this test) nor prefs/settings.
		userID := uuid.Must(uuid.NewV7())
		code, _ := callDigestPreview(t, d, adminID, userID, `{}`)
		if code != http.StatusNotFound {
			t.Fatalf("status: want 404, got %d", code)
		}
		// No mailer call should have leaked.
		if calls := mailer.snapshot(); len(calls) != 0 {
			t.Errorf("mailer.snapshot: want 0 calls, got %d", len(calls))
		}
	})
}

// TestDigestPreview_DefaultsToAdminEmail — omit `recipient` in the
// body; the handler looks up the admin's email via PrincipalFrom +
// _admins. Both the response envelope and the mailer recipient should
// reflect that resolved email.
func TestDigestPreview_DefaultsToAdminEmail(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	if emEventsPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}

	const adminEmail = "admin-default-recipient@example.com"
	adminID := seedAdmin(t, adminEmail)
	userID := uuid.Must(uuid.NewV7())
	seedUserSettings(t, userID, "daily")
	_ = seedDigestDeferral(t, userID, "Default recipient probe", "Body")

	mailer := &previewMailer{}
	d := &Deps{
		Pool:   emEventsPool,
		Admins: admins.NewStore(emEventsPool),
		Mailer: mailer,
	}

	// Empty body — handler defaults recipient.
	code, resp := callDigestPreview(t, d, adminID, userID, `{}`)
	if code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", code)
	}
	if resp.Recipient != adminEmail {
		t.Errorf("response.recipient: want %s, got %s", adminEmail, resp.Recipient)
	}
	calls := mailer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("mailer calls: want 1, got %d", len(calls))
	}
	if calls[0].To != adminEmail {
		t.Errorf("mailer.To: want %s, got %s", adminEmail, calls[0].To)
	}
}

// TestDigestPreview_Unauthenticated_401 — RequireAdmin enforcement.
// Same shape as TestPutPrefs_Unauthenticated in the default-tag file,
// but lives here so the embed_pg test suite catches a regression in
// the gate even if the default-tag file is somehow skipped.
func TestDigestPreview_Unauthenticated_401(t *testing.T) {
	d := &Deps{Pool: emEventsPool}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		d.mountNotificationsPrefs(r)
	})

	req := httptest.NewRequest(http.MethodPost,
		"/notifications/users/"+uuid.Must(uuid.NewV7()).String()+"/digest-preview",
		bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "unauthorized" {
		t.Errorf("error.code: want unauthorized, got %q", env.Error.Code)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
