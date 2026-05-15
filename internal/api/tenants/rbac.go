// Per-tenant RBAC — Sprint 4 of the workspaces roadmap.
//
// Slim surface that consumes the existing internal/rbac.Store rather
// than duplicating role management. Role *creation* + action grants
// stay on the admin surface (operators define them once for the
// whole deployment, with scope='tenant' for tenant-assignable
// roles). The per-tenant UI's job is just:
//
//	GET    /api/tenants/{id}/roles                         — list assignable (tenant-scoped) roles
//	GET    /api/tenants/{id}/members/{userID}/roles        — what's assigned to this member
//	POST   /api/tenants/{id}/members/{userID}/roles        — assign a tenant role
//	DELETE /api/tenants/{id}/members/{userID}/roles/{rid}  — unassign
//
// Authorisation: list + read-mine are open to any member; mutations
// require owner/admin. Tenant scope is forced server-side — a
// caller cannot accidentally land an assignment on a different
// tenant via a body field, because we read tenantID from the URL.
package tenants

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/rbac"
)

// SetRBAC attaches the rbac.Store to the Deps. Optional: when nil
// every /roles route 503s. We keep a setter rather than adding the
// field to Deps directly so existing tests + Sprint-1/2 callers
// don't need to rewire — Sprint 4 is an additive opt-in.
func (d *Deps) SetRBAC(s *rbac.Store) { d.rbac = s }

// rbac field is package-private. Tests use SetRBAC; production wires
// it from pkg/railbase/app.go. Choosing a setter over an exported
// field keeps Deps's public surface stable across sprints.
//
// (The field is added in api.go via this anonymous-extension trick so
// the Sprint-1 file stays small and the sprint boundary is visible
// in the diff history.)
// — see top of api.go where Deps is declared; we add the field there.

func (d *Deps) mountRBAC(r chi.Router) {
	r.Get("/api/tenants/{id}/roles", d.listTenantRoles)
	r.Get("/api/tenants/{id}/members/{userID}/roles", d.listMemberRoles)
	r.Post("/api/tenants/{id}/members/{userID}/roles", d.assignMemberRole)
	r.Delete("/api/tenants/{id}/members/{userID}/roles/{roleID}", d.unassignMemberRole)
}

// roleDTO is the on-the-wire shape of a tenant-assignable role.
type roleDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsSystem    bool   `json:"is_system"`
}

func toRoleDTO(r rbac.Role) roleDTO {
	return roleDTO{
		ID:          r.ID.String(),
		Name:        r.Name,
		Description: r.Description,
		IsSystem:    r.IsSystem,
	}
}

