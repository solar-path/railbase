import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";
import { AdminPage } from "../layout/admin_page";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";

// System admin sessions browser — read-only paginated view of the
// `_admin_sessions` table. Token hashes + revocation timestamps stay
// server-side. Revoke isn't wired here: operators sign sessions out
// via the regular logout flow on the affected device, or by deleting
// the row directly in psql for incident-response cases.
//
// Backend endpoint: GET /api/_admin/_system/admin-sessions (v1.7.x).
//
// Columns: id (truncated), admin_id (truncated), ip, user_agent (60-
// char cap from the server), last_used_at, expires_at, created.

export function SystemAdminSessionsScreen() {
  const [page, setPage] = useState(1);
  const perPage = 50;

  const q = useQuery({
    queryKey: ["system-admin-sessions", { page, perPage }],
    queryFn: () => adminAPI.listSystemAdminSessions({ page, perPage }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, q.data?.totalPages ?? Math.ceil(total / perPage));
  const items = q.data?.items ?? [];

  return (
    <AdminPage>
      <AdminPage.Header
        title="Admin sessions"
        description={
          <>
            {total} session{total === 1 ? "" : "s"} total. Read-only — token
            hashes never leave the server.
          </>
        }
        actions={<Pager page={page} totalPages={totalPages} onChange={setPage} />}
      />

      <AdminPage.Body>
        {q.isError ? (
          <AdminPage.Error
            message={q.error instanceof Error ? q.error.message : String(q.error)}
            retry={() => void q.refetch()}
          />
        ) : items.length === 0 && !q.isLoading ? (
          <AdminPage.Empty
            title="No admin sessions"
            description="No admins have signed in yet."
          />
        ) : (
          <Card>
            <CardContent className="p-0 overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>id</TableHead>
                    <TableHead>admin</TableHead>
                    <TableHead>ip</TableHead>
                    <TableHead>user agent</TableHead>
                    <TableHead>last used</TableHead>
                    <TableHead>expires</TableHead>
                    <TableHead>created</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((s) => (
                    <TableRow key={s.id}>
                      <TableCell className="font-mono text-xs text-muted-foreground" title={s.id}>
                        {s.id.slice(0, 8)}…
                      </TableCell>
                      <TableCell className="font-mono text-xs" title={s.admin_id}>
                        {s.admin_id.slice(0, 8)}…
                      </TableCell>
                      <TableCell className="font-mono text-xs">{s.ip ?? "—"}</TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground max-w-md truncate" title={s.user_agent ?? ""}>
                        {s.user_agent ?? "—"}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {s.last_used_at}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {s.expires_at}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {s.created}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        )}
      </AdminPage.Body>
    </AdminPage>
  );
}
