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

	// signup
	fmt.Fprintf(b, `    /** POST /api/collections/%s/auth-signup */
    signup(input: { email: string; password: string }): Promise<AuthResponse<%s>> {
      return http.request("POST", "/api/collections/%s/auth-signup", { body: input });
    },
`, cName, tName, cName)

	// signin
	fmt.Fprintf(b, `    /** POST /api/collections/%s/auth-with-password */
    signinWithPassword(input: { identity?: string; email?: string; password: string }): Promise<AuthResponse<%s>> {
      return http.request("POST", "/api/collections/%s/auth-with-password", { body: input });
    },
`, cName, tName, cName)

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

	b.WriteString("  };\n")
	b.WriteString("}\n")
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
