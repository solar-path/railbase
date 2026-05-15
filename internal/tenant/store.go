package tenant

// Store is the persistence layer for the `_tenants` + `_tenant_members`
// tables introduced in migration 0032. Distinct from the middleware
// in tenant.go (which scopes per-request Postgres connections by the
// X-Tenant header) — Store is what the /api/tenants HTTP handlers
// talk to.
//
// Lifecycle:
//
//	Create   — POST /api/tenants. Inserts a _tenants row AND a
//	           _tenant_members row with role='owner' bound to the
//	           creator. The two writes go in one transaction so a
//	           failed second insert rolls the tenant back rather than
//	           leaving an orphan.
//	List     — GET /api/tenants. Joins _tenant_members on the principal
//	           and returns the tenants they hold an accepted membership
//	           in. Pending-invite rows are skipped.
//	Get      — GET /api/tenants/{id}. The middleware-level membership
//	           check is the caller's responsibility (Store doesn't
//	           authorize, just reads).
//	Update   — PATCH /api/tenants/{id}. Partial update on name/slug.
//	Delete   — DELETE /api/tenants/{id}. Soft (sets deleted_at).
//	Membership — Sprint 2 surfaces (Add/Update/Remove/ListMembers)
//	             land alongside the user-management API.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role values. Three built-in tiers; operators can grow custom roles
// via the per-tenant RBAC API (Sprint 4) — those are stored verbatim
// in the `role` column as `'custom:<roleID>'`. The well-known values
// MUST keep these exact spellings — UI + RBAC handlers branch on them.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// ErrNotFound is returned when no live row matches the lookup. Hiding
// "exists but deleted" behind the same error is intentional — leaking
// the distinction lets a hostile caller probe for tenant existence.
var ErrNotFound = errors.New("tenant: not found")

// ErrSlugTaken is returned when Create / Update is asked to write a
// slug that already exists on a non-deleted row. The CHECK constraint
// on the table also guards format; we surface the unique-violation
// here so the REST handler can map it to a 409 with a stable code.
var ErrSlugTaken = errors.New("tenant: slug already in use")

// Tenant is one row from `_tenants` (live or soft-deleted).
type Tenant struct {
	ID        uuid.UUID
	Name      string
	Slug      string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time // nil → live
}

// MembershipRole is the resolved role of a (collection, user) on a
// specific tenant — returned by ListMine to render the per-tenant
// pick UI without a second round-trip.
type MembershipRole struct {
	TenantID uuid.UUID
	Role     string
	IsOwner  bool
}

// Store is the persistence handle. Tied to a pgx pool; tests pass an
// embedded-pg pool, production wires the same pool the rest of the
// app uses.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store bound to pool. The pool MUST have
// migration 0032 applied — the constructor does NOT verify schema
// presence (would require an extra round-trip on every boot).
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create inserts a new tenant AND a `_tenant_members` row binding
// the creator as the owner. Both writes happen in one transaction so
// a failed second insert leaves no orphan tenant. Returns the
// persisted row (with server-set timestamps + id).
//
// `slug` must already match the table's CHECK regex
// `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`. Caller normalises (lowercase,
// trim, hyphenate spaces); Store doesn't try to be clever. A unique-
// violation surfaces as ErrSlugTaken.
func (s *Store) Create(ctx context.Context, name, slug, collectionName string, ownerID uuid.UUID) (*Tenant, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("tenant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var t Tenant
	err = tx.QueryRow(ctx, `
        INSERT INTO _tenants (name, slug)
        VALUES ($1, $2)
        RETURNING id, name, slug, created_at, updated_at, deleted_at
    `, name, slug).Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlugTaken
		}
		return nil, fmt.Errorf("tenant: insert: %w", err)
	}
	now := time.Now()
	if _, err := tx.Exec(ctx, `
        INSERT INTO _tenant_members
            (tenant_id, collection_name, user_id, role, accepted_at, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $5, $5)
    `, t.ID, collectionName, ownerID, RoleOwner, now); err != nil {
		return nil, fmt.Errorf("tenant: bind owner: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("tenant: commit: %w", err)
	}
	return &t, nil
}

