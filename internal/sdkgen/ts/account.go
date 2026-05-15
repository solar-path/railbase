package ts

// EmitAccount renders account.ts: typed wrappers for the
// collection-agnostic user-self-service endpoints introduced from
// v0.4.3 onward:
//
//   GET    /api/auth/sessions         — Sprint 1 (sessions list)
//   DELETE /api/auth/sessions/{id}    — Sprint 1
//   DELETE /api/auth/sessions/others  — Sprint 1
//
// Future sprints will add to this same file:
//   - Sprint 2: PATCH /api/auth/me, POST /api/auth/change-password
//   - Sprint 3: POST /api/auth/2fa/{setup,confirm,disable,regen-codes}
//   - Sprint 5: GET/DELETE /api/auth/devices ...
//
// Why a separate emitter rather than extending EmitAuth: EmitAuth's
// signature carries one auth collection per builder (signupTypeName,
// per-collection field overrides). These endpoints don't dispatch on
// collection — the auth middleware reads it off the principal — so a
// flat module surface (`rb.account.listSessions()`) is the right
// ergonomics. Mixing in EmitAuth would have forced an awkward
// "first-collection-wins" choice.
func EmitAccount() string {
	return header + `// account.ts — typed wrappers for /api/auth/* self-service endpoints.

import type { HTTPClient } from "./index.js";

/** One row from GET /api/auth/sessions. ID is the opaque session
 *  handle — pass it back to revokeSession() to invalidate this
 *  specific device. token_hash is intentionally NOT exposed by the
 *  server. */
export interface Session {
  id: string;
  collection_name: string;
  /** ISO-8601 UTC string, e.g. "2026-05-15T09:30:00.000Z". */
  created_at: string;
  last_active_at: string;
  expires_at: string;
  /** Best-effort client IP recorded at signin (may be empty when the
   *  proxy layer didn't forward X-Forwarded-For). */
  ip?: string;
  /** Raw User-Agent string from the signin request. */
  user_agent?: string;
  /** True for the session that issued THE bearer token of the request
   *  that called listSessions. UIs render a "this device" badge and
   *  typically disable the row-revoke action for the current row. */
  current: boolean;
  /** User-supplied label for this device, e.g. "Alice's iPhone".
   *  Empty when not set. Persisted via updateSession(id, { device_name }). */
  device_name?: string;
  /** Whether the user has marked this device as trusted. v0.5+ will
   *  let trusted devices skip 2FA prompts on subsequent signins;
   *  v0.4.3 only records the user's intent. */
  is_trusted: boolean;
}

/** Account-management wrappers. Every call requires an authenticated
 *  bearer token (set via createRailbaseClient({ token }) or setToken).
 *
 *      const rb = createRailbaseClient({ baseURL, token });
 *      const sessions = await rb.account.listSessions();
 *      await rb.account.revokeSession(sessions.find(s => !s.current)!.id);
 *      await rb.account.revokeOtherSessions();
 */
export function accountClient(http: HTTPClient) {
  return {
    /** GET /api/auth/sessions — every live session for the caller. */
    async listSessions(): Promise<Session[]> {
      const r = await http.request<{ sessions: Session[] }>("GET", "/api/auth/sessions");
      return r.sessions;
    },
    /** DELETE /api/auth/sessions/{id} — revoke a specific session.
     *  Backend refuses to revoke the CURRENT session via this call
     *  (use rb.<coll>Auth.logout() instead); pass { force: true } to
     *  override. */
    revokeSession(id: string, opts?: { force?: boolean }): Promise<void> {
      const qs = opts?.force ? "?force=true" : "";
      return http.request("DELETE", "/api/auth/sessions/" + encodeURIComponent(id) + qs);
    },
    /** DELETE /api/auth/sessions/others — revoke every session except
     *  the current one. Returns { revoked: N } so the UI can toast
     *  "Signed out from N other devices". */
    async revokeOtherSessions(): Promise<{ revoked: number }> {
      return http.request<{ revoked: number }>("DELETE", "/api/auth/sessions/others");
    },

    /** PATCH /api/auth/sessions/{id} — update user-supplied device
     *  metadata. Both fields are optional; passing neither is a 400.
     *
     *      await rb.account.updateSession(s.id, { device_name: "Alice's iPhone" });
     *      await rb.account.updateSession(s.id, { is_trusted: true });
     *
     *  Trust enforcement at signin (skip 2FA on trusted devices) is
     *  a v0.5+ follow-up; v0.4.3 only persists the user's intent. */
    updateSession(id: string, input: { device_name?: string; is_trusted?: boolean }): Promise<void> {
      return http.request("PATCH", "/api/auth/sessions/" + encodeURIComponent(id), { body: input });
    },

    /** PATCH /api/auth/me — update arbitrary user-defined fields on
     *  the caller's auth record. Rejected fields: email (use the
     *  request/confirm-email-change flow), password (use changePassword),
     *  any system column (id/verified/created/updated/...).
     *
     *  Type-erased on purpose — the writable surface depends on which
     *  fields the operator declared on the auth collection (avatar_url,
     *  display_name, theme, locale, timezone, ...). Cast the input at
     *  the call site if you maintain a stricter Profile type. */
    async updateProfile<T = unknown>(input: Record<string, unknown>): Promise<T> {
      const r = await http.request<{ record: T }>("PATCH", "/api/auth/me", { body: input });
      return r.record;
    },

    /** POST /api/auth/change-password — verifies current_password,
     *  then sets new_password and AUTO-REVOKES every other live
     *  session for the user. The current session keeps working.
     *
     *  Operators that surface "sign out everywhere" as a separate
     *  toggle can call revokeOtherSessions() after a successful
     *  changePassword() to also kill the current session, but that's
     *  not the default UX (forces re-login on the device that just
     *  changed the password — confusing). */
    changePassword(input: {
      current_password: string;
      new_password: string;
      passwordConfirm?: string;
    }): Promise<void> {
      return http.request("POST", "/api/auth/change-password", { body: input });
    },

    /** GET /api/auth/2fa/status — is the caller TOTP-enrolled?
     *  Returns enrolled=false when 2FA isn't configured on the
     *  deployment OR when the user simply hasn't set it up. The
     *  account-page UI uses this to pick between "Set up 2FA" CTA
     *  and "Disable 2FA" action without trying enroll-start (which
     *  has side effects). The collection-specific mutation
     *  endpoints live on rb.<coll>Auth.totp*. */
    async twoFAStatus(): Promise<{ enrolled: boolean }> {
      return http.request<{ enrolled: boolean }>("GET", "/api/auth/2fa/status");
    },
  };
}
`
}
