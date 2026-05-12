// Package rls drives PostgreSQL Row-Level Security from Go.
//
// The contract: every authenticated request acquires a connection,
// calls Apply at the start of its tx, and lets `set_config(..., true)`
// scope the settings to the tx. RLS policies on tenant-scoped tables
// read these settings via current_setting('railbase.tenant', true) etc.
//
// See docs/04-identity.md ("Tenant enforcement: PostgreSQL Row-Level
// Security как основа") and docs/03-data-layer.md ("Multi-tenancy:
// PostgreSQL Row-Level Security") for the policy DDL emitted by the
// schema migration generator.
package rls

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Role categorizes the calling actor for policy decisions.
//   - RoleAnonymous: unauthenticated public-route caller
//   - RoleAppUser:   authenticated end-user (subject to tenant policies)
//   - RoleAppAdmin:  site admin or system tooling — policies grant bypass
//
// Postgres-side policies look like:
//
//	USING (
//	    current_setting('railbase.role', true) = 'app_admin'
//	    OR tenant_id = current_setting('railbase.tenant', true)::uuid
//	)
type Role string

const (
	RoleAnonymous Role = "anonymous"
	RoleAppUser   Role = "app_user"
	RoleAppAdmin  Role = "app_admin"
)

// Context carries the per-request RLS context that gets pushed into
// session-local Postgres settings. Empty UserID/TenantID become empty
// strings on the SQL side; current_setting with the missing_ok=true
// second argument returns NULL for them — policies must treat NULL as
// "no access".
type Context struct {
	UserID   string
	TenantID string
	Role     Role
}

// Apply pushes ctx into the active transaction's session-local settings.
// Caller MUST hold a tx (not a raw *pgxpool.Conn) — the third argument
// to set_config is is_local=true, which scopes settings to the tx.
//
// Settings written:
//
//	railbase.user   — UUID of the acting user, or "" for anonymous
//	railbase.tenant — UUID of the active tenant, or "" for site-scope
//	railbase.role   — one of the Role constants above
func Apply(ctx context.Context, tx pgx.Tx, rc Context) error {
	if rc.Role == "" {
		rc.Role = RoleAnonymous
	}
	const q = `
		SELECT set_config('railbase.user',   $1, true),
		       set_config('railbase.tenant', $2, true),
		       set_config('railbase.role',   $3, true)
	`
	if _, err := tx.Exec(ctx, q, rc.UserID, rc.TenantID, string(rc.Role)); err != nil {
		return fmt.Errorf("rls: apply settings: %w", err)
	}
	return nil
}
