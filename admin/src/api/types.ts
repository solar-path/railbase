// Shared types for admin API responses. Mirror what the Go side
// emits (see internal/api/adminapi/*).

export interface AdminRecord {
  id: string;
  email: string;
  created: string;
  updated: string;
  last_login_at: string | null;
}

export interface AuthResponse {
  token: string;
  record: AdminRecord;
}

/** One field of a registered collection (mirrors builder.FieldSpec). */
export interface FieldSpec {
  name: string;
  type:
    | "text" | "number" | "bool" | "date" | "email" | "url" | "json"
    | "select" | "multiselect" | "file" | "files"
    | "relation" | "relations" | "password" | "richtext";
  required?: boolean;
  unique?: boolean;
  indexed?: boolean;
  has_default?: boolean;
  default?: unknown;
  min_len?: number;
  max_len?: number;
  pattern?: string;
  fts?: boolean;
  min?: number;
  max?: number;
  is_int?: boolean;
  auto_create?: boolean;
  auto_update?: boolean;
  select_values?: string[];
  min_selections?: number;
  max_selections?: number;
  accept_mime?: string[];
  max_bytes?: number;
  related_collection?: string;
  cascade_delete?: boolean;
  set_null_on_delete?: boolean;
  password_min_len?: number;
  richtext_no_sanitize?: boolean;
}

export interface IndexSpec {
  name: string;
  columns: string[];
  unique?: boolean;
}

export interface RuleSet {
  list?: string;
  view?: string;
  create?: string;
  update?: string;
  delete?: string;
}

export interface CollectionSpec {
  name: string;
  tenant?: boolean;
  auth?: boolean;
  soft_delete?: boolean;
  fields: FieldSpec[];
  indexes?: IndexSpec[];
  rules?: RuleSet;
}

export interface SchemaResponse {
  collections: CollectionSpec[];
  count: number;
  /**
   * Names of collections created via the admin UI — the only ones the
   * UI may edit/delete. Code-defined collections are absent here and
   * render read-only. Older servers omit this field entirely.
   */
  editable?: string[];
}

export interface SettingItem {
  key: string;
  value: unknown;
}

export interface SettingsListResponse {
  items: SettingItem[];
}

export interface AuditEvent {
  seq: number;
  id: string;
  at: string;
  user_id: string | null;
  user_collection: string | null;
  tenant_id: string | null;
  event: string;
  outcome: "success" | "denied" | "failed" | "error";
  error_code: string | null;
  ip: string | null;
  user_agent: string | null;
}

export interface AuditListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: AuditEvent[];
}

/** v3.x — unified Timeline row (one event from _audit_log_site or
 *  _audit_log_tenant). Backs the single-screen Logs UI that replaces
 *  the four-tab split. tenant_id is null for site rows. before/after/
 *  meta/error_data ride as raw JSON (any) — render-time consumers
 *  diff or pretty-print as needed. */
export interface AuditTimelineRow {
  source: "site" | "tenant";
  chain_version: number; // 2 = v3.x split; 1 = legacy (future surface)
  seq: number;
  id: string;
  at: string; // RFC3339 nano
  tenant_id: string | null;
  actor: {
    type: "system" | "admin" | "api_token" | "job" | "user";
    id: string | null; // null = system
    email: string;
    collection: string;
  };
  event: string;
  entity: {
    type: string;
    id: string;
  };
  outcome: "success" | "denied" | "error";
  before: unknown | null;
  after: unknown | null;
  meta: unknown | null;
  error_code: string;
  error_data: unknown | null;
  ip: string;
  user_agent: string;
  request_id: string;
}

export interface AuditTimelineResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: AuditTimelineRow[];
}

export interface LogEvent {
  id: string;
  level: string; // "DEBUG" | "INFO" | "WARN" | "ERROR"
  message: string;
  attrs: Record<string, unknown>;
  source?: string;
  request_id?: string;
  user_id?: string;
  created: string; // RFC3339
}

export interface LogsListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: LogEvent[];
}

/** One row of `_jobs`, shaped for the admin Jobs queue browser. The
 *  raw payload column is intentionally omitted from the listing view —
 *  it can be arbitrary JSON and may be large. */