// Get returns a tenant by ID. Returns ErrNotFound for deleted rows.
// Does NOT check membership — the caller (REST handler) authorises.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx, `
        SELECT id, name, slug, created_at, updated_at, deleted_at
          FROM _tenants
         WHERE id = $1 AND deleted_at IS NULL
    `, id).Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tenant: get: %w", err)
	}
	return &t, nil
}

// GetBySlug is the same lookup keyed by slug. Used by URL-based
// routing (`/t/<slug>`) where the client doesn't know the UUID.
func (s *Store) GetBySlug(ctx context.Context, slug string) (*Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx, `
        SELECT id, name, slug, created_at, updated_at, deleted_at
          FROM _tenants
         WHERE slug = $1 AND deleted_at IS NULL
    `, slug).Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tenant: get-by-slug: %w", err)
	}
	return &t, nil
}

// ListMine returns every live tenant the (collection, user) holds an
// ACCEPTED membership in. Pending invites are excluded — they surface
// on a separate Sprint 2 endpoint. Ordered by tenant.name for stable
// UI rendering.
func (s *Store) ListMine(ctx context.Context, collectionName string, userID uuid.UUID) ([]*Tenant, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT t.id, t.name, t.slug, t.created_at, t.updated_at, t.deleted_at
          FROM _tenants t
          JOIN _tenant_members m
            ON m.tenant_id = t.id
         WHERE t.deleted_at IS NULL
           AND m.collection_name = $1
           AND m.user_id = $2
           AND m.accepted_at IS NOT NULL
         ORDER BY t.name`, collectionName, userID)
	if err != nil {
		return nil, fmt.Errorf("tenant: list-mine: %w", err)
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		t := &Tenant{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt); err != nil {
			return nil, fmt.Errorf("tenant: list-mine scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MyRole returns the principal's role on a specific tenant. Used by
// the per-tenant route guards (e.g. "only owners may rename"). The
// "is_owner" bool collapses 'owner' AND 'admin' into the privileged
// set so callers don't have to remember which strings count.
//
// Returns ErrNotFound when the user has no accepted membership (or
// only a pending invite) on the tenant. Soft-deleted tenants also
// surface as ErrNotFound.
func (s *Store) MyRole(ctx context.Context, tenantID uuid.UUID, collectionName string, userID uuid.UUID) (*MembershipRole, error) {
	var role string
	err := s.pool.QueryRow(ctx, `
        SELECT m.role
          FROM _tenant_members m
          JOIN _tenants t ON t.id = m.tenant_id
         WHERE m.tenant_id = $1
           AND m.collection_name = $2
           AND m.user_id = $3
           AND m.accepted_at IS NOT NULL
           AND t.deleted_at IS NULL`, tenantID, collectionName, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tenant: my-role: %w", err)
	}
	return &MembershipRole{
		TenantID: tenantID,
		Role:     role,
		IsOwner:  role == RoleOwner || role == RoleAdmin,
	}, nil
}

// UpdateInput captures the partial-update body. Nil pointer = leave
// alone. The handler decides which subset of these the caller's role
// can write — Store applies whatever is non-nil.
type UpdateInput struct {
	Name *string
	Slug *string
}

// Update writes the supplied fields. Returns the refreshed row. Empty
// input (both nil) is a no-op that still returns the current row so
// the caller can echo back to the UI.
func (s *Store) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (*Tenant, error) {
	// Build dynamic SET. Same pattern as session.UpdateMetadata.
	set := []string{}
	args := []any{}
	if in.Name != nil {
		args = append(args, *in.Name)
		set = append(set, fmt.Sprintf("name = $%d", len(args)))
	}
	if in.Slug != nil {
		args = append(args, *in.Slug)
		set = append(set, fmt.Sprintf("slug = $%d", len(args)))
	}
	if len(set) == 0 {
		// No-op — just return current state.
		return s.Get(ctx, id)
	}
	args = append(args, id)
	q := fmt.Sprintf(`
        UPDATE _tenants
           SET %s, updated_at = now()
         WHERE id = $%d AND deleted_at IS NULL
        RETURNING id, name, slug, created_at, updated_at, deleted_at`,
		strings.Join(set, ", "), len(args))
	var t Tenant
	err := s.pool.QueryRow(ctx, q, args...).Scan(
		&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlugTaken
		}
		return nil, fmt.Errorf("tenant: update: %w", err)
	}
	return &t, nil
}

// Member is one row from `_tenant_members`. Used by the per-tenant
// user-management endpoints. UserID is uuid.Nil for pending invites
// whose invitee has not yet signed up; once accepted, it stamps with
// the resolved auth-collection user id.
type Member struct {
	TenantID       uuid.UUID
	CollectionName string
	UserID         uuid.UUID
	Role           string
	InvitedEmail   *string
	InvitedAt      *time.Time
	AcceptedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ErrMemberExists fires when an Invite call lands on an already-
// present (tenant, collection, user) row. The handler maps it to 409
// so the UI can render "already a member" without a second probe.
var ErrMemberExists = errors.New("tenant: already a member")

// ListMembers returns every member row for the tenant — accepted +
// pending. Caller is responsible for authorising (handler checks
// MyRole.IsOwner upstream).
func (s *Store) ListMembers(ctx context.Context, tenantID uuid.UUID) ([]*Member, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT tenant_id, collection_name, user_id, role,
               invited_email, invited_at, accepted_at, created_at, updated_at
          FROM _tenant_members
         WHERE tenant_id = $1
         ORDER BY accepted_at NULLS LAST, created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant: list-members: %w", err)
	}
	defer rows.Close()
	var out []*Member
	for rows.Next() {
		m := &Member{}
		if err := rows.Scan(&m.TenantID, &m.CollectionName, &m.UserID, &m.Role,
			&m.InvitedEmail, &m.InvitedAt, &m.AcceptedAt, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("tenant: list-members scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// InviteInput is the wire input for an invite. Email is required;
// UserID is set when the caller has already resolved the invitee
// against the auth collection (= directly adds an accepted member
// rather than a pending invite). Both paths share the same row.
type InviteInput struct {
	TenantID       uuid.UUID
	CollectionName string
	Email          string     // invitee email (always set, even on direct adds — for display)
	UserID         *uuid.UUID // nil → pending invite; set → accepted membership
	Role           string
}

// Invite inserts a (tenant, collection, user_or_email) membership row.
// When UserID is nil the row carries invited_email + invited_at and
// accepted_at = NULL — the invitee accepts via Accept(token) later.
// When UserID is set, accepted_at is stamped immediately (operator
// added a known user directly).
//
// Conflict handling: an existing accepted row for the same
// (tenant, collection, user) yields ErrMemberExists. An existing
// pending invite for the same email is replaced (the latest invite
// wins — typical "re-send invite" UX).
func (s *Store) Invite(ctx context.Context, in InviteInput) (*Member, error) {
	now := time.Now()
	role := in.Role
	if role == "" {
		role = RoleMember
	}
	if in.UserID != nil {
		// Direct add — already-resolved user. Upsert is unsafe because
		// the (tenant, coll, user) PK is the same on a "promote" and on
		// a "double-invite". Insert + return-conflict-as-error keeps
		// the semantics explicit.
		m := &Member{}
		err := s.pool.QueryRow(ctx, `
            INSERT INTO _tenant_members
                (tenant_id, collection_name, user_id, role,
                 invited_email, invited_at, accepted_at, created_at, updated_at)
            VALUES ($1, $2, $3, $4, $5, $6, $6, $6, $6)
            RETURNING tenant_id, collection_name, user_id, role,
                      invited_email, invited_at, accepted_at, created_at, updated_at`,
			in.TenantID, in.CollectionName, *in.UserID, role, in.Email, now).Scan(
			&m.TenantID, &m.CollectionName, &m.UserID, &m.Role,
			&m.InvitedEmail, &m.InvitedAt, &m.AcceptedAt, &m.CreatedAt, &m.UpdatedAt)
		if err != nil {
			if isUniqueViolation(err) {
				return nil, ErrMemberExists
			}
			return nil, fmt.Errorf("tenant: invite (direct): %w", err)
		}
		return m, nil
	}

	// Pending invite — invitee email recorded with a placeholder
	// uuid in the user_id slot so the PK is still satisfied. The
	// placeholder is deterministic per (tenant, email) so re-sending
	// the same invite hits the same row (the ON CONFLICT updates
	// invited_at / role).
	placeholder := pendingUserID(in.TenantID, in.Email)
	m := &Member{}
	err := s.pool.QueryRow(ctx, `
        INSERT INTO _tenant_members
            (tenant_id, collection_name, user_id, role,
             invited_email, invited_at, accepted_at, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6, NULL, $6, $6)
        ON CONFLICT (tenant_id, collection_name, user_id) DO UPDATE
            SET role = EXCLUDED.role,
                invited_email = EXCLUDED.invited_email,
                invited_at = EXCLUDED.invited_at,
                updated_at = EXCLUDED.updated_at
            WHERE _tenant_members.accepted_at IS NULL
        RETURNING tenant_id, collection_name, user_id, role,
                  invited_email, invited_at, accepted_at, created_at, updated_at`,
		in.TenantID, in.CollectionName, placeholder, role, in.Email, now).Scan(
		&m.TenantID, &m.CollectionName, &m.UserID, &m.Role,
		&m.InvitedEmail, &m.InvitedAt, &m.AcceptedAt, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		// pgx surfaces "no rows" when ON CONFLICT's WHERE filters out
		// the update (because the row is already accepted → not a
		// pending invite). That's our ErrMemberExists.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMemberExists
		}
		return nil, fmt.Errorf("tenant: invite (pending): %w", err)
	}
	return m, nil
}

// UpdateMemberRole changes the role on an existing (accepted or
// pending) row. The handler is expected to enforce that the caller
// has owner/admin rights AND that they don't demote the last owner —
// demotion-of-last-owner is checked here in SQL too (no rows touched
// when the role change would leave zero owners on the tenant).
func (s *Store) UpdateMemberRole(ctx context.Context, tenantID uuid.UUID, collectionName string, userID uuid.UUID, role string) error {
	// Guard: if we're demoting an owner, count remaining owners.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("tenant: begin update-role: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentRole string
	err = tx.QueryRow(ctx, `
        SELECT role FROM _tenant_members
         WHERE tenant_id = $1 AND collection_name = $2 AND user_id = $3`,
		tenantID, collectionName, userID).Scan(&currentRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("tenant: read current role: %w", err)
	}
	if currentRole == RoleOwner && role != RoleOwner {
		var ownerCount int
		if err := tx.QueryRow(ctx, `
            SELECT count(*) FROM _tenant_members
             WHERE tenant_id = $1 AND role = 'owner' AND accepted_at IS NOT NULL`,
			tenantID).Scan(&ownerCount); err != nil {
			return fmt.Errorf("tenant: count owners: %w", err)
		}
		if ownerCount <= 1 {
			return ErrLastOwner
		}
	}
	tag, err := tx.Exec(ctx, `
        UPDATE _tenant_members
           SET role = $4, updated_at = now()
         WHERE tenant_id = $1 AND collection_name = $2 AND user_id = $3`,
		tenantID, collectionName, userID, role)
	if err != nil {
		return fmt.Errorf("tenant: update role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// RemoveMember drops the (tenant, collection, user) row entirely.
// Same last-owner guard as UpdateMemberRole — you can't strand a
// tenant without an owner by yanking the only owner.
func (s *Store) RemoveMember(ctx context.Context, tenantID uuid.UUID, collectionName string, userID uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("tenant: begin remove-member: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentRole string
	var accepted *time.Time
	err = tx.QueryRow(ctx, `
        SELECT role, accepted_at FROM _tenant_members
         WHERE tenant_id = $1 AND collection_name = $2 AND user_id = $3`,
		tenantID, collectionName, userID).Scan(&currentRole, &accepted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("tenant: read pre-remove: %w", err)
	}
	// Pending invites can always be revoked — no owner-count concern.
	if currentRole == RoleOwner && accepted != nil {
		var ownerCount int
		if err := tx.QueryRow(ctx, `
            SELECT count(*) FROM _tenant_members
             WHERE tenant_id = $1 AND role = 'owner' AND accepted_at IS NOT NULL`,
			tenantID).Scan(&ownerCount); err != nil {
			return fmt.Errorf("tenant: count owners: %w", err)
		}
		if ownerCount <= 1 {
			return ErrLastOwner
		}
	}
	tag, err := tx.Exec(ctx, `
        DELETE FROM _tenant_members
         WHERE tenant_id = $1 AND collection_name = $2 AND user_id = $3`,
		tenantID, collectionName, userID)
	if err != nil {
		return fmt.Errorf("tenant: delete member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// AcceptInvite turns a pending invite for `email` into an accepted
// membership for `userID` (the auth user that just signed up or
// signed in). Returns ErrNotFound if no pending invite matches.
//
// `tenantID` is optional — when uuid.Nil, the function accepts the
// first matching pending invite across any tenant (typical post-
// signup UX: invitee clicks the link in the email and is dropped
// into the tenant). When non-nil, only that specific tenant's
// invite is accepted (UI-driven accept of a specific row).
//
// The placeholder user_id used during Invite() is REPLACED by the
// real userID via DELETE-then-INSERT inside one transaction. We
// can't UPDATE in place because user_id is part of the PK.
func (s *Store) AcceptInvite(ctx context.Context, tenantID uuid.UUID, collectionName, email string, userID uuid.UUID) (*Member, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("tenant: begin accept: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := `
        SELECT tenant_id, user_id, role, invited_at
          FROM _tenant_members
         WHERE collection_name = $1
           AND lower(invited_email) = lower($2)
           AND accepted_at IS NULL`
	args := []any{collectionName, email}
	if tenantID != uuid.Nil {
		q += ` AND tenant_id = $3`
		args = append(args, tenantID)
	}
	q += ` ORDER BY invited_at DESC LIMIT 1`
	var row struct {
		TenantID  uuid.UUID
		UserID    uuid.UUID
		Role      string
		InvitedAt *time.Time
	}
	if err := tx.QueryRow(ctx, q, args...).Scan(&row.TenantID, &row.UserID, &row.Role, &row.InvitedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("tenant: load pending invite: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM _tenant_members WHERE tenant_id = $1 AND collection_name = $2 AND user_id = $3`,
		row.TenantID, collectionName, row.UserID); err != nil {
		return nil, fmt.Errorf("tenant: drop pending row: %w", err)
	}
	now := time.Now()
	m := &Member{}
	err = tx.QueryRow(ctx, `
        INSERT INTO _tenant_members
            (tenant_id, collection_name, user_id, role,
             invited_email, invited_at, accepted_at, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $7)
        RETURNING tenant_id, collection_name, user_id, role,
                  invited_email, invited_at, accepted_at, created_at, updated_at`,
		row.TenantID, collectionName, userID, row.Role, email, row.InvitedAt, now).Scan(
		&m.TenantID, &m.CollectionName, &m.UserID, &m.Role,
		&m.InvitedEmail, &m.InvitedAt, &m.AcceptedAt, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		// User may already have an accepted membership on this tenant
		// (e.g. they were directly added earlier). Surface that as
		// ErrMemberExists so the handler returns 409 and the invite
		// row is still cleaned up by the DELETE above (rolled back).
		if isUniqueViolation(err) {
			return nil, ErrMemberExists
		}
		return nil, fmt.Errorf("tenant: stamp accepted: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("tenant: commit accept: %w", err)
	}
	return m, nil
}

// ErrLastOwner is returned by UpdateMemberRole / RemoveMember when
// the operation would leave the tenant with zero owners. Mapped to
// 409 by the handler with a stable code so the UI can render
// "promote another member to owner first".
var ErrLastOwner = errors.New("tenant: cannot demote/remove the last owner")

// pendingUserID derives a deterministic placeholder UUID for a
// pending invite from (tenantID, email). The placeholder fills the
// user_id slot until the invitee accepts (DELETE + re-INSERT under
// the real user_id). Using a hash rather than uuid.New() lets the
// "re-send invite" path land on the same row via ON CONFLICT DO
// UPDATE — without it, every resend would create an orphan row.
//
// Namespace: uuid.NameSpaceURL because the tuple (tenant, email)
// reads like a URL-shaped key; the namespace choice is arbitrary so
// long as it's stable.
func pendingUserID(tenantID uuid.UUID, email string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(tenantID.String()+"|"+strings.ToLower(email)))
}

// Delete soft-deletes the tenant (sets deleted_at). All membership
// rows survive — the FK is ON DELETE CASCADE only on hard-delete.
// A v0.5 cron sweep can hard-delete tenants older than retention.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
        UPDATE _tenants
           SET deleted_at = now(), updated_at = now()
         WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("tenant: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// isUniqueViolation matches pg's SQLSTATE 23505. We don't import the
// full pgconn just for this — the error string contains the code.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
