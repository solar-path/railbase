package authority

// Matrix selection + approver resolution engines.
//
// These are read-only helpers used by the DoA gate middleware
// (when deciding which matrix applies to a given mutation) and the
// workflow runtime (when expanding approvers per level into the
// set of qualified user UUIDs).
//
// Slice 0 limits:
//   - Approver resolution supports `role` + `user` types only.
//     `position` + `department_head` would need org-chart primitive
//     (см. docs/26-org-structure-audit.md), deferred to v2.x.
//   - Matrix selector evaluates amount range + tenant + key + status
//     + effective window. condition_expr column is read but NOT
//     evaluated в Slice 0 (opt-in feature, deferred).
//   - Delegation resolution NOT implemented в Slice 0 (`_doa_delegations`
//     table doesn't exist yet — Slice 1).

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SelectFilter is the input to SelectActiveMatrix.
type SelectFilter struct {
	// Key is the matrix key (action_key) — required.
	// Example: "articles.publish".
	Key string

	// TenantID scopes selection. If non-nil, matrices with this
	// tenant_id are preferred over site-scope (tenant_id IS NULL)
	// rows for the same key. If nil, only site-scope rows match.
	TenantID *uuid.UUID

	// Amount is the materiality value from the protected row column
	// named by AuthorityConfig.AmountField. Nil = amount-agnostic
	// selection (matches matrices with min/max_amount unset or any
	// amount in window).
	Amount *int64

	// Currency must match the matrix's currency column when set on
	// both sides. Empty = no currency filter (matches matrices with
	// currency NULL).
	Currency string
}

// SelectActiveMatrix returns the single best-match approved matrix
// for the given filter, or ErrMatrixNotFound if none qualifies.
//
// Selection algorithm (см. docs/26-authority.md):
//
//  1. WHERE status='approved'
//  2. AND effective_from <= now() AND (effective_to IS NULL OR effective_to > now())
//  3. AND (tenant_id = $tenant OR tenant_id IS NULL)
//  4. AND amount in [min_amount, max_amount] (when both Amount and matrix bounds set)
//  5. AND (currency = $currency OR matrix.currency IS NULL)
//  6. ORDER BY tenant_id NULLS LAST, min_amount DESC NULLS LAST
//  7. LIMIT 1
//
// Ordering prefers tenant-specific over site-scope; among same-scope
// matches, the one with the highest min_amount floor wins (more
// specific window). The fully-populated matrix (levels + approvers)
// is returned.
func (s *Store) SelectActiveMatrix(ctx context.Context, f SelectFilter) (*Matrix, error) {
	if f.Key == "" {
		return nil, fmt.Errorf("authority: SelectActiveMatrix: key is required")
	}

	const stmt = `
		SELECT id
		FROM _doa_matrices
		WHERE key = $1
		  AND status = 'approved'
		  AND (effective_from IS NULL OR effective_from <= now())
		  AND (effective_to IS NULL OR effective_to > now())
		  AND ($2::uuid IS NULL OR tenant_id = $2 OR tenant_id IS NULL)
		  AND ($3::bigint IS NULL OR
		       ((min_amount IS NULL OR min_amount <= $3)
		        AND (max_amount IS NULL OR $3 <= max_amount)))
		  AND ($4 = '' OR currency IS NULL OR currency = $4)
		ORDER BY
		    CASE WHEN tenant_id IS NULL THEN 1 ELSE 0 END,   -- prefer non-NULL tenant
		    min_amount DESC NULLS LAST,                       -- prefer higher floor (more specific)
		    version DESC
		LIMIT 1`

	var id uuid.UUID
	err := s.pool.QueryRow(ctx, stmt, f.Key, f.TenantID, f.Amount, f.Currency).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMatrixNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authority: select active matrix: %w", err)
	}

	// Load full body so caller can immediately expand approvers per
	// level when creating a workflow.
	return s.GetMatrix(ctx, id)
}

// ResolveApprovers expands all approvers on the given level into the
// concrete set of user UUIDs that qualify. Returns deduplicated user
// IDs in stable order (sorted) for snapshot-honest workflow state.
//
// Slice 0 implementation:
//   - approver_type='user' → adds the parsed UUID directly
//   - approver_type='role' → queries _user_roles for users with the
//     given role in the workflow's tenant scope
//   - approver_type='position' or 'department_head' → returns error
//     (admin REST rejects upstream; defense-in-depth here)
//
// The tenantID parameter scopes role lookups. Pass nil for site-scope
// matrices (role assignments where tenant_id IS NULL count).
func (s *Store) ResolveApprovers(ctx context.Context, level *Level,
	tenantID *uuid.UUID) ([]uuid.UUID, error) {
	if level == nil {
		return nil, fmt.Errorf("authority: ResolveApprovers: nil level")
	}

	seen := make(map[uuid.UUID]struct{}, 16)
	out := make([]uuid.UUID, 0, 16)

	for _, ap := range level.Approvers {
		switch ap.ApproverType {
		case ApproverTypeUser:
			uid, err := uuid.Parse(ap.ApproverRef)
			if err != nil {
				return nil, fmt.Errorf(
					"authority: invalid user UUID %q on level %d approver %s: %w",
					ap.ApproverRef, level.LevelN, ap.ID, err)
			}
			if _, ok := seen[uid]; !ok {
				seen[uid] = struct{}{}
				out = append(out, uid)
			}

		case ApproverTypeRole:
			roleUsers, err := s.usersWithRole(ctx, ap.ApproverRef, tenantID)
			if err != nil {
				return nil, fmt.Errorf(
					"authority: resolve role %q on level %d: %w",
					ap.ApproverRef, level.LevelN, err)
			}
			for _, uid := range roleUsers {
				if _, ok := seen[uid]; !ok {
					seen[uid] = struct{}{}
					out = append(out, uid)
				}
			}

		case ApproverTypePosition, ApproverTypeDepartmentHead:
			return nil, ErrUnsupportedApproverType

		default:
			return nil, fmt.Errorf(
				"authority: unknown approver type %q on level %d",
				ap.ApproverType, level.LevelN)
		}
	}

	// Stable ordering — uuid.UUID has no Less but bytes compare well.
	// Sort for snapshot honesty (same inputs → same outputs).
	sortUUIDs(out)
	return out, nil
}

