import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { useAuth } from "../auth/context";
import { Pager } from "../layout/pager";
import type {
  DigestPreviewResponse,
  NotificationPrefRow,
  NotificationPrefsEnvelope,
  NotificationUserSettings,
} from "../api/types";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Switch } from "@/lib/ui/switch.ui";
import { Card } from "@/lib/ui/card.ui";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

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

// Per-user settings form schema. The per-kind channel toggles are
// dynamic master-detail state and stay outside zod (see comment near
// UserPrefsEditor). Only the quiet-hours + digest settings form is
// modelled here.
//
// HH:MM[:SS] accepted — the backend re-parses either form via
// parseClockTime. Empty string disables quiet hours.
const TIME_REGEX = /^([01]\d|2[0-3]):[0-5]\d(:[0-5]\d)?$/;

const settingsFormSchema = z.object({
  quiet_hours_start: z
    .string()
    .refine((v) => v === "" || TIME_REGEX.test(v), {
      message: "Use HH:MM (e.g. 22:00)",
    }),
  quiet_hours_end: z
    .string()
    .refine((v) => v === "" || TIME_REGEX.test(v), {
      message: "Use HH:MM (e.g. 07:00)",
    }),
  quiet_hours_tz: z.string(),
  digest_mode: z.enum(["off", "daily", "weekly"]),
  digest_hour: z
    .number()
    .int("must be an integer")
    .min(0, "0-23")
    .max(23, "0-23"),
  digest_dow: z.number().int().min(0).max(6),
  digest_tz: z.string(),
});

type SettingsFormValues = z.infer<typeof settingsFormSchema>;

