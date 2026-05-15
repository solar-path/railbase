import { AdminPage } from "../layout/admin_page";
import { useT } from "../i18n";
import { AppLogPanel } from "./log_app";
import { EmailEventsPanel } from "./email-events";
import { NotificationsPanel } from "./notifications";

// v3.x deep-dive screens. Each is a thin AdminPage wrapper around the
// pre-existing panel — the panel itself emits AdminPage.Toolbar + .Body
// fragments and never owned the page shell.
//
// Why these exist as their own screens:
//   * /logs/app, /logs/email-events, /logs/notifications used to be
//     in-tab views on the unified Logs page. Phase 1 of the unified-
//     audit rollout collapses Logs into a single Timeline view, so the
//     old tabs would become dead clutter.
//   * The data itself stays useful for narrow operational tasks
//     (debug, mailer state-machine, in-app delivery state). Each
//     moves to its natural home in Settings / Health.
//   * The Logs tabs are retired but the old /logs/<x> routes redirect
//     to the new locations so deep links + bookmarks keep working
//     (App.tsx redirects table).

// ProcessLogsScreen — Health → Process logs. Renders the slog stream
// persisted in _logs (gated by `logs.persist` setting / env). Same
// 14-day retention as before. Sibling to /logs/health (dashboard) and
// /logs/realtime (live broadcast state) so all process-level
// diagnostics live under one /health prefix in the IA.
export function ProcessLogsScreen() {
  const { t } = useT();
  return (
    <AdminPage>
      <AdminPage.Header
        title={t("processLogs.title")}
        description={
          <>
            {t("processLogs.descPart1")} <code className="font-mono">_logs</code>.
            {" "}{t("processLogs.descPart2")} <code className="font-mono">logs.persist</code>
            {" "}{t("processLogs.descPart3")} <code className="font-mono">RAILBASE_LOGS_PERSIST</code> {t("processLogs.descPart4")}
          </>
        }
      />
      <AppLogPanel />
    </AdminPage>
  );
}

// MailerDeliveriesScreen — Settings → Mailer → Deliveries. The per-
// recipient state-machine view over _email_events: sent → delivered
// → bounced / complained. Distinct from the Timeline `mailer.send`
// audit row, which records WHO triggered the send; Deliveries
// records what the provider reported AFTER. The drawer on the
// Timeline `mailer.send` row links here scoped to the recipient so
// the operator goes from "who sent it" to "did it land".
export function MailerDeliveriesScreen() {
  const { t } = useT();
  return (
    <AdminPage>
      <AdminPage.Header
        title={t("mailerDeliveries.title")}
        description={
          <>
            {t("mailerDeliveries.descPart1")}{" "}
            <code className="font-mono">mailer.Send</code> {t("mailerDeliveries.descPart2")}{" "}
            <code className="font-mono">mailer.send</code> {t("mailerDeliveries.descPart3")}
          </>
        }
      />
      <EmailEventsPanel />
    </AdminPage>
  );
}

// NotificationsLogScreen — Settings → Notifications → Log. Cross-user
// log of persisted in-app notification deliveries (`_notifications`
// table). Sibling to Settings → Notifications (the prefs editor).
export function NotificationsLogScreen() {
  const { t } = useT();
  return (
    <AdminPage>
      <AdminPage.Header
        title={t("notificationsLog.title")}
        description={t("notificationsLog.description")}
      />
      <NotificationsPanel />
    </AdminPage>
  );
}
