import { Route, Switch, Router } from "wouter-preact";
import { useBrowserLocation } from "wouter-preact/use-browser-location";
import { AuthProvider, useAuth } from "./auth/context";
import { LoginScreen } from "./screens/login";
import { BootstrapScreen } from "./screens/bootstrap";
import { ForgotPasswordScreen } from "./screens/forgot_password";
import { ResetPasswordScreen } from "./screens/reset_password";
import { DashboardScreen } from "./screens/dashboard";
import { SchemaScreen } from "./screens/schema";
import { SettingsScreen } from "./screens/settings";
import { AuditScreen } from "./screens/audit";
import { LogsScreen } from "./screens/logs";
import { JobsScreen } from "./screens/jobs";
import { APITokensScreen } from "./screens/api_tokens";
import { BackupsScreen } from "./screens/backups";
import { NotificationsScreen } from "./screens/notifications";
import { NotificationsPrefsScreen } from "./screens/notifications-prefs";
import { TrashScreen } from "./screens/trash";
import { MailerTemplatesScreen } from "./screens/mailer_templates";
import { EmailEventsScreen } from "./screens/email-events";
import { MailerScreen } from "./screens/mailer";
import { RealtimeScreen } from "./screens/realtime";
import { WebhooksScreen } from "./screens/webhooks";
import { HooksScreen } from "./screens/hooks";
import { I18nScreen } from "./screens/i18n";
import { HealthScreen } from "./screens/health";
import { CacheScreen } from "./screens/cache";
import { RecordsScreen } from "./screens/records";
import { RecordEditorScreen } from "./screens/record_editor";
import { SystemAdminsScreen } from "./screens/system_admins";
import { SystemAdminSessionsScreen } from "./screens/system_admin_sessions";
import { SystemSessionsScreen } from "./screens/system_sessions";
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
        <Route path="/" component={DashboardScreen} />
        <Route path="/schema" component={SchemaScreen} />
        <Route path="/settings" component={SettingsScreen} />
        <Route path="/audit" component={AuditScreen} />
        <Route path="/logs" component={LogsScreen} />
        <Route path="/jobs" component={JobsScreen} />
        <Route path="/api-tokens" component={APITokensScreen} />
        <Route path="/backups" component={BackupsScreen} />
        <Route path="/notifications/prefs" component={NotificationsPrefsScreen} />
        <Route path="/notifications" component={NotificationsScreen} />
        <Route path="/trash" component={TrashScreen} />
        {/* Mailer surface (Wave 2 IA reorg): /mailer is the landing
            tab page; /mailer/templates and /mailer/events delegate to
            the existing screens. The pre-IA paths /mailer-templates
            and /email-events redirect to the new locations for
            bookmark + external-link continuity. */}
        <Route path="/mailer" component={MailerScreen} />
        <Route path="/mailer/templates" component={MailerTemplatesScreen} />
        <Route path="/mailer/events" component={EmailEventsScreen} />
        <Route path="/mailer-templates" component={MailerTemplatesRedirect} />
        <Route path="/email-events" component={EmailEventsRedirect} />
        <Route path="/realtime" component={RealtimeScreen} />
        <Route path="/webhooks" component={WebhooksScreen} />
        <Route path="/hooks" component={HooksScreen} />
        <Route path="/i18n" component={I18nScreen} />
        <Route path="/health" component={HealthScreen} />
        <Route path="/cache" component={CacheScreen} />
        <Route path="/system/admins" component={SystemAdminsScreen} />
        <Route path="/system/admin-sessions" component={SystemAdminSessionsScreen} />
        <Route path="/system/sessions" component={SystemSessionsScreen} />
        <Route path="/data/:name/:id" component={RecordEditorScreen} />
        <Route path="/data/:name" component={RecordsScreen} />
        <Route component={NotFound} />
      </Switch>
    </Shell>
  );
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

// Permanent client-side redirects from pre-IA-reorg paths to the new
// Messaging-group routes. Wave 2 (docs/12 §Layout). Replace history so
// the browser Back button doesn't bounce between the two URLs.
function MailerTemplatesRedirect() {
  if (typeof window !== "undefined") {
    window.history.replaceState({}, "", "/_/mailer/templates");
    window.location.reload();
  }
  return null;
}
function EmailEventsRedirect() {
  if (typeof window !== "undefined") {
    window.history.replaceState({}, "", "/_/mailer/events");
    window.location.reload();
  }
  return null;
}
