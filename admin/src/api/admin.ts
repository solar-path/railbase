// Typed admin endpoint wrappers. These are thin functions over the
// raw client; component code never imports the client directly.

import { api } from "./client";
import type {
  AuthResponse,
  AdminRecord,
  BatchResponse,
  CollectionSpec,
  SchemaResponse,
  SettingsListResponse,
  AuditListResponse,
  AuditTimelineResponse,
  LogsListResponse,
  JobsListResponse,
  APIToken,
  APITokensListResponse,
  APITokenCreatedResponse,
  BackupsListResponse,
  BackupsCapabilities,
  BackupsRestoreDryRunResponse,
  BackupsRestoreBody,
  BackupsRestoreResponse,
  CacheListResponse,
  BackupCreatedResponse,
  CronSchedule,
  CronListResponse,
  CronKindsResponse,
  CronUpsertBody,
  CronRunNowResponse,
  EmailEventsListResponse,
  MailerTemplatesListResponse,
  MailerTemplateView,
  MailerConfigStatus,
  MailerProbeResult,
  MailerSaveResult,
  AuthMethodsStatus,
  AuthSaveResult,
  NotificationsListResponse,
  NotificationsStatsResponse,
  NotificationPrefsUsersResponse,
  NotificationPrefsEnvelope,
  DigestPreviewResponse,
  RecordsListResponse,
  RealtimeStats,
  HealthResponse,
  MetricsSnapshot,
  SystemAdminListResponse,
  SystemAdminSessionListResponse,
  SystemSessionListResponse,
  TrashListResponse,
  Webhook,
  WebhookCreatedResponse,
  WebhooksListResponse,
  Delivery,
  DeliveriesListResponse,
  HooksFile,
  HooksFilesListResponse,
  HookTestRunRequest,
  HookTestRunResult,
  I18nLocalesResponse,
  I18nFileResponse,
  RBACRolesListResponse,
  RBACRoleActionsResponse,
  AdminsWithRolesResponse,
  SettingsCatalogResponse,
  SiteInfo,
} from "./types";