export interface JobRecord {
  id: string;
  queue: string;
  kind: string;
  status: "pending" | "running" | "completed" | "failed" | "cancelled";
  attempts: number;
  max_attempts: number;
  last_error: string | null;
  run_after: string; // RFC3339
  locked_by: string | null;
  locked_until: string | null;
  created_at: string; // RFC3339
  started_at: string | null;
  completed_at: string | null;
  cron_id: string | null;
}

export interface JobsListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: JobRecord[];
}

/** One row of `_api_tokens`, shaped for the admin API-tokens screen.
 *  The raw token (rbat_...) is NEVER emitted on this shape — only the
 *  display fingerprint + metadata. The Create / Rotate responses pair
 *  this record with a sibling `token: string` field that carries the
 *  raw value exactly once (display-once contract). */
export interface APIToken {
  id: string;
  name: string;
  owner_id: string;
  owner_collection: string;
  scopes: string[];
  fingerprint: string;
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
  revoked_at: string | null;
  rotated_from: string | null;
}

export interface APITokensListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: APIToken[];
}

/** Display-once envelope returned by Create / Rotate. The `token`
 *  field carries the raw rbat_... string — the UI MUST surface it
 *  once and discard from memory afterwards. */
export interface APITokenCreatedResponse {
  token: string;
  record: APIToken;
}

/** One archive in the v1.7.7 §3.11 backups listing. `path` is
 *  intentionally relative to DataDir (e.g. "backups/backup-…tar.gz")
 *  so the UI never sees an absolute server path. */
export interface BackupRecord {
  name: string;
  size_bytes: number;
  created: string; // RFC3339, mtime of the .tar.gz file
  path: string; // relative to DataDir
}

/** GET /api/_admin/backups response. No pagination — the operator
 *  typically has < 30 daily archives before retention sweeps. */
export interface BackupsListResponse {
  items: BackupRecord[];
}

/** One row of the persisted `_cron` table as surfaced by the admin
 *  API. `payload` is opaque JSON (per-kind shape). `is_builtin` flags
 *  schedules from jobs.DefaultSchedules() (cleanup_*, audit_seal, …) —
 *  the UI uses it to lock kind + hide the delete affordance. `kind_known`
 *  is false when the kind isn't currently registered in the running
 *  binary (e.g. an old schedule pointing at a removed handler). */
export interface CronSchedule {
  id: string;
  name: string;
  expression: string;
  kind: string;
  payload: unknown; // canonical JSON; UI renders / edits per kind
  enabled: boolean;
  last_run_at: string | null; // RFC3339
  next_run_at: string | null; // RFC3339
  created_at: string;
  updated_at: string;
  is_builtin: boolean;
  kind_known: boolean;
}

/** GET /api/_admin/cron response. */
export interface CronListResponse {
  items: CronSchedule[];
}

/** GET /api/_admin/cron/kinds response. Backs the admin UI's kind
 *  picker — order is unspecified, sort on the client side. */
export interface CronKindsResponse {
  kinds: string[];
}

/** POST /api/_admin/cron body. `payload` rides as raw JSON; the kind's
 *  handler validates the per-kind shape (e.g. scheduled_backup expects
 *  `{retention_days, out_dir?}`). `enabled` is optional: omit to keep
 *  the existing value (defaults to true on first insert). */
export interface CronUpsertBody {
  name: string;
  expression: string;
  kind: string;
  payload?: unknown;
  enabled?: boolean;
}

/** POST /api/_admin/cron/{name}/run-now response. */
export interface CronRunNowResponse {
  job_id: string;
}

/** POST /api/_admin/backups response (201). Carries the manifest
 *  summary (tables_count + rows_count + schema_head) so the success
 *  banner can render "Backup created: N tables, M rows" without a
 *  second fetch. */
export interface BackupCreatedResponse {
  name: string;
  size_bytes: number;
  created: string;
  path: string;
  manifest: {
    tables_count: number;
    rows_count: number;
    schema_head: string;
  };
}

