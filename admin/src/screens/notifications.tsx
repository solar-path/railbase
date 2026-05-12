import { Fragment, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Pager } from "../layout/pager";

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
          <p className="text-sm text-neutral-500">
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
            <span
              key={"kind-" + k}
              className="inline-block bg-neutral-100 rounded px-2 py-0.5 text-xs"
              title={`${n} notifications with kind=${k}`}
            >
              <span className="rb-mono">{k}</span>
              <span className="text-neutral-500"> · {n}</span>
            </span>
          ))}
          {topChannels.map(([c, n]) => (
            <span
              key={"channel-" + c}
              className="inline-block bg-neutral-100 rounded px-2 py-0.5 text-xs"
              title={`${n} notifications delivered via ${c}`}
            >
              {c}
              <span className="text-neutral-500"> · {n}</span>
            </span>
          ))}
          {topKinds.length === 0 && topChannels.length === 0 ? (
            <span className="text-xs text-neutral-400">No deliveries yet.</span>
          ) : null}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">kind</span>
          <input
            type="text"
            value={kindInput}
            onChange={(e) => setKindInput(e.target.value)}
            placeholder="exact match"
            className="rounded border border-neutral-300 px-2 py-1 w-56 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">channel</span>
          <select
            value={channel}
            onChange={(e) => setChannel(e.target.value as ChannelFilter)}
            className="rounded border border-neutral-300 px-2 py-1"
          >
            <option value="">all</option>
            <option value="inapp">inapp</option>
            <option value="email">email</option>
            <option value="push">push</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">user_id</span>
          <input
            type="text"
            value={userIdInput}
            onChange={(e) => setUserIdInput(e.target.value)}
            placeholder="UUID"
            className="rounded border border-neutral-300 px-2 py-1 w-64 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <input
            type="checkbox"
            checked={unreadOnly}
            onChange={(e) => setUnreadOnly(e.target.checked)}
          />
          <span className="text-neutral-600">unread only</span>
        </label>
        {(kind || channel || userId || unreadOnly) ? (
          <button
            type="button"
            onClick={() => {
              setKindInput("");
              setKind("");
              setChannel("");
              setUserIdInput("");
              setUserId("");
              setUnreadOnly(false);
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
              <th>channel</th>
              <th>title</th>
              <th>user</th>
              <th>read</th>
            </tr>
          </thead>
          <tbody>
            {(listQ.data?.items ?? []).map((n) => {
              const isOpen = expandedId === n.id;
              return (
                <Fragment key={n.id}>
                  <tr
                    onClick={() => setExpandedId(isOpen ? null : n.id)}
                    className="cursor-pointer"
                  >
                    <td className="rb-mono text-xs text-neutral-500 whitespace-nowrap">
                      {n.created_at}
                    </td>
                    <td className="rb-mono">{n.kind}</td>
                    <td>
                      <span className={"rounded px-1.5 py-0.5 text-xs " + channelColor(n.channel)}>
                        {n.channel}
                      </span>
                    </td>
                    <td className="max-w-md truncate">{n.title}</td>
                    <td className="rb-mono text-xs" title={n.user_id}>
                      {n.user_id.slice(0, 8)}…
                    </td>
                    <td>
                      {n.read_at ? (
                        <span className="text-xs text-neutral-400">read</span>
                      ) : (
                        <span className="inline-flex items-center gap-1 text-xs text-emerald-700">
                          <span className="inline-block w-2 h-2 rounded-full bg-emerald-500" />
                          unread
                        </span>
                      )}
                    </td>
                  </tr>
                  {isOpen ? (
                    <tr>
                      <td colSpan={6} className="bg-neutral-50">
                        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 p-3 text-xs">
                          <dt className="text-neutral-500">id</dt>
                          <dd className="rb-mono">{n.id}</dd>
                          <dt className="text-neutral-500">user_id</dt>
                          <dd className="rb-mono">{n.user_id}</dd>
                          <dt className="text-neutral-500">tenant_id</dt>
                          <dd className="rb-mono">{n.tenant_id ?? "—"}</dd>
                          <dt className="text-neutral-500">priority</dt>
                          <dd className="rb-mono">{n.priority}</dd>
                          <dt className="text-neutral-500">read_at</dt>
                          <dd className="rb-mono">{n.read_at ?? "—"}</dd>
                          <dt className="text-neutral-500">expires_at</dt>
                          <dd className="rb-mono">{n.expires_at ?? "—"}</dd>
                          {n.body ? (
                            <>
                              <dt className="text-neutral-500 self-start">body</dt>
                              <dd>
                                <pre className="rb-mono text-xs text-neutral-700 whitespace-pre-wrap break-all m-0">
                                  {n.body}
                                </pre>
                              </dd>
                            </>
                          ) : null}
                          <dt className="text-neutral-500 self-start">payload</dt>
                          <dd>
                            <pre className="rb-mono text-xs text-neutral-700 whitespace-pre-wrap break-all m-0">
                              {JSON.stringify(n.payload ?? {}, null, 2)}
                            </pre>
                          </dd>
                        </dl>
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
            {listQ.data?.items.length === 0 ? (
              <tr>
                <td colSpan={6} className="text-neutral-400 text-center py-4">
                  No notifications.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// Channel badge palette per spec: inapp → neutral, email → sky,
// push → emerald.
function channelColor(c: string): string {
  switch (c) {
    case "inapp": return "bg-neutral-50 text-neutral-700 border border-neutral-200";
    case "email": return "bg-sky-50 text-sky-700 border border-sky-200";
    case "push":  return "bg-emerald-50 text-emerald-700 border border-emerald-200";
    default:      return "bg-neutral-50 text-neutral-700 border border-neutral-200";
  }
}
