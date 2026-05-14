import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { CacheInstance } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Alert, AlertDescription } from "@/lib/ui/alert.ui";
import { QDatatable, type ColumnDef, type RowAction } from "@/lib/ui/QDatatable.ui";

// Cache inspector admin screen — read-only listing of registered
// cache.Cache instances + a manual Clear action per row. Backend:
// GET /api/_admin/cache, POST /api/_admin/cache/{name}/clear (v1.7.x
// §3.11 slice). Polls every 5 s so operators see hit-rate / size /
// eviction trends without a manual reload.
//
// Empty state matters: the cache primitive ships in v1.5.1 but per-
// subsystem wiring (settings, roles, jobs scheduler, etc.) lands
// gradually — operators visiting this screen during the rollout
// expect a copy explaining "instances appear here as they're wired",
// not a generic "nothing here yet".
//
// Why no edit / resize: operator-side cache tuning is a CLI/config
// concern; the inspector is observability + the one nuclear button
// (Clear). Surgical knobs (drop one key, change capacity) are a
// future slice if metrics show real demand.

// Column set for the cache instance table. All read-only formatting;
// the per-row Clear action is wired in the component via rowActions.
const columns: ColumnDef<CacheInstance>[] = [
  {
    id: "name",
    header: "Name",
    accessor: "name",
    headClass: "uppercase tracking-wide text-xs",
    cell: (c) => <span class="font-mono text-xs">{c.name}</span>,
  },
  {
    id: "size",
    header: "Size",
    accessor: (c) => c.stats.size,
    align: "right",
    headClass: "uppercase tracking-wide text-xs",
    class: "tabular-nums",
    cell: (c) => c.stats.size.toLocaleString(),
  },
  {
    id: "hits",
    header: "Hits",
    accessor: (c) => c.stats.hits,
    align: "right",
    headClass: "uppercase tracking-wide text-xs",
    class: "tabular-nums",
    cell: (c) => c.stats.hits.toLocaleString(),
  },
  {
    id: "misses",
    header: "Misses",
    accessor: (c) => c.stats.misses,
    align: "right",
    headClass: "uppercase tracking-wide text-xs",
    class: "tabular-nums",
    cell: (c) => c.stats.misses.toLocaleString(),
  },
  {
    id: "hit_rate",
    header: "Hit rate",
    accessor: (c) => c.stats.hit_rate_pct,
    align: "right",
    headClass: "uppercase tracking-wide text-xs",
    class: "tabular-nums",
    cell: (c) => {
      const reqs = c.stats.hits + c.stats.misses;
      return reqs > 0 ? (
        `${c.stats.hit_rate_pct}%`
      ) : (
        <span class="text-muted-foreground">—</span>
      );
    },
  },
  {
    id: "evictions",
    header: "Evictions",
    accessor: (c) => c.stats.evictions,
    align: "right",
    headClass: "uppercase tracking-wide text-xs",
    class: "tabular-nums",
    cell: (c) => (
      <span
        class={
          c.stats.evictions > 0 ? "text-foreground" : "text-muted-foreground"
        }
      >
        {c.stats.evictions.toLocaleString()}
      </span>
    ),
  },
];

export function CacheScreen() {
  const qc = useQueryClient();

  const q = useQuery({
    queryKey: ["cache-instances"],
    queryFn: () => adminAPI.cacheList(),
    refetchInterval: 5_000,
  });

  const clearM = useMutation({
    mutationFn: (name: string) => adminAPI.cacheClear(name),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["cache-instances"] });
    },
  });

  const instances: CacheInstance[] = q.data?.instances ?? [];

  // Aggregate totals across every registered instance. Computed once
  // per render rather than memoised — the list is tiny (typically a
  // handful of caches) so the savings are negligible.
  const totals = instances.reduce(
    (acc, c) => {
      acc.hits += c.stats.hits;
      acc.misses += c.stats.misses;
      acc.evictions += c.stats.evictions;
      acc.size += c.stats.size;
      return acc;
    },
    { hits: 0, misses: 0, evictions: 0, size: 0 },
  );
  const totalRequests = totals.hits + totals.misses;
  const overallHitRate =
    totalRequests > 0
      ? Math.round((totals.hits / totalRequests) * 1000) / 10
      : 0;

  // Per-row Clear — same window.confirm guard as before. The
  // disabled state localises to the in-flight instance.
  const rowActions = (c: CacheInstance): RowAction<CacheInstance>[] => [
    {
      label:
        clearM.isPending && clearM.variables === c.name
          ? "Clearing…"
          : "Clear",
      disabled: () => clearM.isPending && clearM.variables === c.name,
      onSelect: () => {
        if (
          window.confirm(
            `Clear cache "${c.name}"?\n\nThis drops every entry AND resets the hit/miss counters. Active loads in flight will complete; subsequent reads will miss and reload.`,
          )
        ) {
          clearM.mutate(c.name);
        }
      },
    },
  ];

  return (
    <AdminPage>
      <AdminPage.Header
        title="Cache inspector"
        description={
          <>
            Live snapshot of registered in-process caches. Polls every 5&nbsp;s.
            The Clear button drops every entry AND resets the hit/miss
            counters for the selected instance.
          </>
        }
      />

      <AdminPage.Body className="space-y-4">
      {/* Aggregate stats — totals across every registered instance. */}
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-5">
        <StatCard label="Total hits" value={totals.hits.toLocaleString()} />
        <StatCard label="Total misses" value={totals.misses.toLocaleString()} />
        <StatCard
          label="Overall hit rate"
          value={totalRequests > 0 ? `${overallHitRate}%` : "—"}
          hint={totalRequests > 0 ? `${totalRequests.toLocaleString()} reqs` : "no requests yet"}
        />
        <StatCard label="Total entries" value={totals.size.toLocaleString()} />
        <StatCard
          label="Total evictions"
          value={totals.evictions.toLocaleString()}
          warn={totals.evictions > 0}
        />
      </div>

      {/* Table / error — empty state handled by QDatatable's emptyMessage. */}
      {q.isError ? (
        <Alert variant="destructive">
          <AlertDescription>
            Failed to load cache instances: {String(q.error)}
          </AlertDescription>
        </Alert>
      ) : (
        <Card>
          <CardContent class="p-3">
            <QDatatable
              columns={columns}
              data={instances}
              loading={q.isLoading}
              rowKey="name"
              rowActions={rowActions}
              emptyMessage={
                <span>
                  No caches registered.
                  <span class="mt-2 block max-w-xl mx-auto text-xs">
                    The cache primitive ships with v1.5.1 but per-subsystem
                    wiring is gradual — instances will appear here as
                    they&apos;re registered in{" "}
                    <code class="font-mono rounded bg-card px-1">app.go</code>.
                  </span>
                </span>
              }
            />
          </CardContent>
        </Card>
      )}

      {clearM.isError ? (
        <Alert variant="destructive">
          <AlertDescription class="text-xs">
            Failed to clear: {String(clearM.error)}
          </AlertDescription>
        </Alert>
      ) : null}
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
    <Card class={warn ? "border-input bg-muted" : undefined}>
      <CardHeader class="p-3 pb-1 space-y-0">
        <CardDescription
          class={
            "text-xs uppercase tracking-wide " +
            (warn ? "text-foreground" : "text-muted-foreground")
          }
        >
          {label}
        </CardDescription>
      </CardHeader>
      <CardContent class="p-3 pt-0">
        <CardTitle
          class={
            "text-2xl tabular-nums " + (warn ? "text-foreground" : "")
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
