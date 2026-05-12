import { useEffect, useState } from "react";
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
          <p className="text-sm text-muted-foreground">
            {total} event{total === 1 ? "" : "s"} total. Append-only chain — verify with{" "}
            <code className="rb-mono">railbase audit verify</code>.
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">event</span>
          <Input
            type="text"
            value={eventInput}
            onInput={(e) => setEventInput(e.currentTarget.value)}
            placeholder="substring (e.g. auth.signin)"
            className="w-56 h-8 rb-mono text-xs"
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
            className="w-64 h-8 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">since</span>
          <Input
            type="datetime-local"
            value={since}
            onInput={(e) => setSince(e.currentTarget.value)}
            className="h-8 rb-mono text-xs w-auto"
            title="RFC3339 lower bound on the row's `at` column"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">until</span>
          <Input
            type="datetime-local"
            value={until}
            onInput={(e) => setUntil(e.currentTarget.value)}
            className="h-8 rb-mono text-xs w-auto"
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
            className="w-48 h-8 rb-mono text-xs"
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
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>seq</TableHead>
                <TableHead>at</TableHead>
                <TableHead>event</TableHead>
                <TableHead>outcome</TableHead>
                <TableHead>user</TableHead>
                <TableHead>ip</TableHead>
                <TableHead>error</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(q.data?.items ?? []).map((e) => (
                <TableRow key={e.seq}>
                  <TableCell className="rb-mono text-muted-foreground">{e.seq}</TableCell>
                  <TableCell className="rb-mono text-xs text-muted-foreground">{e.at}</TableCell>
                  <TableCell className="rb-mono">{e.event}</TableCell>
                  <TableCell>
                    <Badge variant={outcomeVariant(e.outcome)}>{e.outcome}</Badge>
                  </TableCell>
                  <TableCell className="rb-mono text-xs">
                    {e.user_id ? (
                      <span title={e.user_collection ?? ""}>{e.user_id.slice(0, 8)}…</span>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell className="rb-mono text-xs">{e.ip ?? "—"}</TableCell>
                  <TableCell className="rb-mono text-xs text-amber-700">{e.error_code ?? ""}</TableCell>
                </TableRow>
              ))}
              {q.data?.items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="text-muted-foreground text-center py-4">
                    No events.
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
