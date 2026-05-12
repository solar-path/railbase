import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { CacheInstance } from "../api/types";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Button } from "@/lib/ui/button.ui";
import { Alert, AlertDescription } from "@/lib/ui/alert.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

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

  return (
    <div class="space-y-4">
      <header>
        <h1 class="text-2xl font-semibold">Cache inspector</h1>
        <p class="text-sm text-muted-foreground">
          Live snapshot of registered in-process caches. Polls every 5&nbsp;s.
          The Clear button drops every entry AND resets the hit/miss
          counters for the selected instance.
        </p>
      </header>

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

      {/* Table / empty / error / loading */}
      {q.isLoading ? (
        <div class="text-sm text-muted-foreground">Loading…</div>
      ) : q.isError ? (
        <Alert variant="destructive">
          <AlertDescription>
            Failed to load cache instances: {String(q.error)}
          </AlertDescription>
        </Alert>
      ) : instances.length === 0 ? (
        <Card class="border-dashed bg-muted">
          <CardContent class="p-6 text-center text-sm text-muted-foreground">
            No caches registered.
            <div class="mt-2 max-w-xl mx-auto text-xs">
              The cache primitive ships with v1.5.1 but per-subsystem wiring is
              gradual — instances will appear here as they&apos;re registered in{" "}
              <code class="rb-mono rounded bg-card px-1">app.go</code>.
            </div>
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent class="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead class="uppercase tracking-wide text-xs">Name</TableHead>
                  <TableHead class="text-right uppercase tracking-wide text-xs">Size</TableHead>
                  <TableHead class="text-right uppercase tracking-wide text-xs">Hits</TableHead>
                  <TableHead class="text-right uppercase tracking-wide text-xs">Misses</TableHead>
                  <TableHead class="text-right uppercase tracking-wide text-xs">Hit rate</TableHead>
                  <TableHead class="text-right uppercase tracking-wide text-xs">Evictions</TableHead>
                  <TableHead class="text-right uppercase tracking-wide text-xs">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {instances.map((c) => {
                  const reqs = c.stats.hits + c.stats.misses;
                  const isClearing =
                    clearM.isPending && clearM.variables === c.name;
                  return (
                    <TableRow key={c.name}>
                      <TableCell class="align-top">
                        <span class="rb-mono text-xs">{c.name}</span>
                      </TableCell>
                      <TableCell class="text-right align-top tabular-nums">
                        {c.stats.size.toLocaleString()}
                      </TableCell>
                      <TableCell class="text-right align-top tabular-nums">
                        {c.stats.hits.toLocaleString()}
                      </TableCell>
                      <TableCell class="text-right align-top tabular-nums">
                        {c.stats.misses.toLocaleString()}
                      </TableCell>
                      <TableCell class="text-right align-top tabular-nums">
                        {reqs > 0 ? `${c.stats.hit_rate_pct}%` : (
                          <span class="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell
                        class={
                          "text-right align-top tabular-nums " +
                          (c.stats.evictions > 0
                            ? "text-amber-700"
                            : "text-muted-foreground")
                        }
                      >
                        {c.stats.evictions.toLocaleString()}
                      </TableCell>
                      <TableCell class="text-right align-top">
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={isClearing}
                          onClick={() => {
                            if (
                              window.confirm(
                                `Clear cache "${c.name}"?\n\nThis drops every entry AND resets the hit/miss counters. Active loads in flight will complete; subsequent reads will miss and reload.`,
                              )
                            ) {
                              clearM.mutate(c.name);
                            }
                          }}
                        >
                          {isClearing ? "Clearing…" : "Clear"}
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
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
    <Card class={warn ? "border-amber-300 bg-amber-50" : undefined}>
      <CardHeader class="p-3 pb-1 space-y-0">
        <CardDescription
          class={
            "text-xs uppercase tracking-wide " +
            (warn ? "text-amber-800" : "text-muted-foreground")
          }
        >
          {label}
        </CardDescription>
      </CardHeader>
      <CardContent class="p-3 pt-0">
        <CardTitle
          class={
            "text-2xl tabular-nums " + (warn ? "text-amber-800" : "")
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
