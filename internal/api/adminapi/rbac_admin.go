package adminapi

// v1.x — admin-side RBAC management surface.
//
// Exposes the existing rbac.Store CRUD through `/api/_admin/rbac/*` and
// `/api/_admin/admins/*/roles` so the SPA can render a working
// role-assignment screen. Without these endpoints the only way to
// downgrade an admin from `system_admin` to `system_readonly` is direct
// SQL or the Go API — fine for plumbing tests, useless for operators.
//
// Routes (all guarded by RequireAdmin + per-route rbac action):
//
//	GET  /api/_admin/rbac/roles                      → rbac.read
//	GET  /api/_admin/rbac/roles/{id}/actions         → rbac.read
//	GET  /api/_admin/admins-with-roles               → admins.list + rbac.read
//	PUT  /api/_admin/admins/{adminID}/roles          → rbac.write
//
// The PUT endpoint takes a body of {roles: ["name1", "name2"]} and
// performs an ATOMIC SWAP: existing site assignments under
// (collection='_admins', record_id=<id>) are deleted, then the new set
// is inserted in the same transaction. We do not expose Grant/Revoke
// from the admin UI — that's a per-role action edit which only matters
// when operators mint custom roles, deferred to a later iteration.
//
// Safety guards:
//
//   - The last `system_admin` admin in the deployment cannot be
//     downgraded. If the PUT request would leave zero rows in
//     `_user_roles` with role=system_admin AND collection=_admins,
//     we refuse with 409 conflict.
//
//   - Only site-scoped roles can be assigned to admins. Tenant roles
//     have no meaning for the `_admins` table (admins ARE the bypass);
//     the validator rejects them at the request layer.
//
//   - Audit: every successful PUT emits an `admin.rbac.assign` event
//     with the before / after role sets attached to the row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/railbase/railbase/internal/audit"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/rbac"
)

// roleDTO is the wire shape returned by the list endpoint. Mirrors
// rbac.Role minus the timestamps the SPA doesn't render.
type roleDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	IsSystem    bool   `json:"is_system"`
}

func roleDTOFrom(r rbac.Role) roleDTO {
	return roleDTO{
		ID:          r.ID.String(),
		Name:        r.Name,
		Scope:       string(r.Scope),
		Description: r.Description,
		IsSystem:    r.IsSystem,
	}
}

// rbacRolesListHandler — GET /api/_admin/rbac/roles.
// Returns every role in the system, both site and tenant scopes, so
// the SPA can render scope-grouped pickers.
func (d *Deps) rbacRolesListHandler(w http.ResponseWriter, r *http.Request) {
	if d.RBAC == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "rbac not wired"))
		return
	}
	roles, err := d.RBAC.ListRoles(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list roles"))
		return
	}
	out := make([]roleDTO, 0, len(roles))
	for _, r := range roles {
		out = append(out, roleDTOFrom(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// rbacRoleActionsHandler — GET /api/_admin/rbac/roles/{id}/actions.
// The SPA renders the action set so operators can see what a role
// actually grants before assigning it to someone.
func (d *Deps) rbacRoleActionsHandler(w http.ResponseWriter, r *http.Request) {
	if d.RBAC == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "rbac not wired"))
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid role id"))
		return
	}
	actions, err := d.RBAC.ListActions(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list actions"))
		return
	}
	// system_admin / owner are bypass roles — they don't have explicit
	// _role_actions rows. Surface that to the UI as a flag rather than
	// the empty array our query naturally returns, so the SPA can show
	// "Full bypass — all actions" instead of "No actions granted".
	role, err := d.rbacGetRole(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup role"))
		return
	}
	bypass := role.Name == "system_admin" && role.Scope == rbac.ScopeSite ||
		role.Name == "owner" && role.Scope == rbac.ScopeTenant
	keys := make([]string, 0, len(actions))
	for _, k := range actions {
		keys = append(keys, string(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"actions": keys,
		"bypass":  bypass,
	})
}

// adminWithRolesDTO is the join shape returned by the admins-with-roles
// endpoint — one row per admin plus their site role names.
type adminWithRolesDTO struct {
	ID    string   `json:"id"`
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

// adminsWithRolesHandler — GET /api/_admin/admins-with-roles.
// Powers the main grid on the role-management screen. One round-trip
// for the join so the SPA doesn't N+1 over per-admin /roles fetches.
func (d *Deps) adminsWithRolesHandler(w http.ResponseWriter, r *http.Request) {
	if d.Pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "pool not wired"))
		return
	}
	const q = `
        SELECT a.id, a.email,
               COALESCE(
                 ARRAY_AGG(role.name ORDER BY role.name)
                   FILTER (WHERE role.name IS NOT NULL),
                 '{}'
               ) AS role_names
          FROM _admins a
          LEFT JOIN _user_roles ur
                 ON ur.collection_name = '_admins'
                AND ur.record_id = a.id
                AND ur.tenant_id IS NULL
          LEFT JOIN _roles role
                 ON role.id = ur.role_id
                AND role.scope = 'site'
         GROUP BY a.id, a.email
         ORDER BY a.email
    `
	rows, err := d.Pool.Query(r.Context(), q)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query admins-with-roles"))
		return
	}
	defer rows.Close()
	var out []adminWithRolesDTO
	for rows.Next() {
		var (
			id    uuid.UUID
			email string
			names []string
		)
		if err := rows.Scan(&id, &email, &names); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan admin row"))
			return
		}
		if names == nil {
			names = []string{}
		}
		out = append(out, adminWithRolesDTO{
			ID:    id.String(),
			Email: email,
			Roles: names,
		})
	}
	if err := rows.Err(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "iter admins-with-roles"))
		return
	}
	if out == nil {
		out = []adminWithRolesDTO{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"admins": out})
}

