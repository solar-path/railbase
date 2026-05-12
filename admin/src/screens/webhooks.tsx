import { Fragment, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { Webhook, Delivery } from "../api/types";

// Webhooks admin screen (v1.7.17 §3.11). Companion to the
// `railbase webhooks ...` CLI; backend route family is
// /api/_admin/webhooks. Display-once contract on the secret: Create
// flips into a banner with the raw HMAC key in a <code> block +
// "Copy" button. Dismissing or reloading destroys the raw value.
//
// Per-row affordances: pause/resume (toggle by active flag), delete
// (window.confirm), expand to view recent deliveries. Failed
// deliveries get a "Replay" button that re-enqueues a fresh attempt.

export function WebhooksScreen() {
  const qc = useQueryClient();

  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<
    { secret: string; record: Webhook } | null
  >(null);

  const q = useQuery({
    queryKey: ["webhooks"],
    queryFn: () => adminAPI.webhooksList(),
  });

  const pauseM = useMutation({
    mutationFn: (id: string) => adminAPI.webhookPause(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });
  const resumeM = useMutation({
    mutationFn: (id: string) => adminAPI.webhookResume(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });
  const deleteM = useMutation({
    mutationFn: (id: string) => adminAPI.webhookDelete(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });
  const createM = useMutation({
    mutationFn: adminAPI.webhookCreate,
    onSuccess: (data) => {
      setCreatedSecret({ secret: data.secret, record: data.record });
      setCreateOpen(false);
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });

  const items = q.data?.items ?? [];

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Webhooks</h1>
          <p className="text-sm text-neutral-500">
            {items.length} webhook{items.length === 1 ? "" : "s"}. Outbound
            event subscribers — every matching record event triggers an
            HTTP POST signed with HMAC-SHA256.
          </p>
        </div>
        <button
          type="button"
          onClick={() => setCreateOpen(true)}
          className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800"
        >
          + Create webhook
        </button>
      </header>

      {createdSecret ? (
        <CreatedBanner
          secret={createdSecret.secret}
          record={createdSecret.record}
          onDismiss={() => setCreatedSecret(null)}
        />
      ) : null}

      {q.isLoading ? (
        <div className="text-sm text-neutral-500">Loading…</div>
      ) : items.length === 0 ? (
        <EmptyState onCreate={() => setCreateOpen(true)} />
      ) : (
        <div className="rounded border border-neutral-200 bg-white overflow-x-auto">
          <table className="rb-table">
            <thead>
              <tr>
                <th>name</th>
                <th>url</th>
                <th>events</th>
                <th>status</th>
                <th>created</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {items.map((w) => {
                const isOpen = expandedId === w.id;
                return (
                  <Fragment key={w.id}>
                    <tr
                      onClick={() => setExpandedId(isOpen ? null : w.id)}
                      className="cursor-pointer"
                    >
                      <td className="font-medium">{w.name}</td>
                      <td className="rb-mono text-xs text-neutral-600 max-w-xs truncate">
                        <code className="rb-mono">{w.url}</code>
                      </td>
                      <td>
                        <EventsCell events={w.events} />
                      </td>
                      <td>
                        <StatusBadge active={w.active} />
                      </td>
                      <td className="rb-mono text-xs text-neutral-500 whitespace-nowrap">
                        {w.created_at}
                      </td>
                      <td className="text-right whitespace-nowrap">
                        <div className="flex justify-end gap-1" onClick={(e) => e.stopPropagation()}>
                          {w.active ? (
                            <button
                              type="button"
                              onClick={() => pauseM.mutate(w.id)}
                              disabled={pauseM.isPending}
                              className="rounded border border-amber-300 bg-amber-50 px-2 py-0.5 text-xs text-amber-800 hover:bg-amber-100 disabled:opacity-50"
                            >
                              pause
                            </button>
                          ) : (
                            <button
                              type="button"
                              onClick={() => resumeM.mutate(w.id)}
                              disabled={resumeM.isPending}
                              className="rounded border border-emerald-300 bg-emerald-50 px-2 py-0.5 text-xs text-emerald-800 hover:bg-emerald-100 disabled:opacity-50"
                            >
                              resume
                            </button>
                          )}
                          <button
                            type="button"
                            onClick={() => {
                              if (window.confirm(`Delete webhook "${w.name}"? Recent delivery history will cascade away too.`)) {
                                deleteM.mutate(w.id);
                              }
                            }}
                            className="rounded border border-red-300 bg-red-50 px-2 py-0.5 text-xs text-red-700 hover:bg-red-100"
                          >
                            delete
                          </button>
                        </div>
                      </td>
                    </tr>
                    {isOpen ? (
                      <tr>
                        <td colSpan={6} className="bg-neutral-50">
                          <DeliveryTimeline webhookID={w.id} />
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {createOpen ? (
        <CreateModal
          pending={createM.isPending}
          error={createM.error instanceof Error ? createM.error.message : null}
          onClose={() => setCreateOpen(false)}
          onSubmit={(input) => createM.mutate(input)}
        />
      ) : null}
    </div>
  );
}

// EventsCell renders the comma list, capping visible entries at 3 and
// showing a "+N more" badge for the overflow. We keep the dotted
// patterns un-mangled (record.*.posts) so operators can read them
// verbatim against the docs.
function EventsCell({ events }: { events: string[] }) {
  const visible = events.slice(0, 3);
  const overflow = events.length - visible.length;
  return (
    <div className="rb-mono text-xs text-neutral-700 flex flex-wrap gap-1">
      {visible.map((e) => (
        <span key={e} className="rounded bg-neutral-100 px-1.5 py-0.5">
          {e}
        </span>
      ))}
      {overflow > 0 ? (
        <span className="rounded bg-neutral-200 px-1.5 py-0.5 text-neutral-600">
          +{overflow} more
        </span>
      ) : null}
    </div>
  );
}

function StatusBadge({ active }: { active: boolean }) {
  return active ? (
    <span className="rounded border border-emerald-200 bg-emerald-50 px-1.5 py-0.5 text-xs text-emerald-700">
      active
    </span>
  ) : (
    <span className="rounded border border-amber-200 bg-amber-50 px-1.5 py-0.5 text-xs text-amber-700">
      paused
    </span>
  );
}

// DeliveryTimeline pulls the per-webhook recent attempts inline. The
// query is keyed by webhookID so each expand panel has its own cache
// slot — collapsing + reopening reuses the cached page rather than
// re-fetching.
function DeliveryTimeline({ webhookID }: { webhookID: string }) {
  const qc = useQueryClient();
  const dq = useQuery({
    queryKey: ["webhook-deliveries", webhookID],
    queryFn: () => adminAPI.webhookDeliveries(webhookID, 50),
  });
  const replayM = useMutation({
    mutationFn: (deliveryID: string) => adminAPI.webhookReplay(webhookID, deliveryID),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhook-deliveries", webhookID] });
    },
  });

  if (dq.isLoading) {
    return <div className="text-xs text-neutral-500 p-3">Loading deliveries…</div>;
  }
  const items = dq.data?.items ?? [];
  if (items.length === 0) {
    return <div className="text-xs text-neutral-500 p-3">No deliveries yet.</div>;
  }
  return (
    <div className="p-3">
      <div className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500 mb-1">
        Recent deliveries
      </div>
      <table className="w-full text-xs">
        <thead className="text-neutral-500">
          <tr className="text-left">
            <th className="pr-3 py-1">created</th>
            <th className="pr-3 py-1">event</th>
            <th className="pr-3 py-1">status</th>
            <th className="pr-3 py-1">code</th>
            <th className="pr-3 py-1">attempt</th>
            <th className="pr-3 py-1">error</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {items.map((d) => (
            <tr key={d.id} className="border-t border-neutral-200">
              <td className="rb-mono pr-3 py-1 whitespace-nowrap text-neutral-600">
                {d.created_at}
              </td>
              <td className="rb-mono pr-3 py-1 whitespace-nowrap">{d.event}</td>
              <td className="pr-3 py-1">
                <DeliveryStatusBadge status={d.status} />
              </td>
              <td className="rb-mono pr-3 py-1 whitespace-nowrap">
                {d.response_code ?? "—"}
              </td>
              <td className="rb-mono pr-3 py-1 whitespace-nowrap">{d.attempt}</td>
              <td className="pr-3 py-1 text-red-700 max-w-xs truncate">
                {d.error_msg ?? ""}
              </td>
              <td className="text-right">
                {isFailed(d) ? (
                  <button
                    type="button"
                    onClick={() => replayM.mutate(d.id)}
                    disabled={replayM.isPending}
                    className="rounded border border-neutral-300 bg-white px-2 py-0.5 text-xs text-neutral-700 hover:bg-neutral-100 disabled:opacity-50"
                  >
                    replay
                  </button>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// isFailed mirrors the Store status enum: only `dead` is a permanent
// failure eligible for manual replay. `retry` is still in-flight via
// the jobs framework's exp-backoff — replaying it would race the
// scheduled retry.
function isFailed(d: Delivery): boolean {
  return d.status === "dead";
}

function DeliveryStatusBadge({ status }: { status: string }) {
  const cls = (() => {
    switch (status) {
      case "success":
        return "bg-emerald-50 text-emerald-700 border-emerald-200";
      case "pending":
        return "bg-neutral-100 text-neutral-700 border-neutral-300";
      case "retry":
        return "bg-amber-50 text-amber-700 border-amber-200";
      case "dead":
        return "bg-red-50 text-red-700 border-red-200";
      default:
        return "bg-neutral-100 text-neutral-600 border-neutral-300";
    }
  })();
  return (
    <span className={"rounded border px-1.5 py-0.5 text-[11px] " + cls}>
      {status}
    </span>
  );
}

// CreatedBanner mirrors api_tokens.tsx's display-once UX: emerald
// border, secret in a <code> block with a Copy button. Once dismissed
// the raw value is unrecoverable via the API — operators must rotate
// (delete + create) to mint a fresh secret.
function CreatedBanner({
  secret,
  record,
  onDismiss,
}: {
  secret: string;
  record: Webhook;
  onDismiss: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard API can be blocked; the <code> block is the fallback */
    }
  };
  return (
    <div className="rounded border-2 border-emerald-300 bg-emerald-50 p-4 space-y-2">
      <div className="flex items-start justify-between">
        <div>
          <div className="font-semibold text-emerald-900">
            Webhook created — copy the secret now, it won't be shown again.
          </div>
          <div className="text-xs text-emerald-800 mt-1">
            <span className="rb-mono">{record.name}</span>
            {" — "}
            <code className="rb-mono">{record.url}</code>
          </div>
        </div>
        <button
          type="button"
          onClick={onDismiss}
          className="text-emerald-700 hover:text-emerald-900 text-sm"
        >
          dismiss
        </button>
      </div>
      <div className="flex items-stretch gap-2">
        <code className="flex-1 rounded border border-emerald-300 bg-white px-3 py-2 rb-mono text-xs break-all">
          {secret}
        </code>
        <button
          type="button"
          onClick={copy}
          className="rounded border border-emerald-400 bg-white px-3 py-1 text-sm text-emerald-800 hover:bg-emerald-100"
        >
          {copied ? "Copied!" : "Copy"}
        </button>
      </div>
      <div className="text-xs text-emerald-800">
        Sign incoming payloads with HMAC-SHA256 using this key. See
        docs/21-webhooks.md for the signature header format.
      </div>
    </div>
  );
}

function EmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <div className="rounded-lg border-2 border-dashed border-neutral-300 bg-neutral-50 p-8 text-center">
      <div className="text-sm font-medium text-neutral-700">No webhooks yet.</div>
      <div className="text-xs text-neutral-500 mt-1">
        Outbound webhooks fan out every record event to your URL. HMAC-signed,
        retried with exponential backoff via the jobs framework.
      </div>
      <button
        type="button"
        onClick={onCreate}
        className="mt-3 rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800"
      >
        Create your first webhook
      </button>
    </div>
  );
}

function CreateModal({
  pending,
  error,
  onClose,
  onSubmit,
}: {
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (input: {
    name: string;
    url: string;
    events: string[];
    description?: string;
  }) => void;
}) {
  const [name, setName] = useState("");
  const [url, setURL] = useState("");
  const [eventsCSV, setEventsCSV] = useState("");
  const [description, setDescription] = useState("");

  // Light client-side URL check — must parse and use http/https. The
  // backend re-validates; this is just an immediate-feedback nicety so
  // operators don't round-trip on a typo.
  const urlValid = (() => {
    if (url.trim() === "") return false;
    try {
      const u = new URL(url.trim());
      return u.protocol === "http:" || u.protocol === "https:";
    } catch {
      return false;
    }
  })();

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const events = eventsCSV
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
    onSubmit({
      name: name.trim(),
      url: url.trim(),
      events,
      description: description.trim() || undefined,
    });
  };

  return (
    <ModalShell onClose={onClose} title="Create webhook">
      <form onSubmit={submit} className="space-y-3">
        <ModalField label="Name (required)">
          <input
            autoFocus
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="slack-on-post-create"
            className="w-full rounded border border-neutral-300 px-2 py-1 text-sm"
          />
        </ModalField>
        <ModalField label="URL (https://, required)">
          <input
            type="text"
            value={url}
            onChange={(e) => setURL(e.target.value)}
            placeholder="https://example.com/hooks/railbase"
            className={
              "w-full rounded border px-2 py-1 text-sm rb-mono " +
              (url && !urlValid ? "border-red-400" : "border-neutral-300")
            }
          />
          {url && !urlValid ? (
            <div className="text-[11px] text-red-600 mt-1">
              must be a valid http(s):// URL
            </div>
          ) : null}
        </ModalField>
        <ModalField label="Events (one per line or comma-separated, required)">
          <textarea
            value={eventsCSV}
            onChange={(e) => setEventsCSV(e.target.value)}
            rows={3}
            placeholder={"record.created.posts\nrecord.*.tags"}
            className="w-full rounded border border-neutral-300 px-2 py-1 text-sm rb-mono"
          />
          <div className="text-[11px] text-neutral-500 mt-1">
            Dotted patterns; <span className="rb-mono">*</span> matches one segment.
            See <span className="rb-mono">record.*.posts</span> for every verb on a collection.
          </div>
        </ModalField>
        <ModalField label="Description (optional)">
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={2}
            placeholder="What this webhook does, who owns it…"
            className="w-full rounded border border-neutral-300 px-2 py-1 text-sm"
          />
        </ModalField>

        {error ? (
          <div className="rounded border border-red-300 bg-red-50 p-2 text-xs text-red-700">
            {error}
          </div>
        ) : null}

        <div className="flex justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded border border-neutral-300 px-3 py-1 text-sm text-neutral-700 hover:bg-neutral-100"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={pending || !name.trim() || !urlValid || eventsCSV.trim() === ""}
            className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {pending ? "Creating…" : "Create"}
          </button>
        </div>
      </form>
    </ModalShell>
  );
}

function ModalShell({
  onClose,
  title,
  children,
}: {
  onClose: () => void;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div
      className="fixed inset-0 z-40 bg-black/40 flex items-center justify-center p-4"
      onClick={onClose}
    >
      <div
        className="bg-white rounded-lg p-6 max-w-md w-full shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="text-neutral-400 hover:text-neutral-700 text-xl leading-none"
            aria-label="Close"
          >
            ×
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

function ModalField({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <div className="text-xs font-medium text-neutral-700 mb-1">{label}</div>
      {children}
    </label>
  );
}
