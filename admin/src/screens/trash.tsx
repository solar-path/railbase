import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI, recordsAPI } from "../api/admin";
import { Pager } from "../layout/pager";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";

// Trash admin screen — cross-collection listing of soft-deleted
// records with a per-row restore button. Backend endpoint:
// GET /api/_admin/trash (v1.7.x §3.11 deferred slice).
//
// Restore POSTs to the per-collection REST endpoint
// (POST /api/collections/{name}/records/{id}/restore) — the v1.4.12
// shipped path — gated by the collection's UpdateRule. The admin
// bearer is passed through transparently via rawFetch.
//
// Permanent delete from the UI is intentionally NOT supported in v1
// — operators wanting to purge use the cleanup_trash cron + the
// `trash.retention.<collection>` setting. A button labelled "Delete
// forever" is the kind of thing that destroys production at 3 a.m.

export function TrashScreen() {
  const qc = useQueryClient();
  const [page, setPage] = useState(1);
  const perPage = 50;

  const [collection, setCollection] = useState<string>(""); // "" = all
  const [flash, setFlash] = useState<string | null>(null);

  // Reset to page 1 whenever the collection filter changes.
  useEffect(() => {
    setPage(1);
  }, [collection]);

  // Auto-fade the flash banner. 5 s matches the backups screen.
  useEffect(() => {
    if (!flash) return;
    const t = setTimeout(() => setFlash(null), 5_000);
    return () => clearTimeout(t);
  }, [flash]);

  const q = useQuery({
    queryKey: ["trash", { page, perPage, collection }],
    queryFn: () =>
      adminAPI.trashList({
        page,
        perPage,
        collection: collection || undefined,
      }),
  });

  // Single restore mutation keyed off (collection, id). We expose
  // the in-flight target via the variables so the row spinner can
  // localise to that one button rather than the whole table.
  const restoreM = useMutation({
    mutationFn: (args: { collection: string; id: string }) =>
      recordsAPI.restoreRecord(args.collection, args.id),
    onSuccess: (_data, vars) => {
      setFlash(`Restored ${vars.collection}/${vars.id.slice(0, 8)}…`);
      void qc.invalidateQueries({ queryKey: ["trash"] });
    },
  });

  const items = q.data?.items ?? [];
  const collections = q.data?.collections ?? [];
  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

  const hasSoftDelete = collections.length > 0;

  return (
    <AdminPage>
      <AdminPage.Header
        title="Trash"
        description={
          <>
            Soft-deleted records across all collections with{" "}
            <code className="font-mono">.SoftDelete()</code>. Records here can be
            restored or stay until your retention policy permanently purges them.
          </>
        }
        actions={<Pager page={page} totalPages={totalPages} onChange={setPage} />}
      />

      {flash ? (
        <div className="rounded border border-primary/40 bg-primary/10 px-3 py-2 text-sm text-primary flex items-start justify-between gap-3">
          <div>{flash}</div>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setFlash(null)}
            aria-label="Dismiss"
            className="text-primary/70 hover:text-primary hover:bg-transparent h-auto p-0"
          >
            ×
          </Button>
        </div>
      ) : null}

      {restoreM.isError ? (
        <div className="rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          Restore failed:{" "}
          <span className="font-mono">
            {(restoreM.error as { message?: string } | null)?.message ?? "unknown error"}
          </span>
        </div>
      ) : null}

      {hasSoftDelete ? (
        <AdminPage.Toolbar>
          <label className="flex items-center gap-1">
            <span className="text-muted-foreground">collection</span>
            <select
              value={collection}
              onChange={(e) => setCollection(e.currentTarget.value)}
              className="rounded border border-input px-2 py-1 bg-transparent"
            >
              <option value="">all</option>
              {collections.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </label>
          {collection ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setCollection("")}
            >
              clear
            </Button>
          ) : null}
          <span className="text-xs text-muted-foreground ml-auto">
            {total} record{total === 1 ? "" : "s"} in trash
          </span>
        </AdminPage.Toolbar>
      ) : null}

      <AdminPage.Body>
      {q.isLoading ? (
        <div className="text-sm text-muted-foreground">Loading…</div>
      ) : !hasSoftDelete ? (
        // No collection declares .SoftDelete() — distinct from "empty
        // trash". This guides the dev to the schema builder rather
        // than implying the trash is just empty.
        <div className="rounded border border-dashed border-input bg-muted px-4 py-8 text-center text-sm text-muted-foreground">
          No collection has <code className="font-mono">.SoftDelete()</code> enabled.
          Add the flag to a collection in your schema to start collecting
          tombstones here.
        </div>
      ) : items.length === 0 ? (
        <div className="rounded border border-dashed border-input bg-muted px-4 py-8 text-center text-sm text-muted-foreground space-y-1">
          <div>(No soft-deleted records — nothing to restore.)</div>
          <div className="text-xs text-muted-foreground">
            Soft-deleted records linger here instead of being physically removed,
            so an accidental delete is one click away from being undone.
          </div>
        </div>
      ) : (
        <Card>
          <CardContent className="p-0 overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>deleted</TableHead>
                  <TableHead>collection</TableHead>
                  <TableHead>id</TableHead>
                  <TableHead>created</TableHead>
                  <TableHead>updated</TableHead>
                  <TableHead className="text-right">actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((it) => {
                  const idShort = it.id.slice(0, 8);
                  const pending =
                    restoreM.isPending &&
                    restoreM.variables?.collection === it.collection &&
                    restoreM.variables?.id === it.id;
                  return (
                    <TableRow key={`${it.collection}/${it.id}`}>
                      <TableCell
                        className="font-mono text-xs text-muted-foreground whitespace-nowrap"
                        title={it.deleted}
                      >
                        {relativeTime(it.deleted)}
                      </TableCell>
                      <TableCell>
                        <span className="inline-block bg-muted rounded px-1.5 py-0.5 text-xs font-mono">
                          {it.collection}
                        </span>
                      </TableCell>
                      <TableCell className="font-mono text-xs" title={it.id}>
                        {idShort}…
                      </TableCell>
                      <TableCell
                        className="font-mono text-xs text-muted-foreground whitespace-nowrap"
                        title={it.created}
                      >
                        {relativeTime(it.created)}
                      </TableCell>
                      <TableCell
                        className="font-mono text-xs text-muted-foreground whitespace-nowrap"
                        title={it.updated}
                      >
                        {relativeTime(it.updated)}
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="link"
                          size="sm"
                          disabled={pending}
                          onClick={() => {
                            if (
                              !window.confirm(
                                `Restore ${it.collection}/${idShort}…?`,
                              )
                            ) {
                              return;
                            }
                            restoreM.mutate({
                              collection: it.collection,
                              id: it.id,
                            });
                          }}
                        >
                          {pending ? "Restoring…" : "Restore"}
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
      </AdminPage.Body>
    </AdminPage>
  );
}

// relativeTime mirrors the helper in backups.tsx (kept parallel
// rather than extracted — the admin bundle is small and a one-off
// shared util file would add an import-hop for one function). Falls
// back to the raw timestamp if parsing fails.
function relativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const diffMs = Date.now() - t;
  const sec = Math.round(diffMs / 1000);
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min} minute${min === 1 ? "" : "s"} ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr} hour${hr === 1 ? "" : "s"} ago`;
  const day = Math.round(hr / 24);
  if (day < 30) return `${day} day${day === 1 ? "" : "s"} ago`;
  const mo = Math.round(day / 30);
  if (mo < 12) return `${mo} month${mo === 1 ? "" : "s"} ago`;
  const yr = Math.round(mo / 12);
  return `${yr} year${yr === 1 ? "" : "s"} ago`;
}
