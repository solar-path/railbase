// Admins & roles — v1.x admin-side RBAC management screen.
//
// What it does:
//
//   - Lists every row in `_admins` with the SITE roles currently
//     attached (system_admin / system_readonly / custom). One row per
//     admin; the email is the lookup key the operator usually types.
//
//   - Lets the operator change any admin's role-set in place. The UI
//     uses checkboxes (multi-select) because the backend stores a set,
//     not a single value: an operator can give one admin both
//     `system_admin` and a custom `auditor` role if they like.
//
//   - Surfaces the LAST system_admin safety guard. The backend returns
//     409 with a hint when the change would leave zero system_admins;
//     we render that hint as a toast and DON'T close the editor, so
//     the operator can promote another admin first.
//
//   - Side-panel shows, for the selected role, every action_key it
//     grants — so "what does `system_readonly` actually allow?" is one
//     click away instead of a doc trip.
//
// Out of scope (deferred):
//
//   - Role CRUD (create custom role, grant/revoke action keys). The
//     seeded roles cover the v1.x common case; custom roles can still
//     be minted from the Go API. Adding it to the UI is a per-role
//     editor that's a much bigger lift.
//
//   - Tenant role assignments. Same store, different surface; tenant
//     members aren't on this screen.

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import type {
  AdminWithRoles,
  RBACRole,
  RBACRoleActionsResponse,
} from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";

import { Badge } from "@/lib/ui/badge.ui";
import { Button } from "@/lib/ui/button.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Input } from "@/lib/ui/input.ui";
import {
  QDatatable,
  type ColumnDef,
  type RowAction,
} from "@/lib/ui/QDatatable.ui";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/lib/ui/sheet.ui";
import { toast } from "@/lib/ui/sonner.ui";

export function AdminsRolesScreen() {
  const { t } = useT();
  const qc = useQueryClient();
  const adminsQ = useQuery({
    queryKey: ["admins-with-roles"],
    queryFn: () => adminAPI.adminsWithRoles(),
  });
  const rolesQ = useQuery({
    queryKey: ["rbac", "roles"],
    queryFn: () => adminAPI.rbacRolesList(),
  });

  // Sorted, site-scoped roles only — the editor offers only site roles
  // because admin assignments are site-scoped in the backend.
  const siteRoles = useMemo<RBACRole[]>(() => {
    const all = rolesQ.data?.roles ?? [];
    return all
      .filter((r) => r.scope === "site")
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [rolesQ.data]);

  // Active editor target. When non-null an inline drawer (Sheet) opens
  // for that admin; null closes it.
  const [editTarget, setEditTarget] = useState<AdminWithRoles | null>(null);

  // Side panel target — clicking a role badge in any row opens a
  // read-only "what does this role grant?" panel.
  const [inspectRole, setInspectRole] = useState<RBACRole | null>(null);

  // Filter the grid by email substring — handy on deployments with
  // dozens of admins.
  const [filter, setFilter] = useState("");

  const filtered = useMemo<AdminWithRoles[]>(() => {
    const rows = adminsQ.data?.admins ?? [];
    if (!filter.trim()) return rows;
    const q = filter.trim().toLowerCase();
    return rows.filter((r) => r.email.toLowerCase().includes(q));
  }, [adminsQ.data, filter]);

  const columns = useMemo<ColumnDef<AdminWithRoles>[]>(
    () => [
      {
        id: "email",
        header: t("admins.col.email"),
        accessor: "email",
        cell: (a) => <span className="font-medium">{a.email}</span>,
      },
      {
        id: "id",
        header: t("admins.col.id"),
        accessor: "id",
        cell: (a) => (
          <span className="font-mono text-[11px] text-muted-foreground">
            {a.id.slice(0, 8)}…
          </span>
        ),
      },
      {
        id: "roles",
        header: t("admins.col.siteRoles"),
        accessor: (a) => a.roles.join(","),
        cell: (a) => (
          <RoleBadgeList
            names={a.roles}
            roles={siteRoles}
            onInspect={(r) => setInspectRole(r)}
            t={t}
          />
        ),
      },
    ],
    [siteRoles, t],
  );

  const rowActions: RowAction<AdminWithRoles>[] = [
    {
      label: t("admins.action.editRoles"),
      onSelect: (a) => setEditTarget(a),
    },
  ];

  const loading = adminsQ.isLoading || rolesQ.isLoading;
  const error = adminsQ.error ?? rolesQ.error;

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("admins.title")}
        description={t("admins.description")}
      />
      <AdminPage.Toolbar>
        <Input
          placeholder={t("admins.filterPlaceholder")}
          value={filter}
          onInput={(e: any) => setFilter(e.currentTarget.value)}
          className="max-w-xs"
        />
        <span className="text-xs text-muted-foreground">
          {t("admins.summary", {
            admins: adminsQ.data?.admins.length ?? 0,
            roles: siteRoles.length,
          })}
        </span>
      </AdminPage.Toolbar>
      <AdminPage.Body>
        {error ? (
          <Card>
            <CardContent className="p-4 text-sm text-destructive">
              {t("admins.loadFailed", { msg: errMessage(error) })}
            </CardContent>
          </Card>
        ) : (
          <QDatatable<AdminWithRoles>
            data={filtered}
            columns={columns}
            rowActions={rowActions}
            loading={loading}
            rowKey={(a) => a.id}
            emptyMessage={
              filter
                ? t("admins.empty.filter")
                : t("admins.empty.none")
            }
          />
        )}
      </AdminPage.Body>

      {/* Role editor sheet. */}
      <RoleEditorSheet
        target={editTarget}
        allRoles={siteRoles}
        onClose={() => setEditTarget(null)}
        onSaved={() => {
          setEditTarget(null);
          void qc.invalidateQueries({ queryKey: ["admins-with-roles"] });
        }}
        t={t}
      />

      {/* Role inspector sheet — what does this role grant? */}
      <RoleInspectorSheet
        role={inspectRole}
        onClose={() => setInspectRole(null)}
        t={t}
      />
    </AdminPage>
  );
}

