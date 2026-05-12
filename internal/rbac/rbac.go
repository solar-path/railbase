// Package rbac is the v1.1.4 site + tenant RBAC core.
//
// Three concerns:
//
//	1. Role catalog (Store): site/tenant role rows, grants, assignments
//	2. Action resolution (LoadActions): expand a user's roles into the
//	   flat set of action keys they hold for the current request
//	3. Checks (Has): in-handler "does this principal hold action X?"
//
// Storage shape lives in 0012_rbac.up.sql + 0013_rbac_seed.up.sql.
//
// Bypass roles:
//
//	site:system_admin   → returns true for every action
//	tenant:owner        → returns true for every TENANT-scoped action
//	                      within their tenant
//
// LoadActions short-circuits when it sees one of these so we don't
// have to maintain "system_admin grants every action key" rows.
package rbac

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/rbac/actionkeys"
)

// Scope tags a role as site-wide or tenant-bound. Stable wire values.
type Scope string

const (
	ScopeSite   Scope = "site"
	ScopeTenant Scope = "tenant"
)

// ErrNotFound is returned by lookup paths on miss.
var ErrNotFound = errors.New("rbac: not found")

// Role is the materialised _roles row.
type Role struct {
	ID          uuid.UUID
	Name        string
	Scope       Scope
	Description string
	IsSystem    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Assignment is a _user_roles row joined with the role's name/scope
// for convenience.
type Assignment struct {
	ID             uuid.UUID
	CollectionName string
	RecordID       uuid.UUID
	Role           Role
	TenantID       *uuid.UUID // nil → site
	GrantedAt      time.Time
}

// Store is the persistence handle. Goroutine-safe.
//
// Mutating methods (Grant/Revoke/Assign/Unassign/DeleteRole/CreateRole)
// publish on bus when non-nil — see events.go for the topic catalogue.
// The resolver cache (cache.go) subscribes to these topics via
// SubscribeInvalidation so role changes propagate within milliseconds
// instead of waiting for the 5-minute TTL.
type Store struct {
	pool *pgxpool.Pool
	bus  *eventbus.Bus
	log  *slog.Logger
}

// StoreOptions bundles Store construction inputs. Adding a new field
// later doesn't break callers that already use the options form.
type StoreOptions struct {
	// Pool is required.
	Pool *pgxpool.Pool
	// Bus is optional. When nil, mutation events are not published —
	// callers that don't wire the bus (CLI one-shots, tests) keep the
	// store fully functional but lose live invalidation.
	Bus *eventbus.Bus
	// Log is optional; defaults to slog.Default() when nil.
	Log *slog.Logger
}

// NewStore returns a Store with no bus wired. Kept for callers that
// don't need event publishing (the CLI role tool, ad-hoc tests).
// Long-lived processes should prefer NewStoreWithOptions to enable
// resolver-cache invalidation on role mutations.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, log: slog.Default()}
}

// NewStoreWithOptions is the bus-aware constructor. Pass StoreOptions
// with Pool + Bus + Log; the resulting Store publishes RoleEvent
// payloads on every successful mutation. Pair with
// rbac.SubscribeInvalidation(bus) so the resolver cache observes the
// publishes and purges accordingly.
func NewStoreWithOptions(opts StoreOptions) *Store {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Store{pool: opts.Pool, bus: opts.Bus, log: opts.Log}
}

// publish is the internal helper that fans a mutation event out on the
// bus. No-op when bus is nil (CLI / test path). Fire-and-forget —
// eventbus.Publish is async and dropping under load is acceptable for
// observation events (the TTL is the safety net).
func (s *Store) publish(topic string, payload RoleEvent) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{Topic: topic, Payload: payload})
}

// --- role CRUD ---

// CreateRole inserts a custom role. is_system=false; system roles are
// only created by the seed migration.
//
// On success publishes TopicRoleAssigned-style invalidation event?
// No — CreateRole alone doesn't grant anyone the role yet, so cached
// Resolved sets are unaffected. We skip the publish; the subsequent
// Assign call will trigger invalidation when the role actually enters
// a user's resolved set.
func (s *Store) CreateRole(ctx context.Context, name string, scope Scope, description string) (*Role, error) {
	const q = `
        INSERT INTO _roles (name, scope, description, is_system)
        VALUES ($1, $2, $3, FALSE)
        RETURNING id, name, scope, description, is_system, created_at, updated_at
    `
	row := s.pool.QueryRow(ctx, q, name, string(scope), description)
	return scanRole(row)
}