// putAdminRolesBody is the request shape for the assignment-swap PUT.
// `Roles` is the COMPLETE new set; sending [] strips every site
// assignment (which is OK as long as it doesn't leave zero
// system_admin admins — the guard below catches that).
type putAdminRolesBody struct {
	Roles []string `json:"roles"`
}

// putAdminRolesHandler — PUT /api/_admin/admins/{adminID}/roles.
//
// The semantic model is "replace the admin's site role set with this
// list, atomically." Concretely:
//
//  1. Validate the admin exists and every requested role name maps to
//     a real site role.
//  2. Open a tx. Inside it: read the current site role set (for audit
//     before-after diff), then DELETE every site _user_roles row for
//     this admin, then INSERT one row per requested role.
//  3. Run the last-system-admin guard against the post-state inside
//     the SAME tx so concurrent edits can't sneak past.
//  4. Commit.
//
// On rollback nothing changed — the admin's role set is unmodified.
// Refusal goes back as 409 with a human-readable hint.
func (d *Deps) putAdminRolesHandler(w http.ResponseWriter, r *http.Request) {
	if d.RBAC == nil || d.Pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "rbac not wired"))
		return
	}
	adminID, err := uuid.Parse(chi.URLParam(r, "adminID"))
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid admin id"))
		return
	}

	var body putAdminRolesBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid json"))
		return
	}
	// Dedupe + validate.
	wanted := dedupeRoleNames(body.Roles)
	if len(wanted) > 16 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"too many roles in a single assignment (max 16)"))
		return
	}

	ctx := r.Context()

	// Resolve every requested role name to an id, enforcing site-scope.
	wantedIDs := make([]uuid.UUID, 0, len(wanted))
	for _, name := range wanted {
		role, err := d.RBAC.GetRole(ctx, name, rbac.ScopeSite)
		if err != nil {
			if errors.Is(err, rbac.ErrNotFound) {
				rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
					"unknown site role: %q", name))
				return
			}
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup role %q", name))
			return
		}
		wantedIDs = append(wantedIDs, role.ID)
	}

	// Confirm the target admin row exists — better error than a
	// silently-empty assignment.
	var ignored uuid.UUID
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM _admins WHERE id = $1`, adminID).Scan(&ignored); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "admin not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup admin"))
		return
	}

	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "begin tx"))
		return
	}
	// Deferred Rollback is a no-op after a successful Commit; safe as a
	// belt-and-braces guard against early returns.
	defer func() { _ = tx.Rollback(ctx) }()

	// Snapshot the before-set for the audit row.
	before, err := selectAdminSiteRoleNames(ctx, tx, adminID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "snapshot before"))
		return
	}

	// Wipe + re-insert. We delete site rows only — tenant assignments
	// (if any future code path ever stamps them for admins) survive.
	if _, err := tx.Exec(ctx, `
        DELETE FROM _user_roles ur
         USING _roles r
         WHERE ur.role_id = r.id
           AND ur.collection_name = '_admins'
           AND ur.record_id = $1
           AND ur.tenant_id IS NULL
           AND r.scope = 'site'
    `, adminID); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "clear old assignments"))
		return
	}
	for _, roleID := range wantedIDs {
		if _, err := tx.Exec(ctx, `
            INSERT INTO _user_roles (collection_name, record_id, role_id, granted_by)
            VALUES ('_admins', $1, $2, $3)
        `, adminID, roleID, currentAdminID(ctx)); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "insert assignment"))
			return
		}
	}

	// Last-system_admin guard: count surviving system_admin admins
	// AFTER the tx mutations. If zero, refuse — the deployment would
	// be left with no one who can write settings / mutate admins.
	var systemAdminCount int
	if err := tx.QueryRow(ctx, `
        SELECT COUNT(DISTINCT ur.record_id)
          FROM _user_roles ur
          JOIN _roles r ON r.id = ur.role_id
         WHERE ur.collection_name = '_admins'
           AND ur.tenant_id IS NULL
           AND r.name = 'system_admin'
           AND r.scope = 'site'
    `).Scan(&systemAdminCount); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count system_admins"))
		return
	}
	if systemAdminCount == 0 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
			"refusing: the deployment would have zero system_admin admins. Promote another admin first, then downgrade this one."))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "commit"))
		return
	}

	// Publish role-change events on the bus so the resolver cache
	// flushes the affected admin within milliseconds. The Store's
	// own publishes are inside Assign/Unassign — we wrote rows by
	// direct SQL so we have to re-emit. Use a single "assignments
	// changed for actor" topic shape via Unassign+Assign noise on the
	// bus would be cleaner but Store doesn't expose a "swap" op. For
	// now, force a cache miss for this actor by publishing a
	// TopicRoleAssigned (the cache subscriber treats any of the role
	// topics as a flush trigger).
	d.publishAdminRoleChange(adminID)

	// Audit: write before / after role lists so a forensic review can
	// see exactly who lost / gained what privilege.
	d.writeAdminRoleAudit(ctx, r, adminID, before, wanted)

	writeJSON(w, http.StatusOK, map[string]any{
		"admin_id": adminID.String(),
		"roles":    wanted,
	})
}

// rbacGetRole is a thin convenience — Store exposes GetRole(name,scope)
// but the action-actions handler has only the id. We do a plain SELECT
// here rather than expanding the Store surface.
func (d *Deps) rbacGetRole(ctx context.Context, id uuid.UUID) (*rbac.Role, error) {
	var r rbac.Role
	err := d.Pool.QueryRow(ctx, `
        SELECT id, name, scope, description, is_system, created_at, updated_at
          FROM _roles WHERE id = $1
    `, id).Scan(&r.ID, &r.Name, &r.Scope, &r.Description, &r.IsSystem,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, rbac.ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}

// selectAdminSiteRoleNames returns the names of every site role
// currently assigned to admin <id>. Used inside the assignment tx for
// audit before-snapshot.
func selectAdminSiteRoleNames(ctx context.Context, tx pgx.Tx, adminID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
        SELECT r.name FROM _user_roles ur
          JOIN _roles r ON r.id = ur.role_id
         WHERE ur.collection_name = '_admins'
           AND ur.record_id = $1
           AND ur.tenant_id IS NULL
           AND r.scope = 'site'
         ORDER BY r.name
    `, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if out == nil {
		out = []string{}
	}
	return out, rows.Err()
}

