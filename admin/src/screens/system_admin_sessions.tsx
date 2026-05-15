import { useState } from "react";
import { adminAPI } from "../api/admin";
import type { SystemAdminSessionRow } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// System admin sessions browser — read-only paginated view of the
// `_admin_sessions` table. Token hashes + revocation timestamps stay
// server-side. Revoke isn't wired here: operators sign sessions out
// via the regular logout flow on the affected device, or by deleting
// the row directly in psql for incident-response cases.
//
// Backend endpoint: GET /api/_admin/_system/admin-sessions (v1.7.x).
// Server-paginated via QDatatable's `fetch` mode — the table owns
// page/pageSize; the fetch closure stashes the row count so the header
// can still show the total.
//
// Columns: id (truncated), admin_id (truncated), ip, user_agent (60-
// char cap from the server), last_used_at, expires_at, created.

function buildAdminSessionColumns(t: Translator["t"]): ColumnDef<SystemAdminSessionRow>[] {
  return [
    {
      id: "id",
      header: t("sysAdminSessions.col.id"),
      accessor: "id",
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground" title={s.id}>
          {s.id.slice(0, 8)}…
        </span>
      ),
    },
    {
      id: "admin",
      header: t("sysAdminSessions.col.admin"),
      accessor: "admin_id",
      cell: (s) => (
        <span class="font-mono text-xs" title={s.admin_id}>
          {s.admin_id.slice(0, 8)}…
        </span>
      ),
    },
    {
      id: "ip",
      header: t("sysAdminSessions.col.ip"),
      accessor: "ip",
      cell: (s) => <span class="font-mono text-xs">{s.ip ?? "—"}</span>,
    },
    {
      id: "user_agent",
      header: t("sysAdminSessions.col.userAgent"),
      accessor: "user_agent",
      cell: (s) => (
        <span
          class="font-mono text-xs text-muted-foreground block max-w-md truncate"
          title={s.user_agent ?? ""}
        >
          {s.user_agent ?? "—"}
        </span>
      ),
    },
    {
      id: "last_used_at",
      header: t("sysAdminSessions.col.lastUsed"),
      accessor: "last_used_at",
      sortable: true,
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground">{s.last_used_at}</span>
      ),
    },
    {
      id: "expires_at",
      header: t("sysAdminSessions.col.expires"),
      accessor: "expires_at",
      sortable: true,
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground">{s.expires_at}</span>
      ),
    },
    {
      id: "created",
      header: t("sysAdminSessions.col.created"),
      accessor: "created",
      sortable: true,
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground">{s.created}</span>
      ),
    },
  ];
}

export function SystemAdminSessionsScreen() {
  const { t } = useT();
  const [total, setTotal] = useState(0);

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("sysAdminSessions.title")}
        description={t("sysAdminSessions.description", { count: total })}
      />

      <AdminPage.Body>
        <QDatatable
          columns={buildAdminSessionColumns(t)}
          rowKey="id"
          pageSize={50}
          emptyMessage={t("sysAdminSessions.empty")}
          fetch={async (params) => {
            const r = await adminAPI.listSystemAdminSessions({
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