// GetRole loads by (name, scope).
func (s *Store) GetRole(ctx context.Context, name string, scope Scope) (*Role, error) {
	const q = `
        SELECT id, name, scope, description, is_system, created_at, updated_at
          FROM _roles WHERE name = $1 AND scope = $2
    `
	row := s.pool.QueryRow(ctx, q, name, string(scope))
	r, err := scanRole(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListRoles returns every role; admin UI / CLI consumes this.
func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	const q = `
        SELECT id, name, scope, description, is_system, created_at, updated_at
          FROM _roles ORDER BY scope ASC, name ASC
    `
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// DeleteRole removes a custom role. System roles refuse deletion;
// caller surfaces this as 409.
//
// On success publishes TopicRoleDeleted so the resolver cache flushes:
// FK cascades from _roles → _user_roles + _role_actions silently drop
// every cached Resolved that depended on this role.
func (s *Store) DeleteRole(ctx context.Context, id uuid.UUID) error {
	// Check system flag explicitly so callers see a clean error rather
	// than an FK-removal cascade silently leaving system seed intact.
	var isSystem bool
	if err := s.pool.QueryRow(ctx, `SELECT is_system FROM _roles WHERE id = $1`, id).Scan(&isSystem); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if isSystem {
		return fmt.Errorf("rbac: cannot delete system role")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM _roles WHERE id = $1`, id); err != nil {
		return err
	}
	s.publish(TopicRoleDeleted, RoleEvent{Role: id.String()})
	return nil
}

// --- grants ---

// Grant adds an action to a role. Idempotent: re-granting returns nil.
//
// On success publishes TopicRoleGranted so the resolver cache purges:
// every Resolved set that derived from this role just gained an
// action and needs re-expansion.
func (s *Store) Grant(ctx context.Context, roleID uuid.UUID, action actionkeys.ActionKey) error {
	const q = `
        INSERT INTO _role_actions (role_id, action_key)
        VALUES ($1, $2)
        ON CONFLICT DO NOTHING
    `
	if _, err := s.pool.Exec(ctx, q, roleID, string(action)); err != nil {
		return err
	}
	s.publish(TopicRoleGranted, RoleEvent{Role: roleID.String(), Action: string(action)})
	return nil
}

// Revoke removes a grant. Idempotent.
//
// On success publishes TopicRoleRevoked so the resolver cache purges:
// users who held the revoked action via this role must not retain the
// stale grant for up to 5 minutes.
func (s *Store) Revoke(ctx context.Context, roleID uuid.UUID, action actionkeys.ActionKey) error {
	const q = `
        DELETE FROM _role_actions WHERE role_id = $1 AND action_key = $2
    `
	if _, err := s.pool.Exec(ctx, q, roleID, string(action)); err != nil {
		return err
	}
	s.publish(TopicRoleRevoked, RoleEvent{Role: roleID.String(), Action: string(action)})
	return nil
}

// ListActions returns every action granted to a role. Admin UI uses
// it to render the action matrix.
func (s *Store) ListActions(ctx context.Context, roleID uuid.UUID) ([]actionkeys.ActionKey, error) {
	const q = `SELECT action_key FROM _role_actions WHERE role_id = $1 ORDER BY action_key`
	rows, err := s.pool.Query(ctx, q, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []actionkeys.ActionKey
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, actionkeys.ActionKey(k))
	}
	return out, rows.Err()
}

// --- assignments ---

// AssignInput captures what Assign needs. TenantID=nil → site role.
type AssignInput struct {
	CollectionName string
	RecordID       uuid.UUID
	RoleID         uuid.UUID
	TenantID       *uuid.UUID
	GrantedBy      *uuid.UUID // admin who granted (audit trail)
}

// Assign creates a user→role assignment. Idempotent at the per-
// (user, role, tenant) level: re-assigning returns the existing row.
//
// On a new-row create, publishes TopicRoleAssigned so the resolver
// cache purges; idempotent "already exists" returns do NOT publish
// (nothing changed). The tenant field is empty for site-scoped
// assignments.
func (s *Store) Assign(ctx context.Context, in AssignInput) (*Assignment, error) {
	const q = `
        INSERT INTO _user_roles
            (collection_name, record_id, role_id, tenant_id, granted_by)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT DO NOTHING
        RETURNING id, granted_at
    `
	var id uuid.UUID
	var grantedAt time.Time
	row := s.pool.QueryRow(ctx, q,
		in.CollectionName, in.RecordID, in.RoleID, in.TenantID, in.GrantedBy)
	if err := row.Scan(&id, &grantedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already assigned — return existing. No publish: nothing
			// changed in role-actor state.
			return s.findAssignment(ctx, in.CollectionName, in.RecordID, in.RoleID, in.TenantID)
		}
		return nil, err
	}
	role, err := s.getRoleByID(ctx, in.RoleID)
	if err != nil {
		return nil, err
	}
	ev := RoleEvent{
		Role:   in.RoleID.String(),
		Actor:  in.CollectionName,
		UserID: in.RecordID.String(),
	}
	if in.TenantID != nil {
		ev.Tenant = in.TenantID.String()
	}
	s.publish(TopicRoleAssigned, ev)
	return &Assignment{
		ID:             id,
		CollectionName: in.CollectionName,
		RecordID:       in.RecordID,
		Role:           *role,
		TenantID:       in.TenantID,
		GrantedAt:      grantedAt,
	}, nil
}

// Unassign removes a user→role assignment. Idempotent.
//
// On success publishes TopicRoleUnassigned. We don't check whether a
// row actually existed (would require an extra query); a publish on a
// no-op delete is harmless — the cache flushes regardless.
func (s *Store) Unassign(ctx context.Context, collectionName string, recordID, roleID uuid.UUID, tenantID *uuid.UUID) error {
	if tenantID == nil {
		const q = `
            DELETE FROM _user_roles
             WHERE collection_name = $1 AND record_id = $2
               AND role_id = $3 AND tenant_id IS NULL
        `
		if _, err := s.pool.Exec(ctx, q, collectionName, recordID, roleID); err != nil {
			return err
		}
	} else {
		const q = `
            DELETE FROM _user_roles
             WHERE collection_name = $1 AND record_id = $2
               AND role_id = $3 AND tenant_id = $4
        `
		if _, err := s.pool.Exec(ctx, q, collectionName, recordID, roleID, *tenantID); err != nil {
			return err
		}
	}
	ev := RoleEvent{
		Role:   roleID.String(),
		Actor:  collectionName,
		UserID: recordID.String(),
	}
	if tenantID != nil {
		ev.Tenant = tenantID.String()
	}
	s.publish(TopicRoleUnassigned, ev)
	return nil
}

// ListAssignmentsFor returns every assignment for a user. Filter to
// site-only by passing tenantID=nil + onlySite=true; otherwise both
// site + tenant rows are returned.
func (s *Store) ListAssignmentsFor(ctx context.Context, collectionName string, recordID uuid.UUID) ([]Assignment, error) {
	const q = `
        SELECT ur.id, ur.collection_name, ur.record_id, ur.tenant_id, ur.granted_at,
               r.id, r.name, r.scope, r.description, r.is_system, r.created_at, r.updated_at
          FROM _user_roles ur
          JOIN _roles r ON r.id = ur.role_id
         WHERE ur.collection_name = $1 AND ur.record_id = $2
         ORDER BY r.scope, r.name
    `
	rows, err := s.pool.Query(ctx, q, collectionName, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		var a Assignment
		var r Role
		if err := rows.Scan(
			&a.ID, &a.CollectionName, &a.RecordID, &a.TenantID, &a.GrantedAt,
			&r.ID, &r.Name, &r.Scope, &r.Description, &r.IsSystem, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		a.Role = r
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- action resolution ---

// Resolved is the materialised "what can this principal do right now"
// answer. Cache me in request context (see middleware.go).
type Resolved struct {
	UserCollection string
	UserID         uuid.UUID
	TenantID       *uuid.UUID
	// SiteBypass = true ⇔ user holds the system_admin role. Skip all
	// other checks.
	SiteBypass bool
	// TenantBypass = true ⇔ user holds the tenant:owner role for
	// TenantID. SiteBypass=true implies TenantBypass=true.
	TenantBypass bool
	// Actions is the flat set of action keys held across all the
	// user's site roles + any tenant roles for TenantID. Used by
	// Has(); not consulted when SiteBypass / TenantBypass is set.
	Actions map[actionkeys.ActionKey]struct{}
}

// Resolve loads every role for (collection, recordID) and expands
// into action keys. Pass tenantID=nil to skip tenant-scoped grants.
//
// One indexed query (joined) — fits in a single pool.Acquire.
func (s *Store) Resolve(ctx context.Context, collectionName string, recordID uuid.UUID, tenantID *uuid.UUID) (*Resolved, error) {
	out := &Resolved{
		UserCollection: collectionName,
		UserID:         recordID,
		TenantID:       tenantID,
		Actions:        map[actionkeys.ActionKey]struct{}{},
	}

	// First pass: roles for this user (site + tenant-matched).
	const roleQ = `
        SELECT r.id, r.name, r.scope
          FROM _user_roles ur
          JOIN _roles r ON r.id = ur.role_id
         WHERE ur.collection_name = $1 AND ur.record_id = $2
           AND (ur.tenant_id IS NULL OR ur.tenant_id = $3)
    `
	rows, err := s.pool.Query(ctx, roleQ, collectionName, recordID, nilOrUUID(tenantID))
	if err != nil {
		return nil, err
	}
	type rid struct {
		id    uuid.UUID
		name  string
		scope Scope
	}
	var roleIDs []rid
	for rows.Next() {
		var r rid
		var scope string
		if err := rows.Scan(&r.id, &r.name, &scope); err != nil {
			rows.Close()
			return nil, err
		}
		r.scope = Scope(scope)
		roleIDs = append(roleIDs, r)
		if r.scope == ScopeSite && r.name == "system_admin" {
			out.SiteBypass = true
			out.TenantBypass = true
		}
		if r.scope == ScopeTenant && r.name == "owner" && tenantID != nil {
			out.TenantBypass = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out.SiteBypass {
		return out, nil // skip the action-expand query
	}
	if len(roleIDs) == 0 {
		return out, nil
	}

	// Second pass: every action for these roles.
	ids := make([]uuid.UUID, len(roleIDs))
	for i, r := range roleIDs {
		ids[i] = r.id
	}
	const actionsQ = `SELECT action_key FROM _role_actions WHERE role_id = ANY($1)`
	arows, err := s.pool.Query(ctx, actionsQ, ids)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var k string
		if err := arows.Scan(&k); err != nil {
			return nil, err
		}
		out.Actions[actionkeys.ActionKey(k)] = struct{}{}
	}
	return out, arows.Err()
}

// Has reports whether the resolved set grants `action`. Bypass roles
// short-circuit. Tenant bypass applies only when `action` is in the
// tenant-scoped namespace (prefix "tenant.").
func (r *Resolved) Has(action actionkeys.ActionKey) bool {
	if r == nil {
		return false
	}
	if r.SiteBypass {
		return true
	}
	if r.TenantBypass && isTenantAction(action) {
		return true
	}
	_, ok := r.Actions[action]
	return ok
}

// HasAny reports true if the resolved set holds at least one of the
// listed actions. Convenience for handlers that accept several
// equivalent privileges.
func (r *Resolved) HasAny(actions ...actionkeys.ActionKey) bool {
	for _, a := range actions {
		if r.Has(a) {
			return true
		}
	}
	return false
}

// --- internal helpers ---

func (s *Store) getRoleByID(ctx context.Context, id uuid.UUID) (*Role, error) {
	const q = `
        SELECT id, name, scope, description, is_system, created_at, updated_at
          FROM _roles WHERE id = $1
    `
	return scanRole(s.pool.QueryRow(ctx, q, id))
}

func (s *Store) findAssignment(ctx context.Context, collectionName string, recordID, roleID uuid.UUID, tenantID *uuid.UUID) (*Assignment, error) {
	var q string
	var args []any
	if tenantID == nil {
		q = `
            SELECT ur.id, ur.granted_at,
                   r.id, r.name, r.scope, r.description, r.is_system,
                   r.created_at, r.updated_at
              FROM _user_roles ur JOIN _roles r ON r.id = ur.role_id
             WHERE ur.collection_name = $1 AND ur.record_id = $2
               AND ur.role_id = $3 AND ur.tenant_id IS NULL
        `
		args = []any{collectionName, recordID, roleID}
	} else {
		q = `
            SELECT ur.id, ur.granted_at,
                   r.id, r.name, r.scope, r.description, r.is_system,
                   r.created_at, r.updated_at
              FROM _user_roles ur JOIN _roles r ON r.id = ur.role_id
             WHERE ur.collection_name = $1 AND ur.record_id = $2
               AND ur.role_id = $3 AND ur.tenant_id = $4
        `
		args = []any{collectionName, recordID, roleID, *tenantID}
	}
	row := s.pool.QueryRow(ctx, q, args...)
	var a Assignment
	a.CollectionName = collectionName
	a.RecordID = recordID
	a.TenantID = tenantID
	var r Role
	if err := row.Scan(&a.ID, &a.GrantedAt,
		&r.ID, &r.Name, &r.Scope, &r.Description, &r.IsSystem,
		&r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	a.Role = r
	return &a, nil
}

func scanRole(row pgx.Row) (*Role, error) {
	var r Role
	var scope string
	if err := row.Scan(&r.ID, &r.Name, &scope, &r.Description, &r.IsSystem,
		&r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Scope = Scope(scope)
	return &r, nil
}

func nilOrUUID(u *uuid.UUID) any {
	if u == nil {
		return nil
	}
	return *u
}

// isTenantAction reports whether the key falls under the
// "tenant.*" namespace — the only actions tenant-owner bypass
// covers. Site actions (admins.list, audit.read, etc.) need
// site-scoped roles.
func isTenantAction(k actionkeys.ActionKey) bool {
	s := string(k)
	return len(s) > 7 && s[:7] == "tenant."
}
