import { useState } from "react";
import { adminAPI } from "../api/admin";
import type { SystemAdminRow } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { Badge } from "@/lib/ui/badge.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// System admins browser — read-only paginated view of the `_admins`
// table. CRUD intentionally lives on the CLI (`railbase admin create
// / delete`): the operator-grade write surface for admin records is
// more sensitive than a generic browse UI and we want a single
// canonical path with prompt-driven safety nets.
//
// Backend endpoint: GET /api/_admin/_system/admins (v1.7.x).
// Server-paginated via QDatatable's `fetch` mode — the table owns
// page/pageSize; the fetch closure stashes the row count so the header
// can still show the total.
//
// Columns: id (truncated), email, mfa_enabled, last_active, created.
// `mfa_enabled` is derived from `_totp_enrollments`; today every value
// is `false` until admin-side MFA lands.

function buildSystemAdminColumns(t: Translator["t"]): ColumnDef<SystemAdminRow>[] {
  return [
    {
      id: "id",
      header: t("systemAdmins.col.id"),
      accessor: "id",
      cell: (a) => (
        <span class="font-mono text-xs text-muted-foreground" title={a.id}>
          {a.id.slice(0, 8)}…
        </span>
      ),
    },
    {
      id: "email",
      header: t("systemAdmins.col.email"),
      accessor: "email",
      cell: (a) => <span class="font-mono">{a.email}</span>,
    },
    {
      id: "mfa_enabled",
      header: t("systemAdmins.col.mfa"),
      accessor: "mfa_enabled",
      cell: (a) =>
        a.mfa_enabled ? (
          <Badge variant="default">{t("systemAdmins.mfa.on")}</Badge>
        ) : (
          <Badge variant="secondary">{t("systemAdmins.mfa.off")}</Badge>
        ),
    },
    {
      id: "last_active",
      header: t("systemAdmins.col.lastActive"),
      accessor: "last_active",
      cell: (a) => (
        <span class="font-mono text-xs text-muted-foreground">
          {a.last_active ?? "—"}
        </span>
      ),
    },
    {
      id: "created",
      header: t("systemAdmins.col.created"),
      accessor: "created",
      cell: (a) => (
        <span class="font-mono text-xs text-muted-foreground">{a.created}</span>
      ),
    },
  ];
}

export function SystemAdminsScreen() {
  const { t } = useT();
  const [total, setTotal] = useState(0);
  const columns = buildSystemAdminColumns(t);

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("systemAdmins.title")}
        description={
          <>
            {t("systemAdmins.description", { count: total, plural: total === 1 ? "" : "s" })}{" "}
            <code className="font-mono">railbase admin create/delete</code>.
          </>
        }
      />

      <AdminPage.Body>
        <QDatatable
          columns={columns}
          rowKey="id"
          pageSize={50}
          emptyMessage={t("systemAdmins.empty")}
          fetch={async (params) => {
            const r = await adminAPI.listSystemAdmins({
              page: params.page,
              perPage: params.pageSize,
            });
            setTotal(r.totalItems);
            return { rows: r.items, total: r.totalItems };
          }}
        />
      </AdminPage.Body>
    </AdminPage>
  );
}