// ResolveApproversWithDelegation is ResolveApprovers + delegation
// expansion. For every user U in the base pool, this method queries
// _doa_delegations for active rows where delegator_id=U + the supplied
// context filter, and adds each delegatee to the pool.
//
// Slice 1 hardening of Slice 0 prototype:
//   - One join hop only (no transitive A→B→C grants C anything).
//   - Tenant scope: site delegations apply to tenant workflows; tenant
//     delegations apply only to their tenant.
//   - source_action_keys: NULL = applies to all action_keys; non-NULL =
//     applies only when actionKey is in the array.
//   - max_amount: NULL = no cap; non-NULL = applies only when the
//     workflow's amount is ≤ cap.
//
// Caller responsibility: pass the workflow's actionKey/amount/tenant
// faithfully — incorrect inputs widen the pool incorrectly.
func (s *Store) ResolveApproversWithDelegation(ctx context.Context, level *Level,
	tenantID *uuid.UUID, actionKey string, amount *int64) ([]uuid.UUID, error) {
	base, err := s.ResolveApprovers(ctx, level, tenantID)
	if err != nil {
		return nil, err
	}
	if len(base) == 0 {
		return base, nil
	}

	// For each user in base, find delegatees who hold their authority.
	// Single query — IN-list join is fine for typical pool sizes.
	const stmt = `
		SELECT DISTINCT delegatee_id
		FROM _doa_delegations
		WHERE delegator_id = ANY($1)
		  AND status = 'active'
		  AND effective_from <= now()
		  AND (effective_to IS NULL OR effective_to > now())
		  AND ($2::uuid IS NULL OR tenant_id = $2 OR tenant_id IS NULL)
		  AND (source_action_keys IS NULL OR $3 = ANY(source_action_keys))
		  AND ($4::bigint IS NULL OR max_amount IS NULL OR max_amount >= $4)`

	rows, err := s.pool.Query(ctx, stmt, base, tenantID, actionKey, amount)
	if err != nil {
		return nil, fmt.Errorf("authority: expand delegations: %w", err)
	}
	defer rows.Close()

	seen := make(map[uuid.UUID]struct{}, len(base))
	for _, uid := range base {
		seen[uid] = struct{}{}
	}
	for rows.Next() {
		var uid uuid.UUID
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("authority: scan delegatee: %w", err)
		}
		if _, ok := seen[uid]; !ok {
			seen[uid] = struct{}{}
			base = append(base, uid)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authority: iterate delegatees: %w", err)
	}
	sortUUIDs(base)
	return base, nil
}

// usersWithRole queries _user_roles + _roles to expand a role key
// into the set of user IDs that hold that role within the given
// tenant scope.
//
// _user_roles schema (see internal/db/migrate/sys/0012_rbac.up.sql):
//   (collection_name, record_id, role_id, tenant_id?)
// _roles schema:
//   (id, name, scope)
//
// Site-scope (tenantID nil) matches _user_roles rows with
// tenant_id IS NULL OR tenant_id = ANY (no further filter — both
// site role assignments and tenant role assignments are considered;
// the caller decides scope by the matrix's tenant_id).
func (s *Store) usersWithRole(ctx context.Context, roleName string,
	tenantID *uuid.UUID) ([]uuid.UUID, error) {
	if roleName == "" {
		return nil, fmt.Errorf("empty role name")
	}

	// Note: _user_roles.record_id stores the user UUID as TEXT in
	// the v1.7.x schema (it generalises over multiple auth collections);
	// we cast to UUID at scan time.
	const stmt = `
		SELECT DISTINCT ur.record_id
		FROM _user_roles ur
		JOIN _roles r ON r.id = ur.role_id
		WHERE r.name = $1
		  AND ($2::uuid IS NULL OR ur.tenant_id = $2 OR ur.tenant_id IS NULL)`

	rows, err := s.pool.Query(ctx, stmt, roleName, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query _user_roles: %w", err)
	}
	defer rows.Close()

	out := make([]uuid.UUID, 0, 8)
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			return nil, fmt.Errorf("scan _user_roles row: %w", err)
		}
		uid, err := uuid.Parse(rec)
		if err != nil {
			// Skip malformed record_id (shouldn't happen with proper auth flow).
			continue
		}
		out = append(out, uid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate _user_roles: %w", err)
	}
	return out, nil
}

// sortUUIDs sorts a slice of UUIDs in-place by byte representation.
// Stable across runs — used for snapshot-honest workflow decisions.
func sortUUIDs(s []uuid.UUID) {
	// Insertion sort (good for small N which is the expected case
	// for approver pools — usually <20 actors).
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && lessUUID(s[j], s[j-1]) {
			s[j], s[j-1] = s[j-1], s[j]
			j--
		}
	}
}

func lessUUID(a, b uuid.UUID) bool {
	for i := 0; i < 16; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
