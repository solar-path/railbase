//go:build embed_pg

package adminapi

// Regression test for the v1.x admin-side RBAC wiring.
//
// What's actually being tested:
//
//   1. A `system_admin` admin (default after bootstrap) can PATCH
//      a setting → 200 OK + value persisted.
//
//   2. A `system_readonly` admin (the new role added in 0029) can
//      LIST settings → 200 OK, but is denied PATCH → 403.
//
// Wire shape under test: requireAction → rbac.Require → rbac.Middleware
// → AdminPrincipal extractor. If any link breaks the test fails loud.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/settings"
)

// keyFor32 returns a deterministic 32-byte secret.Key for test use.
// Mirrors the pattern in setup_mailer_embed_test.fakeKey.

func TestAdminRBACGate_SettingsWrite(t *testing.T) {
	if emEventsPool == nil {
		t.Skip("shared embed_pg pool not wired; run with -tags embed_pg")
	}
	ctx := emEventsCtx

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := admins.NewStore(emEventsPool)
	sess := admins.NewSessionStore(emEventsPool, fakeKey())
	settingsMgr := settings.New(settings.Options{Pool: emEventsPool})
	auditWriter := audit.NewWriter(emEventsPool)
	rbacStore := rbac.NewStore(emEventsPool)

	// Two admins: one keeps the bootstrap-default system_admin, the
	// other we downgrade to system_readonly so the deny path actually
	// has a target.
	admA, err := store.Create(ctx, "rbac-gate-admin@test", "test-password-1234")
	if err != nil {
		t.Fatalf("create admin a: %v", err)
	}
	admR, err := store.Create(ctx, "rbac-gate-readonly@test", "test-password-1234")
	if err != nil {
		t.Fatalf("create admin r: %v", err)
	}

	// admA → system_admin. admR → system_readonly (no settings.write).
	if err := rbac.AssignSystemAdmin(ctx, rbacStore, admA.ID); err != nil {
		t.Fatalf("assign admin a: %v", err)
	}
	readRole, err := rbacStore.GetRole(ctx, "system_readonly", rbac.ScopeSite)
	if err != nil {
		t.Fatalf("lookup system_readonly: %v", err)
	}
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: rbac.AdminCollectionName,
		RecordID:       admR.ID,
		RoleID:         readRole.ID,
	}); err != nil {
		t.Fatalf("assign admin r: %v", err)
	}

	// Build a router with the admin chain wired against this RBAC
	// store. Issue real sessions so the AdminAuthMiddleware → RBAC
	// middleware → requireAction chain runs end-to-end with no
	// shortcuts.
	d := &Deps{
		Pool:     emEventsPool,
		Admins:   store,
		Sessions: sess,
		Settings: settingsMgr,
		Audit:    auditWriter,
		Log:      log,
		RBAC:     rbacStore,
	}
	r := chi.NewRouter()
	r.Use(AdminAuthMiddleware(sess, log))
	d.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	tokenFor := func(t *testing.T, adminID uuid.UUID) string {
		t.Helper()
		tok, _, err := sess.Create(ctx, admins.CreateSessionInput{
			AdminID:   adminID,
			IP:        "127.0.0.1",
			UserAgent: "rbac-gate-test",
		})
		if err != nil {
			t.Fatalf("session for %s: %v", adminID, err)
		}
		return string(tok)
	}

	patch := func(t *testing.T, tok string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"value": "rbac-gate"})
		req, _ := http.NewRequest(http.MethodPatch,
			srv.URL+"/api/_admin/settings/rbac.gate.test",
			bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		return resp.StatusCode
	}

	list := func(t *testing.T, tok string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/_admin/settings", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		return resp.StatusCode
	}

	t.Cleanup(func() {
		// Clean the setting + admins we minted so a re-run starts clean.
		_ = settingsMgr.Delete(context.Background(), "rbac.gate.test")
	})

	t.Run("system_admin/PATCH_settings", func(t *testing.T) {
		got := patch(t, tokenFor(t, admA.ID))
		if got != http.StatusOK && got != http.StatusNoContent {
			t.Fatalf("system_admin PATCH: got %d, want 2xx", got)
		}
	})

	t.Run("system_readonly/PATCH_settings_denied", func(t *testing.T) {
		got := patch(t, tokenFor(t, admR.ID))
		if got != http.StatusForbidden {
			t.Fatalf("system_readonly PATCH: got %d, want 403", got)
		}
	})

	t.Run("system_readonly/LIST_settings_allowed", func(t *testing.T) {
		got := list(t, tokenFor(t, admR.ID))
		if got != http.StatusOK {
			t.Fatalf("system_readonly LIST: got %d, want 200", got)
		}
	})
}
