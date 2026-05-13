//go:build embed_pg

// v1.7.51 follow-up — end-to-end test for SCIM-group → RBAC-role
// reconciliation. Walks the full IdP-driven provisioning flow:
//
//   1. Operator pre-creates a custom RBAC role ("scim-test-role").
//   2. SCIM client creates Group G + maps G → role via direct SQL
//      (the operator's setup task — not in the SCIM wire surface).
//   3. SCIM client POSTs user U + PATCHes G to add U → expect
//      _user_roles row appears for (U, role).
//   4. PATCH G to remove U → expect _user_roles row disappears.
//   5. Coverage for the "user still in another group mapping the
//      same role" case: add U to G1 and G2 both mapped to role R;
//      remove from G1 → role R MUST remain (granted via G2).

package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

// scimRoleHarness boots embedded PG, applies sys migrations, creates
// a `users` auth-collection, mints a SCIM token, and exposes the
// rbacStore + a chi router with the SCIM routes mounted.
type scimRoleHarness struct {
	pool      *pgxpool.Pool
	stop      embedded.StopFunc
	rbacStore *rbac.Store
	tokens    *scimauth.TokenStore
	server    *httptest.Server
	rawToken  string
	t         *testing.T
}

func bootHarness(t *testing.T) *scimRoleHarness {
	t.Helper()
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
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = stopPG()
		t.Fatal(err)
	}

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		pool.Close()
		_ = stopPG()
		t.Fatal(err)
	}

	// users auth-collection.
	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(users)
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		pool.Close()
		_ = stopPG()
		t.Fatalf("create users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
        ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id TEXT;
        ALTER TABLE users ADD COLUMN IF NOT EXISTS scim_managed BOOLEAN NOT NULL DEFAULT FALSE;
    `); err != nil {
		pool.Close()
		_ = stopPG()
		t.Fatalf("add scim cols: %v", err)
	}

	var key secret.Key
	for i := range key {
		key[i] = byte(i)
	}
	tokens := scimauth.NewTokenStore(pool, key)
	raw, _, err := tokens.Create(ctx, scimauth.CreateInput{
		Name: "test", Collection: "users", TTL: time.Hour,
	})
	if err != nil {
		pool.Close()
		_ = stopPG()
		t.Fatalf("mint token: %v", err)
	}

	rbacStore := rbac.NewStore(pool)

	r := chi.NewRouter()
	Mount(r, &Deps{Pool: pool, Tokens: tokens, RBAC: rbacStore})
	srv := httptest.NewServer(r)

	t.Cleanup(func() {
		registry.Reset()
		srv.Close()
		pool.Close()
		_ = stopPG()
	})

	return &scimRoleHarness{
		pool:      pool,
		stop:      stopPG,
		rbacStore: rbacStore,
		tokens:    tokens,
		server:    srv,
		rawToken:  raw,
		t:         t,
	}
}

func (h *scimRoleHarness) bearerReq(method, path string, body []byte) (*http.Response, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.server.URL+path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	req.Header.Set("Authorization", "Bearer "+h.rawToken)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// scimCreateUser creates a SCIM user, returns its UUID.
func (h *scimRoleHarness) scimCreateUser(userName string) uuid.UUID {
	h.t.Helper()
	body := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
        "userName":%q,"active":true,
        "emails":[{"value":%q,"primary":true}]
    }`, userName, userName)
	resp, out := h.bearerReq("POST", "/scim/v2/Users", []byte(body))
	if resp.StatusCode != 201 {
		h.t.Fatalf("create user: %d body=%s", resp.StatusCode, out)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	id, _ := uuid.Parse(got["id"].(string))
	return id
}

