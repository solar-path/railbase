package rbac

import (
	"testing"

	"github.com/railbase/railbase/internal/rbac/actionkeys"
)

// rbac.Resolved.Has / HasAny / bypass logic is pure-Go — easy to
// cover without a Postgres. Store paths exercise the DB end-to-end
// in the api/auth integration test.

func TestHas_SiteBypass(t *testing.T) {
	r := &Resolved{SiteBypass: true, Actions: map[actionkeys.ActionKey]struct{}{}}
	// Every action returns true, even ones we never granted.
	if !r.Has(actionkeys.AdminsList) {
		t.Errorf("system_admin should grant AdminsList")
	}
	if !r.Has(actionkeys.TenantRecordsDelete) {
		t.Errorf("system_admin should also grant tenant actions")
	}
	if !r.Has(actionkeys.ActionKey("custom.action.never.seen")) {
		t.Errorf("system_admin should pass custom actions too")
	}
}

func TestHas_TenantBypass_OnlyTenantActions(t *testing.T) {
	r := &Resolved{TenantBypass: true, Actions: map[actionkeys.ActionKey]struct{}{}}
	// Tenant bypass passes tenant.* actions...
	if !r.Has(actionkeys.TenantRecordsDelete) {
		t.Errorf("tenant:owner should grant TenantRecordsDelete")
	}
	if !r.Has(actionkeys.TenantMembersInvite) {
		t.Errorf("tenant:owner should grant TenantMembersInvite")
	}
	// ...but NOT site-scope actions.
	if r.Has(actionkeys.AdminsList) {
		t.Errorf("tenant:owner should NOT grant site action AdminsList")
	}
	if r.Has(actionkeys.AuditRead) {
		t.Errorf("tenant:owner should NOT grant site action AuditRead")
	}
}

func TestHas_ExplicitGrant(t *testing.T) {
	r := &Resolved{
		Actions: map[actionkeys.ActionKey]struct{}{
			actionkeys.AuthMe:       {},
			actionkeys.SettingsRead: {},
		},
	}
	if !r.Has(actionkeys.AuthMe) {
		t.Errorf("explicit grant should pass")
	}
	if r.Has(actionkeys.SettingsWrite) {
		t.Errorf("ungranted action should fail")
	}
}

func TestHasAny(t *testing.T) {
	r := &Resolved{
		Actions: map[actionkeys.ActionKey]struct{}{
			actionkeys.AuthMe: {},
		},
	}
	if !r.HasAny(actionkeys.SettingsWrite, actionkeys.AuthMe) {
		t.Errorf("HasAny should match on AuthMe")
	}
	if r.HasAny(actionkeys.SettingsWrite, actionkeys.AdminsList) {
		t.Errorf("HasAny should fail when none granted")
	}
	if r.HasAny() {
		t.Errorf("HasAny() with empty list should be false")
	}
}

func TestHas_NilResolved(t *testing.T) {
	var r *Resolved
	if r.Has(actionkeys.AuthMe) {
		t.Errorf("nil resolved should grant nothing")
	}
}

func TestIsTenantAction(t *testing.T) {
	cases := []struct {
		key  actionkeys.ActionKey
		want bool
	}{
		{actionkeys.TenantRecordsList, true},
		{actionkeys.TenantMembersInvite, true},
		{actionkeys.AdminsList, false},
		{actionkeys.AuthMe, false},
		{"tenant.custom.thing", true},
		{"tenants.list", false}, // "tenants." vs "tenant." — only the latter counts
		{"", false},
	}
	for _, c := range cases {
		if got := isTenantAction(c.key); got != c.want {
			t.Errorf("isTenantAction(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestDenied_Error(t *testing.T) {
	d := &Denied{Action: actionkeys.AdminsList}
	if d.Error() != "rbac: action denied: admins.list" {
		t.Errorf("unexpected error string: %q", d.Error())
	}
}
