//go:build embed_pg

package adminapi

// Regression tests for the v1.x RBAC management surface
// (rbac_admin.go). Covers the three contracts the SPA depends on:
//
//   1. GET /api/_admin/rbac/roles returns the seeded role catalog,
//      including the system_readonly role added in migration 0029.
//
//   2. PUT /api/_admin/admins/{id}/roles atomically swaps the
//      assignment set. Switching an admin from system_admin to
//      system_readonly lands in `_user_roles` and the SAME admin
//      is then denied write actions by requireAction.
//
//   3. The last-system_admin safety guard. If the deployment has
//      exactly one system_admin, downgrading that admin must be
//      refused with 409 — otherwise the deployment locks itself
//      out of every settings.write action.

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

// newRBACAdminTestServer spins a real httptest server with the admin
// chain mounted. Reuses the shared embed-PG pool from email_events
// TestMain so we don't bring up another instance.
func newRBACAdminTestServer(t *testing.T) (*httptest.Server, *Deps, func()) {
	t.Helper()
	if emEventsPool == nil {
		t.Skip("shared embed_pg pool not wired; run with -tags embed_pg")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Deps{
		Pool:     emEventsPool,
		Admins:   admins.NewStore(emEventsPool),
		Sessions: admins.NewSessionStore(emEventsPool, fakeKey()),
		Settings: settings.New(settings.Options{Pool: emEventsPool}),
		Audit:    audit.NewWriter(emEventsPool),
		Log:      log,
		RBAC:     rbac.NewStore(emEventsPool),
	}
	r := chi.NewRouter()
	r.Use(AdminAuthMiddleware(d.Sessions, log))
	d.Mount(r)
	srv := httptest.NewServer(r)
	return srv, d, srv.Close
}

// authedRequest issues a request with the admin's bearer token.
func authedRequest(
	t *testing.T,
	d *Deps,
	adminID uuid.UUID,
	method, url string,
	body any,
) (*http.Response, []byte) {
	t.Helper()
	tok, _, err := d.Sessions.Create(emEventsCtx, admins.CreateSessionInput{
		AdminID:   adminID,
		IP:        "127.0.0.1",
		UserAgent: "rbac-admin-test",
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	var rb io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rb = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, rb)
	req.Header.Set("Authorization", "Bearer "+string(tok))
	if rb != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func TestRBACAdmin_ListRolesContainsSystemReadonly(t *testing.T) {
	srv, d, stop := newRBACAdminTestServer(t)
	defer stop()
	ctx := emEventsCtx

	// Need a system_admin caller because /rbac/roles is gated by
	// rbac.read which system_readonly also holds — but for this test
	// we ensure the API works at all with a privileged caller.
	caller, err := d.Admins.Create(ctx, "rbac-admin-list@test", "test-password-1234")
	if err != nil {
		t.Fatalf("create caller: %v", err)
	}
	if err := rbac.AssignSystemAdmin(ctx, d.RBAC, caller.ID); err != nil {
		t.Fatalf("assign caller: %v", err)
	}

	resp, body := authedRequest(t, d, caller.ID, "GET", srv.URL+"/api/_admin/rbac/roles", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", resp.StatusCode, body)
	}
	var out struct {
		Roles []struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotSystemReadonly := false
	gotSystemAdmin := false
	for _, r := range out.Roles {
		if r.Name == "system_readonly" && r.Scope == "site" {
			gotSystemReadonly = true
		}
		if r.Name == "system_admin" && r.Scope == "site" {
			gotSystemAdmin = true
		}
	}
	if !gotSystemAdmin {
		t.Error("expected system_admin (site) in role list")
	}
	if !gotSystemReadonly {
		t.Error("expected system_readonly (site) in role list — migration 0029 may not have run")
	}
}

func TestRBACAdmin_SetRolesSwapsAtomically(t *testing.T) {
	srv, d, stop := newRBACAdminTestServer(t)
	defer stop()
	ctx := emEventsCtx

	// Two admins so neither is "the last system_admin" — guard
	// shouldn't fire here.
	keeper, err := d.Admins.Create(ctx, "rbac-keeper@test", "test-password-1234")
	if err != nil {
		t.Fatalf("create keeper: %v", err)
	}
	target, err := d.Admins.Create(ctx, "rbac-target@test", "test-password-1234")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	for _, a := range []uuid.UUID{keeper.ID, target.ID} {
		if err := rbac.AssignSystemAdmin(ctx, d.RBAC, a); err != nil {
			t.Fatalf("assign: %v", err)
		}
	}

	// keeper PUTs target with only system_readonly.
	resp, body := authedRequest(t, d, keeper.ID,
		"PUT", srv.URL+"/api/_admin/admins/"+target.ID.String()+"/roles",
		map[string]any{"roles": []string{"system_readonly"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", resp.StatusCode, body)
	}

	// Verify target now has system_readonly only — and crucially does
	// NOT still carry system_admin.
	roles := dbRoleNames(t, ctx, d, target.ID)
	wantRoles := map[string]bool{"system_readonly": true}
	if len(roles) != 1 || !wantRoles[roles[0]] {
		t.Fatalf("target's site roles = %v; want exactly [system_readonly]", roles)
	}

	// And the gate actually denies: target was downgraded, so trying
	// to PATCH settings should now 403 instead of 200.
	patchBody, _ := json.Marshal(map[string]any{"value": "should-be-denied"})
	resp, _ = authedRequest(t, d, target.ID,
		"PATCH", srv.URL+"/api/_admin/settings/rbac.admin.test",
		map[string]any{"value": "should-be-denied"})
	_ = patchBody
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("downgraded admin PATCH settings: got %d; want 403", resp.StatusCode)
	}
}

func TestRBACAdmin_LastSystemAdminGuardRefuses(t *testing.T) {
	srv, d, stop := newRBACAdminTestServer(t)
	defer stop()
	ctx := emEventsCtx

	// Wipe any system_admin assignments left over from earlier tests
	// in this run so we can reproduce the "exactly one" scenario
	// deterministically. We DON'T delete _admins rows — just the role
	// assignments — so other tests' fixtures stay.
	if _, err := emEventsPool.Exec(ctx, `
        DELETE FROM _user_roles ur
         USING _roles r
         WHERE ur.role_id = r.id
           AND ur.collection_name = '_admins'
           AND r.name = 'system_admin'
           AND r.scope = 'site'
    `); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Exactly one system_admin in the deployment. Promoting this admin
	// to NOTHING would leave zero — guard fires.
	lone, err := d.Admins.Create(ctx, "rbac-lone@test", "test-password-1234")
	if err != nil {
		t.Fatalf("create lone: %v", err)
	}
	if err := rbac.AssignSystemAdmin(ctx, d.RBAC, lone.ID); err != nil {
		t.Fatalf("assign lone: %v", err)
	}

	resp, body := authedRequest(t, d, lone.ID,
		"PUT", srv.URL+"/api/_admin/admins/"+lone.ID.String()+"/roles",
		map[string]any{"roles": []string{"system_readonly"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, body=%s; want 409 conflict from last-system_admin guard",
			resp.StatusCode, body)
	}
	// The body should mention "zero system_admin" so an operator
	// reading the toast knows what's wrong.
	if !bytes.Contains(bytes.ToLower(body), []byte("system_admin")) {
		t.Errorf("guard error body doesn't mention system_admin: %s", body)
	}

	// Lone admin's assignments must be UNCHANGED — tx rolled back.
	roles := dbRoleNames(t, ctx, d, lone.ID)
	wantAdmin := false
	for _, r := range roles {
		if r == "system_admin" {
			wantAdmin = true
		}
	}
	if !wantAdmin {
		t.Fatalf("guard refused but assignment was committed anyway: roles=%v", roles)
	}
}

// dbRoleNames returns the SITE role names currently assigned to an
// admin. Used to assert tx outcomes from the test harness.
func dbRoleNames(t *testing.T, ctx context.Context, d *Deps, adminID uuid.UUID) []string {
	t.Helper()
	rows, err := emEventsPool.Query(ctx, `
        SELECT r.name FROM _user_roles ur
          JOIN _roles r ON r.id = ur.role_id
         WHERE ur.collection_name = '_admins'
           AND ur.record_id = $1
           AND ur.tenant_id IS NULL
           AND r.scope = 'site'
         ORDER BY r.name
    `, adminID)
	if err != nil {
		t.Fatalf("query roles: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	return out
}