// scimCreateGroup creates a SCIM group, returns its UUID.
func (h *scimRoleHarness) scimCreateGroup(displayName string) uuid.UUID {
	h.t.Helper()
	body := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
        "displayName":%q,
        "members":[]
    }`, displayName)
	resp, out := h.bearerReq("POST", "/scim/v2/Groups", []byte(body))
	if resp.StatusCode != 201 {
		h.t.Fatalf("create group: %d body=%s", resp.StatusCode, out)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	id, _ := uuid.Parse(got["id"].(string))
	return id
}

// mapGroupToRole inserts a row into _scim_group_role_map via the
// pool — the wire surface to do this is operator's job; not in SCIM.
func (h *scimRoleHarness) mapGroupToRole(groupID, roleID uuid.UUID) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(),
		`INSERT INTO _scim_group_role_map (scim_group_id, role_id) VALUES ($1, $2)`,
		groupID, roleID); err != nil {
		h.t.Fatalf("map group→role: %v", err)
	}
}

// hasUserRole returns true if (collection, user_id, role_id) row
// exists in _user_roles with tenant_id NULL (site-scoped).
func (h *scimRoleHarness) hasUserRole(userID, roleID uuid.UUID) bool {
	h.t.Helper()
	var exists bool
	if err := h.pool.QueryRow(context.Background(), `
        SELECT EXISTS (
            SELECT 1 FROM _user_roles
             WHERE collection_name = 'users'
               AND record_id = $1 AND role_id = $2 AND tenant_id IS NULL
        )`, userID, roleID).Scan(&exists); err != nil {
		h.t.Fatalf("check role: %v", err)
	}
	return exists
}

// TestSCIM_RoleMapping_PATCH_AddGrants — adding a user to a mapped
// group via PATCH writes a _user_roles row.
func TestSCIM_RoleMapping_PATCH_AddGrants(t *testing.T) {
	h := bootHarness(t)
	ctx := context.Background()

	role, err := h.rbacStore.CreateRole(ctx, "scim-test-developer", rbac.ScopeSite, "Test role")
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	groupID := h.scimCreateGroup("Engineering")
	h.mapGroupToRole(groupID, role.ID)
	userID := h.scimCreateUser("alice@example.com")

	// Pre-condition: no role yet.
	if h.hasUserRole(userID, role.ID) {
		t.Fatalf("[pre] user should not have role yet")
	}

	// PATCH add member.
	patchBody := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        "Operations":[{"op":"add","path":"members","value":[{"value":%q,"type":"User"}]}]
    }`, userID)
	resp, out := h.bearerReq("PATCH", "/scim/v2/Groups/"+groupID.String(), []byte(patchBody))
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH add: %d body=%s", resp.StatusCode, out)
	}

	if !h.hasUserRole(userID, role.ID) {
		t.Fatalf("[post-add] user should have role")
	}
}

// TestSCIM_RoleMapping_PATCH_RemoveRevokes — removing a user from a
// mapped group drops the role from _user_roles.
func TestSCIM_RoleMapping_PATCH_RemoveRevokes(t *testing.T) {
	h := bootHarness(t)
	ctx := context.Background()

	role, _ := h.rbacStore.CreateRole(ctx, "scim-test-developer", rbac.ScopeSite, "Test role")
	groupID := h.scimCreateGroup("Engineering")
	h.mapGroupToRole(groupID, role.ID)
	userID := h.scimCreateUser("bob@example.com")

	// Add → assert grant.
	addBody := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        "Operations":[{"op":"add","path":"members","value":[{"value":%q,"type":"User"}]}]
    }`, userID)
	resp, _ := h.bearerReq("PATCH", "/scim/v2/Groups/"+groupID.String(), []byte(addBody))
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH add: %d", resp.StatusCode)
	}
	if !h.hasUserRole(userID, role.ID) {
		t.Fatalf("[pre-remove] user should have role")
	}

	// Remove (per-user form: members[value eq "<uuid>"]).
	rmBody := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        "Operations":[{"op":"remove","path":"members[value eq \"%s\"]"}]
    }`, userID.String())
	resp, out := h.bearerReq("PATCH", "/scim/v2/Groups/"+groupID.String(), []byte(rmBody))
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH remove: %d body=%s", resp.StatusCode, out)
	}
	if h.hasUserRole(userID, role.ID) {
		t.Fatalf("[post-remove] user should NOT have role")
	}
}

// TestSCIM_RoleMapping_RoleSurvivesRemovalViaOtherGroup — when a role
// is mapped via multiple groups, removing a user from only ONE of
// them must leave the role intact.
func TestSCIM_RoleMapping_RoleSurvivesRemovalViaOtherGroup(t *testing.T) {
	h := bootHarness(t)
	ctx := context.Background()

	role, _ := h.rbacStore.CreateRole(ctx, "shared-role", rbac.ScopeSite, "")
	g1 := h.scimCreateGroup("eng-backend")
	g2 := h.scimCreateGroup("eng-platform")
	h.mapGroupToRole(g1, role.ID)
	h.mapGroupToRole(g2, role.ID)
	userID := h.scimCreateUser("carol@example.com")

	// Add to BOTH groups.
	addBody := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        "Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]
    }`, userID)
	for _, gid := range []uuid.UUID{g1, g2} {
		resp, _ := h.bearerReq("PATCH", "/scim/v2/Groups/"+gid.String(), []byte(addBody))
		if resp.StatusCode != 200 {
			t.Fatalf("PATCH add to %s: %d", gid, resp.StatusCode)
		}
	}
	if !h.hasUserRole(userID, role.ID) {
		t.Fatalf("[pre] user should have role via both groups")
	}

	// Remove from g1 ONLY.
	rmBody := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        "Operations":[{"op":"remove","path":"members[value eq \"%s\"]"}]
    }`, userID.String())
	resp, _ := h.bearerReq("PATCH", "/scim/v2/Groups/"+g1.String(), []byte(rmBody))
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH remove g1: %d", resp.StatusCode)
	}

	// Role MUST remain — granted via g2.
	if !h.hasUserRole(userID, role.ID) {
		t.Fatalf("[post-remove-g1] user should STILL have role (granted via g2)")
	}

	// Now remove from g2 too — role MUST disappear.
	resp, _ = h.bearerReq("PATCH", "/scim/v2/Groups/"+g2.String(), []byte(rmBody))
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH remove g2: %d", resp.StatusCode)
	}
	if h.hasUserRole(userID, role.ID) {
		t.Fatalf("[post-remove-g2] user should NOT have role anymore")
	}
}