/** GET /api/_admin/backups/capabilities response. Probed on screen
 *  mount so the SPA can decide whether to show the Restore affordance.
 *  Three fields are independent on purpose: the UI distinguishes
 *  "env flag missing" (operator action) from "this admin lacks the
 *  role" (RBAC action) so the rendered tooltip is specific. */
export interface BackupsCapabilities {
  /** RAILBASE_ENABLE_UI_RESTORE truthy on the server — when false the
   *  restore endpoints 503 regardless of RBAC. */
  ui_restore_enabled: boolean;
  /** This admin's RBAC view holds `admin.backup.restore`. */
  can_restore: boolean;
  /** A restore is currently in flight in this process (drives the
   *  in-progress banner that stays even if the operator left the tab
   *  open across the maintenance window). */
  maintenance_active: boolean;
}

/** POST /api/_admin/backups/{name}/restore-dry-run response. The admin
 *  UI renders this preview inside the confirm drawer so the operator
 *  sees exactly which tables and how many rows would be TRUNCATEd
 *  before they type the confirm token. */
export interface BackupsRestoreDryRunResponse {
  archive: string;
  archive_schema_head: string;
  current_schema_head: string;
  /** archive_schema_head === current_schema_head. When false, restore
   *  requires `force: true` to proceed. */
  schema_head_matches: boolean;
  format_version: number;
  /** Archive format version is one the running binary recognises. When
   *  false, the archive was created by a newer Railbase and restore is
   *  blocked even with force. */
  format_version_ok: boolean;
  created_at: string;
  railbase_version: string;
  postgres_version: string;
  tables_count: number;
  rows_count: number;
  tables: Array<{ schema: string; name: string; rows: number }>;
}

/** POST /api/_admin/backups/{name}/restore request body. `confirm` MUST
 *  equal the archive filename — the operator types it manually into
 *  the confirm drawer. `force` lets restore proceed across a migration
 *  head mismatch (after the operator ticks the "I understand" box). */
export interface BackupsRestoreBody {
  confirm: string;
  force?: boolean;
}

/** POST /api/_admin/backups/{name}/restore success response. Drives
 *  the post-restore banner: "Restored 12 tables / 4321 rows from
 *  backup-2026-01-12.tar.gz". */
export interface BackupsRestoreResponse {
  archive: string;
  tables_count: number;
  rows_count: number;
  schema_head: string;
  forced: boolean;
}

/** One row of `_notifications`, shaped for the admin Notifications
 *  screen (v1.7.10+). Mirrors the user-facing /api/notifications row
 *  with two derived fields: `channel` is synthesised as "inapp" today
 *  (every persisted row is an in-app delivery), `payload` is an alias
 *  of `data` so consumers can reach for either name. */
export interface NotificationRecord {
  id: string;
  user_id: string;
  tenant_id?: string;
  kind: string;
  channel: "inapp" | "email" | "push";
  title: string;
  body: string;
  data: Record<string, unknown>;
  payload: Record<string, unknown>;
  priority: "low" | "normal" | "high" | "urgent";
  read_at: string | null; // RFC3339, null when unread
  expires_at: string | null;
  created_at: string; // RFC3339
}

export interface NotificationsListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: NotificationRecord[];
}

/** One row in the admin notification-preferences user list (v1.7.35).
 *  `email` + `collection` are best-effort: when the user_id doesn't
 *  exist in any registered auth collection both come back as the empty
 *  string and the UI falls through to showing the truncated UUID. */
export interface NotificationPrefsUser {
  user_id: string;
  email: string;
  collection: string;
  has_prefs: boolean;
  has_settings: boolean;
  updated_at: string; // RFC3339
}

export interface NotificationPrefsUsersResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: NotificationPrefsUser[];
}

/** One row in the admin notification prefs editor's prefs grid. The
 *  `frequency` field is a forward-compat placeholder — the underlying
 *  `_notification_preferences` schema has no such column today, so the
 *  server always returns "" and ignores any value on PUT. */
export interface NotificationPrefRow {
  kind: string;
  channel: "inapp" | "email" | "push";
  enabled: boolean;
  frequency: string;
}

/** Wire shape for the per-user settings card (quiet hours + digest).
 *  Mirrors the Go-side settingsBody struct; times are HH:MM:SS strings
 *  emitted/accepted by the HTML <input type="time"> element. */
