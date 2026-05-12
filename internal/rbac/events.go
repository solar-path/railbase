package rbac

// Eventbus topics published by Store mutations.
//
// Why these exist:
//
//	The resolver cache (cache.go) maps (collection, recordID, tenantID)
//	→ *Resolved. A 5-minute TTL bounds staleness, but operators who
//	just granted/revoked a role expect propagation in seconds, not
//	minutes. The cache subscribes to these topics (see
//	SubscribeInvalidation in cache.go) and purges on receipt so the
//	next request re-runs the underlying Resolve walk.
//
// Layout note: every topic is published ASYNC via Bus.Publish — these
// are observation hooks, not invariant-preserving. The cache reacts
// best-effort; if the bus drops an event under load, the next TTL
// expiry still bounds staleness.
//
// Payloads are by value: subscribers observe, they do not mutate.
const (
	// TopicRoleGranted fires after Store.Grant succeeds (an action key
	// was attached to a role). Payload: RoleEvent with Role=role-id-
	// string, Action=action-key-string. Tenant/UserID empty.
	TopicRoleGranted = "rbac.role_granted"

	// TopicRoleRevoked fires after Store.Revoke succeeds (an action key
	// was detached from a role). Payload: RoleEvent — same shape as
	// TopicRoleGranted.
	TopicRoleRevoked = "rbac.role_revoked"

	// TopicRoleAssigned fires after Store.Assign succeeds (a role was
	// assigned to a user, optionally scoped to a tenant). Payload:
	// RoleEvent with Role=role-id-string, Actor=collection-name,
	// UserID=record-id-string, Tenant=tenant-id-string (empty for
	// site-scope).
	TopicRoleAssigned = "rbac.role_assigned"

	// TopicRoleUnassigned fires after Store.Unassign succeeds.
	// Payload: RoleEvent — same shape as TopicRoleAssigned.
	TopicRoleUnassigned = "rbac.role_unassigned"

	// TopicRoleDeleted fires after Store.DeleteRole succeeds (a custom
	// role row was removed, which cascades to _user_roles +
	// _role_actions via FK). Payload: RoleEvent with Role=role-id-
	// string.
	TopicRoleDeleted = "rbac.role_deleted"
)

// RoleEvent is the payload for every rbac.role_* topic. Fields are
// optional — only the ones meaningful for a given topic are populated:
//
//	role_granted / role_revoked    Role, Action
//	role_assigned / role_unassigned Role, Actor, UserID, Tenant
//	role_deleted                   Role
//
// All IDs are rendered as their string form (UUID.String() / scope name)
// so cross-process subscribers via pgbridge don't need to share Go
// types. The cache invalidation handler doesn't inspect payload fields
// at all — it purges coarsely — but downstream observers (audit log,
// admin UI live tail) consume the structured fields.
type RoleEvent struct {
	// Role is the role identifier (UUID string for catalog rows).
	Role string
	// Action is the action key (for grant/revoke topics).
	Action string
	// Actor is the user collection name (for assign/unassign topics).
	Actor string
	// UserID is the user record id (for assign/unassign topics).
	UserID string
	// Tenant is the tenant scope (UUID string), empty for site-scoped
	// events.
	Tenant string
}
