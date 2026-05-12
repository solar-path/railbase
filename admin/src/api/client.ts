// Admin API client — talks to /api/_admin/*.
//
// Distinct from the generated user SDK at /tmp/.../client/ because:
//
//  - Different cookie name (`railbase_admin_session`)
//  - Different base path (/api/_admin)
//  - No tenant scoping
//  - Token persisted in localStorage so a hard reload survives
//
// We could hand-write the API surface as typed wrappers per endpoint,
// but the admin surface is small (~10 endpoints) and the React layer
// already wraps each in a typed react-query hook (see ./hooks.ts).
// The client below just owns the fetch primitive + auth state.

const TOKEN_KEY = "rb_admin_token";
const BASE = "/api/_admin";

export type APIErrorBody = {
  code: string;
  message: string;
  details?: unknown;
};

export class APIError extends Error {
  readonly code: string;
  readonly status: number;
  readonly body: APIErrorBody;
  constructor(status: number, body: APIErrorBody) {
    super(body.message);
    this.name = "APIError";
    this.code = body.code;
    this.status = status;
    this.body = body;
  }
}

/** True when err is an APIError with the given code. */
export function isAPIError(err: unknown, code?: string): err is APIError {
  if (!(err instanceof APIError)) return false;
  return code === undefined || err.code === code;
}

class AdminClient {
  private token: string | null;

  constructor() {
    // Hydrate from localStorage on construction. Cookie is also set
    // by the server but reading it from JS is blocked (HttpOnly), so
    // we keep a parallel client-side copy for the bearer-header path.
    this.token = typeof localStorage !== "undefined"
      ? localStorage.getItem(TOKEN_KEY)
      : null;
  }

  hasToken() {
    return !!this.token;
  }

  setToken(t: string | null) {
    this.token = t;
    if (typeof localStorage !== "undefined") {
      if (t) localStorage.setItem(TOKEN_KEY, t);
      else localStorage.removeItem(TOKEN_KEY);
    }
  }

  async request<T>(
    method: string,
    path: string,
    opts: { body?: unknown; query?: Record<string, string | number | undefined> } = {},
  ): Promise<T> {
    const headers: Record<string, string> = { Accept: "application/json" };
    let body: BodyInit | undefined;
    if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.body);
    }
    if (this.token) headers["Authorization"] = "Bearer " + this.token;

    let url = BASE + path;
    if (opts.query) {
      const q = new URLSearchParams();
      for (const [k, v] of Object.entries(opts.query)) {
        if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
      }
      const qs = q.toString();
      if (qs) url += "?" + qs;
    }

    const res = await fetch(url, { method, headers, body, credentials: "include" });
    if (res.status === 204) return undefined as T;

    const text = await res.text();
    let parsed: unknown = null;
    if (text) {
      try { parsed = JSON.parse(text); } catch { /* fall through */ }
    }

    if (!res.ok) {
      const err = (parsed as { error?: APIErrorBody } | null)?.error;
      const fallback: APIErrorBody = { code: "internal", message: text || res.statusText };
      // Auto-clear token on 401 so the auth context picks up the
      // logged-out state on next render.
      if (res.status === 401) this.setToken(null);
      throw new APIError(res.status, err ?? fallback);
    }
    return parsed as T;
  }
}

export const api = new AdminClient();
