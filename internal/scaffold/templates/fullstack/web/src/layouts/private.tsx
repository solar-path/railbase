import { useEffect, useState } from "preact/hooks";
import { Link, useLocation } from "wouter-preact";
import type { ComponentChildren } from "preact";
import { rb } from "../api.js";
import { userSignal, signOut } from "../auth.js";
import type { Tenant } from "../_generated/tenants.js";

// PrivateLayout — signed-in shell.
//
//   - Top bar: brand + tenant picker + user menu (account, sign out)
//   - Side nav: Dashboard / Tenants / Logs / Members / RBAC links
//                (per-tenant items appear when a tenant is selected)
//
// Tenant selection writes the X-Tenant header on the rb client via
// rb.setTenant(); subsequent collection requests get scoped to that
// tenant by the backend's tenant middleware. The picker remembers
// the last selection in localStorage so reloads land back where the
// user was.

const TENANT_KEY = "rb_tenant_id";

export function PrivateLayout({ children }: { children: ComponentChildren }) {
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [current, setCurrent] = useState<string | null>(null);
  const [, navigate] = useLocation();

  useEffect(() => {
    void (async () => {
      try {
        const list = await rb.tenants.list();
        setTenants(list);
        const stored = localStorage.getItem(TENANT_KEY);
        if (stored && list.some((t) => t.id === stored)) {
          setCurrent(stored);
          rb.setTenant(stored);
        } else if (list.length > 0) {
          setCurrent(list[0].id);
          rb.setTenant(list[0].id);
          localStorage.setItem(TENANT_KEY, list[0].id);
        }
      } catch {
        // ignore — anonymous fall-through handled by app.tsx router
      }
    })();
  }, []);

  const pickTenant = (id: string) => {
    setCurrent(id);
    rb.setTenant(id);
    localStorage.setItem(TENANT_KEY, id);
  };

  const doSignOut = async () => {
    await signOut();
    rb.setTenant(null);
    localStorage.removeItem(TENANT_KEY);
    navigate("/");
  };

  return (
    <div class="min-h-screen bg-slate-50 text-slate-900">
      <header class="border-b border-slate-200 bg-white">
        <div class="mx-auto flex max-w-7xl items-center justify-between px-6 py-3">
          <div class="flex items-center gap-6">
            <Link href="/dashboard" class="text-base font-semibold tracking-tight">Acme</Link>
            <select
              class="rounded border border-slate-300 bg-white px-2 py-1 text-sm"
              value={current ?? ""}
              onChange={(e) => pickTenant((e.target as HTMLSelectElement).value)}
            >
              {tenants.length === 0 ? <option value="">No tenants</option> : null}
              {tenants.map((t) => (
                <option key={t.id} value={t.id}>{t.name}</option>
              ))}
            </select>
          </div>
          <div class="flex items-center gap-4 text-sm">
            <span class="text-slate-500">{userSignal.value?.email}</span>
            <Link href="/account" class="text-slate-700 hover:text-slate-900">Account</Link>
            <button type="button" onClick={doSignOut} class="rounded bg-slate-100 px-3 py-1.5 hover:bg-slate-200">
              Sign out
            </button>
          </div>
        </div>
      </header>
      <div class="mx-auto flex max-w-7xl gap-6 px-6 py-6">
        <aside class="w-48 shrink-0">
          <nav class="flex flex-col gap-1 text-sm">
            <Link href="/dashboard" class="rounded px-3 py-1.5 text-slate-700 hover:bg-slate-100">Dashboard</Link>
            <Link href="/tenants" class="rounded px-3 py-1.5 text-slate-700 hover:bg-slate-100">Workspaces</Link>
            {current ? (
              <>
                <div class="mt-4 px-3 text-xs uppercase tracking-wide text-slate-400">Workspace</div>
                <Link href={`/tenants/${current}/members`} class="rounded px-3 py-1.5 text-slate-700 hover:bg-slate-100">Members</Link>
                <Link href={`/tenants/${current}/logs`} class="rounded px-3 py-1.5 text-slate-700 hover:bg-slate-100">Activity</Link>
                <Link href={`/tenants/${current}/rbac`} class="rounded px-3 py-1.5 text-slate-700 hover:bg-slate-100">Roles</Link>
                <Link href={`/tenants/${current}/settings`} class="rounded px-3 py-1.5 text-slate-700 hover:bg-slate-100">Settings</Link>
              </>
            ) : null}
          </nav>
        </aside>
        <main class="flex-1 rounded-lg border border-slate-200 bg-white p-6">{children}</main>
      </div>
    </div>
  );
}
