import { Route, Switch, Router, Redirect } from "wouter-preact";
import { useBrowserLocation } from "wouter-preact/use-browser-location";
import { AuthProvider, useAuth } from "./auth/context";
import { LoginScreen } from "./screens/login";
import { BootstrapScreen } from "./screens/bootstrap";
import { ForgotPasswordScreen } from "./screens/forgot_password";
import { ResetPasswordScreen } from "./screens/reset_password";
import { DashboardScreen } from "./screens/dashboard";
import { SchemaScreen } from "./screens/schema";
import { SettingsScreen } from "./screens/settings";
import { LogsScreen } from "./screens/logs";
import {
  ProcessLogsScreen,
  MailerDeliveriesScreen,
  NotificationsLogScreen,
} from "./screens/deep_dive_views";
import { BackupsScreen } from "./screens/backups";
import { NotificationsPrefsScreen } from "./screens/notifications-prefs";
import { TrashScreen } from "./screens/trash";
import { MailerTemplatesScreen } from "./screens/mailer_templates";
import { MailerConfigScreen } from "./screens/mailer_config";
import { AuthMethodsScreen } from "./screens/auth_methods";
import { AdminsRolesScreen } from "./screens/admins_roles";
import { RealtimeScreen } from "./screens/realtime";
import { WebhooksScreen } from "./screens/webhooks";
import { HooksScreen } from "./screens/hooks";
import { I18nScreen } from "./screens/i18n";
import { HealthScreen } from "./screens/health";
import { CacheScreen } from "./screens/cache";
import { StripeScreen } from "./screens/stripe";
import { RecordsScreen, DataHomeScreen } from "./screens/records";
import { RecordEditorScreen } from "./screens/record_editor";
// Collection create/edit is no longer its own screen — it's a Drawer
// hosted by SchemaScreen. The /collections/* routes below render
// SchemaScreen, which opens the drawer from the matched route.
// System tables (api_tokens / admins / admin_sessions / sessions /
// jobs) are now reached via /data/_xxx URLs and dispatched from
// records.tsx — no direct routes in this file.
import { Shell } from "./layout/shell";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "./api/admin";

// Router is mounted with base="/_/" so when the binary serves the
// SPA from /_/index.html, every wouter <Link href="/foo"> resolves
// to "/_/foo" and history navigation just works.
//
// During Vite dev (port 5173) the base is also "/_/" so URLs match
// what the embedded build will serve. The Vite proxy forwards
// /api/_admin/* to the Go backend.

export function App() {
  return (
    <Router base="/_" hook={useBrowserLocation}>
      <AuthProvider>
        <Routes />
      </AuthProvider>
    </Router>
  );
}

