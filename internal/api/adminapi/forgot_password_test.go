//go:build embed_pg

package adminapi

// E2E for v1.7.46 — admin password-reset flow.
//
// Covers:
//   - 503 + helpful hint when mailer isn't configured
//   - 200 + token issuance when mailer is configured AND admin exists
//   - 200 anti-enumeration when admin doesn't exist (no leaking signal)
//   - Reset endpoint consumes a fresh token, sets new pw, revokes sessions
//   - Reset endpoint rejects an already-used token (single-use contract)
//   - Reset endpoint rejects a token issued for a non-_admins collection
//
// Uses the shared emEventsPool TestMain (see email_events_test.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	"github.com/railbase/railbase/internal/settings"
)

func newForgotPwDeps(t *testing.T) *Deps {
	t.Helper()
	if emEventsPool == nil {
		t.Skip("shared embed_pg pool not wired")
	}
	key := fakeKey()
	return &Deps{
		Pool:         emEventsPool,
		Admins:       admins.NewStore(emEventsPool),
		Sessions:     admins.NewSessionStore(emEventsPool, key),
		Settings:     settings.New(settings.Options{Pool: emEventsPool}),
		RecordTokens: recordtoken.NewStore(emEventsPool, key),
	}
}

// resetMailerConfigured clears mailer flags for a clean slate, then
// optionally stamps configured_at so the forgot-password endpoint
// recognises the mailer as ready.
func setMailerConfigured(t *testing.T, d *Deps, configured bool) {
	t.Helper()
	ctx := emEventsCtx
	_ = d.Settings.Delete(ctx, settingsKeyConfiguredAt)
	_ = d.Settings.Delete(ctx, settingsKeySkippedAt)
	if configured {
		if err := d.Settings.Set(ctx, settingsKeyConfiguredAt, "2026-05-13T00:00:00Z"); err != nil {
			t.Fatalf("set mailer configured_at: %v", err)
		}
	}
}

func newForgotPwServer(d *Deps) *httptest.Server {
	r := chi.NewRouter()
	r.Post("/api/_admin/forgot-password", d.forgotPasswordHandler)
	r.Post("/api/_admin/reset-password", d.resetPasswordHandler)
	return httptest.NewServer(r)
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestForgotPassword_MailerNotConfigured_Returns503(t *testing.T) {
	d := newForgotPwDeps(t)
	setMailerConfigured(t, d, false)
	srv := newForgotPwServer(d)
	defer srv.Close()

	// Even with a nonexistent email, mailer-down trumps the anti-
	// enumeration path — the operator should see WHY they can't recover.
	status, body := postJSON(t, srv, "/api/_admin/forgot-password", map[string]any{
		"email": "nobody@example.com",
	})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%+v", status, body)
	}
	// The hint must mention the CLI escape hatch — that's the entire
	// reason this isn't a generic anti-enumeration 200.
	errMsg, _ := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(errMsg, "railbase admin reset-password") {
		t.Errorf("error message should mention CLI hint; got %q", errMsg)
	}
}

func TestForgotPassword_AdminExists_Returns200AndEnqueues(t *testing.T) {
	d := newForgotPwDeps(t)
	setMailerConfigured(t, d, true)
	srv := newForgotPwServer(d)
	defer srv.Close()

	// Seed an admin row.
	ctx := emEventsCtx
	cleanupAdmins(t, d)
	a, err := d.Admins.Create(ctx, "exists@example.com", "OldP@ss123")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	status, body := postJSON(t, srv, "/api/_admin/forgot-password", map[string]any{
		"email": "exists@example.com",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%+v", status, body)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok=false; body=%+v", body)
	}

	// We don't have direct access to the issued token in this test —
	// but the row was inserted by recordtoken.Store.Create. Query it
	// directly to verify the side effect, then use it in the reset
	// test below.
	var n int
	if err := emEventsPool.QueryRow(ctx,
		`SELECT count(*) FROM _record_tokens WHERE collection_name='_admins' AND record_id=$1 AND consumed_at IS NULL`,
		a.ID,
	).Scan(&n); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 unconsumed reset token for the admin, got %d", n)
	}
}

func TestForgotPassword_AdminDoesNotExist_AntiEnumeration(t *testing.T) {
	d := newForgotPwDeps(t)
	setMailerConfigured(t, d, true)
	srv := newForgotPwServer(d)
	defer srv.Close()

	cleanupAdmins(t, d)
	status, body := postJSON(t, srv, "/api/_admin/forgot-password", map[string]any{
		"email": "ghost@example.com",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%+v", status, body)
	}
	// Generic message — should NOT reveal whether the email is known.
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "If that email") {
		t.Errorf("expected anti-enum-shaped message; got %q", msg)
	}
}

