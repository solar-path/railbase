package authority

// Delegation primitive — Slice 1 hardening of Slice 0 prototype.
//
// Adds: _doa_delegations table CRUD + integration with ResolveApprovers.
// When a delegation is active for actor D, the runtime treats D as
// qualified anywhere the delegator is qualified, scoped by the
// optional source_action_keys whitelist + amount cap + time window.
//
// Design choices:
//   - Snapshot-honest: delegation resolution happens at RecordDecision
//     time, not at workflow creation. A delegation revoked after a
//     workflow starts but before D signs WILL block D's signature.
//     This is intentional — delegations are runtime authority, not
//     workflow-state.
//   - No cycle detection. Slice 1 allows A→B and B→A simultaneously;
//     the runtime joins one hop deep, so chains of delegation
//     (A→B→C) don't transitively grant C the authority of A.
//     Multi-hop is Slice 2 work.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrDelegationNotFound — get / revoke against nonexistent row.
var ErrDelegationNotFound = errors.New("authority: delegation not found")

// ErrDelegationTerminal — attempted operation on a revoked delegation.
var ErrDelegationTerminal = errors.New("authority: delegation is revoked")

// DelegationStatus values.
const (
	DelegationStatusActive  = "active"
	DelegationStatusRevoked = "revoked"
)

// Delegation is one _doa_delegations row.
type Delegation struct {
	ID                uuid.UUID  `json:"id"`
	DelegatorID       uuid.UUID  `json:"delegator_id"`
	DelegateeID       uuid.UUID  `json:"delegatee_id"`
	TenantID          *uuid.UUID `json:"tenant_id,omitempty"`
	SourceActionKeys  []string   `json:"source_action_keys,omitempty"`
	MaxAmount         *int64     `json:"max_amount,omitempty"`
	EffectiveFrom     time.Time  `json:"effective_from"`
	EffectiveTo       *time.Time `json:"effective_to,omitempty"`
	Status            string     `json:"status"`
	RevokedReason     string     `json:"revoked_reason,omitempty"`
	RevokedBy         *uuid.UUID `json:"revoked_by,omitempty"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	CreatedBy         *uuid.UUID `json:"created_by,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Notes             string     `json:"notes,omitempty"`
}

// DelegationCreateInput is the create-time payload.
type DelegationCreateInput struct {
	DelegatorID      uuid.UUID
	DelegateeID      uuid.UUID
	TenantID         *uuid.UUID
	SourceActionKeys []string
	MaxAmount        *int64
	EffectiveFrom    *time.Time // nil → now()
	EffectiveTo      *time.Time
	Notes            string
	CreatedBy        *uuid.UUID
}

// CreateDelegation inserts a new active delegation. Validation:
//   - Delegator ≠ Delegatee (DB CHECK also enforces; we surface a
//     clean error message here).
//   - EffectiveTo > EffectiveFrom when both set.
func (s *Store) CreateDelegation(ctx context.Context, in DelegationCreateInput) (*Delegation, error) {
	if in.DelegatorID == in.DelegateeID {
		return nil, fmt.Errorf("authority: delegator must differ from delegatee")
	}
	if in.DelegatorID == uuid.Nil || in.DelegateeID == uuid.Nil {
		return nil, fmt.Errorf("authority: delegator_id and delegatee_id are required")
	}
	if in.EffectiveFrom != nil && in.EffectiveTo != nil &&
		!in.EffectiveTo.After(*in.EffectiveFrom) {
		return nil, fmt.Errorf("authority: effective_to must be after effective_from")
	}

	const stmt = `
		INSERT INTO _doa_delegations (
			delegator_id, delegatee_id, tenant_id,
			source_action_keys, max_amount,
			effective_from, effective_to,
			notes, created_by
		) VALUES (
			$1, $2, $3,
			$4, $5,
			COALESCE($6, now()), $7,
			$8, $9
		)
		RETURNING id, effective_from, status, created_at, updated_at`

	d := &Delegation{
		DelegatorID:      in.DelegatorID,
		DelegateeID:      in.DelegateeID,
		TenantID:         in.TenantID,
		SourceActionKeys: in.SourceActionKeys,
		MaxAmount:        in.MaxAmount,
		EffectiveTo:      in.EffectiveTo,
		Notes:            in.Notes,
		CreatedBy:        in.CreatedBy,
	}
	err := s.pool.QueryRow(ctx, stmt,
		in.DelegatorID, in.DelegateeID, in.TenantID,
		stringSliceOrNull(in.SourceActionKeys), in.MaxAmount,
		in.EffectiveFrom, in.EffectiveTo,
		nullString(in.Notes), in.CreatedBy,
	).Scan(&d.ID, &d.EffectiveFrom, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("authority: insert delegation: %w", err)
	}
	return d, nil
}

// GetDelegation reads a delegation by id.
func (s *Store) GetDelegation(ctx context.Context, id uuid.UUID) (*Delegation, error) {
	const stmt = `
		SELECT id, delegator_id, delegatee_id, tenant_id,
		       source_action_keys, max_amount,
		       effective_from, effective_to,
		       status, COALESCE(revoked_reason, ''),
		       revoked_by, revoked_at,
		       created_at, created_by, updated_at,
		       COALESCE(notes, '')
		FROM _doa_delegations
		WHERE id = $1`

	d := &Delegation{}
	err := s.pool.QueryRow(ctx, stmt, id).Scan(
		&d.ID, &d.DelegatorID, &d.DelegateeID, &d.TenantID,
		&d.SourceActionKeys, &d.MaxAmount,
		&d.EffectiveFrom, &d.EffectiveTo,
		&d.Status, &d.RevokedReason,
		&d.RevokedBy, &d.RevokedAt,
		&d.CreatedAt, &d.CreatedBy, &d.UpdatedAt,
		&d.Notes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDelegationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authority: get delegation: %w", err)
	}
	return d, nil
}

