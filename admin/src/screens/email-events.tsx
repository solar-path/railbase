import { useEffect, useState } from "react";
import { adminAPI } from "../api/admin";
import type { EmailEvent } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// Email events browser — paginated, filterable list of `_email_events`
// rows (one row per recipient per send, populated by every
// mailer.Send call via internal/mailer.EventStore).
//
// Backend endpoint: GET /api/_admin/email-events (v1.7.35e+).
//
// Server-paginated via QDatatable's `fetch` mode — the table owns
// page/pageSize. Bespoke filters flow through `deps`; recipient is
// debounced ~300ms because every keystroke would otherwise fire a
// network round-trip on substring search.

type EventFilter = "" | "sent" | "failed" | "bounced" | "opened" | "clicked" | "complained";
type BounceTypeFilter = "" | "hard" | "soft" | "transient";

const columns: ColumnDef<EmailEvent>[] = [
  {
    id: "time",
    header: "time",
    accessor: "occurred_at",
    cell: (e) => (
      <span className="font-mono text-xs text-muted-foreground whitespace-nowrap">
        {e.occurred_at}
      </span>
    ),
  },
  {
    id: "event",
    header: "event",
    accessor: "event",
    cell: (e) => <Badge variant={eventVariant(e.event)}>{e.event}</Badge>,
  },
  {
    id: "recipient",
    header: "recipient",
    accessor: "recipient",
    cell: (e) => (
      <span className="font-mono text-xs block max-w-xs truncate" title={e.recipient}>
        {e.recipient}
      </span>
    ),
  },
  {
    id: "subject",
    header: "subject",
    accessor: "subject",
    cell: (e) => (
      <span className="block max-w-md truncate" title={e.subject ?? ""}>
        {e.subject || <span className="text-muted-foreground">—</span>}
      </span>
    ),
  },
  {
    id: "template",
    header: "template",
    accessor: "template",
    cell: (e) => (
      <span className="font-mono text-xs" title={e.template ?? ""}>
        {e.template || <span className="text-muted-foreground">—</span>}
      </span>
    ),
  },
  {
    id: "driver",
    header: "driver",
    accessor: "driver",
    cell: (e) => <span className="font-mono text-xs">{e.driver}</span>,
  },
  {
    id: "code",
    header: "code",
    accessor: "error_code",
    cell: (e) => (
      <span className="font-mono text-xs" title={e.error_code ?? ""}>
        {e.error_code || <span className="text-muted-foreground">—</span>}
      </span>
    ),
  },
];

export function EmailEventsScreen() {
  const [total, setTotal] = useState(0);

  const [recipientInput, setRecipientInput] = useState("");
  const [recipient, setRecipient] = useState(""); // debounced
  const [event, setEvent] = useState<EventFilter>("");
  const [template, setTemplate] = useState("");
  const [bounceType, setBounceType] = useState<BounceTypeFilter>("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");

  // Debounce the recipient input. 300ms feels snappy without hammering.
  useEffect(() => {
    const t = setTimeout(() => {
      setRecipient(recipientInput);
    }, 300);
    return () => clearTimeout(t);
  }, [recipientInput]);

  const hasFilter = !!(recipient || event || template || bounceType || since || until);

  return (
    <AdminPage>
      <AdminPage.Header
        title="Email events"
        description={
          <>
            {total} event{total === 1 ? "" : "s"} total. Showing newest first.
            One row per recipient per <code className="font-mono">mailer.Send</code> call.
          </>
        }
      />

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">recipient</span>
          <Input
            type="text"
            value={recipientInput}
            onInput={(e) => setRecipientInput(e.currentTarget.value)}
            placeholder="alice@example.com"
            className="w-56 h-8 font-mono text-xs"
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
            className="w-48 h-8 font-mono text-xs"
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
      </AdminPage.Toolbar>

      <AdminPage.Body>
        <QDatatable
          columns={columns}
          rowKey="id"
          pageSize={50}
          emptyMessage="No email events."
          deps={[recipient, event, template, bounceType, since, until]}
          fetch={async (params) => {
            const r = await adminAPI.listEmailEvents({
              page: params.page,
              perPage: params.pageSize,
              recipient: recipient || undefined,
              event: event || undefined,
              template: template || undefined,
              bounce_type: bounceType || undefined,
              since: since ? localToRFC3339(since) : undefined,
              until: until ? localToRFC3339(until) : undefined,
            });
            setTotal(r.totalItems);
            return { rows: r.items, total: r.totalItems };
          }}
        />
      </AdminPage.Body>
    </AdminPage>
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
