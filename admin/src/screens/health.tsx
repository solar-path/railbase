import { lazy, Suspense } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { AdminPage } from "../layout/admin_page";
import { useMetricBuffer, useMetricRate } from "../hooks/use_metric_buffer";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Alert, AlertDescription } from "@/lib/ui/alert.ui";

// Recharts is heavy (~80 KB gzip including its dependency on
// d3-shape/d3-scale subsets). We lazy-import it so the Dashboard +
// Health charts go to their own chunk — main bundle stays under the
// docs/12 §Bundle cost target of 145 KB gzip.
const TrendChart = lazy(() => import("../components/trend_chart"));

// Health / metrics admin dashboard — read-only snapshot of runtime,
// DB pool, jobs queue, audit, logs, realtime, backups, and schema
// stats. Backend: GET /api/_admin/health (v1.7.23 §3.11 slice).
//
// Polls every 5 s so the operator sees pool saturation / job backlog /
// goroutine leaks within a few seconds of them happening. Every
// sub-section is independently nil-guarded server-side — a wired-down
// subsystem just shows zero counts.

export function HealthScreen() {
  const q = useQuery({
    queryKey: ["admin-health"],
    queryFn: () => adminAPI.health(),
    refetchInterval: 5_000,
  });
  // Second poll alongside /health for the in-process metric registry.
  // Same 5 s cadence so the HTTP-rate trend and the runtime trend stay
  // visually aligned. Independent query key — TanStack Query handles
  // both concurrently without re-running either.
  const metricsQ = useQuery({
    queryKey: ["admin-metrics"],
    queryFn: () => adminAPI.metrics(),
    refetchInterval: 5_000,
  });

  // Counter-derived rates. The /metrics endpoint returns absolute
  // monotonic counters; useMetricRate derives a per-unit-time series.
  // We pass the absolute value + a pollKey (`snapshot_at`) so the hook
  // dedupes within the same poll AND computes the delta against the
  // previous poll's value. Unit=1 → events / second.
  const reqRate = useMetricRate(
    metricsQ.data?.counters["http.requests_total"],
    metricsQ.data?.snapshot_at,
  );
  // Errors per MINUTE (rather than per second) because the absolute
  // count is usually < 1 / second even on a busy box; showing /min
  // keeps the headline value non-zero at typical traffic levels.
  const errRate = useMetricRate(
    (metricsQ.data?.counters["http.errors_4xx_total"] ?? 0) +
      (metricsQ.data?.counters["http.errors_5xx_total"] ?? 0),
    metricsQ.data?.snapshot_at,
    24,
    60,
  );
  const hooksRate = useMetricRate(
    metricsQ.data?.counters["hooks.invocations_total"],
    metricsQ.data?.snapshot_at,
  );
  // p95 latency is a histogram, not a counter — useMetricBuffer is the
  // right shape since the absolute value at each poll IS the data
  // point (no derivative). Convert ns→ms in the format() callback.
  const p95Ms = metricsQ.data?.histograms?.["http.latency"]?.p95_ns
    ? metricsQ.data.histograms["http.latency"].p95_ns / 1_000_000
    : 0;
  const latTrend = useMetricBuffer(p95Ms, metricsQ.data?.snapshot_at);

  if (q.isLoading) {
    return <div class="text-sm text-muted-foreground">Loading…</div>;
  }
  if (q.isError || !q.data) {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          Failed to load health metrics: {String(q.error)}
        </AlertDescription>
      </Alert>
    );
  }
  const h = q.data;

  // Roll a small ring buffer of recent samples keyed off the response's
  // `now` field. ~24 polls at 5s = ~2 min window. This is purely client-
  // side; on refresh the trend resets to one sample. A future revision
  // can pull historical series from a Prometheus-style /metrics endpoint
  // (docs/12 screen #17 "live charts 1m/5m/1h/24h" target).
  const goroutinesTrend = useMetricBuffer(h.memory.goroutines, h.now);
  const poolTrend = useMetricBuffer(h.pool.acquired, h.now);
  const jobsTrend = useMetricBuffer(h.jobs.pending + h.jobs.running, h.now);
  const memTrend = useMetricBuffer(h.memory.alloc_bytes / 1024 / 1024, h.now);

  return (
    <AdminPage className="space-y-6">
      <AdminPage.Header
        title={<>Health &amp; metrics</>}
        description={
          <>
            Live snapshot of runtime, DB pool, jobs, audit, logs, realtime,
            and backups. Polls every 5&nbsp;s.
          </>
        }
      />

      <AdminPage.Body className="space-y-6">
      {/* Row 1 — runtime / pool / memory */}
      <section class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard
          label="Uptime"
          value={formatDuration(h.uptime_sec)}
          hint={`Started ${formatRelative(h.started_at)}`}
        />
        <StatCard
          label="Goroutines"
          value={h.memory.goroutines}
          warn={h.memory.goroutines > 10_000}
          hint={`> 10k may indicate a leak`}
        />
        <StatCard
          label="Pool conns"
          value={`${h.pool.acquired}/${h.pool.total}`}
          warn={h.pool.max > 0 && h.pool.acquired >= h.pool.max - 1}
          hint={`max ${h.pool.max} • idle ${h.pool.idle}`}
        />
        <StatCard
          label="Memory"
          value={`${(h.memory.alloc_bytes / 1024 / 1024).toFixed(1)} MB`}
          hint={`sys ${(h.memory.sys_bytes / 1024 / 1024).toFixed(0)} MB · ${h.memory.num_gc} GC cycles`}
        />
      </section>

      {/* Trend strip — last ~2 min (24 polls × 5s) of the runtime
          metrics that matter most. Lazy-loaded; main bundle skips
          Recharts. */}
      <section class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <TrendCard
          label="Goroutines"
          value={h.memory.goroutines}
          intent={h.memory.goroutines > 10_000 ? "danger" : "neutral"}
          data={goroutinesTrend}
        />
        <TrendCard
          label="Pool acquired"
          value={h.pool.acquired}
          intent={
            h.pool.max > 0 && h.pool.acquired >= h.pool.max - 1
              ? "warn"
              : "primary"
          }
          data={poolTrend}
        />
        <TrendCard
          label="Jobs in flight"
          value={h.jobs.pending + h.jobs.running}
          intent={
            h.jobs.pending + h.jobs.running > 100 ? "warn" : "info"
          }
          data={jobsTrend}
        />
        <TrendCard
          label="Memory MB"
          value={(h.memory.alloc_bytes / 1024 / 1024).toFixed(1)}
          intent="neutral"
          data={memTrend}
          format={(v) => v.toFixed(1) + " MB"}
        />
      </section>

      {/* HTTP + hooks trend strip — v1.7.x §3.11 / docs/14 §Health.
          Derived from the in-process metric registry via /api/_admin/
          metrics. Rates are computed client-side (useMetricRate)
          against the previous poll's absolute counter — no historical
          series stored server-side, so a page refresh resets the
          trend to one sample. */}
      <section class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <TrendCard
          label="Requests/sec"
          value={reqRate.rate != null ? reqRate.rate.toFixed(1) : "—"}
          intent="primary"
          data={reqRate.samples}
          format={(v) => v.toFixed(2) + "/s"}
        />
        <TrendCard
          label="Errors/min"
          value={errRate.rate != null ? errRate.rate.toFixed(1) : "—"}
          intent={errRate.rate != null && errRate.rate > 0 ? "warn" : "neutral"}
          data={errRate.samples}
          format={(v) => v.toFixed(1) + "/min"}
        />
        <TrendCard
          label="p95 latency"
          value={p95Ms > 0 ? p95Ms.toFixed(0) + " ms" : "—"}
          intent={p95Ms > 500 ? "warn" : "info"}
          data={latTrend}
          format={(v) => v.toFixed(0) + " ms"}
        />
        <TrendCard
          label="Hook invocations/sec"
          value={hooksRate.rate != null ? hooksRate.rate.toFixed(2) : "—"}
          intent="info"
          data={hooksRate.samples}
          format={(v) => v.toFixed(2) + "/s"}
        />
      </section>

      {/* Row 2 — jobs / audit / logs / realtime */}
      <section class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard
          label="Jobs"
          value={`${h.jobs.pending + h.jobs.running}`}
          warn={h.jobs.pending + h.jobs.running > 100}
          hint={`pending ${h.jobs.pending} · running ${h.jobs.running} · failed ${h.jobs.failed} · completed ${h.jobs.completed}`}
        />
        <StatCard
          label="Audit (24h)"
          value={h.audit.last_24h}
          hint={`total ${h.audit.total.toLocaleString()}`}
        />
        <StatCard
          label="Logs (24h)"
          value={h.logs.last_24h}
          warn={(h.logs.by_level?.error ?? 0) > 0}
          hint={`error ${h.logs.by_level?.error ?? 0} · warn ${h.logs.by_level?.warn ?? 0} · info ${h.logs.by_level?.info ?? 0}`}
        />
        <StatCard
          label="Realtime"
          value={h.realtime.subscriptions}
          warn={h.realtime.events_dropped_total > 0}
          hint={`events dropped: ${h.realtime.events_dropped_total}`}
        />
      </section>

      {/* Row 3 — backups / schema */}
      <section class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <StatCard
          label="Backups"
          value={h.backups.count}
          hint={
            h.backups.last_completed_at
              ? `last ${formatRelative(h.backups.last_completed_at)} · ${(h.backups.total_bytes / 1024 / 1024).toFixed(1)} MB total`
              : "no completed backups yet"
          }
        />
        <StatCard
          label="Schema"
          value={`${h.schema.collections} collections`}
          hint={`${h.schema.auth_collections} auth · ${h.schema.tenant_collections} tenant`}
        />
      </section>

      {/* Footer — version */}
      <Card class="bg-muted">
        <CardContent class="p-3 text-xs text-muted-foreground">
          <div>
            <span class="font-medium text-foreground">Railbase {h.version}</span>{" "}
            · {h.go_version}
          </div>
          <div class="mt-1">
            Started at <code class="rounded bg-card px-1 py-0.5">{h.started_at}</code>{" "}
            · now <code class="rounded bg-card px-1 py-0.5">{h.now}</code>
          </div>
        </CardContent>
      </Card>
      </AdminPage.Body>
    </AdminPage>
  );
}