function Routes() {
  const { state } = useAuth();

  // Even before sign-in we may want the bootstrap wizard if no admin
  // exists at all. We probe `/me` (already done by AuthProvider); if
  // that's anon, also probe `/api/_admin/_bootstrap` to learn whether
  // the system is empty. Until that endpoint exists we approximate:
  // anon + no env-supplied admin → show login. Bootstrap wizard is
  // accessible via /bootstrap path.

  if (state.kind === "loading") {
    return (
      <div className="min-h-screen flex items-center justify-center text-muted-foreground">
        Loading…
      </div>
    );
  }

  if (state.kind === "anon") {
    return (
      <Switch>
        <Route path="/bootstrap" component={BootstrapScreen} />
        <Route path="/forgot-password" component={ForgotPasswordScreen} />
        <Route path="/reset-password" component={ResetPasswordScreen} />
        <Route path="/login" component={LoginScreen} />
        <Route component={LoginGate} />
      </Switch>
    );
  }

  return (
    <Shell>
      <Switch>
        {/* v0.9 IA reorg: PocketBase-style 3-tab layout (Data / Logs /
            Settings). Old URLs continue to work via the redirects
            registered first, so bookmarks and external links keep
            resolving. See docs/12-admin-ui.md §Layout for the full
            mapping. */}

        {/* --- Redirects (pre-v0.9 URLs → new locations) --- */}
        <Route path="/audit" component={makeRedirect("/logs/timeline")} />
        <Route path="/logs" component={makeRedirect("/logs/timeline")} />
        <Route path="/realtime" component={makeRedirect("/logs/realtime")} />
        <Route path="/health" component={makeRedirect("/logs/health")} />
        <Route path="/cache" component={makeRedirect("/logs/cache")} />
        <Route path="/notifications/prefs" component={makeRedirect("/settings/notifications")} />
        {/* v3.x unified-audit reorg — deep-dive views moved out of
            Logs. App logs → Health → Process logs; email events →
            Settings → Mailer → Deliveries; notifications log →
            Settings → Notifications → Log. */}
        <Route path="/logs/app" component={makeRedirect("/health/process-logs")} />
        <Route path="/logs/email-events" component={makeRedirect("/settings/mailer/deliveries")} />
        <Route path="/logs/notifications" component={makeRedirect("/settings/notifications/log")} />
        <Route path="/logs/audit" component={makeRedirect("/logs/timeline")} />
        <Route path="/notifications" component={makeRedirect("/settings/notifications/log")} />
        <Route path="/email-events" component={makeRedirect("/settings/mailer/deliveries")} />
        <Route path="/mailer/events" component={makeRedirect("/settings/mailer/deliveries")} />
        <Route path="/mailer-templates" component={makeRedirect("/settings/mailer/templates")} />
        <Route path="/mailer/templates" component={makeRedirect("/settings/mailer/templates")} />
        <Route path="/mailer" component={makeRedirect("/settings/mailer")} />
        <Route path="/webhooks" component={makeRedirect("/settings/webhooks")} />
        <Route path="/backups" component={makeRedirect("/settings/backups")} />
        <Route path="/hooks" component={makeRedirect("/settings/hooks")} />
        <Route path="/i18n" component={makeRedirect("/settings/i18n")} />
        <Route path="/trash" component={makeRedirect("/settings/trash")} />
        <Route path="/api-tokens" component={makeRedirect("/data/_api_tokens")} />
        <Route path="/system/admins" component={makeRedirect("/data/_admins")} />
        <Route path="/system/admin-sessions" component={makeRedirect("/data/_admin_sessions")} />
        <Route path="/system/sessions" component={makeRedirect("/data/_sessions")} />
        <Route path="/jobs" component={makeRedirect("/data/_jobs")} />

        {/* --- Primary routes --- */}
        <Route path="/" component={DashboardScreen} />
        <Route path="/schema" component={SchemaScreen} />

        {/* Runtime collection management. The create/edit UI is a Drawer
            on the Schemas page — these routes render SchemaScreen, which
            reads the matched route and opens the drawer accordingly.
            Distinct prefix from /data/* so it doesn't collide with the
            parametric record routes; activeTopTab treats /collections as
            the Data tab. */}
        <Route path="/collections/new" component={SchemaScreen} />
        <Route path="/collections/:name/edit" component={SchemaScreen} />

        {/* Data: /data is the tab landing (redirects to the first
            collection); /data/:name handles both user collections and
            the system tables (records.tsx dispatches on the _ prefix). */}
        <Route path="/data/:name/:id" component={RecordEditorScreen} />
        <Route path="/data/:name" component={RecordsScreen} />
        <Route path="/data" component={DataHomeScreen} />

        {/* Logs — Realtime / Health / Cache are live-state inspectors
            and keep their own routes; they MUST be matched before the
            `/logs/:category` catch-all below. The four event streams
            (audit / app / email-events / notifications) all resolve to
            the unified LogsScreen, which reads the category from the
            URL and renders the matching in-page panel. */}
        <Route path="/logs/realtime" component={RealtimeScreen} />
        <Route path="/logs/health" component={HealthScreen} />
        <Route path="/logs/cache" component={CacheScreen} />
        <Route path="/logs/:category" component={LogsScreen} />

        {/* v3.x deep-dive views (moved out of Logs by the unified-
            audit reorg — see docs/19-unified-audit.md). */}
        <Route path="/health/process-logs" component={ProcessLogsScreen} />
        <Route path="/settings/mailer/deliveries" component={MailerDeliveriesScreen} />
        <Route path="/settings/notifications/log" component={NotificationsLogScreen} />

        {/* Settings */}
        <Route path="/settings" component={SettingsScreen} />
        <Route path="/settings/mailer" component={MailerConfigScreen} />
        <Route path="/settings/mailer/templates" component={MailerTemplatesScreen} />
        <Route path="/settings/auth" component={AuthMethodsScreen} />
        <Route path="/settings/admins" component={AdminsRolesScreen} />
        <Route path="/settings/notifications" component={NotificationsPrefsScreen} />
        <Route path="/settings/webhooks" component={WebhooksScreen} />
        <Route path="/settings/stripe" component={StripeScreen} />
        <Route path="/settings/backups" component={BackupsScreen} />
        <Route path="/settings/hooks" component={HooksScreen} />
        <Route path="/settings/i18n" component={I18nScreen} />
        <Route path="/settings/trash" component={TrashScreen} />

        <Route component={NotFound} />
      </Switch>
    </Shell>
  );
}

// makeRedirect — pure SPA redirect via wouter's <Redirect>. Replaces
// the prior hand-rolled history.replaceState + reload pattern so the
// 20+ legacy URL mappings don't all trigger a full page reload.
function makeRedirect(to: string) {
  return function RedirectTo() {
    return <Redirect to={to} replace />;
  };
}

// LoginGate decides between login and bootstrap based on a server
// probe: if /api/_admin/_bootstrap reports empty admins, we redirect
// to /bootstrap. Otherwise we show the login screen.
//
// The probe doesn't exist server-side yet (Phase B follow-up). For
// v0.8 we simply show the login screen and surface a "create one
// with railbase admin create" hint.
function LoginGate() {
  // Soft probe — count==0 only if backend wires the bootstrap
  // endpoint. Until then we degrade gracefully to the login screen.
  const probe = useQuery({
    queryKey: ["bootstrap-probe"],
    queryFn: async () => {
      try {
        return await fetch("/api/_admin/_bootstrap").then((r) => r.json());
      } catch {
        return { needsBootstrap: false };
      }
    },
    staleTime: 60_000,
    retry: false,
  });

  if (probe.data?.needsBootstrap === true) return <BootstrapScreen />;
  return <LoginScreen />;
}

// We surface stash recently-noticed admin info so the bootstrap probe
// can avoid a flicker. v0.9 will swap this for a real probe.
void adminAPI;

function NotFound() {
  return (
    <div className="text-sm text-muted-foreground">
      Not found. <a href="/_/" className="underline">Go home</a>.
    </div>
  );
}

