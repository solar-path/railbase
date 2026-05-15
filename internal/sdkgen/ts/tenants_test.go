// Fast TS-gen output assertions for the v0.4.3 tenants.ts module.
// No Postgres — pure string assertions catching the high-cost
// regressions: missing field, missing method, missing wire in
// index.ts.
package ts

import (
	"strings"
	"testing"
)

func TestEmitTenants_Surface(t *testing.T) {
	out := EmitTenants()

	// Tenant interface — every field the UI relies on must be
	// declared. ID/slug as strings (UUIDs aren't a JS primitive),
	// timestamps as ISO strings.
	for _, want := range []string{
		"export interface Tenant {",
		"id: string;",
		"name: string;",
		"slug: string;",
		"created_at: string;",
		"updated_at: string;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants.ts missing %q\n---\n%s", want, out)
		}
	}

	// TenantRole — the result of myRole().
	for _, want := range []string{
		"export interface TenantRole {",
		"tenant_id: string;",
		"role: string;",
		"is_owner: boolean;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants.ts missing %q (TenantRole)\n---\n%s", want, out)
		}
	}

	// Six methods, each with its HTTP wire.
	for _, want := range []string{
		"export function tenantsClient(http: HTTPClient)",

		"async list(): Promise<Tenant[]>",
		`http.request<{ tenants: Tenant[] }>("GET", "/api/tenants")`,

		"async create(input: { name: string; slug?: string }): Promise<Tenant>",
		`"POST", "/api/tenants"`,

		"async get(id: string): Promise<Tenant>",
		`"GET", "/api/tenants/" + encodeURIComponent(id)`,

		"async update(id: string, input: { name?: string; slug?: string }): Promise<Tenant>",
		`"PATCH",`,

		"delete(id: string): Promise<void>",
		`"DELETE", "/api/tenants/" + encodeURIComponent(id)`,

		"async myRole(id: string): Promise<TenantRole>",
		`"/api/tenants/" + encodeURIComponent(id) + "/me"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants.ts missing %q\n---\n%s", want, out)
		}
	}
}

// Sprint 2 — members + invites surface.
func TestEmitTenants_MembersSurface(t *testing.T) {
	out := EmitTenants()

	// Member interface — pending-invite rows carry placeholder user_id
	// (must be doc-flagged) and an is_pending boolean.
	for _, want := range []string{
		"export interface Member {",
		"tenant_id: string;",
		"collection_name: string;",
		"user_id: string;",
		"role: TenantMemberRole;",
		"invited_email?: string;",
		"invited_at?: string;",
		"accepted_at?: string;",
		"is_pending: boolean;",
		`export type TenantMemberRole = "owner" | "admin" | "member" | string;`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants.ts missing %q (members types)\n---\n%s", want, out)
		}
	}

	for _, want := range []string{
		"async listMembers(tenantID: string): Promise<Member[]>",
		`"GET", "/api/tenants/" + encodeURIComponent(tenantID) + "/members"`,

		"async invite(tenantID: string, input: { email: string; role?: TenantMemberRole }): Promise<Member>",
		`"POST", "/api/tenants/" + encodeURIComponent(tenantID) + "/members"`,

		"updateMemberRole(tenantID: string, userID: string, role: TenantMemberRole): Promise<void>",
		`"PATCH"`,
		`"/api/tenants/" + encodeURIComponent(tenantID) + "/members/" + encodeURIComponent(userID)`,

		"removeMember(tenantID: string, userID: string): Promise<void>",
		`"DELETE"`,

		"async acceptInvite(input?: { tenant_id?: string }): Promise<Member>",
		`"POST", "/api/tenants/invites/accept"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants.ts missing %q (members methods)\n---\n%s", want, out)
		}
	}
}

// Sprint 3 — per-tenant audit-log slice.
func TestEmitTenants_LogsSurface(t *testing.T) {
	out := EmitTenants()
	for _, want := range []string{
		"export interface TenantLogsQuery {",
		"event?: string;",
		"outcome?: string;",
		"user_id?: string;",
		"since?: string;",
		"until?: string;",
		"error_code?: string;",

		"export interface TenantLogEntry {",
		"seq: number;",
		"at: string;",
		"event: string;",
		"outcome: string;",

		"export interface TenantLogsPage {",
		"totalItems: number;",
		"items: TenantLogEntry[];",

		"async listLogs(tenantID: string, opts?: TenantLogsQuery): Promise<TenantLogsPage>",
		`"/api/tenants/" + encodeURIComponent(tenantID) + "/logs"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants.ts missing %q (logs surface)\n---\n%s", want, out)
		}
	}
}

// Sprint 4 — per-tenant RBAC surface.
func TestEmitTenants_RBACSurface(t *testing.T) {
	out := EmitTenants()
	for _, want := range []string{
		"export interface TenantRBACRole {",
		"is_system: boolean;",

		"async listRoles(tenantID: string): Promise<TenantRBACRole[]>",
		`"GET", "/api/tenants/" + encodeURIComponent(tenantID) + "/roles"`,

		"async listMemberRoles(tenantID: string, userID: string): Promise<TenantRBACRole[]>",
		`"/members/" + encodeURIComponent(userID) + "/roles"`,

		"assignRole(",
		"role: { role_id: string } | { role_name: string },",

		"unassignRole(tenantID: string, userID: string, roleID: string): Promise<void>",
		`"/roles/" + encodeURIComponent(roleID)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants.ts missing %q (RBAC surface)\n---\n%s", want, out)
		}
	}
}

// TestEmitIndex_WiresTenantsClient — without the index wire, callers
// can't reach rb.tenants.list() even though the module exists.
func TestEmitIndex_WiresTenantsClient(t *testing.T) {
	out := EmitIndex(nil)
	for _, want := range []string{
		`import { tenantsClient } from "./tenants.js";`,
		"tenants: tenantsClient(http),",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("index.ts missing %q (tenants wiring)\n---\n%s", want, out)
		}
	}
}
