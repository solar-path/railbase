import { Link } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { SettingsListResponse } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Card, CardContent } from "@/lib/ui/card.ui";
import { Badge } from "@/lib/ui/badge.ui";

// MailerScreen — Mailer landing surface (Wave 2 IA reorg).
//
// Per docs/12-admin-ui.md screen #13 "Mailer" the surface unifies:
//   - Templates browser (→ /mailer/templates, existing screen)
//   - Preview / test send (lives inside Templates today)
//   - Send-log / Email events (→ /mailer/events, was top-level
//     before this wave — folded under Mailer)
//   - SMTP config (lives under Settings → Mailer tab)
//   - i18n variants (lives inside Templates)
//
// This landing screen is intentionally lightweight: it shows the
// driver-status pill + KPIs (templates count, recent events) + two
// big card-links to the sub-pages. We do NOT duplicate functionality
// that already lives in /mailer/templates or /mailer/events.

export function MailerScreen() {
  const settingsQ = useQuery({
    queryKey: ["settings"],
    queryFn: () => adminAPI.settingsList(),
    staleTime: 30_000,
  });
  const templatesQ = useQuery({
    queryKey: ["mailer", "templates"],
    queryFn: () => adminAPI.mailerTemplatesList(),
    staleTime: 30_000,
  });
  const eventsQ = useQuery({
    queryKey: ["mailer", "events", { limit: 1 }],
    queryFn: () => adminAPI.listEmailEvents({ page: 1, perPage: 1 }),
    staleTime: 15_000,
  });

  const driver = pickSettingString(settingsQ.data, "mailer.driver") ?? "—";
  const fromAddress =
    pickSettingString(settingsQ.data, "mailer.from_address") ?? "—";
  const configuredAt = pickSettingString(settingsQ.data, "mailer.configured_at");
  const isConfigured = Boolean(configuredAt) && driver !== "—";

  const templatesCount = templatesQ.data?.templates?.length ?? null;
  const recentTotal = eventsQ.data?.totalItems ?? null;

  return (
    <AdminPage>
      <AdminPage.Header
        title="Mailer"
        description={
          <>
            Outbound email surface. SMTP / console driver, markdown
            templates, send-log. Detailed config lives in{" "}
            <Link href="/settings" className="underline">
              Settings → Mailer
            </Link>
            .
          </>
        }
        actions={
          isConfigured ? (
            <Badge variant="default">configured</Badge>
          ) : (
            <Badge variant="secondary">not configured</Badge>
          )
        }
      />

      <AdminPage.Body className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardContent className="p-4">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">
              Driver
            </p>
            <p className="text-lg font-semibold mt-1 font-mono">{driver}</p>
            <p className="text-xs text-muted-foreground mt-2 truncate">
              from: <span className="font-mono">{fromAddress}</span>
            </p>
          </CardContent>
        </Card>

        <Link
          href="/settings/mailer/templates"
          className="block rounded-lg border bg-card hover:bg-accent transition-colors"
        >
          <CardContent className="p-4">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">
              Templates
            </p>
            <p className="text-lg font-semibold mt-1">
              {templatesCount === null ? "—" : templatesCount}
            </p>
            <p className="text-xs text-muted-foreground mt-2">
              Browse + preview · edit on disk
            </p>
          </CardContent>
        </Link>

        <Link
          href="/logs/email-events"
          className="block rounded-lg border bg-card hover:bg-accent transition-colors"
        >
          <CardContent className="p-4">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">
              Send log
            </p>
            <p className="text-lg font-semibold mt-1">
              {recentTotal === null ? "—" : recentTotal}
            </p>
            <p className="text-xs text-muted-foreground mt-2">
              Delivery, bounces, opens
            </p>
          </CardContent>
        </Link>
      </AdminPage.Body>

      <AdminPage.Body>
        <Card>
          <CardContent className="p-4 text-sm text-muted-foreground">
            <p className="font-medium text-foreground">CLI</p>
            <p className="mt-1">
              Test the SMTP round-trip end-to-end without leaving the
              terminal:
            </p>
            <pre className="font-mono text-xs bg-muted px-3 py-2 rounded mt-2 overflow-x-auto">
              railbase mailer test --to operator@example.com
            </pre>
          </CardContent>
        </Card>
      </AdminPage.Body>
    </AdminPage>
  );
}

function pickSettingString(
  data: SettingsListResponse | undefined,
  key: string,
): string | null {
  if (!data?.items) return null;
  for (const s of data.items) {
    if (s.key === key) {
      const v = (s as { value?: unknown }).value;
      if (typeof v === "string") return v;
      if (v == null) return null;
      return String(v);
    }
  }
  return null;
}
