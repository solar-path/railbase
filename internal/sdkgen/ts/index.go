package ts

import (
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// EmitIndex produces index.ts: the public entry point.
// `createRailbaseClient(opts)` returns an object with one property
// per collection plus per-auth-collection auth wrappers, all wired to
// a shared HTTPClient that handles bearer-token auth, JSON encoding,
// and error envelope unwrapping.
//
// Drift detection: clients pass the schemaHash they were generated
// against, and the client logs a console warning when the live
// `_meta.json` (fetched on first call) doesn't match. v0.7 keeps
// drift handling client-side; v0.8 will surface it in the admin UI.
func EmitIndex(specs []builder.CollectionSpec) string {
	var b strings.Builder
	b.WriteString(header)

	b.WriteString(`// index.ts — public entry point.
//
// Usage:
//   import { createRailbaseClient } from "./client";
//   const rb = createRailbaseClient({ baseURL: "http://localhost:8080" });
//   const posts = await rb.posts.list();
//   const me = await rb.users.signinWithPassword({ email, password });

import { RailbaseAPIError } from "./errors.js";
import type { RailbaseError } from "./errors.js";
import { stripeClient } from "./stripe.js";
import { notificationsClient } from "./notifications.js";
import { realtimeClient } from "./realtime.js";
import { i18nClient } from "./i18n.js";
`)

	// Imports: one per collection wrapper + types.
	for _, spec := range specs {
		fmt.Fprintf(&b, `import { %sCollection } from "./collections/%s.js";`+"\n",
			lowerFirst(typeName(spec.Name)), spec.Name)
	}
	authCols := filterAuth(specs)
	if len(authCols) > 0 {
		// Pull every <name>Auth factory + getMe into scope.
		var names []string
		for _, spec := range authCols {
			names = append(names, lowerFirst(typeName(spec.Name))+"Auth")
		}
		names = append(names, "getMe")
		fmt.Fprintf(&b, `import { %s } from "./auth.js";`+"\n", strings.Join(names, ", "))
	}

	b.WriteString(`
export type { RailbaseError } from "./errors.js";
export { RailbaseAPIError, isRailbaseError } from "./errors.js";

/** Options accepted by createRailbaseClient. */
export interface ClientOptions {
  baseURL: string;
  /** Optional bearer token. Use setToken() to swap at runtime. */
  token?: string;
  /** Optional X-Tenant header value. Use setTenant() to swap at runtime. */
  tenant?: string;
  /** Override fetch (Node 18+ has it global; testing benefits from injection). */
  fetch?: typeof fetch;
}

/** Subset of the request surface every wrapper needs. */
export interface HTTPClient {
  request<T>(method: string, path: string, opts?: { body?: unknown }): Promise<T>;
  /** Authenticated raw fetch — escape hatch for streaming endpoints
   *  (SSE). Returns the raw Response so the caller can read res.body;
   *  realtime.ts uses this since EventSource can't carry a bearer token. */
  stream(path: string, init?: RequestInit): Promise<Response>;
  setToken(token: string | null): void;
  setTenant(tenant: string | null): void;
}

class FetchHTTPClient implements HTTPClient {
  private baseURL: string;
  private token: string | null;
  private tenant: string | null;
  private fetchImpl: typeof fetch;

  constructor(opts: ClientOptions) {
    if (!opts.baseURL) throw new Error("createRailbaseClient: baseURL is required");
    this.baseURL = opts.baseURL.replace(/\/$/, "");
    this.token = opts.token ?? null;
    this.tenant = opts.tenant ?? null;
    this.fetchImpl = opts.fetch ?? fetch;
  }

  setToken(token: string | null) { this.token = token; }
  setTenant(tenant: string | null) { this.tenant = tenant; }

  /** Authenticated raw fetch. Stamps the same Authorization / X-Tenant
   *  headers as request(), but returns the Response untouched so a
   *  caller can stream res.body (used by realtime.ts for SSE). */
  async stream(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers);
    if (this.token) headers.set("Authorization", "Bearer " + this.token);
    if (this.tenant) headers.set("X-Tenant", this.tenant);
    return this.fetchImpl(this.baseURL + path, { ...init, headers });
  }

  async request<T>(method: string, path: string, opts: { body?: unknown } = {}): Promise<T> {
    const headers: Record<string, string> = { "Accept": "application/json" };
    let body: BodyInit | undefined;
    if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.body);
    }
    if (this.token) headers["Authorization"] = "Bearer " + this.token;
    if (this.tenant) headers["X-Tenant"] = this.tenant;

    const res = await this.fetchImpl(this.baseURL + path, { method, headers, body });
    if (res.status === 204) return undefined as T;

    const text = await res.text();
    let parsed: unknown = null;
    if (text) {
      try { parsed = JSON.parse(text); } catch { /* fall through */ }
    }

    if (!res.ok) {
      const err = (parsed as { error?: RailbaseError } | null)?.error;
      const fallback: RailbaseError = { code: "internal", message: text || res.statusText };
      throw new RailbaseAPIError(res.status, err ?? fallback);
    }
    return parsed as T;
  }
}

/** Construct a typed Railbase client. */
export function createRailbaseClient(opts: ClientOptions) {
  const http = new FetchHTTPClient(opts);
  return {
    /** Raw HTTP client — escape hatch for endpoints not yet typed. */
    http,
    setToken: (t: string | null) => http.setToken(t),
    setTenant: (t: string | null) => http.setTenant(t),
    /** GET /api/auth/me — collection-agnostic. */
    me: () => getMe(http),
    /** Stripe billing — config + checkout wrappers. Schema-independent. */
    stripe: stripeClient(http),
    /** In-app notifications — list / read / preferences. Schema-independent. */
    notifications: notificationsClient(http),
    /** Realtime — typed SSE topic subscriptions. Schema-independent. */
    realtime: realtimeClient(http),
    /** i18n — translation bundles + client-side Translator. Schema-independent. */
    i18n: i18nClient(http),

`)

	for _, spec := range specs {
		fmt.Fprintf(&b, "    %s: %sCollection(http),\n",
			spec.Name, lowerFirst(typeName(spec.Name)))
	}
	for _, spec := range authCols {
		// Per-auth-collection auth methods nested under the
		// collection key — `rb.users.signinWithPassword(...)` reads
		// well and avoids polluting the top-level namespace.
		fmt.Fprintf(&b, "    %sAuth: %sAuth(http),\n",
			spec.Name, lowerFirst(typeName(spec.Name)))
	}

	b.WriteString(`  };
}
`)

	return b.String()
}
