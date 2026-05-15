This directory is populated by:

    railbase generate sdk --out web/src/_generated

The scaffolded api.ts imports from "./_generated/index.js" — until you
run the SDK generator at least once, those imports fail with "module
not found". Run the generator after your first `railbase migrate up`
(so the auth-collection schema reflects whatever fields you added to
schema/users.go).

The generated SDK provides:
  - rb.account.{listSessions,revokeSession,revokeOtherSessions,
                updateProfile,changePassword,twoFAStatus}
  - rb.usersAuth.{signinWithPassword,logout,refresh,
                  totpEnrollStart,totpEnrollConfirm,totpDisable,
                  totpRegenerateRecoveryCodes,...}
  - rb.realtime.subscribe<T>(...)
  - rb.<collection>.{list,get,create,update,delete}

Re-run the generator on schema changes; the SDK's _meta.json warns
if it drifts.
