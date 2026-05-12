import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Alert, AlertDescription } from "@/lib/ui/alert.ui";

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

  return (
    <div class="space-y-6">
      <header>
        <h1 class="text-2xl font-semibold">Health &amp; metrics</h1>
        <p class="text-sm text-muted-foreground">
          Live snapshot of runtime, DB pool, jobs, audit, logs, realtime,
          and backups. Polls every 5&nbsp;s.
        </p>
      </header>

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
    </div>
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
