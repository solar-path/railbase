import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";

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
          <p className="text-sm text-neutral-500">
            {total} job{total === 1 ? "" : "s"} total. Showing newest first.
            Use the <code className="rb-mono">railbase jobs</code> CLI for cancel /
            run-now / reset / recover.
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">status</span>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value as StatusFilter)}
            className="rounded border border-neutral-300 px-2 py-1"
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
          <span className="text-neutral-600">kind</span>
          <input
            type="text"
            value={kindInput}
            onChange={(e) => setKindInput(e.target.value)}
            placeholder="substring"
            className="rounded border border-neutral-300 px-2 py-1 w-56 rb-mono text-xs"
          />
        </label>
        {(status || kind) ? (
          <button
            type="button"
            onClick={() => {
              setStatus("");
              setKindInput("");
              setKind("");
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
              <th>created</th>
              <th>kind</th>
              <th>queue</th>
              <th>status</th>
              <th>attempts</th>
              <th>last error</th>
              <th>id</th>
            </tr>
          </thead>
          <tbody>
            {(q.data?.items ?? []).map((j) => {
              const isOpen = expandedId === j.id;
              return (
                <Fragment key={j.id}>
                  <tr
                    onClick={() => setExpandedId(isOpen ? null : j.id)}
                    className="cursor-pointer"
                  >
                    <td className="rb-mono text-xs text-neutral-500 whitespace-nowrap">
                      {j.created_at}
                    </td>
                    <td className="rb-mono">{j.kind}</td>
                    <td className="rb-mono text-xs text-neutral-600">{j.queue}</td>
                    <td>
                      <span className={"rounded px-1.5 py-0.5 text-xs " + statusColor(j.status)}>
                        {j.status}
                      </span>
                    </td>
                    <td className="rb-mono text-xs whitespace-nowrap">
                      {j.attempts}/{j.max_attempts}
                    </td>
                    <td className="max-w-md truncate text-xs text-red-700">
                      {j.last_error ? firstLine(j.last_error) : ""}
                    </td>
                    <td className="rb-mono text-xs" title={j.id}>
                      {j.id.slice(0, 8)}…
                    </td>
                  </tr>
                  {isOpen ? (
                    <tr>
                      <td colSpan={7} className="bg-neutral-50">
                        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 p-3 text-xs">
                          <dt className="text-neutral-500">id</dt>
                          <dd className="rb-mono">{j.id}</dd>
                          <dt className="text-neutral-500">queue</dt>
                          <dd className="rb-mono">{j.queue}</dd>
                          <dt className="text-neutral-500">run_after</dt>
                          <dd className="rb-mono">{j.run_after}</dd>
                          <dt className="text-neutral-500">started_at</dt>
                          <dd className="rb-mono">{j.started_at ?? "—"}</dd>
                          <dt className="text-neutral-500">completed_at</dt>
                          <dd className="rb-mono">{j.completed_at ?? "—"}</dd>
                          <dt className="text-neutral-500">locked_by</dt>
                          <dd className="rb-mono">{j.locked_by ?? "—"}</dd>
                          <dt className="text-neutral-500">locked_until</dt>
                          <dd className="rb-mono">{j.locked_until ?? "—"}</dd>
                          <dt className="text-neutral-500">cron_id</dt>
                          <dd className="rb-mono">{j.cron_id ?? "—"}</dd>
                          {j.last_error ? (
                            <>
                              <dt className="text-neutral-500 self-start">last_error</dt>
                              <dd>
                                <pre className="rb-mono text-xs text-red-700 whitespace-pre-wrap break-all m-0">
                                  {j.last_error}
                                </pre>
                              </dd>
                            </>
                          ) : null}
                        </dl>
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
            {q.data?.items.length === 0 ? (
              <tr>
                <td colSpan={7} className="text-neutral-400 text-center py-4">
                  No jobs.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// Status badge palette matches the convention from the brief:
// pending → neutral, running → sky, completed → emerald, failed → red,
// cancelled → amber. (The brief used "succeeded" in prose, but the
// backend enum is "completed" — we follow the wire.)
function statusColor(s: string): string {
  switch (s) {
    case "pending":   return "bg-neutral-50 text-neutral-700 border border-neutral-200";
    case "running":   return "bg-sky-50 text-sky-700 border border-sky-200";
    case "completed": return "bg-emerald-50 text-emerald-700 border border-emerald-200";
    case "failed":    return "bg-red-50 text-red-700 border border-red-200";
    case "cancelled": return "bg-amber-50 text-amber-700 border border-amber-200";
    default:          return "bg-neutral-50 text-neutral-700 border border-neutral-200";
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
