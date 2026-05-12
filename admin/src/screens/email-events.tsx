import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { EmailEvent } from "../api/types";
import { Pager } from "../layout/pager";

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
          <p className="text-sm text-neutral-500">
            {total} event{total === 1 ? "" : "s"} total. Showing newest first.
            One row per recipient per <code className="rb-mono">mailer.Send</code> call.
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">recipient</span>
          <input
            type="text"
            value={recipientInput}
            onChange={(e) => setRecipientInput(e.target.value)}
            placeholder="alice@example.com"
            className="rounded border border-neutral-300 px-2 py-1 w-56 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">event</span>
          <select
            value={event}
            onChange={(e) => setEvent(e.target.value as EventFilter)}
            className="rounded border border-neutral-300 px-2 py-1"
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
          <span className="text-neutral-600">template</span>
          <input
            type="text"
            value={template}
            onChange={(e) => setTemplate(e.target.value)}
            placeholder="invite_received"
            className="rounded border border-neutral-300 px-2 py-1 w-48 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">bounce_type</span>
          <select
            value={bounceType}
            onChange={(e) => setBounceType(e.target.value as BounceTypeFilter)}
            className="rounded border border-neutral-300 px-2 py-1"
          >
            <option value="">all</option>
            <option value="hard">hard</option>
            <option value="soft">soft</option>
            <option value="transient">transient</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">since</span>
          <input
            type="datetime-local"
            value={since}
            onChange={(e) => setSince(e.target.value)}
            className="rounded border border-neutral-300 px-2 py-1 text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">until</span>
          <input
            type="datetime-local"
            value={until}
            onChange={(e) => setUntil(e.target.value)}
            className="rounded border border-neutral-300 px-2 py-1 text-xs"
          />
        </label>
        {hasFilter ? (
          <button
            type="button"
            onClick={() => {
              setRecipientInput("");
              setRecipient("");
              setEvent("");
              setTemplate("");
              setBounceType("");
              setSince("");
              setUntil("");
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
              <th>time</th>
              <th>event</th>
              <th>recipient</th>
              <th>subject</th>
              <th>template</th>
              <th>driver</th>
              <th>code</th>
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
                      {e.occurred_at}
                    </td>
                    <td>
                      <span className={"rounded px-1.5 py-0.5 text-xs " + eventColor(e.event)}>
                        {e.event}
                      </span>
                    </td>
                    <td className="rb-mono text-xs max-w-xs truncate" title={e.recipient}>
                      {e.recipient}
                    </td>
                    <td className="max-w-md truncate" title={e.subject ?? ""}>
                      {e.subject || <span className="text-neutral-400">—</span>}
                    </td>
                    <td className="rb-mono text-xs" title={e.template ?? ""}>
                      {e.template || <span className="text-neutral-400">—</span>}
                    </td>
                    <td className="rb-mono text-xs">{e.driver}</td>
                    <td className="rb-mono text-xs" title={e.error_code ?? ""}>
                      {e.error_code || <span className="text-neutral-400">—</span>}
                    </td>
                  </tr>
                  {isOpen ? (
                    <tr>
                      <td colSpan={7} className="bg-neutral-50">
                        <ExpandedRow event={e} />
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
            {q.data?.items.length === 0 ? (
              <tr>
                <td colSpan={7} className="text-neutral-400 text-center py-4">
                  No email events.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
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

// eventColor pairs each event type with a Tailwind pill class. Sent
// reads as a "happy" green; failed / bounced / complained are warn /
// red / amber to surface in operator scans; opened / clicked are
// neutral-info because they're informational, not actionable.
function eventColor(ev: string): string {
  switch (ev) {
    case "sent":
      return "bg-emerald-50 text-emerald-700 border border-emerald-200";
    case "failed":
      return "bg-red-50 text-red-700 border border-red-200";
    case "bounced":
      return "bg-amber-50 text-amber-700 border border-amber-200";
    case "complained":
      return "bg-rose-50 text-rose-700 border border-rose-200";
    case "opened":
      return "bg-sky-50 text-sky-700 border border-sky-200";
    case "clicked":
      return "bg-indigo-50 text-indigo-700 border border-indigo-200";
    default:
      return "bg-neutral-50 text-neutral-700 border border-neutral-200";
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
          <div className="text-neutral-500">metadata</div>
          <pre className="rb-mono text-xs text-neutral-700 whitespace-pre-wrap break-all rounded bg-white border border-neutral-200 p-2 mt-1">
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
      <span className="text-neutral-500 w-32 shrink-0">{label}</span>
      <span className={"text-neutral-800 break-all " + (mono ? "rb-mono" : "")}>{value}</span>
    </div>
  );
}
