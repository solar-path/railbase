import { useState } from "react";
import { adminAPI } from "../api/admin";
import type { SystemSessionRow } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { Badge } from "@/lib/ui/badge.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// User sessions browser — read-only paginated view of the `_sessions`
// table (one row per active user-side session, discriminated by
// collection_name). Mirrors the admin-sessions sibling screen.
//
// Backend endpoint: GET /api/_admin/_system/sessions (v1.7.x).
// Server-paginated via QDatatable's `fetch` mode — the table owns
// page/pageSize; the fetch closure stashes the row count so the header
// can still show the total.

function buildSessionColumns(t: Translator["t"]): ColumnDef<SystemSessionRow>[] {
  return [
    {
      id: "id",
      header: t("sysSessions.col.id"),
      accessor: "id",
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground" title={s.id}>
          {s.id.slice(0, 8)}…
        </span>
      ),
    },
    {
      id: "collection",
      header: t("sysSessions.col.collection"),
      accessor: "user_collection",
      cell: (s) => (
        <Badge variant="outline" class="font-mono">
          {s.user_collection}
        </Badge>
      ),
    },
    {
      id: "user",
      header: t("sysSessions.col.user"),
      accessor: "user_id",
      cell: (s) => (
        <span class="font-mono text-xs" title={s.user_id}>
          {s.user_id.slice(0, 8)}…
        </span>
      ),
    },
    {
      id: "ip",
      header: t("sysSessions.col.ip"),
      accessor: "ip",
      cell: (s) => <span class="font-mono text-xs">{s.ip ?? "—"}</span>,
    },
    {
      id: "user_agent",
      header: t("sysSessions.col.userAgent"),
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
      header: t("sysSessions.col.lastUsed"),
      accessor: "last_used_at",
      sortable: true,
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground">{s.last_used_at}</span>
      ),
    },
    {
      id: "expires_at",
      header: t("sysSessions.col.expires"),
      accessor: "expires_at",
      sortable: true,
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground">{s.expires_at}</span>
      ),
    },
    {
      id: "created",
      header: t("sysSessions.col.created"),
      accessor: "created",
      sortable: true,
      cell: (s) => (
        <span class="font-mono text-xs text-muted-foreground">{s.created}</span>
      ),
    },
  ];
}

export function SystemSessionsScreen() {
  const { t } = useT();
  const [total, setTotal] = useState(0);

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("sysSessions.title")}
        description={t("sysSessions.description", { count: total })}
      />

      <AdminPage.Body>
        <QDatatable
          columns={buildSessionColumns(t)}
          rowKey="id"
          pageSize={50}
          emptyMessage={t("sysSessions.empty")}
          fetch={async (params) => {
            const r = await adminAPI.listSystemSessions({
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
