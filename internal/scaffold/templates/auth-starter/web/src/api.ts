// API client. After `railbase generate sdk --out web/src/_generated`,
// this file imports the typed wrappers and re-exports a single
// process-wide client. The session token is persisted in localStorage
// under "rb_token" so a page reload doesn't sign the user out.
//
// Per Railbase v0.4.2+ guarantee: routes registered via
// app.OnBeforeServe inherit the same auth middleware as built-in
// routes — so anything the backend authenticates, this client can
// reach with the bearer token attached automatically.

import { createRailbaseClient } from "./_generated/index.js";

export const rb = createRailbaseClient({
  baseURL: "", // same-origin in production; Vite proxies in dev
  storage: typeof window !== "undefined" ? window.localStorage : undefined,
  storageKey: "rb_token",
});

export type RB = typeof rb;
