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

const archiveColumns: ColumnDef<BackupRecord>[] = [
  {
    id: "name",
    header: "name",
    accessor: "name",
    sortable: true,
    cell: (b) => <span class="font-mono">{b.name}</span>,
  },
  {
    id: "size",
    header: "size",
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
    header: "created",
    accessor: "created",
    sortable: true,
    cell: (b) => (
      <span
        class="font-mono text-xs text-muted-foreground whitespace-nowrap"
        title={b.created}
      >
        {relativeTime(b.created)}
      </span>
    ),
  },
];

export function BackupsScreen() {
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
        title="Backups"
        description={
          <>
            {items.length} archive{items.length === 1 ? "" : "s"}
            {items.length > 0 ? <> — {humanSize(totalSize)} total</> : null}.
            Stored under <code className="font-mono">&lt;dataDir&gt;/backups/</code>.
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
                Creating…
              </>
            ) : (
              <>+ Create backup</>
            )}
          </Button>
        }
      />

      {maintenanceActive ? (
        <div className="rounded border border-amber-400/40 bg-amber-50 dark:bg-amber-950/40 px-3 py-2 text-sm text-amber-900 dark:text-amber-200 flex items-center gap-3">
          <Spinner />
          <div>
            <strong>Database restore in progress.</strong> User traffic to{" "}
            <code className="font-mono">/api/*</code> is being served 503 with{" "}
            <code className="font-mono">Retry-After: 30</code>. The admin UI
            stays reachable.
          </div>
        </div>
      ) : null}

      {flash ? (
        <div className="rounded border border-primary/40 bg-primary/10 px-3 py-2 text-sm text-primary flex items-start justify-between gap-3">
          <div>
            Backup created: <span className="font-mono">{flash.name}</span>{" "}
            ({flash.manifest.tables_count} table
            {flash.manifest.tables_count === 1 ? "" : "s"},{" "}
            {flash.manifest.rows_count.toLocaleString()} row
            {flash.manifest.rows_count === 1 ? "" : "s"})
          </div>
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

      {restoreFlash ? (
        <div className="rounded border border-primary/40 bg-primary/10 px-3 py-2 text-sm text-primary flex items-start justify-between gap-3">
          <div>
            Restored <span className="font-mono">{restoreFlash.archive}</span>{" "}
            ({restoreFlash.tables} table
            {restoreFlash.tables === 1 ? "" : "s"},{" "}
            {restoreFlash.rows.toLocaleString()} row
            {restoreFlash.rows === 1 ? "" : "s"})
            {restoreFlash.forced ? (
              <span className="ml-2 text-xs">— forced past head mismatch</span>
            ) : null}
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setRestoreFlash(null)}
            aria-label="Dismiss"
            className="text-primary/70 hover:text-primary hover:bg-transparent h-auto p-0"
          >
            ×
          </Button>
        </div>
      ) : null}

      {createM.isError ? (
        <div className="rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          Backup failed:{" "}
          <span className="font-mono">
            {(createM.error as { message?: string } | null)?.message ?? "unknown error"}
          </span>
        </div>
      ) : null}

      <AdminPage.Body className="space-y-6">
        <QDatatable
          columns={archiveColumns}
          data={items}
          loading={q.isLoading}
          rowKey="path"
          emptyMessage="No backups yet — click Create backup to make your first one."
          rowActions={(row) => {
            const actions: Array<{
              label: string;
              destructive?: boolean;
              disabled?: () => boolean;
              onSelect: () => void;
            }> = [];
            if (restoreVisible) {
              actions.push({
                label: "Restore…",
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
              Restoring TRUNCATES every table in the archive before
              re-inserting rows. The action is gated by{" "}
              <code className="font-mono">RAILBASE_ENABLE_UI_RESTORE</code> +
              the <code className="font-mono">admin.backup.restore</code> RBAC
              key, fences user traffic for the transaction window, and
              records an audit event. CLI alternative:{" "}
              <code className="font-mono">
                railbase backup restore &lt;path&gt; --force
              </code>
              .
            </>
          ) : caps && !caps.ui_restore_enabled ? (
            <>
              UI restore is disabled. Set{" "}
              <code className="font-mono">RAILBASE_ENABLE_UI_RESTORE=true</code>{" "}
              on the server to enable, or use{" "}
              <code className="font-mono">
                railbase backup restore &lt;path&gt; --force
              </code>{" "}
              from the CLI.
            </>
          ) : caps && !caps.can_restore ? (
            <>
              This admin lacks the{" "}
              <code className="font-mono">admin.backup.restore</code> RBAC
              action. Operators with{" "}
              <code className="font-mono">site:system_admin</code> can restore
              from the UI; downgraded admins must use the CLI.
            </>
          ) : (
            <>
              Restore is loaded on demand — the Restore action will appear
              once capabilities resolve.
            </>
          )}
        </p>

        <SchedulesSection />
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
          <DrawerTitle>Restore from backup</DrawerTitle>
          <DrawerDescription>
            This will <strong>TRUNCATE CASCADE</strong> every table in the
            archive before re-inserting rows. User traffic to{" "}
            <code className="font-mono">/api/*</code> is fenced for the
            transaction window. The action is recorded in the audit log.
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
            Dry-run preview
          </strong>
          {dryRunQ.isFetching ? (
            <span className="text-xs text-muted-foreground flex items-center gap-1">
              <Spinner /> inspecting…
            </span>
          ) : null}
        </header>
        {dryRunQ.isLoading ? (
          <p className="text-xs text-muted-foreground">
            Reading archive manifest…
          </p>
        ) : dryError ? (
          <p className="text-xs text-destructive">
            Dry-run failed: {errMessage(dryError)}
          </p>
        ) : dry ? (
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs font-mono">
            <dt className="text-muted-foreground">archive</dt>
            <dd>{dry.archive}</dd>
            <dt className="text-muted-foreground">created</dt>
            <dd>{dry.created_at}</dd>
            <dt className="text-muted-foreground">railbase</dt>
            <dd>{dry.railbase_version}</dd>
            <dt className="text-muted-foreground">postgres</dt>
            <dd className="truncate" title={dry.postgres_version}>
              {dry.postgres_version}
            </dd>
            <dt className="text-muted-foreground">format</dt>
            <dd className="flex items-center gap-2">
              v{dry.format_version}{" "}
              {dry.format_version_ok ? (
                <Badge variant="secondary" className="text-[10px]">
                  OK
                </Badge>
              ) : (
                <Badge variant="outline" className="text-[10px] text-destructive border-destructive/40">
                  unsupported
                </Badge>
              )}
            </dd>
            <dt className="text-muted-foreground">schema head</dt>
            <dd className="flex items-center gap-2 truncate">
              <span className="truncate" title={dry.archive_schema_head}>
                {short(dry.archive_schema_head)}
              </span>
              {dry.schema_head_matches ? (
                <Badge variant="secondary" className="text-[10px]">
                  matches current
                </Badge>
              ) : (
                <Badge variant="outline" className="text-[10px] text-destructive border-destructive/40">
                  diverges from {short(dry.current_schema_head)}
                </Badge>
              )}
            </dd>
            <dt className="text-muted-foreground">tables / rows</dt>
            <dd>
              {dry.tables_count} table
              {dry.tables_count === 1 ? "" : "s"} —{" "}
              {dry.rows_count.toLocaleString()} row
              {dry.rows_count === 1 ? "" : "s"} will be TRUNCATEd + re-inserted
            </dd>
          </dl>
        ) : null}
      </section>

      {formatBlocked ? (
        <div className="rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          This archive's format version is newer than the running binary
          supports. Upgrade Railbase or recreate the archive on a matching
          version before restoring.
        </div>
      ) : null}

      {needsForce && !formatBlocked ? (
        <div className="rounded border border-amber-400/40 bg-amber-50 dark:bg-amber-950/40 px-3 py-2 text-xs text-amber-900 dark:text-amber-200 space-y-2">
          <p>
            <strong>Schema head mismatch.</strong> The archive was created
            against a different migration head. Restoring may leave columns
            unpopulated or fail outright if the shape diverged. Tick{" "}
            <em>I understand</em> to proceed anyway.
          </p>
          <label className="flex items-center gap-2 text-amber-900 dark:text-amber-200">
            <Checkbox
              checked={force}
              onCheckedChange={(c) => setForce(Boolean(c))}
            />
            <span>I understand head heads diverge — force restore</span>
          </label>
        </div>
      ) : null}

      <div className="space-y-1.5">
        <label className="font-mono text-xs font-medium text-muted-foreground">
          To confirm, type the archive name exactly:{" "}
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
            Doesn't match yet — type the filename character-for-character.
          </p>
        ) : null}
      </div>

      {submitError ? (
        <div className="rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          Restore failed: <span className="font-mono">{submitError}</span>
        </div>
      ) : null}

      <DrawerFooter className="px-0 pb-0">
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose} disabled={submitM.isPending}>
            Cancel
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
                Restoring…
              </>
            ) : (
              <>Restore now</>
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

function SchedulesSection() {
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
      toast.success(`Schedule ${vars.enabled ? "enabled" : "disabled"}.`);
      invalidate();
    },
    onError: (err) => toast.error(errMessage(err)),
  });

  const runNowM = useMutation({
    mutationFn: (name: string) => adminAPI.cronRunNow(name),
    onSuccess: (data) => {
      toast.success(`Job queued: ${data.job_id.slice(0, 8)}…`);
    },
    onError: (err) => toast.error(errMessage(err)),
  });

  const deleteM = useMutation({
    mutationFn: (name: string) => adminAPI.cronDelete(name),
    onSuccess: () => {
      toast.success("Schedule deleted.");
      invalidate();
    },
    onError: (err) => toast.error(errMessage(err)),
  });

  const columns: ColumnDef<CronSchedule>[] = [
    {
      id: "name",
      header: "name",
      accessor: "name",
      cell: (s) => (
        <span className="flex items-center gap-2 font-mono">
          {s.name}
          {s.is_builtin ? (
            <Badge variant="secondary" className="text-[10px]">
              builtin
            </Badge>
          ) : null}
          {!s.kind_known ? (
            <Badge variant="outline" className="text-[10px] text-destructive border-destructive/40">
              unknown kind
            </Badge>
          ) : null}
        </span>
      ),
    },
    {
      id: "expression",
      header: "expression",
      cell: (s) => <CronCell value={s.expression} />,
    },
    {
      id: "kind",
      header: "kind",
      cell: (s) => <span className="font-mono text-xs">{s.kind}</span>,
    },
    {
      id: "enabled",
      header: "enabled",
      cell: (s) =>
        s.enabled ? (
          <Badge variant="secondary">on</Badge>
        ) : (
          <Badge variant="outline">paused</Badge>
        ),
    },
    {
      id: "next_run_at",
      header: "next run",
      cell: (s) => (
        <span className="font-mono text-xs text-muted-foreground" title={s.next_run_at ?? ""}>
          {s.next_run_at ? relativeTime(s.next_run_at) : "—"}
        </span>
      ),
    },
    {
      id: "last_run_at",
      header: "last run",
      cell: (s) => (
        <span className="font-mono text-xs text-muted-foreground" title={s.last_run_at ?? ""}>
          {s.last_run_at ? relativeTime(s.last_run_at) : "—"}
        </span>
      ),
    },
  ];

  return (
    <section className="space-y-3">
      <header className="flex items-center justify-between gap-4 border-t pt-4">
        <div>
          <h2 className="text-lg font-semibold">Scheduled jobs</h2>
          <p className="text-xs text-muted-foreground">
            Persisted cron schedules. The default destination for{" "}
            <code className="font-mono">scheduled_backup</code> mirrors the
            manual archives above — both share the same retention sweep.
          </p>
        </div>
        <Button size="sm" onClick={() => setTarget("new")}>
          + New schedule
        </Button>
      </header>

      <QDatatable
        columns={columns}
        data={listQ.data?.items ?? []}
        loading={listQ.isLoading}
        rowKey={(s) => s.name}
        emptyMessage="No scheduled jobs yet — click + New schedule to add one."
        rowActions={(row) => [
          {
            label: "Run now",
            disabled: () => !row.enabled || runNowM.isPending,
            onSelect: () => runNowM.mutate(row.name),
          },
          {
            label: row.enabled ? "Disable" : "Enable",
            onSelect: () =>
              enableM.mutate({ name: row.name, enabled: !row.enabled }),
          },
          { label: "Edit", onSelect: () => setTarget(row) },
          {
            label: "Delete",
            destructive: true,
            hidden: () => row.is_builtin,
            onSelect: () => {
              if (
                window.confirm(
                  `Delete schedule "${row.name}"? This cannot be undone.`,
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
      />
    </section>
  );
}

// ─── Editor Drawer ────────────────────────────────────────────

function ScheduleEditorDrawer({
  target,
  onClose,
  onSaved,
}: {
  target: ScheduleTarget;
  onClose: () => void;
  onSaved: () => void;
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
            {isEdit ? `Edit schedule: ${seed!.name}` : "New schedule"}
          </DrawerTitle>
          <DrawerDescription>
            {isEdit
              ? seed!.is_builtin
                ? "Builtin schedule — name + kind are locked. You can retune the expression or pause it."
                : "Update the cron expression, kind, payload, or pause state."
              : "Persisted cron job. Defaults to a backup schedule — pick a different kind for cleanup or other handlers."}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {open ? (
            <ScheduleEditorBody
              key={isEdit ? seed!.id : "new"}
              seed={seed}
              onClose={onClose}
              onSaved={onSaved}
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
}: {
  seed: CronSchedule | null;
  onClose: () => void;
  onSaved: () => void;
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
        label: "Name",
        required: !isEdit,
        readOnly: isEdit,
        helpText: !isEdit
          ? "Letters, digits, underscore, hyphen. Used as the schedule identifier — cannot be changed later."
          : undefined,
      },
      {
        key: "kind",
        label: "Kind",
        required: true,
        readOnly: isBuiltin,
        helpText: isBuiltin ? (
          "Builtin schedule — kind is locked."
        ) : kind !== "scheduled_backup" ? (
          <>
            This kind takes an empty payload. For richer per-kind
            configuration use the CLI:{" "}
            <code className="font-mono">
              railbase cron upsert {String(d.name || "<name>")} &quot;
              {String(d.expression ?? "")}&quot; {kind} --payload
              &apos;&#123;...&#125;&apos;
            </code>
          </>
        ) : undefined,
      },
      { key: "expression", label: "Cron expression", required: true },
      { key: "enabled", label: "Enabled" },
      ...(kind === "scheduled_backup"
        ? [
            {
              key: "retention_days",
              label: "Retention (days)",
              helpText:
                "Older archives are pruned after a successful backup. 0 disables pruning.",
            } as QEditableField,
            {
              key: "out_dir",
              label: "Output directory (optional)",
              helpText:
                "Defaults to <dataDir>/backups so manual + scheduled archives share the same retention sweep.",
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
              {value ? "Active" : "Paused (no jobs are queued)"}
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
            placeholder="leave blank for default"
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
        fe.name = "Name required";
      } else if (!/^[A-Za-z0-9_-]{1,80}$/.test(name)) {
        fe.name = "Name: letters, digits, underscore, hyphen (≤ 80 chars)";
      }
    }
    const kind = String(d.kind ?? "").trim();
    if (!kind) {
      fe.kind = "Kind required";
    }
    const expr = String(d.expression ?? "").trim();
    if (!expr) {
      fe.expression = "Cron expression required";
    } else if (!/^[\d*/,-]+(\s+[\d*/,-]+){4}$/.test(expr)) {
      fe.expression = "Must be 5 fields (digits, *, /, comma, hyphen)";
    }
    if (kind === "scheduled_backup") {
      const r = Number(d.retention_days);
      if (!Number.isInteger(r) || r < 0) {
        fe.retention_days = "Must be a non-negative integer";
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
      submitLabel={isEdit ? "Save" : "Create schedule"}
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
function relativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const diffMs = Date.now() - t;
  const sec = Math.round(diffMs / 1000);
  if (sec > -5 && sec < 5) return "just now";
  if (sec < 0) {
    const abs = -sec;
    if (abs < 60) return `in ${abs}s`;
    const min = Math.round(abs / 60);
    if (min < 60) return `in ${min} minute${min === 1 ? "" : "s"}`;
    const hr = Math.round(min / 60);
    if (hr < 24) return `in ${hr} hour${hr === 1 ? "" : "s"}`;
    const day = Math.round(hr / 24);
    return `in ${day} day${day === 1 ? "" : "s"}`;
  }
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
