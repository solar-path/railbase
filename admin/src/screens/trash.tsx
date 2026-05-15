import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { adminAPI, recordsAPI } from "../api/admin";
import type { TrashRecord } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { Button } from "@/lib/ui/button.ui";
import { QDatatable, type ColumnDef, type RowAction } from "@/lib/ui/QDatatable.ui";
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

function buildTrashColumns(t: Translator["t"]): ColumnDef<TrashRecord>[] {
  return [
    {
      id: "deleted",
      header: t("trash.col.deleted"),
      accessor: "deleted",
      cell: (it) => (
        <span
          className="font-mono text-xs text-muted-foreground whitespace-nowrap"
          title={it.deleted}
        >
          {relativeTime(it.deleted, t)}
        </span>
      ),
    },
    {
      id: "collection",
      header: t("trash.col.collection"),
      accessor: "collection",
      cell: (it) => (
        <span className="inline-block bg-muted rounded px-1.5 py-0.5 text-xs font-mono">
          {it.collection}
        </span>
      ),
    },
    {
      id: "id",
      header: t("trash.col.id"),
      accessor: "id",
      cell: (it) => (
        <span className="font-mono text-xs" title={it.id}>
          {it.id.slice(0, 8)}…
        </span>
      ),
    },
    {
      id: "created",
      header: t("trash.col.created"),
      accessor: "created",
      cell: (it) => (
        <span
          className="font-mono text-xs text-muted-foreground whitespace-nowrap"
          title={it.created}
        >
          {relativeTime(it.created, t)}
        </span>
      ),
    },
    {
      id: "updated",
      header: t("trash.col.updated"),
      accessor: "updated",
      cell: (it) => (
        <span
          className="font-mono text-xs text-muted-foreground whitespace-nowrap"
          title={it.updated}
        >
          {relativeTime(it.updated, t)}
        </span>
      ),
    },
  ];
}

