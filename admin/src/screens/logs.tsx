import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";

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
          <p className="text-sm text-neutral-500">
            {total} event{total === 1 ? "" : "s"} total. Showing newest first.
            Past 14 days by default (configurable via{" "}
            <code className="rb-mono">logs.retention_days</code>).
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">level</span>
          <select
            value={level}
            onChange={(e) => setLevel(e.target.value as LevelFilter)}
            className="rounded border border-neutral-300 px-2 py-1"
          >
            <option value="">all</option>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">search</span>
          <input
            type="text"
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            placeholder="message substring"
            className="rounded border border-neutral-300 px-2 py-1 w-56"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">request_id</span>
          <input
            type="text"
            value={requestId}
            onChange={(e) => setRequestId(e.target.value)}
            placeholder="exact match"
            className="rounded border border-neutral-300 px-2 py-1 w-64 rb-mono text-xs"
          />
        </label>
        {(level || search || requestId) ? (
          <button
            type="button"
            onClick={() => {
              setLevel("");
              setSearchInput("");
              setSearch("");
              setRequestId("");
            }}
            className="rounded border border-neutral-300 px-2 py-1 text-neutral-600 hover:bg-neutral-100"
          >
            clear
          </button>
        ) : null}
      </div>

      <div className="rounded border border-neutral-200 bg-white overflow-x-auto">
        <table className="rb-table">
          <thead>
            <tr>
              <th>at</th>
              <th>level</th>
              <th>message</th>
              <th>attrs</th>
              <th>request</th>
              <th>user</th>
            </tr>
          </thead>
          <tbody>
            {(q.data?.items ?? []).map((e) => {
              const isOpen = expandedId === e.id;
              return (
                <Fragment key={e.id}>
                  <tr
                    onClick={() => setExpandedId(isOpen ? null : e.id)}
                    className="cursor-pointer"
                  >
                    <td className="rb-mono text-xs text-neutral-500 whitespace-nowrap">
                      {e.created}
                    </td>
                    <td>
                      <span className={"rounded px-1.5 py-0.5 text-xs " + levelColor(e.level)}>
                        {e.level}
                      </span>
                    </td>
                    <td className="max-w-md truncate">{e.message}</td>
                    <td className="rb-mono text-xs text-neutral-500 max-w-xs truncate">
                      {attrsPreview(e.attrs)}
                    </td>
                    <td className="rb-mono text-xs" title={e.request_id ?? ""}>
                      {e.request_id ? e.request_id.slice(0, 8) + "…" : "—"}
                    </td>
                    <td className="rb-mono text-xs" title={e.user_id ?? ""}>
                      {e.user_id ? e.user_id.slice(0, 8) + "…" : "—"}
                    </td>
                  </tr>
                  {isOpen ? (
                    <tr>
                      <td colSpan={6} className="bg-neutral-50">
                        <pre className="rb-mono text-xs text-neutral-700 whitespace-pre-wrap break-all p-2">
                          {JSON.stringify(e.attrs ?? {}, null, 2)}
                        </pre>
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
            {q.data?.items.length === 0 ? (
              <tr>
                <td colSpan={6} className="text-neutral-400 text-center py-4">
                  No log events.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function levelColor(l: string): string {
  switch (l.toUpperCase()) {
    case "DEBUG": return "bg-neutral-50 text-neutral-700 border border-neutral-200";
    case "INFO":  return "bg-sky-50 text-sky-700 border border-sky-200";
    case "WARN":  return "bg-amber-50 text-amber-700 border border-amber-200";
    case "ERROR": return "bg-red-50 text-red-700 border border-red-200";
    default:      return "bg-neutral-50 text-neutral-700 border border-neutral-200";
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
