import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { RealtimeSubscription } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Alert, AlertDescription } from "@/lib/ui/alert.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

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
    <AdminPage>
      <AdminPage.Header
        title="Realtime monitor"
        description="Live snapshot of SSE subscriptions on this replica. Polls every 5 s."
      />

      <AdminPage.Body className="space-y-4">
      {/* Stats banner */}
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
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
        <div class="text-sm text-muted-foreground">Loading…</div>
      ) : q.isError ? (
        <Alert variant="destructive">
          <AlertDescription>
            Failed to load realtime stats. Is the realtime broker wired? ({String(q.error)})
          </AlertDescription>
        </Alert>
      ) : subs.length === 0 ? (
        <Card class="border-dashed bg-muted">
          <CardContent class="p-6 text-center text-sm text-muted-foreground">
            No active subscriptions.
            <div class="mt-1 text-xs">
              Connect with{" "}
              <code class="font-mono rounded bg-card px-1">
                curl -N {`<host>`}/api/realtime?topics=posts/*
              </code>{" "}
              to see one appear here.
            </div>
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent class="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead class="uppercase tracking-wide text-xs">User</TableHead>
                  <TableHead class="uppercase tracking-wide text-xs">Tenant</TableHead>
                  <TableHead class="uppercase tracking-wide text-xs">Topics</TableHead>
                  <TableHead class="uppercase tracking-wide text-xs">Created</TableHead>
                  <TableHead class="text-right uppercase tracking-wide text-xs">Dropped</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {subs.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell class="align-top">
                      <span class="font-mono text-xs">{shortenID(s.user_id)}</span>
                    </TableCell>
                    <TableCell class="align-top">
                      {s.tenant_id ? (
                        <span class="font-mono text-xs">{shortenID(s.tenant_id)}</span>
                      ) : (
                        <span class="text-xs text-muted-foreground">site</span>
                      )}
                    </TableCell>
                    <TableCell class="align-top">
                      {s.topics.map((t, i) => (
                        <code
                          key={`${s.id}-t-${i}`}
                          class="font-mono mr-1 inline-block rounded bg-muted px-1.5 py-0.5 text-xs"
                        >
                          {t}
                        </code>
                      ))}
                    </TableCell>
                    <TableCell class="align-top text-xs text-muted-foreground">
                      {relativeTime(s.created_at)}
                    </TableCell>
                    <TableCell
                      class={
                        "text-right align-top tabular-nums " +
                        (s.dropped > 0
                          ? "font-semibold text-destructive"
                          : "text-muted-foreground")
                      }
                    >
                      {s.dropped}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
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