func TestResetPassword_HappyPath_SetsPwAndRevokesSessions(t *testing.T) {
	d := newForgotPwDeps(t)
	setMailerConfigured(t, d, true)
	srv := newForgotPwServer(d)
	defer srv.Close()

	ctx := emEventsCtx
	cleanupAdmins(t, d)
	a, err := d.Admins.Create(ctx, "reset@example.com", "OldP@ss123")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	// Seed a live session so we can verify RevokeAllFor fires.
	_, _, err = d.Sessions.Create(ctx, admins.CreateSessionInput{AdminID: a.ID})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Issue a token directly via the store — same code path the
	// HTTP forgot endpoint uses internally.
	tok, _, err := d.RecordTokens.Create(ctx, recordtoken.CreateInput{
		CollectionName: "_admins",
		RecordID:       a.ID,
		Purpose:        recordtoken.PurposeReset,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	status, body := postJSON(t, srv, "/api/_admin/reset-password", map[string]any{
		"token":        string(tok),
		"new_password": "NewP@ss12345",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%+v", status, body)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok=false; body=%+v", body)
	}
	if revoked, _ := body["sessions_revoked"].(float64); revoked < 1 {
		t.Errorf("expected >=1 sessions_revoked, got %v", body["sessions_revoked"])
	}

	// New password must authenticate.
	if _, err := d.Admins.Authenticate(ctx, "reset@example.com", "NewP@ss12345"); err != nil {
		t.Errorf("new password should authenticate: %v", err)
	}
	// Old password must NOT.
	if _, err := d.Admins.Authenticate(ctx, "reset@example.com", "OldP@ss123"); err == nil {
		t.Error("old password still works after reset")
	}
}

func TestResetPassword_TokenReuseRejected(t *testing.T) {
	d := newForgotPwDeps(t)
	setMailerConfigured(t, d, true)
	srv := newForgotPwServer(d)
	defer srv.Close()

	ctx := emEventsCtx
	cleanupAdmins(t, d)
	a, _ := d.Admins.Create(ctx, "reuse@example.com", "OldP@ss123")
	tok, _, _ := d.RecordTokens.Create(ctx, recordtoken.CreateInput{
		CollectionName: "_admins",
		RecordID:       a.ID,
		Purpose:        recordtoken.PurposeReset,
	})

	// First use succeeds.
	status, _ := postJSON(t, srv, "/api/_admin/reset-password", map[string]any{
		"token":        string(tok),
		"new_password": "FirstP@ss1!",
	})
	if status != http.StatusOK {
		t.Fatalf("first use status = %d", status)
	}
	// Second use rejected.
	status, body := postJSON(t, srv, "/api/_admin/reset-password", map[string]any{
		"token":        string(tok),
		"new_password": "SecondP@ss2!",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("second use status = %d, want 401; body=%+v", status, body)
	}
}

func TestResetPassword_NonAdminCollectionTokenRejected(t *testing.T) {
	d := newForgotPwDeps(t)
	setMailerConfigured(t, d, true)
	srv := newForgotPwServer(d)
	defer srv.Close()

	ctx := emEventsCtx
	cleanupAdmins(t, d)
	a, _ := d.Admins.Create(ctx, "crosscoll@example.com", "OldP@ss123")
	// Issue a token claiming collection="users" (app collection, not _admins).
	tok, _, err := d.RecordTokens.Create(ctx, recordtoken.CreateInput{
		CollectionName: "users",
		RecordID:       a.ID,
		Purpose:        recordtoken.PurposeReset,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	status, _ := postJSON(t, srv, "/api/_admin/reset-password", map[string]any{
		"token":        string(tok),
		"new_password": "ShouldFail123!",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("cross-collection token status = %d, want 401", status)
	}
	// And the admin password should be unchanged.
	if _, err := d.Admins.Authenticate(ctx, "crosscoll@example.com", "OldP@ss123"); err != nil {
		t.Errorf("old password should still work: %v", err)
	}
}

func TestResetPassword_ShortPasswordRejected(t *testing.T) {
	d := newForgotPwDeps(t)
	setMailerConfigured(t, d, true)
	srv := newForgotPwServer(d)
	defer srv.Close()

	status, _ := postJSON(t, srv, "/api/_admin/reset-password", map[string]any{
		"token":        "anything",
		"new_password": "short",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

// cleanupAdmins truncates _admins + _admin_sessions + _record_tokens
// for clean test isolation. The shared TestMain pool is reused across
// tests, so per-test cleanup is necessary.
func cleanupAdmins(t *testing.T, _ *Deps) {
	t.Helper()
	ctx, cancel := context.WithTimeout(emEventsCtx, 5)
	defer cancel()
	_ = ctx // best-effort
	_, _ = emEventsPool.Exec(emEventsCtx, `TRUNCATE _admins, _admin_sessions, _record_tokens RESTART IDENTITY CASCADE`)
}

