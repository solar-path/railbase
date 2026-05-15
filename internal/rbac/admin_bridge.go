package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// AdminCollectionName is the synthetic collection name under which
// `_admins` rows are recorded in `_user_roles`. The string is opaque
// to RBAC — Store treats collection_name as a label — but every
// adminapi callsite must agree on this constant so the principal
// extractor and the assignment writer point at the same rows.
//
// Picking the same name as the table avoids ambiguity in audit logs
// and SQL inspections; a `\_` underscore prefix means it can never
// collide with a user-defined collection name (which the schema
// validator forbids from starting with `_`).
const AdminCollectionName = "_admins"

// AssignSystemAdmin grants the site `system_admin` role to the given
// admin row. Idempotent: re-running for an admin who already holds it
// is a no-op (see Store.Assign).
//
// Use cases:
//
//   - Bootstrap: after Admins.Create succeeds in bootstrapCreateHandler.
//   - CLI `railbase admin create` for the same reason.
//   - Migrations: 0029_rbac_admin_bridge backfills every existing row.
//
// Errors surface only on infrastructure failure (lookup, insert,
// missing role). Callers should LOG-AND-CONTINUE rather than fail
// admin creation: a missing assignment is recoverable through the UI,
// a failed admin create isn't.
func AssignSystemAdmin(ctx context.Context, store *Store, adminID uuid.UUID) error {
	if store == nil {
		return errors.New("rbac: AssignSystemAdmin: store is nil")
	}
	if adminID == uuid.Nil {
		return errors.New("rbac: AssignSystemAdmin: admin id is required")
	}
	role, err := store.GetRole(ctx, "system_admin", ScopeSite)
	if err != nil {
		return fmt.Errorf("rbac: lookup system_admin role: %w", err)
	}
	if _, err := store.Assign(ctx, AssignInput{
		CollectionName: AdminCollectionName,
		RecordID:       adminID,
		RoleID:         role.ID,
		// site-scoped → TenantID nil
	}); err != nil {
		return fmt.Errorf("rbac: assign system_admin to %s: %w", adminID, err)
	}
	return nil
}
