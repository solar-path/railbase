// Package actionkeys is the compile-time catalog of action keys
// Railbase's core uses for RBAC checks.
//
// Action keys are opaque strings ("posts.list", "audit.verify") that
// flow through middleware and grant rows. Defining them as typed
// constants here gives:
//
//   - IDE completion when calling rbac.Require(ctx, ActionPostsList)
//   - refactor-safety: rename a constant and the compiler tells you
//     every call site
//   - documentation: a single grep shows every check site for a given
//     action across the codebase
//
// Plugins / user code can MINT their own action keys without
// registering them here — the DB schema treats action_key as an
// opaque string. Convention: `<scope>.<resource>.<verb>` lowercase
// with dots. Examples:
//
//	"posts.list"            (collection-level)
//	"tenant.members.invite" (tenant-scoped action)
//	"admin.backup"          (system action)
package actionkeys

// ActionKey is the type used everywhere checks happen. Defined as a
// distinct type (not string alias) so a stray "posts.list" literal in
// place of an action constant trips the compiler.
type ActionKey string

// Catalog the core surface ships with. Mirrors the seed in
// 0013_rbac_seed.up.sql — see that file for who gets what by default.
const (
	// Auth surface (mostly guest-allowed for the unauthenticated path).
	AuthSignin        ActionKey = "auth.signin"
	AuthSignup        ActionKey = "auth.signup"
	AuthSignout       ActionKey = "auth.signout"
	AuthRefresh       ActionKey = "auth.refresh"
	AuthMe            ActionKey = "auth.me"
	AuthPasswordReset ActionKey = "auth.password_reset"

	// MFA surface.
	TOTPEnroll      ActionKey = "totp.enroll"
	TOTPDisable     ActionKey = "totp.disable"
	WebAuthnEnroll  ActionKey = "webauthn.enroll"
	WebAuthnLogin   ActionKey = "webauthn.login"
	WebAuthnDelete  ActionKey = "webauthn.delete"

	// Admin surface.
	AdminsList    ActionKey = "admins.list"
	AdminsCreate  ActionKey = "admins.create"
	AdminsDelete  ActionKey = "admins.delete"
	UsersList     ActionKey = "users.list"
	UsersCreate   ActionKey = "users.create"
	UsersUpdate   ActionKey = "users.update"
	UsersDelete   ActionKey = "users.delete"
	TenantsList   ActionKey = "tenants.list"
	TenantsCreate ActionKey = "tenants.create"
	TenantsUpdate ActionKey = "tenants.update"
	TenantsDelete ActionKey = "tenants.delete"
	AuditRead     ActionKey = "audit.read"
	AuditVerify   ActionKey = "audit.verify"
	SettingsRead  ActionKey = "settings.read"
	SettingsWrite ActionKey = "settings.write"
	SchemaRead    ActionKey = "schema.read"
	MailerTest    ActionKey = "mailer.test"
	RBACRead      ActionKey = "rbac.read"
	RBACWrite     ActionKey = "rbac.write"

	// Tenant-scoped actions (granted via tenant roles, not site roles).
	TenantMembersList   ActionKey = "tenant.members.list"
	TenantMembersInvite ActionKey = "tenant.members.invite"
	TenantMembersRemove ActionKey = "tenant.members.remove"
	TenantRecordsList   ActionKey = "tenant.records.list"
	TenantRecordsRead   ActionKey = "tenant.records.read"
	TenantRecordsCreate ActionKey = "tenant.records.create"
	TenantRecordsUpdate ActionKey = "tenant.records.update"
	TenantRecordsDelete ActionKey = "tenant.records.delete"
	// _own variants encode the "user can only mutate their own rows"
	// pattern. The check is "do you have action.<verb> OR (action.<verb>_own AND row.owner == @me)";
	// implementing that intersection sits inside the filter layer (deferred —
	// see plan.md §3.3 follow-ons).
	TenantRecordsUpdateOwn ActionKey = "tenant.records.update_own"
	TenantRecordsDeleteOwn ActionKey = "tenant.records.delete_own"
	TenantSettingsRead     ActionKey = "tenant.settings.read"
	TenantSettingsWrite    ActionKey = "tenant.settings.write"
)
