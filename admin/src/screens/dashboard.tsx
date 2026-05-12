import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Link } from "wouter";

// Dashboard — minimal v0.8 cut: collection count, recent audit
// events, links to deep screens. The "stats cards / health checks /
// charts" rich variant from docs/12 §Dashboard lands in v1 along
// with the metrics endpoint.

export function DashboardScreen() {
  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const auditQ = useQuery({
    queryKey: ["audit", { perPage: 10 }],
    queryFn: () => adminAPI.audit({ perPage: 10 }),
  });

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold">Dashboard</h1>
        <p className="text-sm text-neutral-500">Quick health overview.</p>
      </header>

      <section className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Card label="Collections" value={schemaQ.data?.count ?? "—"} href="/schema" />
        <Card label="Audit events" value={auditQ.data?.totalItems ?? "—"} href="/audit" />
        <Card label="Settings" value="↗" href="/settings" />
        <Card label="Docs" value="↗" href="https://github.com/railbase/railbase" external />
      </section>

      <section className="space-y-2">
        <h2 className="text-sm font-medium text-neutral-700">Recent audit events</h2>
        <div className="rounded border border-neutral-200 bg-white">
          <table className="rb-table">
            <thead>
              <tr>
                <th>seq</th>
                <th>event</th>
                <th>outcome</th>
                <th>at</th>
              </tr>
            </thead>
            <tbody>
              {(auditQ.data?.items ?? []).slice(0, 10).map((e) => (
                <tr key={e.seq}>
                  <td className="rb-mono">{e.seq}</td>
                  <td className="rb-mono">{e.event}</td>
                  <td>
                    <span
                      className={
                        "rounded px-1.5 py-0.5 text-xs " +
                        outcomeColor(e.outcome)
                      }
                    >
                      {e.outcome}
                    </span>
                  </td>
                  <td className="rb-mono text-neutral-500">{e.at}</td>
                </tr>
              ))}
              {auditQ.data?.items.length === 0 ? (
                <tr>
                  <td colSpan={4} className="text-neutral-400 text-center py-4">
                    No events yet.
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

function Card({
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
    <div className="rounded border border-neutral-200 bg-white p-3 hover:border-neutral-300 transition-colors">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="text-2xl font-semibold mt-1">{value}</div>
    </div>
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

function outcomeColor(o: string): string {
  switch (o) {
    case "success": return "bg-emerald-50 text-emerald-700 border border-emerald-200";
    case "denied":  return "bg-amber-50 text-amber-700 border border-amber-200";
    case "failed":  return "bg-red-50 text-red-700 border border-red-200";
    case "error":   return "bg-red-50 text-red-700 border border-red-200";
    default:        return "bg-neutral-50 text-neutral-700 border border-neutral-200";
  }
}
