import { Link, useRoute } from "wouter-preact";
import { AdminPage } from "../layout/admin_page";
import { useT } from "../i18n";
import { cn } from "@/lib/ui/cn";
import { AuditPanel } from "./audit";
import { AppLogPanel } from "./log_app";
import { EmailEventsPanel } from "./email-events";
import { NotificationsPanel } from "./notifications";

// Unified Logs screen — one page hosting the four append-only event
// streams as in-page categories: Audit, App logs, Email events,
// Notifications. Replaces the four standalone sidebar entries of the
// same name (v0.9 IA reorg follow-up).
//
// Category is URL-driven (`/logs/:category`) so deep links, the command
// palette, breadcrumbs and the old redirects keep working. The three
// live-state observability surfaces — Realtime, Cache, Health — stay
// separate routes / sidebar entries: they're poll-driven inspectors and
// a dashboard, not event streams, and don't share the table+filter UX.
//
// Each category renders a *panel* (AuditPanel etc.) — those components
// emit AdminPage.Toolbar + .Body fragments only; this screen owns the
// AdminPage shell, the page title, and the category tab strip.

type LogCategory = "audit" | "app" | "email-events" | "notifications";

const CATEGORIES: Array<{ id: LogCategory; labelKey: string }> = [
  { id: "audit", labelKey: "nav.logs.audit" },
  { id: "app", labelKey: "nav.logs.app" },
  { id: "email-events", labelKey: "nav.logs.emailEvents" },
  { id: "notifications", labelKey: "nav.logs.notifications" },
];

function isLogCategory(s: string | undefined): s is LogCategory {
  return (
    s === "audit" ||
    s === "app" ||
    s === "email-events" ||
    s === "notifications"
  );
}

export function LogsScreen() {
  const { t } = useT();
  const [, params] = useRoute("/logs/:category");
  // Unknown / missing category falls back to "app" — the same default
  // the bare `/logs` redirect points at.
  const category: LogCategory = isLogCategory(params?.category)
    ? params.category
    : "app";

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("tab.logs")}
        actions={
          <nav className="flex items-center gap-0.5 rounded-lg border bg-muted/40 p-0.5 text-sm">
            {CATEGORIES.map((c) => (
              <Link
                key={c.id}
                href={`/logs/${c.id}`}
                data-active={c.id === category}
                className={cn(
                  "px-3 py-1 rounded-md transition-colors",
                  c.id === category
                    ? "bg-background text-foreground font-medium shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {t(c.labelKey)}
              </Link>
            ))}
          </nav>
        }
      />

      {category === "audit" ? <AuditPanel /> : null}
      {category === "app" ? <AppLogPanel /> : null}
      {category === "email-events" ? <EmailEventsPanel /> : null}
      {category === "notifications" ? <NotificationsPanel /> : null}
    </AdminPage>
  );
}