// TrendCard — small Card containing a metric label, its current value,
// and a lazy-loaded TrendChart sparkline of the last ~24 samples. The
// chart only renders when there are ≥ 2 samples; before that we show
// a "warming up…" hint so the screen doesn't look broken on first
// render right after page load.
function TrendCard({
  label,
  value,
  intent,
  data,
  format,
}: {
  label: string;
  value: number | string;
  intent: "neutral" | "primary" | "warn" | "danger" | "info";
  data: ReadonlyArray<{ t: number; v: number }>;
  format?: (v: number) => string;
}) {
  return (
    <Card>
      <CardHeader class="p-3 pb-1 space-y-0">
        <CardDescription class="text-xs uppercase tracking-wide text-muted-foreground">
          {label}
        </CardDescription>
      </CardHeader>
      <CardContent class="p-3 pt-0">
        <CardTitle class="text-xl tabular-nums">{value}</CardTitle>
        <div class="mt-2 h-12 -mx-1">
          {data.length >= 2 ? (
            <Suspense
              fallback={
                <div class="h-full flex items-center justify-center text-[10px] text-muted-foreground">
                  loading chart…
                </div>
              }
            >
              <TrendChart data={data.slice()} intent={intent} format={format} />
            </Suspense>
          ) : (
            <div class="h-full flex items-center justify-center text-[10px] text-muted-foreground">
              warming up… ({data.length}/2 samples)
            </div>
          )}
        </div>
      </CardContent>
    </Card>
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

// formatDuration turns 3725s into "1h 2m 5s". Anything ≥ 1 day adds the
// day prefix. Used only for the human-readable display; the raw second
// count is in the underlying response field.
function formatDuration(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return "—";
  const days = Math.floor(sec / 86_400);
  const hrs = Math.floor((sec % 86_400) / 3600);
  const mins = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  if (days > 0) return `${days}d ${hrs}h ${mins}m`;
  if (hrs > 0) return `${hrs}h ${mins}m ${s}s`;
  if (mins > 0) return `${mins}m ${s}s`;
  return `${s}s`;
}

// formatRelative renders an ISO timestamp as "Nm/h/d ago" using the
// elapsed delta. Falls back to the raw string on parse failure. Cheap
// enough to recompute on every render — no memoization needed.
function formatRelative(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const secondsAgo = Math.max(0, (Date.now() - d.getTime()) / 1000);
  return formatDuration(secondsAgo) + " ago";
}
