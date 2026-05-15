import { AdminPage } from "../layout/admin_page";
import { useT } from "../i18n";
import { TimelinePanel } from "./timeline";

// Logs screen — v3.x unified Timeline only.
//
// Replaces the four-tab Audit / App logs / Email events / Notifications
// split. The deep-dive views moved to their natural homes:
//
//   * App logs (slog stream)    → /health/process-logs
//   * Email events (deliveries) → /settings/mailer/deliveries
//   * Notifications log         → /settings/notifications/log
//   * Audit (legacy)            → folded into Timeline (legacy rows
//                                  still verify via `railbase audit
//                                  verify` until Phase 1.5 ports them)
//
// Old URLs redirect via App.tsx so bookmarks keep working. See
// docs/19-unified-audit.md for the design rationale.
//
// The unified screen is intentionally minimal — TimelinePanel owns the
// toolbar, filters, table, and detail drawer; this screen just wraps
// it in the AdminPage shell with the standard page title.

export function LogsScreen() {
  const { t } = useT();
  return (
    <AdminPage>
      <AdminPage.Header title={t("tab.logs")} />
      <TimelinePanel />
    </AdminPage>
  );
}
