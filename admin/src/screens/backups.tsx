import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import type {
  BackupRecord,
  BackupCreatedResponse,
  BackupsCapabilities,
  BackupsRestoreDryRunResponse,
  CronSchedule,
} from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { toast } from "@/lib/ui/sonner.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerFooter,
  DrawerHeader,
  DrawerTitle,
} from "@/lib/ui/drawer.ui";
import {
  QEditableForm,
  type QEditableField,
} from "@/lib/ui/QEditableForm.ui";
import { CronInput, CronCell } from "../fields/cron";
import { useT, type Translator } from "../i18n";

// Backups admin screen — read-only listing of .tar.gz archives in
// <DataDir>/backups/ plus a "create new backup" button, AND a
// "Scheduled jobs" section that surfaces the persisted `_cron` table
// (manual + scheduled archives are indistinguishable on disk — same
// filename pattern, same retention sweep).
//
// Restore is intentionally NOT surfaced here — the operator path is
// `railbase backup restore` from the CLI. Restoring from a one-click
// button in a browser is the kind of thing that destroys production
// at 3 a.m. by accident.
//
// The Scheduled jobs section uses the general /api/_admin/cron API,
// so although the screen header is "Backups", every cron row shows
// here — including system schedules (cleanup_*, audit_seal, ...).
// Builtins are visually flagged and protected from delete + kind
// change (see internal/api/adminapi/cron.go). The "+ New schedule"
// drawer defaults to `kind=scheduled_backup` so the primary
// "schedule a backup" flow is one click + one form.

function buildArchiveColumns(t: Translator["t"]): ColumnDef<BackupRecord>[] {
  return [
    {
      id: "name",
      header: t("backups.col.name"),
      accessor: "name",
      sortable: true,
      cell: (b) => <span class="font-mono">{b.name}</span>,
    },
    {
      id: "size",
      header: t("backups.col.size"),
      accessor: "size_bytes",
      sortable: true,
      cell: (b) => (
        <span class="font-mono text-xs whitespace-nowrap">
          {humanSize(b.size_bytes)}
        </span>
      ),
    },
    {
      id: "created",
      header: t("backups.col.created"),
      accessor: "created",
      sortable: true,
      cell: (b) => (
        <span
          class="font-mono text-xs text-muted-foreground whitespace-nowrap"
          title={b.created}
        >
          {relativeTime(t, b.created)}
        </span>
      ),
    },
  ];
}