export const adminAPI = {
  // ---- auth ----
  signin(email: string, password: string): Promise<AuthResponse> {
    return api.request("POST", "/auth", { body: { email, password } });
  },
  refresh(): Promise<AuthResponse> {
    return api.request("POST", "/auth-refresh");
  },
  logout(): Promise<void> {
    return api.request("POST", "/auth-logout");
  },
  me(): Promise<{ record: AdminRecord }> {
    return api.request("GET", "/me");
  },

  // ---- password recovery (v1.7.46) ----
  // Public endpoints; safe to call without an active session. The
  // backend always responds 200 on forgot-password (anti-enumeration)
  // except when the mailer is unconfigured — that returns 503 with a
  // hint pointing at the CLI escape hatch.
  forgotPassword(email: string): Promise<{ ok: true; message: string }> {
    return api.request("POST", "/forgot-password", { body: { email } });
  },
  resetPassword(
    token: string,
    newPassword: string,
  ): Promise<{ ok: true; sessions_revoked?: number; message: string }> {
    return api.request("POST", "/reset-password", {
      body: { token, new_password: newPassword },
    });
  },

  // ---- schema ----
  schema(): Promise<SchemaResponse> {
    return api.request("GET", "/schema");
  },

  // ---- runtime collection management (v0.9) ----
  // Create / edit / drop collections without a code deploy. The server
  // runs the DDL, persists the spec, and updates the live registry so
  // the new collection's /api/collections/{name}/records routes work
  // immediately. Only collections listed in SchemaResponse.editable
  // may be updated or deleted — code-defined ones are source-owned.
  createCollection(spec: CollectionSpec): Promise<CollectionSpec> {
    return api.request("POST", "/collections", { body: spec });
  },
  updateCollection(name: string, spec: CollectionSpec): Promise<CollectionSpec> {
    return api.request("PATCH", `/collections/${encodeURIComponent(name)}`, { body: spec });
  },
  deleteCollection(name: string): Promise<void> {
    return api.request("DELETE", `/collections/${encodeURIComponent(name)}`);
  },

  // ---- site identity (v1.x) ----
  // Public-readable {name, url} pulled from `site.name` / `site.url`
  // settings. The shell uses this to render the sidebar brand live
  // when the operator edits site.name. Mailer + WebAuthn still hold
  // their boot-time copy — the General Settings screen surfaces a
  // "restart required" badge for those consumers.
  siteInfo(): Promise<SiteInfo> {
    return api.request("GET", "/site-info");
  },

  // ---- settings ----
  settingsList(): Promise<SettingsListResponse> {
    return api.request("GET", "/settings");
  },
  // v1.x typed catalog. Returns the curated set of known settings
  // grouped by feature, plus the list of unknown persisted keys for
  // the "Advanced (raw)" fallback. The General settings screen reads
  // from THIS, not /settings, to render typed form controls.
  settingsCatalog(): Promise<SettingsCatalogResponse> {
    return api.request("GET", "/settings/catalog");
  },
  settingsSet(key: string, value: unknown): Promise<{ key: string; value: unknown }> {
    return api.request("PATCH", `/settings/${encodeURIComponent(key)}`, { body: value });
  },
  settingsDelete(key: string): Promise<void> {
    return api.request("DELETE", `/settings/${encodeURIComponent(key)}`);
  },

  // ---- audit ----
  // v1.7.11 — filter params added to match the backend filter bar.
  // Response shape (AuditListResponse) is unchanged; only the inbound
  // query keys expanded.
  audit(opts: {
    page?: number;
    perPage?: number;
    event?: string;
    outcome?: string;
    user_id?: string;
    since?: string;
    until?: string;
    error_code?: string;
  } = {}): Promise<AuditListResponse> {
    return api.request("GET", "/audit", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        event: opts.event,
        outcome: opts.outcome,
        user_id: opts.user_id,
        since: opts.since,
        until: opts.until,
        error_code: opts.error_code,
      },
    });
  },

  // v3.x — unified Timeline. Single endpoint backing the
  // Logs → Timeline screen (replaces the four-tab Audit / App logs /
  // Email events / Notifications split). UNION'd across
  // _audit_log_site + _audit_log_tenant; legacy _audit_log will join
  // when the Phase 1.5 migration consolidates.
  auditTimeline(opts: {
    page?: number;
    perPage?: number;
    actor_type?: string;
    actor_id?: string;
    event?: string;
    entity_type?: string;
    entity_id?: string;
    outcome?: string;
    tenant_id?: string;
    request_id?: string;
    since?: string;
    until?: string;
    source?: "all" | "site" | "tenant";
  } = {}): Promise<AuditTimelineResponse> {
    return api.request("GET", "/audit/timeline", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        actor_type: opts.actor_type,
        actor_id: opts.actor_id,
        event: opts.event,
        entity_type: opts.entity_type,
        entity_id: opts.entity_id,
        outcome: opts.outcome,
        tenant_id: opts.tenant_id,
        request_id: opts.request_id,
        since: opts.since,
        until: opts.until,
        source: opts.source,
      },
    });
  },

  // ---- logs ----
  logs(opts: {
    page?: number;
    perPage?: number;
    level?: string;
    since?: string;
    until?: string;
    request_id?: string;
    search?: string;
    user_id?: string;
  } = {}): Promise<LogsListResponse> {
    return api.request("GET", "/logs", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        level: opts.level,
        since: opts.since,
        until: opts.until,
        request_id: opts.request_id,
        search: opts.search,
        user_id: opts.user_id,
      },
    });
  },

  // ---- jobs ----
  jobs(opts: {
    page?: number;
    perPage?: number;
    status?: string;
    kind?: string;
  } = {}): Promise<JobsListResponse> {
    return api.request("GET", "/jobs", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        status: opts.status,
        kind: opts.kind,
      },
    });
  },

  // ---- api tokens ----
  apiTokensList(opts: {
    page?: number;
    perPage?: number;
    owner?: string;
    owner_collection?: string;
    include_revoked?: boolean;
  } = {}): Promise<APITokensListResponse> {
    return api.request("GET", "/api-tokens", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        owner: opts.owner,
        owner_collection: opts.owner_collection,
        include_revoked: opts.include_revoked ? "true" : undefined,
      },
    });
  },
  apiTokensCreate(input: {
    name: string;
    owner_id: string;
    owner_collection: string;
    scopes?: string[];
    ttl_seconds?: number;
  }): Promise<APITokenCreatedResponse> {
    return api.request("POST", "/api-tokens", { body: input });
  },
  apiTokensRevoke(id: string): Promise<{ record: APIToken }> {
    return api.request("POST", `/api-tokens/${encodeURIComponent(id)}/revoke`);
  },
  apiTokensRotate(id: string, ttl_seconds?: number): Promise<APITokenCreatedResponse> {
    return api.request("POST", `/api-tokens/${encodeURIComponent(id)}/rotate`, {
      body: ttl_seconds && ttl_seconds > 0 ? { ttl_seconds } : {},
    });
  },

  // ---- backups (v1.7.7 §3.11) ----
  // No pagination on list — operators typically have < 30 daily
  // archives before retention sweeps; a flat array keeps the screen
  // simple. The Create call can take a few seconds for non-trivial
  // databases; the caller is expected to disable the trigger button
  // while the promise is in flight.
  backupsList(): Promise<BackupsListResponse> {
    return api.request("GET", "/backups");
  },
  backupsCreate(): Promise<BackupCreatedResponse> {
    return api.request("POST", "/backups");
  },
  // ---- restore (gated by RAILBASE_ENABLE_UI_RESTORE + RBAC) ----
  // Capabilities is the SPA's first call on screen mount — it drives
  // affordance visibility and the "disabled because …" tooltip. The
  // dry-run endpoint is harmless (read-only manifest inspection) and
  // fires when the operator opens the confirm drawer. The execute
  // endpoint is the destructive one: maintenance.Begin flips on the
  // server, the user-facing /api/* surface 503s until restore commits.
  backupsCapabilities(): Promise<BackupsCapabilities> {
    return api.request("GET", "/backups/capabilities");
  },
  backupsRestoreDryRun(name: string): Promise<BackupsRestoreDryRunResponse> {
    return api.request(
      "POST",
      `/backups/${encodeURIComponent(name)}/restore-dry-run`,
    );
  },
  backupsRestore(
    name: string,
    body: BackupsRestoreBody,
  ): Promise<BackupsRestoreResponse> {
    return api.request("POST", `/backups/${encodeURIComponent(name)}/restore`, {
      body,
    });
  },

  // ---- cron schedules ----
  // CRUD-ish surface over `_cron`. Builtin schedules (from
  // jobs.DefaultSchedules) are protected: their kind can't be changed
  // via upsert and they can't be deleted from the admin UI — the
  // backend returns a typed validation error in both cases.
  cronList(): Promise<CronListResponse> {
    return api.request("GET", "/cron");
  },
  cronKinds(): Promise<CronKindsResponse> {
    return api.request("GET", "/cron/kinds");
  },
  cronUpsert(body: CronUpsertBody): Promise<CronSchedule> {
    return api.request("POST", "/cron", { body });
  },
  cronEnable(name: string): Promise<CronSchedule> {
    return api.request("POST", `/cron/${encodeURIComponent(name)}/enable`);
  },
  cronDisable(name: string): Promise<CronSchedule> {
    return api.request("POST", `/cron/${encodeURIComponent(name)}/disable`);
  },
  cronRunNow(name: string): Promise<CronRunNowResponse> {
    return api.request("POST", `/cron/${encodeURIComponent(name)}/run-now`);
  },
  cronDelete(name: string): Promise<void> {
    return api.request("DELETE", `/cron/${encodeURIComponent(name)}`);
  },

  // ---- email events (v1.7.35e §3.1.4 follow-up) ----
  // Read-only paginated browser over `_email_events`. Mirrors the
  // logs / audit wrappers — every filter is optional; the backend
  // applies defaults (perPage=50, page=1, no filters → newest first).
  listEmailEvents(opts: {
    page?: number;
    perPage?: number;
    recipient?: string;
    event?: string;
    template?: string;
    bounce_type?: string;
    since?: string;
    until?: string;
  } = {}): Promise<EmailEventsListResponse> {
    return api.request("GET", "/email-events", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        recipient: opts.recipient,
        event: opts.event,
        template: opts.template,
        bounce_type: opts.bounce_type,
        since: opts.since,
        until: opts.until,
      },
    });
  },

  // ---- mailer templates (v1.7.x §3.11) ----
  // Read-only viewer for the Mailer's email templates. The list
  // endpoint returns one row per built-in kind with override-status
  // flags; the per-kind endpoint returns raw markdown + rendered
  // HTML so the viewer can swap between Raw / Preview tabs without
  // a second round-trip. Editing is deferred to v1.1.x.
  mailerTemplatesList(): Promise<MailerTemplatesListResponse> {
    return api.request("GET", "/mailer-templates");
  },
  mailerTemplateView(kind: string): Promise<MailerTemplateView> {
    return api.request("GET", `/mailer-templates/${encodeURIComponent(kind)}`);
  },

  // ---- mailer config (Settings → Mailer) ----
  // The masked status snapshot + the probe / save endpoints behind
  // RequireAdmin. Going through the client (not raw fetch) so the
  // bearer token rides along.
  mailerStatus(): Promise<MailerConfigStatus> {
    return api.request("GET", "/_setup/mailer-status");
  },
  mailerProbe(body: Record<string, unknown>): Promise<MailerProbeResult> {
    return api.request("POST", "/_setup/mailer-probe", { body });
  },
  mailerSave(body: Record<string, unknown>): Promise<MailerSaveResult> {
    return api.request("POST", "/_setup/mailer-save", { body });
  },

  // ---- auth methods config (Settings → Auth methods) ----
  authStatus(): Promise<AuthMethodsStatus> {
    return api.request("GET", "/_setup/auth-status");
  },
  authSave(body: Record<string, unknown>): Promise<AuthSaveResult> {
    return api.request("POST", "/_setup/auth-save", { body });
  },

  // ---- notifications (v1.7.10 §3.11 / docs/17 #132-133) ----
  // Cross-user notifications log. Distinct from the user-facing
  // /api/notifications endpoints (per-user, mounted under the
  // authenticated user surface). These hit /api/_admin/notifications.
  notificationsList(opts: {
    page?: number;
    perPage?: number;
    kind?: string;
    channel?: string;
    user_id?: string;
    unread_only?: boolean;
  } = {}): Promise<NotificationsListResponse> {
    return api.request("GET", "/notifications", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        kind: opts.kind,
        channel: opts.channel,
        user_id: opts.user_id,
        unread_only: opts.unread_only ? "true" : undefined,
      },
    });
  },
  notificationsStats(): Promise<NotificationsStatsResponse> {
    return api.request("GET", "/notifications/stats");
  },

  // ---- notification preferences editor (v1.7.35 §3.9.1) ----
  // Admin-side counterpart to the v1.5.3 user-facing preferences
  // surface. The list endpoint walks the union of distinct user_ids
  // across both `_notification_preferences` and
  // `_notification_user_settings`; the per-user GET/PUT carry the full
  // envelope so the editor can submit both halves in one round-trip.
  notificationsPrefsUsersList(opts: {
    page?: number;
    perPage?: number;
    q?: string;
  } = {}): Promise<NotificationPrefsUsersResponse> {
    return api.request("GET", "/notifications/users", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        q: opts.q,
      },
    });
  },
  notificationsPrefsGet(userID: string): Promise<NotificationPrefsEnvelope> {
    return api.request("GET", `/notifications/users/${encodeURIComponent(userID)}/prefs`);
  },
  notificationsPrefsPut(
    userID: string,
    body: NotificationPrefsEnvelope,
  ): Promise<NotificationPrefsEnvelope> {
    return api.request("PUT", `/notifications/users/${encodeURIComponent(userID)}/prefs`, {
      body,
    });
  },
  // v1.7.36 — "Send digest preview" button on the prefs editor's
  // digest card. Renders a sample digest email using the user's
  // currently-queued `_notification_deferred` rows (or a 3-item synth
  // fallback when they have nothing queued) and emails it to the given
  // recipient. Omit `recipient` to default to the admin's own email —
  // useful so the operator can eyeball the layout without spamming the
  // user. Subject is prefixed with `[Preview]` server-side.
  sendDigestPreview(
    userID: string,
    recipient?: string,
  ): Promise<DigestPreviewResponse> {
    return api.request(
      "POST",
      `/notifications/users/${encodeURIComponent(userID)}/digest-preview`,
      { body: recipient ? { recipient } : {} },
    );
  },

  // ---- realtime monitor (v1.7.16 §3.11) ----
  // Snapshot of active SSE subscriptions + per-sub drop counters. The
  // monitor screen polls this every 5 s.
  realtimeStats(): Promise<RealtimeStats> {
    return api.request("GET", "/realtime");
  },

  // ---- health / metrics dashboard (v1.7.23 §3.11) ----
  // Aggregated runtime + DB + jobs + audit + logs + realtime + backups
  // metrics. Polled every 5 s by the dashboard screen.
  health(): Promise<HealthResponse> {
    return api.request("GET", "/health");
  },
  // In-process metric registry snapshot — HTTP throughput, error
  // counters, latency histogram, hook invocations. Companion to
  // health(): /health is point-in-time external state, /metrics is
  // monotonic counters that the chart strip converts into rates
  // client-side via useMetricRate.
  metrics(): Promise<MetricsSnapshot> {
    return api.request("GET", "/metrics");
  },

  // ---- webhooks (v1.7.17 §3.11) ----
  // Outbound webhooks admin surface. Display-once secret on create
  // (mirrors APIToken Create / Rotate). Pause / Resume return the
  // updated record so the UI doesn't need to re-list. Delete is
  // idempotent and surfaces {deleted: bool} so the UI can flash
  // "already gone" vs "removed".
  webhooksList(): Promise<WebhooksListResponse> {
    return api.request("GET", "/webhooks");
  },
  webhookCreate(body: {
    name: string;
    url: string;
    events: string[];
    description?: string;
    headers?: Record<string, string>;
    max_attempts?: number;
    timeout_ms?: number;
    active?: boolean;
  }): Promise<WebhookCreatedResponse> {
    return api.request("POST", "/webhooks", { body });
  },
  webhookPause(id: string): Promise<{ record: Webhook }> {
    return api.request("POST", `/webhooks/${encodeURIComponent(id)}/pause`);
  },
  webhookResume(id: string): Promise<{ record: Webhook }> {
    return api.request("POST", `/webhooks/${encodeURIComponent(id)}/resume`);
  },
  webhookDelete(id: string): Promise<{ deleted: boolean }> {
    return api.request("DELETE", `/webhooks/${encodeURIComponent(id)}`);
  },
  webhookDeliveries(id: string, limit?: number): Promise<DeliveriesListResponse> {
    return api.request("GET", `/webhooks/${encodeURIComponent(id)}/deliveries`, {
      query: { limit },
    });
  },
  webhookReplay(id: string, deliveryID: string): Promise<{ record: Delivery }> {
    return api.request(
      "POST",
      `/webhooks/${encodeURIComponent(id)}/deliveries/${encodeURIComponent(deliveryID)}/replay`,
    );
  },

  // ---- hooks editor (v1.7.20 §3.14 #123 / §3.11) ----
  // The {path} URL param carries a slash-separated relative path under
  // HooksDir. We delegate per-segment encoding to the slash-splitter so
  // sub-folders survive while filenames with reserved chars stay safe.
  hooksFilesList(): Promise<HooksFilesListResponse> {
    return api.request("GET", "/hooks/files");
  },
  hooksFileGet(path: string): Promise<HooksFile> {
    return api.request("GET", `/hooks/files/${encodeHooksPath(path)}`);
  },
  hooksFilePut(path: string, content: string): Promise<HooksFile> {
    return api.request("PUT", `/hooks/files/${encodeHooksPath(path)}`, {
      body: { content },
    });
  },
  hooksFileDelete(path: string): Promise<void> {
    return api.request("DELETE", `/hooks/files/${encodeHooksPath(path)}`);
  },

  // ---- Hooks test panel (v1.7.20 §3.4.11) ----
  // Fires the runtime against a synthetic record + captures output.
  // No DB side effects — the request body is the canonical contract;
  // see internal/api/adminapi/hooks_test_run.go for the runtime
  // sandboxing rationale.
  runHookTest(req: HookTestRunRequest): Promise<HookTestRunResult> {
    return api.request("POST", "/hooks/test-run", { body: req });
  },

  // ---- translations editor (v1.7.20 §3.11) ----
  // Locales are validated server-side against ^[a-z]{2,3}(-[A-Z]{2})?$
  // so the encodeURIComponent below is mostly defensive — any payload
  // that survives encoding but fails the regex gets a typed 400 from
  // the backend. Empty `entries` is a synonym for DELETE (the "clear
  // all" button uses that).
  i18nLocalesList(): Promise<I18nLocalesResponse> {
    return api.request("GET", "/i18n/locales");
  },
  i18nFileGet(locale: string): Promise<I18nFileResponse> {
    return api.request("GET", `/i18n/files/${encodeURIComponent(locale)}`);
  },
  i18nFilePut(locale: string, entries: Record<string, string>): Promise<unknown> {
    return api.request("PUT", `/i18n/files/${encodeURIComponent(locale)}`, {
      body: { entries },
    });
  },
  i18nFileDelete(locale: string): Promise<void> {
    return api.request("DELETE", `/i18n/files/${encodeURIComponent(locale)}`);
  },

  // ---- cache inspector (v1.7.x §3.11) ----
  // Read-only listing of registered cache.Cache instances + a manual
  // Clear action per instance. Empty registry → empty `instances`
  // array (not 503): the cache primitive ships in v1.5.1 but per-
  // subsystem wiring is gradual, so "no caches yet" is a normal
  // state during the rollout.
  cacheList(): Promise<CacheListResponse> {
    return api.request("GET", "/cache");
  },
  cacheClear(name: string): Promise<void> {
    return api.request("POST", `/cache/${encodeURIComponent(name)}/clear`);
  },

  // ---- system tables (v1.7.x §3.11) ----
  // Read-only browsers over the sensitive `_admins`, `_admin_sessions`,
  // and `_sessions` tables. CRUD lives on the CLI; the audit hook on
  // each call ensures every operator read is on the chain.
  listSystemAdmins(
    opts: { page?: number; perPage?: number } = {},
  ): Promise<SystemAdminListResponse> {
    return api.request("GET", "/_system/admins", {
      query: { page: opts.page, perPage: opts.perPage },
    });
  },
  listSystemAdminSessions(
    opts: { page?: number; perPage?: number } = {},
  ): Promise<SystemAdminSessionListResponse> {
    return api.request("GET", "/_system/admin-sessions", {
      query: { page: opts.page, perPage: opts.perPage },
    });
  },
  listSystemSessions(
    opts: { page?: number; perPage?: number } = {},
  ): Promise<SystemSessionListResponse> {
    return api.request("GET", "/_system/sessions", {
      query: { page: opts.page, perPage: opts.perPage },
    });
  },

  // ---- trash (v1.7.x §3.11) ----
  // Cross-collection listing of soft-deleted records. Restore is per-
  // collection via the regular REST endpoint (see restoreRecord below
  // on recordsAPI) — no admin restore route by design.
  trashList(opts: {
    page?: number;
    perPage?: number;
    collection?: string;
  } = {}): Promise<TrashListResponse> {
    return api.request("GET", "/trash", {
      query: {
        page: opts.page,
        perPage: opts.perPage,
        collection: opts.collection,
      },
    });
  },

  // ---- RBAC management (v1.x) ----
  // The roles/actions endpoints are read-gated (rbac.read); the
  // role-set swap is write-gated (rbac.write). The backend enforces
  // the "last system_admin can't be downgraded" safety guard and
  // returns 409 with a human-readable hint when violated — surface
  // that hint to the operator instead of swallowing it.
  rbacRolesList(): Promise<RBACRolesListResponse> {
    return api.request("GET", "/rbac/roles");
  },
  rbacRoleActions(roleID: string): Promise<RBACRoleActionsResponse> {
    return api.request("GET", `/rbac/roles/${encodeURIComponent(roleID)}/actions`);
  },
  adminsWithRoles(): Promise<AdminsWithRolesResponse> {
    return api.request("GET", "/admins-with-roles");
  },
  setAdminRoles(
    adminID: string,
    roleNames: string[],
  ): Promise<{ admin_id: string; roles: string[] }> {
    return api.request("PUT", `/admins/${encodeURIComponent(adminID)}/roles`, {
      body: { roles: roleNames },
    });
  },
};