// RoleBadgeList renders one Badge per role-name. Clicking a badge
// opens the inspector sheet. Bypass roles get the destructive variant
// (visual cue: full privilege).
function RoleBadgeList({
  names,
  roles,
  onInspect,
  t,
}: {
  names: string[];
  roles: RBACRole[];
  onInspect: (r: RBACRole) => void;
  t: Translator["t"];
}) {
  if (names.length === 0) {
    return (
      <span className="text-xs text-muted-foreground italic">
        {t("admins.noSiteRoles")}
      </span>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-1">
      {names.map((n) => {
        const role = roles.find((r) => r.name === n);
        const variant = n === "system_admin" ? "destructive" : "secondary";
        return (
          <button
            key={n}
            type="button"
            className="text-left"
            onClick={() => role && onInspect(role)}
            disabled={!role}
            title={role?.description ?? n}
          >
            <Badge variant={variant as any} className="font-mono">
              {n}
            </Badge>
          </button>
        );
      })}
    </div>
  );
}

// RoleEditorSheet — multi-checkbox role-set picker. Save submits the
// COMPLETE new set; backend swaps atomically.
function RoleEditorSheet({
  target,
  allRoles,
  onClose,
  onSaved,
  t,
}: {
  target: AdminWithRoles | null;
  allRoles: RBACRole[];
  onClose: () => void;
  onSaved: () => void;
  t: Translator["t"];
}) {
  const [selected, setSelected] = useState<string[]>([]);

  // Reset selection when target changes — opening the editor on a
  // different admin should reset the form.
  useEffect(() => {
    setSelected(target?.roles ?? []);
  }, [target]);

  const saveM = useMutation({
    mutationFn: () => {
      if (!target) throw new Error("no target");
      return adminAPI.setAdminRoles(target.id, selected);
    },
    onSuccess: () => {
      toast.success(t("admins.editor.savedToast"));
      onSaved();
    },
    onError: (err) => {
      // The backend returns 409 + hint when the change would leave
      // zero system_admins. Render the hint verbatim so the operator
      // sees the actionable next step ("promote another admin first").
      toast.error(errMessage(err));
    },
  });

  const open = target !== null;
  const dirty = useMemo(() => {
    if (!target) return false;
    const before = [...target.roles].sort();
    const after = [...selected].sort();
    if (before.length !== after.length) return true;
    return before.some((v, i) => v !== after[i]);
  }, [target, selected]);

  function toggle(name: string) {
    setSelected((prev) =>
      prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name],
    );
  }

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{t("admins.editor.title")}</SheetTitle>
          <SheetDescription>
            {target ? (
              <>
                {t("admins.editor.adminLabel")} <span className="font-mono">{target.email}</span>
              </>
            ) : null}
          </SheetDescription>
        </SheetHeader>
        <div className="mt-4 space-y-3">
          {allRoles.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("admins.editor.noRoles")}</p>
          ) : (
            allRoles.map((r) => (
              <label
                key={r.id}
                className="flex items-start gap-3 cursor-pointer rounded-md border p-3 hover:bg-accent"
              >
                <Checkbox
                  checked={selected.includes(r.name)}
                  onCheckedChange={() => toggle(r.name)}
                />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm font-medium">{r.name}</span>
                    {r.is_system ? (
                      <Badge variant="outline" className="text-[10px]">{t("admins.editor.systemBadge")}</Badge>
                    ) : null}
                  </div>
                  {r.description ? (
                    <p className="text-xs text-muted-foreground mt-0.5">{r.description}</p>
                  ) : null}
                </div>
              </label>
            ))
          )}
          {selected.length === 0 ? (
            <p className="text-xs text-amber-700 dark:text-amber-300">
              {t("admins.editor.warnZero")}
            </p>
          ) : null}
          <div className="flex justify-end gap-2 pt-2 border-t">
            <Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button>
            <Button
              disabled={!dirty || saveM.isPending}
              onClick={() => saveM.mutate()}
            >
              {saveM.isPending ? t("admins.editor.saving") : t("common.save")}
            </Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}

