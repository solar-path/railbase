import { useEffect, useState } from "react";
import { adminAPI } from "../api/admin";
import type { JobRecord } from "../api/types";
import { AdminPage } from "../layout/admin_page";
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

const columns: ColumnDef<JobRecord>[] = [
  {
    id: "created",
    header: "created",
    accessor: "created_at",
    cell: (j) => (
      <span className="font-mono text-xs text-muted-foreground whitespace-nowrap">
        {j.created_at}
      </span>
    ),
  },
  {
    id: "kind",
    header: "kind",
    accessor: "kind",
    cell: (j) => <span className="font-mono">{j.kind}</span>,
  },
  {
    id: "queue",
    header: "queue",
    accessor: "queue",
    cell: (j) => (
      <span className="font-mono text-xs text-muted-foreground">{j.queue}</span>
    ),
  },
  {
    id: "status",
    header: "status",
    accessor: "status",
    cell: (j) => <Badge variant={statusVariant(j.status)}>{j.status}</Badge>,
  },
  {
    id: "attempts",
    header: "attempts",
    cell: (j) => (
      <span className="font-mono text-xs whitespace-nowrap">
        {j.attempts}/{j.max_attempts}
      </span>
    ),
  },
  {
    id: "last_error",
    header: "last error",
    accessor: "last_error",
    cell: (j) => (
      <span className="block max-w-md truncate text-xs text-destructive">
        {j.last_error ? firstLine(j.last_error) : ""}
      </span>
    ),
  },
  {
    id: "id",
    header: "id",
    accessor: "id",
    cell: (j) => (
      <span className="font-mono text-xs" title={j.id}>
        {j.id.slice(0, 8)}…
      </span>
    ),
  },
];

export function JobsScreen() {
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
        title="Jobs queue"
        description={
          <>
            {total} job{total === 1 ? "" : "s"} total. Showing newest first.
            Use the <code className="font-mono">railbase jobs</code> CLI for cancel /
            run-now / reset / recover.
          </>
        }
      />

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">status</span>
          <select
            value={status}
            onChange={(e) => setStatus(e.currentTarget.value as StatusFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">all</option>
            <option value="pending">pending</option>
            <option value="running">running</option>
            <option value="completed">completed</option>
            <option value="failed">failed</option>
            <option value="cancelled">cancelled</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">kind</span>
          <Input
            type="text"
            value={kindInput}
            onInput={(e) => setKindInput(e.currentTarget.value)}
            placeholder="substring"
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
            clear
          </Button>
        ) : null}
      </AdminPage.Toolbar>

      <AdminPage.Body>
        <QDatatable
          columns={columns}
          rowKey="id"
          pageSize={50}
          emptyMessage="No jobs."
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
