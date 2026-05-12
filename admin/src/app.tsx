import { Route, Switch, Router } from "wouter";
import { useBrowserLocation } from "wouter/use-browser-location";
import { AuthProvider, useAuth } from "./auth/context";
import { LoginScreen } from "./screens/login";
import { BootstrapScreen } from "./screens/bootstrap";
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
import { RealtimeScreen } from "./screens/realtime";
import { WebhooksScreen } from "./screens/webhooks";
import { HooksScreen } from "./screens/hooks";
import { I18nScreen } from "./screens/i18n";
import { HealthScreen } from "./screens/health";
import { CacheScreen } from "./screens/cache";
import { RecordsScreen } from "./screens/records";
import { RecordEditorScreen } from "./screens/record_editor";
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
      <div className="min-h-screen flex items-center justify-center text-neutral-500">
        Loading…
      </div>
    );
  }

  if (state.kind === "anon") {
    return (
      <Switch>
        <Route path="/bootstrap" component={BootstrapScreen} />
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
        <Route path="/mailer-templates" component={MailerTemplatesScreen} />
        <Route path="/email-events" component={EmailEventsScreen} />
        <Route path="/realtime" component={RealtimeScreen} />
        <Route path="/webhooks" component={WebhooksScreen} />
        <Route path="/hooks" component={HooksScreen} />
        <Route path="/i18n" component={I18nScreen} />
        <Route path="/health" component={HealthScreen} />
        <Route path="/cache" component={CacheScreen} />
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
    <div className="text-sm text-neutral-500">
      Not found. <a href="/_/" className="underline">Go home</a>.
    </div>
  );
}
