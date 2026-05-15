package ts

import (
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// EmitAuth renders auth.ts: one builder per auth collection, plus
// `getMe` which is collection-agnostic. v0.7 surface mirrors the
// 5 endpoints internal/api/auth/auth.go ships:
//
//   - POST /api/collections/{name}/auth-signup
//   - POST /api/collections/{name}/auth-with-password
//   - POST /api/collections/{name}/auth-refresh
//   - POST /api/collections/{name}/auth-logout
//   - GET  /api/auth/me
//
// Verification / password reset / email-change flows are deferred
// to v1.1 (depend on the mailer).
//
// The HTTPClient interface is defined in index.ts — auth.ts depends
// only on its `request` method, keeping the wrappers testable in
// isolation (a stub HTTP client is enough).
func EmitAuth(specs []builder.CollectionSpec) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`// auth.ts — typed wrappers for auth-collection endpoints.

import type { HTTPClient } from "./index.js";
`)

	authCollections := filterAuth(specs)
	for _, spec := range authCollections {
		fmt.Fprintf(&b, `import type { %s } from "./types.js";`+"\n", typeName(spec.Name))
	}
	if len(authCollections) > 0 {
		b.WriteString("\n")
	}

	b.WriteString(`/** Common auth response shape (token + record). */
export interface AuthResponse<T> {
  token: string;
  record: T;
}

/** Discovery payload returned by GET /api/collections/{name}/auth-methods.
 *
 * Front-ends call this BEFORE signin to know which login paths are
 * configured server-side. The shape mirrors PocketBase 0.23+ so the
 * PB JS SDK can drop in unchanged.
 */
export interface AuthMethods {
  password: { enabled: boolean; identityFields: string[] };
  oauth2: Array<{ name: string; displayName: string }>;
  otp: { enabled: boolean; duration: number };
  mfa: { enabled: boolean; duration: number };
  webauthn: { enabled: boolean };
}

/** GET /api/auth/me — returns the currently-authenticated record. */
export async function getMe<T = unknown>(http: HTTPClient): Promise<T> {
  const r = await http.request<{ record: T }>("GET", "/api/auth/me");
  return r.record;
}

`)

	for _, spec := range authCollections {
		writeAuthBuilder(&b, spec)
		b.WriteString("\n")
	}

	return b.String()
}

func filterAuth(specs []builder.CollectionSpec) []builder.CollectionSpec {
	out := make([]builder.CollectionSpec, 0, len(specs))
	for _, s := range specs {
		if s.Auth {
			out = append(out, s)
		}
	}
	return out
}

