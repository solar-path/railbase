import { useState } from "react";
import { adminAPI } from "../api/admin";
import type { SystemSessionRow } from "../api/types";
import { AdminPage } from "../layout/admin_page";
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

const columns: ColumnDef<SystemSessionRow>[] = [
  {
    id: "id",
    header: "id",
    accessor: "id",
    cell: (s) => (
      <span class="font-mono text-xs text-muted-foreground" title={s.id}>
        {s.id.slice(0, 8)}…
      </span>
    ),
  },
  {
    id: "collection",
    header: "collection",
    accessor: "user_collection",
    cell: (s) => (
      <Badge variant="outline" class="font-mono">
        {s.user_collection}
      </Badge>
    ),
  },
  {
    id: "user",
    header: "user",
    accessor: "user_id",
    cell: (s) => (
      <span class="font-mono text-xs" title={s.user_id}>
        {s.user_id.slice(0, 8)}…
      </span>
    ),
  },
  {
    id: "ip",
    header: "ip",
    accessor: "ip",
    cell: (s) => <span class="font-mono text-xs">{s.ip ?? "—"}</span>,
  },
  {
    id: "user_agent",
    header: "user agent",
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
    header: "last used",
    accessor: "last_used_at",
    sortable: true,
    cell: (s) => (
      <span class="font-mono text-xs text-muted-foreground">{s.last_used_at}</span>
    ),
  },
  {
    id: "expires_at",
    header: "expires",
    accessor: "expires_at",
    sortable: true,
    cell: (s) => (
      <span class="font-mono text-xs text-muted-foreground">{s.expires_at}</span>
    ),
  },
  {
    id: "created",
    header: "created",
    accessor: "created",
    sortable: true,
    cell: (s) => (
      <span class="font-mono text-xs text-muted-foreground">{s.created}</span>
    ),
  },
];

export function SystemSessionsScreen() {
  const [total, setTotal] = useState(0);

  return (
    <AdminPage>
      <AdminPage.Header
        title="User sessions"
        description={
          <>
            {total} session{total === 1 ? "" : "s"} total. Read-only — to
            revoke a session, use the user record's logout flow.
          </>
        }
      />

      <AdminPage.Body>
        <QDatatable
          columns={columns}
          rowKey="id"
          pageSize={50}
          emptyMessage="No user sessions — no users have signed in yet."
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
