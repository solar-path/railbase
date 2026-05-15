// Fast TS-gen output assertions for the v0.4.3 account.ts module.
// No Postgres — just checks the emitter renders the expected client
// surface so a missing wire is caught at unit-test speed.
package ts

import (
	"strings"
	"testing"
)

func TestEmitAccount_SessionsSurface(t *testing.T) {
	out := EmitAccount()

	// Type — session row exposed to JS.
	for _, want := range []string{
		"export interface Session {",
		"id: string;",
		"collection_name: string;",
		"created_at: string;",
		"last_active_at: string;",
		"expires_at: string;",
		"ip?: string;",
		"user_agent?: string;",
		"current: boolean;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("account.ts missing %q\n---\n%s", want, out)
		}
	}

	// token_hash is a privacy footgun — the generated Session
	// interface must not declare it as a field. We allow the bare
	// word to appear in doc comments (the current emitter explicitly
	// documents it as "NOT exposed"), so we look for the field-shape
	// `token_hash:` / `token_hash?:` instead of substring match.
	for _, bad := range []string{"token_hash:", "token_hash ?", "token_hash?:"} {
		if strings.Contains(out, bad) {
			t.Errorf("account.ts leaks token_hash as a TS field (%q):\n%s", bad, out)
		}
	}

	// Client factory + 3 methods.
	for _, want := range []string{
		"export function accountClient(http: HTTPClient)",
		"async listSessions(): Promise<Session[]>",
		`http.request<{ sessions: Session[] }>("GET", "/api/auth/sessions")`,
		"revokeSession(id: string, opts?: { force?: boolean }): Promise<void>",
		`"DELETE", "/api/auth/sessions/" + encodeURIComponent(id) + qs`,
		"async revokeOtherSessions(): Promise<{ revoked: number }>",
		`"DELETE", "/api/auth/sessions/others"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("account.ts missing %q\n---\n%s", want, out)
		}
	}
}

// TestEmitIndex_WiresAccountClient — the root createRailbaseClient
// must expose `account: accountClient(http)` so call sites can do
// `rb.account.listSessions()`. Without the wire, the namespace is
// missing and Sentinel-style consumers can't reach the API.
func TestEmitIndex_WiresAccountClient(t *testing.T) {
	out := EmitIndex(nil)
	for _, want := range []string{
		`import { accountClient } from "./account.js";`,
		"account: accountClient(http),",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("index.ts missing %q (account wiring)\n---\n%s", want, out)
		}
	}
}

// Sprint 2 — profile + password surfaces on account.ts.
func TestEmitAccount_ProfileAndPasswordSurface(t *testing.T) {
	out := EmitAccount()
	for _, want := range []string{
		"async updateProfile<T = unknown>(input: Record<string, unknown>): Promise<T>",
		`"PATCH", "/api/auth/me"`,
		"changePassword(input: {",
		"current_password: string;",
		"new_password: string;",
		"passwordConfirm?: string;",
		`"POST", "/api/auth/change-password"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("account.ts missing %q (profile/password)\n---\n%s", want, out)
		}
	}
}

// Sprint 3 — 2FA status (read) on account.ts; the TOTP mutation
// endpoints are per-collection and live on the auth builder (see
// TestEmitAuth_TOTPSurface below).
func TestEmitAccount_TwoFAStatus(t *testing.T) {
	out := EmitAccount()
	for _, want := range []string{
		"async twoFAStatus(): Promise<{ enrolled: boolean }>",
		`"GET", "/api/auth/2fa/status"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("account.ts missing %q (2FA status)\n---\n%s", want, out)
		}
	}
}

// Sprint 5 — device labelling: PATCH endpoint + the two new fields
// surface on the Session interface.
func TestEmitAccount_DeviceLabelling(t *testing.T) {
	out := EmitAccount()
	for _, want := range []string{
		"device_name?: string;",
		"is_trusted: boolean;",
		"updateSession(id: string, input: { device_name?: string; is_trusted?: boolean }): Promise<void>",
		`"PATCH", "/api/auth/sessions/"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("account.ts missing %q (device labelling)\n---\n%s", want, out)
		}
	}
}

// Sprint 3 — per-collection TOTP wrappers must appear on every auth
// collection's builder. Air/rail's "Security" tab calls these to
// enroll, confirm via code, disable, and regenerate recovery codes.
func TestEmitAuth_TOTPSurface(t *testing.T) {
	out := EmitAuth(fixtureSpecs()) // includes the `users` auth coll
	for _, want := range []string{
		"totpEnrollStart(): Promise<{ secret: string; provisioning_uri: string; recovery_codes: string[] }>",
		`"POST", "/api/collections/users/totp-enroll-start"`,
		"totpEnrollConfirm(input: { code: string }): Promise<void>",
		`"POST", "/api/collections/users/totp-enroll-confirm"`,
		"totpDisable(input: { code: string }): Promise<void>",
		`"POST", "/api/collections/users/totp-disable"`,
		"totpRegenerateRecoveryCodes(): Promise<{ codes: string[] }>",
		`"POST", "/api/collections/users/totp-recovery-codes"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("auth.ts missing %q (TOTP wrappers)\n---\n%s", want, out)
		}
	}
}