// TestSCIM_RoleMapping_PUT_FullReplace — full PUT replacement diffs
// old vs new members; only the delta drives role grant/revoke.
func TestSCIM_RoleMapping_PUT_FullReplace(t *testing.T) {
	h := bootHarness(t)
	ctx := context.Background()

	role, _ := h.rbacStore.CreateRole(ctx, "put-role", rbac.ScopeSite, "")
	g := h.scimCreateGroup("Group")
	h.mapGroupToRole(g, role.ID)
	alice := h.scimCreateUser("alice@put.example.com")
	bob := h.scimCreateUser("bob@put.example.com")

	// PUT with alice only.
	put1 := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
        "displayName":"Group",
        "members":[{"value":%q}]
    }`, alice)
	resp, _ := h.bearerReq("PUT", "/scim/v2/Groups/"+g.String(), []byte(put1))
	if resp.StatusCode != 200 {
		t.Fatalf("PUT#1: %d", resp.StatusCode)
	}
	if !h.hasUserRole(alice, role.ID) {
		t.Fatalf("[PUT#1] alice should have role")
	}
	if h.hasUserRole(bob, role.ID) {
		t.Fatalf("[PUT#1] bob must NOT have role")
	}

	// PUT replacing with bob only — alice drops, bob gains.
	put2 := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
        "displayName":"Group",
        "members":[{"value":%q}]
    }`, bob)
	resp, _ = h.bearerReq("PUT", "/scim/v2/Groups/"+g.String(), []byte(put2))
	if resp.StatusCode != 200 {
		t.Fatalf("PUT#2: %d", resp.StatusCode)
	}
	if h.hasUserRole(alice, role.ID) {
		t.Fatalf("[PUT#2] alice should no longer have role")
	}
	if !h.hasUserRole(bob, role.ID) {
		t.Fatalf("[PUT#2] bob should have role")
	}
}

// TestSCIM_RoleMapping_DeleteGroup_RevokesAll — deleting a group
// revokes the role from every member who held it ONLY via that group.
func TestSCIM_RoleMapping_DeleteGroup_RevokesAll(t *testing.T) {
	h := bootHarness(t)
	ctx := context.Background()

	role, _ := h.rbacStore.CreateRole(ctx, "del-role", rbac.ScopeSite, "")
	g := h.scimCreateGroup("ToDelete")
	h.mapGroupToRole(g, role.ID)
	alice := h.scimCreateUser("alice@del.example.com")

	// Add alice + grant role via PATCH.
	addBody := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        "Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]
    }`, alice)
	resp, _ := h.bearerReq("PATCH", "/scim/v2/Groups/"+g.String(), []byte(addBody))
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH add: %d", resp.StatusCode)
	}
	if !h.hasUserRole(alice, role.ID) {
		t.Fatalf("[pre-delete] alice should have role")
	}

	// Delete the group.
	resp, _ = h.bearerReq("DELETE", "/scim/v2/Groups/"+g.String(), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
	if h.hasUserRole(alice, role.ID) {
		t.Fatalf("[post-delete] alice should not have role anymore (only granted via deleted group)")
	}
}

// TestSCIM_RoleMapping_NilRBAC_NoOp — if rbacStore is nil (e.g.
// minimal test deployments), reconciliation is a no-op and SCIM
// PATCH still succeeds.
func TestSCIM_RoleMapping_NilRBAC_NoOp(t *testing.T) {
	h := bootHarness(t)
	// Re-mount with nil RBAC to verify graceful degradation.
	r := chi.NewRouter()
	Mount(r, &Deps{Pool: h.pool, Tokens: h.tokens, RBAC: nil})
	srv2 := httptest.NewServer(r)
	defer srv2.Close()

	body := []byte(`{
        "schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
        "displayName":"NilRBAC","members":[]
    }`)
	req, _ := http.NewRequest("POST", srv2.URL+"/scim/v2/Groups", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.rawToken)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create with nil RBAC: %d", resp.StatusCode)
	}
}