func writeAuthBuilder(b *strings.Builder, spec builder.CollectionSpec) {
	tName := typeName(spec.Name)
	cName := spec.Name

	fmt.Fprintf(b, "/** Auth wrappers for collection `%s`. */\n", cName)
	fmt.Fprintf(b, "export function %sAuth(http: HTTPClient) {\n", lowerFirst(tName))
	b.WriteString("  return {\n")

	// signup — accept user-defined fields alongside email/password, so
	// the auth collection's extra columns (e.g. `name`) are typed at
	// the call site rather than requiring `as any`. Sentinel's
	// `signup({ ..., name } as any)` papercut is closed by this.
	signupExtra := signupExtraFieldsTS(spec)
	fmt.Fprintf(b, `    /** POST /api/collections/%s/auth-signup */
    signup(input: { email: string; password: string; passwordConfirm?: string%s }): Promise<AuthResponse<%s>> {
      return http.request("POST", "/api/collections/%s/auth-signup", { body: input });
    },
`, cName, signupExtra, tName, cName)

	// signin
	fmt.Fprintf(b, `    /** POST /api/collections/%s/auth-with-password */
    signinWithPassword(input: { identity?: string; email?: string; password: string }): Promise<AuthResponse<%s>> {
      return http.request("POST", "/api/collections/%s/auth-with-password", { body: input });
    },
`, cName, tName, cName)

	// v3.x password reset + email verification — previously the SDK
	// only exposed signup/signin/refresh/logout, leaving operators to
	// `http.request("POST", "/api/collections/.../request-password-reset")`
	// by hand. Both endpoints are first-class on the backend (auth
	// flows package); generating wrappers eliminates the raw-HTTP
	// escape hatch for the common "forgot password" + "verify email"
	// UX. Server returns 204 No Content on success.
	fmt.Fprintf(b, `    /** POST /api/collections/%s/request-password-reset
     *  Sends a reset-link email. Always returns 204 even when the
     *  identity does not exist — the anti-enumeration posture is
     *  enforced server-side. */
    requestPasswordReset(input: { email: string }): Promise<void> {
      return http.request("POST", "/api/collections/%s/request-password-reset", { body: input });
    },
    /** POST /api/collections/%s/confirm-password-reset
     *  token comes from the email body. New password (+ confirm)
     *  replaces the old hash and revokes every existing session. */
    confirmPasswordReset(input: { token: string; password: string; passwordConfirm?: string }): Promise<void> {
      return http.request("POST", "/api/collections/%s/confirm-password-reset", { body: input });
    },
    /** POST /api/collections/%s/request-verification
     *  Re-sends the email-verification link. 204 even when the
     *  account is already verified (anti-enumeration). */
    requestVerification(input: { email: string }): Promise<void> {
      return http.request("POST", "/api/collections/%s/request-verification", { body: input });
    },
    /** POST /api/collections/%s/confirm-verification
     *  token comes from the email body. Flips verified=true and is
     *  idempotent: replaying the same token after success is a 204. */
    confirmVerification(input: { token: string }): Promise<void> {
      return http.request("POST", "/api/collections/%s/confirm-verification", { body: input });
    },
`, cName, cName, cName, cName, cName, cName, cName, cName)

	// refresh
	fmt.Fprintf(b, `    /** POST /api/collections/%s/auth-refresh — rotates the session token. */
    refresh(): Promise<AuthResponse<%s>> {
      return http.request("POST", "/api/collections/%s/auth-refresh");
    },
`, cName, tName, cName)

	// logout
	fmt.Fprintf(b, `    /** POST /api/collections/%s/auth-logout — soft-revokes the session. */
    logout(): Promise<void> {
      return http.request("POST", "/api/collections/%s/auth-logout");
    },
`, cName, cName)

	// me convenience (collection-typed)
	fmt.Fprintf(b, `    /** GET /api/auth/me — typed to this collection. */
    me(): Promise<%s> {
      return getMe<%s>(http);
    },
`, tName, tName)

	// v1.7.0 — PB-compat discovery.
	fmt.Fprintf(b, `    /** GET /api/collections/%s/auth-methods — discover configured signin paths. */
    authMethods(): Promise<AuthMethods> {
      return http.request("GET", "/api/collections/%s/auth-methods");
    },
`, cName, cName)

	// v0.4.3 Sprint 3 — TOTP enrollment management. All four endpoints
	// are AUTHED — caller must already be signed in (the account-page
	// security tab is the canonical UX). Backend lives in mfa_flow.go;
	// the 2FA status read is on the global accountClient (collection-
	// agnostic) so it isn't duplicated per auth-collection here.
	fmt.Fprintf(b, `    /** POST /api/collections/%s/totp-enroll-start
     *  Generates a fresh TOTP secret + provisioning URI for the
     *  caller's QR-code scanner and a one-time set of recovery codes.
     *  The enrollment stays PENDING until totpEnrollConfirm() lands
     *  with a working code. Calling enroll-start twice ROLLS the
     *  pending secret — render the QR every time the user opens the
     *  setup screen. */
    totpEnrollStart(): Promise<{ secret: string; provisioning_uri: string; recovery_codes: string[] }> {
      return http.request("POST", "/api/collections/%s/totp-enroll-start");
    },
    /** POST /api/collections/%s/totp-enroll-confirm
     *  Verifies the user's first authenticator code and flips the
     *  enrollment from pending → active. Idempotent — once confirmed,
     *  replays are 204. */
    totpEnrollConfirm(input: { code: string }): Promise<void> {
      return http.request("POST", "/api/collections/%s/totp-enroll-confirm", { body: input });
    },
    /** POST /api/collections/%s/totp-disable
     *  Disables 2FA. Requires a current TOTP code OR a recovery code
     *  (proves second-factor possession — defends against a stolen
     *  session disabling 2FA outright). */
    totpDisable(input: { code: string }): Promise<void> {
      return http.request("POST", "/api/collections/%s/totp-disable", { body: input });
    },
    /** POST /api/collections/%s/totp-recovery-codes
     *  Regenerates the recovery-codes list, invalidating all previous
     *  codes. Render the returned codes ONCE — the server only keeps
     *  hashes after this returns. */
    totpRegenerateRecoveryCodes(): Promise<{ codes: string[] }> {
      return http.request("POST", "/api/collections/%s/totp-recovery-codes");
    },
`, cName, cName, cName, cName, cName, cName, cName, cName)

	b.WriteString("  };\n")
	b.WriteString("}\n")
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// signupExtraFieldsTS builds the additional `; field: type; ...`
// suffix appended to the signup body type. Walks the auth collection's
// user-defined fields (skipping the built-in email/password_hash/
// verified/token_key/last_login_at) and renders each as a TS
// property. Required fields are emitted without `?`, optional with
// `?`. JSON / Files / Relations work too — same tsType() the read
// interface uses, so signup input + read shape stay aligned.
//
// Returns an empty string when the collection has no user-defined
// fields beyond the built-ins (the common case before Sentinel-like
// projects start adding `name`, `display_name`, etc.).
func signupExtraFieldsTS(spec builder.CollectionSpec) string {
	var b strings.Builder
	for _, f := range spec.Fields {
		if isAuthSystemField(f.Name) {
			continue
		}
		// Password fields belong in the email/password/passwordConfirm
		// triad already; user-declared TypePassword would collide.
		if f.Type == builder.TypePassword {
			continue
		}
		b.WriteString("; ")
		b.WriteString(f.Name)
		if !f.Required {
			b.WriteString("?")
		}
		b.WriteString(": ")
		b.WriteString(tsType(f))
	}
	return b.String()
}
