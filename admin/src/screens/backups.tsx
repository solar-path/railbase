import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { BackupCreatedResponse } from "../api/types";
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

// Backups admin screen — read-only listing of .tar.gz archives in
// <DataDir>/backups/ plus a "create new backup" button. Backend:
// GET/POST /api/_admin/backups (v1.7.7 §3.11 deferred slice).
//
// Restore is intentionally NOT surfaced here — the operator path is
// `railbase backup restore` from the CLI. Restoring from a one-click
// button in a browser is the kind of thing that destroys production
// at 3 a.m. by accident.
//
// No pagination — operators typically have < 30 daily archives
// before retention sweeps; a flat table is fine.

export function BackupsScreen() {
  const qc = useQueryClient();

  // Success banner state — populated by a successful Create, cleared
  // after 5 seconds OR when the user dismisses it. We hold the full
  // response so the banner text can reference name + manifest counts.
  const [flash, setFlash] = useState<BackupCreatedResponse | null>(null);

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

  const createM = useMutation({
    mutationFn: () => adminAPI.backupsCreate(),
    onSuccess: (data) => {
      setFlash(data);
      void qc.invalidateQueries({ queryKey: ["backups"] });
    },
  });

  const items = q.data?.items ?? [];
  const totalSize = items.reduce((acc, it) => acc + (it.size_bytes ?? 0), 0);

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
            disabled={createM.isPending}
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

      {createM.isError ? (
        <div className="rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          Backup failed:{" "}
          <span className="font-mono">
            {(createM.error as { message?: string } | null)?.message ?? "unknown error"}
          </span>
        </div>
      ) : null}

      <AdminPage.Body className="space-y-4">
      {q.isLoading ? (
        <div className="text-sm text-muted-foreground">Loading…</div>
      ) : items.length === 0 ? (
        <div className="rounded border border-dashed border-input bg-muted px-4 py-8 text-center text-sm text-muted-foreground">
          No backups yet — click <span className="font-medium">Create backup</span> to make your first one.
        </div>
      ) : (
        <Card>
          <CardContent className="p-0 overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>name</TableHead>
                  <TableHead>size</TableHead>
                  <TableHead>created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((b) => (
                  <TableRow key={b.path}>
                    <TableCell className="font-mono">{b.name}</TableCell>
                    <TableCell className="font-mono text-xs whitespace-nowrap">
                      {humanSize(b.size_bytes)}
                    </TableCell>
                    <TableCell
                      className="font-mono text-xs text-muted-foreground whitespace-nowrap"
                      title={b.created}
                    >
                      {relativeTime(b.created)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      <p className="text-xs text-muted-foreground">
        To restore a backup, use{" "}
        <code className="font-mono">railbase backup restore &lt;path&gt; --force</code>{" "}
        from the CLI. Restoring from the admin UI is intentionally not
        supported in v1.
      </p>
      </AdminPage.Body>
    </AdminPage>
  );
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
