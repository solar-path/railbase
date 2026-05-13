package scim

// SCIM group → RBAC role reconciliation (v1.7.51 follow-up).
//
// Migration 0026 declares `_scim_group_role_map` (scim_group_id,
// role_id) but the initial SCIM PATCH handler only wrote to
// `_scim_group_members`; the RBAC `_user_roles` table was never
// touched. This file closes that gap.
//
// Reconciliation contract on every PATCH that mutates Group
// membership:
//
//	add member X to Group G:
//	    for each role mapped to G:
//	        if X doesn't already hold the role: Assign it
//
//	remove member X from Group G:
//	    for each role mapped to G:
//	        if X is NOT a member of any OTHER Group that maps to the
//	        same role: Unassign it
//
// The "other groups mapping to the same role" check exists because
// roles can be reached via MANY groups (an "engineering" user may
// inherit `developer` role via both `eng-backend` and `eng-platform`
// groups). Removing from one group musn't drop a role still owed
// via another.
//
// Tenant scope: v1.7.51 only supports SITE-SCOPED grants — the same
// rule the SAML group-mapping handler uses (v1.7.50.1d). Per-tenant
// SCIM group → role mapping is a future slice (would require a
// `tenant_id` column on `_scim_group_role_map`).
//
// Idempotency: `rbac.Store.Assign` is already idempotent (ON CONFLICT
// DO NOTHING). `Unassign` is too. So this helper is safe to call on
// every PATCH without diffing — but we DO compute the "other groups"
// check so we don't drop legitimately-granted roles.
//
// Bus events: each grant/revoke publishes TopicRoleAssigned /
// TopicRoleUnassigned via the existing rbac.Store wiring → resolver
// cache flushes → next signin sees the new role set.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/rbac"
)

// reconcileGroupGrants applies role-mapping deltas after a PATCH that
// added or removed `userID` from `groupID`. `added` = true means the
// user was just added; false means removed.
//
// We pass the RBAC store + pool separately because the SCIM handler
// only has the pool in Deps today. Adding the rbac.Store to scimapi.Deps
// is a wider change; the per-call signature is clearer.
//
// Errors: best-effort. A single failed grant doesn't roll back the
// PATCH — the operator can re-run the PATCH or the next signin's
// resolver path will catch up via the existing cache invalidation.
// Errors are logged via the returned slice so callers can audit.
func reconcileGroupGrants(
	ctx context.Context,
	pool *pgxpool.Pool,
	rbacStore *rbac.Store,
	collection string,
	groupID uuid.UUID,
	userID uuid.UUID,
	added bool,
) []error {
	if rbacStore == nil {
		// No RBAC store wired (tests, minimal deployments). Treat as
		// "no-op" — same contract as the SAML group-mapping handler.
		return nil
	}

	// Roles mapped to THIS group.
	roleIDs, err := mappedRolesForGroup(ctx, pool, groupID)
	if err != nil {
		return []error{fmt.Errorf("load mapped roles: %w", err)}
	}
	if len(roleIDs) == 0 {
		return nil // nothing to reconcile
	}

	var errs []error

	if added {
		// ADD path: grant each mapped role. Idempotent — already-held
		// roles are silently skipped by the store.
		for _, rid := range roleIDs {
			if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
				CollectionName: collection,
				RecordID:       userID,
				RoleID:         rid,
				// No GrantedBy: this is a SCIM-driven system grant,
				// not an admin action. The audit trail traces back to
				// the IdP via the SCIM token used.
			}); err != nil {
				errs = append(errs, fmt.Errorf("assign role %s: %w", rid, err))
			}
		}
		return errs
	}

	// REMOVE path: for each role that THIS group maps to, drop it
	// ONLY if no other group the user is still a member of maps to
	// the same role.
	for _, rid := range roleIDs {
		stillOwed, err := userHasMappedRoleViaOtherGroup(
			ctx, pool, userID, collection, rid, groupID)
		if err != nil {
			errs = append(errs, fmt.Errorf("check residual mapping for role %s: %w", rid, err))
			continue
		}
		if stillOwed {
			continue // user is in another group that grants this role; leave the grant
		}
		if err := rbacStore.Unassign(ctx, collection, userID, rid, nil); err != nil {
			errs = append(errs, fmt.Errorf("unassign role %s: %w", rid, err))
		}
	}
	return errs
}

// mappedRolesForGroup reads every role_id mapped to a SCIM group.
func mappedRolesForGroup(ctx context.Context, pool *pgxpool.Pool, groupID uuid.UUID) ([]uuid.UUID, error) {
	const q = `SELECT role_id FROM _scim_group_role_map WHERE scim_group_id = $1`
	rows, err := pool.Query(ctx, q, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var rid uuid.UUID
		if err := rows.Scan(&rid); err != nil {
			return nil, err
		}
		out = append(out, rid)
	}
	return out, rows.Err()
}

// userHasMappedRoleViaOtherGroup checks whether the user is a member
// of ANY OTHER group (excluding `excludeGroupID`) that maps to
// `roleID`. Used by the REMOVE path to avoid dropping a role the user
// still inherits via a sibling group.
//
// The query is a single round-trip:
//   - join `_scim_group_members` (user's current group memberships)
//   - join `_scim_group_role_map` filtered to `roleID`
//   - exclude `excludeGroupID`
//   - LIMIT 1 — we only need to know if ANY row exists.
func userHasMappedRoleViaOtherGroup(
	ctx context.Context,
	pool *pgxpool.Pool,
	userID uuid.UUID,
	collection string,
	roleID uuid.UUID,
	excludeGroupID uuid.UUID,
) (bool, error) {
	const q = `
        SELECT 1
          FROM _scim_group_members m
          JOIN _scim_group_role_map r ON r.scim_group_id = m.group_id
         WHERE m.user_id         = $1
           AND m.user_collection = $2
           AND r.role_id         = $3
           AND m.group_id        <> $4
         LIMIT 1
    `
	var exists int
	err := pool.QueryRow(ctx, q, userID, collection, roleID, excludeGroupID).Scan(&exists)
	if err != nil {
		// pgx.ErrNoRows = no other group grants the role → user is no
		// longer owed it.
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return exists == 1, nil
}