export interface NotificationUserSettings {
  quiet_hours_start: string;
  quiet_hours_end: string;
  quiet_hours_tz: string;
  digest_mode: "off" | "daily" | "weekly" | string;
  digest_hour: number;
  digest_dow: number;
  digest_tz: string;
}

/** GET / PUT envelope for /api/_admin/notifications/users/{user_id}/prefs. */
export interface NotificationPrefsEnvelope {
  user_id: string;
  email?: string;
  prefs: NotificationPrefRow[];
  settings: NotificationUserSettings;
}

/** Response envelope for the v1.7.36 "send digest preview" endpoint
 *  (POST /api/_admin/notifications/users/{user_id}/digest-preview).
 *  `kind_count` is the number of items that landed in the previewed
 *  email body — either queued digest deferrals or the 3-item synth
 *  fallback when the user has nothing queued. */
export interface DigestPreviewResponse {
  sent: boolean;
  recipient: string;
  kind_count: number;
}

/** Stats summary used by the admin Notifications screen header banner.
 *  by_channel reports zeros for email/push today — the field is forward-
 *  compatible for v1.6+ per-channel delivery tracking. */
export interface NotificationsStatsResponse {
  total: number;
  unread: number;
  by_kind: Record<string, number>;
  by_channel: Record<string, number>;
}

/** Generic /api/collections/{name}/records list response. */
export interface RecordsListResponse<T = Record<string, unknown>> {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: T[];
}

/** One result row from POST /api/collections/{name}/records/batch.
 *  In atomic mode the response is 200 with all-success items; in non-
 *  atomic mode the response is 207 Multi-Status and each item carries
 *  its own status code + optional error envelope. Mirrors
 *  internal/api/rest.batchResultItem. */
export interface BatchResultItem {
  action: "create" | "update" | "delete";
  status: number;
  data?: unknown;
  error?: {
    code: string;
    message: string;
    details?: Record<string, unknown>;
  };
}

/** POST /api/collections/{name}/records/batch response envelope. */
export interface BatchResponse {
  results: BatchResultItem[];
}

/** One row in the v1.7.x §3.11 trash listing. The four timestamps
 *  + id + source collection are all the screen needs to render
 *  "deleted X ago — collection/id — [Restore]". The full row payload
 *  is intentionally not returned by the admin endpoint — the column
 *  shape varies across collections and the trash UI is built around
 *  identity, not data inspection (the per-collection records browser
 *  is the right place for that). */
export interface TrashRecord {
  collection: string;
  id: string;
  created: string; // RFC3339
  updated: string; // RFC3339
  deleted: string; // RFC3339
}

/** One row of the v1.7.x §3.11 Mailer templates listing. `override_exists`
 *  is true when `<DataDir>/email_templates/<kind>.md` is a regular file
 *  on disk — the Mailer prefers that over the embedded built-in. When
 *  false, the size + mtime fields are zero/null and the viewer falls
 *  through to the built-in defaults. */
export interface MailerTemplateMeta {
  kind: string;
  override_exists: boolean;
  override_size_bytes: number;
  override_modified: string | null; // RFC3339 mtime, null when no override
}

/** GET /api/_admin/mailer-templates response. Flat list — there are
 *  only 8 known kinds today, so no pagination. */
export interface MailerTemplatesListResponse {
  templates: MailerTemplateMeta[];
}

/** GET /api/_admin/mailer-templates/{kind} response. `source` is the
 *  markdown text the Mailer would render today (override wins, else
 *  built-in). `html` is the same text piped through the built-in
 *  markdown renderer — safe to dangerously-set; the renderer is a
 *  fixed allowlist (see internal/mailer/markdown.go). */
export interface MailerTemplateView {
  kind: string;
  source: string;
  html: string;
  override_exists: boolean;
  override_size_bytes: number;
  override_modified: string | null;
}

/** GET /api/_admin/trash response. The flat `collections` list at
 *  the envelope top level enumerates every `.SoftDelete()` collection
 *  in the registry — the React filter dropdown reads it without a
 *  second round-trip. */
