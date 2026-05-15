import { useEffect } from "preact/hooks";
import { Route, Router, Switch, Redirect } from "wouter-preact";
import { authLoading, refreshMe, userSignal } from "./auth.js";
import { PublicLayout } from "./layouts/public.js";
import { PrivateLayout } from "./layouts/private.js";
import { LandingPage } from "./pages/public/landing.js";
import { PricingPage } from "./pages/public/pricing.js";
import { ContactPage } from "./pages/public/contact.js";
import { DocsPage } from "./pages/public/docs.js";
import { LoginPage } from "./pages/login.js";
import { AccountPage } from "./pages/account.js";
import { DashboardPage } from "./pages/private/dashboard.js";
import { TenantsPage } from "./pages/private/tenants.js";
import { TenantSettingsPage } from "./pages/private/tenant_settings.js";

// app.tsx — auth-aware router.
//
//   authLoading                        → spinner
//   userSignal.value (signed in)       → PrivateLayout + private routes
//   userSignal.value undefined (anon)  → PublicLayout + public routes,
//                                        with /login / /signup escape hatches
//
// Why one router, not two: wouter-preact's Switch is cheap and the
// route surface is small enough that splitting public/private into
// two trees would mean duplicating the redirect logic. We branch on
// the user signal inside the route component, so navigation between
// public and private screens after signin is free.

export function App() {
  useEffect(() => { void refreshMe(); }, []);

  if (authLoading.value) {
    return <div class="grid min-h-screen place-items-center text-sm text-slate-500">Loading…</div>;
  }

  const authed = !!userSignal.value;

  return (
    <Router>
      <Switch>
        {/* Public marketing routes. Authed visitors are still allowed
            to see them (so an admin can preview pricing) — UIs that
            want to redirect can do so in their page component. */}
        <Route path="/" component={authed ? RedirectToDashboard : wrap(PublicLayout, LandingPage)} />
        <Route path="/pricing" component={wrap(PublicLayout, PricingPage)} />
        <Route path="/contact" component={wrap(PublicLayout, ContactPage)} />
        <Route path="/docs" component={wrap(PublicLayout, DocsPage)} />
        <Route path="/docs/:slug" component={wrap(PublicLayout, DocsPage)} />

        {/* Auth screens — public-shell-less, full-bleed form. */}
        <Route path="/login" component={authed ? RedirectToDashboard : LoginPage} />

        {/* Private routes: redirect anonymous callers back to /login. */}
        <Route path="/dashboard" component={authed ? wrap(PrivateLayout, DashboardPage) : RedirectToLogin} />
        <Route path="/tenants" component={authed ? wrap(PrivateLayout, TenantsPage) : RedirectToLogin} />
        <Route path="/tenants/:id" component={authed ? wrap(PrivateLayout, TenantSettingsPage) : RedirectToLogin} />
        <Route path="/tenants/:id/:tab" component={authed ? wrap(PrivateLayout, TenantSettingsPage) : RedirectToLogin} />
        <Route path="/account" component={authed ? wrap(PrivateLayout, AccountPage) : RedirectToLogin} />

        {/* 404 → home. */}
        <Route>{authed ? <RedirectToDashboard /> : <PublicLayout><LandingPage /></PublicLayout>}</Route>
      </Switch>
    </Router>
  );
}

// wrap composes a layout shell around a page. wouter-preact wants a
// component reference per route; we close over the page so the route
// is one expression.
function wrap<L extends preact.ComponentChildren, P>(
  Layout: preact.FunctionComponent<{ children: preact.ComponentChildren }>,
  Page: preact.FunctionComponent<P>,
) {
  return function Wrapped(props: P) {
    return (
      <Layout>
        <Page {...props} />
      </Layout>
    );
  };
}

function RedirectToDashboard() {
  return <Redirect to="/dashboard" />;
}
function RedirectToLogin() {
  return <Redirect to="/login" />;
}
