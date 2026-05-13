//go:build embed_pg

// Block A follow-up — DELETE /Users/{id} soft-delete toggle.
//
// The `auth.scim.soft_delete` boolean setting controls whether SCIM
// DELETE physically removes the row or tombstones it via the
// `.SoftDelete()` builder's `deleted TIMESTAMPTZ` column.
//
// Both pre-conditions MUST hold for the soft path (toggle=true AND
// collection.SoftDelete=true); either missing falls back to hard
// DELETE (graceful degradation). Regardless of branch, role-grants
// the user held via SCIM group memberships are revoked — a
// deprovisioned user must not retain group-granted RBAC roles even
// when the row is preserved for audit.
//
// We bypass the standard scimRoleHarness Mount() path because the
// production Deps wiring doesn't (yet) thread Settings through to
// UsersDeps — that's the Block B "wire-up" follow-up. Tests construct
// UsersDeps directly so the toggle is exercised end-to-end.

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
	"github.com/railbase/railbase/internal/settings"
)

// scimDeleteHarness is the soft-delete test fixture. Compared to the
// scimRoleHarness it: (a) lets the caller pick SoftDelete on the
// users auth-collection, (b) constructs UsersDeps directly with
// Settings + RBAC injected, and (c) exposes the Settings manager so
// tests can flip `auth.scim.soft_delete` per-case.
type scimDeleteHarness struct {
	pool      *pgxpool.Pool
	stop      embedded.StopFunc
	rbacStore *rbac.Store
	tokens    *scimauth.TokenStore
	server    *httptest.Server
	rawToken  string
	settings  *settings.Manager
	t         *testing.T
}

func bootDeleteHarness(t *testing.T, softDeleteCollection bool) *scimDeleteHarness {
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

	// users auth-collection — optional .SoftDelete() so the toggle's
	// "collection lacks the column" branch is exercisable.
	users := schemabuilder.NewAuthCollection("users")
	if softDeleteCollection {
		users.SoftDelete()
	}
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
	mgr := settings.New(settings.Options{Pool: pool, Log: log})

	// Mount Users routes directly so we can inject Settings + RBAC
	// into UsersDeps. Groups are mounted via the production Mount()
	// path because the TestSCIM_Delete_RoleGrantsRevoked test PATCHes
	// a group to seed the user → role relationship.
	r := chi.NewRouter()
	r.Route("/scim/v2", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(AuthMiddleware(tokens))
			MountUsers(r, &UsersDeps{
				Pool:     pool,
				Settings: mgr,
				RBAC:     rbacStore,
			})
			MountGroups(r, &GroupsDeps{Pool: pool, RBAC: rbacStore})
		})
	})
	srv := httptest.NewServer(r)

	t.Cleanup(func() {
		registry.Reset()
		srv.Close()
		pool.Close()
		_ = stopPG()
	})

	return &scimDeleteHarness{
		pool:      pool,
		stop:      stopPG,
		rbacStore: rbacStore,
		tokens:    tokens,
		server:    srv,
		rawToken:  raw,
		settings:  mgr,
		t:         t,
	}
}

