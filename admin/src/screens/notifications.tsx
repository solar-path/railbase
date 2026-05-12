import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

// Notifications browser — cross-user log of every persisted notification.
// Backend endpoint: GET /api/_admin/notifications (v1.7.10+).
//
// Distinct from the user-facing /api/notifications surface — admins
// can see what every user got delivered, filtered by kind / channel /
// user_id / unread. Read-only: marking read / deleting is a user
// affordance and stays on the user side (no admin mutation surface
// for v1).

type ChannelFilter = "" | "inapp" | "email" | "push";

export function NotificationsScreen() {
  const [page, setPage] = useState(1);
  const perPage = 50;

  const [kindInput, setKindInput] = useState("");
  const [kind, setKind] = useState(""); // debounced
  const [channel, setChannel] = useState<ChannelFilter>("");
  const [userIdInput, setUserIdInput] = useState("");
  const [userId, setUserId] = useState(""); // debounced
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // Debounce text inputs. 300ms matches the logs / jobs viewers.
  useEffect(() => {
    const t = setTimeout(() => setKind(kindInput), 300);
    return () => clearTimeout(t);
  }, [kindInput]);
  useEffect(() => {
    const t = setTimeout(() => setUserId(userIdInput), 300);
    return () => clearTimeout(t);
  }, [userIdInput]);

  // Reset to page 1 when any filter changes.
  useEffect(() => {
    setPage(1);
  }, [kind, channel, userId, unreadOnly]);

  const listQ = useQuery({
    queryKey: ["notifications", { page, perPage, kind, channel, userId, unreadOnly }],
    queryFn: () =>
      adminAPI.notificationsList({
        page,
        perPage,
        kind: kind || undefined,
        channel: channel || undefined,
        user_id: userId || undefined,
        unread_only: unreadOnly || undefined,
      }),
  });

  // Stats endpoint feeds the header banner. Loaded once; refetched
  // when the user clicks the page-1 affordance via react-query's
  // default focus-revalidate. No filter coupling — the banner reflects
  // the global state, not the filter view.
  const statsQ = useQuery({
    queryKey: ["notifications-stats"],
    queryFn: () => adminAPI.notificationsStats(),
    staleTime: 30_000,
  });

  const total = listQ.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

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
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Notifications</h1>
          <p className="text-sm text-muted-foreground">
            {stats
              ? `${stats.total} delivered (${stats.unread} unread). `
              : ""}
            Cross-user log of persisted notifications. Showing newest first.
          </p>
        </div>
        <Pager page={page} totalPages={totalPages} onChange={setPage} />
      </header>

      {stats ? (
        <div className="flex gap-2 items-baseline flex-wrap">
          {topKinds.map(([k, n]) => (
            <Badge
              key={"kind-" + k}
              variant="secondary"
              title={`${n} notifications with kind=${k}`}
            >
              <span className="rb-mono">{k}</span>
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

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">kind</span>
          <Input
            type="text"
            value={kindInput}
            onInput={(e) => setKindInput(e.currentTarget.value)}
            placeholder="exact match"
            className="w-56 h-8 rb-mono text-xs"
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
            className="w-64 h-8 rb-mono text-xs"
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
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>created</TableHead>
                <TableHead>kind</TableHead>
                <TableHead>channel</TableHead>
                <TableHead>title</TableHead>
                <TableHead>user</TableHead>
                <TableHead>read</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(listQ.data?.items ?? []).map((n) => {
                const isOpen = expandedId === n.id;
                return (
                  <Fragment key={n.id}>
                    <TableRow
                      onClick={() => setExpandedId(isOpen ? null : n.id)}
                      className="cursor-pointer"
                    >
                      <TableCell className="rb-mono text-xs text-muted-foreground whitespace-nowrap">
                        {n.created_at}
                      </TableCell>
                      <TableCell className="rb-mono">{n.kind}</TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={channelBadgeClass(n.channel)}
                        >
                          {n.channel}
                        </Badge>
                      </TableCell>
                      <TableCell className="max-w-md truncate">{n.title}</TableCell>
                      <TableCell className="rb-mono text-xs" title={n.user_id}>
                        {n.user_id.slice(0, 8)}…
                      </TableCell>
                      <TableCell>
                        {n.read_at ? (
                          <span className="text-xs text-muted-foreground">read</span>
                        ) : (
                          <span className="inline-flex items-center gap-1 text-xs text-emerald-700">
                            <span className="inline-block w-2 h-2 rounded-full bg-emerald-500" />
                            unread
                          </span>
                        )}
                      </TableCell>
                    </TableRow>
                    {isOpen ? (
                      <TableRow>
                        <TableCell colSpan={6} className="bg-muted">
                          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 p-3 text-xs">
                            <dt className="text-muted-foreground">id</dt>
                            <dd className="rb-mono">{n.id}</dd>
                            <dt className="text-muted-foreground">user_id</dt>
                            <dd className="rb-mono">{n.user_id}</dd>
                            <dt className="text-muted-foreground">tenant_id</dt>
                            <dd className="rb-mono">{n.tenant_id ?? "—"}</dd>
                            <dt className="text-muted-foreground">priority</dt>
                            <dd className="rb-mono">{n.priority}</dd>
                            <dt className="text-muted-foreground">read_at</dt>
                            <dd className="rb-mono">{n.read_at ?? "—"}</dd>
                            <dt className="text-muted-foreground">expires_at</dt>
                            <dd className="rb-mono">{n.expires_at ?? "—"}</dd>
                            {n.body ? (
                              <>
                                <dt className="text-muted-foreground self-start">body</dt>
                                <dd>
                                  <pre className="rb-mono text-xs text-foreground whitespace-pre-wrap break-all m-0">
                                    {n.body}
                                  </pre>
                                </dd>
                              </>
                            ) : null}
                            <dt className="text-muted-foreground self-start">payload</dt>
                            <dd>
                              <pre className="rb-mono text-xs text-foreground whitespace-pre-wrap break-all m-0">
                                {JSON.stringify(n.payload ?? {}, null, 2)}
                              </pre>
                            </dd>
                          </dl>
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
              {listQ.data?.items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="text-muted-foreground text-center py-4">
                    No notifications.
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

// Channel badge palette per spec: inapp → neutral, email → sky,
// push → emerald.
function channelBadgeClass(c: string): string {
  switch (c) {
    case "inapp": return "border-input bg-muted text-foreground";
    case "email": return "border-sky-200 bg-sky-50 text-sky-700";
    case "push":  return "border-emerald-200 bg-emerald-50 text-emerald-700";
    default:      return "border-input bg-muted text-foreground";
  }
}
