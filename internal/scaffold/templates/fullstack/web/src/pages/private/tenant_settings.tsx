import { useParams, Link, useLocation } from "wouter-preact";
import { useEffect, useState } from "preact/hooks";
import { rb } from "../../api.js";
import type {
  Member, TenantLogEntry, TenantRBACRole, Tenant,
} from "../../_generated/tenants.js";

// TenantSettingsPage — single component, tab-routed, handles
//   /tenants/:id              → Settings (name, slug, danger zone)
//   /tenants/:id/members      → roster + invite
//   /tenants/:id/logs         → activity stream
//   /tenants/:id/rbac         → role chips per member
//
// Splitting into 4 components would mean 4 useParams + 4 effects.
// One screen with a tab switch is simpler and exactly mirrors the
// rail/air settings shell.

const TABS = [
  { id: "members", label: "Members" },
  { id: "logs",    label: "Activity" },
  { id: "rbac",    label: "Roles" },
  { id: "settings",label: "Settings" },
] as const;

export function TenantSettingsPage() {
  const params = useParams<{ id: string; tab?: string }>();
  const id = params.id;
  const tab = (params.tab as (typeof TABS)[number]["id"]) || "members";
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [role, setRole] = useState<{ role: string; is_owner: boolean } | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        setTenant(await rb.tenants.get(id));
        const r = await rb.tenants.myRole(id);
        setRole({ role: r.role, is_owner: r.is_owner });
      } catch {/* the layout will render an empty state */}
    })();
  }, [id]);

  return (
    <div class="space-y-6">
      <header>
        <h1 class="text-2xl font-semibold">{tenant?.name ?? "Workspace"}</h1>
        <p class="font-mono text-xs text-slate-500">{tenant?.slug}</p>
      </header>

      <nav class="flex gap-1 border-b border-slate-200">
        {TABS.map((t) => (
          <Link
            key={t.id}
            href={`/tenants/${id}/${t.id}`}
            class={`-mb-px border-b-2 px-3 py-2 text-sm ${
              tab === t.id ? "border-slate-900 font-medium" : "border-transparent text-slate-500 hover:text-slate-900"
            }`}
          >
            {t.label}
          </Link>
        ))}
      </nav>

      {tab === "members" && <MembersTab tenantID={id} canManage={!!role?.is_owner} />}
      {tab === "logs" && <LogsTab tenantID={id} />}
      {tab === "rbac" && <RbacTab tenantID={id} canManage={!!role?.is_owner} />}
      {tab === "settings" && <SettingsTab tenantID={id} canManage={!!role?.is_owner} canDelete={role?.role === "owner"} />}
    </div>
  );
}

// ============================== Members ==============================