export interface TrashListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  items: TrashRecord[];
  collections: string[];
}

// ---- Webhooks (v1.7.17 §3.11) ----
// Mirrors `internal/webhooks.Webhook` json tags exactly. `SecretB64` is
// json:"-" server-side so it never appears in this shape; the Create
// response wraps the record with a sibling `secret` field carrying the
// raw value exactly once (display-once contract, mirrors APIToken).

export interface Webhook {
  id: string;
  name: string;
  url: string;
  events: string[];
  active: boolean;
  max_attempts: number;
  timeout_ms: number;
  headers?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

/** Display-once envelope returned by POST /api/_admin/webhooks. `secret`
 *  is the base64-encoded HMAC key — surface it once, then discard. */
export interface WebhookCreatedResponse {
  record: Webhook;
  secret: string;
}

export interface WebhooksListResponse {
  items: Webhook[];
}

/** Mirrors `internal/webhooks.Delivery`. `payload` is intentionally
 *  omitted (json:"-" on the Go struct) — the timeline view is metadata-
 *  only; if a payload-aware "replay with edit" lands later, the Go
 *  struct can add `payload json.RawMessage` and this interface follows. */
export interface Delivery {
  id: string;
  webhook_id: string;
  event: string;
  attempt: number;
  superseded_by?: string;
  status: "pending" | "success" | "retry" | "dead" | string;
  response_code?: number;
  response_body?: string;
  error_msg?: string;
  created_at: string;
  completed_at?: string;
}

export interface DeliveriesListResponse {
  items: Delivery[];
}

// ---- Hooks editor (v1.7.20 §3.14 #123 / §3.11) ----
// Mirrors `internal/api/adminapi.hooksFile`. The list endpoint omits
// `content`; the per-file GET populates it. Paths are slash-separated
// relative to HooksDir — never absolute.

export interface HooksFile {
  path: string;
  size: number;
  modified: string; // RFC3339
  content?: string; // populated by the per-file GET, omitted by list
}

export interface HooksFilesListResponse {
  items: HooksFile[];
}

// ---- Hooks test panel (v1.7.20 §3.4.11) ----
// Mirrors `internal/api/adminapi.hookTestRunRequest` /
// `hookTestRunResponse`. The event names are the wire identifiers the
// backend accepts (case-sensitive) — keep this union in sync with the
// `hookTestRunEvents` map.

export type HookEventName =
  | "BeforeCreate"
  | "AfterCreate"
  | "BeforeUpdate"
  | "AfterUpdate"
  | "BeforeDelete"
  | "AfterDelete";

export interface HookTestRunPrincipal {
  id: string;
  collection: string;
}

export interface HookTestRunRequest {
  event: HookEventName;
  /** Empty string fires only the wildcard ("*") handler set. */
  collection: string;
  record: Record<string, unknown>;
  principal?: HookTestRunPrincipal;
}

/** Outcome of a test-run. Mirrors the backend's three-state classification:
 *  ok = clean completion, rejected = handler threw, error = runtime
 *  failure (watchdog, load failure, internal panic). */
export type HookTestRunOutcome = "ok" | "rejected" | "error";

export interface HookTestRunResult {
  outcome: HookTestRunOutcome;
  console: string[];
  modified_record: Record<string, unknown>;
  duration_ms: number;
  /** Empty / omitted when outcome=ok. */
  error?: string;
}

// ---- Translations editor (v1.7.20 §3.11) ----
// Mirrors `internal/api/adminapi.i18nLocalesResponse` /
// `i18nFileResponse`. The list endpoint exposes the union of embedded
// + override locales plus per-locale coverage stats; the per-file GET
// returns the embedded reference bundle alongside the editable
// override (null when no file exists on disk).

export interface I18nCoverage {
  total_keys: number;
  translated: number;
  missing_keys: string[];
}

export interface I18nLocalesResponse {
  default: string;
  supported: string[];
  embedded: string[];
  overrides: string[];
  coverage: Record<string, I18nCoverage>;
}

export interface I18nFileResponse {
  locale: string;
  embedded: Record<string, string>;
  /** null when no override file exists on disk. */
  override: Record<string, string> | null;
}

// ---- Realtime monitor (v1.7.16 §3.11) ----
// Mirrors `internal/realtime.Stats` / `SubStats` — same field names so
// the React screen consumes the response without re-shaping.

export interface RealtimeSubscription {
  id: string;
  user_id: string;
  tenant_id?: string;
  topics: string[];
  created_at: string;
  dropped: number;
}

export interface RealtimeStats {
  subscription_count: number;
  subscriptions?: RealtimeSubscription[];
}

// ---- Cache inspector (v1.7.x §3.11) ----
// Mirrors `internal/api/adminapi.cacheInstanceJSON` / `cacheStatsJSON`.
// `hit_rate_pct` is computed server-side on int64 hits/misses to
// avoid floating-point drift in JS when the counters get large.

export interface CacheStats {
  hits: number;
  misses: number;
  hit_rate_pct: number;
  loads: number;
  load_fails: number;
  evictions: number;
  size: number;
}

export interface CacheInstance {
  name: string;
  stats: CacheStats;
}

export interface CacheListResponse {
  instances: CacheInstance[];
}

// ---- Email events browser (v1.7.35e §3.1.4 follow-up) ----
// Mirrors `internal/mailer.EmailEvent` — the admin endpoint emits the
// same struct verbatim (no DTO). `template` is empty for SendDirect
// calls and populated for SendTemplate; `bounce_type` is plugin-
// populated and stays empty for the core sent/failed events.

export interface EmailEvent {
  id: string;
  occurred_at: string; // RFC3339
  event: "sent" | "failed" | "bounced" | "opened" | "clicked" | "complained";
  driver: string; // smtp|console|ses|...
  message_id?: string;
  recipient: string;
  subject?: string;
  template?: string;
  bounce_type?: "hard" | "soft" | "transient" | string;
  error_code?: string;
  error_message?: string;
  metadata?: Record<string, unknown>;
}

export interface EmailEventsListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: EmailEvent[];
}

