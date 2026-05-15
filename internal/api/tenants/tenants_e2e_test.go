//go:build embed_pg

// E2E for the v0.4.3 tenants/workspaces CRUD surface.
//
// Validated against embedded Postgres so we exercise the real schema
// (migration 0032: `_tenants` + `_tenant_members`), the real auth
// middleware stamping Principal, and the real tenant.Store.
//
// Coverage:
//   - Anonymous → 401 on every route
//   - Create binds caller as 'owner' AND echoes the row back
//   - Slug auto-derivation, then conflict detection (case-insensitive)
//   - Slug-format validation (CHECK regex enforced server-side)
//   - List exposes only the caller's tenants (alice ≠ bob)
//   - Get + MyRole refuse non-members with 404 (no existence leak)
//   - Update is owner/admin only; member gets 403; slug conflict → 409
//   - Delete is owner-only; admin gets 403; deleted row vanishes from list
//   - Re-create with the same slug after delete is permitted (partial idx)
package tenants

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	authapi "github.com/railbase/railbase/internal/api/auth"
	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/tenant"
)

func TestTenants_E2E(t *testing.T) {
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
	store := tenant.NewStore(pool)
	auditWriter := audit.NewWriter(pool)

	// Build a router that has both the auth-collection signup/signin
	// routes AND the tenants surface, sharing the auth middleware so
	// the Bearer token from auth-signup is honoured by /api/tenants.
	r := chi.NewRouter()
	r.Use(authmw.New(sessions, log))
	authapi.Mount(r, &authapi.Deps{
		Pool:     pool,
		Sessions: sessions,
		Log:      log,
	})
	rbacStore := rbac.NewStore(pool)
	d := &Deps{Pool: pool, Tenants: store, Audit: auditWriter, Log: log}
	d.SetRBAC(rbacStore)
	Mount(r, d)
	srv := httptest.NewServer(r)
	defer srv.Close()

	c := &http.Client{Timeout: 5 * time.Second}

	signup := func(email string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"email": email, "password": "correcthorse-9", "passwordConfirm": "correcthorse-9",
		})
		req, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-signup", bytes.NewReader(body))
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

	doJSON := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rdr)
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
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	// === [1] Anonymous → 401 on every gated route ===
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/tenants"},
		{"POST", "/api/tenants"},
		{"GET", "/api/tenants/" + uuid.New().String()},
		{"PATCH", "/api/tenants/" + uuid.New().String()},
		{"DELETE", "/api/tenants/" + uuid.New().String()},
		{"GET", "/api/tenants/" + uuid.New().String() + "/me"},
	} {
		code, _ := doJSON(route.method, route.path, "", nil)
		if code != http.StatusUnauthorized {
			t.Errorf("[1] %s %s anon = %d, want 401", route.method, route.path, code)
		}
	}

	aliceTok := signup("alice@example.com")
	bobTok := signup("bob@example.com")

	// === [2] Alice creates a tenant with auto-derived slug ===
	code, body := doJSON("POST", "/api/tenants", aliceTok, map[string]string{
		"name": "Acme Corp",
	})
	if code != http.StatusCreated {
		t.Fatalf("[2] create: %d %s", code, body)
	}
	var created struct {
		Tenant struct {
			ID, Name, Slug, CreatedAt, UpdatedAt string
		}
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("[2] decode: %v", err)
	}
	if created.Tenant.ID == "" || created.Tenant.Name != "Acme Corp" {
		t.Errorf("[2] tenant echo wrong: %+v", created.Tenant)
	}
	if created.Tenant.Slug != "acme-corp" {
		t.Errorf("[2] slug auto-derive: got %q, want acme-corp", created.Tenant.Slug)
	}
	acmeID := created.Tenant.ID

	// === [3] Slug conflict on create (same string, different name) → 409 ===
	code, body = doJSON("POST", "/api/tenants", aliceTok, map[string]string{
		"name": "Acme Corp 2", "slug": "acme-corp",
	})
	if code != http.StatusConflict {
		t.Errorf("[3] dup slug: %d %s, want 409", code, body)
	}

	// === [4] Invalid slug format → 400 ===
	code, body = doJSON("POST", "/api/tenants", aliceTok, map[string]string{
		"name": "Bad", "slug": "BAD!SLUG",
	})
	if code != http.StatusBadRequest && code != 422 {
		t.Errorf("[4] bad slug: %d %s, want 400/422", code, body)
	}

	// === [5] Empty name → 400 ===
	code, body = doJSON("POST", "/api/tenants", aliceTok, map[string]string{
		"name": "   ",
	})
	if code != http.StatusBadRequest && code != 422 {
		t.Errorf("[5] empty name: %d %s, want 400/422", code, body)
	}

	// === [6] Alice lists → 1 tenant; Bob lists → 0 (membership scoping) ===
	code, body = doJSON("GET", "/api/tenants", aliceTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[6] list alice: %d %s", code, body)
	}
	var listed struct {
		Tenants []struct{ ID, Name, Slug string }
	}
	_ = json.Unmarshal(body, &listed)
	if len(listed.Tenants) != 1 || listed.Tenants[0].Slug != "acme-corp" {
		t.Errorf("[6] alice list: %+v, want 1 tenant 'acme-corp'", listed.Tenants)
	}
	code, body = doJSON("GET", "/api/tenants", bobTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[6] list bob: %d %s", code, body)
	}
	_ = json.Unmarshal(body, &listed)
	if len(listed.Tenants) != 0 {
		t.Errorf("[6] bob list: %+v, want 0", listed.Tenants)
	}

	// === [7] Bob GETs alice's tenant → 404 (no existence leak) ===
	code, _ = doJSON("GET", "/api/tenants/"+acmeID, bobTok, nil)
	if code != http.StatusNotFound {
		t.Errorf("[7] bob GET stranger tenant: %d, want 404", code)
	}
	// And MyRole likewise.
	code, _ = doJSON("GET", "/api/tenants/"+acmeID+"/me", bobTok, nil)
	if code != http.StatusNotFound {
		t.Errorf("[7] bob my-role stranger tenant: %d, want 404", code)
	}

	// === [8] Alice GET own tenant → 200; MyRole → owner+is_owner=true ===
	code, body = doJSON("GET", "/api/tenants/"+acmeID, aliceTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[8] alice GET own: %d %s", code, body)
	}
	code, body = doJSON("GET", "/api/tenants/"+acmeID+"/me", aliceTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[8] alice my-role: %d %s", code, body)
	}
	var role struct {
		Role    string `json:"role"`
		IsOwner bool   `json:"is_owner"`
	}
	_ = json.Unmarshal(body, &role)
	if role.Role != "owner" || !role.IsOwner {
		t.Errorf("[8] alice role: %+v, want owner+is_owner=true", role)
	}

	// === [9] Update: alice renames her tenant → 200, name updated ===
	rename := "Acme Renamed"
	code, body = doJSON("PATCH", "/api/tenants/"+acmeID, aliceTok, map[string]any{
		"name": rename,
	})
	if code != http.StatusOK {
		t.Fatalf("[9] update name: %d %s", code, body)
	}
	var updated struct {
		Tenant struct{ Name, Slug string }
	}
	_ = json.Unmarshal(body, &updated)
	if updated.Tenant.Name != rename {
		t.Errorf("[9] update name: got %q want %q", updated.Tenant.Name, rename)
	}

	// === [10] Bob tries to PATCH alice's tenant → 404 (membership gate) ===
	code, _ = doJSON("PATCH", "/api/tenants/"+acmeID, bobTok, map[string]any{
		"name": "Hijacked",
	})
	if code != http.StatusNotFound {
		t.Errorf("[10] bob PATCH stranger tenant: %d, want 404", code)
	}

	// === [11] Empty PATCH body → 400 ===
	code, _ = doJSON("PATCH", "/api/tenants/"+acmeID, aliceTok, map[string]any{})
	if code == http.StatusOK {
		t.Errorf("[11] empty PATCH treated as no-op should NOT 200 with our handler shape")
	}

	// === [12] Slug conflict on update → 409 ===
	// First make a second tenant with a known slug.
	_, _ = doJSON("POST", "/api/tenants", aliceTok, map[string]string{
		"name": "Beta", "slug": "beta",
	})
	code, body = doJSON("PATCH", "/api/tenants/"+acmeID, aliceTok, map[string]any{
		"slug": "beta",
	})
	if code != http.StatusConflict {
		t.Errorf("[12] dup slug on update: %d %s, want 409", code, body)
	}

	// === [13] Member-role gating: directly insert bob as 'member' so we can
	// verify the 403 on PATCH/DELETE. Going through an invite flow lives in
	// Sprint 2, so we shortcut with SQL — testing the authz, not the flow.
	bobUUID := principalID(t, srv.URL, c, bobTok)
	if _, err := pool.Exec(ctx, `
        INSERT INTO _tenant_members
            (tenant_id, collection_name, user_id, role, accepted_at, created_at, updated_at)
        VALUES ($1, 'users', $2, 'member', now(), now(), now())`,
		acmeID, bobUUID); err != nil {
		t.Fatalf("[13] insert bob membership: %v", err)
	}
	// Member CAN read the tenant.
	code, _ = doJSON("GET", "/api/tenants/"+acmeID, bobTok, nil)
	if code != http.StatusOK {
		t.Errorf("[13a] member GET: %d, want 200", code)
	}
	// Member CANNOT rename.
	code, _ = doJSON("PATCH", "/api/tenants/"+acmeID, bobTok, map[string]any{
		"name": "Member Rename",
	})
	if code != http.StatusForbidden {
		t.Errorf("[13b] member PATCH: %d, want 403", code)
	}
	// Member CANNOT delete.
	code, _ = doJSON("DELETE", "/api/tenants/"+acmeID, bobTok, nil)
	if code != http.StatusForbidden {
		t.Errorf("[13c] member DELETE: %d, want 403", code)
	}

	// === [14] Promote bob to 'admin' — admin may rename but NOT delete.
	if _, err := pool.Exec(ctx,
		`UPDATE _tenant_members SET role = 'admin' WHERE tenant_id = $1 AND user_id = $2`,
		acmeID, bobUUID); err != nil {
		t.Fatalf("[14] promote bob admin: %v", err)
	}
	code, _ = doJSON("PATCH", "/api/tenants/"+acmeID, bobTok, map[string]any{
		"name": "Admin-Renamed",
	})
	if code != http.StatusOK {
		t.Errorf("[14a] admin PATCH: %d, want 200", code)
	}
	code, _ = doJSON("DELETE", "/api/tenants/"+acmeID, bobTok, nil)
	if code != http.StatusForbidden {
		t.Errorf("[14b] admin DELETE: %d, want 403", code)
	}

	// === [15] Owner alice deletes → 204; list drops 'acme-corp' but keeps 'beta'.
	code, _ = doJSON("DELETE", "/api/tenants/"+acmeID, aliceTok, nil)
	if code != http.StatusNoContent {
		t.Errorf("[15] owner DELETE: %d, want 204", code)
	}
	code, body = doJSON("GET", "/api/tenants", aliceTok, nil)
	_ = json.Unmarshal(body, &listed)
	if len(listed.Tenants) != 1 || listed.Tenants[0].Slug != "beta" {
		t.Errorf("[15] after delete: %+v, want only [beta]", listed.Tenants)
	}
	// Subsequent GET on the deleted id → 404 (membership row still
	// exists but joined tenant is soft-deleted — store filters it).
	code, _ = doJSON("GET", "/api/tenants/"+acmeID, aliceTok, nil)
	if code != http.StatusNotFound {
		t.Errorf("[15b] GET deleted tenant: %d, want 404", code)
	}

	// === [16] Slug reuse after delete is permitted (partial unique index).
	code, body = doJSON("POST", "/api/tenants", aliceTok, map[string]string{
		"name": "Acme Reborn", "slug": "acme-corp",
	})
	if code != http.StatusCreated {
		t.Errorf("[16] re-create same slug after delete: %d %s, want 201", code, body)
	}

	// ====================================================================
	// Sprint 2 — per-tenant user-management.
	//
	// Setup: alice creates a fresh tenant "gamma"; she's its owner.
	// ====================================================================
	code, body = doJSON("POST", "/api/tenants", aliceTok, map[string]string{
		"name": "Gamma", "slug": "gamma",
	})
	if code != http.StatusCreated {
		t.Fatalf("[m-setup] create gamma: %d %s", code, body)
	}
	var gamma struct {
		Tenant struct{ ID, Slug string }
	}
	_ = json.Unmarshal(body, &gamma.Tenant)
	_ = json.Unmarshal(body, &gamma)
	gammaID := gamma.Tenant.ID

	// === [m1] Listing members at startup → 1 row (alice as owner, accepted) ===
	code, body = doJSON("GET", "/api/tenants/"+gammaID+"/members", aliceTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[m1] list members: %d %s", code, body)
	}
	var membersList struct {
		Members []struct {
			UserID       string `json:"user_id"`
			Role         string `json:"role"`
			InvitedEmail string `json:"invited_email"`
			IsPending    bool   `json:"is_pending"`
		}
	}
	_ = json.Unmarshal(body, &membersList)
	if len(membersList.Members) != 1 ||
		membersList.Members[0].Role != "owner" ||
		membersList.Members[0].IsPending {
		t.Errorf("[m1] startup roster wrong: %+v", membersList.Members)
	}

	// === [m2] Bob (already-signed-up user) invited by alice → direct-add path ===
	bobUUID2 := principalID(t, srv.URL, c, bobTok)
	_ = bobUUID2
	code, body = doJSON("POST", "/api/tenants/"+gammaID+"/members", aliceTok, map[string]string{
		"email": "bob@example.com", "role": "admin",
	})
	if code != http.StatusCreated {
		t.Fatalf("[m2] invite bob: %d %s", code, body)
	}
	var invited struct {
		Member struct {
			UserID    string `json:"user_id"`
			Role      string `json:"role"`
			IsPending bool   `json:"is_pending"`
		}
	}
	_ = json.Unmarshal(body, &invited)
	if invited.Member.IsPending {
		t.Errorf("[m2] bob already exists → should be direct-add, not pending: %+v", invited.Member)
	}
	if invited.Member.Role != "admin" {
		t.Errorf("[m2] bob role: %q, want admin", invited.Member.Role)
	}

	// Bob now sees gamma in his /api/tenants list.
	code, body = doJSON("GET", "/api/tenants", bobTok, nil)
	_ = json.Unmarshal(body, &listed)
	hasGamma := false
	for _, t := range listed.Tenants {
		if t.Slug == "gamma" {
			hasGamma = true
		}
	}
	if !hasGamma {
		t.Errorf("[m2b] bob list after invite: %+v, want gamma included", listed.Tenants)
	}

	// === [m3] Duplicate invite → 409 ===
	code, body = doJSON("POST", "/api/tenants/"+gammaID+"/members", aliceTok, map[string]string{
		"email": "bob@example.com", "role": "member",
	})
	if code != http.StatusConflict {
		t.Errorf("[m3] dup invite: %d %s, want 409", code, body)
	}

	// === [m4] Invite an UNKNOWN email → pending invite row ===
	code, body = doJSON("POST", "/api/tenants/"+gammaID+"/members", aliceTok, map[string]string{
		"email": "charlie@example.com", "role": "member",
	})
	if code != http.StatusCreated {
		t.Fatalf("[m4] pending invite: %d %s", code, body)
	}
	var pending struct {
		Member struct {
			UserID       string `json:"user_id"`
			IsPending    bool   `json:"is_pending"`
			InvitedEmail string `json:"invited_email"`
		}
	}
	_ = json.Unmarshal(body, &pending)
	if !pending.Member.IsPending || pending.Member.InvitedEmail != "charlie@example.com" {
		t.Errorf("[m4] expected pending row for charlie, got %+v", pending.Member)
	}
	pendingPlaceholderID := pending.Member.UserID

	// Re-sending the same invite must NOT create a duplicate.
	code, body = doJSON("POST", "/api/tenants/"+gammaID+"/members", aliceTok, map[string]string{
		"email": "charlie@example.com", "role": "admin",
	})
	if code != http.StatusCreated {
		t.Errorf("[m4b] resend pending invite: %d %s, want 201", code, body)
	}
	_ = json.Unmarshal(body, &pending)
	if pending.Member.UserID != pendingPlaceholderID {
		t.Errorf("[m4b] resend changed placeholder uuid: %q vs %q",
			pendingPlaceholderID, pending.Member.UserID)
	}

	// === [m5] Charlie signs up and accepts the invite ===
	charlieTok := signup("charlie@example.com")
	code, body = doJSON("POST", "/api/tenants/invites/accept", charlieTok, map[string]any{
		"tenant_id": gammaID,
	})
	if code != http.StatusOK {
		t.Fatalf("[m5] accept invite: %d %s", code, body)
	}
	var accepted struct {
		Member struct {
			Role      string `json:"role"`
			IsPending bool   `json:"is_pending"`
		}
	}
	_ = json.Unmarshal(body, &accepted)
	if accepted.Member.IsPending || accepted.Member.Role != "admin" {
		t.Errorf("[m5] accept result: %+v, want role=admin pending=false", accepted.Member)
	}

	// === [m6] Member roster now: alice (owner) + bob (admin) + charlie (admin) ===
	code, body = doJSON("GET", "/api/tenants/"+gammaID+"/members", aliceTok, nil)
	_ = json.Unmarshal(body, &membersList)
	if len(membersList.Members) != 3 {
		t.Errorf("[m6] roster size: %d, want 3 (%+v)", len(membersList.Members), membersList.Members)
	}

	// === [m7] Bob (admin) tries to promote himself to owner → 403 (admin can't grant owner) ===
	code, body = doJSON("PATCH",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2, bobTok,
		map[string]string{"role": "owner"})
	if code != http.StatusForbidden {
		t.Errorf("[m7] admin self-promote to owner: %d %s, want 403", code, body)
	}

	// === [m8] Alice promotes bob to owner → 204 ===
	code, body = doJSON("PATCH",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2, aliceTok,
		map[string]string{"role": "owner"})
	if code != http.StatusNoContent {
		t.Errorf("[m8] promote bob to owner: %d %s, want 204", code, body)
	}

	// === [m9] Alice (now-only-other-owner) is removed by bob → alice gone ===
	aliceUUID := principalID(t, srv.URL, c, aliceTok)
	code, body = doJSON("DELETE",
		"/api/tenants/"+gammaID+"/members/"+aliceUUID, bobTok, nil)
	if code != http.StatusNoContent {
		t.Errorf("[m9] bob removes alice: %d %s, want 204", code, body)
	}
	// Alice should no longer see gamma in her list.
	code, body = doJSON("GET", "/api/tenants", aliceTok, nil)
	_ = json.Unmarshal(body, &listed)
	for _, t2 := range listed.Tenants {
		if t2.Slug == "gamma" {
			t.Errorf("[m9b] alice still sees gamma after removal: %+v", listed.Tenants)
		}
	}

	// === [m10] Bob (last owner) attempts to demote himself → 409 last_owner ===
	code, body = doJSON("PATCH",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2, bobTok,
		map[string]string{"role": "admin"})
	if code != http.StatusConflict {
		t.Errorf("[m10] last-owner demote: %d %s, want 409", code, body)
	}

	// === [m11] Bob (last owner) attempts to remove himself → 409 last_owner ===
	code, body = doJSON("DELETE",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2, bobTok, nil)
	if code != http.StatusConflict {
		t.Errorf("[m11] last-owner self-remove: %d %s, want 409", code, body)
	}

	// === [m12] Charlie (admin) self-leaves → 204 (not last-owner; self-leave allowed) ===
	charlieUUID := principalID(t, srv.URL, c, charlieTok)
	code, body = doJSON("DELETE",
		"/api/tenants/"+gammaID+"/members/"+charlieUUID, charlieTok, nil)
	if code != http.StatusNoContent {
		t.Errorf("[m12] admin self-leave: %d %s, want 204", code, body)
	}

	// === [m13] Stranger (alice, not a member of gamma anymore) hits members list → 404 ===
	code, _ = doJSON("GET", "/api/tenants/"+gammaID+"/members", aliceTok, nil)
	if code != http.StatusNotFound {
		t.Errorf("[m13] non-member members-list: %d, want 404", code)
	}

	// ====================================================================
	// Sprint 3 — per-tenant audit-log slice.
	//
	// Seed a few tenant-scoped events directly via the audit writer
	// (the real signin path doesn't tag tenant_id; the per-tenant
	// audit emission happens from REST handlers via TenantEvent).
	// ====================================================================
	// Bootstrap the writer so the chain prev_hash is loaded.
	if err := auditWriter.Bootstrap(ctx); err != nil {
		t.Fatalf("[l-seed] bootstrap audit writer: %v", err)
	}
	bobUUIDP, _ := uuid.Parse(bobUUID2)
	gammaUUID, _ := uuid.Parse(gammaID)
	for i := 0; i < 3; i++ {
		if _, err := auditWriter.Write(ctx, audit.Event{
			TenantID:       gammaUUID,
			UserID:         bobUUIDP,
			UserCollection: "users",
			Event:          "tenant.member.invited",
			Outcome:        audit.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("[l-seed] write tenant audit: %v", err)
		}
	}
	// Bob (only owner of gamma) reads the log → sees the 3 rows.
	code, body = doJSON("GET", "/api/tenants/"+gammaID+"/logs", bobTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[l1] list logs: %d %s", code, body)
	}
	var logsPage struct {
		Page       int                      `json:"page"`
		PerPage    int                      `json:"perPage"`
		TotalItems int                      `json:"totalItems"`
		Items      []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(body, &logsPage); err != nil {
		t.Fatalf("[l1] decode: %v body=%s", err, body)
	}
	if logsPage.TotalItems < 3 {
		t.Errorf("[l1] totalItems = %d, want >=3", logsPage.TotalItems)
	}
	if len(logsPage.Items) < 3 {
		t.Errorf("[l1] items = %d, want >=3", len(logsPage.Items))
	}
	// Every row must be tagged with gamma's tenant_id — proves the
	// server-side filter is forced.
	for _, item := range logsPage.Items {
		if got, _ := item["tenant_id"].(string); got != gammaID {
			t.Errorf("[l1] row tenant_id = %v, want %s", item["tenant_id"], gammaID)
		}
	}
	// === [l2] Alice (no longer a member of gamma) → 404 from the gate ===
	code, _ = doJSON("GET", "/api/tenants/"+gammaID+"/logs", aliceTok, nil)
	if code != http.StatusNotFound {
		t.Errorf("[l2] non-member logs: %d, want 404", code)
	}
	// === [l3] event filter narrows result ===
	code, body = doJSON("GET",
		"/api/tenants/"+gammaID+"/logs?event=tenant.member", bobTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[l3] filter: %d %s", code, body)
	}
	_ = json.Unmarshal(body, &logsPage)
	if logsPage.TotalItems < 3 {
		t.Errorf("[l3] event filter cut too much: total=%d", logsPage.TotalItems)
	}
	code, body = doJSON("GET",
		"/api/tenants/"+gammaID+"/logs?event=nonexistent.never", bobTok, nil)
	_ = json.Unmarshal(body, &logsPage)
	if logsPage.TotalItems != 0 || len(logsPage.Items) != 0 {
		t.Errorf("[l3b] nonexistent event filter should return zero, got total=%d items=%d",
			logsPage.TotalItems, len(logsPage.Items))
	}

	// ====================================================================
	// Sprint 4 — per-tenant RBAC.
	//
	// Seed a tenant-scoped role directly via the rbac store, then exercise
	// the API: list, assign, list-mine, unassign.
	// ====================================================================
	finance, err := rbacStore.CreateRole(ctx, "finance_reviewer", rbac.ScopeTenant,
		"Reviewer for finance dashboards")
	if err != nil {
		t.Fatalf("[rbac-seed] create role: %v", err)
	}
	// Also seed a site-scoped role to verify it's filtered out of the
	// tenant /roles surface.
	if _, err := rbacStore.CreateRole(ctx, "site_only_role", rbac.ScopeSite, "should not appear"); err != nil {
		t.Fatalf("[rbac-seed] create site role: %v", err)
	}

	// === [r1] Member (bob, owner of gamma) lists tenant roles ===
	code, body = doJSON("GET", "/api/tenants/"+gammaID+"/roles", bobTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[r1] list roles: %d %s", code, body)
	}
	var rolesList struct {
		Roles []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			IsSystem bool   `json:"is_system"`
		}
	}
	_ = json.Unmarshal(body, &rolesList)
	foundFinance := false
	for _, role := range rolesList.Roles {
		if role.Name == "site_only_role" {
			t.Errorf("[r1] site role leaked into tenant roles list: %+v", role)
		}
		if role.Name == "finance_reviewer" {
			foundFinance = true
		}
	}
	if !foundFinance {
		t.Errorf("[r1] finance_reviewer missing from list: %+v", rolesList.Roles)
	}

	// === [r2] Bob assigns finance_reviewer to himself by name ===
	code, body = doJSON("POST",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2+"/roles", bobTok,
		map[string]string{"role_name": "finance_reviewer"})
	if code != http.StatusNoContent {
		t.Errorf("[r2] assign role: %d %s, want 204", code, body)
	}

	// === [r3] listMemberRoles echoes the assignment ===
	code, body = doJSON("GET",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2+"/roles", bobTok, nil)
	if code != http.StatusOK {
		t.Fatalf("[r3] list member roles: %d %s", code, body)
	}
	var memberRoles struct {
		Roles []struct{ Name string }
	}
	_ = json.Unmarshal(body, &memberRoles)
	hasFinance := false
	for _, role := range memberRoles.Roles {
		if role.Name == "finance_reviewer" {
			hasFinance = true
		}
	}
	if !hasFinance {
		t.Errorf("[r3] member roles missing finance_reviewer: %+v", memberRoles.Roles)
	}

	// === [r4] Unauth alice (no longer a member of gamma) → 404 from the gate ===
	code, _ = doJSON("GET", "/api/tenants/"+gammaID+"/roles", aliceTok, nil)
	if code != http.StatusNotFound {
		t.Errorf("[r4] non-member list-roles: %d, want 404", code)
	}
	code, _ = doJSON("POST",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2+"/roles", aliceTok,
		map[string]string{"role_name": "finance_reviewer"})
	if code != http.StatusNotFound {
		t.Errorf("[r4b] non-member assign: %d, want 404", code)
	}

	// === [r5] Unassign by role id ===
	code, body = doJSON("DELETE",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2+"/roles/"+finance.ID.String(),
		bobTok, nil)
	if code != http.StatusNoContent {
		t.Errorf("[r5] unassign: %d %s, want 204", code, body)
	}
	// Verify gone.
	code, body = doJSON("GET",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2+"/roles", bobTok, nil)
	_ = json.Unmarshal(body, &memberRoles)
	for _, role := range memberRoles.Roles {
		if role.Name == "finance_reviewer" {
			t.Errorf("[r5b] role still present after unassign: %+v", memberRoles.Roles)
		}
	}

	// === [r6] Assigning a site-scoped role through the tenant endpoint → 403 ===
	// (find the site role's id from the rbac store)
	siteRole, err := rbacStore.GetRole(ctx, "site_only_role", rbac.ScopeSite)
	if err != nil {
		t.Fatalf("[r6] get site role: %v", err)
	}
	code, body = doJSON("POST",
		"/api/tenants/"+gammaID+"/members/"+bobUUID2+"/roles", bobTok,
		map[string]string{"role_id": siteRole.ID.String()})
	if code != http.StatusForbidden {
		t.Errorf("[r6] assign site-scoped role via tenant endpoint: %d %s, want 403", code, body)
	}
}

// principalID rips the caller's user id out of /api/auth/me so we
// don't have to hand-track the uuid returned by signup. Cheap;
// confined to the test path.
func principalID(t *testing.T, base string, c *http.Client, token string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Record struct{ ID string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("me decode: %v", err)
	}
	if out.Record.ID == "" {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("me empty id: %s", b)
	}
	return out.Record.ID
}

