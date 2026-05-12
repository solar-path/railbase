import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { useAuth } from "../auth/context";
import { Pager } from "../layout/pager";
import type {
  DigestPreviewResponse,
  NotificationPrefRow,
  NotificationPrefsEnvelope,
  NotificationUserSettings,
} from "../api/types";

// Admin notification preferences editor (v1.7.35 §3.9.1). Closes the
// v1.5.3 "admin-side preferences editor deferred" note. Backend
// endpoint family: /api/_admin/notifications/users + /users/{id}/prefs.
//
// Layout choice: master-detail. Left pane is a searchable user list;
// right pane shows two cards (prefs grid + settings form) for the
// selected user. The alternative shapes considered were:
//
//   - tabs (per-user-id) → forces the operator to remember UUIDs;
//     poor fit for a sparse table with hundreds of users.
//   - accordion-per-user → vertical scrolling explodes once any user
//     has more than a few kinds.
//
// Master-detail keeps both halves visible, supports type-to-search on
// the email column, and matches how PocketBase's analogous screen
// works — which is the closest UX target since this is the surface
// operators most often arrive at FROM that ecosystem.
//
// Save semantics: the Save button below the right pane PUTs the full
// envelope back. Server-side UPSERTs both tables atomically per row;
// the response carries the canonical post-update state and we
// invalidate the cached envelope to reflect any normalisation
// (digest_mode defaulting from "" → "off" being the common one).

// CHANNELS pins the channel-column order so the prefs grid renders
// stable across renders even when the source array's order varies.
const CHANNELS: Array<NotificationPrefRow["channel"]> = ["inapp", "email", "push"];

// DOW_LABELS pairs the digest_dow integer (0=Sun..6=Sat) with a
// short label for the dropdown. Matches Postgres EXTRACT(dow) — see
// docs in internal/notifications/quiet_digest.go.
const DOW_LABELS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// Common IANA tzs surfaced as quick-picks. The input is freeform
// (the backend validates via time.LoadLocation) so any IANA id works;
// these are just the dropdown shortcuts. Empty string = "UTC".
const TZ_QUICKPICKS = [
  "UTC",
  "America/New_York",
  "America/Los_Angeles",
  "Europe/London",
  "Europe/Paris",
  "Europe/Moscow",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Australia/Sydney",
];