function MembersTab({ tenantID, canManage }: { tenantID: string; canManage: boolean }) {
  const [rows, setRows] = useState<Member[]>([]);
  const [loading, setLoading] = useState(true);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<"owner" | "admin" | "member">("member");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = async () => {
    setLoading(true);
    try {
      setRows(await rb.tenants.listMembers(tenantID));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, [tenantID]);

  const invite = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      await rb.tenants.invite(tenantID, { email, role });
      setEmail("");
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Invite failed");
    } finally {
      setBusy(false);
    }
  };

  const changeRole = async (m: Member, next: "owner" | "admin" | "member") => {
    try {
      await rb.tenants.updateMemberRole(tenantID, m.user_id, next);
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Role change failed");
    }
  };

  const remove = async (m: Member) => {
    if (!confirm(`Remove ${m.invited_email ?? m.user_id} from this workspace?`)) return;
    try {
      await rb.tenants.removeMember(tenantID, m.user_id);
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Remove failed");
    }
  };

  return (
    <div class="space-y-6">
      {loading ? <p class="text-sm text-slate-500">Loading…</p> : (
        <ul class="divide-y divide-slate-200 rounded border border-slate-200 bg-white">
          {rows.map((m) => (
            <li key={m.user_id} class="flex items-center justify-between px-4 py-3 text-sm">
              <div>
                <div class="font-medium">{m.invited_email ?? "(unnamed)"}</div>
                <div class="text-xs text-slate-500">
                  {m.is_pending ? "Pending invite" : "Member"} · {m.role}
                </div>
              </div>
              {canManage ? (
                <div class="flex gap-2">
                  <select
                    value={m.role}
                    onChange={(e) => changeRole(m, (e.target as HTMLSelectElement).value as "owner" | "admin" | "member")}
                    class="rounded border border-slate-300 px-2 py-1 text-xs"
                  >
                    <option value="owner">owner</option>
                    <option value="admin">admin</option>
                    <option value="member">member</option>
                  </select>
                  <button type="button" onClick={() => remove(m)} class="rounded bg-red-50 px-2 py-1 text-xs text-red-700 hover:bg-red-100">
                    Remove
                  </button>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}

      {canManage ? (
        <form onSubmit={invite} class="rounded border border-slate-200 bg-white p-5">
          <h2 class="text-base font-semibold">Invite a teammate</h2>
          <div class="mt-3 flex flex-wrap gap-3">
            <input
              type="email" required
              placeholder="someone@example.com"
              value={email}
              onInput={(e) => setEmail((e.target as HTMLInputElement).value)}
              class="flex-1 rounded border border-slate-300 px-3 py-2 text-sm"
            />
            <select
              value={role}
              onChange={(e) => setRole((e.target as HTMLSelectElement).value as "owner" | "admin" | "member")}
              class="rounded border border-slate-300 px-2 py-2 text-sm"
            >
              <option value="member">member</option>
              <option value="admin">admin</option>
              <option value="owner">owner</option>
            </select>
            <button type="submit" disabled={busy || !email}
              class="rounded bg-slate-900 px-4 py-2 text-sm text-white hover:bg-slate-800 disabled:opacity-50">
              {busy ? "Sending…" : "Send invite"}
            </button>
          </div>
          {err ? <p class="mt-2 text-sm text-red-600">{err}</p> : null}
        </form>
      ) : null}
    </div>
  );
}

// ============================== Activity ==============================

function LogsTab({ tenantID }: { tenantID: string }) {
  const [rows, setRows] = useState<TenantLogEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);

  useEffect(() => {
    void (async () => {
      setLoading(true);
      try {
        const p = await rb.tenants.listLogs(tenantID, { page, perPage: 25 });
        setRows(p.items);
        setTotal(p.totalItems);
      } finally {
        setLoading(false);
      }
    })();
  }, [tenantID, page]);

  return (
    <div>
      {loading ? <p class="text-sm text-slate-500">Loading…</p> : (
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500">
              <th class="py-2">When</th>
              <th class="py-2">Event</th>
              <th class="py-2">Outcome</th>
              <th class="py-2">User</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((e) => (
              <tr key={e.id} class="border-b border-slate-100">
                <td class="py-2 font-mono text-xs">{new Date(e.at).toLocaleString()}</td>
                <td class="py-2 font-mono text-xs">{e.event}</td>
                <td class="py-2">{e.outcome}</td>
                <td class="py-2 font-mono text-xs text-slate-500">{e.user_id ?? "—"}</td>
              </tr>
            ))}
            {rows.length === 0 ? <tr><td colSpan={4} class="py-6 text-center text-slate-500">No activity yet.</td></tr> : null}
          </tbody>
        </table>
      )}
      {total > 25 ? (
        <div class="mt-4 flex items-center justify-between text-sm">
          <button type="button" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}
            class="rounded border border-slate-300 px-3 py-1 disabled:opacity-50">← Prev</button>
          <span class="text-slate-500">Page {page} of {Math.ceil(total / 25)}</span>
          <button type="button" disabled={page * 25 >= total} onClick={() => setPage((p) => p + 1)}
            class="rounded border border-slate-300 px-3 py-1 disabled:opacity-50">Next →</button>
        </div>
      ) : null}
    </div>
  );
}

// ============================== Roles =================================

function RbacTab({ tenantID, canManage }: { tenantID: string; canManage: boolean }) {
  const [members, setMembers] = useState<Member[]>([]);
  const [roles, setRoles] = useState<TenantRBACRole[]>([]);
  const [memberRoles, setMemberRoles] = useState<Record<string, TenantRBACRole[]>>({});
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  const load = async () => {
    setLoading(true);
    try {
      const [m, r] = await Promise.all([
        rb.tenants.listMembers(tenantID),
        rb.tenants.listRoles(tenantID),
      ]);
      setMembers(m.filter((x) => !x.is_pending));
      setRoles(r);
      const map: Record<string, TenantRBACRole[]> = {};
      for (const mem of m) {
        if (!mem.is_pending) {
          map[mem.user_id] = await rb.tenants.listMemberRoles(tenantID, mem.user_id);
        }
      }
      setMemberRoles(map);
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Load failed");
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, [tenantID]);

  const assign = async (userID: string, roleID: string) => {
    try {
      await rb.tenants.assignRole(tenantID, userID, { role_id: roleID });
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Assign failed");
    }
  };
  const unassign = async (userID: string, roleID: string) => {
    try {
      await rb.tenants.unassignRole(tenantID, userID, roleID);
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Unassign failed");
    }
  };

  if (loading) return <p class="text-sm text-slate-500">Loading…</p>;
  if (err) return <p class="text-sm text-red-600">{err}</p>;
  if (roles.length === 0) {
    return (
      <p class="rounded border border-dashed border-slate-300 p-6 text-center text-sm text-slate-500">
        No tenant-assignable roles defined. Ask your operator to add roles
        via the admin UI (scope = tenant).
      </p>
    );
  }

  return (
    <ul class="divide-y divide-slate-200 rounded border border-slate-200 bg-white">
      {members.map((m) => {
        const assigned = memberRoles[m.user_id] ?? [];
        const assignedIDs = new Set(assigned.map((r) => r.id));
        const assignable = roles.filter((r) => !assignedIDs.has(r.id));
        return (
          <li key={m.user_id} class="px-4 py-3 text-sm">
            <div class="flex items-center justify-between">
              <span class="font-medium">{m.invited_email}</span>
              <span class="text-xs text-slate-500">{m.role}</span>
            </div>
            <div class="mt-2 flex flex-wrap gap-2">
              {assigned.map((r) => (
                <span key={r.id} class="inline-flex items-center gap-1 rounded bg-slate-100 px-2 py-0.5 text-xs">
                  {r.name}
                  {canManage ? (
                    <button type="button" onClick={() => unassign(m.user_id, r.id)} class="text-slate-500 hover:text-red-700">×</button>
                  ) : null}
                </span>
              ))}
              {canManage && assignable.length > 0 ? (
                <select
                  class="rounded border border-slate-300 px-2 py-0.5 text-xs"
                  value=""
                  onChange={(e) => {
                    const v = (e.target as HTMLSelectElement).value;
                    if (v) void assign(m.user_id, v);
                  }}
                >
                  <option value="">+ assign role…</option>
                  {assignable.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
                </select>
              ) : null}
            </div>
          </li>
        );
      })}
    </ul>
  );
}

// ============================== Settings ==============================

function SettingsTab({
  tenantID, canManage, canDelete,
}: { tenantID: string; canManage: boolean; canDelete: boolean }) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const [, navigate] = useLocation();

  useEffect(() => {
    void (async () => {
      try {
        const t = await rb.tenants.get(tenantID);
        setName(t.name); setSlug(t.slug);
      } catch {/* layout handles */}
    })();
  }, [tenantID]);

  const save = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      await rb.tenants.update(tenantID, { name, slug });
      setMsg("Saved.");
    } catch (e: unknown) {
      setMsg(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(false);
    }
  };

  const drop = async () => {
    if (!confirm("Delete this workspace? This is reversible only via SQL.")) return;
    try {
      await rb.tenants.delete(tenantID);
      navigate("/tenants");
    } catch (e: unknown) {
      setMsg(e instanceof Error ? e.message : "Delete failed");
    }
  };

  return (
    <div class="space-y-6">
      <form onSubmit={save} class="space-y-4 rounded border border-slate-200 bg-white p-5">
        <h2 class="text-base font-semibold">General</h2>
        <div>
          <label class="mb-1 block text-sm font-medium text-slate-700">Name</label>
          <input type="text" required maxLength={120} disabled={!canManage}
            value={name} onInput={(e) => setName((e.target as HTMLInputElement).value)}
            class="w-full rounded border border-slate-300 px-3 py-2 disabled:bg-slate-50" />
        </div>
        <div>
          <label class="mb-1 block text-sm font-medium text-slate-700">Slug</label>
          <input type="text" disabled={!canManage}
            value={slug} onInput={(e) => setSlug((e.target as HTMLInputElement).value)}
            class="w-full rounded border border-slate-300 px-3 py-2 font-mono text-sm disabled:bg-slate-50" />
        </div>
        {msg ? <p class="text-sm text-slate-600">{msg}</p> : null}
        {canManage ? (
          <button type="submit" disabled={busy}
            class="rounded bg-slate-900 px-4 py-2 text-sm text-white hover:bg-slate-800 disabled:opacity-50">
            {busy ? "Saving…" : "Save"}
          </button>
        ) : <p class="text-xs text-slate-500">Only owners and admins can edit.</p>}
      </form>

      {canDelete ? (
        <div class="rounded border border-red-200 bg-red-50 p-5">
          <h2 class="text-base font-semibold text-red-800">Danger zone</h2>
          <p class="mt-2 text-sm text-red-700">
            Deleting this workspace soft-deletes the row. All members lose access immediately.
          </p>
          <button type="button" onClick={drop}
            class="mt-3 rounded bg-red-600 px-4 py-2 text-sm text-white hover:bg-red-700">
            Delete workspace
          </button>
        </div>
      ) : null}
    </div>
  );
}
