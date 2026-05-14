import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { useAuth } from "../auth/context";
import { AdminPage } from "../layout/admin_page";
import type {
  DigestPreviewResponse,
  NotificationPrefRow,
  NotificationPrefsEnvelope,
  NotificationPrefsUser,
  NotificationUserSettings,
} from "../api/types";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/lib/ui/drawer.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import {
  QEditableForm,
  type QEditableField,
} from "@/lib/ui/QEditableForm.ui";
import {
  QEditableList,
  type QEditableColumn,
} from "@/lib/ui/QEditableList.ui";

// Admin notification preferences editor (v1.7.35 §3.9.1). Closes the
// v1.5.3 "admin-side preferences editor deferred" note. Backend
// endpoint family: /api/_admin/notifications/users + /users/{id}/prefs.
//
// Layout: a QDatatable of every user who has a notification pref or
// settings row; clicking a row opens a right-side Drawer hosting the
// editor (the Schemas/Collections pattern). Inside the drawer:
//
//   • `digest_mode` is a parent-owned scalar <select> — the
//     discriminator that gates whether the digest-day-of-week field
//     renders (weekly only).
//   • a single QEditableForm (create mode, "Save") carries everything
//     else: the per-kind/per-channel grid is one field rendered as a
//     QEditableList; quiet-hours + digest settings are scalar fields.
//   • "Send digest preview" lives below the form — it shares the
//     parent's digest_mode so it disables itself when mode === "off".
//
// Save semantics: the Save button PUTs the full envelope back. The
// server UPSERTs both tables atomically per row and returns the
// canonical post-update state; we invalidate the cached envelope +
// user list to reflect any normalisation.

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

// HH:MM[:SS] accepted — the backend re-parses either form via
// parseClockTime. Empty string disables quiet hours.
const TIME_REGEX = /^([01]\d|2[0-3]):[0-5]\d(:[0-5]\d)?$/;

type DigestMode = "off" | "daily" | "weekly";

const SETTINGS_DEFAULTS: NotificationUserSettings = {
  quiet_hours_start: "",
  quiet_hours_end: "",
  quiet_hours_tz: "",
  digest_mode: "off",
  digest_hour: 8,
  digest_dow: 1,
  digest_tz: "",
};

// One row of the per-kind QEditableList: a kind plus its three
// per-channel enabled flags. The on-wire shape is one NotificationPrefRow
// per (kind, channel); these helpers fold/unfold between the two.
interface PrefsGridRow {
  kind: string;
  inapp: boolean;
  email: boolean;
  push: boolean;
}

function envelopeToGrid(prefs: NotificationPrefRow[]): PrefsGridRow[] {
  const byKind = new Map<string, PrefsGridRow>();
  for (const p of prefs) {
    let row = byKind.get(p.kind);
    if (!row) {
      row = { kind: p.kind, inapp: false, email: false, push: false };
      byKind.set(p.kind, row);
    }
    row[p.channel] = p.enabled;
  }
  return Array.from(byKind.values()).sort((a, b) => a.kind.localeCompare(b.kind));
}

function gridToPrefs(grid: PrefsGridRow[]): NotificationPrefRow[] {
  const out: NotificationPrefRow[] = [];
  for (const row of grid) {
    const kind = row.kind.trim();
    if (!kind) continue;
    for (const channel of CHANNELS) {
      // `frequency` is a forward-compat placeholder the server ignores.
      out.push({ kind, channel, enabled: !!row[channel], frequency: "" });
    }
  }
  return out;
}