// Health / metrics dashboard (v1.7.23 §3.11). Polled every 5s by
// admin/src/screens/health.tsx. Every sub-section is independently
// nil-guarded server-side — a wired-down subsystem (no logs persistence,
// no realtime broker) returns zero counts rather than 500.
export interface HealthResponse {
  version: string;
  go_version: string;
  uptime_sec: number;
  started_at: string;
  now: string;
  pool: { acquired: number; idle: number; total: number; max: number };
  memory: {
    alloc_bytes: number;
    total_alloc_bytes: number;
    sys_bytes: number;
    num_gc: number;
    goroutines: number;
  };
  jobs: {
    pending: number;
    running: number;
    failed: number;
    completed: number;
    total: number;
  };
  audit: { total: number; last_24h: number };
  logs: {
    total: number;
    last_24h: number;
    by_level: Record<string, number>;
  };
  realtime: { subscriptions: number; events_dropped_total: number };
  backups: { count: number; total_bytes: number; last_completed_at: string | null };
  schema: {
    collections: number;
    auth_collections: number;
    tenant_collections: number;
  };
  request_id?: string;
}

// Metrics snapshot (v1.7.x §3.11). Companion to HealthResponse: where
// /health surfaces external state (DB pool, jobs, audit row counts),
// /metrics surfaces the in-process metric registry — HTTP throughput,
// status-bucketed error counters, request-latency histogram, and
// hook-dispatcher invocations.
//
// Counters are monotonic uint64s; consumers compute rates as
// (latestValue - previousValue) / dtSeconds (see useMetricRate in
// admin/src/hooks/use_metric_buffer.ts). Histograms expose count +
// p50/p95/p99 in integer nanoseconds; the React side converts to ms
// at display time so the JSON boundary stays unit-agnostic.
//
// Both maps are open-ended: future metric additions just appear as new
// keys without a schema bump. Today's canonical names:
//
//   - counters
//     - http.requests_total
//     - http.errors_4xx_total
//     - http.errors_5xx_total
//     - hooks.invocations_total
//   - histograms
//     - http.latency
export interface MetricsSnapshot {
  snapshot_at: string;
  counters: Record<string, number>;
  histograms: Record<string, {
    count: number;
    p50_ns: number;
    p95_ns: number;
    p99_ns: number;
  }>;
}

