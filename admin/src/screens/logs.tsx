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

// Logs viewer — paginated, filterable list of structured log events.
// Backend endpoint: GET /api/_admin/logs (v1.7.6+).
//
// Filters update the query key directly; changing any filter resets
// the page to 1. Search input is debounced (~300ms) so we don't fire
// a request on every keystroke.

type LevelFilter = "" | "debug" | "info" | "warn" | "error";

export function LogsScreen() {
  const [page, setPage] = useState(1);
  const perPage = 50;

  const [level, setLevel] = useState<LevelFilter>("");
  const [requestId, setRequestId] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState(""); // debounced
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // Debounce the search input. 300ms feels snappy without hammering.
  useEffect(() => {
    const t = setTimeout(() => {
      setSearch(searchInput);
    }, 300);
    return () => clearTimeout(t);
  }, [searchInput]);

  // Reset to page 1 whenever any filter changes. Keep this in an
  // effect rather than the setters because the debounced search lives
  // on its own clock.
  useEffect(() => {
    setPage(1);
  }, [level, search, requestId]);

  const q = useQuery({
    queryKey: ["logs", { page, perPage, level, search, request_id: requestId }],
    queryFn: () =>
      adminAPI.logs({
        page,
        perPage,
        level: level || undefined,
        search: search || undefined,
        request_id: requestId || undefined,
      }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Logs</h1>
          <p className="text-sm text-muted-foreground">
            {total} event{total === 1 ? "" : "s"} total. Showing newest first.
            Past 14 days by default (configurable via{" "}
            <code className="rb-mono">logs.retention_days</code>).
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">level</span>
          <select
            value={level}
            onChange={(e) => setLevel(e.currentTarget.value as LevelFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">all</option>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">search</span>
          <Input
            type="text"
            value={searchInput}
            onInput={(e) => setSearchInput(e.currentTarget.value)}
            placeholder="message substring"
            className="w-56 h-8"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">request_id</span>
          <Input
            type="text"
            value={requestId}
            onInput={(e) => setRequestId(e.currentTarget.value)}
            placeholder="exact match"
            className="w-64 h-8 rb-mono text-xs"
          />
        </label>
        {(level || search || requestId) ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setLevel("");
              setSearchInput("");
              setSearch("");
              setRequestId("");
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
                <TableHead>at</TableHead>
                <TableHead>level</TableHead>
                <TableHead>message</TableHead>
                <TableHead>attrs</TableHead>
                <TableHead>request</TableHead>
                <TableHead>user</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(q.data?.items ?? []).map((e) => {
                const isOpen = expandedId === e.id;
                return (
                  <Fragment key={e.id}>
                    <TableRow
                      onClick={() => setExpandedId(isOpen ? null : e.id)}
                      className="cursor-pointer"
                    >
                      <TableCell className="rb-mono text-xs text-muted-foreground whitespace-nowrap">
                        {e.created}
                      </TableCell>
                      <TableCell>
                        <Badge variant={levelVariant(e.level)}>{e.level}</Badge>
                      </TableCell>
                      <TableCell className="max-w-md truncate">{e.message}</TableCell>
                      <TableCell className="rb-mono text-xs text-muted-foreground max-w-xs truncate">
                        {attrsPreview(e.attrs)}
                      </TableCell>
                      <TableCell className="rb-mono text-xs" title={e.request_id ?? ""}>
                        {e.request_id ? e.request_id.slice(0, 8) + "…" : "—"}
                      </TableCell>
                      <TableCell className="rb-mono text-xs" title={e.user_id ?? ""}>
                        {e.user_id ? e.user_id.slice(0, 8) + "…" : "—"}
                      </TableCell>
                    </TableRow>
                    {isOpen ? (
                      <TableRow>
                        <TableCell colSpan={6} className="bg-muted">
                          <pre className="rb-mono text-xs text-foreground whitespace-pre-wrap break-all p-2">
                            {JSON.stringify(e.attrs ?? {}, null, 2)}
                          </pre>
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
              {q.data?.items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="text-muted-foreground text-center py-4">
                    No log events.
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

// levelVariant maps a log level to the closest Badge variant. The
// kit's badge palette is small (default/secondary/destructive/outline)
// so we approximate: error → destructive, warn → default (primary
// emphasis), info → secondary, debug → outline.
function levelVariant(l: string): "default" | "secondary" | "destructive" | "outline" {
  switch (l.toUpperCase()) {
    case "ERROR": return "destructive";
    case "WARN":  return "default";
    case "INFO":  return "secondary";
    case "DEBUG": return "outline";
    default:      return "outline";
  }
}

// Compact one-line preview of an attrs object for the table cell.
// Full JSON is shown in the expanded sub-row.
function attrsPreview(a: Record<string, unknown> | undefined | null): string {
  if (!a) return "";
  const keys = Object.keys(a);
  if (keys.length === 0) return "";
  try {
    return JSON.stringify(a);
  } catch {
    return `{${keys.join(", ")}}`;
  }
}