export function TrashScreen() {
  const { t } = useT();
  const qc = useQueryClient();

  const [collection, setCollection] = useState<string>(""); // "" = all
  const [flash, setFlash] = useState<string | null>(null);
  const [total, setTotal] = useState(0);
  // `collections` is the .SoftDelete() registry list — it rides along
  // in the same /trash response, so the fetch closure stashes it here
  // for the filter dropdown rather than spending a second round-trip.
  // `loaded` gates the "no soft-delete" empty state so it doesn't
  // flash before the first fetch resolves.
  const [collections, setCollections] = useState<string[]>([]);
  const [loaded, setLoaded] = useState(false);

  // Auto-fade the flash banner. 5 s matches the backups screen.
  useEffect(() => {
    if (!flash) return;
    const timer = setTimeout(() => setFlash(null), 5_000);
    return () => clearTimeout(timer);
  }, [flash]);

  // Single restore mutation keyed off (collection, id).
  const restoreM = useMutation({
    mutationFn: (args: { collection: string; id: string }) =>
      recordsAPI.restoreRecord(args.collection, args.id),
    onSuccess: (_data, vars) => {
      setFlash(t("trash.restored", { name: `${vars.collection}/${vars.id.slice(0, 8)}…` }));
      void qc.invalidateQueries({ queryKey: ["trash"] });
    },
  });

  const hasSoftDelete = collections.length > 0;

  const columns = buildTrashColumns(t);

  const rowActions = (it: TrashRecord): RowAction<TrashRecord>[] => {
    const idShort = it.id.slice(0, 8);
    const pending =
      restoreM.isPending &&
      restoreM.variables?.collection === it.collection &&
      restoreM.variables?.id === it.id;
    return [
      {
        label: pending ? t("trash.action.restoring") : t("trash.action.restore"),
        disabled: () => pending,
        onSelect: () => {
          if (!window.confirm(t("trash.confirmRestore", { name: `${it.collection}/${idShort}…` }))) {
            return;
          }
          restoreM.mutate({ collection: it.collection, id: it.id });
        },
      },
    ];
  };

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("trash.title")}
        description={
          <>
            {t("trash.descPart1")}{" "}
            <code className="font-mono">.SoftDelete()</code>. {t("trash.descPart2")}
          </>
        }
      />

      {flash ? (
        <div className="rounded border border-primary/40 bg-primary/10 px-3 py-2 text-sm text-primary flex items-start justify-between gap-3">
          <div>{flash}</div>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setFlash(null)}
            aria-label={t("trash.dismiss")}
            className="text-primary/70 hover:text-primary hover:bg-transparent h-auto p-0"
          >
            ×
          </Button>
        </div>
      ) : null}

      {restoreM.isError ? (
        <div className="rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {t("trash.restoreFailed")}{" "}
          <span className="font-mono">
            {(restoreM.error as { message?: string } | null)?.message ?? t("trash.unknownError")}
          </span>
        </div>
      ) : null}

      {hasSoftDelete ? (
        <AdminPage.Toolbar>
          <label className="flex items-center gap-1">
            <span className="text-muted-foreground">{t("trash.collectionLabel")}</span>
            <select
              value={collection}
              onChange={(e) => setCollection(e.currentTarget.value)}
              className="rounded border border-input px-2 py-1 bg-transparent"
            >
              <option value="">{t("trash.allCollections")}</option>
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
              {t("trash.clear")}
            </Button>
          ) : null}
          <span className="text-xs text-muted-foreground ml-auto">
            {t(total === 1 ? "trash.totalOne" : "trash.totalMany", { count: total })}
          </span>
        </AdminPage.Toolbar>
      ) : null}

      <AdminPage.Body>
      {loaded && !hasSoftDelete ? (
        // No collection declares .SoftDelete() — distinct from "empty
        // trash". This guides the dev to the schema builder rather
        // than implying the trash is just empty.
        <div className="rounded border border-dashed border-input bg-muted px-4 py-8 text-center text-sm text-muted-foreground">
          {t("trash.noSoftDeletePart1")} <code className="font-mono">.SoftDelete()</code> {t("trash.noSoftDeletePart2")}
        </div>
      ) : (
        <Card>
          <CardContent className="p-3 overflow-x-auto">
            <QDatatable
              columns={columns}
              rowKey={(it) => `${it.collection}/${it.id}`}
              pageSize={50}
              rowActions={rowActions}
              deps={[collection]}
              emptyMessage={
                <span className="space-y-1">
                  <span className="block">
                    {t("trash.empty")}
                  </span>
                  <span className="block text-xs text-muted-foreground">
                    {t("trash.emptyHint")}
                  </span>
                </span>
              }
              fetch={async (params) => {
                const r = await adminAPI.trashList({
                  page: params.page,
                  perPage: params.pageSize,
                  collection: collection || undefined,
                });
                setTotal(r.totalItems);
                setCollections(r.collections);
                setLoaded(true);
                return { rows: r.items, total: r.totalItems };
              }}
            />
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
function relativeTime(iso: string, t: Translator["t"]): string {
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return iso;
  const diffMs = Date.now() - ts;
  const sec = Math.round(diffMs / 1000);
  if (sec < 5) return t("relative.justNow");
  if (sec < 60) return t("relative.secondsAgo", { n: sec });
  const min = Math.round(sec / 60);
  if (min < 60) return t(min === 1 ? "relative.minuteAgo" : "relative.minutesAgo", { n: min });
  const hr = Math.round(min / 60);
  if (hr < 24) return t(hr === 1 ? "relative.hourAgo" : "relative.hoursAgo", { n: hr });
  const day = Math.round(hr / 24);
  if (day < 30) return t(day === 1 ? "relative.dayAgo" : "relative.daysAgo", { n: day });
  const mo = Math.round(day / 30);
  if (mo < 12) return t(mo === 1 ? "relative.monthAgo" : "relative.monthsAgo", { n: mo });
  const yr = Math.round(mo / 12);
  return t(yr === 1 ? "relative.yearAgo" : "relative.yearsAgo", { n: yr });
}