// RoleInspectorSheet — read-only "what does this role grant?" panel.
function RoleInspectorSheet({
  role,
  onClose,
  t,
}: {
  role: RBACRole | null;
  onClose: () => void;
  t: Translator["t"];
}) {
  const actionsQ = useQuery<RBACRoleActionsResponse>({
    queryKey: ["rbac", "role", role?.id, "actions"],
    queryFn: () => adminAPI.rbacRoleActions(role!.id),
    enabled: role !== null,
  });
  const open = role !== null;

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="sm:max-w-md">
        <SheetHeader>
          <SheetTitle>
            <span className="font-mono">{role?.name ?? "—"}</span>
          </SheetTitle>
          <SheetDescription>
            {role?.description}
          </SheetDescription>
        </SheetHeader>
        <div className="mt-4 space-y-3 text-sm">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="outline">{role?.scope}</Badge>
            {role?.is_system ? <Badge variant="outline">{t("admins.editor.systemBadge")}</Badge> : null}
          </div>
          {actionsQ.isLoading ? (
            <p className="text-muted-foreground">{t("admins.inspector.loadingActions")}</p>
          ) : actionsQ.error ? (
            <p className="text-destructive">{errMessage(actionsQ.error)}</p>
          ) : actionsQ.data?.bypass ? (
            <Card className="border-destructive/40 bg-destructive/5">
              <CardContent className="p-3 text-sm">
                <strong>{t("admins.inspector.bypassTitle")}</strong> {t("admins.inspector.bypassDesc")}
              </CardContent>
            </Card>
          ) : actionsQ.data && actionsQ.data.actions.length > 0 ? (
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">
                {t("admins.inspector.actionsGranted", { count: actionsQ.data.actions.length })}
              </p>
              <ul className="divide-y border rounded-md">
                {actionsQ.data.actions.map((a) => (
                  <li key={a} className="px-3 py-1.5 font-mono text-xs">
                    {a}
                  </li>
                ))}
              </ul>
            </div>
          ) : (
            <p className="text-muted-foreground">
              {t("admins.inspector.noActions")}
            </p>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// errMessage normalises errors from typed APIError + plain Error +
// stringy throws into a single human-readable line. Mirrors the
// shape used in backups.tsx so toasts read consistently across the
// SPA.
function errMessage(err: unknown): string {
  if (isAPIError(err)) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}
