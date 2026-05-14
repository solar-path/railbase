import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { NotificationRecord } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// Notifications panel — cross-user log of every persisted notification.
// Rendered as the "Notifications" category of the unified Logs screen
// (logs.tsx); it returns a stats banner + AdminPage.Toolbar + .Body
// fragments, not a full AdminPage shell.
//
// Backend endpoint: GET /api/_admin/notifications (v1.7.10+).
//
// Distinct from the user-facing /api/notifications surface — admins
// can see what every user got delivered, filtered by kind / channel /
// user_id / unread. Read-only: marking read / deleting is a user
// affordance and stays on the user side (no admin mutation surface
// for v1).
//
// The list table is server-paginated via QDatatable's `fetch` mode;
// bespoke filters flow through `deps`. The stats banner stays on its
// own `useQuery` — it reflects global state, not the filter view.

type ChannelFilter = "" | "inapp" | "email" | "push";

const columns: ColumnDef<NotificationRecord>[] = [
  {
    id: "created",
    header: "created",
    accessor: "created_at",
    cell: (n) => (
      <span className="font-mono text-xs text-muted-foreground whitespace-nowrap">
        {n.created_at}
      </span>
    ),
  },
  {
    id: "kind",
    header: "kind",
    accessor: "kind",
    cell: (n) => <span className="font-mono">{n.kind}</span>,
  },
  {
    id: "channel",
    header: "channel",
    accessor: "channel",
    cell: (n) => (
      <Badge variant="outline" className={channelBadgeClass(n.channel)}>
        {n.channel}
      </Badge>
    ),
  },
  {
    id: "title",
    header: "title",
    accessor: "title",
    cell: (n) => <span className="block max-w-md truncate">{n.title}</span>,
  },
  {
    id: "user",
    header: "user",
    accessor: "user_id",
    cell: (n) => (
      <span className="font-mono text-xs" title={n.user_id}>
        {n.user_id.slice(0, 8)}…
      </span>
    ),
  },
  {
    id: "read",
    header: "read",
    cell: (n) =>
      n.read_at ? (
        <span className="text-xs text-muted-foreground">read</span>
      ) : (
        <span className="inline-flex items-center gap-1 text-xs text-primary">
          <span className="inline-block w-2 h-2 rounded-full bg-primary" />
          unread
        </span>
      ),
  },
];

export function NotificationsPanel() {
  const [kindInput, setKindInput] = useState("");
  const [kind, setKind] = useState(""); // debounced
  const [channel, setChannel] = useState<ChannelFilter>("");
  const [userIdInput, setUserIdInput] = useState("");
  const [userId, setUserId] = useState(""); // debounced
  const [unreadOnly, setUnreadOnly] = useState(false);

  // Debounce text inputs. 300ms matches the logs / jobs viewers.
  useEffect(() => {
    const t = setTimeout(() => setKind(kindInput), 300);
    return () => clearTimeout(t);
  }, [kindInput]);
  useEffect(() => {
    const t = setTimeout(() => setUserId(userIdInput), 300);
    return () => clearTimeout(t);
  }, [userIdInput]);

  // Stats endpoint feeds the header banner. Loaded once; refetched
  // when the user clicks the page-1 affordance via react-query's
  // default focus-revalidate. No filter coupling — the banner reflects
  // the global state, not the filter view.
  const statsQ = useQuery({
    queryKey: ["notifications-stats"],
    queryFn: () => adminAPI.notificationsStats(),
    staleTime: 30_000,
  });

  const stats = statsQ.data;
  // Top 3 kinds by count + top 2 channels for the pill row. Sort
  // happens server-side for kinds via `count(*) DESC, kind ASC` —
  // here we just slice. Channels are an unordered map; we sort by
  // value DESC.
  const topKinds = stats
    ? Object.entries(stats.by_kind)
        .sort((a, b) => b[1] - a[1])
        .slice(0, 3)
    : [];
  const topChannels = stats
    ? Object.entries(stats.by_channel)
        .sort((a, b) => b[1] - a[1])
        .slice(0, 2)
    : [];

  return (
    <>
      <p className="text-sm text-muted-foreground">
        {stats ? `${stats.total} delivered (${stats.unread} unread). ` : ""}
        Cross-user log of persisted notifications. Showing newest first.
      </p>

      {stats ? (
        <div className="flex gap-2 items-baseline flex-wrap">
          {topKinds.map(([k, n]) => (
            <Badge
              key={"kind-" + k}
              variant="secondary"
              title={`${n} notifications with kind=${k}`}
            >
              <span className="font-mono">{k}</span>
              <span className="text-muted-foreground"> · {n}</span>
            </Badge>
          ))}
          {topChannels.map(([c, n]) => (
            <Badge
              key={"channel-" + c}
              variant="secondary"
              title={`${n} notifications delivered via ${c}`}
            >
              {c}
              <span className="text-muted-foreground"> · {n}</span>
            </Badge>
          ))}
          {topKinds.length === 0 && topChannels.length === 0 ? (
            <span className="text-xs text-muted-foreground">No deliveries yet.</span>
          ) : null}
        </div>
      ) : null}

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">kind</span>
          <Input
            type="text"
            value={kindInput}
            onInput={(e) => setKindInput(e.currentTarget.value)}
            placeholder="exact match"
            className="w-56 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">channel</span>
          <select
            value={channel}
            onChange={(e) => setChannel(e.currentTarget.value as ChannelFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">all</option>
            <option value="inapp">inapp</option>
            <option value="email">email</option>
            <option value="push">push</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">user_id</span>
          <Input
            type="text"
            value={userIdInput}
            onInput={(e) => setUserIdInput(e.currentTarget.value)}
            placeholder="UUID"
            className="w-64 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <Checkbox
            checked={unreadOnly}
            onCheckedChange={(c) => setUnreadOnly(c === true)}
          />
          <span className="text-muted-foreground">unread only</span>
        </label>
        {(kind || channel || userId || unreadOnly) ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setKindInput("");
              setKind("");
              setChannel("");
              setUserIdInput("");
              setUserId("");
              setUnreadOnly(false);
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
          emptyMessage="No notifications."
          deps={[kind, channel, userId, unreadOnly]}
          fetch={async (params) => {
            const r = await adminAPI.notificationsList({
              page: params.page,
              perPage: params.pageSize,
              kind: kind || undefined,
              channel: channel || undefined,
              user_id: userId || undefined,
              unread_only: unreadOnly || undefined,
            });
            return { rows: r.items, total: r.totalItems };
          }}
        />
      </AdminPage.Body>
    </>
  );
}

// Channel badge palette per spec: inapp → neutral, email → sky,
// push → emerald.
function channelBadgeClass(c: string): string {
  switch (c) {
    case "inapp": return "border-input bg-muted text-foreground";
    case "email": return "border-primary/40 bg-primary/10 text-primary";
    case "push":  return "border-primary/40 bg-primary/10 text-primary";
    default:      return "border-input bg-muted text-foreground";
  }
}