// ---- System tables (read-only browser, v1.7.x §3.11) ----
//
// The `_admins`, `_admin_sessions`, and `_sessions` rows are sensitive
// enough that the admin UI surfaces only the columns the operator
// actually needs to triage an incident — password hashes, token
// hashes, and revocation timestamps stay server-side. CRUD is on the
// CLI (`railbase admin create/delete`); these types back the read-only
// browser surface only.

/** One row of `_admins` shaped for the admin UI. `last_active` maps
 *  onto the underlying `last_login_at` column; `mfa_enabled` is
 *  derived from `_totp_enrollments` (always `false` until admin MFA
 *  lands — the column is plumbed already so a future toggle doesn't
 *  break the type). */
export interface SystemAdminRow {
  id: string;
  email: string;
  created: string;
  last_active: string | null;
  mfa_enabled: boolean;
}

export interface SystemAdminListResponse {
  items: SystemAdminRow[];
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
}

/** One row of `_admin_sessions` shaped for the admin UI. `created` /
 *  `last_used_at` map onto `created_at` / `last_active_at`. `user_agent`
 *  is truncated server-side to 60 chars. */
export interface SystemAdminSessionRow {
  id: string;
  admin_id: string;
  created: string;
  expires_at: string;
  last_used_at: string;
  ip: string | null;
  user_agent: string | null;
}

export interface SystemAdminSessionListResponse {
  items: SystemAdminSessionRow[];
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
}

/** One row of `_sessions` (user sessions) shaped for the admin UI.
 *  `user_collection` maps onto the underlying `collection_name`. */
export interface SystemSessionRow {
  id: string;
  user_id: string;
  user_collection: string;
  created: string;
  expires_at: string;
  last_used_at: string;
  ip: string | null;
  user_agent: string | null;
}

export interface SystemSessionListResponse {
  items: SystemSessionRow[];
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
}

/** Masked mailer config snapshot from /api/_admin/_setup/mailer-status. */
export interface MailerConfigStatus {
  configured_at?: string;
  config: {
    driver?: string;
    from_address?: string;
    from_name?: string;
    smtp_host?: string;
    smtp_port?: number;
    smtp_user?: string;
    tls?: string;
    smtp_password_set: boolean;
  };
}

/** Result of /api/_admin/_setup/mailer-probe (a test send). */
export interface MailerProbeResult {
  ok: boolean;
  error?: string;
  hint?: string;
  driver?: string;
}

/** Result of /api/_admin/_setup/mailer-save. */
export interface MailerSaveResult {
  ok?: boolean;
  note?: string;
}

// ---- auth methods config (Settings → Auth methods) ----

export interface AuthOAuthSnapshot {
  enabled: boolean;
  client_id?: string;
  /** "set" sentinel on read — never the real value. */
  client_secret?: string;
  issuer?: string;
}

export interface AuthLDAPSnapshot {
  enabled: boolean;
  url?: string;
  tls_mode?: string;
  insecure_skip_verify?: boolean;
  bind_dn?: string;
  bind_password_set: boolean;
  user_base_dn?: string;
  user_filter?: string;
  email_attr?: string;
  name_attr?: string;
}

export interface AuthSAMLSnapshot {
  enabled: boolean;
  idp_metadata_url?: string;
  idp_metadata_xml?: string;
  sp_entity_id?: string;
  sp_acs_url?: string;
  sp_slo_url?: string;
  email_attribute?: string;
  name_attribute?: string;
  allow_idp_initiated?: boolean;
  sign_authn_requests?: boolean;
  sp_cert_pem?: string;
  sp_key_pem_set?: boolean;
  group_attribute?: string;
  role_mapping?: string;
}

export interface AuthSCIMSnapshot {
  enabled: boolean;
  collection?: string;
  tokens_active: number;
  endpoint_url?: string;
}

export interface AuthPluginGated {
  name: string;
  display_name: string;
  plugin: string;
  available_in: string;
}

