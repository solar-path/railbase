// Auth state — single source of truth for "is an admin signed in?".
//
// Implemented as a module-level @preact/signals signal rather than a
// React Context. The signals approach has two advantages here:
//
//   1. No Provider boilerplate. Any component that reads `authState.value`
//      or calls `useAuth()` gets the current state, no <AuthProvider>
//      wrapping required.
//   2. Fine-grained reactivity. Components that read `authState.value`
//      automatically re-render ONLY when the signal changes. There's
//      no Context fan-out re-rendering every consumer below the
//      provider — which used to happen with the React version.
//
// Public API stays compatible with the React version:
//   - useAuth() → { state, signin, signout, refresh }
//   - <AuthProvider> still wraps the app (now just runs the boot probe
//     once; rendering pass-through).

import { signal } from "@preact/signals";
import { useEffect, type ReactNode } from "react";
import { api, isAPIError } from "../api/client";
import { adminAPI } from "../api/admin";
import type { AdminRecord } from "../api/types";

export type AuthState =
  | { kind: "loading" }
  | { kind: "anon" }
  | { kind: "signed-in"; me: AdminRecord };

// Module-level signal — the ONLY copy of auth state in the app.
// `signal()` returns a reactive cell. Components reading `.value`
// inside their render auto-subscribe; mutating `.value` triggers
// re-render of only those subscribers.
export const authState = signal<AuthState>({ kind: "loading" });

export async function refresh() {
  try {
    const r = await adminAPI.me();
    authState.value = { kind: "signed-in", me: r.record };
  } catch (err) {
    if (isAPIError(err) && err.status === 401) {
      authState.value = { kind: "anon" };
      return;
    }
    // Unexpected error — treat as anon to avoid wedging the UI.
    authState.value = { kind: "anon" };
  }
}

export async function signin(email: string, password: string) {
  const resp = await adminAPI.signin(email, password);
  api.setToken(resp.token);
  authState.value = { kind: "signed-in", me: resp.record };
}

export async function signout() {
  try {
    await adminAPI.logout();
  } catch {
    // Logout is idempotent; ignore failures.
  }
  api.setToken(null);
  authState.value = { kind: "anon" };
}

// useAuth() — kept for back-compat with the old React API. Returns the
// current state plus the bound action functions. Components that ONLY
// need an action (e.g. signout button) can also import it directly.
export function useAuth() {
  return {
    state: authState.value,
    signin,
    signout,
    refresh,
  };
}

// AuthProvider runs the boot probe once and renders its children. No
// React Context involved — we just need a mount point for the useEffect
// that asks /me on first paint. Could be moved into <App/> directly,
// but the named wrapper makes the boot flow easy to find.
export function AuthProvider({ children }: { children: ReactNode }) {
  useEffect(() => {
    if (api.hasToken()) {
      void refresh();
    } else {
      authState.value = { kind: "anon" };
    }
  }, []);

  return <>{children}</>;
}
