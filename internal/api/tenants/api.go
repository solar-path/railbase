// Package tenants mounts the user-facing tenants/workspaces CRUD
// surface introduced in v0.4.3 alongside the _tenants + _tenant_members
// tables (migration 0032).
//
// This package is distinct from `internal/tenant` — that one is the
// REQUEST-SCOPED middleware (X-Tenant header → session var for RLS).
// Here we serve the workspace-management endpoints:
//
//	GET    /api/tenants            — list tenants the caller is a member of
//	POST   /api/tenants            — create new tenant + bind caller as owner
//	GET    /api/tenants/{id}       — fetch one (membership required)
//	PATCH  /api/tenants/{id}       — partial update (owner/admin only)
//	DELETE /api/tenants/{id}       — soft delete (owner only)
//	GET    /api/tenants/{id}/me    — caller's role on the tenant
//
// All routes are gated by the same auth middleware as /api/auth/* —
// PrincipalFrom returning anonymous yields 401. Authorisation past
// authentication is per-handler: list/create are open to any signed-in
// user; per-tenant routes call MyRole to refuse non-members with 404
// (same posture as account_sessions — never leak existence).
package tenants

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/tenant"
)

// Deps is the runtime wiring. The Pool is kept around for symmetry
// with the rest of the API packages even though Store already holds
// its own handle — future endpoints (e.g. cross-tenant search) may
// need ad-hoc queries.
//
// Audit is nil-tolerant: when unset, the per-tenant logs handler 503s
// with a clear message. Production wiring in pkg/railbase/app.go
// always sets it.
type Deps struct {
	Pool    *pgxpool.Pool
	Tenants *tenant.Store
	Audit   *audit.Writer
	Log     *slog.Logger

	// rbac is the optional RBAC store for the Sprint 4 surface
	// (per-tenant role assign/list). Wired via SetRBAC() rather than
	// the struct literal so existing Sprint-1/2 callers don't need to
	// rewire — the field stays nil for tests that don't exercise the
	// /roles routes, and those routes 503 when nil.
	rbac *rbac.Store
}

// Mount wires every route under /api/tenants. Caller is responsible
// for installing the auth middleware upstream.
func Mount(r chi.Router, d *Deps) {
	r.Get("/api/tenants", d.list)
	r.Post("/api/tenants", d.create)
	r.Get("/api/tenants/{id}", d.get)
	r.Patch("/api/tenants/{id}", d.update)
	r.Delete("/api/tenants/{id}", d.delete)
	r.Get("/api/tenants/{id}/me", d.myRole)
	// Sprint 2 — per-tenant user-management (list/invite/role/remove/accept).
	d.mountMembers(r)
	// Sprint 3 — per-tenant audit-log slice.
	r.Get("/api/tenants/{id}/logs", d.listLogs)
	// Sprint 4 — per-tenant RBAC (list/assign/unassign roles).
	d.mountRBAC(r)
}

// tenantDTO is the on-the-wire shape. UUIDs come out as strings (PB
// convention + JSON-safety), timestamps in ISO 8601 with millisecond
// precision so JS Date.parse() round-trips cleanly. `deleted_at` is
// always omitted on live rows (this surface never returns deleted
// rows anyway — Get + List filter at the SQL layer).
type tenantDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toDTO(t *tenant.Tenant) tenantDTO {
	return tenantDTO{
		ID:        t.ID.String(),
		Name:      t.Name,
		Slug:      t.Slug,
		CreatedAt: t.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt: t.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

// --- handlers -----------------------------------------------------------

func (d *Deps) list(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	rows, err := d.Tenants.ListMine(r.Context(), p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list tenants failed"))
		return
	}
	out := make([]tenantDTO, 0, len(rows))
	for _, t := range rows {
		out = append(out, toDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
}

// createBody is what POST /api/tenants accepts. Slug is optional —
// when blank we derive it from name (lowercase, hyphenate spaces,
// strip junk). The server validates the final form so a client that
// passes garbage gets a 400 regardless of which field carried it.
type createBody struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (d *Deps) create(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	var body createBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name is required"))
		return
	}
	if len(name) > 120 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name max length is 120"))
		return
	}
	slug := strings.TrimSpace(body.Slug)
	if slug == "" {
		slug = deriveSlug(name)
	} else {
		slug = strings.ToLower(slug)
	}
	if !slugRE.MatchString(slug) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"slug must match ^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$"))
		return
	}
	t, err := d.Tenants.Create(r.Context(), name, slug, p.CollectionName, p.UserID)
	if err != nil {
		switch err {
		case tenant.ErrSlugTaken:
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "slug already in use").
				WithDetail("field", "slug"))
		default:
			d.Log.Error("tenants: create failed", "err", err, "name", name, "slug", slug)
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create tenant failed"))
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"tenant": toDTO(t)})
}