// encodeHooksPath escapes each `/`-separated segment but preserves the
// separators themselves, so a nested path like `sub/foo.js` survives as
// `sub/foo.js` (not `sub%2Ffoo.js`). The Go side uses chi's `*` glob
// param which captures slashes literally, so this matches what the
// handler expects.
function encodeHooksPath(p: string): string {
  return p.split("/").map(encodeURIComponent).join("/");
}

// ---- generic /api/collections/{name}/records ----
//
// The admin UI browses regular CRUD endpoints with the admin token.
// The backend's user-auth middleware accepts the admin bearer token
// transparently because admins are first-class principals once
// promoted via per-route RBAC (deferred to v1.1). For v0.8 we treat
// "admin can read everything" as the implicit policy — RBAC editor
// in the admin UI surfaces it explicitly later.
//
// We define the helpers here (not in adminAPI) because they hit a
// different base path: /api/collections/* not /api/_admin/*.

const recordsBase = "/api/collections";

async function rawFetch<T>(method: string, path: string, body?: unknown): Promise<T> {
  // Use the same token the admin client holds. We can't reuse
  // client.request() because that hardcodes the /api/_admin/ prefix;
  // bypass it for /api/collections/.
  const headers: Record<string, string> = { Accept: "application/json" };
  let raw: BodyInit | undefined;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    raw = JSON.stringify(body);
  }
  const tok = (api as unknown as { token: string | null }).token;
  if (tok) headers["Authorization"] = "Bearer " + tok;

  const res = await fetch(path, { method, headers, body: raw, credentials: "include" });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let parsed: unknown = null;
  if (text) {
    try { parsed = JSON.parse(text); } catch { /* ignore */ }
  }
  if (!res.ok) {
    const err = (parsed as { error?: { code: string; message: string } } | null)?.error
      ?? { code: "internal", message: text || res.statusText };
    throw Object.assign(new Error(err.message), { code: err.code, status: res.status });
  }
  return parsed as T;
}

