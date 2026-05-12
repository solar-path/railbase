import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { BackupCreatedResponse } from "../api/types";

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
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Backups</h1>
          <p className="text-sm text-neutral-500">
            {items.length} archive{items.length === 1 ? "" : "s"}
            {items.length > 0 ? <> — {humanSize(totalSize)} total</> : null}.
            Stored under <code className="rb-mono">&lt;dataDir&gt;/backups/</code>.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => createM.mutate()}
            disabled={createM.isPending}
            className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800 disabled:opacity-60 disabled:cursor-not-allowed inline-flex items-center gap-2"
          >
            {createM.isPending ? (
              <>
                <Spinner />
                Creating…
              </>
            ) : (
              <>+ Create backup</>
            )}
          </button>
        </div>
      </header>

      {flash ? (
        <div className="rounded border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-700 flex items-start justify-between gap-3">
          <div>
            Backup created: <span className="rb-mono">{flash.name}</span>{" "}
            ({flash.manifest.tables_count} table
            {flash.manifest.tables_count === 1 ? "" : "s"},{" "}
            {flash.manifest.rows_count.toLocaleString()} row
            {flash.manifest.rows_count === 1 ? "" : "s"})
          </div>
          <button
            type="button"
            onClick={() => setFlash(null)}
            className="text-emerald-700/70 hover:text-emerald-900"
            aria-label="Dismiss"
          >
            ×
          </button>
        </div>
      ) : null}

      {createM.isError ? (
        <div className="rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          Backup failed:{" "}
          <span className="rb-mono">
            {(createM.error as { message?: string } | null)?.message ?? "unknown error"}
          </span>
        </div>
      ) : null}

      {q.isLoading ? (
        <div className="text-sm text-neutral-500">Loading…</div>
      ) : items.length === 0 ? (
        <div className="rounded border border-dashed border-neutral-300 bg-neutral-50 px-4 py-8 text-center text-sm text-neutral-500">
          No backups yet — click <span className="font-medium">Create backup</span> to make your first one.
        </div>
      ) : (
        <div className="rounded border border-neutral-200 bg-white overflow-x-auto">
          <table className="rb-table">
            <thead>
              <tr>
                <th>name</th>
                <th>size</th>
                <th>created</th>
              </tr>
            </thead>
            <tbody>
              {items.map((b) => (
                <tr key={b.path}>
                  <td className="rb-mono">{b.name}</td>
                  <td className="rb-mono text-xs whitespace-nowrap">
                    {humanSize(b.size_bytes)}
                  </td>
                  <td
                    className="rb-mono text-xs text-neutral-500 whitespace-nowrap"
                    title={b.created}
                  >
                    {relativeTime(b.created)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <p className="text-xs text-neutral-500">
        To restore a backup, use{" "}
        <code className="rb-mono">railbase backup restore &lt;path&gt; --force</code>{" "}
        from the CLI. Restoring from the admin UI is intentionally not
        supported in v1.
      </p>
    </div>
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
