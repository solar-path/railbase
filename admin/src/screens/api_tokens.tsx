import { Fragment, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { APIToken } from "../api/types";
import { Pager } from "../layout/pager";

// API tokens admin screen — paginated browser over `_api_tokens` with
// create / revoke / rotate affordances. Backend endpoint family:
// /api/_admin/api-tokens (v1.7.9+). Companion to the v1.7.3
// `railbase auth token` CLI; same data model, web-ergonomic surface.
//
// Display-once contract is enforced on the surface: Create and Rotate
// flip into a banner with the raw token in a <code> block + copy
// button. Reloading the screen or dismissing the banner destroys the
// raw value — there is no path back to it.

type TTLPreset = "1h" | "24h" | "30d" | "90d" | "never";

const TTL_SECONDS: Record<TTLPreset, number | undefined> = {
  "1h": 60 * 60,
  "24h": 24 * 60 * 60,
  "30d": 30 * 24 * 60 * 60,
  "90d": 90 * 24 * 60 * 60,
  never: undefined,
};

export function APITokensScreen() {
  const qc = useQueryClient();

  const [page, setPage] = useState(1);
  const perPage = 50;
  const [ownerInput, setOwnerInput] = useState("");
  const [owner, setOwner] = useState(""); // debounced
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // Display-once banner state. When a create / rotate succeeds we
  // stash the raw token here; clearing the banner discards it.
  const [createdToken, setCreatedToken] = useState<
    { token: string; record: APIToken; context: "create" | "rotate" } | null
  >(null);

  // Modal visibility.
  const [createOpen, setCreateOpen] = useState(false);
  const [rotateFor, setRotateFor] = useState<APIToken | null>(null);

  // Debounce owner filter. 300ms matches jobs/logs.
  useEffect(() => {
    const t = setTimeout(() => setOwner(ownerInput.trim()), 300);
    return () => clearTimeout(t);
  }, [ownerInput]);

  useEffect(() => {
    setPage(1);
  }, [owner, includeRevoked]);

  const q = useQuery({
    queryKey: ["api-tokens", { page, perPage, owner, includeRevoked }],
    queryFn: () =>
      adminAPI.apiTokensList({
        page,
        perPage,
        owner: owner || undefined,
        include_revoked: includeRevoked,
      }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

  const revokeM = useMutation({
    mutationFn: (id: string) => adminAPI.apiTokensRevoke(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["api-tokens"] });
    },
  });

  const rotateM = useMutation({
    mutationFn: (args: { id: string; ttl_seconds?: number }) =>
      adminAPI.apiTokensRotate(args.id, args.ttl_seconds),
    onSuccess: (data) => {
      setCreatedToken({ token: data.token, record: data.record, context: "rotate" });
      setRotateFor(null);
      void qc.invalidateQueries({ queryKey: ["api-tokens"] });
    },
  });

  const createM = useMutation({
    mutationFn: adminAPI.apiTokensCreate,
    onSuccess: (data) => {
      setCreatedToken({ token: data.token, record: data.record, context: "create" });
      setCreateOpen(false);
      void qc.invalidateQueries({ queryKey: ["api-tokens"] });
    },
  });

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">API tokens</h1>
          <p className="text-sm text-neutral-500">
            {total} token{total === 1 ? "" : "s"} total. Long-lived bearer
            credentials for service-to-service auth. Raw values are shown
            exactly once on create / rotate — copy them then.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Pager page={page} totalPages={totalPages} onChange={setPage} />
          <button
            type="button"
            onClick={() => setCreateOpen(true)}
            className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800"
          >
            + Create token
          </button>
        </div>
      </header>

      {createdToken ? (
        <CreatedBanner
          token={createdToken.token}
          record={createdToken.record}
          context={createdToken.context}
          onDismiss={() => setCreatedToken(null)}
        />
      ) : null}

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-neutral-600">owner</span>
          <input
            type="text"
            value={ownerInput}
            onChange={(e) => setOwnerInput(e.target.value)}
            placeholder="UUID"
            className="rounded border border-neutral-300 px-2 py-1 w-72 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <input
            type="checkbox"
            checked={includeRevoked}
            onChange={(e) => setIncludeRevoked(e.target.checked)}
          />
          <span className="text-neutral-600">include revoked</span>
        </label>
        {ownerInput || includeRevoked ? (
          <button
            type="button"
            onClick={() => {
              setOwnerInput("");
              setOwner("");
              setIncludeRevoked(false);
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
              <th>name</th>
              <th>owner</th>
              <th>fingerprint</th>
              <th>scopes</th>
              <th>last used</th>
              <th>expires</th>
              <th>status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {(q.data?.items ?? []).map((t) => {
              const status = tokenStatus(t);
              const isOpen = expandedId === t.id;
              return (
                <Fragment key={t.id}>
                  <tr
                    onClick={() => setExpandedId(isOpen ? null : t.id)}
                    className="cursor-pointer"
                  >
                    <td className="font-medium">{t.name}</td>
                    <td className="rb-mono text-xs text-neutral-600 whitespace-nowrap">
                      {t.owner_collection}/{t.owner_id.slice(0, 8)}…
                    </td>
                    <td className="rb-mono text-xs">{t.fingerprint || "—"}</td>
                    <td className="rb-mono text-xs text-neutral-600">
                      {t.scopes.length === 0
                        ? <span className="text-neutral-400">(owner-bounded)</span>
                        : t.scopes.join(",")}
                    </td>
                    <td className="rb-mono text-xs text-neutral-500 whitespace-nowrap">
                      {t.last_used_at ?? "—"}
                    </td>
                    <td className="rb-mono text-xs text-neutral-500 whitespace-nowrap">
                      {t.expires_at ?? "never"}
                    </td>
                    <td>
                      <span className={"rounded px-1.5 py-0.5 text-xs " + statusColor(status)}>
                        {status}
                      </span>
                    </td>
                    <td className="text-right whitespace-nowrap">
                      <div className="flex justify-end gap-1" onClick={(e) => e.stopPropagation()}>
                        {status === "active" ? (
                          <>
                            <button
                              type="button"
                              onClick={() => setRotateFor(t)}
                              className="rounded border border-neutral-300 px-2 py-0.5 text-xs text-neutral-700 hover:bg-neutral-100"
                            >
                              rotate
                            </button>
                            <button
                              type="button"
                              onClick={() => {
                                if (window.confirm(`Revoke "${t.name}"? Existing clients using this token will lose access immediately.`)) {
                                  revokeM.mutate(t.id);
                                }
                              }}
                              className="rounded border border-red-300 bg-red-50 px-2 py-0.5 text-xs text-red-700 hover:bg-red-100"
                            >
                              revoke
                            </button>
                          </>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                  {isOpen ? (
                    <tr>
                      <td colSpan={8} className="bg-neutral-50">
                        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 p-3 text-xs">
                          <dt className="text-neutral-500">id</dt>
                          <dd className="rb-mono">{t.id}</dd>
                          <dt className="text-neutral-500">owner_id</dt>
                          <dd className="rb-mono">{t.owner_id}</dd>
                          <dt className="text-neutral-500">created_at</dt>
                          <dd className="rb-mono">{t.created_at}</dd>
                          <dt className="text-neutral-500">last_used_at</dt>
                          <dd className="rb-mono">{t.last_used_at ?? "—"}</dd>
                          <dt className="text-neutral-500">expires_at</dt>
                          <dd className="rb-mono">{t.expires_at ?? "never"}</dd>
                          <dt className="text-neutral-500">revoked_at</dt>
                          <dd className="rb-mono">{t.revoked_at ?? "—"}</dd>
                          <dt className="text-neutral-500">rotated_from</dt>
                          <dd className="rb-mono">{t.rotated_from ?? "—"}</dd>
                          <dt className="text-neutral-500">scopes</dt>
                          <dd className="rb-mono">
                            {t.scopes.length === 0 ? "(owner-bounded)" : t.scopes.join(", ")}
                          </dd>
                        </dl>
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
            {q.data?.items.length === 0 ? (
              <tr>
                <td colSpan={8} className="text-neutral-400 text-center py-4">
                  No tokens.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      {createOpen ? (
        <CreateModal
          pending={createM.isPending}
          error={createM.error instanceof Error ? createM.error.message : null}
          onClose={() => setCreateOpen(false)}
          onSubmit={(input) => createM.mutate(input)}
        />
      ) : null}

      {rotateFor ? (
        <RotateModal
          record={rotateFor}
          pending={rotateM.isPending}
          error={rotateM.error instanceof Error ? rotateM.error.message : null}
          onClose={() => setRotateFor(null)}
          onSubmit={(ttl_seconds) =>
            rotateM.mutate({ id: rotateFor.id, ttl_seconds })
          }
        />
      ) : null}
    </div>
  );
}

// CreatedBanner surfaces the raw token (display-once). The banner
// sits at the top of the screen until dismissed; once dismissed, the
// raw value is unrecoverable — clients must rotate to get a new one.
function CreatedBanner({
  token,
  record,
  context,
  onDismiss,
}: {
  token: string;
  record: APIToken;
  context: "create" | "rotate";
  onDismiss: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(token);
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
            Token {context === "create" ? "created" : "rotated"} — copy now, it won't be shown again.
          </div>
          <div className="text-xs text-emerald-800 mt-1">
            <span className="rb-mono">{record.name}</span>
            {" — "}
            fingerprint <span className="rb-mono">{record.fingerprint || "—"}</span>
            {record.expires_at ? (
              <>
                {" — expires "}
                <span className="rb-mono">{record.expires_at}</span>
              </>
            ) : (
              <span> — non-expiring</span>
            )}
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
          {token}
        </code>
        <button
          type="button"
          onClick={copy}
          className="rounded border border-emerald-400 bg-white px-3 py-1 text-sm text-emerald-800 hover:bg-emerald-100"
        >
          {copied ? "Copied!" : "Copy"}
        </button>
      </div>
      {context === "rotate" ? (
        <div className="text-xs text-emerald-800">
          The predecessor is still active. Once the successor is deployed,
          revoke the predecessor explicitly.
        </div>
      ) : null}
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
    owner_id: string;
    owner_collection: string;
    scopes?: string[];
    ttl_seconds?: number;
  }) => void;
}) {
  const [name, setName] = useState("");
  const [ownerID, setOwnerID] = useState("");
  const [ownerCollection, setOwnerCollection] = useState("users");
  const [scopesCSV, setScopesCSV] = useState("");
  const [ttl, setTTL] = useState<TTLPreset>("30d");

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const scopes = scopesCSV
      .split(",")
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
    onSubmit({
      name: name.trim(),
      owner_id: ownerID.trim(),
      owner_collection: ownerCollection.trim() || "users",
      scopes: scopes.length > 0 ? scopes : undefined,
      ttl_seconds: TTL_SECONDS[ttl],
    });
  };

  return (
    <ModalShell onClose={onClose} title="Create API token">
      <form onSubmit={submit} className="space-y-3">
        <ModalField label="Name (required)">
          <input
            autoFocus
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="CI deploy bot"
            className="w-full rounded border border-neutral-300 px-2 py-1 text-sm"
          />
        </ModalField>
        <ModalField label="Owner ID (UUID, required)">
          <input
            type="text"
            value={ownerID}
            onChange={(e) => setOwnerID(e.target.value)}
            placeholder="019e8a72-…"
            className="w-full rounded border border-neutral-300 px-2 py-1 text-sm rb-mono"
          />
        </ModalField>
        <ModalField label="Owner collection">
          <input
            type="text"
            value={ownerCollection}
            onChange={(e) => setOwnerCollection(e.target.value)}
            placeholder="users"
            className="w-full rounded border border-neutral-300 px-2 py-1 text-sm rb-mono"
          />
        </ModalField>
        <ModalField label="Scopes (comma-separated, optional)">
          <input
            type="text"
            value={scopesCSV}
            onChange={(e) => setScopesCSV(e.target.value)}
            placeholder="post.create, post.read"
            className="w-full rounded border border-neutral-300 px-2 py-1 text-sm rb-mono"
          />
          <div className="text-[11px] text-neutral-500 mt-1">
            Advisory in v1 — token authenticates as the owner with full owner permissions.
          </div>
        </ModalField>
        <ModalField label="TTL">
          <div className="flex flex-wrap gap-1">
            {(["1h", "24h", "30d", "90d", "never"] as const).map((p) => (
              <button
                type="button"
                key={p}
                onClick={() => setTTL(p)}
                className={
                  "rounded px-2 py-1 text-xs border " +
                  (ttl === p
                    ? "bg-neutral-900 text-white border-neutral-900"
                    : "bg-white text-neutral-700 border-neutral-300 hover:bg-neutral-100")
                }
              >
                {p}
              </button>
            ))}
          </div>
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
            disabled={pending || !name.trim() || !ownerID.trim()}
            className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {pending ? "Creating…" : "Create"}
          </button>
        </div>
      </form>
    </ModalShell>
  );
}

function RotateModal({
  record,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  record: APIToken;
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (ttl_seconds: number | undefined) => void;
}) {
  // For rotate the empty TTL means "inherit from predecessor", which
  // is the safe default per the Store contract.
  const [ttl, setTTL] = useState<TTLPreset | "inherit">("inherit");

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    onSubmit(ttl === "inherit" ? undefined : TTL_SECONDS[ttl]);
  };

  return (
    <ModalShell onClose={onClose} title={`Rotate "${record.name}"`}>
      <form onSubmit={submit} className="space-y-3">
        <div className="text-sm text-neutral-600">
          The predecessor will stay active until you revoke it explicitly.
          Distribute the successor first, then revoke this row.
        </div>
        <ModalField label="TTL for the new token">
          <div className="flex flex-wrap gap-1">
            <button
              type="button"
              onClick={() => setTTL("inherit")}
              className={
                "rounded px-2 py-1 text-xs border " +
                (ttl === "inherit"
                  ? "bg-neutral-900 text-white border-neutral-900"
                  : "bg-white text-neutral-700 border-neutral-300 hover:bg-neutral-100")
              }
            >
              inherit
            </button>
            {(["1h", "24h", "30d", "90d", "never"] as const).map((p) => (
              <button
                type="button"
                key={p}
                onClick={() => setTTL(p)}
                className={
                  "rounded px-2 py-1 text-xs border " +
                  (ttl === p
                    ? "bg-neutral-900 text-white border-neutral-900"
                    : "bg-white text-neutral-700 border-neutral-300 hover:bg-neutral-100")
                }
              >
                {p}
              </button>
            ))}
          </div>
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
            disabled={pending}
            className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {pending ? "Rotating…" : "Rotate"}
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

// tokenStatus derives the display status from the record's timestamps.
// Order matters: revoked outranks expired (a revoked token shouldn't
// flip back to "active" just because its expiry passes).
function tokenStatus(t: APIToken): "active" | "revoked" | "expired" {
  if (t.revoked_at) return "revoked";
  if (t.expires_at && new Date(t.expires_at).getTime() < Date.now()) return "expired";
  return "active";
}

function statusColor(s: "active" | "revoked" | "expired"): string {
  switch (s) {
    case "active":  return "bg-emerald-50 text-emerald-700 border border-emerald-200";
    case "revoked": return "bg-neutral-100 text-neutral-600 border border-neutral-300";
    case "expired": return "bg-amber-50 text-amber-700 border border-amber-200";
  }
}