export function BackupsScreen() {
  const { t } = useT();
  const qc = useQueryClient();

  // Success banner state — populated by a successful Create, cleared
  // after 5 seconds OR when the user dismisses it. We hold the full
  // response so the banner text can reference name + manifest counts.
  const [flash, setFlash] = useState<BackupCreatedResponse | null>(null);

  // Restore success banner — sticky (no auto-fade): TRUNCATE CASCADE
  // is one of the few admin actions where the operator deserves a
  // permanent ack until they dismiss it.
  const [restoreFlash, setRestoreFlash] = useState<{
    archive: string;
    tables: number;
    rows: number;
    forced: boolean;
  } | null>(null);

  // Which archive (if any) the operator has opened the Restore drawer
  // for. `null` ↔ drawer closed.
  const [restoreTarget, setRestoreTarget] = useState<BackupRecord | null>(null);

  // Auto-fade the banner. 5 s matches the spec; long enough for a
  // human to read "Backup created: foo (12 tables, 4321 rows)" but
  // short enough to not linger.
  useEffect(() => {
    if (!flash) return;
    const t = setTimeout(() => setFlash(null), 5_000);
    return () => clearTimeout(t);
  }, [flash]);

  const q = useQuery({
    queryKey: ["backups"],
    queryFn: () => adminAPI.backupsList(),
  });

  // Capabilities probe — drives Restore affordance visibility + the
  // "disabled because …" tooltip. We poll while a restore is in
  // flight so the in-progress banner clears on its own once the
  // server flips maintenance.End().
  const capsQ = useQuery({
    queryKey: ["backups-capabilities"],
    queryFn: () => adminAPI.backupsCapabilities(),
    refetchInterval: (query) =>
      query.state.data?.maintenance_active ? 3_000 : false,
  });
  const caps = capsQ.data;

  const createM = useMutation({
    mutationFn: () => adminAPI.backupsCreate(),
    onSuccess: (data) => {
      setFlash(data);
      void qc.invalidateQueries({ queryKey: ["backups"] });
    },
  });

  const items = q.data?.items ?? [];
  const totalSize = items.reduce((acc, it) => acc + (it.size_bytes ?? 0), 0);

  // Render the Restore row action only when the deployment + admin
  // both clear the bar. We still show it (greyed) when capabilities
  // are still loading so the layout doesn't jitter; the actual click
  // hits a disabled menu item until caps land.
  const restoreVisible = Boolean(caps?.ui_restore_enabled && caps?.can_restore);

  // Disable both Create and Restore while a restore is mid-flight —
  // the server would 503 the Create anyway (it's a /api/* route the
  // maintenance middleware fences) but cleaner to gate at the UI.
  const maintenanceActive = Boolean(caps?.maintenance_active);

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("backups.title")}
        description={
          <>
            {t("backups.archiveCount", { count: items.length })}
            {items.length > 0 ? <> — {t("backups.totalSize", { size: humanSize(totalSize) })}</> : null}.{" "}
            {t("backups.storedUnder")}{" "}
            <code className="font-mono">&lt;dataDir&gt;/backups/</code>.
          </>
        }
        actions={
          <Button
            onClick={() => createM.mutate()}
            disabled={createM.isPending || maintenanceActive}
            size="sm"
          >
            {createM.isPending ? (
              <>
                <Spinner />
                {t("backups.creating")}
              </>
            ) : (
              <>{t("backups.create")}</>
            )}
          </Button>
        }
      />

      {maintenanceActive ? (
        <div className="rounded border border-amber-400/40 bg-amber-50 dark:bg-amber-950/40 px-3 py-2 text-sm text-amber-900 dark:text-amber-200 flex items-center gap-3">
          <Spinner />
          <div>
            <strong>{t("backups.restoreInProgress")}</strong>{" "}
            {t("backups.restoreInProgressLead")}{" "}
            <code className="font-mono">/api/*</code>{" "}
            {t("backups.restoreInProgressMid")}{" "}
            <code className="font-mono">Retry-After: 30</code>.{" "}
            {t("backups.restoreInProgressTail")}
          </div>
        </div>
      ) : null}

      {flash ? (
        <div className="rounded border border-primary/40 bg-primary/10 px-3 py-2 text-sm text-primary flex items-start justify-between gap-3">
          <div>
            {t("backups.createdBanner")}{" "}
            <span className="font-mono">{flash.name}</span>{" "}
            ({t("backups.tableCount", { count: flash.manifest.tables_count })},{" "}
            {t("backups.rowCount", { count: flash.manifest.rows_count.toLocaleString() })})
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setFlash(null)}
            aria-label={t("backups.dismiss")}
            className="text-primary/70 hover:text-primary hover:bg-transparent h-auto p-0"
          >
            ×
          </Button>
        </div>
      ) : null}

      {restoreFlash ? (
        <div className="rounded border border-primary/40 bg-primary/10 px-3 py-2 text-sm text-primary flex items-start justify-between gap-3">
          <div>
            {t("backups.restoredBanner")}{" "}
            <span className="font-mono">{restoreFlash.archive}</span>{" "}
            ({t("backups.tableCount", { count: restoreFlash.tables })},{" "}
            {t("backups.rowCount", { count: restoreFlash.rows.toLocaleString() })})
            {restoreFlash.forced ? (
              <span className="ml-2 text-xs">{t("backups.forcedNote")}</span>
            ) : null}
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setRestoreFlash(null)}
            aria-label={t("backups.dismiss")}
            className="text-primary/70 hover:text-primary hover:bg-transparent h-auto p-0"
          >
            ×
          </Button>
        </div>
      ) : null}

      {createM.isError ? (
        <div className="rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {t("backups.createFailed")}{" "}
          <span className="font-mono">
            {(createM.error as { message?: string } | null)?.message ?? t("backups.unknownError")}
          </span>
        </div>
      ) : null}

      <AdminPage.Body className="space-y-6">
        <QDatatable
          columns={buildArchiveColumns(t)}
          data={items}
          loading={q.isLoading}
          rowKey="path"
          emptyMessage={t("backups.empty")}
          rowActions={(row) => {
            const actions: Array<{
              label: string;
              destructive?: boolean;
              disabled?: () => boolean;
              onSelect: () => void;
            }> = [];
            if (restoreVisible) {
              actions.push({
                label: t("backups.restoreAction"),
                destructive: true,
                disabled: () => maintenanceActive,
                onSelect: () => setRestoreTarget(row),
              });
            }
            return actions;
          }}
        />

        <p className="text-xs text-muted-foreground">
          {restoreVisible ? (
            <>
              {t("backups.help.restoreLead")}{" "}
              <code className="font-mono">RAILBASE_ENABLE_UI_RESTORE</code>{" "}
              {t("backups.help.plus")}{" "}
              <code className="font-mono">admin.backup.restore</code>{" "}
              {t("backups.help.restoreMid")}{" "}
              <code className="font-mono">
                railbase backup restore &lt;path&gt; --force
              </code>
              .
            </>
          ) : caps && !caps.ui_restore_enabled ? (
            <>
              {t("backups.help.disabledLead")}{" "}
              <code className="font-mono">RAILBASE_ENABLE_UI_RESTORE=true</code>{" "}
              {t("backups.help.disabledMid")}{" "}
              <code className="font-mono">
                railbase backup restore &lt;path&gt; --force
              </code>{" "}
              {t("backups.help.disabledTail")}
            </>
          ) : caps && !caps.can_restore ? (
            <>
              {t("backups.help.noRbacLead")}{" "}
              <code className="font-mono">admin.backup.restore</code>{" "}
              {t("backups.help.noRbacMid")}{" "}
              <code className="font-mono">site:system_admin</code>{" "}
              {t("backups.help.noRbacTail")}
            </>
          ) : (
            <>{t("backups.help.onDemand")}</>
          )}
        </p>

        <SchedulesSection t={t} />
      </AdminPage.Body>

      <RestoreDrawer
        target={restoreTarget}
        caps={caps}
        onClose={() => setRestoreTarget(null)}
        onRestored={(resp) => {
          setRestoreTarget(null);
          setRestoreFlash({
            archive: resp.archive,
            tables: resp.tables_count,
            rows: resp.rows_count,
            forced: resp.forced,
          });
          void qc.invalidateQueries({ queryKey: ["backups"] });
          void qc.invalidateQueries({ queryKey: ["backups-capabilities"] });
        }}
        t={t}
      />
    </AdminPage>
  );
}

// ─── Restore Drawer ───────────────────────────────────────────
// Confirms the destructive action with three layers of friction:
//   1. type-to-confirm — the operator MUST type the exact archive name
//   2. dry-run preview — auto-runs on open, shows schema head match +
//      table/row counts the restore will TRUNCATE
//   3. force checkbox — required iff the dry-run reports a mismatch
//      between archive_schema_head and current_schema_head

function RestoreDrawer({
  target,
  caps,
  onClose,
  onRestored,
  t,
}: {
  target: BackupRecord | null;
  caps: BackupsCapabilities | undefined;
  onClose: () => void;
  onRestored: (resp: {
    archive: string;
    tables_count: number;
    rows_count: number;
    schema_head: string;
    forced: boolean;
  }) => void;
  t: Translator["t"];
}) {
  const open = target !== null;

  return (
    <Drawer
      direction="right"
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-xl">
        <DrawerHeader>
          <DrawerTitle>{t("backups.restoreDrawer.title")}</DrawerTitle>
          <DrawerDescription>
            {t("backups.restoreDrawer.descLead")}{" "}
            <strong>TRUNCATE CASCADE</strong>{" "}
            {t("backups.restoreDrawer.descMid")}{" "}
            <code className="font-mono">/api/*</code>{" "}
            {t("backups.restoreDrawer.descTail")}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {open && target ? (
            <RestoreDrawerBody
              key={target.path}
              target={target}
              caps={caps}
              onClose={onClose}
              onRestored={onRestored}
              t={t}
            />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

function RestoreDrawerBody({
  target,
  caps,
  onClose,
  onRestored,
  t,
}: {
  target: BackupRecord;
  caps: BackupsCapabilities | undefined;
  onClose: () => void;
  onRestored: (resp: {
    archive: string;
    tables_count: number;
    rows_count: number;
    schema_head: string;
    forced: boolean;
  }) => void;
  t: Translator["t"];
}) {
  const archive = target.name;
  const [confirmText, setConfirmText] = useState("");
  const [force, setForce] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  // Dry-run preview auto-runs when the drawer opens. The endpoint is
  // read-only (manifest inspection + migration-head probe) so we can
  // fire it without an extra confirm step.
  const dryRunQ = useQuery<BackupsRestoreDryRunResponse>({
    queryKey: ["backups-restore-dry-run", archive],
    queryFn: () => adminAPI.backupsRestoreDryRun(archive),
    refetchOnWindowFocus: false,
  });

  const submitM = useMutation({
    mutationFn: () =>
      adminAPI.backupsRestore(archive, {
        confirm: confirmText.trim(),
        force,
      }),
    onSuccess: (resp) => {
      onRestored(resp);
    },
    onError: (err) => setSubmitError(errMessage(err)),
  });

  const dry = dryRunQ.data;
  const dryError = dryRunQ.error;
  // Block the submit button until: dry-run resolved, archive format
  // version is OK, type-to-confirm matches exactly, and `force` is
  // ticked iff the migration head diverges.
  const confirmMatches = confirmText.trim() === archive;
  const needsForce = Boolean(dry && !dry.schema_head_matches);
  const formatBlocked = Boolean(dry && !dry.format_version_ok);
  const submitDisabled =
    !dry ||
    dryRunQ.isLoading ||
    submitM.isPending ||
    formatBlocked ||
    !confirmMatches ||
    (needsForce && !force) ||
    !caps?.ui_restore_enabled ||
    !caps?.can_restore;

  return (
    <div className="space-y-4">
      {/* Dry-run preview card. Renders three states: loading, error,
          and the full manifest summary. */}
      <section className="rounded border bg-muted/40 p-3 text-sm space-y-2">
        <header className="flex items-center justify-between">
          <strong className="text-xs uppercase tracking-wide text-muted-foreground">
            {t("backups.dryRun.title")}
          </strong>
          {dryRunQ.isFetching ? (
            <span className="text-xs text-muted-foreground flex items-center gap-1">
              <Spinner /> {t("backups.dryRun.inspecting")}
            </span>
          ) : null}
        </header>
        {dryRunQ.isLoading ? (
          <p className="text-xs text-muted-foreground">
            {t("backups.dryRun.reading")}
          </p>
        ) : dryError ? (
          <p className="text-xs text-destructive">
            {t("backups.dryRun.failed")}: {errMessage(dryError)}
          </p>
        ) : dry ? (
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs font-mono">
            <dt className="text-muted-foreground">{t("backups.dryRun.archive")}</dt>
            <dd>{dry.archive}</dd>
            <dt className="text-muted-foreground">{t("backups.dryRun.created")}</dt>
            <dd>{dry.created_at}</dd>
            <dt className="text-muted-foreground">railbase</dt>
            <dd>{dry.railbase_version}</dd>
            <dt className="text-muted-foreground">postgres</dt>
            <dd className="truncate" title={dry.postgres_version}>
              {dry.postgres_version}
            </dd>
            <dt className="text-muted-foreground">{t("backups.dryRun.format")}</dt>
            <dd className="flex items-center gap-2">
              v{dry.format_version}{" "}
              {dry.format_version_ok ? (
                <Badge variant="secondary" className="text-[10px]">
                  {t("backups.dryRun.ok")}
                </Badge>
              ) : (
                <Badge variant="outline" className="text-[10px] text-destructive border-destructive/40">
                  {t("backups.dryRun.unsupported")}
                </Badge>
              )}
            </dd>
            <dt className="text-muted-foreground">{t("backups.dryRun.schemaHead")}</dt>
            <dd className="flex items-center gap-2 truncate">
              <span className="truncate" title={dry.archive_schema_head}>
                {short(dry.archive_schema_head)}
              </span>
              {dry.schema_head_matches ? (
                <Badge variant="secondary" className="text-[10px]">
                  {t("backups.dryRun.matchesCurrent")}
                </Badge>
              ) : (
                <Badge variant="outline" className="text-[10px] text-destructive border-destructive/40">
                  {t("backups.dryRun.divergesFrom", { head: short(dry.current_schema_head) })}
                </Badge>
              )}
            </dd>
            <dt className="text-muted-foreground">{t("backups.dryRun.tablesRows")}</dt>
            <dd>
              {t("backups.dryRun.summary", {
                tables: dry.tables_count,
                rows: dry.rows_count.toLocaleString(),
              })}
            </dd>
          </dl>
        ) : null}
      </section>

      {formatBlocked ? (
        <div className="rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {t("backups.formatBlocked")}
        </div>
      ) : null}

      {needsForce && !formatBlocked ? (
        <div className="rounded border border-amber-400/40 bg-amber-50 dark:bg-amber-950/40 px-3 py-2 text-xs text-amber-900 dark:text-amber-200 space-y-2">
          <p>
            <strong>{t("backups.headMismatch.title")}</strong>{" "}
            {t("backups.headMismatch.body")}{" "}
            <em>{t("backups.headMismatch.iUnderstand")}</em>{" "}
            {t("backups.headMismatch.toProceed")}
          </p>
          <label className="flex items-center gap-2 text-amber-900 dark:text-amber-200">
            <Checkbox
              checked={force}
              onCheckedChange={(c) => setForce(Boolean(c))}
            />
            <span>{t("backups.headMismatch.forceLabel")}</span>
          </label>
        </div>
      ) : null}

      <div className="space-y-1.5">
        <label className="font-mono text-xs font-medium text-muted-foreground">
          {t("backups.typeToConfirm")}{" "}
          <span className="text-foreground">{archive}</span>
        </label>
        <Input
          value={confirmText}
          onInput={(e) => setConfirmText(e.currentTarget.value)}
          placeholder={archive}
          className="font-mono"
          autoComplete="off"
          autoCorrect="off"
          spellcheck={false}
        />
        {confirmText.length > 0 && !confirmMatches ? (
          <p className="text-xs text-amber-700 dark:text-amber-300">
            {t("backups.typeMismatch")}
          </p>
        ) : null}
      </div>

      {submitError ? (
        <div className="rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {t("backups.restoreFailed")}: <span className="font-mono">{submitError}</span>
        </div>
      ) : null}

      <DrawerFooter className="px-0 pb-0">
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose} disabled={submitM.isPending}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="destructive"
            onClick={() => {
              setSubmitError(null);
              submitM.mutate();
            }}
            disabled={submitDisabled}
          >
            {submitM.isPending ? (
              <>
                <Spinner />
                {t("backups.restoring")}
              </>
            ) : (
              <>{t("backups.restoreNow")}</>
            )}
          </Button>
        </div>
      </DrawerFooter>
    </div>
  );
}

