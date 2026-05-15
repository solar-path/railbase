import { lazy, Suspense } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Link } from "wouter-preact";
import { AdminPage } from "../layout/admin_page";
import { useMetricBuffer, useMetricRate } from "../hooks/use_metric_buffer";
import { useT, type Translator } from "../i18n";

// Lazy: keeps Recharts in its own chunk shared with HealthScreen.
const TrendChart = lazy(() => import("../components/trend_chart"));
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import type { AuditEvent } from "../api/types";

// Dashboard — minimal v0.8 cut: collection count, recent audit
// events, links to deep screens. The "stats cards / health checks /
// charts" rich variant from docs/12 §Dashboard lands in v1 along
// with the metrics endpoint.

export function DashboardScreen() {
  const tr = useT();
  const { t } = tr;
  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const auditQ = useQuery({
    queryKey: ["audit", { perPage: 10 }],
    queryFn: () => adminAPI.audit({ perPage: 10 }),
    refetchInterval: 15_000,
  });
  // Poll the health endpoint at 15s cadence so the dashboard can show
  // a "live activity" trend without slamming the backend. Re-uses the
  // same TanStack Query cache the Health screen polls more frequently.
  const healthQ = useQuery({
    queryKey: ["admin-health"],
    queryFn: () => adminAPI.health(),
    refetchInterval: 15_000,
  });
  // Same cadence for the metrics snapshot — the dashboard's trend
  // strip shows request + error rates derived from monotonic counters.
  const metricsQ = useQuery({
    queryKey: ["admin-metrics"],
    queryFn: () => adminAPI.metrics(),
    refetchInterval: 15_000,
  });
  const auditTrend = useMetricBuffer(
    healthQ.data?.audit.last_24h,
    healthQ.data?.now,
    16,
  );
  const jobsTrend = useMetricBuffer(
    healthQ.data
      ? healthQ.data.jobs.pending + healthQ.data.jobs.running
      : null,
    healthQ.data?.now,
    16,
  );
  // Counter-derived rates. Per-MINUTE for both since the dashboard
  // poll cadence is 15 s and a "/sec" value would round to zero on
  // typical traffic; /min keeps the headline meaningful.
  const reqRate = useMetricRate(
    metricsQ.data?.counters["http.requests_total"],
    metricsQ.data?.snapshot_at,
    16,
    60,
  );
  const errRate = useMetricRate(
    (metricsQ.data?.counters["http.errors_4xx_total"] ?? 0) +
      (metricsQ.data?.counters["http.errors_5xx_total"] ?? 0),
    metricsQ.data?.snapshot_at,
    16,
    60,
  );

  return (
    <AdminPage className="space-y-6">
      <AdminPage.Header
        title={t("dashboard.title")}
        description={t("dashboard.subtitle")}
      />

      <AdminPage.Body className="space-y-6">
      <section class="grid grid-cols-2 md:grid-cols-4 gap-3">
        <StatCard label={t("dashboard.collections")} value={schemaQ.data?.count ?? "—"} href="/schema" />
        <StatCard label={t("dashboard.auditEvents")} value={auditQ.data?.totalItems ?? "—"} href="/logs/audit" />
        <StatCard label={t("dashboard.settings")} value="↗" href="/settings" />
        <StatCard label={t("dashboard.docs")} value="↗" href="https://github.com/railbase/railbase" external />
      </section>

      {/* Live trend strip — last ~4 min (16 polls × 15s). Trend-first
          per docs/14 §Health: shows direction, not just current scalar.
          v1.7.x §3.11 expansion: HTTP request + error rates from the
          /metrics endpoint join the original audit-24h + jobs-in-flight
          pair. Two-row layout on small screens keeps each card
          full-width legible; collapses to 4 columns on lg+ for a
          single-glance summary. Recharts is lazy-loaded into a shared
          chunk with the Health screen. */}
      <section class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <TrendStripCard
          tr={tr}
          title={t("dashboard.requestsPerMin")}
          value={reqRate.rate != null ? reqRate.rate.toFixed(0) : "—"}
          data={reqRate.samples}
          href="/logs/health"
          intent="primary"
        />
        <TrendStripCard
          tr={tr}
          title={t("dashboard.errorsPerMin")}
          value={errRate.rate != null ? errRate.rate.toFixed(1) : "—"}
          data={errRate.samples}
          href="/logs/health"
          intent={errRate.rate != null && errRate.rate > 0 ? "warn" : "neutral"}
        />
        <TrendStripCard
          tr={tr}
          title={t("dashboard.auditEvents24h")}
          value={healthQ.data?.audit.last_24h ?? "—"}
          data={auditTrend}
          href="/logs/audit"
          intent="info"
        />
        <TrendStripCard
          tr={tr}
          title={t("dashboard.jobsInFlight")}
          value={
            healthQ.data
              ? healthQ.data.jobs.pending + healthQ.data.jobs.running
              : "—"
          }
          data={jobsTrend}
          href="/data/_jobs"
          intent="primary"
        />
      </section>

      <section class="space-y-2">
        <h2 class="text-sm font-medium text-foreground">{t("dashboard.recentAuditEvents")}</h2>
        <QDatatable
          columns={buildRecentAuditColumns(t)}
          data={(auditQ.data?.items ?? []).slice(0, 10)}
          loading={auditQ.isLoading}
          rowKey="seq"
          pageSize={10}
          emptyMessage={t("dashboard.noEvents")}
        />
      </section>
      </AdminPage.Body>
    </AdminPage>
  );
}

