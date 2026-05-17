//go:build embed_pg

// Live RBAC smoke. Runs against a real Postgres applying the v1.1.4
// seed migrations, exercising:
//
//	1. seed-rolls — 8 default roles created on bootstrap
//	2. seed grants — site:admin holds the documented action set
//	3. CreateRole / DeleteRole + system-role refusal
//	4. Grant / Revoke idempotency
//	5. Assign / Unassign with site + tenant scopes
//	6. Resolve composes site + tenant assignments into Actions
//	7. SiteBypass for system_admin
//	8. TenantBypass for tenant:owner — only on tenant.* actions
//
// Run:
//	go test -tags embed_pg -run TestRBACFlowE2E -timeout 60s \
//	    ./internal/rbac/...

package rbac

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac/actionkeys"
)

func TestRBACFlowE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	store := NewStore(pool)

	// === [1] Seed: 9 default roles present ===
	// 8 from 0013_rbac_seed (system_admin/admin/user/guest at site scope +
	// owner/admin/member/viewer at tenant scope) + 1 from 0029_rbac_admin_bridge
	// (system_readonly at site scope).
	all, err := store.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 9 {
		t.Errorf("[1] expected 9 seed roles, got %d", len(all))
	}
	seen := map[string]bool{}
	for _, r := range all {
		seen[string(r.Scope)+":"+r.Name] = true
		if !r.IsSystem {
			t.Errorf("[1] seed role %s:%s should be is_system", r.Scope, r.Name)
		}
	}
	for _, want := range []string{
		"site:system_admin", "site:system_readonly", "site:admin", "site:user", "site:guest",
		"tenant:owner", "tenant:admin", "tenant:member", "tenant:viewer",
	} {
		if !seen[want] {
			t.Errorf("[1] missing seed role %s", want)
		}
	}
	t.Logf("[1] seed: 9 roles confirmed")

	// === [2] Seed grants: site:admin has audit.read ===
	siteAdmin, err := store.GetRole(ctx, "admin", ScopeSite)
	if err != nil {
		t.Fatal(err)
	}
	adminActions, err := store.ListActions(ctx, siteAdmin.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasAudit := false
	for _, a := range adminActions {
		if a == actionkeys.AuditRead {
			hasAudit = true
		}
	}
	if !hasAudit {
		t.Errorf("[2] site:admin should have audit.read in seed: %v", adminActions)
	}
	t.Logf("[2] site:admin grants %d actions including audit.read", len(adminActions))

	// === [3] CreateRole / DeleteRole + system-role refusal ===
	custom, err := store.CreateRole(ctx, "moderator", ScopeSite, "spam patrol")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRole(ctx, siteAdmin.ID); err == nil {
		t.Errorf("[3] deleting a system role should fail")
	}
	if err := store.DeleteRole(ctx, custom.ID); err != nil {
		t.Errorf("[3] deleting custom role failed: %v", err)
	}
	t.Logf("[3] custom-role create+delete OK, system-role delete refused")

	// === [4] Grant / Revoke idempotency ===
	customRole, _ := store.CreateRole(ctx, "tester", ScopeSite, "")
	defer store.DeleteRole(ctx, customRole.ID) //nolint:errcheck
	if err := store.Grant(ctx, customRole.ID, actionkeys.AuditRead); err != nil {
		t.Fatal(err)
	}
	if err := store.Grant(ctx, customRole.ID, actionkeys.AuditRead); err != nil {
		t.Errorf("[4] re-grant should be idempotent: %v", err)
	}
	if err := store.Revoke(ctx, customRole.ID, actionkeys.AuditRead); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, customRole.ID, actionkeys.AuditRead); err != nil {
		t.Errorf("[4] double-revoke should be idempotent: %v", err)
	}
	t.Logf("[4] grant/revoke idempotency OK")

	// === [5] Assign / Unassign site + tenant scopes ===
	userColl := "users"
	userID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	siteUser, _ := store.GetRole(ctx, "user", ScopeSite)
	tenantMember, _ := store.GetRole(ctx, "member", ScopeTenant)

	if _, err := store.Assign(ctx, AssignInput{
		CollectionName: userColl, RecordID: userID, RoleID: siteUser.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assign(ctx, AssignInput{
		CollectionName: userColl, RecordID: userID, RoleID: tenantMember.ID,
		TenantID: &tenantID,
	}); err != nil {
		t.Fatal(err)
	}
	// Re-assign should be idempotent.
	if _, err := store.Assign(ctx, AssignInput{
		CollectionName: userColl, RecordID: userID, RoleID: siteUser.ID,
	}); err != nil {
		t.Errorf("[5] re-assign should be idempotent: %v", err)
	}
	t.Logf("[5] assignments OK")

	// === [6] Resolve composes site + tenant actions ===
	r, err := store.Resolve(ctx, userColl, userID, &tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if r.SiteBypass {
		t.Errorf("[6] unexpected SiteBypass for regular user")
	}
	if r.TenantBypass {
		t.Errorf("[6] unexpected TenantBypass for tenant:member")
	}
	// site:user grants auth.me; tenant:member grants tenant.records.list.
	if !r.Has(actionkeys.AuthMe) {
		t.Errorf("[6] should have AuthMe via site:user")
	}
	if !r.Has(actionkeys.TenantRecordsList) {
		t.Errorf("[6] should have TenantRecordsList via tenant:member")
	}
	if r.Has(actionkeys.AdminsList) {
		t.Errorf("[6] should NOT have AdminsList (not granted)")
	}
	t.Logf("[6] resolved %d actions for composite assignment", len(r.Actions))

	// === [7] SiteBypass for system_admin ===
	sysAdminID := uuid.Must(uuid.NewV7())
	sysRole, _ := store.GetRole(ctx, "system_admin", ScopeSite)
	if _, err := store.Assign(ctx, AssignInput{
		CollectionName: userColl, RecordID: sysAdminID, RoleID: sysRole.ID,
	}); err != nil {
		t.Fatal(err)
	}
	r2, err := store.Resolve(ctx, userColl, sysAdminID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.SiteBypass {
		t.Errorf("[7] system_admin should set SiteBypass")
	}
	if !r2.Has(actionkeys.ActionKey("custom.action.we.never.granted")) {
		t.Errorf("[7] SiteBypass should grant everything")
	}
	t.Logf("[7] SiteBypass works")

	// === [8] TenantBypass for tenant:owner — ONLY tenant.* actions ===
	ownerID := uuid.Must(uuid.NewV7())
	ownerRole, _ := store.GetRole(ctx, "owner", ScopeTenant)
	if _, err := store.Assign(ctx, AssignInput{
		CollectionName: userColl, RecordID: ownerID, RoleID: ownerRole.ID,
		TenantID: &tenantID,
	}); err != nil {
		t.Fatal(err)
	}
	r3, err := store.Resolve(ctx, userColl, ownerID, &tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if r3.SiteBypass {
		t.Errorf("[8] tenant:owner should NOT trigger SiteBypass")
	}
	if !r3.TenantBypass {
		t.Errorf("[8] tenant:owner should trigger TenantBypass")
	}
	if !r3.Has(actionkeys.TenantRecordsDelete) {
		t.Errorf("[8] TenantBypass should pass tenant.records.delete")
	}
	if r3.Has(actionkeys.AdminsList) {
		t.Errorf("[8] TenantBypass should NOT pass site action admins.list")
	}
	t.Logf("[8] TenantBypass scoped correctly (tenant.* only)")

	// === [9] Unassign clears the role ===
	if err := store.Unassign(ctx, userColl, userID, siteUser.ID, nil); err != nil {
		t.Fatal(err)
	}
	r4, _ := store.Resolve(ctx, userColl, userID, &tenantID)
	if r4.Has(actionkeys.AuthMe) {
		t.Errorf("[9] after unassign, AuthMe should be gone")
	}
	// Tenant role still held.
	if !r4.Has(actionkeys.TenantRecordsList) {
		t.Errorf("[9] tenant assignment should survive site unassign")
	}
	t.Logf("[9] unassign clears site role but preserves tenant role")

	t.Log("RBAC E2E: 9/9 checks passed")
}