// short trims a SHA-style migration head identifier to its first 8
// chars for display. The full value still rides as the title attr
// for hover-reveal.
function short(s: string): string {
  if (!s) return "—";
  if (s.length <= 12) return s;
  return s.slice(0, 8) + "…";
}

// ─── Scheduled jobs section ───────────────────────────────────
// Persisted cron schedules. The QDatatable shows every `_cron` row;
// the "+ New schedule" Drawer defaults to kind=scheduled_backup so
// the primary "schedule a backup" flow is one click + one form.

type ScheduleTarget = CronSchedule | "new" | null;

function SchedulesSection({ t }: { t: Translator["t"] }) {
  const qc = useQueryClient();
  const [target, setTarget] = useState<ScheduleTarget>(null);

  const listQ = useQuery({
    queryKey: ["cron-list"],
    queryFn: () => adminAPI.cronList(),
  });

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["cron-list"] });
  };

  const enableM = useMutation({
    mutationFn: ({ name, enabled }: { name: string; enabled: boolean }) =>
      enabled ? adminAPI.cronEnable(name) : adminAPI.cronDisable(name),
    onSuccess: (_data, vars) => {
      toast.success(
        vars.enabled
          ? t("backups.toast.scheduleEnabled")
          : t("backups.toast.scheduleDisabled"),
      );
      invalidate();
    },
    onError: (err) => toast.error(errMessage(err)),
  });

  const runNowM = useMutation({
    mutationFn: (name: string) => adminAPI.cronRunNow(name),
    onSuccess: (data) => {
      toast.success(t("backups.toast.jobQueued", { id: data.job_id.slice(0, 8) }));
    },
    onError: (err) => toast.error(errMessage(err)),
  });

  const deleteM = useMutation({
    mutationFn: (name: string) => adminAPI.cronDelete(name),
    onSuccess: () => {
      toast.success(t("backups.toast.scheduleDeleted"));
      invalidate();
    },
    onError: (err) => toast.error(errMessage(err)),
  });

  const columns: ColumnDef<CronSchedule>[] = [
    {
      id: "name",
      header: t("backups.sched.name"),
      accessor: "name",
      cell: (s) => (
        <span className="flex items-center gap-2 font-mono">
          {s.name}
          {s.is_builtin ? (
            <Badge variant="secondary" className="text-[10px]">
              {t("backups.sched.builtin")}
            </Badge>
          ) : null}
          {!s.kind_known ? (
            <Badge variant="outline" className="text-[10px] text-destructive border-destructive/40">
              {t("backups.sched.unknownKind")}
            </Badge>
          ) : null}
        </span>
      ),
    },
    {
      id: "expression",
      header: t("backups.sched.expression"),
      cell: (s) => <CronCell value={s.expression} />,
    },
    {
      id: "kind",
      header: t("backups.sched.kind"),
      cell: (s) => <span className="font-mono text-xs">{s.kind}</span>,
    },
    {
      id: "enabled",
      header: t("backups.sched.enabled"),
      cell: (s) =>
        s.enabled ? (
          <Badge variant="secondary">{t("backups.sched.on")}</Badge>
        ) : (
          <Badge variant="outline">{t("backups.sched.paused")}</Badge>
        ),
    },
    {
      id: "next_run_at",
      header: t("backups.sched.nextRun"),
      cell: (s) => (
        <span className="font-mono text-xs text-muted-foreground" title={s.next_run_at ?? ""}>
          {s.next_run_at ? relativeTime(t, s.next_run_at) : "—"}
        </span>
      ),
    },
    {
      id: "last_run_at",
      header: t("backups.sched.lastRun"),
      cell: (s) => (
        <span className="font-mono text-xs text-muted-foreground" title={s.last_run_at ?? ""}>
          {s.last_run_at ? relativeTime(t, s.last_run_at) : "—"}
        </span>
      ),
    },
  ];

  return (
    <section className="space-y-3">
      <header className="flex items-center justify-between gap-4 border-t pt-4">
        <div>
          <h2 className="text-lg font-semibold">{t("backups.sched.title")}</h2>
          <p className="text-xs text-muted-foreground">
            {t("backups.sched.descLead")}{" "}
            <code className="font-mono">scheduled_backup</code>{" "}
            {t("backups.sched.descTail")}
          </p>
        </div>
        <Button size="sm" onClick={() => setTarget("new")}>
          {t("backups.sched.new")}
        </Button>
      </header>

      <QDatatable
        columns={columns}
        data={listQ.data?.items ?? []}
        loading={listQ.isLoading}
        rowKey={(s) => s.name}
        emptyMessage={t("backups.sched.empty")}
        rowActions={(row) => [
          {
            label: t("backups.sched.runNow"),
            disabled: () => !row.enabled || runNowM.isPending,
            onSelect: () => runNowM.mutate(row.name),
          },
          {
            label: row.enabled ? t("backups.sched.disable") : t("backups.sched.enableAction"),
            onSelect: () =>
              enableM.mutate({ name: row.name, enabled: !row.enabled }),
          },
          { label: t("backups.sched.edit"), onSelect: () => setTarget(row) },
          {
            label: t("backups.sched.delete"),
            destructive: true,
            hidden: () => row.is_builtin,
            onSelect: () => {
              if (
                window.confirm(
                  t("backups.sched.deleteConfirm", { name: row.name }),
                )
              ) {
                deleteM.mutate(row.name);
              }
            },
          },
        ]}
      />

      <ScheduleEditorDrawer
        target={target}
        onClose={() => setTarget(null)}
        onSaved={() => {
          invalidate();
          setTarget(null);
        }}
        t={t}
      />
    </section>
  );
}

