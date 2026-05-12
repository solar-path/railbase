import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { RealtimeSubscription } from "../api/types";

// Realtime monitor admin screen — read-only snapshot of active SSE
// subscriptions on the broker. Backend: GET /api/_admin/realtime
// (v1.7.16 §3.11 slice).
//
// Polls every 5 s so the operator sees subscriptions arrive/depart
// without a manual reload. No unsubscribe/disconnect actions —
// surgically kicking subs is rarely the right tool (fix the slow
// client, not the symptom). Per-row drop count surfaces in red when
// non-zero so operators notice backpressure events.

export function RealtimeScreen() {
  const q = useQuery({
    queryKey: ["realtime-stats"],
    queryFn: () => adminAPI.realtimeStats(),
    refetchInterval: 5_000,
  });

  const stats = q.data;
  const subs: RealtimeSubscription[] = stats?.subscriptions ?? [];
  const totalDropped = subs.reduce((acc, s) => acc + (s.dropped ?? 0), 0);

  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">Realtime monitor</h1>
        <p className="text-sm text-neutral-500">
          Live snapshot of SSE subscriptions on this replica. Polls every 5 s.
        </p>
      </header>

      {/* Stats banner */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <StatCard label="Active subscriptions" value={stats?.subscription_count ?? 0} />
        <StatCard
          label="Total events dropped"
          value={totalDropped}
          warn={totalDropped > 0}
        />
        <StatCard
          label="Auto-refresh"
          value="5 s"
          hint="Polling cadence; subscription state updates without reload."
        />
      </div>

      {/* Table */}
      {q.isLoading ? (
        <div className="text-sm text-neutral-500">Loading…</div>
      ) : q.isError ? (
        <div className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          Failed to load realtime stats. Is the realtime broker wired? ({String(q.error)})
        </div>
      ) : subs.length === 0 ? (
        <div className="rounded border-2 border-dashed border-neutral-300 bg-neutral-50 p-6 text-center text-sm text-neutral-500">
          No active subscriptions.
          <div className="mt-1 text-xs text-neutral-400">
            Connect with{" "}
            <code className="rb-mono rounded bg-neutral-100 px-1">
              curl -N {`<host>`}/api/realtime?topics=posts/*
            </code>{" "}
            to see one appear here.
          </div>
        </div>
      ) : (
        <div className="overflow-x-auto rounded border border-neutral-200">
          <table className="w-full text-sm">
            <thead className="bg-neutral-50 text-left text-xs font-medium uppercase tracking-wide text-neutral-500">
              <tr>
                <th className="px-3 py-2">User</th>
                <th className="px-3 py-2">Tenant</th>
                <th className="px-3 py-2">Topics</th>
                <th className="px-3 py-2">Created</th>
                <th className="px-3 py-2 text-right">Dropped</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-neutral-200">
              {subs.map((s) => (
                <tr key={s.id}>
                  <td className="px-3 py-2 align-top">
                    <span className="rb-mono text-xs">{shortenID(s.user_id)}</span>
                  </td>
                  <td className="px-3 py-2 align-top">
                    {s.tenant_id ? (
                      <span className="rb-mono text-xs">{shortenID(s.tenant_id)}</span>
                    ) : (
                      <span className="text-xs text-neutral-400">site</span>
                    )}
                  </td>
                  <td className="px-3 py-2 align-top">
                    {s.topics.map((t, i) => (
                      <code
                        key={`${s.id}-t-${i}`}
                        className="rb-mono mr-1 inline-block rounded bg-neutral-100 px-1.5 py-0.5 text-xs"
                      >
                        {t}
                      </code>
                    ))}
                  </td>
                  <td className="px-3 py-2 align-top text-xs text-neutral-500">
                    {relativeTime(s.created_at)}
                  </td>
                  <td
                    className={`px-3 py-2 text-right align-top tabular-nums ${
                      s.dropped > 0 ? "font-semibold text-red-700" : "text-neutral-500"
                    }`}
                  >
                    {s.dropped}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
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
        warn
          ? "border-red-300 bg-red-50"
          : "border-neutral-200 bg-white"
      }`}
    >
      <div className={`text-xs uppercase tracking-wide ${warn ? "text-red-700" : "text-neutral-500"}`}>
        {label}
      </div>
      <div className={`mt-1 text-2xl font-semibold tabular-nums ${warn ? "text-red-700" : ""}`}>
        {value}
      </div>
      {hint ? (
        <div className="mt-1 text-[11px] text-neutral-400">{hint}</div>
      ) : null}
    </div>
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
function relativeTime(iso: string): string {
  const d = Date.parse(iso);
  if (Number.isNaN(d)) return iso;
  const secs = Math.max(0, Math.floor((Date.now() - d) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
