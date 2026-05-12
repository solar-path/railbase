import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { CacheInstance } from "../api/types";

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
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">Cache inspector</h1>
        <p className="text-sm text-neutral-500">
          Live snapshot of registered in-process caches. Polls every 5&nbsp;s.
          The Clear button drops every entry AND resets the hit/miss
          counters for the selected instance.
        </p>
      </header>

      {/* Aggregate stats — totals across every registered instance. */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
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
        <div className="text-sm text-neutral-500">Loading…</div>
      ) : q.isError ? (
        <div className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          Failed to load cache instances: {String(q.error)}
        </div>
      ) : instances.length === 0 ? (
        <div className="rounded border-2 border-dashed border-neutral-300 bg-neutral-50 p-6 text-center text-sm text-neutral-500">
          No caches registered.
          <div className="mt-2 max-w-xl mx-auto text-xs text-neutral-400">
            The cache primitive ships with v1.5.1 but per-subsystem wiring is
            gradual — instances will appear here as they&apos;re registered in{" "}
            <code className="rb-mono rounded bg-neutral-100 px-1">app.go</code>.
          </div>
        </div>
      ) : (
        <div className="overflow-x-auto rounded border border-neutral-200">
          <table className="w-full text-sm">
            <thead className="bg-neutral-50 text-left text-xs font-medium uppercase tracking-wide text-neutral-500">
              <tr>
                <th className="px-3 py-2">Name</th>
                <th className="px-3 py-2 text-right">Size</th>
                <th className="px-3 py-2 text-right">Hits</th>
                <th className="px-3 py-2 text-right">Misses</th>
                <th className="px-3 py-2 text-right">Hit rate</th>
                <th className="px-3 py-2 text-right">Evictions</th>
                <th className="px-3 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-neutral-200">
              {instances.map((c) => {
                const reqs = c.stats.hits + c.stats.misses;
                const isClearing =
                  clearM.isPending && clearM.variables === c.name;
                return (
                  <tr key={c.name}>
                    <td className="px-3 py-2 align-top">
                      <span className="rb-mono text-xs">{c.name}</span>
                    </td>
                    <td className="px-3 py-2 text-right align-top tabular-nums">
                      {c.stats.size.toLocaleString()}
                    </td>
                    <td className="px-3 py-2 text-right align-top tabular-nums">
                      {c.stats.hits.toLocaleString()}
                    </td>
                    <td className="px-3 py-2 text-right align-top tabular-nums">
                      {c.stats.misses.toLocaleString()}
                    </td>
                    <td className="px-3 py-2 text-right align-top tabular-nums">
                      {reqs > 0 ? `${c.stats.hit_rate_pct}%` : (
                        <span className="text-neutral-400">—</span>
                      )}
                    </td>
                    <td
                      className={`px-3 py-2 text-right align-top tabular-nums ${
                        c.stats.evictions > 0 ? "text-amber-700" : "text-neutral-500"
                      }`}
                    >
                      {c.stats.evictions.toLocaleString()}
                    </td>
                    <td className="px-3 py-2 text-right align-top">
                      <button
                        type="button"
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
                        className="rounded border border-neutral-300 px-2 py-0.5 text-xs text-neutral-700 hover:bg-neutral-100 disabled:opacity-50"
                      >
                        {isClearing ? "Clearing…" : "Clear"}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {clearM.isError ? (
        <div className="rounded border border-red-300 bg-red-50 p-2 text-xs text-red-800">
          Failed to clear: {String(clearM.error)}
        </div>
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
    <div
      className={`rounded border p-3 ${
        warn ? "border-amber-300 bg-amber-50" : "border-neutral-200 bg-white"
      }`}
    >
      <div
        className={`text-xs uppercase tracking-wide ${
          warn ? "text-amber-800" : "text-neutral-500"
        }`}
      >
        {label}
      </div>
      <div
        className={`mt-1 text-2xl font-semibold tabular-nums ${
          warn ? "text-amber-800" : ""
        }`}
      >
        {value}
      </div>
      {hint ? (
        <div className="mt-1 text-[11px] text-neutral-400">{hint}</div>
      ) : null}
    </div>
  );
}