// ─── Editor Drawer ────────────────────────────────────────────

function ScheduleEditorDrawer({
  target,
  onClose,
  onSaved,
  t,
}: {
  target: ScheduleTarget;
  onClose: () => void;
  onSaved: () => void;
  t: Translator["t"];
}) {
  const open = target !== null;
  const isEdit = target !== null && target !== "new";
  const seed = isEdit ? (target as CronSchedule) : null;

  return (
    <Drawer
      direction="right"
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-xl">
        <DrawerHeader>
          <DrawerTitle>
            {isEdit
              ? t("backups.sched.editTitle", { name: seed!.name })
              : t("backups.sched.newTitle")}
          </DrawerTitle>
          <DrawerDescription>
            {isEdit
              ? seed!.is_builtin
                ? t("backups.sched.descBuiltin")
                : t("backups.sched.descEdit")
              : t("backups.sched.descNew")}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {open ? (
            <ScheduleEditorBody
              key={isEdit ? seed!.id : "new"}
              seed={seed}
              onClose={onClose}
              onSaved={onSaved}
              t={t}
            />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// ScheduleEditorBody — every editable knob lives inside one
// QEditableForm. `kind` is a discriminator: the form's `fields` is a
// function of the live draft, so changing `kind` to `scheduled_backup`
// makes `retention_days` + `out_dir` appear without the host having
// to lift draft state.
function ScheduleEditorBody({
  seed,
  onClose,
  onSaved,
  t,
}: {
  seed: CronSchedule | null;
  onClose: () => void;
  onSaved: () => void;
  t: Translator["t"];
}) {
  const isEdit = seed !== null;
  const isBuiltin = seed?.is_builtin === true;

  // Discovery: list registered job kinds so the operator can't pick a
  // kind the running binary won't execute.
  const kindsQ = useQuery({
    queryKey: ["cron-kinds"],
    queryFn: () => adminAPI.cronKinds(),
  });
  const availableKinds = useMemo(
    () => [...(kindsQ.data?.kinds ?? [])].sort(),
    [kindsQ.data],
  );

  // Initial payload values lifted out of the opaque seed for the
  // friendly per-kind fields. For unknown shapes we still pass them
  // through verbatim on save.
  const seedPayload = useMemo<Record<string, unknown>>(() => {
    if (seed?.payload && typeof seed.payload === "object") {
      return seed.payload as Record<string, unknown>;
    }
    return {};
  }, [seed]);

  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  // One-shot seed for QEditableForm. Carries every key so toggling
  // `kind` between `scheduled_backup` and other handlers doesn't drop
  // the operator's typing in retention_days / out_dir.
  const [draftSeed] = useState<Record<string, unknown>>(() => ({
    name: seed?.name ?? "",
    kind: seed?.kind ?? "scheduled_backup",
    expression: seed?.expression ?? "0 4 * * *",
    enabled: seed?.enabled ?? true,
    retention_days:
      typeof seedPayload.retention_days === "number"
        ? seedPayload.retention_days
        : 7,
    out_dir: typeof seedPayload.out_dir === "string" ? seedPayload.out_dir : "",
  }));

  // Function-typed `fields` — recomputed on each draft change so the
  // discriminator (`kind`) drives whether the backup-specific payload
  // fields render. Other kinds get an empty payload + a CLI hint
  // (rendered via QEditableForm's `notice` slot).
  const fieldsForDraft = (d: Record<string, unknown>): QEditableField[] => {
    const kind = String(d.kind ?? "");
    return [
      {
        key: "name",
        label: t("backups.sched.field.name"),
        required: !isEdit,
        readOnly: isEdit,
        helpText: !isEdit
          ? t("backups.sched.field.nameHelp")
          : undefined,
      },
      {
        key: "kind",
        label: t("backups.sched.field.kind"),
        required: true,
        readOnly: isBuiltin,
        helpText: isBuiltin ? (
          t("backups.sched.field.kindBuiltin")
        ) : kind !== "scheduled_backup" ? (
          <>
            {t("backups.sched.field.kindCliHint")}{" "}
            <code className="font-mono">
              railbase cron upsert {String(d.name || "<name>")} &quot;
              {String(d.expression ?? "")}&quot; {kind} --payload
              &apos;&#123;...&#125;&apos;
            </code>
          </>
        ) : undefined,
      },
      { key: "expression", label: t("backups.sched.field.expression"), required: true },
      { key: "enabled", label: t("backups.sched.field.enabled") },
      ...(kind === "scheduled_backup"
        ? [
            {
              key: "retention_days",
              label: t("backups.sched.field.retentionDays"),
              helpText: t("backups.sched.field.retentionDaysHelp"),
            } as QEditableField,
            {
              key: "out_dir",
              label: t("backups.sched.field.outDir"),
              helpText: t("backups.sched.field.outDirHelp"),
            } as QEditableField,
          ]
        : []),
    ];
  };

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "name":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="nightly-backup"
            className="font-mono"
          />
        );
      case "kind":
        return (
          <select
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm font-mono"
            value={(value as string) ?? ""}
            onChange={(e) => onChange(e.currentTarget.value)}
          >
            {availableKinds.length === 0 ? (
              <option value={String(value ?? "")}>{String(value ?? "")}</option>
            ) : (
              availableKinds.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))
            )}
          </select>
        );
      case "expression":
        return <CronInput value={value} onChange={onChange} />;
      case "enabled":
        return (
          <div class="flex items-center gap-2">
            <Checkbox
              checked={Boolean(value)}
              onCheckedChange={(checked) => onChange(Boolean(checked))}
            />
            <span class="text-sm">
              {value ? t("backups.sched.statusActive") : t("backups.sched.statusPaused")}
            </span>
          </div>
        );
      case "retention_days":
        return (
          <Input
            type="number"
            min={0}
            className="w-32 font-mono"
            value={value == null ? "" : String(value)}
            onInput={(e) => {
              const raw = parseInt(e.currentTarget.value || "0", 10);
              onChange(Number.isFinite(raw) && raw >= 0 ? raw : 0);
            }}
          />
        );
      case "out_dir":
        return (
          <Input
            type="text"
            placeholder={t("backups.sched.field.outDirPlaceholder")}
            className="font-mono"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
          />
        );
      default:
        return null;
    }
  };

  const validate = (
    d: Record<string, unknown>,
  ): Record<string, string> => {
    const fe: Record<string, string> = {};
    const name = String(d.name ?? "").trim();
    if (!isEdit) {
      if (!name) {
        fe.name = t("backups.sched.err.nameRequired");
      } else if (!/^[A-Za-z0-9_-]{1,80}$/.test(name)) {
        fe.name = t("backups.sched.err.nameInvalid");
      }
    }
    const kind = String(d.kind ?? "").trim();
    if (!kind) {
      fe.kind = t("backups.sched.err.kindRequired");
    }
    const expr = String(d.expression ?? "").trim();
    if (!expr) {
      fe.expression = t("backups.sched.err.expressionRequired");
    } else if (!/^[\d*/,-]+(\s+[\d*/,-]+){4}$/.test(expr)) {
      fe.expression = t("backups.sched.err.expressionInvalid");
    }
    if (kind === "scheduled_backup") {
      const r = Number(d.retention_days);
      if (!Number.isInteger(r) || r < 0) {
        fe.retention_days = t("backups.sched.err.retentionInvalid");
      }
    }
    return fe;
  };

  const handleSave = async (d: Record<string, unknown>) => {
    setFieldErrors({});
    setFormError(null);
    const fe = validate(d);
    if (Object.keys(fe).length > 0) {
      setFieldErrors(fe);
      return;
    }
    const kind = String(d.kind ?? "").trim();
    // Build the payload per kind. For scheduled_backup we emit the
    // friendly {retention_days, out_dir} shape; for other kinds we
    // preserve whatever the schedule already carried (so editing a
    // builtin's expression doesn't wipe its payload — empty `{}` for
    // most builtins, but future kinds may grow fields).
    let payload: unknown;
    if (kind === "scheduled_backup") {
      const body: Record<string, unknown> = {
        retention_days: Number(d.retention_days) || 0,
      };
      const outDir = String(d.out_dir ?? "").trim();
      if (outDir) body.out_dir = outDir;
      payload = body;
    } else {
      payload = seed?.payload ?? {};
    }

    try {
      await adminAPI.cronUpsert({
        name: isEdit ? seed!.name : String(d.name ?? "").trim(),
        expression: String(d.expression ?? "").trim(),
        kind,
        payload,
        enabled: Boolean(d.enabled),
      });
      onSaved();
    } catch (err) {
      setFormError(errMessage(err));
    }
  };

  return (
    <QEditableForm
      mode="create"
      fields={fieldsForDraft}
      values={draftSeed}
      renderInput={renderInput}
      onCreate={handleSave}
      submitLabel={isEdit ? t("common.save") : t("backups.sched.createSchedule")}
      onCancel={onClose}
      fieldErrors={fieldErrors}
      formError={formError}
    />
  );
}

