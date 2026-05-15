import { useEffect, useState } from "react";
import { adminAPI } from "../api/admin";
import type { JobRecord } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// Jobs queue browser — paginated, filterable list of `_jobs` rows.
// Backend endpoint: GET /api/_admin/jobs (v1.7.7+).
//
// Read-only: actions like cancel / run-now / reset live in the
// `railbase jobs` CLI. The admin screen is the operator's "what's
// happening" pane, not the control surface.
//
// Server-paginated via QDatatable's `fetch` mode — the table owns
// page/pageSize; bespoke status/kind filters flow through `deps`.

type StatusFilter = "" | "pending" | "running" | "completed" | "failed" | "cancelled";

function buildJobsColumns(t: Translator["t"]): ColumnDef<JobRecord>[] {
  return [
    {
      id: "created",
      header: t("jobs.col.created"),
      accessor: "created_at",
      cell: (j) => (
        <span className="font-mono text-xs text-muted-foreground whitespace-nowrap">
          {j.created_at}
        </span>
      ),
    },
    {
      id: "kind",
      header: t("jobs.col.kind"),
      accessor: "kind",
      cell: (j) => <span className="font-mono">{j.kind}</span>,
    },
    {
      id: "queue",
      header: t("jobs.col.queue"),
      accessor: "queue",
      cell: (j) => (
        <span className="font-mono text-xs text-muted-foreground">{j.queue}</span>
      ),
    },
    {
      id: "status",
      header: t("jobs.col.status"),
      accessor: "status",
      cell: (j) => <Badge variant={statusVariant(j.status)}>{translateStatus(t, j.status)}</Badge>,
    },
    {
      id: "attempts",
      header: t("jobs.col.attempts"),
      cell: (j) => (
        <span className="font-mono text-xs whitespace-nowrap">
          {j.attempts}/{j.max_attempts}
        </span>
      ),
    },
    {
      id: "last_error",
      header: t("jobs.col.lastError"),
      accessor: "last_error",
      cell: (j) => (
        <span className="block max-w-md truncate text-xs text-destructive">
          {j.last_error ? firstLine(j.last_error) : ""}
        </span>
      ),
    },
    {
      id: "id",
      header: t("jobs.col.id"),
      accessor: "id",
      cell: (j) => (
        <span className="font-mono text-xs" title={j.id}>
          {j.id.slice(0, 8)}…
        </span>
      ),
    },
  ];
}

export function JobsScreen() {
  const { t } = useT();
  const [total, setTotal] = useState(0);

  const [status, setStatus] = useState<StatusFilter>("");
  const [kindInput, setKindInput] = useState("");
  const [kind, setKind] = useState(""); // debounced

  // Debounce the kind filter input. 300ms matches the logs viewer.
  useEffect(() => {
    const t = setTimeout(() => {
      setKind(kindInput);
    }, 300);
    return () => clearTimeout(t);
  }, [kindInput]);

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("jobs.title")}
        description={t("jobs.description", { count: total, cmd: "railbase jobs" })}
      />

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("jobs.filter.status")}</span>
          <select
            value={status}
            onChange={(e) => setStatus(e.currentTarget.value as StatusFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">{t("jobs.filter.all")}</option>
            <option value="pending">{t("jobs.status.pending")}</option>
            <option value="running">{t("jobs.status.running")}</option>
            <option value="completed">{t("jobs.status.completed")}</option>
            <option value="failed">{t("jobs.status.failed")}</option>
            <option value="cancelled">{t("jobs.status.cancelled")}</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("jobs.filter.kind")}</span>
          <Input
            type="text"
            value={kindInput}
            onInput={(e) => setKindInput(e.currentTarget.value)}
            placeholder={t("jobs.filter.kindPlaceholder")}
            className="w-56 h-8 font-mono text-xs"
          />
        </label>
        {(status || kind) ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setStatus("");
              setKindInput("");
              setKind("");
            }}
          >
            {t("jobs.filter.clear")}
          </Button>
        ) : null}
      </AdminPage.Toolbar>

      <AdminPage.Body>
        <QDatatable
          columns={buildJobsColumns(t)}
          rowKey="id"
          pageSize={50}
          emptyMessage={t("jobs.empty")}
          deps={[status, kind]}
          fetch={async (params) => {
            const r = await adminAPI.jobs({
              page: params.page,
              perPage: params.pageSize,
              status: status || undefined,
              kind: kind || undefined,
            });
            setTotal(r.totalItems);
            return { rows: r.items, total: r.totalItems };
          }}
        />
      </AdminPage.Body>
    </AdminPage>
  );
}

// translateStatus localises a job status string for badge display. The
// raw enum is the source of truth on the wire; this is presentation only.
function translateStatus(t: Translator["t"], s: string): string {
  switch (s) {
    case "pending":   return t("jobs.status.pending");
    case "running":   return t("jobs.status.running");
    case "completed": return t("jobs.status.completed");
    case "failed":    return t("jobs.status.failed");
    case "cancelled": return t("jobs.status.cancelled");
    default:          return s;
  }
}

// Status badge mapping. The kit's badge palette is small, so we
// approximate: failed → destructive, completed → default (primary
// emphasis as the "success" affordance), running/cancelled → secondary,
// pending → outline.
function statusVariant(s: string): "default" | "secondary" | "destructive" | "outline" {
  switch (s) {
    case "pending":   return "outline";
    case "running":   return "secondary";
    case "completed": return "default";
    case "failed":    return "destructive";
    case "cancelled": return "secondary";
    default:          return "outline";
  }
}

// Truncate a multi-line error to the first non-empty line for the
// table cell.
function firstLine(s: string): string {
  for (const line of s.split("\n")) {
    const t = line.trim();
    if (t) return t;
  }
  return s;
}
