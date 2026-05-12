// Auth context — single source of truth for "is an admin signed in?".
//
// Two state machines wired together:
//   1. The `api` client owns the token in localStorage.
//   2. The React context owns the resolved AdminRecord (`me`).
//
// On boot we ask /me — if 401, no admin is signed in and we render
// the login screen. Otherwise we render the protected app.
//
// Exposes:
//   - useAuth() → { state, signin, signout, refresh }
//   - <AuthGate> wraps the app and decides what to render

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { api, isAPIError } from "../api/client";
import { adminAPI } from "../api/admin";
import type { AdminRecord } from "../api/types";

type AuthState =
  | { kind: "loading" }
  | { kind: "anon" }
  | { kind: "signed-in"; me: AdminRecord };

interface AuthContextValue {
  state: AuthState;
  signin(email: string, password: string): Promise<void>;
  signout(): Promise<void>;
  refresh(): Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function useAuth() {
  const v = useContext(AuthContext);
  if (!v) throw new Error("useAuth: no AuthProvider in tree");
  return v;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({ kind: "loading" });

  const refresh = useCallback(async () => {
    try {
      const r = await adminAPI.me();
      setState({ kind: "signed-in", me: r.record });
    } catch (err) {
      if (isAPIError(err) && err.status === 401) {
        setState({ kind: "anon" });
        return;
      }
      // Unexpected — treat as anon to avoid wedging the UI.
      setState({ kind: "anon" });
    }
  }, []);

  // Boot probe: ask /me once on mount.
  useEffect(() => {
    if (api.hasToken()) {
      void refresh();
    } else {
      setState({ kind: "anon" });
    }
  }, [refresh]);

  const signin = useCallback(async (email: string, password: string) => {
    const resp = await adminAPI.signin(email, password);
    api.setToken(resp.token);
    setState({ kind: "signed-in", me: resp.record });
  }, []);

  const signout = useCallback(async () => {
    try {
      await adminAPI.logout();
    } catch {
      // Logout is idempotent; ignore failures.
    }
    api.setToken(null);
    setState({ kind: "anon" });
  }, []);

  return (
    <AuthContext.Provider value={{ state, signin, signout, refresh }}>
      {children}
    </AuthContext.Provider>
  );
}