func (d *Deps) get(w http.ResponseWriter, r *http.Request) {
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
	// Membership gate FIRST. MyRole returns ErrNotFound when the
	// caller is not an accepted member — that's our 404. Looking up
	// the tenant before the membership check would leak existence
	// (the same reason ErrNotFound collapses "deleted" + "missing").
	if _, err := d.Tenants.MyRole(r.Context(), id, p.CollectionName, p.UserID); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	t, err := d.Tenants.Get(r.Context(), id)
	if err != nil {
		if err == tenant.ErrNotFound {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "get tenant failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": toDTO(t)})
}

// updateBody is the partial-update wire shape. Pointers distinguish
// "field omitted" (leave it) from "field present and empty" (always
// rejected — empty name/slug is never valid).
type updateBody struct {
	Name *string `json:"name"`
	Slug *string `json:"slug"`
}

func (d *Deps) update(w http.ResponseWriter, r *http.Request) {
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
	role, err := d.Tenants.MyRole(r.Context(), id, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	// Only owner / admin may mutate workspace identity. Members can
	// see + use the tenant but can't rename it.
	if !role.IsOwner {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden, "only owners and admins may update the tenant"))
		return
	}
	var body updateBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	in := tenant.UpdateInput{}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name cannot be empty"))
			return
		}
		if len(n) > 120 {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name max length is 120"))
			return
		}
		in.Name = &n
	}
	if body.Slug != nil {
		s := strings.ToLower(strings.TrimSpace(*body.Slug))
		if !slugRE.MatchString(s) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"slug must match ^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$"))
			return
		}
		in.Slug = &s
	}
	// Empty PATCH ({} or all-null fields) is a malformed request — a
	// client that wanted a no-op shouldn't be PATCHing. Surface a 400
	// so the no-op intent is explicit rather than a silent 200.
	if in.Name == nil && in.Slug == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"patch must include at least one updatable field (name, slug)"))
		return
	}
	t, err := d.Tenants.Update(r.Context(), id, in)
	if err != nil {
		switch err {
		case tenant.ErrSlugTaken:
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "slug already in use").
				WithDetail("field", "slug"))
		case tenant.ErrNotFound:
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		default:
			d.Log.Error("tenants: update failed", "err", err, "id", id)
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update tenant failed"))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": toDTO(t)})
}

func (d *Deps) delete(w http.ResponseWriter, r *http.Request) {
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
	role, err := d.Tenants.MyRole(r.Context(), id, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	// Deleting a workspace is owner-only — admin can administer but
	// not destroy. Mirrors rail's authorisation matrix.
	if role.Role != tenant.RoleOwner {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden, "only the owner may delete the tenant"))
		return
	}
	if err := d.Tenants.Delete(r.Context(), id); err != nil {
		if err == tenant.ErrNotFound {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
			return
		}
		d.Log.Error("tenants: delete failed", "err", err, "id", id)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete tenant failed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// myRole answers "what's my role on this tenant?" — useful for UIs
// that want to gate per-row actions (rename, invite) without
// duplicating the membership join into every screen.
func (d *Deps) myRole(w http.ResponseWriter, r *http.Request) {
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
	role, err := d.Tenants.MyRole(r.Context(), id, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": role.TenantID.String(),
		"role":      role.Role,
		"is_owner":  role.IsOwner,
	})
}

// --- helpers ------------------------------------------------------------

// slugRE mirrors the table CHECK constraint exactly. Kept here as the
// authoritative validator for handler input so a 400 fires before the
// SQL roundtrip — the DB-side CHECK is the safety net.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// deriveSlug turns a human name into a valid slug. Mirrors the
// air/rail behaviour: lowercase, replace runs of non-alphanum with a
// single hyphen, trim leading/trailing hyphens. Falls back to "w" +
// short suffix when the input contains no usable chars (e.g. an
// all-emoji name) — the caller still gets a 400 from slugRE if even
// that synthetic form doesn't validate, prompting them to supply a
// real slug.
func deriveSlug(name string) string {
	var b strings.Builder
	prevHyphen := true
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) < 3 {
		// Pad so the regex (min length 3) accepts it. Operator can
		// supply a custom slug if the auto-form is unfortunate.
		s = (s + "-workspace")
		s = strings.Trim(s, "-")
	}
	if len(s) > 64 {
		s = s[:64]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// decodeJSON / writeJSON are local mirrors of the auth-package helpers
// so this package stays free of an internal/api/auth import (would
// cycle through Deps → app.go wiring).
func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if len(body) == 0 {
		return errEmptyBody
	}
	return json.Unmarshal(body, dst)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// errEmptyBody is the one named error we surface from decodeJSON so
// the handler can pattern-match if it ever needs to (it doesn't, for
// now; %s on err is enough).
var errEmptyBody = &emptyBodyErr{}

type emptyBodyErr struct{}

func (e *emptyBodyErr) Error() string { return "empty request body" }