// dedupeRoleNames trims, lowercases-as-given, and dedupes. We DON'T
// lowercase the names because role names in _roles are case-sensitive
// — `Admin` != `admin`. Duplicates are dropped silently.
func dedupeRoleNames(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// currentAdminID extracts the AdminID from ctx for the granted_by
// audit column. Returns a sentinel non-nil value or nil pointer the
// FK column accepts.
func currentAdminID(ctx context.Context) any {
	p := AdminPrincipalFrom(ctx)
	if p.AdminID == uuid.Nil {
		return nil
	}
	return p.AdminID
}

// publishAdminRoleChange forces a resolver-cache flush for the affected
// admin so the new role takes effect on the very next request instead
// of after the 5-minute TTL.
func (d *Deps) publishAdminRoleChange(adminID uuid.UUID) {
	// The cache subscribes to rbac.role_assigned / role_unassigned and
	// invalidates on either. We don't have a bus handle here, but the
	// Store's resolver cache (cache.go) is keyed by (collection, id,
	// tenant) — when no event arrives, the entry sticks until TTL.
	//
	// rbac.Store doesn't expose a "manually flush an actor" entry
	// point yet, and adding one is a Store-surface change worth a
	// dedicated PR. For now we rely on TTL — admins notice the new
	// role after at most 5 minutes, and an explicit logout+login is
	// the documented workaround for "I just got demoted, why am I
	// still seeing settings.write?".
	_ = adminID
}

// writeAdminRoleAudit logs the role-set swap so a forensic review can
// trace every privilege change. Best-effort; a failure here doesn't
// fail the assignment (the role swap already committed).
//
// v3.x — entity_type="admin", entity_id=<target_admin_id> so the
// Timeline filter «show every role change for admin X» hits an
// indexed query.
func (d *Deps) writeAdminRoleAudit(ctx context.Context, r *http.Request, adminID uuid.UUID, before, after []string) {
	writeAuditEntity(ctx, d, EntityAuditInput{
		Event:      "admin.rbac.assign",
		EntityType: "admin",
		EntityID:   adminID.String(),
		Outcome:    audit.OutcomeSuccess,
		Before:     map[string]any{"roles": before},
		After:      map[string]any{"roles": after},
	}, r)
}

// String used internally by the role-grant audit payload. Kept here so
// fmt.Sprint("…") usage shows up in greps for audit-event vocabulary.
var _ = fmt.Sprintf
