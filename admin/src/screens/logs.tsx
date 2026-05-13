import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";
import { AdminPage } from "../layout/admin_page";
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
import { ScrollText } from "@/lib/ui/icons";

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

  // Auto-poll while on page 1 — Logs is debug-tool first; operator
  // expects to SEE new lines flowing. Pages 2+ stop auto-refresh so a
  // historical browse isn't yanked from under the operator's cursor.
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
    refetchInterval: page === 1 ? 10_000 : false,
  });
  const isLive = page === 1;
  // Pulse ring next to the title every time a fresh fetch lands while
  // on page 1 — visual signal that the tail is alive.
  const [pulseKey, setPulseKey] = useState(0);
  useEffect(() => {
    if (!q.dataUpdatedAt) return;
    setPulseKey((k) => k + 1);
  }, [q.dataUpdatedAt]);

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

  return (
    <AdminPage>
      <AdminPage.Header
        title={
          <span className="inline-flex items-center gap-2">
            <ScrollText className="h-5 w-5 text-muted-foreground" />
            Logs
            {isLive ? (
              <span
                key={pulseKey}
                className="inline-flex items-center gap-1.5 text-[10px] uppercase tracking-wide text-primary"
                title="Live tail — refetches every 10s on page 1"
              >
                <LivePulseDot />
                live
              </span>
            ) : (
              <Badge variant="outline" className="font-mono text-[10px] uppercase tracking-wide">
                paused (page {page})
              </Badge>
            )}
          </span>
        }
        description={
          <>
            {total} event{total === 1 ? "" : "s"} total. Showing newest first.
            Past 14 days by default (configurable via{" "}
            <code className="font-mono">logs.retention_days</code>).
            Debug surface — for compliance / forensic review, see{" "}
            <a href="/_/logs/audit" className="underline">Audit log</a>.
          </>
        }
        actions={<Pager page={page} totalPages={totalPages} onChange={setPage} />}
      />

      <AdminPage.Toolbar>
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
            className="w-64 h-8 font-mono text-xs"
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
      </AdminPage.Toolbar>

      <AdminPage.Body>
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
                      <TableCell className="font-mono text-xs text-muted-foreground whitespace-nowrap">
                        {e.created}
                      </TableCell>
                      <TableCell>
                        <Badge variant={levelVariant(e.level)}>{e.level}</Badge>
                      </TableCell>
                      <TableCell className="max-w-md truncate">{e.message}</TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground max-w-xs truncate">
                        {attrsPreview(e.attrs)}
                      </TableCell>
                      <TableCell className="font-mono text-xs" title={e.request_id ?? ""}>
                        {e.request_id ? e.request_id.slice(0, 8) + "…" : "—"}
                      </TableCell>
                      <TableCell className="font-mono text-xs" title={e.user_id ?? ""}>
                        {e.user_id ? e.user_id.slice(0, 8) + "…" : "—"}
                      </TableCell>
                    </TableRow>
                    {isOpen ? (
                      <TableRow>
                        <TableCell colSpan={6} className="bg-muted">
                          <pre className="font-mono text-xs text-foreground whitespace-pre-wrap break-all p-2">
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
      </AdminPage.Body>
    </AdminPage>
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

// LivePulseDot — small green dot with a pulse animation; signals that
// the Logs screen is in live-tail mode (refetchInterval armed). The
// animation uses `animate-ping` (provided by tw-animate-css) on an
// absolutely-positioned outer disc; inner disc is the static fill.
function LivePulseDot() {
  return (
    <span className="relative inline-flex h-1.5 w-1.5">
      <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary opacity-75" />
      <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-primary" />
    </span>
  );
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