const SETTINGS_DEFAULTS: SettingsFormValues = {
  quiet_hours_start: "",
  quiet_hours_end: "",
  quiet_hours_tz: "",
  digest_mode: "off",
  digest_hour: 8,
  digest_dow: 1,
  digest_tz: "",
};

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
          <p className="text-sm text-muted-foreground">
            Edit per-user notification posture: per-kind/per-channel toggles
            plus quiet hours and digest mode. Changes are audited as{" "}
            <span className="rb-mono">notifications.admin_prefs_changed</span>.
          </p>
        </div>
      </header>

      <div className="grid grid-cols-[320px_1fr] gap-4 min-h-[480px]">
        {/* Left pane: user list */}
        <Card className="flex flex-col p-0">
          <div className="px-3 py-2 border-b">
            <Input
              type="text"
              value={searchInput}
              onInput={(e) => setSearchInput(e.currentTarget.value)}
              placeholder="Filter by email…"
              className="h-8 text-sm"
              spellcheck={false}
              autoComplete="off"
            />
          </div>

          <div className="flex-1 min-h-0 overflow-y-auto">
            {usersQ.isLoading ? (
              <div className="p-4 text-sm text-muted-foreground">Loading…</div>
            ) : users.length === 0 ? (
              <div className="p-4 text-sm text-muted-foreground">
                {search
                  ? "No users match that filter."
                  : "No users have notification preferences yet."}
              </div>
            ) : (
              <ul className="divide-y">
                {users.map((u) => (
                  <li
                    key={u.user_id}
                    onClick={() => setSelectedUserID(u.user_id)}
                    className={
                      "px-3 py-2 cursor-pointer text-sm " +
                      (selectedUserID === u.user_id
                        ? "bg-neutral-900 text-white"
                        : "hover:bg-muted")
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
                          : "text-muted-foreground")
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

          <div className="border-t px-3 py-2 flex items-center justify-between">
            <span className="text-xs text-muted-foreground">{total} users</span>
            <Pager page={page} totalPages={totalPages} onChange={setPage} />
          </div>
        </Card>

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
    <div className="rounded-lg border-2 border-dashed border-input bg-muted p-8 text-center h-full flex items-center justify-center">
      <div>
        <div className="text-sm font-medium text-foreground">
          Select a user to edit their preferences.
        </div>
        <div className="text-xs text-muted-foreground mt-1">
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
  //
  // Note: the per-kind / per-channel prefs grid stays on useState
  // intentionally — the column set is dynamic per kind, the rows are
  // master-detail UI state, and the "Save" button batches whatever
  // toggles the operator made. The settings block (below) is the part
  // we hoisted onto react-hook-form + zod.
  const [prefs, setPrefs] = useState<NotificationPrefRow[]>([]);
  // Track an in-progress "add row" so the operator can introduce a
  // new (kind, channel) tuple without immediately writing to the DB.
  const [newKind, setNewKind] = useState("");

  // Settings form — quiet-hours + digest fields. Trim seconds on the
  // way in so the <input type="time"> renders the short form; the
  // backend re-parses either shape via parseClockTime on save.
  const settingsForm = useForm<SettingsFormValues>({
    resolver: zodResolver(settingsFormSchema),
    defaultValues: SETTINGS_DEFAULTS,
    mode: "onSubmit",
  });

  useEffect(() => {
    if (!envQ.data) return;
    setPrefs(envQ.data.prefs ?? []);
    const s = envQ.data.settings;
    settingsForm.reset(
      s
        ? {
            quiet_hours_start: trimSeconds(s.quiet_hours_start),
            quiet_hours_end: trimSeconds(s.quiet_hours_end),
            quiet_hours_tz: s.quiet_hours_tz,
            // Server type widens to string for forward-compat; zod
            // narrows back to the enum, with "off" as the safe
            // fallback for any unrecognised mode.
            digest_mode:
              s.digest_mode === "daily" || s.digest_mode === "weekly"
                ? s.digest_mode
                : "off",
            digest_hour: s.digest_hour,
            digest_dow: s.digest_dow,
            digest_tz: s.digest_tz,
          }
        : SETTINGS_DEFAULTS,
    );
    // settingsForm is stable per render. Avoid re-running on form
    // identity churn by depending only on the server payload.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [envQ.data]);

  const saveM = useMutation({
    mutationFn: (body: NotificationPrefsEnvelope) =>
      adminAPI.notificationsPrefsPut(userID, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["notifications-prefs", userID] });
      void qc.invalidateQueries({ queryKey: ["notifications-prefs-users"] });
    },
    onError: (err) => {
      // 422 with field-level errors → setError per field. Otherwise
      // fall back to the inline banner the header strip already
      // renders from saveM.error.
      if (isAPIError(err) && err.status === 422) {
        const details = err.body.details;
        const fields =
          details &&
          typeof details === "object" &&
          "fields" in details &&
          (details as { fields?: unknown }).fields &&
          typeof (details as { fields?: unknown }).fields === "object"
            ? ((details as { fields: Record<string, unknown> }).fields)
            : null;
        if (fields) {
          for (const [k, v] of Object.entries(fields)) {
            const msg = typeof v === "string" ? v : String(v);
            // Only map keys that are actually in our settings schema.
            if (k in SETTINGS_DEFAULTS) {
              settingsForm.setError(k as keyof SettingsFormValues, {
                type: "server",
                message: msg,
              });
            }
          }
        }
      }
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

  // The header Save button submits the settings form, which runs
  // zod validation first and only then PUTs the combined envelope
  // (prefs grid + validated settings). Field-level errors stay on the
  // form; transport errors surface through saveM.error in the header.
  const onSave = settingsForm.handleSubmit((values) => {
    saveM.mutate({
      user_id: userID,
      prefs,
      settings: values satisfies NotificationUserSettings,
    });
  });

  if (envQ.isLoading) {
    return (
      <div className="text-sm text-muted-foreground p-4">Loading prefs…</div>
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
      <Card className="px-4 py-2 flex items-center justify-between">
        <div className="min-w-0">
          <div className="text-sm font-medium truncate">
            {envQ.data?.email || (
              <span className="rb-mono text-xs">{userID}</span>
            )}
          </div>
          <div className="rb-mono text-[11px] text-muted-foreground truncate">
            {userID}
          </div>
        </div>
        <div className="flex items-center gap-2">
          {saveM.isSuccess ? (
            <span className="text-xs text-emerald-700">Saved.</span>
          ) : null}
          {saveM.error ? (
            <span className="text-xs text-destructive">
              {(saveM.error as Error).message}
            </span>
          ) : null}
          <Button
            type="button"
            size="sm"
            onClick={onSave}
            disabled={saveM.isPending}
          >
            {saveM.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </Card>

      {/* Card 1: prefs grid */}
      <Card className="p-0">
        <header className="px-4 py-2 border-b flex items-center justify-between">
          <h2 className="text-sm font-semibold">Per-kind preferences</h2>
          <div className="flex items-center gap-2">
            <Input
              type="text"
              value={newKind}
              onInput={(e) => setNewKind(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addKindRow(newKind);
                }
              }}
              placeholder="Add kind (e.g. invite_received)"
              className="h-7 w-56 text-xs rb-mono"
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => addKindRow(newKind)}
              disabled={!newKind.trim()}
            >
              + add
            </Button>
          </div>
        </header>

        {prefsByKind.length === 0 ? (
          <div className="px-4 py-6 text-sm text-muted-foreground text-center">
            No per-kind preferences yet. Add a kind above to override
            channel defaults.
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>kind</TableHead>
                {CHANNELS.map((c) => (
                  <TableHead key={c} className="text-center">
                    {c}
                  </TableHead>
                ))}
                <TableHead></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {prefsByKind.map(({ kind, byChannel }) => (
                <TableRow key={kind}>
                  <TableCell className="rb-mono text-sm">{kind}</TableCell>
                  {CHANNELS.map((c) => {
                    const row = byChannel.get(c);
                    return (
                      <TableCell key={c} className="text-center">
                        <div className="inline-flex">
                          <Switch
                            checked={row?.enabled ?? false}
                            onCheckedChange={() => togglePref(kind, c)}
                            aria-label={`${kind} on ${c}`}
                          />
                        </div>
                      </TableCell>
                    );
                  })}
                  <TableCell className="text-right">
                    <Button
                      type="button"
                      variant="link"
                      size="sm"
                      onClick={() => removeKindRow(kind)}
                      className="h-auto px-0 text-xs text-destructive"
                    >
                      remove
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>

      {/* Card 2: settings form (RHF + zod) */}
      <Card className="p-0">
        <header className="px-4 py-2 border-b">
          <h2 className="text-sm font-semibold">Quiet hours and digest</h2>
        </header>
        <Form {...settingsForm}>
          <form onSubmit={onSave} className="p-4 grid grid-cols-2 gap-4 text-sm">
            <FormField
              control={settingsForm.control}
              name="quiet_hours_start"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Quiet hours start</FormLabel>
                  <FormControl>
                    <Input type="time" className="h-8 rb-mono w-auto" {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={settingsForm.control}
              name="quiet_hours_end"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Quiet hours end</FormLabel>
                  <FormControl>
                    <Input type="time" className="h-8 rb-mono w-auto" {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={settingsForm.control}
              name="quiet_hours_tz"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Quiet hours timezone (IANA)</FormLabel>
                  <FormControl>
                    <TZInput
                      value={field.value}
                      onChange={(v) => field.onChange(v)}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={settingsForm.control}
              name="digest_mode"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Digest mode</FormLabel>
                  <FormControl>
                    <select
                      className="h-8 rounded border border-input bg-transparent px-2 text-sm"
                      {...field}
                    >
                      <option value="off">off</option>
                      <option value="daily">daily</option>
                      <option value="weekly">weekly</option>
                    </select>
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={settingsForm.control}
              name="digest_hour"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Digest hour (0-23, local to digest tz)</FormLabel>
                  <FormControl>
                    <Input
                      type="number"
                      min={0}
                      max={23}
                      className="h-8 w-24 rb-mono"
                      value={field.value}
                      onInput={(e) => {
                        const raw = parseInt(
                          (e.currentTarget as HTMLInputElement).value || "0",
                          10,
                        );
                        field.onChange(clamp(raw, 0, 23));
                      }}
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={settingsForm.control}
              name="digest_dow"
              render={({ field }) => {
                const mode = settingsForm.watch("digest_mode");
                return (
                  <FormItem>
                    <FormLabel>Digest day of week (weekly only)</FormLabel>
                    <FormControl>
                      <select
                        disabled={mode !== "weekly"}
                        className="h-8 rounded border border-input bg-transparent px-2 text-sm disabled:opacity-50"
                        value={field.value}
                        onChange={(e) =>
                          field.onChange(
                            parseInt(
                              (e.currentTarget as HTMLSelectElement).value,
                              10,
                            ),
                          )
                        }
                        onBlur={field.onBlur}
                        name={field.name}
                        ref={field.ref}
                      >
                        {DOW_LABELS.map((label, idx) => (
                          <option key={label} value={idx}>
                            {label}
                          </option>
                        ))}
                      </select>
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                );
              }}
            />
            <FormField
              control={settingsForm.control}
              name="digest_tz"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Digest timezone (IANA, blank = quiet-hours tz)</FormLabel>
                  <FormControl>
                    <TZInput
                      value={field.value}
                      onChange={(v) => field.onChange(v)}
                    />
                  </FormControl>
                  <FormDescription>
                    Leave blank to inherit the quiet-hours timezone.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </form>
        </Form>
        {/* v1.7.36 — Send a digest-preview email so the operator can
            eyeball the layout without waiting for the cron to fire.
            Disabled when digest_mode === "off" (a preview of "no
            digest configured" is meaningless). Pre-fills the recipient
            with the admin's own email so the default click doesn't
            spam the user. */}
        <DigestPreviewControls
          userID={userID}
          digestMode={settingsForm.watch("digest_mode")}
        />
      </Card>
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
    <div className="border-t px-4 py-3 flex items-center gap-2 text-sm">
      <span className="text-xs font-medium text-foreground">
        Send a sample digest to:
      </span>
      <Input
        type="email"
        value={recipient}
        onInput={(e) => setRecipient(e.currentTarget.value)}
        placeholder={adminEmail || "operator@example.com"}
        className="h-7 w-64 text-xs rb-mono"
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
    <div className="flex items-center gap-1">
      <Input
        type="text"
        value={value}
        onInput={(e) => onChange(e.currentTarget.value)}
        placeholder="UTC"
        list="iana-tz-quickpicks"
        className="h-8 w-56 rb-mono"
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
