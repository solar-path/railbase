package scim

import (
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/rbac"
)

// Deps bundles everything the SCIM router needs to mount.
type Deps struct {
	Pool   *pgxpool.Pool
	Tokens *scimauth.TokenStore
	// v1.7.51 follow-up — RBAC store. When non-nil, SCIM Group
	// mutations reconcile `_user_roles` via `_scim_group_role_map`:
	// adding a user to a group grants every mapped role; removing
	// drops roles no longer reachable via the user's remaining group
	// memberships. Nil-tolerant: tests / minimal deployments leave it
	// unset and SCIM behaves as v1.7.51 release shipped (membership-
	// only, no automatic role grants).
	RBAC *rbac.Store
}

// Mount attaches /scim/v2/* on the given router. All Users + Groups
// routes are gated by the bearer-token middleware; discovery routes
// (ServiceProviderConfig / ResourceTypes / Schemas) are PUBLIC per
// RFC 7644 §4 + §5 — the IdP needs to read them BEFORE it has a
// token.
//
// Returns silently if Deps.Tokens is nil so callers can wire the
// mount unconditionally — a `scim.enabled=false` install just leaves
// every route 503-ing via the middleware.
func Mount(r chi.Router, d *Deps) {
	if d == nil {
		return
	}
	r.Route("/scim/v2", func(r chi.Router) {
		// Public discovery endpoints — IdP reads these BEFORE auth.
		r.Get("/ServiceProviderConfig", writeServiceProviderConfig)
		r.Get("/ResourceTypes", writeResourceTypes)
		r.Get("/Schemas", writeSchemas)

		// Auth-gated resource endpoints.
		r.Group(func(r chi.Router) {
			r.Use(AuthMiddleware(d.Tokens))
			MountUsers(r, &UsersDeps{Pool: d.Pool})
			MountGroups(r, &GroupsDeps{Pool: d.Pool, RBAC: d.RBAC})
		})
	})
}