/** Masked auth-methods snapshot from /api/_admin/_setup/auth-status. */
export interface AuthMethodsStatus {
  configured_at?: string;
  methods: Record<string, boolean>;
  oauth: Record<string, AuthOAuthSnapshot>;
  ldap: AuthLDAPSnapshot;
  saml: AuthSAMLSnapshot;
  scim: AuthSCIMSnapshot;
  plugin_gated: AuthPluginGated[];
  redirect_base: string;
}

/** Result of /api/_admin/_setup/auth-save. */
export interface AuthSaveResult {
  ok?: boolean;
  note?: string;
}

// ===== RBAC management (v1.x) =====

/**
 * One role from /api/_admin/rbac/roles. The `scope` is "site" or
 * "tenant" — the admin-management UI cares only about site roles
 * (the admins-with-roles join filters by scope='site'), but the type
 * is general so a future tenant-role UI can reuse it.
 *
 * `is_system` marks the seeded roles (system_admin, system_readonly,
 * admin, user, guest, owner, …). The UI uses it to disable delete
 * and to render a small "system" badge.
 */
export interface RBACRole {
  id: string;
  name: string;
  scope: "site" | "tenant";
  description: string;
  is_system: boolean;
}

export interface RBACRolesListResponse {
  roles: RBACRole[];
}

/**
 * Result of /api/_admin/rbac/roles/{id}/actions. When the role is a
 * bypass role (site:system_admin or tenant:owner) the `actions` list
 * is empty and `bypass` is true — the SPA renders "Full bypass — all
 * actions" instead of "No actions granted".
 */
export interface RBACRoleActionsResponse {
  actions: string[];
  bypass: boolean;
}

/**
 * One row of the admins-with-roles grid. `roles` is the list of SITE
 * role names currently assigned. An empty list means the admin holds
 * zero site roles — gated handlers will deny them, but the
 * AdminAuthMiddleware still recognises them as authenticated, so they
 * can sign in and see the (empty) admin home page.
 */
export interface AdminWithRoles {
  id: string;
  email: string;
  roles: string[];
}

export interface AdminsWithRolesResponse {
  admins: AdminWithRoles[];
}

// ===== Settings catalog (v1.x) =====

/**
 * The shape each form control should render. Stable wire values; the
 * SPA's `switch` on this string MUST stay in sync with the backend's
 * SettingType constants in settings_catalog.go.
 */
export type SettingType =
  | "string"
  | "bool"
  | "int"
  | "csv"
  | "duration"
  | "json";

/**
 * Whether changing this setting takes effect immediately or needs
 * a server restart. The SPA renders a "restart required" badge for
 * "restart" so operators don't expect Save to do anything visible
 * until they bounce the process.
 *
 * Mixed-reload settings (one consumer live, another boot-only — e.g.
 * `site.name` re-renders the admin UI live but the mailer holds the
 * old value) are marked "restart"; the Description carries the
 * nuance.
 */
export type SettingReload = "live" | "restart";

/**
 * One curated catalog entry. The backend declares this in
 * settings_catalog.go; the SPA renders a typed form control from
 * `type` + `label` + `description` and PATCH-es the value through
 * the existing /settings/{key} endpoint.
 */
export interface SettingDef {
  key: string;
  type: SettingType;
  group: string;
  label: string;
  description: string;
  default?: unknown;
  placeholder?: string;
  /** Whether changing this requires a server restart to take effect. */
  reload: SettingReload;
  secret?: boolean;
  env_var?: string;
}

/** GET /api/_admin/site-info — public-readable site identity. */
export interface SiteInfo {
  name: string;
  url: string;
}

/** A catalog entry paired with the current persisted value. */
export interface SettingsCatalogEntry {
  def: SettingDef;
  value?: unknown;
  is_set: boolean;
}

/**
 * GET /api/_admin/settings/catalog response. `groups` is the ordered
 * list of group labels; `entries` carries every cataloged setting
 * with its current value; `unknown_keys` is the list of persisted
 * keys that don't appear in the catalog and aren't owned by another
 * dedicated screen (the General > Advanced fallback edits those).
 */
export interface SettingsCatalogResponse {
  groups: string[];
  entries: SettingsCatalogEntry[];
  unknown_keys: string[];
}