export function NotificationsPrefsScreen() {
  // --- Left pane state ---
  const [page, setPage] = useState(1);
  const perPage = 25;
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState(""); // debounced
  const [selectedUserID, setSelectedUserID] = useState<string | null>(null);

  // Debounce the search box. 300ms matches the notifications screen.
  useEffect(() => {
    const t = setTimeout(() => setSearch(searchInput), 300);
    return () => clearTimeout(t);
  }, [searchInput]);
  useEffect(() => {
    setPage(1);
  }, [search]);

  const usersQ = useQuery({
    queryKey: ["notifications-prefs-users", { page, perPage, search }],
    queryFn: () =>
      adminAPI.notificationsPrefsUsersList({
        page,
        perPage,
        q: search || undefined,
      }),
  });

  const total = usersQ.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));
  const users = usersQ.data?.items ?? [];

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Notification preferences</h1>
          <p className="text-sm text-neutral-500">
            Edit per-user notification posture: per-kind/per-channel toggles
            plus quiet hours and digest mode. Changes are audited as{" "}
            <span className="rb-mono">notifications.admin_prefs_changed</span>.
          </p>
        </div>
      </header>

      <div className="grid grid-cols-[320px_1fr] gap-4 min-h-[480px]">
        {/* Left pane: user list */}
        <div className="rounded border border-neutral-200 bg-white flex flex-col">
          <div className="px-3 py-2 border-b border-neutral-200">
            <input
              type="text"
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
              placeholder="Filter by email…"
              className="w-full rounded border border-neutral-300 px-2 py-1 text-sm"
              spellCheck={false}
              autoComplete="off"
            />
          </div>

          <div className="flex-1 min-h-0 overflow-y-auto">
            {usersQ.isLoading ? (
              <div className="p-4 text-sm text-neutral-500">Loading…</div>
            ) : users.length === 0 ? (
              <div className="p-4 text-sm text-neutral-400">
                {search
                  ? "No users match that filter."
                  : "No users have notification preferences yet."}
              </div>
            ) : (
              <ul className="divide-y divide-neutral-100">
                {users.map((u) => (
                  <li
                    key={u.user_id}
                    onClick={() => setSelectedUserID(u.user_id)}
                    className={
                      "px-3 py-2 cursor-pointer text-sm " +
                      (selectedUserID === u.user_id
                        ? "bg-neutral-900 text-white"
                        : "hover:bg-neutral-50")
                    }
                  >
                    <div className="truncate font-medium">
                      {u.email || (
                        <span className="rb-mono text-xs opacity-70">
                          {u.user_id.slice(0, 8)}…
                        </span>
                      )}
                    </div>
                    <div
                      className={
                        "text-xs " +
                        (selectedUserID === u.user_id
                          ? "text-neutral-300"
                          : "text-neutral-500")
                      }
                    >
                      <span className="rb-mono">{u.user_id.slice(0, 8)}…</span>
                      {u.collection ? (
                        <span className="ml-2">
                          ·{" "}
                          <span className="rb-mono">{u.collection}</span>
                        </span>
                      ) : null}
                      {u.has_prefs ? " · prefs" : ""}
                      {u.has_settings ? " · settings" : ""}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="border-t border-neutral-200 px-3 py-2 flex items-center justify-between">
            <span className="text-xs text-neutral-500">{total} users</span>
            <Pager page={page} totalPages={totalPages} onChange={setPage} />
          </div>
        </div>

        {/* Right pane: editor */}
        <div className="min-w-0">
          {selectedUserID ? (
            <UserPrefsEditor userID={selectedUserID} />
          ) : (
            <EmptyDetailState />
          )}
        </div>
      </div>
    </div>
  );
}

function EmptyDetailState() {
  return (
    <div className="rounded-lg border-2 border-dashed border-neutral-300 bg-neutral-50 p-8 text-center h-full flex items-center justify-center">
      <div>
        <div className="text-sm font-medium text-neutral-700">
          Select a user to edit their preferences.
        </div>
        <div className="text-xs text-neutral-500 mt-1">
          The left pane lists every user who has at least one
          notification preference or settings row.
        </div>
      </div>
    </div>
  );
}

// UserPrefsEditor renders the two-card right pane for the selected
// user. We keep the form state local to the editor (rather than
// hoisted to the parent) so navigating between users discards the
// staged edits — matches the audit story ("a save IS the action").
function UserPrefsEditor({ userID }: { userID: string }) {
  const qc = useQueryClient();
  const envQ = useQuery({
    queryKey: ["notifications-prefs", userID],
    queryFn: () => adminAPI.notificationsPrefsGet(userID),
    retry: false,
  });

  // Local editable copy. We rebuild it whenever the server payload
  // changes (loaded fresh / after save invalidation).
  const [prefs, setPrefs] = useState<NotificationPrefRow[]>([]);
  const [settings, setSettings] = useState<NotificationUserSettings>({
    quiet_hours_start: "",
    quiet_hours_end: "",
    quiet_hours_tz: "",
    digest_mode: "off",
    digest_hour: 8,
    digest_dow: 1,
    digest_tz: "",
  });
  // Track an in-progress "add row" so the operator can introduce a
  // new (kind, channel) tuple without immediately writing to the DB.
  const [newKind, setNewKind] = useState("");

  useEffect(() => {
    if (envQ.data) {
      setPrefs(envQ.data.prefs ?? []);
      setSettings(
        envQ.data.settings ?? {
          quiet_hours_start: "",
          quiet_hours_end: "",
          quiet_hours_tz: "",
          digest_mode: "off",
          digest_hour: 8,
          digest_dow: 1,
          digest_tz: "",
        },
      );
    }
  }, [envQ.data]);

  const saveM = useMutation({
    mutationFn: (body: NotificationPrefsEnvelope) =>
      adminAPI.notificationsPrefsPut(userID, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["notifications-prefs", userID] });
      void qc.invalidateQueries({ queryKey: ["notifications-prefs-users"] });
    },
  });

  // Group prefs by kind for the grid renderer. Each row owns three
  // cells (one per channel) — missing entries default to "no row yet"
  // and render as a faint check. Toggling fills the row.
  const prefsByKind = useMemo(() => {
    const byKind = new Map<string, Map<string, NotificationPrefRow>>();
    for (const p of prefs) {
      if (!byKind.has(p.kind)) byKind.set(p.kind, new Map());
      byKind.get(p.kind)!.set(p.channel, p);
    }
    return Array.from(byKind.entries())
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([kind, m]) => ({ kind, byChannel: m }));
  }, [prefs]);

  const togglePref = (kind: string, channel: NotificationPrefRow["channel"]) => {
    setPrefs((current) => {
      const idx = current.findIndex(
        (p) => p.kind === kind && p.channel === channel,
      );
      if (idx === -1) {
        return [
          ...current,
          { kind, channel, enabled: true, frequency: "" },
        ];
      }
      const out = current.slice();
      out[idx] = { ...out[idx], enabled: !out[idx].enabled };
      return out;
    });
  };

  const addKindRow = (kind: string) => {
    const trimmed = kind.trim();
    if (!trimmed) return;
    setPrefs((current) => {
      // Idempotent: if the kind already exists, no-op. The toggles
      // for missing (kind, channel) tuples create on demand.
      if (current.some((p) => p.kind === trimmed)) return current;
      return [
        ...current,
        { kind: trimmed, channel: "inapp", enabled: true, frequency: "" },
      ];
    });
    setNewKind("");
  };

  const removeKindRow = (kind: string) => {
    setPrefs((current) => current.filter((p) => p.kind !== kind));
  };

  const onSave = () => {
    saveM.mutate({
      user_id: userID,
      prefs,
      settings,
    });
  };

  if (envQ.isLoading) {
    return (
      <div className="text-sm text-neutral-500 p-4">Loading prefs…</div>
    );
  }
  if (envQ.error) {
    const err = envQ.error as Error & { code?: string };
    const is404 = err.code === "not_found";
    return (
      <div className="rounded border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900">
        <div className="font-medium">
          {is404
            ? "No preferences yet for this user."
            : "Failed to load prefs."}
        </div>
        <div className="text-xs mt-1">{err.message}</div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {/* Header strip for the selected user */}
      <div className="rounded border border-neutral-200 bg-white px-4 py-2 flex items-center justify-between">
        <div className="min-w-0">
          <div className="text-sm font-medium truncate">
            {envQ.data?.email || (
              <span className="rb-mono text-xs">{userID}</span>
            )}
          </div>
          <div className="rb-mono text-[11px] text-neutral-500 truncate">
            {userID}
          </div>
        </div>
        <div className="flex items-center gap-2">
          {saveM.isSuccess ? (
            <span className="text-xs text-emerald-700">Saved.</span>
          ) : null}
          {saveM.error ? (
            <span className="text-xs text-red-700">
              {(saveM.error as Error).message}
            </span>
          ) : null}
          <button
            type="button"
            onClick={onSave}
            disabled={saveM.isPending}
            className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {saveM.isPending ? "Saving…" : "Save"}
          </button>
        </div>
      </div>

      {/* Card 1: prefs grid */}
      <section className="rounded border border-neutral-200 bg-white">
        <header className="px-4 py-2 border-b border-neutral-200 flex items-center justify-between">
          <h2 className="text-sm font-semibold">Per-kind preferences</h2>
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={newKind}
              onChange={(e) => setNewKind(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addKindRow(newKind);
                }
              }}
              placeholder="Add kind (e.g. invite_received)"
              className="rounded border border-neutral-300 px-2 py-1 text-xs w-56 rb-mono"
            />
            <button
              type="button"
              onClick={() => addKindRow(newKind)}
              disabled={!newKind.trim()}
              className="rounded border border-neutral-300 px-2 py-1 text-xs hover:bg-neutral-100 disabled:opacity-30"
            >
              + add
            </button>
          </div>
        </header>

        {prefsByKind.length === 0 ? (
          <div className="px-4 py-6 text-sm text-neutral-500 text-center">
            No per-kind preferences yet. Add a kind above to override
            channel defaults.
          </div>
        ) : (
          <table className="rb-table">
            <thead>
              <tr>
                <th>kind</th>
                {CHANNELS.map((c) => (
                  <th key={c} className="text-center">
                    {c}
                  </th>
                ))}
                <th></th>
              </tr>
            </thead>
            <tbody>
              {prefsByKind.map(({ kind, byChannel }) => (
                <tr key={kind}>
                  <td className="rb-mono text-sm">{kind}</td>
                  {CHANNELS.map((c) => {
                    const row = byChannel.get(c);
                    return (
                      <td key={c} className="text-center">
                        <input
                          type="checkbox"
                          checked={row?.enabled ?? false}
                          onChange={() => togglePref(kind, c)}
                          aria-label={`${kind} on ${c}`}
                        />
                      </td>
                    );
                  })}
                  <td className="text-right">
                    <button
                      type="button"
                      onClick={() => removeKindRow(kind)}
                      className="text-xs text-red-600 hover:underline"
                    >
                      remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      {/* Card 2: settings form */}
      <section className="rounded border border-neutral-200 bg-white">
        <header className="px-4 py-2 border-b border-neutral-200">
          <h2 className="text-sm font-semibold">Quiet hours and digest</h2>
        </header>
        <div className="p-4 grid grid-cols-2 gap-4 text-sm">
          <SettingsField label="Quiet hours start">
            <input
              type="time"
              value={trimSeconds(settings.quiet_hours_start)}
              onChange={(e) =>
                setSettings({ ...settings, quiet_hours_start: e.target.value })
              }
              className="rounded border border-neutral-300 px-2 py-1 rb-mono"
            />
          </SettingsField>
          <SettingsField label="Quiet hours end">
            <input
              type="time"
              value={trimSeconds(settings.quiet_hours_end)}
              onChange={(e) =>
                setSettings({ ...settings, quiet_hours_end: e.target.value })
              }
              className="rounded border border-neutral-300 px-2 py-1 rb-mono"
            />
          </SettingsField>
          <SettingsField label="Quiet hours timezone (IANA)">
            <TZInput
              value={settings.quiet_hours_tz}
              onChange={(v) =>
                setSettings({ ...settings, quiet_hours_tz: v })
              }
            />
          </SettingsField>
          <SettingsField label="Digest mode">
            <select
              value={settings.digest_mode}
              onChange={(e) =>
                setSettings({
                  ...settings,
                  digest_mode: e.target.value as NotificationUserSettings["digest_mode"],
                })
              }
              className="rounded border border-neutral-300 px-2 py-1"
            >
              <option value="off">off</option>
              <option value="daily">daily</option>
              <option value="weekly">weekly</option>
            </select>
          </SettingsField>
          <SettingsField label="Digest hour (0-23, local to digest tz)">
            <input
              type="number"
              min={0}
              max={23}
              value={settings.digest_hour}
              onChange={(e) =>
                setSettings({
                  ...settings,
                  digest_hour: clamp(parseInt(e.target.value || "0", 10), 0, 23),
                })
              }
              className="rounded border border-neutral-300 px-2 py-1 rb-mono w-24"
            />
          </SettingsField>
          <SettingsField label="Digest day of week (weekly only)">
            <select
              value={settings.digest_dow}
              disabled={settings.digest_mode !== "weekly"}
              onChange={(e) =>
                setSettings({
                  ...settings,
                  digest_dow: parseInt(e.target.value, 10),
                })
              }
              className="rounded border border-neutral-300 px-2 py-1 disabled:opacity-50"
            >
              {DOW_LABELS.map((label, idx) => (
                <option key={label} value={idx}>
                  {label}
                </option>
              ))}
            </select>
          </SettingsField>
          <SettingsField label="Digest timezone (IANA, blank = quiet-hours tz)">
            <TZInput
              value={settings.digest_tz}
              onChange={(v) =>
                setSettings({ ...settings, digest_tz: v })
              }
            />
          </SettingsField>
        </div>
        {/* v1.7.36 — Send a digest-preview email so the operator can
            eyeball the layout without waiting for the cron to fire.
            Disabled when digest_mode === "off" (a preview of "no
            digest configured" is meaningless). Pre-fills the recipient
            with the admin's own email so the default click doesn't
            spam the user. */}
        <DigestPreviewControls
          userID={userID}
          digestMode={settings.digest_mode}
        />
      </section>
    </div>
  );
}

// DigestPreviewControls renders the "Send digest preview" button + a
// recipient input under the digest card. State is local — no point
// hoisting since the rest of the editor doesn't care whether a
// preview has been sent. Status surfaces as a transient pill next to
// the button so the operator gets a confirm without losing context.
function DigestPreviewControls({
  userID,
  digestMode,
}: {
  userID: string;
  digestMode: string;
}) {
  const { state } = useAuth();
  const adminEmail =
    state.kind === "signed-in" ? state.me.email : "";
  const [recipient, setRecipient] = useState(adminEmail);
  // Keep the input in sync if the admin's email loads after first
  // mount (initial render can fire before the /me probe completes).
  useEffect(() => {
    if (adminEmail && !recipient) {
      setRecipient(adminEmail);
    }
  }, [adminEmail, recipient]);

  const previewM = useMutation({
    mutationFn: (): Promise<DigestPreviewResponse> =>
      adminAPI.sendDigestPreview(userID, recipient.trim() || undefined),
  });

  const disabled = digestMode === "off" || previewM.isPending;

  return (
    <div className="border-t border-neutral-200 px-4 py-3 flex items-center gap-2 text-sm">
      <span className="text-xs font-medium text-neutral-700">
        Send a sample digest to:
      </span>
      <input
        type="email"
        value={recipient}
        onChange={(e) => setRecipient(e.target.value)}
        placeholder={adminEmail || "operator@example.com"}
        className="rounded border border-neutral-300 px-2 py-1 text-xs rb-mono w-64"
        spellCheck={false}
        autoComplete="off"
      />
      <button
        type="button"
        onClick={() => previewM.mutate()}
        disabled={disabled}
        title={
          digestMode === "off"
            ? "Set a digest mode (daily or weekly) to enable preview"
            : "Render and email a sample digest for this user"
        }
        className="rounded border border-neutral-300 px-3 py-1 text-xs hover:bg-neutral-100 disabled:opacity-30"
      >
        {previewM.isPending ? "Sending…" : "Send preview"}
      </button>
      {previewM.isSuccess && previewM.data ? (
        <span className="text-xs text-emerald-700">
          Sent to{" "}
          <span className="rb-mono">{previewM.data.recipient}</span>
          {" · "}
          {previewM.data.kind_count} item
          {previewM.data.kind_count === 1 ? "" : "s"}
        </span>
      ) : null}
      {previewM.error ? (
        <span className="text-xs text-red-700">
          {(previewM.error as Error).message}
        </span>
      ) : null}
    </div>
  );
}

function SettingsField({
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

// TZInput is a freeform text box paired with a datalist of common
// IANA ids — the operator can pick from the list or type any other id
// (e.g. America/Argentina/Buenos_Aires). Backend validates on PUT.
function TZInput({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="flex items-center gap-1">
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="UTC"
        list="iana-tz-quickpicks"
        className="rounded border border-neutral-300 px-2 py-1 rb-mono w-56"
      />
      <datalist id="iana-tz-quickpicks">
        {TZ_QUICKPICKS.map((tz) => (
          <option key={tz} value={tz} />
        ))}
      </datalist>
    </div>
  );
}

// trimSeconds normalises an HH:MM:SS value down to HH:MM for the
// <input type="time"> default UI. The HTML element accepts the
// longer form but renders the seconds spinner only when one is
// emitted — operators rarely care, and the backend re-parses either
// form via parseClockTime.
function trimSeconds(t: string): string {
  if (!t) return "";
  return t.length >= 5 ? t.slice(0, 5) : t;
}

function clamp(n: number, lo: number, hi: number): number {
  if (Number.isNaN(n)) return lo;
  if (n < lo) return lo;
  if (n > hi) return hi;
  return n;
}
