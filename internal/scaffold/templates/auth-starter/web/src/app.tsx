import { useEffect } from "preact/hooks";
import { Route, Router, Switch } from "wouter-preact";
import { LoginPage } from "./pages/login.js";
import { AccountPage } from "./pages/account.js";
import { authLoading, refreshMe, userSignal } from "./auth.js";

// app.tsx — root router with auth-aware dispatch:
//
//   authLoading           → spinner
//   userSignal.value      → AccountPage (signed-in routes)
//   undefined             → LoginPage (anonymous routes)
//
// Wouter's Router is light — no need for a layout shell for the
// scaffold's two-route surface. Add /reset-password, /verify, etc.
// here as you grow the auth flows.

export function App() {
  useEffect(() => { void refreshMe(); }, []);

  if (authLoading.value) {
    return <div class="p-8 text-sm text-slate-500">Loading…</div>;
  }

  if (!userSignal.value) return <LoginPage />;

  return (
    <Router>
      <Switch>
        <Route path="/" component={AccountPage} />
        <Route path="/account" component={AccountPage} />
        {/* 404 → home */}
        <Route><AccountPage /></Route>
      </Switch>
    </Router>
  );
}
