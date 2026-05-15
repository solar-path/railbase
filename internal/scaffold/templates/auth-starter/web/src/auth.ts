// Module-level auth state — Preact signals so any component reading
// `userSignal.value` re-renders on signin/signout without prop drilling
// or a React-Context provider. Pattern lifted from air/rail.
//
// userSignal.value is null while we don't know (initial probe in
// flight) → undefined when explicitly unauthenticated → an object
// when signed in. Components branch on the tri-state to render
// loading skeleton / login form / dashboard.

import { signal } from "@preact/signals";
import { rb } from "./api.js";

export type Me = {
  id: string;
  email: string;
  verified: boolean;
  [key: string]: unknown;
};

export const userSignal = signal<Me | null | undefined>(null);
export const authLoading = signal<boolean>(true);

// Probe /api/auth/me on boot. 401 → set undefined (anonymous); 200 →
// stash the record. Other errors → log + stay anonymous so a transient
// backend issue doesn't trap the SPA on a blank screen.
export async function refreshMe(): Promise<void> {
  authLoading.value = true;
  try {
    const me = (await rb.me()) as Me;
    userSignal.value = me;
  } catch (err) {
    // 401 is expected for anonymous; any other error we still treat
    // as "not signed in" — the security tab will refuse to load
    // until a valid signin happens, which is the correct posture.
    userSignal.value = undefined;
  } finally {
    authLoading.value = false;
  }
}

export async function signIn(email: string, password: string, collection = "users"): Promise<void> {
  const collKey = collection + "Auth" as keyof typeof rb;
  // The per-collection auth helper is named e.g. `usersAuth`. Cast
  // through unknown because the generated rb shape is widened by the
  // scaffold's typing.
  const auth = rb[collKey] as { signinWithPassword: (i: { identity?: string; email?: string; password: string }) => Promise<{ token: string; record: Me }> };
  const res = await auth.signinWithPassword({ identity: email, password });
  userSignal.value = res.record;
}

export async function signOut(collection = "users"): Promise<void> {
  const collKey = collection + "Auth" as keyof typeof rb;
  const auth = rb[collKey] as { logout: () => Promise<void> };
  try {
    await auth.logout();
  } finally {
    userSignal.value = undefined;
  }
}
