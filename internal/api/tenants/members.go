// Per-tenant user-management — Sprint 2 of the workspaces roadmap.
// Backs the "Users" tab on the air/rail-style tenant settings page:
// see who's a member, invite by email, change roles, remove people.
//
// Routes (all gated by tenant membership; mutations require owner/admin):
//
//	GET    /api/tenants/{id}/members           — list (accepted + pending)
//	POST   /api/tenants/{id}/members           — invite by email
//	PATCH  /api/tenants/{id}/members/{userID}  — change role
//	DELETE /api/tenants/{id}/members/{userID}  — remove
//	POST   /api/tenants/invites/accept         — caller accepts a pending invite
package tenants

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/tenant"
)

// MountMembers installs the per-tenant membership routes. Called by
// Mount() in api.go so wiring is one Mount() call from app.go.
func (d *Deps) mountMembers(r chi.Router) {
	r.Get("/api/tenants/{id}/members", d.listMembers)
	r.Post("/api/tenants/{id}/members", d.inviteMember)
	r.Patch("/api/tenants/{id}/members/{userID}", d.updateMemberRole)
	r.Delete("/api/tenants/{id}/members/{userID}", d.removeMember)
	r.Post("/api/tenants/invites/accept", d.acceptInvite)
}

// memberDTO is the on-the-wire shape. user_id is the placeholder
// UUID for pending invites — callers should key UIs off (email,
// accepted_at) rather than user_id until accept.
type memberDTO struct {
	TenantID       string  `json:"tenant_id"`
	CollectionName string  `json:"collection_name"`
	UserID         string  `json:"user_id"`
	Role           string  `json:"role"`
	InvitedEmail   *string `json:"invited_email,omitempty"`
	InvitedAt      *string `json:"invited_at,omitempty"`
	AcceptedAt     *string `json:"accepted_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	// IsPending is true when accepted_at is NULL; convenience flag so
	// UIs can render a "Pending invite" badge without parsing nullable
	// fields.
	IsPending bool `json:"is_pending"`
}

func toMemberDTO(m *tenant.Member) memberDTO {
	out := memberDTO{
		TenantID:       m.TenantID.String(),
		CollectionName: m.CollectionName,
		UserID:         m.UserID.String(),
		Role:           m.Role,
		InvitedEmail:   m.InvitedEmail,
		CreatedAt:      m.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:      m.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		IsPending:      m.AcceptedAt == nil,
	}
	if m.InvitedAt != nil {
		s := m.InvitedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		out.InvitedAt = &s
	}
	if m.AcceptedAt != nil {
		s := m.AcceptedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		out.AcceptedAt = &s
	}
	return out
}

// listMembers — any member of the tenant may view the full roster
// (parity with rail). Stranger gets 404 from the membership gate.
func (d *Deps) listMembers(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant id"))
		return
	}
	if _, err := d.Tenants.MyRole(r.Context(), id, p.CollectionName, p.UserID); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	rows, err := d.Tenants.ListMembers(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list members failed"))
		return
	}
	out := make([]memberDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, toMemberDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

type inviteBody struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// inviteMember creates a pending invite row (or a direct-add when
// the email already maps to a known user — looked up via the auth
// collection table). Sending the email itself is the mailer's job
// and lives in pkg/railbase wiring; the row creation here is the
// system-of-record so the invite can be accepted even if the email
// was lost.
func (d *Deps) inviteMember(w http.ResponseWriter, r *http.Request) {
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
	role, err := d.Tenants.MyRole(r.Context(), tenantID, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	if !role.IsOwner {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden, "only owners and admins may invite members"))
		return
	}
	var body inviteBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || !strings.Contains(email, "@") {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "valid email is required"))
		return
	}
	newRole := strings.ToLower(strings.TrimSpace(body.Role))
	if newRole == "" {
		newRole = tenant.RoleMember
	}
	if !isAllowedAssignableRole(newRole, role) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"role %q is not assignable by your role", newRole))
		return
	}
	// Best-effort: if the email already matches a known user on the
	// SAME auth collection the caller belongs to, add them directly
	// rather than as a pending invite. This is the "you typed a
	// colleague's email and they're already in the system" path —
	// instant access, no email round-trip. Strict tenant-isolation
	// is preserved because we only look at the SAME auth collection
	// the caller already authenticated against.
	var userIDOpt *uuid.UUID
	if existing, ok := d.lookupUserByEmail(r, p.CollectionName, email); ok {
		userIDOpt = &existing
	}
	m, err := d.Tenants.Invite(r.Context(), tenant.InviteInput{
		TenantID:       tenantID,
		CollectionName: p.CollectionName,
		Email:          email,
		UserID:         userIDOpt,
		Role:           newRole,
	})
	if err != nil {
		switch err {
		case tenant.ErrMemberExists:
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "user is already a member of this tenant"))
		default:
			d.Log.Error("tenants: invite failed", "err", err, "tenant", tenantID, "email", email)
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "invite failed"))
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"member": toMemberDTO(m)})
}

type roleBody struct {
	Role string `json:"role"`
}

func (d *Deps) updateMemberRole(w http.ResponseWriter, r *http.Request) {
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
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden, "only owners and admins may change roles"))
		return
	}
	var body roleBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	newRole := strings.ToLower(strings.TrimSpace(body.Role))
	if !isAllowedAssignableRole(newRole, role) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"role %q is not assignable by your role", newRole))
		return
	}
	if err := d.Tenants.UpdateMemberRole(r.Context(), tenantID, p.CollectionName, targetID, newRole); err != nil {
		switch err {
		case tenant.ErrNotFound:
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "member not found"))
		case tenant.ErrLastOwner:
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
				"cannot demote the last owner; promote another member first").
				WithDetail("reason", "last_owner"))
		default:
			d.Log.Error("tenants: update role failed", "err", err, "tenant", tenantID, "user", targetID)
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update role failed"))
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) removeMember(w http.ResponseWriter, r *http.Request) {
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
	// Self-leave is permitted for everyone (member can drop their
	// own membership). Removing OTHERS requires owner/admin.
	if targetID != p.UserID && !role.IsOwner {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden, "only owners and admins may remove other members"))
		return
	}
	if err := d.Tenants.RemoveMember(r.Context(), tenantID, p.CollectionName, targetID); err != nil {
		switch err {
		case tenant.ErrNotFound:
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "member not found"))
		case tenant.ErrLastOwner:
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
				"cannot remove the last owner; promote another member first").
				WithDetail("reason", "last_owner"))
		default:
			d.Log.Error("tenants: remove member failed", "err", err, "tenant", tenantID, "user", targetID)
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "remove member failed"))
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type acceptBody struct {
	TenantID string `json:"tenant_id"` // optional; "" = accept any matching pending invite
}

// acceptInvite is the "I've signed in, claim my pending invite" path.
// The caller's email is taken from the authenticated principal's
// auth-collection row — the user can't claim invites addressed to
// other emails. Returns the resulting accepted member row.
func (d *Deps) acceptInvite(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	email, ok := d.lookupEmailByUserID(r, p.CollectionName, p.UserID)
	if !ok || email == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "could not resolve caller email"))
		return
	}
	var body acceptBody
	// Empty body is allowed (= accept any).
	_ = decodeJSON(r, &body)
	var tenantID uuid.UUID
	if body.TenantID != "" {
		if tid, err := uuid.Parse(body.TenantID); err == nil {
			tenantID = tid
		} else {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant_id"))
			return
		}
	}
	m, err := d.Tenants.AcceptInvite(r.Context(), tenantID, p.CollectionName, email, p.UserID)
	if err != nil {
		switch err {
		case tenant.ErrNotFound:
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "no pending invite for your email"))
		case tenant.ErrMemberExists:
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "already a member of this tenant"))
		default:
			d.Log.Error("tenants: accept invite failed", "err", err, "user", p.UserID, "email", email)
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "accept invite failed"))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": toMemberDTO(m)})
}

// --- helpers ------------------------------------------------------------

// isAllowedAssignableRole gates the role values the caller is
// allowed to grant. Owners can grant any of owner/admin/member;
// admins can grant admin/member (no creating peers above them).
// Custom roles (`custom:<id>`) are deferred to Sprint 4.
func isAllowedAssignableRole(role string, caller *tenant.MembershipRole) bool {
	switch role {
	case tenant.RoleOwner:
		return caller.Role == tenant.RoleOwner
	case tenant.RoleAdmin, tenant.RoleMember:
		return caller.IsOwner
	}
	return false
}

// lookupUserByEmail / lookupEmailByUserID hit the auth collection
// table directly. We avoid importing internal/api/auth (would
// cycle) — the SQL is straightforward and only two columns. The
// auth-collection table is named by the principal so we don't have
// to thread a registry handle in.
//
// Returns ok=false when the user isn't found (caller treats that as
// "create a pending invite" / "error").
func (d *Deps) lookupUserByEmail(r *http.Request, collection, email string) (uuid.UUID, bool) {
	// Defensive: collection name comes from the authenticated principal
	// (set by the auth middleware), not user input — but we still
	// validate it as a bare identifier to silence linters and protect
	// against future paths that might pass an attacker-controlled value.
	if !isSafeIdent(collection) {
		return uuid.Nil, false
	}
	var id uuid.UUID
	err := d.Pool.QueryRow(r.Context(),
		"SELECT id FROM "+collection+" WHERE lower(email) = lower($1) LIMIT 1", email).Scan(&id)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func (d *Deps) lookupEmailByUserID(r *http.Request, collection string, userID uuid.UUID) (string, bool) {
	if !isSafeIdent(collection) {
		return "", false
	}
	var email string
	err := d.Pool.QueryRow(r.Context(),
		"SELECT email FROM "+collection+" WHERE id = $1", userID).Scan(&email)
	if err != nil {
		return "", false
	}
	return email, true
}

// isSafeIdent matches the conservative pattern Railbase uses for
// collection names: lowercase a-z, digits, underscore. Out-of-paranoia
// since the value reaches us from the authenticated principal — but
// defense in depth.
func isSafeIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}
