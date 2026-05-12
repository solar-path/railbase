import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";

// Audit log viewer — paginated, filterable list of `_audit_log` rows.
// Backend endpoint: GET /api/_admin/audit (v0.8 list, v1.7.11 filters).
//
// Filter parity with the docs/17 §Admin UI tests checklist: event,
// outcome, user_id, since/until, error_code. Hash-chain verify lives
// on the CLI (`railbase audit verify`); the UI surface is read-only.
//
// Debounce strategy mirrors logs.tsx / jobs.tsx — substring inputs
// (event, user_id, error_code) wait 300ms; the outcome <select> and
// since/until date inputs fire on change.

type OutcomeFilter = "" | "success" | "denied" | "failed" | "error";

export function AuditScreen() {
  const [page, setPage] = useState(1);
  const perPage = 50;

  const [eventInput, setEventInput] = useState("");
  const [event, setEvent] = useState(""); // debounced
  const [outcome, setOutcome] = useState<OutcomeFilter>("");
  const [userIdInput, setUserIdInput] = useState("");
  const [userId, setUserId] = useState(""); // debounced
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [errorCodeInput, setErrorCodeInput] = useState("");
  const [errorCode, setErrorCode] = useState(""); // debounced

  // Debounce substring inputs. 300ms matches logs / jobs.
  useEffect(() => {
    const t = setTimeout(() => setEvent(eventInput), 300);
    return () => clearTimeout(t);
  }, [eventInput]);
  useEffect(() => {
    const t = setTimeout(() => setUserId(userIdInput), 300);
    return () => clearTimeout(t);
  }, [userIdInput]);
  useEffect(() => {
    const t = setTimeout(() => setErrorCode(errorCodeInput), 300);
    return () => clearTimeout(t);
  }, [errorCodeInput]);

  // Reset to page 1 whenever any filter changes.
  useEffect(() => {
    setPage(1);
  }, [event, outcome, userId, since, until, errorCode]);

  const anyFilter =
    event || outcome || userId || since || until || errorCode;

  const q = useQuery({
    queryKey: [
      "audit",
      { page, perPage, event, outcome, user_id: userId, since, until, error_code: errorCode },
    ],
    queryFn: () =>
      adminAPI.audit({
        page,
        perPage,
        event: event || undefined,
        outcome: outcome || undefined,
        user_id: userId || undefined,
        since: toRFC3339OrUndefined(since),
        until: toRFC3339OrUndefined(until),
        error_code: errorCode || undefined,
      }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Audit log</h1>
          <p className="text-sm text-neutral-500">
            {total} event{total === 1 ? "" : "s"} total. Append-only chain — verify with{" "}
            <code className="rb-mono">railbase audit verify</code>.
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">event</span>
          <input
            type="text"
            value={eventInput}
            onChange={(e) => setEventInput(e.target.value)}
            placeholder="substring (e.g. auth.signin)"
            className="rounded border border-neutral-300 px-2 py-1 w-56 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">outcome</span>
          <select
            value={outcome}
            onChange={(e) => setOutcome(e.target.value as OutcomeFilter)}
            className="rounded border border-neutral-300 px-2 py-1"
          >
            <option value="">all</option>
            <option value="success">success</option>
            <option value="denied">denied</option>
            <option value="failed">failed</option>
            <option value="error">error</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">user_id</span>
          <input
            type="text"
            value={userIdInput}
            onChange={(e) => setUserIdInput(e.target.value)}
            placeholder="UUID exact match"
            className="rounded border border-neutral-300 px-2 py-1 w-64 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">since</span>
          <input
            type="datetime-local"
            value={since}
            onChange={(e) => setSince(e.target.value)}
            className="rounded border border-neutral-300 px-2 py-1 rb-mono text-xs"
            title="RFC3339 lower bound on the row's `at` column"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">until</span>
          <input
            type="datetime-local"
            value={until}
            onChange={(e) => setUntil(e.target.value)}
            className="rounded border border-neutral-300 px-2 py-1 rb-mono text-xs"
            title="RFC3339 upper bound on the row's `at` column"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">error_code</span>
          <input
            type="text"
            value={errorCodeInput}
            onChange={(e) => setErrorCodeInput(e.target.value)}
            placeholder="substring"
            className="rounded border border-neutral-300 px-2 py-1 w-48 rb-mono text-xs"
          />
        </label>
        {anyFilter ? (
          <button
            type="button"
            onClick={() => {
              setEventInput("");
              setEvent("");
              setOutcome("");
              setUserIdInput("");
              setUserId("");
              setSince("");
              setUntil("");
              setErrorCodeInput("");
              setErrorCode("");
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
              <th>seq</th>
              <th>at</th>
              <th>event</th>
              <th>outcome</th>
              <th>user</th>
              <th>ip</th>
              <th>error</th>
            </tr>
          </thead>
          <tbody>
            {(q.data?.items ?? []).map((e) => (
              <tr key={e.seq}>
                <td className="rb-mono text-neutral-500">{e.seq}</td>
                <td className="rb-mono text-xs text-neutral-500">{e.at}</td>
                <td className="rb-mono">{e.event}</td>
                <td>
                  <span className={"rounded px-1.5 py-0.5 text-xs " + outcomeColor(e.outcome)}>
                    {e.outcome}
                  </span>
                </td>
                <td className="rb-mono text-xs">
                  {e.user_id ? (
                    <span title={e.user_collection ?? ""}>{e.user_id.slice(0, 8)}…</span>
                  ) : (
                    "—"
                  )}
                </td>
                <td className="rb-mono text-xs">{e.ip ?? "—"}</td>
                <td className="rb-mono text-xs text-amber-700">{e.error_code ?? ""}</td>
              </tr>
            ))}
            {q.data?.items.length === 0 ? (
              <tr>
                <td colSpan={7} className="text-neutral-400 text-center py-4">
                  No events.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function outcomeColor(o: string): string {
  switch (o) {
    case "success": return "bg-emerald-50 text-emerald-700 border border-emerald-200";
    case "denied":  return "bg-amber-50 text-amber-700 border border-amber-200";
    case "failed":  return "bg-red-50 text-red-700 border border-red-200";
    case "error":   return "bg-red-50 text-red-700 border border-red-200";
    default:        return "bg-neutral-50 text-neutral-700 border border-neutral-200";
  }
}

// toRFC3339OrUndefined converts a <input type="datetime-local"> value
// ("YYYY-MM-DDTHH:MM") into a Z-suffixed RFC3339 string the backend
// time.Parse(time.RFC3339, _) can consume. We treat the local string
// as UTC for now — the audit `at` column is stored in UTC and the
// table renders UTC too; a future revision can prompt the user with
// their local zone.
function toRFC3339OrUndefined(local: string): string | undefined {
  if (!local) return undefined;
  // datetime-local emits "YYYY-MM-DDTHH:MM" (or with ":SS"). Append
  // ":00Z" / "Z" to make it RFC3339-conformant.
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(local)) {
    return local + ":00Z";
  }
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$/.test(local)) {
    return local + "Z";
  }
  // Already includes a timezone offset — pass through.
  return local;
}
