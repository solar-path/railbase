import { useEffect, useState } from "preact/hooks";
import { Link } from "wouter-preact";
import { rb } from "../../api.js";
import { userSignal } from "../../auth.js";
import type { Tenant, TenantLogEntry } from "../../_generated/tenants.js";

// Dashboard — landing screen for signed-in users.
//
// Renders:
//   - Greeting with caller's email
//   - Workspace count + quick CTA to /tenants
//   - "Recent activity" — last 5 audit rows scoped to the currently-
//     selected tenant (via rb.tenants.listLogs). Activity is the
//     single most useful zero-config widget for a fresh tenant; if
//     it's empty the empty-state nudges the user to invite a team
//     member, which is the canonical first action.

export function DashboardPage() {
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [recent, setRecent] = useState<TenantLogEntry[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    void (async () => {
      try {
        const list = await rb.tenants.list();
        setTenants(list);
        if (list.length > 0) {
          const stored = localStorage.getItem("rb_tenant_id") ?? list[0].id;
          const target = list.some((t) => t.id === stored) ? stored : list[0].id;
          const logs = await rb.tenants.listLogs(target, { perPage: 5 });
          setRecent(logs.items);
        }
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  return (
    <div class="space-y-6">
      <header>
        <h1 class="text-2xl font-semibold">Welcome back</h1>
        <p class="text-sm text-slate-500">{userSignal.value?.email}</p>
      </header>

      <div class="grid gap-4 md:grid-cols-3">
        <Stat label="Workspaces" value={loading ? "…" : String(tenants.length)} cta={<Link href="/tenants" class="text-slate-600 underline">Manage</Link>} />
        <Stat label="Recent activity" value={loading ? "…" : String(recent.length)} cta={<span class="text-slate-400">last 5</span>} />
        <Stat label="Account" value="Settings" cta={<Link href="/account" class="text-slate-600 underline">Open</Link>} />
      </div>

      <section>
        <h2 class="mb-3 text-base font-semibold">Recent activity</h2>
        {loading ? (
          <p class="text-sm text-slate-500">Loading…</p>
        ) : recent.length === 0 ? (
          <div class="rounded border border-dashed border-slate-300 p-6 text-center text-sm text-slate-500">
            Quiet here. Invite a teammate from
            <Link href="/tenants" class="ml-1 underline">Workspaces</Link>.
          </div>
        ) : (
          <ul class="divide-y divide-slate-200 rounded border border-slate-200 bg-white">
            {recent.map((e) => (
              <li key={e.id} class="flex items-center justify-between px-4 py-2 text-sm">
                <div>
                  <span class="font-mono text-xs text-slate-500">{e.event}</span>
                  <span class="ml-2">{e.outcome}</span>
                </div>
                <span class="text-xs text-slate-400">{new Date(e.at).toLocaleString()}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function Stat({ label, value, cta }: { label: string; value: string; cta: preact.ComponentChildren }) {
  return (
    <div class="rounded-lg border border-slate-200 bg-white p-4">
      <div class="text-xs uppercase tracking-wide text-slate-500">{label}</div>
      <div class="mt-2 text-2xl font-semibold">{value}</div>
      <div class="mt-3 text-sm">{cta}</div>
    </div>
  );
}
