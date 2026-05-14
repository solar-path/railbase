import { useEffect, useState } from "react";
import { adminAPI } from "../api/admin";
import type { AuditEvent } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import { Shield, Download } from "@/lib/ui/icons";

// Audit log viewer — paginated, filterable list of `_audit_log` rows.
// Backend endpoint: GET /api/_admin/audit (v0.8 list, v1.7.11 filters).
//
// Filter parity with the docs/17 §Admin UI tests checklist: event,
// outcome, user_id, since/until, error_code. Hash-chain verify lives
// on the CLI (`railbase audit verify`); the UI surface is read-only.
//
// Debounce strategy mirrors logs.tsx / jobs.tsx — substring inputs
// (event, user_id, error_code) wait 300ms; the outcome <select> and
// since/until date inputs fire on change. Server-paginated via
// QDatatable's `fetch` mode — the table owns page/pageSize; bespoke
// filters flow through `deps`.

type OutcomeFilter = "" | "success" | "denied" | "failed" | "error";

const columns: ColumnDef<AuditEvent>[] = [
  {
    id: "seq",
    header: "seq",
    accessor: "seq",
    cell: (e) => <span className="font-mono text-muted-foreground">{e.seq}</span>,
  },
  {
    id: "at",
    header: "at",
    accessor: "at",
    cell: (e) => (
      <span className="font-mono text-xs text-muted-foreground">{e.at}</span>
    ),
  },
  {
    id: "event",
    header: "event",
    accessor: "event",
    cell: (e) => <span className="font-mono">{e.event}</span>,
  },
  {
    id: "outcome",
    header: "outcome",
    accessor: "outcome",
    cell: (e) => <Badge variant={outcomeVariant(e.outcome)}>{e.outcome}</Badge>,
  },
  {
    id: "user",
    header: "user",
    accessor: "user_id",
    cell: (e) => (
      <span className="font-mono text-xs">
        {e.user_id ? (
          <span title={e.user_collection ?? ""}>{e.user_id.slice(0, 8)}…</span>
        ) : (
          "—"
        )}
      </span>
    ),
  },
  {
    id: "ip",
    header: "ip",
    accessor: "ip",
    cell: (e) => <span className="font-mono text-xs">{e.ip ?? "—"}</span>,
  },
  {
    id: "error",
    header: "error",
    accessor: "error_code",
    cell: (e) => (
      <span className="font-mono text-xs text-foreground">{e.error_code ?? ""}</span>
    ),
  },
];

export function AuditScreen() {
  const [total, setTotal] = useState(0);

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

  const anyFilter =
    event || outcome || userId || since || until || errorCode;

  return (
    <AdminPage>
      <AdminPage.Header
        title={
          <span className="inline-flex items-center gap-2">
            <Shield className="h-5 w-5 text-primary" />
            Audit log
            <Badge variant="outline" className="font-mono text-[10px] uppercase tracking-wide">
              compliance
            </Badge>
          </span>
        }
        description={
          <>
            {total} event{total === 1 ? "" : "s"} total. Append-only hash chain —
            verify integrity with <code className="font-mono">railbase audit verify</code>.
            Every system_admin action is recorded here for forensic review.
          </>
        }
      />

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">event</span>
          <Input
            type="text"
            value={eventInput}
            onInput={(e) => setEventInput(e.currentTarget.value)}
            placeholder="substring (e.g. auth.signin)"
            className="w-56 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">outcome</span>
          <select
            value={outcome}
            onChange={(e) => setOutcome(e.currentTarget.value as OutcomeFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">all</option>
            <option value="success">success</option>
            <option value="denied">denied</option>
            <option value="failed">failed</option>
            <option value="error">error</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">user_id</span>
          <Input
            type="text"
            value={userIdInput}
            onInput={(e) => setUserIdInput(e.currentTarget.value)}
            placeholder="UUID exact match"
            className="w-64 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">since</span>
          <Input
            type="datetime-local"
            value={since}
            onInput={(e) => setSince(e.currentTarget.value)}
            className="h-8 font-mono text-xs w-auto"
            title="RFC3339 lower bound on the row's `at` column"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">until</span>
          <Input
            type="datetime-local"
            value={until}
            onInput={(e) => setUntil(e.currentTarget.value)}
            className="h-8 font-mono text-xs w-auto"
            title="RFC3339 upper bound on the row's `at` column"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">error_code</span>
          <Input
            type="text"
            value={errorCodeInput}
            onInput={(e) => setErrorCodeInput(e.currentTarget.value)}
            placeholder="substring"
            className="w-48 h-8 font-mono text-xs"
          />
        </label>
        {anyFilter ? (
          <Button
            variant="outline"
            size="sm"
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
          >
            clear
          </Button>
        ) : null}
        {/* v1.7.x §3.15 Block A — XLSX export with the same filter
            vocabulary. We rely on the admin session cookie riding
            with window.location so the browser handles the download
            natively (no fetch + blob + revokeObjectURL dance). */}
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            const params = new URLSearchParams();
            if (event) params.set("event", event);
            if (outcome) params.set("outcome", outcome);
            if (userId) params.set("user_id", userId);
            const sinceRFC = toRFC3339OrUndefined(since);
            if (sinceRFC) params.set("since", sinceRFC);
            const untilRFC = toRFC3339OrUndefined(until);
            if (untilRFC) params.set("until", untilRFC);
            if (errorCode) params.set("error_code", errorCode);
            const qs = params.toString();
            const url = "/api/_admin/audit/export.xlsx" + (qs ? "?" + qs : "");
            window.location.href = url;
          }}
          title="Export the current filter slice as XLSX (cap 100k rows)"
        >
          <Download className="h-3.5 w-3.5 mr-1" />
          Export XLSX
        </Button>
      </AdminPage.Toolbar>

      <AdminPage.Body>
        <QDatatable
          columns={columns}
          rowKey="seq"
          pageSize={50}
          emptyMessage="No events."
          deps={[event, outcome, userId, since, until, errorCode]}
          fetch={async (params) => {
            const r = await adminAPI.audit({
              page: params.page,
              perPage: params.pageSize,
              event: event || undefined,
              outcome: outcome || undefined,
              user_id: userId || undefined,
              since: toRFC3339OrUndefined(since),
              until: toRFC3339OrUndefined(until),
              error_code: errorCode || undefined,
            });
            setTotal(r.totalItems);
            return { rows: r.items, total: r.totalItems };
          }}
        />
      </AdminPage.Body>
    </AdminPage>
  );
}

// outcomeVariant maps audit outcomes onto Badge variants. success →
// default (primary positive), denied → secondary (informational
// warning), failed / error → destructive.
function outcomeVariant(o: string): "default" | "secondary" | "destructive" | "outline" {
  switch (o) {
    case "success": return "default";
    case "denied":  return "secondary";
    case "failed":  return "destructive";
    case "error":   return "destructive";
    default:        return "outline";
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