export function NotificationsPrefsScreen() {
  const [selectedUserID, setSelectedUserID] = useState<string | null>(null);

  const columns: ColumnDef<NotificationPrefsUser>[] = [
    {
      id: "email",
      header: "Email",
      accessor: (u) => u.email,
      cell: (u) =>
        u.email ? (
          <span className="font-medium">{u.email}</span>
        ) : (
          <span className="font-mono text-xs text-muted-foreground">
            {u.user_id.slice(0, 8)}…
          </span>
        ),
    },
    {
      id: "user_id",
      header: "User ID",
      cell: (u) => (
        <span className="font-mono text-xs text-muted-foreground">
          {u.user_id}
        </span>
      ),
    },
    {
      id: "collection",
      header: "Collection",
      cell: (u) =>
        u.collection ? (
          <span className="font-mono text-xs">{u.collection}</span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      id: "state",
      header: "State",
      cell: (u) => (
        <span className="flex gap-1">
          {u.has_prefs ? (
            <Badge variant="secondary" className="text-[10px]">
              prefs
            </Badge>
          ) : null}
          {u.has_settings ? (
            <Badge variant="secondary" className="text-[10px]">
              settings
            </Badge>
          ) : null}
        </span>
      ),
    },
  ];

  return (
    <AdminPage>
      <AdminPage.Header
        title="Notification preferences"
        description={
          <>
            Edit per-user notification posture: per-kind/per-channel toggles
            plus quiet hours and digest mode. Changes are audited as{" "}
            <span className="font-mono">notifications.admin_prefs_changed</span>.
          </>
        }
      />

      <AdminPage.Body>
        <QDatatable
          columns={columns}
          rowKey={(u) => u.user_id}
          search
          searchPlaceholder="Filter by email…"
          onRowClick={(u) => setSelectedUserID(u.user_id)}
          emptyMessage="No users have notification preferences yet."
          fetch={async (params) => {
            const res = await adminAPI.notificationsPrefsUsersList({
              page: params.page,
              perPage: params.pageSize,
              q: params.search || undefined,
            });
            return { rows: res.items, total: res.totalItems };
          }}
        />
      </AdminPage.Body>

      <PrefsEditorDrawer
        userID={selectedUserID}
        onClose={() => setSelectedUserID(null)}
      />
    </AdminPage>
  );
}

// PrefsEditorDrawer — right-side Drawer shell. The body remounts each
// time the selected user changes (keyed on userID) so it re-seeds from
// the freshest envelope.
function PrefsEditorDrawer({
  userID,
  onClose,
}: {
  userID: string | null;
  onClose: () => void;
}) {
  return (
    <Drawer
      direction="right"
      open={userID !== null}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-xl">
        <DrawerHeader>
          <DrawerTitle>Notification preferences</DrawerTitle>
          <DrawerDescription>
            Per-kind channel toggles plus quiet hours and digest mode. Save
            PUTs the full envelope back.
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {userID !== null ? (
            <PrefsEditorBody key={userID} userID={userID} onClose={onClose} />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// PrefsEditorBody loads the envelope, then hands a fully-resolved
// snapshot to PrefsEditorForm so the form seeds its draft exactly once.
function PrefsEditorBody({
  userID,
  onClose,
}: {
  userID: string;
  onClose: () => void;
}) {
  const envQ = useQuery({
    queryKey: ["notifications-prefs", userID],
    queryFn: () => adminAPI.notificationsPrefsGet(userID),
    retry: false,
  });

  if (envQ.isLoading) {
    return <p className="text-sm text-muted-foreground">Loading prefs…</p>;
  }
  if (envQ.error) {
    const err = envQ.error as Error & { code?: string };
    const is404 = err.code === "not_found";
    return (
      <div className="rounded border border-input bg-muted p-4 text-sm text-foreground">
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
    <PrefsEditorForm
      userID={userID}
      envelope={envQ.data!}
      onClose={onClose}
    />
  );
}

// trimSeconds normalises an HH:MM:SS value down to HH:MM for the
// <input type="time"> UI. The backend re-parses either form.
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

// PrefsEditorForm — `digest_mode` is a parent-owned scalar above the
// QEditableForm; the form's `fields` are recomputed from it (the
// digest-day-of-week field renders for weekly only). The per-kind grid
// is a single QEditableForm field rendered as a QEditableList.
function PrefsEditorForm({
  userID,
  envelope,
  onClose,
}: {
  userID: string;
  envelope: NotificationPrefsEnvelope;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const settings = envelope.settings ?? SETTINGS_DEFAULTS;

  const [digestMode, setDigestMode] = useState<DigestMode>(
    settings.digest_mode === "daily" || settings.digest_mode === "weekly"
      ? settings.digest_mode
      : "off",
  );
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  // Seeded once — QEditableForm copies this into its draft on mount.
  // `digest_dow` always lives in the seed even when the field isn't
  // rendered, so toggling digest_mode never drops the value.
  const [seed] = useState<Record<string, unknown>>(() => ({
    prefs: envelopeToGrid(envelope.prefs ?? []),
    quiet_hours_start: trimSeconds(settings.quiet_hours_start),
    quiet_hours_end: trimSeconds(settings.quiet_hours_end),
    quiet_hours_tz: settings.quiet_hours_tz,
    digest_hour: settings.digest_hour,
    digest_dow: settings.digest_dow,
    digest_tz: settings.digest_tz,
  }));

  const prefsColumns: QEditableColumn<PrefsGridRow>[] = [
    {
      key: "kind",
      header: "kind",
      type: "text",
      width: 220,
      required: true,
      placeholder: "invite_received",
    },
    { key: "inapp", header: "inapp", type: "checkbox", width: 80 },
    { key: "email", header: "email", type: "checkbox", width: 80 },
    { key: "push", header: "push", type: "checkbox", width: 80 },
  ];

  const fields: QEditableField[] = [
    { key: "prefs", label: "Per-kind preferences" },
    {
      key: "quiet_hours_start",
      label: "Quiet hours start",
      helpText: "HH:MM, e.g. 22:00. Blank disables quiet hours.",
    },
    { key: "quiet_hours_end", label: "Quiet hours end", helpText: "HH:MM, e.g. 07:00." },
    { key: "quiet_hours_tz", label: "Quiet hours timezone (IANA)" },
    { key: "digest_hour", label: "Digest hour (0-23, local to digest tz)" },
    ...(digestMode === "weekly"
      ? [{ key: "digest_dow", label: "Digest day of week" } as QEditableField]
      : []),
    {
      key: "digest_tz",
      label: "Digest timezone (IANA)",
      helpText: "Leave blank to inherit the quiet-hours timezone.",
    },
  ];

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "prefs":
        return (
          <QEditableList
            columns={prefsColumns}
            data={(value as PrefsGridRow[]) ?? []}
            onChange={(rows) => onChange(rows)}
            createEmpty={() => ({
              kind: "",
              inapp: false,
              email: false,
              push: false,
            })}
            minRows={0}
            showAddButton
            addLabel="Add kind"
          />
        );
      case "quiet_hours_start":
      case "quiet_hours_end":
        return (
          <Input
            type="time"
            className="font-mono"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
          />
        );
      case "quiet_hours_tz":
      case "digest_tz":
        return (
          <TZInput
            value={(value as string) ?? ""}
            onChange={(v) => onChange(v)}
          />
        );
      case "digest_hour":
        return (
          <Input
            type="number"
            min={0}
            max={23}
            className="w-24 font-mono"
            value={value == null ? "" : String(value)}
            onInput={(e) => {
              const raw = parseInt(e.currentTarget.value || "0", 10);
              onChange(clamp(raw, 0, 23));
            }}
          />
        );
      case "digest_dow":
        return (
          <select
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
            value={String(value ?? 1)}
            onChange={(e) => onChange(parseInt(e.currentTarget.value, 10))}
          >
            {DOW_LABELS.map((label, idx) => (
              <option key={label} value={idx}>
                {label}
              </option>
            ))}
          </select>
        );
      default:
        return null;
    }
  };

  const validate = (d: Record<string, unknown>): Record<string, string> => {
    const fe: Record<string, string> = {};
    const qs = String(d.quiet_hours_start ?? "");
    const qe = String(d.quiet_hours_end ?? "");
    if (qs !== "" && !TIME_REGEX.test(qs)) {
      fe.quiet_hours_start = "Use HH:MM (e.g. 22:00)";
    }
    if (qe !== "" && !TIME_REGEX.test(qe)) {
      fe.quiet_hours_end = "Use HH:MM (e.g. 07:00)";
    }
    const h = Number(d.digest_hour);
    if (!Number.isInteger(h) || h < 0 || h > 23) {
      fe.digest_hour = "Must be an integer 0-23";
    }
    return fe;
  };

  const handleSave = async (d: Record<string, unknown>) => {
    setFieldErrors({});
    setFormError(null);
    const fe = validate(d);
    if (Object.keys(fe).length > 0) {
      setFieldErrors(fe);
      return;
    }
    const body: NotificationPrefsEnvelope = {
      user_id: userID,
      prefs: gridToPrefs((d.prefs as PrefsGridRow[]) ?? []),
      settings: {
        quiet_hours_start: String(d.quiet_hours_start ?? ""),
        quiet_hours_end: String(d.quiet_hours_end ?? ""),
        quiet_hours_tz: String(d.quiet_hours_tz ?? ""),
        digest_mode: digestMode,
        digest_hour: Number(d.digest_hour) || 0,
        digest_dow: Number(d.digest_dow) || 0,
        digest_tz: String(d.digest_tz ?? ""),
      },
    };
    try {
      await adminAPI.notificationsPrefsPut(userID, body);
      void qc.invalidateQueries({ queryKey: ["notifications-prefs", userID] });
      void qc.invalidateQueries({ queryKey: ["notifications-prefs-users"] });
      onClose();
    } catch (err) {
      // 422 with field-level errors → per-field errors. Otherwise banner.
      if (isAPIError(err) && err.status === 422) {
        const details = err.body.details;
        const raw =
          details &&
          typeof details === "object" &&
          "fields" in details &&
          typeof (details as { fields?: unknown }).fields === "object"
            ? (details as { fields: Record<string, unknown> }).fields
            : null;
        if (raw) {
          const next: Record<string, string> = {};
          for (const [k, v] of Object.entries(raw)) {
            next[k] = typeof v === "string" ? v : String(v);
          }
          if (Object.keys(next).length > 0) {
            setFieldErrors(next);
            return;
          }
        }
      }
      setFormError(err instanceof Error ? err.message : "Save failed.");
    }
  };

  return (
    <div className="space-y-4">
      <div className="rounded border bg-muted/40 px-3 py-2">
        <div className="text-sm font-medium truncate">
          {envelope.email || (
            <span className="font-mono text-xs">{userID}</span>
          )}
        </div>
        <div className="font-mono text-[11px] text-muted-foreground truncate">
          {userID}
        </div>
      </div>

      <div className="space-y-1.5">
        <span className="font-mono text-xs font-medium text-muted-foreground">
          Digest mode
        </span>
        <select
          className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
          value={digestMode}
          onChange={(e) => setDigestMode(e.currentTarget.value as DigestMode)}
        >
          <option value="off">off</option>
          <option value="daily">daily</option>
          <option value="weekly">weekly</option>
        </select>
      </div>

      <QEditableForm
        mode="create"
        fields={fields}
        values={seed}
        renderInput={renderInput}
        onCreate={handleSave}
        submitLabel="Save"
        onCancel={onClose}
        fieldErrors={fieldErrors}
        formError={formError}
      />

      <DigestPreviewControls userID={userID} digestMode={digestMode} />
    </div>
  );
}

// DigestPreviewControls renders the "Send digest preview" button + a
// recipient input. State is local. Disabled when digestMode === "off"
// (a preview of "no digest configured" is meaningless). Pre-fills the
// recipient with the admin's own email.
function DigestPreviewControls({
  userID,
  digestMode,
}: {
  userID: string;
  digestMode: string;
}) {
  const { state } = useAuth();
  const adminEmail = state.kind === "signed-in" ? state.me.email : "";
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
    <div className="rounded border bg-muted/40 px-3 py-3 space-y-2 text-sm">
      <span className="text-xs font-medium text-foreground">
        Send a sample digest to:
      </span>
      <div className="flex items-center gap-2">
        <Input
          type="email"
          value={recipient}
          onInput={(e) => setRecipient(e.currentTarget.value)}
          placeholder={adminEmail || "operator@example.com"}
          className="h-8 flex-1 text-xs font-mono"
          spellcheck={false}
          autoComplete="off"
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => previewM.mutate()}
          disabled={disabled}
          title={
            digestMode === "off"
              ? "Set a digest mode (daily or weekly) to enable preview"
              : "Render and email a sample digest for this user"
          }
        >
          {previewM.isPending ? "Sending…" : "Send preview"}
        </Button>
      </div>
      {previewM.isSuccess && previewM.data ? (
        <span className="text-xs text-primary">
          Sent to{" "}
          <span className="font-mono">{previewM.data.recipient}</span>
          {" · "}
          {previewM.data.kind_count} item
          {previewM.data.kind_count === 1 ? "" : "s"}
        </span>
      ) : null}
      {previewM.error ? (
        <span className="text-xs text-destructive">
          {(previewM.error as Error).message}
        </span>
      ) : null}
    </div>
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
    <>
      <Input
        type="text"
        value={value}
        onInput={(e) => onChange(e.currentTarget.value)}
        placeholder="UTC"
        list="iana-tz-quickpicks"
        className="font-mono"
      />
      <datalist id="iana-tz-quickpicks">
        {TZ_QUICKPICKS.map((tz) => (
          <option key={tz} value={tz} />
        ))}
      </datalist>
    </>
  );
}
