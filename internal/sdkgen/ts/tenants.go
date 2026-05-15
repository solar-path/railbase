package ts

// EmitTenants renders tenants.ts: typed wrappers for the v0.4.3
// /api/tenants/* surface (workspaces CRUD + per-tenant membership
// role lookup).
//
// Schema-independent — these are global routes, not per-collection,
// so the emitter (like account.ts / stripe.ts / notifications.ts)
// takes no specs. The auth middleware on the server reads the
// caller's collection off the principal.
//
// Surface:
//
//	rb.tenants.list()                  GET    /api/tenants
//	rb.tenants.create({name, slug?})   POST   /api/tenants
//	rb.tenants.get(id)                 GET    /api/tenants/{id}
//	rb.tenants.update(id, patch)       PATCH  /api/tenants/{id}
//	rb.tenants.delete(id)              DELETE /api/tenants/{id}
//	rb.tenants.myRole(id)              GET    /api/tenants/{id}/me
//
// Why a dedicated module rather than a collection wrapper:
// `_tenants` isn't a user collection — it's a sys table with bespoke
// authorisation (only owners may rename / delete) and a non-CRUD
// helper (myRole). Tying it to the generic CollectionSpec emitter
// would have meant special-casing it everywhere; a flat module is
// cleaner.
func EmitTenants() string {
	return header + `// tenants.ts — typed wrappers for /api/tenants/* (workspaces CRUD).
//
// Use rb.setTenant(id) AFTER selecting a tenant to scope subsequent
// collection reads / writes — that header is what the per-request
// tenant middleware reads to set the RLS session var. This module
// only manages the workspace metadata + membership.

import type { HTTPClient } from "./index.js";

/** One row from GET /api/tenants. */
export interface Tenant {
  id: string;
  name: string;
  /** URL-safe identifier matching ^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$.
   *  Live slugs are unique; reusable after a soft-delete. */
  slug: string;
  /** ISO-8601 UTC, e.g. "2026-05-15T09:30:00.000Z". */
  created_at: string;
  updated_at: string;
}

/** Caller's role on a specific tenant, returned by myRole(). */
export interface TenantRole {
  tenant_id: string;
  /** "owner" | "admin" | "member" | "custom:<roleID>" */
  role: string;
  /** True iff role is "owner" or "admin" — collapses the two
   *  privileged tiers so UIs don't have to remember which is which. */
  is_owner: boolean;
}

/** Wrappers for the /api/tenants/* surface. All calls require an
 *  authenticated bearer token; the server returns 401 otherwise.
 *
 *      const rb = createRailbaseClient({ baseURL, token });
 *      const list = await rb.tenants.list();
 *      const t = await rb.tenants.create({ name: "Acme" });
 *      rb.setTenant(t.id);            // scope subsequent CRUD
 *      const role = await rb.tenants.myRole(t.id);
 *      if (role.is_owner) {
 *        await rb.tenants.update(t.id, { name: "Acme Inc" });
 *      }
 */
export function tenantsClient(http: HTTPClient) {
  return {
    /** GET /api/tenants — every live tenant the caller has an
     *  ACCEPTED membership in. Pending invites surface on a separate
     *  Sprint 2 endpoint. */
    async list(): Promise<Tenant[]> {
      const r = await http.request<{ tenants: Tenant[] }>("GET", "/api/tenants");
      return r.tenants;
    },

    /** POST /api/tenants — create a tenant and bind the caller as
     *  the OWNER in one transaction. Slug is optional; when omitted
     *  the server derives it from name (lowercase, hyphenate
     *  non-alphanum). Throws RailbaseAPIError(409, "conflict") if
     *  the slug is taken. */
    async create(input: { name: string; slug?: string }): Promise<Tenant> {
      const r = await http.request<{ tenant: Tenant }>("POST", "/api/tenants", { body: input });
      return r.tenant;
    },

    /** GET /api/tenants/{id}. Returns 404 (NOT 403) when the caller
     *  is not a member — by design, leaking "exists but you can't
     *  see it" would let an attacker probe for tenant ids. */
    async get(id: string): Promise<Tenant> {
      const r = await http.request<{ tenant: Tenant }>("GET", "/api/tenants/" + encodeURIComponent(id));
      return r.tenant;
    },

    /** PATCH /api/tenants/{id} — partial update of name and/or slug.
     *  Owner / admin only; member callers get 403. Empty body (no
     *  fields supplied) is rejected as 400. */
    async update(id: string, input: { name?: string; slug?: string }): Promise<Tenant> {
      const r = await http.request<{ tenant: Tenant }>(
        "PATCH",
        "/api/tenants/" + encodeURIComponent(id),
        { body: input },
      );
      return r.tenant;
    },

    /** DELETE /api/tenants/{id} — soft-delete (sets deleted_at).
     *  Owner-only; admin callers get 403. The slug becomes reusable
     *  after delete (the unique index is partial on live rows). */
    delete(id: string): Promise<void> {
      return http.request("DELETE", "/api/tenants/" + encodeURIComponent(id));
    },

    /** GET /api/tenants/{id}/me — caller's role on this tenant.
     *  Useful for gating per-tenant UI affordances without
     *  duplicating the membership join into every screen. */
    async myRole(id: string): Promise<TenantRole> {
      return http.request<TenantRole>("GET", "/api/tenants/" + encodeURIComponent(id) + "/me");
    },

    // ---- Members + invites (Sprint 2) ----

    /** GET /api/tenants/{id}/members — every member of the tenant
     *  (accepted + pending). Visible to any member; non-members
     *  receive 404 from the membership gate.
     *
     *  is_pending=true means the row is an open invite — invited_email
     *  is set, user_id is a placeholder until accept. */
    async listMembers(tenantID: string): Promise<Member[]> {
      const r = await http.request<{ members: Member[] }>(
        "GET", "/api/tenants/" + encodeURIComponent(tenantID) + "/members");
      return r.members;
    },

    /** POST /api/tenants/{id}/members — invite by email. When the
     *  email matches a known user on the caller's auth collection,
     *  the row is added as ACCEPTED (direct add). Otherwise a
     *  pending invite is created and the user accepts on next signin
     *  via acceptInvite().
     *
     *  Owner / admin only. Owners can grant any of owner/admin/member;
     *  admins can grant admin/member (no creating peers above them). */
    async invite(tenantID: string, input: { email: string; role?: TenantMemberRole }): Promise<Member> {
      const r = await http.request<{ member: Member }>(
        "POST", "/api/tenants/" + encodeURIComponent(tenantID) + "/members",
        { body: input });
      return r.member;
    },

    /** PATCH /api/tenants/{id}/members/{userID} — change a member's
     *  role. Server refuses to demote the last owner (409 with
     *  details.reason="last_owner") — promote another member first. */
    updateMemberRole(tenantID: string, userID: string, role: TenantMemberRole): Promise<void> {
      return http.request(
        "PATCH",
        "/api/tenants/" + encodeURIComponent(tenantID) + "/members/" + encodeURIComponent(userID),
        { body: { role } });
    },

    /** DELETE /api/tenants/{id}/members/{userID} — remove a member.
     *  Self-leave is allowed for any role; removing OTHERS requires
     *  owner/admin. Server refuses to remove the last owner (409). */
    removeMember(tenantID: string, userID: string): Promise<void> {
      return http.request(
        "DELETE",
        "/api/tenants/" + encodeURIComponent(tenantID) + "/members/" + encodeURIComponent(userID));
    },

    /** POST /api/tenants/invites/accept — claim a pending invite
     *  whose invited_email matches the caller's authenticated
     *  account. Pass tenant_id to accept a specific invite; omit it
     *  to accept the most recent pending invite for the caller's
     *  email across any tenant (typical post-signup landing UX). */
    async acceptInvite(input?: { tenant_id?: string }): Promise<Member> {
      const r = await http.request<{ member: Member }>(
        "POST", "/api/tenants/invites/accept", { body: input ?? {} });
      return r.member;
    },

    // ---- Audit log slice (Sprint 3) ----

    /** GET /api/tenants/{id}/logs — paginated, tenant-scoped slice of
     *  the audit log. ANY member of the tenant may read. The site-wide
     *  log browser lives on the admin surface; this endpoint is the
     *  workspace-level peer that workspace settings UIs render.
     *
     *  Filters mirror the admin endpoint (event substring, outcome,
     *  user_id, since/until, error_code). perPage hard-caps at 200
     *  (admin gets 500 — tenant page sizes are tighter). */
    async listLogs(tenantID: string, opts?: TenantLogsQuery): Promise<TenantLogsPage> {
      const params = new URLSearchParams();
      if (opts?.page != null) params.set("page", String(opts.page));
      if (opts?.perPage != null) params.set("perPage", String(opts.perPage));
      if (opts?.event) params.set("event", opts.event);
      if (opts?.outcome) params.set("outcome", opts.outcome);
      if (opts?.user_id) params.set("user_id", opts.user_id);
      if (opts?.since) params.set("since", opts.since);
      if (opts?.until) params.set("until", opts.until);
      if (opts?.error_code) params.set("error_code", opts.error_code);
      const qs = params.toString();
      return http.request<TenantLogsPage>(
        "GET",
        "/api/tenants/" + encodeURIComponent(tenantID) + "/logs" + (qs ? "?" + qs : ""),
      );
    },

    // ---- Per-tenant RBAC (Sprint 4) ----

    /** GET /api/tenants/{id}/roles — every tenant-scoped RBAC role
     *  defined on the deployment. The result is the universe of
     *  roles a tenant owner may assign to members; ANY member may
     *  read so UIs can render role chips even for non-privileged
     *  callers. Role creation + action grants live on the admin
     *  surface (operators define the role catalogue once for the
     *  whole deployment). */
    async listRoles(tenantID: string): Promise<TenantRBACRole[]> {
      const r = await http.request<{ roles: TenantRBACRole[] }>(
        "GET", "/api/tenants/" + encodeURIComponent(tenantID) + "/roles");
      return r.roles;
    },

    /** GET /api/tenants/{id}/members/{userID}/roles — every
     *  tenant-scoped RBAC role currently assigned to the member on
     *  this tenant. Site-scoped roles (e.g. system_admin) are NOT
     *  returned — those are outside the tenant owner's concern. */
    async listMemberRoles(tenantID: string, userID: string): Promise<TenantRBACRole[]> {
      const r = await http.request<{ roles: TenantRBACRole[] }>(
        "GET",
        "/api/tenants/" + encodeURIComponent(tenantID) +
          "/members/" + encodeURIComponent(userID) + "/roles");
      return r.roles;
    },

    /** POST /api/tenants/{id}/members/{userID}/roles — grant a
     *  tenant-scoped role to a member. Owner / admin only. The
     *  body accepts either { role_id } or { role_name } (the latter
     *  resolved against scope='tenant'). Idempotent: re-granting an
     *  existing assignment is a no-op. */
    assignRole(
      tenantID: string,
      userID: string,
      role: { role_id: string } | { role_name: string },
    ): Promise<void> {
      return http.request(
        "POST",
        "/api/tenants/" + encodeURIComponent(tenantID) +
          "/members/" + encodeURIComponent(userID) + "/roles",
        { body: role });
    },

    /** DELETE /api/tenants/{id}/members/{userID}/roles/{roleID} —
     *  revoke a previously granted tenant role. Owner / admin only. */
    unassignRole(tenantID: string, userID: string, roleID: string): Promise<void> {
      return http.request(
        "DELETE",
        "/api/tenants/" + encodeURIComponent(tenantID) +
          "/members/" + encodeURIComponent(userID) +
          "/roles/" + encodeURIComponent(roleID));
    },
  };
}

/** Query parameters for tenants.listLogs(). All fields optional;
 *  omitted fields apply no filter. Date strings are RFC3339. */
export interface TenantLogsQuery {
  page?: number;
  perPage?: number;
  event?: string;
  outcome?: string;
  user_id?: string;
  /** RFC3339 lower bound on the row's "at" timestamp. */
  since?: string;
  /** RFC3339 upper bound on the row's "at" timestamp. */
  until?: string;
  error_code?: string;
}

/** One row from the per-tenant audit log. Nullable fields surface as
 *  null (not empty string) to match the on-the-wire shape. */
export interface TenantLogEntry {
  seq: number;
  id: string;
  /** ISO-8601 UTC string. */
  at: string;
  user_id: string | null;
  user_collection: string | null;
  tenant_id: string | null;
  event: string;
  outcome: string;
  error_code: string | null;
  ip: string | null;
  user_agent: string | null;
}

/** Paginated response from tenants.listLogs(). totalItems is the
 *  unpaginated row count matching the filter; items is the current
 *  page window. */
export interface TenantLogsPage {
  page: number;
  perPage: number;
  totalItems: number;
  items: TenantLogEntry[];
}

/** An RBAC role assignable to a tenant member. is_system marks the
 *  seed roles ("owner", "admin", "member") that operators cannot
 *  delete; UIs typically render them in a separate group. */
export interface TenantRBACRole {
  id: string;
  name: string;
  description?: string;
  is_system: boolean;
}

/** Built-in tenant roles. Operators can grow custom roles via the
 *  per-tenant RBAC API (Sprint 4); those land in the underlying
 *  string column as ` + "`" + `'custom:<id>'` + "`" + ` — typed callers cast that
 *  literal at the call site if they want strictness. */
export type TenantMemberRole = "owner" | "admin" | "member" | string;

/** One row from GET /api/tenants/{id}/members. Pending invites have
 *  is_pending=true; user_id on those rows is a deterministic
 *  placeholder UUID derived from (tenant, email) — DO NOT use it as
 *  a real user id until is_pending flips to false. */
export interface Member {
  tenant_id: string;
  collection_name: string;
  user_id: string;
  role: TenantMemberRole;
  /** Set for pending invites + (for display) on direct-adds. */
  invited_email?: string;
  /** ISO-8601 UTC, null when the row is a direct add (created
   *  already-accepted). */
  invited_at?: string;
  /** ISO-8601 UTC, null on pending invites. */
  accepted_at?: string;
  created_at: string;
  updated_at: string;
  /** Convenience flag: true iff accepted_at is null. */
  is_pending: boolean;
}
`
}
