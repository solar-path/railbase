import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";
import { AdminPage } from "../layout/admin_page";
import { Badge } from "@/lib/ui/badge.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";

// System admins browser — read-only paginated view of the `_admins`
// table. CRUD intentionally lives on the CLI (`railbase admin create
// / delete`): the operator-grade write surface for admin records is
// more sensitive than a generic browse UI and we want a single
// canonical path with prompt-driven safety nets.
//
// Backend endpoint: GET /api/_admin/_system/admins (v1.7.x).
//
// Columns: id (truncated), email, mfa_enabled, last_active, created.
// `mfa_enabled` is derived from `_totp_enrollments`; today every value
// is `false` until admin-side MFA lands.

export function SystemAdminsScreen() {
  const [page, setPage] = useState(1);
  const perPage = 50;

  const q = useQuery({
    queryKey: ["system-admins", { page, perPage }],
    queryFn: () => adminAPI.listSystemAdmins({ page, perPage }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, q.data?.totalPages ?? Math.ceil(total / perPage));
  const items = q.data?.items ?? [];

  return (
    <AdminPage>
      <AdminPage.Header
        title="System admins"
        description={
          <>
            {total} admin{total === 1 ? "" : "s"} total. Read-only. To add or
            remove admins, use{" "}
            <code className="font-mono">railbase admin create/delete</code>.
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
            title="No admins"
            description={
              <>
                Create the first admin via{" "}
                <code className="font-mono">railbase admin create</code>.
              </>
            }
          />
        ) : (
          <Card>
            <CardContent className="p-0 overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>id</TableHead>
                    <TableHead>email</TableHead>
                    <TableHead>MFA</TableHead>
                    <TableHead>last active</TableHead>
                    <TableHead>created</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((a) => (
                    <TableRow key={a.id}>
                      <TableCell className="font-mono text-xs text-muted-foreground" title={a.id}>
                        {a.id.slice(0, 8)}…
                      </TableCell>
                      <TableCell className="font-mono">{a.email}</TableCell>
                      <TableCell>
                        {a.mfa_enabled ? (
                          <Badge variant="default">on</Badge>
                        ) : (
                          <Badge variant="secondary">off</Badge>
                        )}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {a.last_active ?? "—"}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {a.created}
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