// errMessage normalises errors from typed APIError + plain Error +
// stringy throws into a single human-readable line for toasts / banners.
function errMessage(err: unknown): string {
  if (isAPIError(err)) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}

// humanSize is a tiny human-readable byte formatter that mirrors the
// CLI's helper of the same name (see pkg/railbase/cli/backup.go). We
// keep the implementations parallel rather than ship a shared client
// util — the admin bundle stays self-contained.
function humanSize(n: number): string {
  const k = 1024;
  if (n < k) return `${n}B`;
  if (n < k * k) return `${(n / k).toFixed(1)}KB`;
  if (n < k * k * k) return `${(n / (k * k)).toFixed(1)}MB`;
  return `${(n / (k * k * k)).toFixed(1)}GB`;
}

// relativeTime renders an RFC3339 timestamp as a "2 hours ago" style
// label. Cheap inline impl: we don't pull date-fns just for one
// helper. Falls back to the raw timestamp if parsing fails, so a
// malformed value never blanks the cell.
function relativeTime(t: Translator["t"], iso: string): string {
  const parsed = Date.parse(iso);
  if (Number.isNaN(parsed)) return iso;
  const diffMs = Date.now() - parsed;
  const sec = Math.round(diffMs / 1000);
  if (sec > -5 && sec < 5) return t("backups.rel.justNow");
  if (sec < 0) {
    const abs = -sec;
    if (abs < 60) return t("backups.rel.inSeconds", { count: abs });
    const min = Math.round(abs / 60);
    if (min < 60) return t("backups.rel.inMinutes", { count: min });
    const hr = Math.round(min / 60);
    if (hr < 24) return t("backups.rel.inHours", { count: hr });
    const day = Math.round(hr / 24);
    return t("backups.rel.inDays", { count: day });
  }
  if (sec < 60) return t("backups.rel.secondsAgo", { count: sec });
  const min = Math.round(sec / 60);
  if (min < 60) return t("backups.rel.minutesAgo", { count: min });
  const hr = Math.round(min / 60);
  if (hr < 24) return t("backups.rel.hoursAgo", { count: hr });
  const day = Math.round(hr / 24);
  if (day < 30) return t("backups.rel.daysAgo", { count: day });
  const mo = Math.round(day / 30);
  if (mo < 12) return t("backups.rel.monthsAgo", { count: mo });
  const yr = Math.round(mo / 12);
  return t("backups.rel.yearsAgo", { count: yr });
}

// Spinner is a tiny inline SVG; the Tailwind `animate-spin` utility
// rotates it. Used inside the Create button while the mutation is in
// flight.
function Spinner() {
  return (
    <svg
      className="animate-spin h-3.5 w-3.5"
      xmlns="http://www.w3.org/2000/svg"
      fill="none"
      viewBox="0 0 24 24"
      aria-hidden="true"
    >
      <circle
        className="opacity-25"
        cx="12"
        cy="12"
        r="10"
        stroke="currentColor"
        strokeWidth="4"
      />
      <path
        className="opacity-75"
        fill="currentColor"
        d="M4 12a8 8 0 018-8v4a4 4 0 00-4 4H4z"
      />
    </svg>
  );
}