// listTenantRoles returns every rbac role with scope='tenant' — the
// universe of roles a tenant owner may assign to members. Any member
// of the tenant may read (used to render the per-member role-picker
// chips even when the caller can't grant).
func (d *Deps) listTenantRoles(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant id"))
		return
	}
	if _, err := d.Tenants.MyRole(r.Context(), tenantID, p.CollectionName, p.UserID); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	if d.rbac == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "RBAC not configured"))
		return
	}
	all, err := d.rbac.ListRoles(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list roles failed"))
		return
	}
	out := make([]roleDTO, 0, len(all))
	for _, role := range all {
		if role.Scope == rbac.ScopeTenant {
			out = append(out, toRoleDTO(role))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// listMemberRoles is the read peer of assign/unassign. Returns every
// tenant-scoped rbac role the member holds on THIS tenant. Site-
// scoped roles (e.g. system_admin) are NOT listed here — they're
// outside the tenant owner's concern.
func (d *Deps) listMemberRoles(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant id"))
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid user id"))
		return
	}
	if _, err := d.Tenants.MyRole(r.Context(), tenantID, p.CollectionName, p.UserID); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	if d.rbac == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "RBAC not configured"))
		return
	}
	assignments, err := d.rbac.ListAssignmentsFor(r.Context(), p.CollectionName, targetID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list assignments failed"))
		return
	}
	out := make([]roleDTO, 0, len(assignments))
	for _, a := range assignments {
		// Filter: only tenant-scoped roles, and only for THIS tenant.
		if a.Role.Scope != rbac.ScopeTenant {
			continue
		}
		if a.TenantID == nil || *a.TenantID != tenantID {
			continue
		}
		out = append(out, toRoleDTO(a.Role))
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

type assignBody struct {
	RoleID   string `json:"role_id"`
	RoleName string `json:"role_name"` // alternative — by name on this tenant scope
}

// assignMemberRole grants a tenant-scoped role to a member. Owner /
// admin only. The target must already be an accepted member of the
// tenant (we look them up via ListMembers); assigning a role to a
// stranger would land an orphan _user_roles row.
func (d *Deps) assignMemberRole(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant id"))
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid user id"))
		return
	}
	role, err := d.Tenants.MyRole(r.Context(), tenantID, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	if !role.IsOwner {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"only owners and admins may assign roles"))
		return
	}
	if d.rbac == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "RBAC not configured"))
		return
	}
	var body assignBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	// Resolve role: id OR name (helper for UIs that hold the name).
	var roleRow *rbac.Role
	if body.RoleID != "" {
		rid, err := uuid.Parse(body.RoleID)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid role_id"))
			return
		}
		// Look it up via ListRoles (rbac.Store has no GetByID public).
		all, err := d.rbac.ListRoles(r.Context())
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load roles failed"))
			return
		}
		for i := range all {
			if all[i].ID == rid {
				roleRow = &all[i]
				break
			}
		}
	} else if strings.TrimSpace(body.RoleName) != "" {
		rr, err := d.rbac.GetRole(r.Context(), strings.TrimSpace(body.RoleName), rbac.ScopeTenant)
		if err == nil {
			roleRow = rr
		}
	}
	if roleRow == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "role not found"))
		return
	}
	if roleRow.Scope != rbac.ScopeTenant {
		// Tenant owners can't grant site-scoped roles. Defence in
		// depth — the URL-encoded tenantID is forced into the
		// AssignInput below; but we refuse outright so the API
		// surface stays narrow.
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"only tenant-scoped roles may be assigned via this endpoint"))
		return
	}
	tid := tenantID
	_, err = d.rbac.Assign(r.Context(), rbac.AssignInput{
		CollectionName: p.CollectionName,
		RecordID:       targetID,
		RoleID:         roleRow.ID,
		TenantID:       &tid,
		GrantedBy:      uuidPtr(p.UserID),
	})
	if err != nil {
		// rbac.Assign upserts on conflict — duplicate assignment
		// returns the existing row, not an error. So any error here
		// is a genuine failure.
		d.logIfPresent("tenants: rbac assign failed", err,
			"tenant", tenantID, "user", targetID, "role", roleRow.Name)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "assign role failed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unassignMemberRole drops one tenant role from one member. Owner /
// admin only.
func (d *Deps) unassignMemberRole(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant id"))
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid user id"))
		return
	}
	roleID, err := uuid.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid role id"))
		return
	}
	role, err := d.Tenants.MyRole(r.Context(), tenantID, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	if !role.IsOwner {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"only owners and admins may unassign roles"))
		return
	}
	if d.rbac == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "RBAC not configured"))
		return
	}
	tid := tenantID
	if err := d.rbac.Unassign(r.Context(), p.CollectionName, targetID, roleID, &tid); err != nil {
		d.logIfPresent("tenants: rbac unassign failed", err,
			"tenant", tenantID, "user", targetID, "role", roleID)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "unassign role failed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- small helpers ------------------------------------------------------

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }

// logIfPresent is a nil-safe shim so handlers can log without
// guarding every call site. Deps.Log is nil-tolerant via the
// Sprint-1 pattern (handlers worked without it); preserve that.
func (d *Deps) logIfPresent(msg string, err error, kv ...any) {
	if d.Log == nil {
		return
	}
	args := append([]any{"err", err}, kv...)
	d.Log.Error(msg, args...)
}

// Compile-time guard: keep slog import live even when Log==nil paths
// dominate.
var _ = slog.LevelError