// RevokeDelegation transitions an active delegation to revoked.
// Reason is required (mirror of matrix revoke contract).
func (s *Store) RevokeDelegation(ctx context.Context, id uuid.UUID,
	revokedBy uuid.UUID, reason string) error {
	if reason == "" {
		return fmt.Errorf("authority: revoke delegation: reason is required")
	}
	const stmt = `
		UPDATE _doa_delegations
		SET status = 'revoked',
		    revoked_by = $2,
		    revoked_reason = $3,
		    revoked_at = now(),
		    updated_at = now()
		WHERE id = $1 AND status = 'active'`
	tag, err := s.pool.Exec(ctx, stmt, id, revokedBy, reason)
	if err != nil {
		return fmt.Errorf("authority: revoke delegation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var status string
		probe := s.pool.QueryRow(ctx,
			`SELECT status FROM _doa_delegations WHERE id = $1`, id,
		).Scan(&status)
		if errors.Is(probe, pgx.ErrNoRows) {
			return ErrDelegationNotFound
		}
		return ErrDelegationTerminal
	}
	return nil
}

// DelegationFilter is the list-time query shape.
type DelegationFilter struct {
	DelegatorID *uuid.UUID
	DelegateeID *uuid.UUID
	TenantID    *uuid.UUID
	Status      string // empty = any
}

// ListDelegations returns matching rows. Ordering: newest first.
func (s *Store) ListDelegations(ctx context.Context, f DelegationFilter) ([]Delegation, error) {
	const stmt = `
		SELECT id, delegator_id, delegatee_id, tenant_id,
		       source_action_keys, max_amount,
		       effective_from, effective_to,
		       status, COALESCE(revoked_reason, ''),
		       revoked_by, revoked_at,
		       created_at, created_by, updated_at,
		       COALESCE(notes, '')
		FROM _doa_delegations
		WHERE ($1::uuid IS NULL OR delegator_id = $1)
		  AND ($2::uuid IS NULL OR delegatee_id = $2)
		  AND ($3::uuid IS NULL OR tenant_id = $3)
		  AND ($4 = '' OR status = $4)
		ORDER BY created_at DESC
		LIMIT 200`

	rows, err := s.pool.Query(ctx, stmt,
		f.DelegatorID, f.DelegateeID, f.TenantID, f.Status)
	if err != nil {
		return nil, fmt.Errorf("authority: list delegations: %w", err)
	}
	defer rows.Close()

	out := make([]Delegation, 0, 16)
	for rows.Next() {
		var d Delegation
		err := rows.Scan(
			&d.ID, &d.DelegatorID, &d.DelegateeID, &d.TenantID,
			&d.SourceActionKeys, &d.MaxAmount,
			&d.EffectiveFrom, &d.EffectiveTo,
			&d.Status, &d.RevokedReason,
			&d.RevokedBy, &d.RevokedAt,
			&d.CreatedAt, &d.CreatedBy, &d.UpdatedAt,
			&d.Notes,
		)
		if err != nil {
			return nil, fmt.Errorf("authority: scan delegation: %w", err)
		}
		out = append(out, d)
	}
	return out, nil
}

// activeDelegatorsFor returns the set of delegator IDs whose authority
// the given user effectively wields RIGHT NOW for the given context.
//
// Filter: status='active' + effective_from <= now() + (effective_to IS
// NULL OR effective_to > now()) + tenant match + action_key match +
// amount cap.
//
// Used by ResolveApprovers to expand qualified pools transparently.
func (s *Store) activeDelegatorsFor(ctx context.Context, userID uuid.UUID,
	tenantID *uuid.UUID, actionKey string, amount *int64) ([]uuid.UUID, error) {
	const stmt = `
		SELECT delegator_id
		FROM _doa_delegations
		WHERE delegatee_id = $1
		  AND status = 'active'
		  AND effective_from <= now()
		  AND (effective_to IS NULL OR effective_to > now())
		  AND ($2::uuid IS NULL OR tenant_id = $2 OR tenant_id IS NULL)
		  AND (source_action_keys IS NULL OR $3 = ANY(source_action_keys))
		  AND ($4::bigint IS NULL OR max_amount IS NULL OR max_amount >= $4)`

	rows, err := s.pool.Query(ctx, stmt, userID, tenantID, actionKey, amount)
	if err != nil {
		return nil, fmt.Errorf("authority: active delegators: %w", err)
	}
	defer rows.Close()

	out := make([]uuid.UUID, 0, 4)
	for rows.Next() {
		var uid uuid.UUID
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("authority: scan delegator: %w", err)
		}
		out = append(out, uid)
	}
	return out, nil
}

// stringSliceOrNull returns nil for empty slices so the SQL NULL path
// fires (whitelist column NULL = "all action keys").
func stringSliceOrNull(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s
}
