import { useEffect, useState } from "preact/hooks";
import { Link } from "wouter-preact";
import { rb } from "../../api.js";
import type { Tenant } from "../../_generated/tenants.js";

// Workspaces index — list + create form. Owner-only "delete" lives
// on the per-workspace settings page; this screen is the directory.

export function TenantsPage() {
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = async () => {
    setLoading(true);
    try {
      setTenants(await rb.tenants.list());
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const create = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      await rb.tenants.create({ name, slug: slug || undefined });
      setName(""); setSlug("");
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Create failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="space-y-8">
      <div class="flex items-center justify-between">
        <h1 class="text-2xl font-semibold">Workspaces</h1>
      </div>

      {loading ? (
        <p class="text-sm text-slate-500">Loading…</p>
      ) : (
        <ul class="divide-y divide-slate-200 rounded border border-slate-200 bg-white">
          {tenants.length === 0 ? (
            <li class="px-4 py-6 text-center text-sm text-slate-500">
              No workspaces yet. Create one below.
            </li>
          ) : null}
          {tenants.map((t) => (
            <li key={t.id} class="flex items-center justify-between px-4 py-3">
              <div>
                <Link href={`/tenants/${t.id}/members`} class="font-medium hover:underline">
                  {t.name}
                </Link>
                <div class="font-mono text-xs text-slate-500">{t.slug}</div>
              </div>
              <span class="text-xs text-slate-400">created {new Date(t.created_at).toLocaleDateString()}</span>
            </li>
          ))}
        </ul>
      )}

      <section class="rounded border border-slate-200 bg-white p-5">
        <h2 class="text-base font-semibold">Create workspace</h2>
        <form onSubmit={create} class="mt-4 space-y-3">
          <div>
            <label class="mb-1 block text-sm font-medium text-slate-700">Name</label>
            <input
              type="text" required maxLength={120}
              value={name}
              onInput={(e) => setName((e.target as HTMLInputElement).value)}
              class="w-full rounded border border-slate-300 px-3 py-2"
            />
          </div>
          <div>
            <label class="mb-1 block text-sm font-medium text-slate-700">Slug (optional)</label>
            <input
              type="text"
              value={slug}
              onInput={(e) => setSlug((e.target as HTMLInputElement).value)}
              placeholder="auto-derived from name"
              class="w-full rounded border border-slate-300 px-3 py-2"
            />
          </div>
          {err ? <p class="text-sm text-red-600">{err}</p> : null}
          <button
            type="submit" disabled={busy || !name}
            class="rounded bg-slate-900 px-4 py-2 text-sm text-white hover:bg-slate-800 disabled:opacity-50"
          >
            {busy ? "Creating…" : "Create workspace"}
          </button>
        </form>
      </section>
    </div>
  );
}