func (h *scimDeleteHarness) bearerReq(method, path string, body []byte) (*http.Response, []byte) {
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

func (h *scimDeleteHarness) createUser(userName string) uuid.UUID {
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

func (h *scimDeleteHarness) createGroup(displayName string) uuid.UUID {
	h.t.Helper()
	body := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
        "displayName":%q,"members":[]
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

func (h *scimDeleteHarness) mapGroupToRole(groupID, roleID uuid.UUID) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(),
		`INSERT INTO _scim_group_role_map (scim_group_id, role_id) VALUES ($1, $2)`,
		groupID, roleID); err != nil {
		h.t.Fatalf("map group→role: %v", err)
	}
}

func (h *scimDeleteHarness) addMember(groupID, userID uuid.UUID) {
	h.t.Helper()
	body := fmt.Sprintf(`{
        "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        "Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]
    }`, userID)
	resp, out := h.bearerReq("PATCH", "/scim/v2/Groups/"+groupID.String(), []byte(body))
	if resp.StatusCode != 200 {
		h.t.Fatalf("PATCH add: %d body=%s", resp.StatusCode, out)
	}
}

// countUserRows returns how many rows exist for the user id (with any
// `deleted` state). Used to distinguish hard-delete (count=0) from
// soft-delete (count=1).
func (h *scimDeleteHarness) countUserRows(uid uuid.UUID) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM users WHERE id = $1`, uid).Scan(&n); err != nil {
		h.t.Fatalf("count: %v", err)
	}
	return n
}

// userIsTombstoned returns true iff the row exists AND `deleted` is
// not NULL. Used for the soft-delete asserts.
func (h *scimDeleteHarness) userIsTombstoned(uid uuid.UUID) bool {
	h.t.Helper()
	var deleted *time.Time
	if err := h.pool.QueryRow(context.Background(),
		`SELECT deleted FROM users WHERE id = $1`, uid).Scan(&deleted); err != nil {
		return false
	}
	return deleted != nil
}

// hasUserRole — site-scoped grant probe, mirrors scimRoleHarness for
// the role-revocation assertion.
func (h *scimDeleteHarness) hasUserRole(userID, roleID uuid.UUID) bool {
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

// TestSCIM_Delete_HardWhenSettingOff — default setting is unset/false;
// DELETE must physically remove the row even when the collection was
// declared with .SoftDelete().
func TestSCIM_Delete_HardWhenSettingOff(t *testing.T) {
	h := bootDeleteHarness(t, /*softDeleteCollection=*/ true)
	uid := h.createUser("hard@example.com")

	// Pre: setting is unset → GetBool returns ok=false → fallback = hard.
	resp, out := h.bearerReq("DELETE", "/scim/v2/Users/"+uid.String(), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("DELETE: %d body=%s", resp.StatusCode, out)
	}
	if n := h.countUserRows(uid); n != 0 {
		t.Fatalf("expected hard-delete (rows=0), got rows=%d", n)
	}
}

// TestSCIM_Delete_SoftWhenBothEnabled — both pre-conditions hold:
// `auth.scim.soft_delete=true` AND collection has .SoftDelete(). The
// row MUST remain with `deleted IS NOT NULL`.
func TestSCIM_Delete_SoftWhenBothEnabled(t *testing.T) {
	h := bootDeleteHarness(t, /*softDeleteCollection=*/ true)
	if err := h.settings.Set(context.Background(), "auth.scim.soft_delete", true); err != nil {
		t.Fatalf("set toggle: %v", err)
	}
	uid := h.createUser("soft@example.com")

	resp, out := h.bearerReq("DELETE", "/scim/v2/Users/"+uid.String(), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("DELETE: %d body=%s", resp.StatusCode, out)
	}
	if n := h.countUserRows(uid); n != 1 {
		t.Fatalf("expected soft-delete (rows=1), got rows=%d", n)
	}
	if !h.userIsTombstoned(uid) {
		t.Fatalf("expected `deleted IS NOT NULL`, got NULL")
	}

	// Idempotency: second DELETE on the same tombstoned row finds
	// nothing matching `AND deleted IS NULL` → 404. The user already
	// got the audit-trail row; double-deprovisioning shouldn't
	// silently 204 (RFC 7644 §3.6 expects a 404 for "not found").
	resp, _ = h.bearerReq("DELETE", "/scim/v2/Users/"+uid.String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("second DELETE on tombstone: expected 404, got %d", resp.StatusCode)
	}
}

// TestSCIM_Delete_HardWhenCollectionNotSoftDelete — the setting is on
// but the collection wasn't declared with .SoftDelete(); the `deleted`
// column doesn't exist. The handler MUST gracefully fall back to hard
// DELETE rather than 500ing on a missing-column error.
func TestSCIM_Delete_HardWhenCollectionNotSoftDelete(t *testing.T) {
	h := bootDeleteHarness(t, /*softDeleteCollection=*/ false)
	if err := h.settings.Set(context.Background(), "auth.scim.soft_delete", true); err != nil {
		t.Fatalf("set toggle: %v", err)
	}
	uid := h.createUser("graceful@example.com")

	resp, out := h.bearerReq("DELETE", "/scim/v2/Users/"+uid.String(), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("DELETE: %d body=%s", resp.StatusCode, out)
	}
	if n := h.countUserRows(uid); n != 0 {
		t.Fatalf("expected hard-delete fallback (rows=0), got rows=%d", n)
	}
}

// TestSCIM_Delete_RoleGrantsRevoked — deprovisioning (soft OR hard)
// MUST revoke every role the user held via a SCIM group's mapped
// role. Otherwise a "soft" delete would leave a logically-inactive
// user with live RBAC grants — and any subsequent admin reactivation
// would silently bypass IdP intent.
func TestSCIM_Delete_RoleGrantsRevoked(t *testing.T) {
	h := bootDeleteHarness(t, /*softDeleteCollection=*/ true)
	ctx := context.Background()

	if err := h.settings.Set(ctx, "auth.scim.soft_delete", true); err != nil {
		t.Fatalf("set toggle: %v", err)
	}
	role, err := h.rbacStore.CreateRole(ctx, "scim-soft-delete-role", rbac.ScopeSite, "")
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	gid := h.createGroup("Engineering")
	h.mapGroupToRole(gid, role.ID)
	uid := h.createUser("soft-role@example.com")
	h.addMember(gid, uid)

	// Pre: user has the role via group membership.
	if !h.hasUserRole(uid, role.ID) {
		t.Fatalf("[pre] user should have role")
	}

	// DELETE the user (soft-delete branch).
	resp, out := h.bearerReq("DELETE", "/scim/v2/Users/"+uid.String(), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("DELETE: %d body=%s", resp.StatusCode, out)
	}

	// Post: tombstoned user MUST NOT retain the group-granted role.
	if !h.userIsTombstoned(uid) {
		t.Fatalf("expected soft-delete tombstone, got hard")
	}
	if h.hasUserRole(uid, role.ID) {
		t.Fatalf("[post-delete] user should NOT have role anymore (deprovisioned)")
	}
}
