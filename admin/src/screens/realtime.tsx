import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { RealtimeSubscription } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Alert, AlertDescription } from "@/lib/ui/alert.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// Realtime monitor admin screen — read-only snapshot of active SSE
// subscriptions on the broker. Backend: GET /api/_admin/realtime
// (v1.7.16 §3.11 slice).
//
// Polls every 5 s so the operator sees subscriptions arrive/depart
// without a manual reload. No unsubscribe/disconnect actions —
// surgically kicking subs is rarely the right tool (fix the slow
// client, not the symptom). Per-row drop count surfaces in red when
// non-zero so operators notice backpressure events.

// Read-only column factory for the active-subscription table.
function buildRealtimeColumns(t: Translator["t"]): ColumnDef<RealtimeSubscription>[] {
  return [
    {
      id: "user",
      header: t("realtime.col.user"),
      accessor: "user_id",
      headClass: "uppercase tracking-wide text-xs",
      cell: (s) => <span class="font-mono text-xs">{shortenID(s.user_id)}</span>,
    },
    {
      id: "tenant",
      header: t("realtime.col.tenant"),
      accessor: "tenant_id",
      headClass: "uppercase tracking-wide text-xs",
      cell: (s) =>
        s.tenant_id ? (
          <span class="font-mono text-xs">{shortenID(s.tenant_id)}</span>
        ) : (
          <span class="text-xs text-muted-foreground">{t("realtime.tenant.site")}</span>
        ),
    },
    {
      id: "topics",
      header: t("realtime.col.topics"),
      accessor: (s) => s.topics.join(","),
      headClass: "uppercase tracking-wide text-xs",
      cell: (s) => (
        <>
          {s.topics.map((t, i) => (
            <code
              key={`${s.id}-t-${i}`}
              class="font-mono mr-1 inline-block rounded bg-muted px-1.5 py-0.5 text-xs"
            >
              {t}
            </code>
          ))}
        </>
      ),
    },
    {
      id: "created",
      header: t("realtime.col.created"),
      accessor: "created_at",
      headClass: "uppercase tracking-wide text-xs",
      class: "text-xs text-muted-foreground",
      cell: (s) => relativeTime(t, s.created_at),
    },
    {
      id: "dropped",
      header: t("realtime.col.dropped"),
      accessor: "dropped",
      align: "right",
      headClass: "uppercase tracking-wide text-xs",
      cell: (s) => (
        <span
          class={
            "tabular-nums " +
            (s.dropped > 0
              ? "font-semibold text-destructive"
              : "text-muted-foreground")
          }
        >
          {s.dropped}
        </span>
      ),
    },
  ];
}

export function RealtimeScreen() {
  const { t } = useT();
  const q = useQuery({
    queryKey: ["realtime-stats"],
    queryFn: () => adminAPI.realtimeStats(),
    refetchInterval: 5_000,
  });

  const stats = q.data;
  const subs: RealtimeSubscription[] = stats?.subscriptions ?? [];
  const totalDropped = subs.reduce((acc, s) => acc + (s.dropped ?? 0), 0);

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("realtime.title")}
        description={t("realtime.description")}
      />

      <AdminPage.Body className="space-y-4">
      {/* Stats banner */}
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <StatCard label={t("realtime.stat.active")} value={stats?.subscription_count ?? 0} />
        <StatCard
          label={t("realtime.stat.dropped")}
          value={totalDropped}
          warn={totalDropped > 0}
        />
        <StatCard
          label={t("realtime.stat.autoRefresh")}
          value={t("realtime.stat.autoRefreshValue")}
          hint={t("realtime.stat.autoRefreshHint")}
        />
      </div>

      {/* Table — error rendered above; empty + loading handled inline by QDatatable. */}
      {q.isError ? (
        <Alert variant="destructive">
          <AlertDescription>
            {t("realtime.error.load", { error: String(q.error) })}
          </AlertDescription>
        </Alert>
      ) : (
        <Card>
          <CardContent class="p-3">
            <QDatatable
              columns={buildRealtimeColumns(t)}
              data={subs}
              loading={q.isLoading}
              rowKey="id"
              emptyMessage={
                <span>
                  {t("realtime.empty.title")}
                  <span class="mt-1 block text-xs">
                    {t("realtime.empty.body", {
                      cmd: `curl -N <host>/api/realtime?topics=posts/*`,
                    })}
                  </span>
                </span>
              }
            />
          </CardContent>
        </Card>
      )}
      </AdminPage.Body>
    </AdminPage>
  );
}

function StatCard({
  label,
  value,
  warn,
  hint,
}: {
  label: string;
  value: number | string;
  warn?: boolean;
  hint?: string;
}) {
  return (
    <Card class={warn ? "border-destructive/50 bg-destructive/5" : undefined}>
      <CardHeader class="p-3 pb-1 space-y-0">
        <CardDescription
          class={
            "text-xs uppercase tracking-wide " +
            (warn ? "text-destructive" : "text-muted-foreground")
          }
        >
          {label}
        </CardDescription>
      </CardHeader>
      <CardContent class="p-3 pt-0">
        <CardTitle
          class={
            "text-2xl tabular-nums " + (warn ? "text-destructive" : "")
          }
        >
          {value}
        </CardTitle>
        {hint ? (
          <div class="mt-1 text-[11px] text-muted-foreground">{hint}</div>
        ) : null}
      </CardContent>
    </Card>
  );
}

// shortenID renders the first 8 chars of a UUID for the table cell.
// Operators rarely need the full UUID at a glance; they can copy from
// the broker's API directly if they do.
function shortenID(id: string): string {
  if (!id) return "—";
  if (id.length <= 8) return id;
  return `${id.slice(0, 8)}…`;
}

// relativeTime renders an ISO-8601 instant as "5s ago" / "2m ago".
// Matches the cadence-feel of the rest of the admin UI. Falls back to
// the raw string if parsing fails.
function relativeTime(t: Translator["t"], iso: string): string {
  const d = Date.parse(iso);
  if (Number.isNaN(d)) return iso;
  const secs = Math.max(0, Math.floor((Date.now() - d) / 1000));
  if (secs < 60) return t("realtime.time.secAgo", { n: secs });
  const mins = Math.floor(secs / 60);
  if (mins < 60) return t("realtime.time.minAgo", { n: mins });
  const hours = Math.floor(mins / 60);
  if (hours < 24) return t("realtime.time.hourAgo", { n: hours });
  const days = Math.floor(hours / 24);
  return t("realtime.time.dayAgo", { n: days });
}