function TrendStripCard({
  tr,
  title,
  value,
  data,
  href,
  intent,
}: {
  tr: Translator;
  title: string;
  value: number | string;
  data: ReadonlyArray<{ t: number; v: number }>;
  href: string;
  intent: "neutral" | "primary" | "warn" | "danger" | "info";
}) {
  return (
    <Link href={href}>
      <Card class="transition-colors hover:border-ring">
        <CardHeader class="p-3 pb-1 space-y-0 flex flex-row items-baseline justify-between">
          <CardDescription class="text-xs uppercase tracking-wide text-muted-foreground">
            {title}
          </CardDescription>
          <CardTitle class="text-xl tabular-nums">{value}</CardTitle>
        </CardHeader>
        <CardContent class="p-3 pt-1">
          <div class="h-16 -mx-1">
            {data.length >= 2 ? (
              <Suspense
                fallback={
                  <div class="h-full flex items-center justify-center text-[10px] text-muted-foreground">
                    {tr.t("dashboard.loadingChart")}
                  </div>
                }
              >
                <TrendChart data={data.slice()} intent={intent} />
              </Suspense>
            ) : (
              <div class="h-full flex items-center justify-center text-[10px] text-muted-foreground">
                {tr.t("dashboard.warmingUp", { have: data.length, need: 2 })}
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}

function StatCard({
  label,
  value,
  href,
  external,
}: {
  label: string;
  value: string | number;
  href: string;
  external?: boolean;
}) {
  const inner = (
    <Card class="transition-colors hover:border-ring">
      <CardHeader class="p-3 pb-1 space-y-0">
        <CardDescription class="text-xs">{label}</CardDescription>
      </CardHeader>
      <CardContent class="p-3 pt-1">
        <CardTitle class="text-2xl">{value}</CardTitle>
      </CardContent>
    </Card>
  );
  if (external) {
    return (
      <a href={href} target="_blank" rel="noreferrer">
        {inner}
      </a>
    );
  }
  return <Link href={href}>{inner}</Link>;
}

// buildRecentAuditColumns is a factory (not a module-level const)
// because column headers go through useT's `t` and have to capture
// the active locale per render. The function is cheap and the array
// shape doesn't churn between renders for the same locale, so
// QDatatable's identity-based memo is unaffected in practice.
function buildRecentAuditColumns(t: Translator["t"]): ColumnDef<AuditEvent>[] {
  return [
    {
      id: "seq",
      header: t("dashboard.col.seq"),
      accessor: "seq",
      cell: (e) => <span class="font-mono">{e.seq}</span>,
    },
    {
      id: "event",
      header: t("dashboard.col.event"),
      accessor: "event",
      cell: (e) => <span class="font-mono">{e.event}</span>,
    },
    {
      id: "outcome",
      header: t("dashboard.col.outcome"),
      accessor: "outcome",
      cell: (e) => <Badge variant={outcomeVariant(e.outcome)}>{e.outcome}</Badge>,
    },
    {
      id: "at",
      header: t("dashboard.col.at"),
      accessor: "at",
      cell: (e) => <span class="font-mono text-muted-foreground">{e.at}</span>,
    },
  ];
}

function outcomeVariant(o: string): "default" | "secondary" | "destructive" | "outline" {
  switch (o) {
    case "success": return "secondary";
    case "denied":  return "outline";
    case "failed":  return "destructive";
    case "error":   return "destructive";
    default:        return "outline";
  }
}
