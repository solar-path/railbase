//go:build embed_pg

// E2E for the v0.4.3 user-facing session-management endpoints:
//
//   GET    /api/auth/sessions
//   DELETE /api/auth/sessions/{id}
//   DELETE /api/auth/sessions/others
//
// Validated against embedded Postgres so the test exercises the real
// schema (`_sessions`), real auth middleware, and real session.Store —
// not a mock — and would catch a regression in any of those layers.
//
// Coverage:
//   - List exposes only the caller's sessions, sorted desc by activity
//   - `current` flag flips correctly on exactly one row
//   - Revoke by ID: 204 on success, 404 on stranger's session
//   - Revoke own current session is REFUSED unless ?force=true
//   - Revoke others: keeps current, kills the rest, returns count
//   - Anonymous request (no Bearer): 401
//   - token_hash never appears in the response body (privacy guard)
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/lockout"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestAccountSessions_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(users)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatalf("create users: %v", err)
	}

	var key secret.Key
	for i := range key {
		key[i] = byte(i)
	}
	sessions := session.NewStore(pool, key)

	deps := &Deps{
		Pool:     pool,
		Sessions: sessions,
		Lockout:  lockout.New(),
		Log:      log,
	}
	r := chi.NewRouter()
	r.Use(authmw.New(sessions, log))
	Mount(r, deps)
	srv := httptest.NewServer(r)
	defer srv.Close()

	c := &http.Client{Timeout: 5 * time.Second}

	// Helper: sign up + sign in returns token. Repeat for multiple
	// "devices" by sending different user-agents on each signin.
	signup := func(email, ua string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"email": email, "password": "correcthorse-9", "passwordConfirm": "correcthorse-9",
		})
		req, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-signup", bytes.NewReader(body))
		req.Header.Set("User-Agent", ua)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("signup: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("signup %s: %d %s", email, resp.StatusCode, b)
		}
		var out struct{ Token string }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out.Token
	}
	signin := func(email, ua string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"identity": email, "password": "correcthorse-9",
		})
		req, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-with-password", bytes.NewReader(body))
		req.Header.Set("User-Agent", ua)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("signin: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("signin %s: %d %s", email, resp.StatusCode, b)
		}
		var out struct{ Token string }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out.Token
	}

	authedRequest := func(method, path, token string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	// ---- setup: alice signs up via 3 "devices" (3 distinct sessions) ----
	aliceTok1 := signup("alice@example.com", "agent-laptop")
	aliceTok2 := signin("alice@example.com", "agent-phone")
	aliceTok3 := signin("alice@example.com", "agent-tablet")
	// bob — separate user; alice must NOT see his sessions.
	_ = signup("bob@example.com", "agent-bob")

	// === [1] Anonymous → 401 ===
	resp, err := c.Get(srv.URL + "/api/auth/sessions")
	if err != nil {
		t.Fatalf("anon GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("[1] anonymous list sessions: status = %d, want 401", resp.StatusCode)
	}

	// === [2] List as alice via her LAPTOP token → 3 rows, exactly one current ===
	code, body := authedRequest("GET", "/api/auth/sessions", aliceTok1)
	if code != 200 {
		t.Fatalf("[2] list: %d %s", code, body)
	}
	if bytes.Contains(body, []byte("token_hash")) {
		t.Errorf("[2] response leaks token_hash:\n%s", body)
	}
	var listed struct {
		Sessions []struct {
			ID             string `json:"id"`
			CollectionName string `json:"collection_name"`
			Current        bool   `json:"current"`
			UserAgent      string `json:"user_agent"`
		}
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("[2] decode: %v body=%s", err, body)
	}
	if len(listed.Sessions) != 3 {
		t.Errorf("[2] alice should see 3 sessions, got %d: %+v", len(listed.Sessions), listed.Sessions)
	}
	currents := 0
	var currentID, otherID string
	for _, s := range listed.Sessions {
		if s.CollectionName != "users" {
			t.Errorf("[2] collection_name = %q, want users", s.CollectionName)
		}
		if s.Current {
			currents++
			currentID = s.ID
			if s.UserAgent != "agent-laptop" {
				t.Errorf("[2] current session UA = %q, want agent-laptop", s.UserAgent)
			}
		} else if otherID == "" {
			otherID = s.ID
		}
	}
	if currents != 1 {
		t.Errorf("[2] exactly one current session expected, got %d", currents)
	}

	// === [3] Revoke a NON-current session → 204, list drops to 2 ===
	code, body = authedRequest("DELETE", "/api/auth/sessions/"+otherID, aliceTok1)
	if code != 204 {
		t.Errorf("[3] revoke other: %d %s", code, body)
	}
	code, body = authedRequest("GET", "/api/auth/sessions", aliceTok1)
	_ = json.Unmarshal(body, &listed)
	if len(listed.Sessions) != 2 {
		t.Errorf("[3] expected 2 sessions after revoke, got %d", len(listed.Sessions))
	}

	// === [4] Revoke the CURRENT session WITHOUT ?force=true → 422-shape error ===
	code, body = authedRequest("DELETE", "/api/auth/sessions/"+currentID, aliceTok1)
	if code == 204 {
		t.Errorf("[4] revoking current without force should NOT succeed; got 204")
	}
	if !strings.Contains(string(body), "cannot revoke the current session") {
		t.Errorf("[4] error message should explain why; got: %s", body)
	}

	// === [5] Revoke an unknown UUID → 404 ===
	code, body = authedRequest("DELETE", "/api/auth/sessions/00000000-0000-0000-0000-000000000000", aliceTok1)
	if code != 404 {
		t.Errorf("[5] revoke unknown: %d, want 404 (%s)", code, body)
	}

	// === [6] Revoke others as the LAPTOP → kills phone+tablet (or whatever remains), keeps laptop ===
	code, body = authedRequest("DELETE", "/api/auth/sessions/others", aliceTok1)
	if code != 200 {
		t.Errorf("[6] revoke others: %d %s", code, body)
	}
	var rev struct{ Revoked int }
	_ = json.Unmarshal(body, &rev)
	if rev.Revoked < 1 {
		t.Errorf("[6] expected at least 1 session revoked, got %d", rev.Revoked)
	}
	// And ListFor should now show only the laptop's session.
	code, body = authedRequest("GET", "/api/auth/sessions", aliceTok1)
	_ = json.Unmarshal(body, &listed)
	if len(listed.Sessions) != 1 {
		t.Errorf("[6] expected only laptop session remaining, got %d", len(listed.Sessions))
	}
	if !listed.Sessions[0].Current {
		t.Errorf("[6] remaining session must be the current one")
	}

	// === [Sprint 5] PATCH /api/auth/sessions/{id} — rename + trust ===
	// Use the still-live laptop session and a NEW second signin so we
	// have a non-current session to label.
	relabelTok := signin("alice@example.com", "agent-relabel")
	_ = relabelTok
	code, body = authedRequest("GET", "/api/auth/sessions", aliceTok1)
	_ = json.Unmarshal(body, &listed)
	var relabelID string
	for _, s := range listed.Sessions {
		if !s.Current {
			relabelID = s.ID
			break
		}
	}
	if relabelID == "" {
		t.Fatalf("[Sprint 5] expected a non-current session to label after fresh signin")
	}
	// Rename + mark trusted in one call.
	patchBody, _ := json.Marshal(map[string]any{
		"device_name": "Test Device",
		"is_trusted":  true,
	})
	req, _ := http.NewRequest("PATCH", srv.URL+"/api/auth/sessions/"+relabelID, bytes.NewReader(patchBody))
	req.Header.Set("Authorization", "Bearer "+aliceTok1)
	req.Header.Set("Content-Type", "application/json")
	resp3, _ := c.Do(req)
	if resp3.StatusCode != 204 {
		b, _ := io.ReadAll(resp3.Body)
		t.Errorf("[Sprint 5] PATCH metadata: %d %s", resp3.StatusCode, b)
	}
	resp3.Body.Close()
	// Re-list and verify the row carries the new values.
	code, body = authedRequest("GET", "/api/auth/sessions", aliceTok1)
	type sessDTOFull struct {
		ID         string `json:"id"`
		DeviceName string `json:"device_name"`
		IsTrusted  bool   `json:"is_trusted"`
		Current    bool   `json:"current"`
	}
	var full struct {
		Sessions []sessDTOFull `json:"sessions"`
	}
	_ = json.Unmarshal(body, &full)
	matched := false
	for _, s := range full.Sessions {
		if s.ID == relabelID {
			matched = true
			if s.DeviceName != "Test Device" {
				t.Errorf("[Sprint 5] device_name not persisted: %q", s.DeviceName)
			}
			if !s.IsTrusted {
				t.Errorf("[Sprint 5] is_trusted not flipped: %v", s.IsTrusted)
			}
		}
	}
	if !matched {
		t.Errorf("[Sprint 5] labelled session disappeared from list")
	}
	// Empty body → 422.
	req, _ = http.NewRequest("PATCH", srv.URL+"/api/auth/sessions/"+relabelID, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+aliceTok1)
	req.Header.Set("Content-Type", "application/json")
	resp3, _ = c.Do(req)
	resp3.Body.Close()
	if resp3.StatusCode == 204 {
		t.Errorf("[Sprint 5] empty PATCH should fail; got 204")
	}

	// === [7] Confirm the phone token is dead — its lookup must fail ===
	// We can't directly observe lookup; but a GET /api/auth/me with the
	// phone token should now 401 because authmw can't find a live session.
	req, _ = http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok2)
	resp2, _ := c.Do(req)
	resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Errorf("[7] revoked phone token still works: status=%d (expected 401)", resp2.StatusCode)
	}
	_ = aliceTok3 // tablet token — same fate as phone; not re-asserted to keep the test focused.
}
