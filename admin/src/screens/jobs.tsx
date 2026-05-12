import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
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

// Jobs queue browser — paginated, filterable list of `_jobs` rows.
// Backend endpoint: GET /api/_admin/jobs (v1.7.7+).
//
// Read-only: actions like cancel / run-now / reset live in the
// `railbase jobs` CLI. The admin screen is the operator's "what's
// happening" pane, not the control surface.

type StatusFilter = "" | "pending" | "running" | "completed" | "failed" | "cancelled";

export function JobsScreen() {
  const [page, setPage] = useState(1);
  const perPage = 50;

  const [status, setStatus] = useState<StatusFilter>("");
  const [kindInput, setKindInput] = useState("");
  const [kind, setKind] = useState(""); // debounced
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // Debounce the kind filter input. 300ms matches the logs viewer.
  useEffect(() => {
    const t = setTimeout(() => {
      setKind(kindInput);
    }, 300);
    return () => clearTimeout(t);
  }, [kindInput]);

  // Reset to page 1 whenever any filter changes.
  useEffect(() => {
    setPage(1);
  }, [status, kind]);

  const q = useQuery({
    queryKey: ["jobs", { page, perPage, status, kind }],
    queryFn: () =>
      adminAPI.jobs({
        page,
        perPage,
        status: status || undefined,
        kind: kind || undefined,
      }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Jobs queue</h1>
          <p className="text-sm text-muted-foreground">
            {total} job{total === 1 ? "" : "s"} total. Showing newest first.
            Use the <code className="rb-mono">railbase jobs</code> CLI for cancel /
            run-now / reset / recover.
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
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
            className="w-56 h-8 rb-mono text-xs"
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
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>created</TableHead>
                <TableHead>kind</TableHead>
                <TableHead>queue</TableHead>
                <TableHead>status</TableHead>
                <TableHead>attempts</TableHead>
                <TableHead>last error</TableHead>
                <TableHead>id</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(q.data?.items ?? []).map((j) => {
                const isOpen = expandedId === j.id;
                return (
                  <Fragment key={j.id}>
                    <TableRow
                      onClick={() => setExpandedId(isOpen ? null : j.id)}
                      className="cursor-pointer"
                    >
                      <TableCell className="rb-mono text-xs text-muted-foreground whitespace-nowrap">
                        {j.created_at}
                      </TableCell>
                      <TableCell className="rb-mono">{j.kind}</TableCell>
                      <TableCell className="rb-mono text-xs text-muted-foreground">{j.queue}</TableCell>
                      <TableCell>
                        <Badge variant={statusVariant(j.status)}>{j.status}</Badge>
                      </TableCell>
                      <TableCell className="rb-mono text-xs whitespace-nowrap">
                        {j.attempts}/{j.max_attempts}
                      </TableCell>
                      <TableCell className="max-w-md truncate text-xs text-destructive">
                        {j.last_error ? firstLine(j.last_error) : ""}
                      </TableCell>
                      <TableCell className="rb-mono text-xs" title={j.id}>
                        {j.id.slice(0, 8)}…
                      </TableCell>
                    </TableRow>
                    {isOpen ? (
                      <TableRow>
                        <TableCell colSpan={7} className="bg-muted">
                          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 p-3 text-xs">
                            <dt className="text-muted-foreground">id</dt>
                            <dd className="rb-mono">{j.id}</dd>
                            <dt className="text-muted-foreground">queue</dt>
                            <dd className="rb-mono">{j.queue}</dd>
                            <dt className="text-muted-foreground">run_after</dt>
                            <dd className="rb-mono">{j.run_after}</dd>
                            <dt className="text-muted-foreground">started_at</dt>
                            <dd className="rb-mono">{j.started_at ?? "—"}</dd>
                            <dt className="text-muted-foreground">completed_at</dt>
                            <dd className="rb-mono">{j.completed_at ?? "—"}</dd>
                            <dt className="text-muted-foreground">locked_by</dt>
                            <dd className="rb-mono">{j.locked_by ?? "—"}</dd>
                            <dt className="text-muted-foreground">locked_until</dt>
                            <dd className="rb-mono">{j.locked_until ?? "—"}</dd>
                            <dt className="text-muted-foreground">cron_id</dt>
                            <dd className="rb-mono">{j.cron_id ?? "—"}</dd>
                            {j.last_error ? (
                              <>
                                <dt className="text-muted-foreground self-start">last_error</dt>
                                <dd>
                                  <pre className="rb-mono text-xs text-destructive whitespace-pre-wrap break-all m-0">
                                    {j.last_error}
                                  </pre>
                                </dd>
                              </>
                            ) : null}
                          </dl>
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
              {q.data?.items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="text-muted-foreground text-center py-4">
                    No jobs.
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
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
// table cell. Full text shows in the expanded sub-row.
function firstLine(s: string): string {
  for (const line of s.split("\n")) {
    const t = line.trim();
    if (t) return t;
  }
  return s;
}
