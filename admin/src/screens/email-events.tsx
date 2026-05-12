import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { EmailEvent } from "../api/types";
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

// Email events browser — paginated, filterable list of `_email_events`
// rows (one row per recipient per send, populated by every
// mailer.Send call via internal/mailer.EventStore).
//
// Backend endpoint: GET /api/_admin/email-events (v1.7.35e+).
//
// Filter bar shape mirrors logs.tsx: every input updates the query key
// directly; changing any filter resets the page to 1. Recipient is
// debounced ~300ms because every keystroke would otherwise fire a
// network round-trip on substring search.

type EventFilter = "" | "sent" | "failed" | "bounced" | "opened" | "clicked" | "complained";
type BounceTypeFilter = "" | "hard" | "soft" | "transient";

export function EmailEventsScreen() {
  const [page, setPage] = useState(1);
  const perPage = 50;

  const [recipientInput, setRecipientInput] = useState("");
  const [recipient, setRecipient] = useState(""); // debounced
  const [event, setEvent] = useState<EventFilter>("");
  const [template, setTemplate] = useState("");
  const [bounceType, setBounceType] = useState<BounceTypeFilter>("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // Debounce the recipient input. 300ms feels snappy without hammering.
  useEffect(() => {
    const t = setTimeout(() => {
      setRecipient(recipientInput);
    }, 300);
    return () => clearTimeout(t);
  }, [recipientInput]);

  // Reset to page 1 whenever any filter changes. The debounced
  // `recipient` lives on its own clock — track that one too so the
  // first character of a substring search snaps back to page 1.
  useEffect(() => {
    setPage(1);
  }, [recipient, event, template, bounceType, since, until]);

  const q = useQuery({
    queryKey: [
      "email-events",
      { page, perPage, recipient, event, template, bounce_type: bounceType, since, until },
    ],
    queryFn: () =>
      adminAPI.listEmailEvents({
        page,
        perPage,
        recipient: recipient || undefined,
        event: event || undefined,
        template: template || undefined,
        bounce_type: bounceType || undefined,
        since: since ? localToRFC3339(since) : undefined,
        until: until ? localToRFC3339(until) : undefined,
      }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, q.data?.totalPages ?? Math.ceil(total / perPage));
  const hasFilter = !!(recipient || event || template || bounceType || since || until);

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Email events</h1>
          <p className="text-sm text-muted-foreground">
            {total} event{total === 1 ? "" : "s"} total. Showing newest first.
            One row per recipient per <code className="rb-mono">mailer.Send</code> call.
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">recipient</span>
          <Input
            type="text"
            value={recipientInput}
            onInput={(e) => setRecipientInput(e.currentTarget.value)}
            placeholder="alice@example.com"
            className="w-56 h-8 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">event</span>
          <select
            value={event}
            onChange={(e) => setEvent(e.currentTarget.value as EventFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">all</option>
            <option value="sent">sent</option>
            <option value="failed">failed</option>
            <option value="bounced">bounced</option>
            <option value="opened">opened</option>
            <option value="clicked">clicked</option>
            <option value="complained">complained</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">template</span>
          <Input
            type="text"
            value={template}
            onInput={(e) => setTemplate(e.currentTarget.value)}
            placeholder="invite_received"
            className="w-48 h-8 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">bounce_type</span>
          <select
            value={bounceType}
            onChange={(e) => setBounceType(e.currentTarget.value as BounceTypeFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">all</option>
            <option value="hard">hard</option>
            <option value="soft">soft</option>
            <option value="transient">transient</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">since</span>
          <Input
            type="datetime-local"
            value={since}
            onInput={(e) => setSince(e.currentTarget.value)}
            className="h-8 text-xs w-auto"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">until</span>
          <Input
            type="datetime-local"
            value={until}
            onInput={(e) => setUntil(e.currentTarget.value)}
            className="h-8 text-xs w-auto"
          />
        </label>
        {hasFilter ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setRecipientInput("");
              setRecipient("");
              setEvent("");
              setTemplate("");
              setBounceType("");
              setSince("");
              setUntil("");
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
                <TableHead>time</TableHead>
                <TableHead>event</TableHead>
                <TableHead>recipient</TableHead>
                <TableHead>subject</TableHead>
                <TableHead>template</TableHead>
                <TableHead>driver</TableHead>
                <TableHead>code</TableHead>
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
                        {e.occurred_at}
                      </TableCell>
                      <TableCell>
                        <Badge variant={eventVariant(e.event)}>{e.event}</Badge>
                      </TableCell>
                      <TableCell className="rb-mono text-xs max-w-xs truncate" title={e.recipient}>
                        {e.recipient}
                      </TableCell>
                      <TableCell className="max-w-md truncate" title={e.subject ?? ""}>
                        {e.subject || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                      <TableCell className="rb-mono text-xs" title={e.template ?? ""}>
                        {e.template || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                      <TableCell className="rb-mono text-xs">{e.driver}</TableCell>
                      <TableCell className="rb-mono text-xs" title={e.error_code ?? ""}>
                        {e.error_code || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                    </TableRow>
                    {isOpen ? (
                      <TableRow>
                        <TableCell colSpan={7} className="bg-muted">
                          <ExpandedRow event={e} />
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
              {q.data?.items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="text-muted-foreground text-center py-4">
                    No email events.
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

// localToRFC3339 reshapes the HTML datetime-local value ("2026-05-12T14:30")
// into the RFC3339 wire format the backend expects ("2026-05-12T14:30:00Z").
// The naive form is interpreted as the user's local clock, then converted
// to UTC by the Date constructor — same behaviour the audit screen uses.
function localToRFC3339(local: string): string {
  if (!local) return "";
  const d = new Date(local);
  if (Number.isNaN(d.getTime())) return "";
  return d.toISOString();
}

// eventVariant maps an email event to the closest Badge variant. The
// kit's badge palette is small (default/secondary/destructive/outline)
// so we approximate: sent → default (positive primary), failed →
// destructive, bounced / complained → destructive (they read as bad
// outcomes for the operator), opened / clicked → secondary
// (informational), anything else → outline.
function eventVariant(ev: string): "default" | "secondary" | "destructive" | "outline" {
  switch (ev) {
    case "sent":       return "default";
    case "failed":     return "destructive";
    case "bounced":    return "destructive";
    case "complained": return "destructive";
    case "opened":     return "secondary";
    case "clicked":    return "secondary";
    default:           return "outline";
  }
}

// ExpandedRow renders the full event payload below the table row. It
// surfaces error_message + metadata which are too long for the table
// itself; metadata is pretty-printed JSON so the operator can spot the
// MTA / plugin extras at a glance.
function ExpandedRow({ event }: { event: EmailEvent }) {
  return (
    <div className="p-3 space-y-2 text-xs">
      <Field label="id" value={event.id} mono />
      <Field label="message_id" value={event.message_id || "—"} mono />
      {event.error_message ? (
        <Field label="error_message" value={event.error_message} />
      ) : null}
      {event.bounce_type ? (
        <Field label="bounce_type" value={event.bounce_type} mono />
      ) : null}
      {event.metadata && Object.keys(event.metadata).length > 0 ? (
        <div>
          <div className="text-muted-foreground">metadata</div>
          <pre className="rb-mono text-xs text-foreground whitespace-pre-wrap break-all rounded bg-background border border-input p-2 mt-1">
            {JSON.stringify(event.metadata, null, 2)}
          </pre>
        </div>
      ) : null}
    </div>
  );
}

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <span className="text-muted-foreground w-32 shrink-0">{label}</span>
      <span className={"text-foreground break-all " + (mono ? "rb-mono" : "")}>{value}</span>
    </div>
  );
}