export const recordsAPI = {
  list<T = Record<string, unknown>>(
    collection: string,
    opts: { page?: number; perPage?: number; filter?: string; sort?: string } = {},
  ): Promise<RecordsListResponse<T>> {
    const q = new URLSearchParams();
    if (opts.page) q.set("page", String(opts.page));
    if (opts.perPage) q.set("perPage", String(opts.perPage));
    if (opts.filter) q.set("filter", opts.filter);
    if (opts.sort) q.set("sort", opts.sort);
    const qs = q.toString();
    return rawFetch("GET", `${recordsBase}/${encodeURIComponent(collection)}/records${qs ? "?" + qs : ""}`);
  },
  get<T = Record<string, unknown>>(collection: string, id: string): Promise<T> {
    return rawFetch("GET", `${recordsBase}/${encodeURIComponent(collection)}/records/${encodeURIComponent(id)}`);
  },
  create<T = Record<string, unknown>>(collection: string, input: Record<string, unknown>): Promise<T> {
    return rawFetch("POST", `${recordsBase}/${encodeURIComponent(collection)}/records`, input);
  },
  update<T = Record<string, unknown>>(collection: string, id: string, input: Record<string, unknown>): Promise<T> {
    return rawFetch("PATCH", `${recordsBase}/${encodeURIComponent(collection)}/records/${encodeURIComponent(id)}`, input);
  },
  delete(collection: string, id: string): Promise<void> {
    return rawFetch("DELETE", `${recordsBase}/${encodeURIComponent(collection)}/records/${encodeURIComponent(id)}`);
  },

  // recordsBatchDelete fans the selected ids out into a single non-
  // atomic batch op against POST /api/collections/{name}/records/batch.
  // Non-atomic mode (atomic:false) so partial failures surface row-by-
  // row in the 207 Multi-Status response — the admin UI renders the
  // per-op error breakdown so the operator can see exactly which rows
  // failed (rule denial, FK constraint, already-tombstoned, …) without
  // the whole batch rolling back.
  recordsBatchDelete(collection: string, ids: string[]): Promise<BatchResponse> {
    return rawFetch(
      "POST",
      `${recordsBase}/${encodeURIComponent(collection)}/records/batch`,
      {
        atomic: false,
        ops: ids.map((id) => ({ action: "delete", id })),
      },
    );
  },

  // restoreRecord clears the `deleted` tombstone on a soft-deleted
  // row. Backed by the v1.4.12 REST endpoint
  // POST /api/collections/{name}/records/{id}/restore. Returns the
  // restored record (same shape as `get`) on success; throws on
  // 404 (the row never existed, or wasn't a tombstone) or 403 if
  // the collection's UpdateRule rejects the admin principal.
  restoreRecord<T = Record<string, unknown>>(collection: string, id: string): Promise<T> {
    return rawFetch(
      "POST",
      `${recordsBase}/${encodeURIComponent(collection)}/records/${encodeURIComponent(id)}/restore`,
    );
  },
};
