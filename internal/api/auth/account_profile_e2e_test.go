//go:build embed_pg

// E2E for the v0.4.3 Sprint 2 endpoints:
//
//   PATCH /api/auth/me
//   POST  /api/auth/change-password
//
// Validates against embedded Postgres with a real auth-collection that
// has user-defined fields (`display_name`, `theme`) so the dynamic
// UPDATE path exercises beyond the trivial single-column case.
//
// Coverage:
//   - PATCH /me succeeds for whitelisted user-defined fields → returns updated record
//   - PATCH /me rejects email (use confirm-email-change flow)
//   - PATCH /me rejects password (use change-password endpoint)
//   - PATCH /me rejects unknown fields → 422
//   - PATCH /me rejects empty body → 422
//   - PATCH /me anonymous → 401
//   - change-password with wrong current → 401
//   - change-password with short new → 422
//   - change-password with mismatched confirm → 422
//   - change-password success: keeps current session, kills others
//   - change-password anonymous → 401
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

func TestAccountProfileAndPassword_E2E(t *testing.T) {
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

	// Auth collection with two writable user fields — the air/rail-
	// style "display_name" + "theme" pair lets us exercise the
	// multi-column UPDATE path AND the per-type acceptance.
	users := schemabuilder.NewAuthCollection("users").
		Field("display_name", schemabuilder.NewText()).
		Field("theme", schemabuilder.NewSelect("system", "light", "dark"))
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

	deps := &Deps{Pool: pool, Sessions: sessions, Lockout: lockout.New(), Log: log}
	r := chi.NewRouter()
	r.Use(authmw.New(sessions, log))
	Mount(r, deps)
	srv := httptest.NewServer(r)
	defer srv.Close()
	c := &http.Client{Timeout: 5 * time.Second}

	doAuth := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		bts, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, bts
	}

	// Sign up alice — laptop session.
	code, body := doAuth("POST", "/api/collections/users/auth-signup", "", map[string]string{
		"email": "alice@example.com", "password": "correcthorse-9", "passwordConfirm": "correcthorse-9",
	})
	if code/100 != 2 {
		t.Fatalf("signup: %d %s", code, body)
	}
	var sign struct{ Token string }
	_ = json.Unmarshal(body, &sign)
	laptopTok := sign.Token

	// === PATCH /me ===

	// [1] anonymous → 401
	code, _ = doAuth("PATCH", "/api/auth/me", "", map[string]any{"display_name": "alice"})
	if code != 401 {
		t.Errorf("[1] anon PATCH: status=%d, want 401", code)
	}

	// [2] empty body → 422
	code, body = doAuth("PATCH", "/api/auth/me", laptopTok, map[string]any{})
	if code == 200 {
		t.Errorf("[2] empty PATCH should fail; got 200 (%s)", body)
	}

	// [3] success — update both whitelisted fields, expect them in response
	code, body = doAuth("PATCH", "/api/auth/me", laptopTok, map[string]any{
		"display_name": "Alice Liddell",
		"theme":        "dark",
	})
	if code != 200 {
		t.Fatalf("[3] PATCH success: %d %s", code, body)
	}
	var pr struct {
		Record map[string]any `json:"record"`
	}
	_ = json.Unmarshal(body, &pr)
	if pr.Record["display_name"] != "Alice Liddell" {
		t.Errorf("[3] display_name not echoed: %v", pr.Record["display_name"])
	}
	if pr.Record["theme"] != "dark" {
		t.Errorf("[3] theme not echoed: %v", pr.Record["theme"])
	}

	// [4] email rejection
	code, body = doAuth("PATCH", "/api/auth/me", laptopTok, map[string]any{"email": "evil@example.com"})
	if code == 200 {
		t.Errorf("[4] email update via PATCH should be rejected; got 200 (%s)", body)
	}
	if !strings.Contains(string(body), "email") {
		t.Errorf("[4] error should mention email; got: %s", body)
	}

	// [5] password rejection
	code, body = doAuth("PATCH", "/api/auth/me", laptopTok, map[string]any{"password_hash": "x"})
	if code == 200 {
		t.Errorf("[5] password_hash via PATCH should be rejected; got 200 (%s)", body)
	}

	// [6] unknown field rejection
	code, body = doAuth("PATCH", "/api/auth/me", laptopTok, map[string]any{"made_up_field": 1})
	if code == 200 {
		t.Errorf("[6] unknown field should be rejected; got 200 (%s)", body)
	}
	if !strings.Contains(string(body), "made_up_field") {
		t.Errorf("[6] error should name the offending key; got: %s", body)
	}

	// [7] verified rejection (system bool)
	code, _ = doAuth("PATCH", "/api/auth/me", laptopTok, map[string]any{"verified": true})
	if code == 200 {
		t.Errorf("[7] verified flip via PATCH should be rejected; got 200")
	}

	// === change-password ===

	// Sign in a second time (phone session) so we can verify it gets
	// killed by the change.
	code, body = doAuth("POST", "/api/collections/users/auth-with-password", "", map[string]string{
		"identity": "alice@example.com", "password": "correcthorse-9",
	})
	if code/100 != 2 {
		t.Fatalf("phone signin: %d %s", code, body)
	}
	var s2 struct{ Token string }
	_ = json.Unmarshal(body, &s2)
	phoneTok := s2.Token

	// [8] anonymous → 401
	code, _ = doAuth("POST", "/api/auth/change-password", "", map[string]any{
		"current_password": "correcthorse-9", "new_password": "newpassword1",
	})
	if code != 401 {
		t.Errorf("[8] anon change-password: status=%d, want 401", code)
	}

	// [9] wrong current → 401
	code, body = doAuth("POST", "/api/auth/change-password", laptopTok, map[string]any{
		"current_password": "wrong", "new_password": "newpassword1",
	})
	if code != 401 {
		t.Errorf("[9] wrong current: status=%d (%s)", code, body)
	}

	// [10] short new → 422
	code, body = doAuth("POST", "/api/auth/change-password", laptopTok, map[string]any{
		"current_password": "correcthorse-9", "new_password": "short",
	})
	if code == 200 || code == 204 {
		t.Errorf("[10] short new password allowed: %d %s", code, body)
	}

	// [11] confirm mismatch → 422
	code, body = doAuth("POST", "/api/auth/change-password", laptopTok, map[string]any{
		"current_password": "correcthorse-9",
		"new_password":     "newpassword1",
		"passwordConfirm":  "newpassword2",
	})
	if code == 200 || code == 204 {
		t.Errorf("[11] mismatched confirm allowed: %d %s", code, body)
	}

	// [12] success — 204, current keeps working, phone token dies.
	code, body = doAuth("POST", "/api/auth/change-password", laptopTok, map[string]any{
		"current_password": "correcthorse-9",
		"new_password":     "newpassword1",
		"passwordConfirm":  "newpassword1",
	})
	if code != 204 {
		t.Fatalf("[12] success expected 204, got %d %s", code, body)
	}
	// Laptop still authed.
	code, _ = doAuth("GET", "/api/auth/me", laptopTok, nil)
	if code != 200 {
		t.Errorf("[12] laptop session killed by change-password (should keep current): %d", code)
	}
	// Phone token revoked → 401.
	code, _ = doAuth("GET", "/api/auth/me", phoneTok, nil)
	if code != 401 {
		t.Errorf("[12] phone session NOT revoked by change-password: %d (want 401)", code)
	}
	// New password works for fresh signin.
	code, body = doAuth("POST", "/api/collections/users/auth-with-password", "", map[string]string{
		"identity": "alice@example.com", "password": "newpassword1",
	})
	if code/100 != 2 {
		t.Errorf("[12] signin with new password: %d %s", code, body)
	}
}
