# Railbase — Work Breakdown Structure

**Источник истины для функциональности**: `docs/00–23` (см. навигацию в §9).
**Этот файл**: задачи + статусы + зависимости. **Что именно отгружено в каждом milestone — в [progress.md](progress.md)** (архитектурные решения, deferred, тестовое покрытие).

Дата редакции: 2026-05-11. После каждого закрытого milestone обновлять статусы и переоценить критический путь.

---

## Легенда

| Маркер | Значение |
|---|---|
| ✅ | Сделано, в `main` |
| 🔄 | В работе сейчас |
| ⏳ | Следующая задача (готова к взятию) |
| 📋 | Запланирована (есть deps) |
| ⏸ | Заморожено (за пределами текущего фокуса) |
| 🔥 | На критическом пути |

**Оценки**: S = ≤1 день · M = 2-3 дня · L = 1 неделя · XL = 2+ недели · ?? = слишком рано. Все оценки — для одного разработчика, full-time.

---

## 0. Статус-снапшот

| Milestone | Статус | Кратко |
|---|---|---|
| v0.1 — bootable skeleton | ✅ | config / pool / embedded PG / migrations / probes / shutdown / logger / errors |
| v0.2 — schema DSL + scaffold | ✅ | 15 field types / registry / SQL DDL gen / snapshot diff / `railbase init` / migrate CLI |
| v0.3.1 — generic CRUD | ✅ | PB-compat `/api/collections/{name}/records[/{id}]` × 5 verbs |
| v0.3.2 — auth core | ✅ | AuthCollection / Argon2id / sessions HMAC-SHA-256 / 5 endpoints / middleware / lockout |
| v0.3.3 — filter parser + rules | ✅ | recursive-descent AST → parameterized SQL / `?filter` / `?sort` / Rules enforcement |
| v0.4 — tenant middleware | ✅ | `X-Tenant` header / conn-affinity / RLS + app-layer WHERE / forced tenant_id on insert |
| v0.5 — settings + admin CLI + eventbus | ✅ | `_settings`/`_admins` / Manager / in-process bus / admin/tenant/config CLI |
| v0.6 — eventbus LISTEN/NOTIFY + audit | ✅ | PGBridge cross-process / hash chain / `audit verify` / 6 events smoke |
| v0.7 — TS SDK gen | ✅ | types / zod / collections / auth / errors / `_meta.json` drift / `generate sdk --check` |
| v0.8 — embedded admin UI v0 | ✅ | A: adminapi (10 endpoints). B: React 19 + Vite + Tailwind 4 + wouter + 9-check UI smoke |
| **v0.9 — v0 verification gate** | ✅ | 5 gates: 5-min smoke / TodoMVC TS SDK / SDK drift / RLS / cross-compile 6 binaries |
| **v0 SHIP** 🚢 | ✅ | **2026-05-10** |
| v1.0 — Mailer core | ✅ | SMTP + Console driver / Markdown templates / 8 builtins / rate limit / `mailer test` CLI |
| v1.1 — Auth flows (record tokens) | ✅ | `_record_tokens` / 6 purposes / verify+reset+email-change+OTP+magic-link / anti-enum |
| **v1.1.1 — OAuth2 + OIDC** | ✅ | Google + GitHub + Apple + generic OIDC / state cookie / Apple secret CLI / 8 e2e |
| **v1.1.2 — TOTP 2FA + MFA** | ✅ | RFC 6238 hand-rolled / recovery codes / challenge state machine / 11 e2e |
| **v1.1.3 — WebAuthn / passkeys** | ✅ | Hand-rolled CBOR + COSE + ES256 / "none" attestation / discoverable flow / 8 e2e |
| **v1.1.4 — RBAC core (site + tenant)** | ✅ | 8 seed roles / SiteBypass + TenantBypass / lazy middleware / 9 CLI / 9 e2e |
| **v1.2.0 — Hooks core (goja JS)** | ✅ | 6 record events / fsnotify hot-reload <1s / watchdog / `$app.onRecord*` / 10 e2e |
| **v1.3.0 — Realtime (SSE)** | ✅ | `?topics=` wildcards / `record.changed` bus / per-sub queue cap 64 / 6 e2e |
| **v1.3.1 — Files (inline)** | ✅ | `_files` content-addressed FSDriver / multipart upload / signed URLs / `{name, url}` / 8 e2e |
| **v1.4.0 — Jobs queue + cron** | ✅ | `_jobs` SKIP LOCKED claim / hand-rolled 5-field cron / exp backoff / 3 cleanup builtins / 6 e2e |
| **v1.4.1 — Jobs operational tooling** | ✅ | Stuck-job recovery (lock-expired sweep wired в Cron tick) + 7-cmd jobs CLI + 7-cmd cron CLI / 10 e2e |
| **v1.4.2 — Domain types slice 1 (Communication)** | ✅ | `tel` (E.164 normalisation + CHECK) + `person_name` (JSONB structured) / SDK gen + zod / 11 unit + 8 e2e |
| **v1.4.3 — Zero-config UX** | ✅ | `./railbase serve` (no env, no init) auto-flips embedded PG + auto-generates `.secret`; ready-banner with URL; preflight bind-check fails fast w/ recovery hints; quieted OAuth/WebAuthn no-config noise; prod still demands `RAILBASE_DSN` + pre-existing `.secret` |
| **v1.4.4 — Domain types slice 2 (Identifiers)** | ✅ | `slug` (CHECK ^[a-z0-9]+(-[a-z0-9]+)*$ + `.From()` auto-derive on INSERT + ASCII-strip normaliser) + `sequential_code` (per-collection SEQUENCE + prefix + zero-pad, server-owned: INSERT default + UPDATE silently stripped) / SDK gen + zod / 8 unit + 9 e2e |
| **v1.4.5 — Domain types slice 3 (Content)** | ✅ | `color` (#RGB→#RRGGBB normaliser + CHECK lowercase hex) + `cron` (5-field parser-validated, whitespace-normalised) + `markdown` (TEXT + FTS opt-in) / SQL-reserved-keywords denylist (~120 entries) added to validator / SDK gen + zod / 13 unit + 8 e2e |
| **v1.4.6 — Domain types slice 4 (Money primitives)** | ✅ | `finance` (NUMERIC(15,4) default + Min/Max decimal CHECK + `::text` read-cast for string wire form) + `percentage` (NUMERIC(5,2) + 0..100 CHECK + .Range() override) / canonical-decimal validator (no float drift) / SDK gen string-typed (NEVER float; consumers use bignumber.js/decimal.js) / 9 unit + 9 e2e |
| **v1.4.7 — Domain types slice 5 (Locale)** | ✅ | `country` (ISO 3166-1 alpha-2, 249 codes embedded, uppercase normaliser, shape CHECK) + `timezone` (IANA validated via stdlib `time.LoadLocation`, same tz DB Postgres uses → `AT TIME ZONE` interop) / 5 unit + 8 e2e |
| **v1.4.8 — Domain types slice 6 (Banking)** | ✅ | `iban` (ISO 13616 + mod-97/ISO 7064 check digits + per-country length table 79 entries + space/hyphen strip normaliser) + `bic` (SWIFT 8/11-char shape, cross-checks country with ISO 3166-1) / 6 unit + 9 e2e |
| **v1.4.9 — Domain types slice 7 (Quantities)** | ✅ | `quantity` (JSONB `{value, unit}`, decimal-string value + per-field unit allow-list, `"10.5 kg"` string sugar) + `duration` (ISO 8601 grammar parser P[nY][nM][nD][T[nH][nM][nS]], uppercase normaliser, CHECK shape) / 4 unit + 9 e2e |
| **v1.4.10 — Domain types slice 8 (Workflow)** | ✅ | `status` (TEXT state-machine, first-state default, membership CHECK + REST, transitions advisory metadata for admin UI / hooks) + `priority` (SMALLINT 0..3) + `rating` (SMALLINT 1..5) / TS literal-union types for status / 6 unit + 10 e2e |
| **v1.4.11 — Domain types slice 9 (Hierarchies)** | ✅ | `tags` (TEXT[] + GIN index + dedup/trim/lowercase normaliser + per-tag length + cardinality CHECK) + `tree_path` (Postgres LTREE + GIST index + label-shape validator, `@>`/`<@`/`nlevel()` operators verified e2e) / 4 unit + 9 e2e |
| **v1.4.12 — Soft-delete** (§3.9.6) | ✅ | `.SoftDelete()` builder flag adds `deleted TIMESTAMPTZ NULL` system column + partial alive-index + LIST/VIEW filter `deleted IS NULL` (opt-in via `?includeDeleted=true`) + DELETE → soft-update + UPDATE refuses tombstones + `POST /restore` endpoint with UpdateRule auth / 10 e2e |
| **v1.4.13 — Batch ops** (§3.9.7) | ✅ | `POST /api/collections/{name}/batch` accepts up to 200 ops (create/update/delete) in one request; `atomic=true` wraps in pgx.Tx (rollback on first error) + `atomic=false` returns 207 Multi-Status with per-op result; reuses CRUD builders + rule enforcement + realtime events (buffered until commit in atomic mode) / 8 e2e |
| **v1.4.14 — Security headers + IP filter** (§3.9.5) | ✅ | `internal/security` ships HSTS/X-Frame-Options/X-Content-Type-Options/Referrer-Policy middleware (default-on in production) + `IPFilter` with atomic-swap CIDRs (live-updated via `settings.changed` bus) + X-Forwarded-For walking gated on trusted-proxies allow-list. 14 unit + 3 wiring tests. Details: [progress.md](progress.md). |
| **v1.5.0 — Outbound webhooks** (§3.9.2) | ✅ | Migration 0016 + dispatcher subscribed to `record.changed` bus → fan-out via `*`-segment glob → INSERT delivery row → enqueue `webhook_deliver` job (exp-backoff via v1.4.0 framework). HMAC-SHA-256 `t=<unix>,v1=<hex>` signing per docs/21. Anti-SSRF URL validator (rejects file://, loopback, RFC 1918/4193 in prod). 7-cmd CLI. 18 unit + 9 e2e. Details: [progress.md](progress.md). |
| **v1.5.1 — Cache primitive** (§3.9.4) | ✅ | Hand-rolled generic `internal/cache.Cache[K,V]` (no `hashicorp/golang-lru` dep): sharded LRU + per-entry TTL + `GetOrLoad` singleflight + atomic Stats + Clock injection. 13 unit tests under `-race`. Wiring to hot paths (rules AST / RBAC / filter AST) deferred to per-subsystem milestones. Details: [progress.md](progress.md). |
| **v1.5.2 — Streaming response helpers** (§3.9.8) | ✅ | `internal/stream`: `SSEWriter` (text/event-stream + heartbeat) / `JSONLWriter` (application/x-ndjson) / `ChunkedWriter` (raw binary). Auto-flush + `X-Accel-Buffering: no` + ctx-tied completion. 13 unit tests under `-race`. Details: [progress.md](progress.md). |
| **v1.5.3 — Notifications core** (§3.9.1) | ✅ | Migration 0017 (`_notifications` + `_notification_preferences` w/ partial unread-only index) + `internal/notifications.{Store,Service}` (Send → resolve channels via prefs → INSERT inapp + publish bus → optional email via mailer). `/api/notifications` REST: 7 endpoints w/ cross-user isolation. Push / quiet hours / digests deferred. 10 e2e. Details: [progress.md](progress.md). |
| **v1.5.4 — CSRF protection** (§3.9.5 tail) | ✅ | `security.CSRF` double-submit cookie pattern — `railbase_csrf` non-HttpOnly cookie + `X-CSRF-Token` header w/ constant-time compare. Bearer bypasses; cookie-auth state-changing requests require matching header → 403. Production-gated. `GET /api/csrf-token`. 11 unit tests. Details: [progress.md](progress.md). |
| **v1.5.5 — i18n core** (§3.9.3) | ✅ | `internal/i18n.Catalog` w/ 3-step lookup fallback (requested → base → default), `{name}` interp, Plural helper. Accept-Language Negotiator (q-quality). Middleware + `?lang=` override + `/api/i18n/{lang}` + `/api/i18n/locales`. `Locale.Dir()` for RTL. Embedded `en.json` + `ru.json`. ICU / `.Translatable()` / `_translations` deferred. 23 unit tests. Details: [progress.md](progress.md). |
| **v1.5.6 — Domain types slice 10 (Locale completion)** | ✅ | Closes §3.8 Locale at 5/5: `language` (ISO 639-1 alpha-2, 184 codes) + `locale` (BCP-47 `lang[-REGION]`, feeds v1.5.5 catalog) + `coordinates` (JSONB `{lat, lng}` w/ range CHECK; PostGIS deferred). 8 unit + 11 e2e. Details: [progress.md](progress.md). |
| **v1.5.7 — Domain types slice 11 (Communication completion)** | ✅ | Closes §3.8 Communication at 3/3: `address` (structured JSONB w/ street/street2/city/region/postal/country, ≥1 required, ISO 3166-1 country validated, sorted-key canonical encoding). 5 unit + 9 e2e. Details: [progress.md](progress.md). |
| **v1.5.8 — Domain types slice 12 (Identifiers completion)** | ✅ | Closes §3.8 Identifiers at 4/4: `tax_id` (per-country validator — EU VAT auto-detect from 2-letter prefix + `.Country("US/RU/CA/IN/BR/MX")` for non-prefix IDs) + `barcode` (auto-detect EAN-8/UPC-A/EAN-13 w/ GS1 mod-10 + `.Format("code128")` opt-out). 8 unit + 10 e2e. Details: [progress.md](progress.md). |
| **v1.5.9 — Domain types slice 13 (Money completion)** | ✅ | Closes §3.8 Money at 4/4: `currency` (ISO 4217 alpha-3, ~180 codes incl. precious metals) + `money_range` (JSONB `{min, max, currency}` w/ decimal-string bounds preserving fixed-point; hand-rolled `decimalLE` lex-compare avoiding float drift). 16 unit + 9 e2e. Details: [progress.md](progress.md). |
| **v1.5.10 — Domain types slice 14 (Quantities completion)** | ✅ | Closes §3.8 Quantities at 4/4: `date_range` (Postgres native DATERANGE w/ `@>`/`&&` operators) + `time_range` (JSONB w/ `::time` cast CHECK). 5 unit + 9 e2e. Details: [progress.md#v1510](progress.md#v1510--domain-types-slice-14-quantities-completion-38). |
| **v1.5.11 — Domain types slice 15 (Banking + Content completion)** | ✅ | Closes §3.8 Banking at 3/3 (`bank_account` JSONB w/ per-country schemas — US/GB/CA/AU/IN) AND Content at 4/4 (`qr_code` TEXT 1-4096 chars w/ `.Format(url/vcard/wifi/epc)` builder hint). 7 unit + 10 e2e. Details: [progress.md#v1511](progress.md#v1511--domain-types-slice-15-banking--content-completion-38). |
| **v1.5.12 — Hierarchies slice 16 (AdjacencyList + Ordered)** | ✅ | Collection-level builder modifiers (akin to SoftDelete). `.AdjacencyList()` adds `parent UUID NULL` + FK + recursive-CTE cycle prevention + `.MaxDepth(N)` default 64. `.Ordered()` adds `sort_index INTEGER` + auto-MAX+1 on INSERT. §3.8 Hierarchies 4/7. Details: [progress.md#v1512](progress.md#v1512--hierarchies-slice-16-adjacencylist--ordered-38). |
| **v1.6.0 — XLSX export (sync REST MVP)** | ✅ | First slice of §3.10. `internal/export.XLSXWriter` via excelize.StreamWriter (temp-file backed; 1M rows constant memory). `GET /api/collections/{name}/export.xlsx?filter=&sort=&columns=&sheet=` reuses ListRule + tenant + filter chain. 100k row cap (sync); auth-collections 403. 9 unit + 8 e2e. Details: [progress.md#v160](progress.md#v160--xlsx-export-sync-rest-mvp--310-kickoff). |
| **v1.6.1 — PDF export (programmatic, native gopdf)** | ✅ | Second slice of §3.10. `internal/export.PDFWriter` — A4 portrait via `signintech/gopdf` w/ embedded Roboto Regular TTF (pure-Go). Auto-pagination, rune-aware truncation. `GET /api/collections/{name}/export.pdf` mirrors XLSX RBAC + filter. 10k row cap (sync). 9 unit + 8 e2e. Details: [progress.md#v161](progress.md#v161--pdf-export-programmatic-native-gopdf--310-sub-task-28). |
| **v1.6.2 — Markdown → PDF** | ✅ | Third slice of §3.10. `export.RenderMarkdownToPDF(md, data)` via `gomarkdown/markdown` AST → gopdf primitives. Headings h1-h6, lists, code blocks, blockquotes, tables, HRs. Frontmatter parsing for title/header/footer/sheet. 12 unit. Details: [progress.md#v162](progress.md#v162--markdown--pdf--310-sub-task-38). |
| **v1.6.3 — `.Export()` schema-declarative builder** | ✅ | Fourth slice of §3.10. CollectionBuilder `.Export(ExportXLSX{...}, ExportPDF{...})` attaches per-format configs. Handler precedence: query param > config > default. Unknown columns 400 + allow-list. 6 builder + 9 handler unit + 8 e2e. Details: [progress.md#v163](progress.md#v163---export-schema-declarative-builder--310-sub-task-48). |
| **v1.6.4 — PDF Markdown templates** | ✅ | Fifth slice of §3.10. `PDFExportConfig.Template` references file in `pb_data/pdf_templates/`. `internal/export.PDFTemplates` loader w/ fsnotify hot-reload. Template ctx: `.Records`, `.Tenant`, `.Now`, `.Filter`. Helpers: `date`, `default`, `truncate`, `money`, `each`. 15 unit + 8 e2e. Details: [progress.md#v164](progress.md#v164--pdf-markdown-templates--310-sub-task-58). |
| **v1.6.5 — Async export via jobs** | ✅ | Sixth slice of §3.10. `POST /api/exports` enqueues `export_xlsx`/`export_pdf` jobs (202 + status_url). Migration 0018 adds `_exports`. Worker replays Principal + tenant in filter ctx. Async caps: 1M XLSX / 100k PDF. HMAC-signed file URLs w/ TTL. 3 unit + 10 e2e. Details: [progress.md#v165](progress.md#v165--async-export-via-jobs--310-sub-task-68). |
| **v1.6.6 — Export CLI** | ✅ | Seventh slice of §3.10. `railbase export collection <name> --format xlsx\|pdf [--filter --sort --columns --out --sheet --title --header --footer --include-deleted --template --template-dir --max-rows]`. Local DB via pgxpool; RBAC bypassed (operator surface). 12 unit + 8 e2e. Details: [progress.md#v166](progress.md#v166--export-cli--310-sub-task-78). |
| **v1.7.0 — PB-compat auth-methods discovery** | ✅ | `GET /api/collections/{name}/auth-methods` — PB 0.23+ discovery payload (`password` / `oauth2[]` / `otp` / `mfa` / `webauthn`). Public; enabled-ness derived from runtime Deps shape. TS SDK gains `.authMethods()`. §3.13 1/9. Details: [progress.md#v170](progress.md#v170--pb-compat-auth-methods-discovery--313-sub-task-19-kickoff). |
| **v1.7.1 — OpenAPI 3.1 spec generator** | ✅ | `railbase generate openapi [--check]` emits a static OpenAPI 3.1 spec from registered schema. New `internal/openapi` package (hand-rolled, no kin-openapi dep). 31 field types mapped to JSON Schema; `x-railbase.schemaHash` paired with sdkgen for drift detection. 19 unit tests. Details: [progress.md#v171](progress.md#v171--openapi-31-spec-generator--313-sub-task-49). |
| **v1.7.2 — Rate limiting (per-IP / per-user / per-tenant)** | ✅ | `internal/security.Limiter` — three-axis token-bucket rate limiter; sharded `map[string]*bucket` w/ background sweeper. `ParseRule("100/min")`. Live-updated via `settings.changed` bus. 429 + standard `X-RateLimit-*` + `Retry-After` headers. 16 unit tests. Details: [progress.md#v172](progress.md#v172--rate-limiting-per-ip--per-user--per-tenant--313-sub-task-79). |
| **v1.7.3 — API token manager (scoped, rotation, 30d TTL)** | ✅ | Service-to-service bearer credentials; wire format `rbat_<43-char base64url>` — prefix routes via auth middleware. HMAC-SHA-256 hash under master key; display-once contract. Migration 0019 + `internal/auth/apitoken.Store` + CLI (`token create/list/revoke/rotate`). 8 unit + 16 e2e. Details: [progress.md#v173](progress.md#v173--api-token-manager-scoped-rotation-30d-ttl--313-sub-task-89). |
| **v1.7.4 — Compat modes (strict / native / both)** | ✅ | `internal/compat` ships `Mode` enum + atomic `Resolver` + `GET /api/_compat-mode` discovery. Default `strict` (PB-compat v1 SHIP target). Live settings updates via bus. 13 unit tests. Details: [progress.md#v174](progress.md#v174--compat-modes-strict--native--both--313-sub-task-19). |
| **v1.7.5 — `cleanup_exports` cron + SSE resume tokens (parallel slices)** | ✅ | (a) `cleanup_exports` cron closes v1.6.5 deferred — sweeps `_exports` past `expires_at` + best-effort file rm. (b) SSE resume — 1000-event ring buffer + `SubscribeWithResume(sinceID)`; honours `Last-Event-ID` header + `?since=`; truncation marker. Details: [progress.md#v175](progress.md#v175--cleanup_exports-cron--sse-resume-tokens-parallel-sub-agent-slices). |
| **v1.7.6 — Logs-as-records + OpenAPI export/realtime/upload extensions (parallel slices)** | ✅ | (a) `internal/logs` — slog.Handler → `_logs` via batched CopyFrom; settings-gated (`logs.persist`); admin `GET /api/_admin/logs` + filters; `cleanup_logs` cron. Migration 0020. (b) OpenAPI extensions — per-collection `exportXlsx`/`exportPdf` ops, async export trio, SSE realtime declaration, multipart variants. 16 unit + 9 e2e + 13 new openapi unit. Details: [progress.md#v176](progress.md#v176--logs-as-records-3136--openapi-exportrealtimeupload-extensions-3134-polish--parallel-slices). |
| **v1.7.7 — Backup / restore + v1.7.6c exports audit rows + v1.7.7c JS hooks export (parallel slices)** | ✅ | (a) `internal/backup` — pure-Go pgx COPY dump/restore, gzipped tar w/ manifest.json; `SERIALIZABLE READ ONLY DEFERRABLE` snapshot; restore via `session_replication_role = 'replica'`. CLI `backup create/list/restore`. (b) exports audit rows on every code path. (c) JS hooks `$export.xlsx/.pdf/.pdfFromMarkdown`. Details: [progress.md#v177](progress.md#v177--backuprestore-manual-cli-3135--exports-audit-rows-3106-polish-v176c--js-hooks-export-3107-v177c--parallel-slices). |
| **v1.7.8 — PB schema import (§3.13.3)** | ✅ | `railbase import schema --from-pb <url>` fetches PB v0.22+ schema + emits Go source via builder; 13 field types translated; system+view collections skipped; dangling relations → TODO. §3.13 → **9/9** ✅. Details: [progress.md#v178](progress.md#v178--pb-schema-import-3133--last-313-item). |
| **v1.7.9 — Admin UI: Jobs + API tokens screens (parallel slices)** | ✅ | First §3.11 progress since v0.8: Jobs queue viewer (`/api/_admin/jobs` w/ status+kind filters, expandable rows) + API tokens manager (4 endpoints — list/create/revoke/rotate w/ display-once banner) + shared Pager component (`admin/src/layout/pager.tsx`). Details: [progress.md#v179](progress.md#v179--admin-ui-jobs-queue--api-tokens-manager-311-progress--parallel-slices). |
| **v1.7.10 — Verification gate audit (§3.14)** | ✅ | `docs/17-verification-audit.md` (~280 lines) maps 175+ docs/17 items to coverage (✅ 89 / 🔄 8 / 📋 17 / ⏸ 52 / ⏭ 9). Triages 📋 Small (≤1d, 9) / Medium (1-3d, 8). Details: [progress.md#v1710](progress.md#v1710--verification-gate-audit-314). |
| **v1.7.11 — Verification 📋 burn-down (#52 + #125) + Backups/Notifications admin UI (parallel slices)** | ✅ | §3.14 #52 realtime latency benchmark + §3.14 #125 `generate schema-json` + §3.11 Backups + Notifications admin UI. Details: [progress.md#v1711](progress.md#v1711--verification--burn-down-52--125--backupsnotifications-admin-ui-parallel-slices). |
| **v1.7.12 — 5-min smoke script (§3.14 #2)** | ✅ | `scripts/smoke-5min.sh` + `make smoke`. 13 HTTP probes across every RequireAdmin endpoint; binary-size ≤ 30 MB gate; runtime ~60-90s warm. Details: [progress.md#v1712](progress.md#v1712--5-min-smoke-script-3144-2). |
| **v1.7.13 — Audit retention auto-archive cron (§3.14 #93)** | ✅ | Migration 0021 adds `archived_at` to `_audit_log`; new `cleanup_audit_archive` cron (`"30 4 * * *"`, default retention = 0 / never; archive-not-delete preserves hash chain). Details: [progress.md#v1713](progress.md#v1713--audit-retention-auto-archive-cron-3144-93). |
| **v1.7.14 — Verification 📋 burn-down + Admin UI Trash (parallel slices)** | ✅ | §3.14 #10ax / #173 / audit filters + §3.11 Trash. Details: [progress.md#v1714](progress.md#v1714--verification--burn-down--admin-ui-trash-parallel-slices). |
| **v1.7.15 — `$app.realtime().publish()` hook binding + Admin UI Mailer template editor (parallel slices)** | ✅ | §3.14 #50 + §3.11 Mailer templates. Details: [progress.md#v1715](progress.md#v1715--apprealtimepublish-hook-binding--admin-ui-mailer-template-editor-parallel-slices). |
| **v1.7.16 — Pagination cursor stability test + Admin UI Realtime monitor (parallel slices)** | ✅ | §3.14 #13 + §3.11 Realtime monitor. Details: [progress.md#v1716](progress.md#v1716--pagination-cursor-stability-test--admin-ui-realtime-monitor-parallel-slices). |
| **v1.7.17 — `$app.routerAdd()` JS hook binding + Admin UI Webhooks management (parallel slices)** | ✅ | §3.14 #60 critical-path + §3.11 Webhooks management. Hook authors can now register custom HTTP endpoints (`$app.routerAdd("GET", "/api/hello/{name}", e => e.json(200, {...}))`) — chi-backed, atomic-swap hot-reload, path-param + query + headers + body, watchdog-protected. Webhooks admin UI: 7-endpoint CRUD + display-once secret + delivery timeline + dead-letter replay. Details: [progress.md#v1717](progress.md#v1717--approuteradd-js-hook-binding--admin-ui-webhooks-management-parallel-slices). |
| **v1.7.18 — `$app.cronAdd()` JS hook binding + Admin UI Command palette ⌘K (parallel slices)** | ✅ | §3.14 #61 critical-path + §3.11 Command palette. Hook authors register scheduled tasks via `$app.cronAdd("name", "0 4 * * *", () => {...})` — in-memory snapshot atomic-swap, minute-aligned ticker, watchdog-protected, throw-isolated (one bad job can't take down the loop). `internal/jobs.Schedule.Matches` exported for in-memory callers. Command palette ⌘K (Cmd/Ctrl+K) opens overlay; fuzzy substring search across 13 admin pages + N collections; ArrowUp/Down + Enter; Escape/click-outside closes; `⌘K` header badge dispatches synthetic keydown. Details: [progress.md#v1718](progress.md#v1718--appcronadd-js-hook-binding--admin-ui-command-palette-k-parallel-slices). |
| **v1.7.19 — CSV `railbase import data` + Admin UI bulk-ops & inline-edit on records (parallel slices)** | ✅ | §3.14 #144-145 + §3.11 records polish. `railbase import data <collection> --file <path.csv\|.csv.gz>` via Postgres `COPY FROM STDIN` — 50× faster than INSERT-per-row; header row required + unknown columns rejected pre-DB; auto-gzip; configurable delimiter/null/quote. 14 unit + 6 embed_pg subtests. Bulk-ops admin UI: checkbox column + sticky toolbar + bulk-delete via `POST /records/batch`; inline cell edit for text/number/email/url/bool/select. Details: [progress.md#v1719](progress.md#v1719--csv-railbase-import-data--admin-ui-bulk-ops--inline-edit-parallel-slices). |
| **v1.7.20 — Testing infrastructure (`testapp`) + Admin UI Hooks editor (parallel slices)** | ✅ | §3.14 #146-147 critical-path + §3.11 #123 (last admin-UI Medium). New `pkg/railbase/testapp` — `testapp.New(t, WithCollection(...))` boots embedded PG + sys migrations + REST + auth in one call; `AsAnonymous` / `AsUser(collection, email, password)` actors w/ chainable `Get/Post/Patch/Put/Delete` → `Status / StatusIn / JSON / DecodeJSON / Body / Header`. JSON fixtures via `LoadFixtures` (YAML deferred, justified in-file). 8 self-test subtests sharing one embedded PG. Hooks editor: `admin/src/screens/hooks.tsx` Monaco-based editor + file tree (`@monaco-editor/react`, ~+30 KB gzip); 4 admin-API endpoints `GET/PUT/DELETE /api/_admin/hooks/files[/{path}]` (path-traversal-safe); 800ms-debounced auto-save; 503 unavailable until `HooksDir` wired in `app.go` (v1.7.21+). 11 Go test functions / 19 sub-tests on backend; admin build clean. Details: [progress.md#v1720](progress.md#v1720--testing-infrastructure-testapp--admin-ui-hooks-editor-parallel-slices). |
| **v1.7.21 — Filter `BETWEEN` parser + IN coverage extension** | ✅ | §3.14 #17 critical-path (the last Small item). Closes the long-standing filter parser feature gap — `field BETWEEN lo AND hi` now parses + emits `(target BETWEEN $N AND $N)`. Lexer adds `tkBetween`; AST adds `Between{Target, Lo, Hi Node}`; parser branch in `parseCompare` requires literal `AND` keyword between bounds (case-insensitive; `&&` rejected to avoid ambiguity w/ logical conjunction). Magic-var bounds + identifier bounds + nested AND/OR composition all work. NOT BETWEEN deferred (no unary `!` until v1.x). IN coverage hardened: single-item, mixed-numeric, 50-element, case-insensitive, AND composition. 16 new tests in `between_test.go` under `-race`. Details: [progress.md#v1721](progress.md#v1721--filter-between-parser--in-coverage-extension). |
| **v1.7.22 — Performance benchmark suite + Admin UI Translations editor (parallel slices)** | ✅ | §3.14 #169 / #171 / #174 critical-path + §3.11 Translations editor (off-critical-path agent slice). (a) Benchmarks: realtime fan-out `BenchmarkPublishToDeliver_FanOut[_1k/_10k]` (M2: 43µs@100 / 259µs@1k / 2.3ms@10k subs, sub-linear scaling, 100× under the 100ms gate) + DB write throughput `BenchmarkThroughput_Insert_Serial/_Concurrent_8/_32/_CopyFrom` (21k serial rows/sec, 258k via COPY — 2.1×/25× docs/17 #171 target) + hook concurrency `BenchmarkDispatch_NoHandlers/_SingleHandler/_Concurrent_10/_100` (58ns no-op fast-path, 2.6µs single, 304k disp/sec under 100 goroutines) + `TestDispatch_Concurrent_NoDeadlock` invariant (100×50 dispatches in 30s budget). Bug fix bonus: `pkg/railbase/testapp.WithTB` no longer copies sync.Once (cleared go vet warning). (b) Translations editor: `admin/src/screens/i18n.tsx` w/ locale dropdown + coverage badges + per-key edit table; backend `GET/PUT/DELETE /api/_admin/i18n/{files,locales}[/{locale}]` reading from `Deps.I18nDir` (await app.go wiring, 503 until then); reference bundle = embedded `en.json`; missing-keys & translated-counts computed per locale (empty values = "not translated"). 10 Go test funcs / 19 subtests; admin build clean. Details: [progress.md#v1722](progress.md#v1722--performance-benchmark-suite--admin-ui-translations-editor-parallel-slices). |
| **v1.7.23 — RLS overhead + FS upload concurrency benches + Admin UI Health dashboard (parallel slices)** | ✅ | §3.14 #172 + #175 critical-path (closes the last two 📋 items — **v1 SHIP test-debt list is now empty**) + §3.11 Health/metrics dashboard (off-critical-path agent slice). (a) `internal/api/rest/rls_bench_test.go` — two collections (plain `bench_rls_off` + tenant-scoped `bench_rls_on` w/ RLS policy), seed 100k rows each via COPY in ~1.1s, `BenchmarkRLS_Select[_Range]_{NoRLS,WithRLS}` × 4 + `TestRLS_Overhead_Under5Pct` invariant — M2 measured **2.53% overhead, half the docs/17 5% budget**. (b) `internal/files/concurrent_bench_test.go` — `BenchmarkFSDriver_Put_{Serial,Concurrent_8,Concurrent_50}` (M2: 6.4k / 11k / 9.5k uploads/sec) + `TestFSDriver_Concurrent_NoCorruption` — 50 goroutines × 20 uploads = **1000 distinct files**, each verified by SHA256, zero orphan `.tmp` files (atomic-rename invariant). (c) Health/metrics dashboard: `admin/src/screens/health.tsx` polling `GET /api/_admin/health` every 5s; backend aggregates pool/runtime/jobs/audit/logs/realtime/backups/schema metrics via nil-guarded reads off existing Deps; `StartedAt` lazy-initialised on first request (no `app.go` wiring needed). Details: [progress.md#v1723](progress.md#v1723--rls-overhead--fs-upload-concurrency-benches--admin-ui-health-dashboard-parallel-slices). |
| **v1.7.24 — SHIP-gate technical acceptance (`.goreleaser.yml` + cross-compile + size budget) + Admin UI Cache inspector (parallel slices)** | ✅ | §3.14 #1 SHIP-gate task (closes the last ⏭ → ✅ promotion) + §3.11 Cache inspector. (a) Committed `.goreleaser.yml` (6-target matrix linux/darwin/windows × amd64/arm64, CGO_ENABLED=0, draft release, semver prerelease detection, embedded changelog grouping); `make cross-compile` + `make check-size` + `scripts/check-binary-size.sh` enforce the docs/17 #1 30 MB binary budget without requiring goreleaser locally. **Measured 2026-05-12: all 6 binaries under ceiling — largest 26.25 MB (Windows amd64), smallest 23.88 MB (linux arm64), 3.75 MB headroom**. Native binary `--version` + `--help` smoke-tested. (b) Cache inspector admin UI: `admin/src/screens/cache.tsx` polls `GET /api/_admin/cache` every 5s; backend `internal/api/adminapi/cache.go` walks the new `internal/cache.Registry` (a sync.Map-backed name→`StatsProvider` index; caches register on construction via `cache.Register(name, instance)`). Per-row Clear action via `POST /api/_admin/cache/{name}/clear` (audit-logged as `cache.cleared`). Wiring of cache.Register calls in `app.go` is a follow-up slice — screen shows empty-state until then. Details: [progress.md#v1724](progress.md#v1724--ship-gate-technical-acceptance--admin-ui-cache-inspector-parallel-slices). |
| **v1.7.25 — Admin-screen `app.go` wiring + Field renderer extensions for top-5 domain types (parallel slices)** | ✅ | Pre-SHIP polish — Claude wires `adminDeps.HooksDir` + `adminDeps.I18nDir` + `adminDeps.StartedAt` in `app.go` (3 fields, 6 lines of comments — invisible at binary size; cross-compile re-verified all 6 targets unchanged ±0.01 MB). Lifts the 503 empty-state from Hooks editor (v1.7.20b) + Translations editor (v1.7.22b) — both screens now read live files from `pb_data/hooks/` and `pb_data/i18n/`. Health dashboard (v1.7.23c) now reflects actual process-start time, not "first /health request" (lazy-init fallback retained for tests). Agent slice: `admin/src/fields/{tel,finance,currency,slug,country}.tsx` + registry — domain-aware cell renderers + edit affordances for 5 priority types from §3.8 (tel E.164 formatting, finance comma-grouped + 2-decimal, currency 3-letter badge, slug regex live-validation, country ISO 3166-1 badge). Details: [progress.md#v1725](progress.md#v1725--admin-screen-appgo-wiring--field-renderer-extensions-parallel-slices). |
| **v1.7.26 — Release artifacts (`docs/RELEASE_v1.md` + `.github/workflows/release.yml` + README status fix) + Field renderer extensions slice 2 (parallel slices)** | ✅ | Pre-SHIP operator-facing release artifacts (Claude critical-path): committed `docs/RELEASE_v1.md` — operator-audience summary of the v1 feature surface (stack / platform matrix / functionally-complete tracks / deferred items / performance baselines / PB upgrade path / install + smoke). README status line updated from "проектирование, реализация ещё не начата" → "v1 SHIP unblocked (2026-05-12)". `.github/workflows/release.yml` scaffold — tag-triggered (`v*.*.*`), checkout + go setup + sanity build + goreleaser action + size-budget re-enforcement via `scripts/check-binary-size.sh dist/`, uploads tarballs + checksums as workflow artifact. Agent slice: field renderers slice 2 — `admin/src/fields/{iban,bic,tax_id,barcode,color}.tsx` (IBAN mod-97 + 4-char grouping / BIC 8-or-11 regex + auto-uppercase / tax_id length sanity / barcode EAN-8 / UPC-A / EAN-13 GS1 mod-10 / color hex + native swatch). 138 → 143 modules, 413 → 421 KB JS / 115 → 116 KB gzip. Details: [progress.md#v1726](progress.md#v1726--release-artifacts--field-renderer-extensions-slice-2-parallel-slices). |
| **v1.7.27 — `.goreleaser.yml` project_name + `make verify-release` Makefile target + Field renderer extensions slice 3 (parallel slices)** | ✅ | Pre-SHIP polish — Claude pins `project_name: railbase` in `.goreleaser.yml` (avoided capital-R drift from directory-name default; matches binary + docs). New `make verify-release` Makefile target bundles `vet + test-race + cross-compile + check-size` as a one-shot pre-tag gate so operator runs ONE command before `git tag`. Agent slice: field renderers slice 3 — `admin/src/fields/{status,priority,rating,tags,tree_path}.tsx` (status pill w/ 5-hue palette derived from value; priority 4-button segmented toggle; rating 5-star hover-preview row w/ clear; tags pill-row + tag-input pattern w/ trim+lowercase+dedup; tree_path dot-segmented `›` display w/ LTREE label-shape validator). 143 → 148 modules, 421 → 429 KB JS / 116 → 118 KB gzip. **15 / ~25 domain types now have dedicated renderers.** `field.select_values` for status read directly off `FieldSpec` (declared on TS shape — no defensive cast needed unlike tax_id `field.country`). Details: [progress.md#v1727](progress.md#v1727--goreleaser-project_name--make-verify-release--field-renderer-extensions-slice-3-parallel-slices). |
| **v1.7.28 — CI binary-size gate per-PR + Field renderer extensions slice 4 (JSONB-structured) + `railbase test` CLI subcommand (parallel slices)** | ✅ | Pre-SHIP polish — Claude critical-path: added "Enforce 30 MB binary budget" step to `.github/workflows/ci.yml` cross-build matrix job. Previously the 30 MB gate ran ONLY at tag-time via `goreleaser`; now it runs on every PR via `bash scripts/check-binary-size.sh bin/` over all 6 cross-built binaries. Catches size regression at PR time, before it lands in main and bites a future release. Agent slice A: field renderers slice 4 — `admin/src/fields/{coordinates,address,bank_account,quantity,duration}.tsx` (JSONB-structured group: coordinates 4dp w/ N/S/E/W suffix + per-axis ±90/±180 validation, address 6-field stacked form w/ empty-strip + CountryInput delegation from slice 1, bank_account IBAN preference w/ 4-char grouping fallback to first-non-empty sub-field, quantity decimal-string-preserving `inputMode="decimal"` + per-field unit allow-list w/ 12-entry default, duration humanised top-two-component cell w/ ISO 8601 grammar regex). 148 → 153 modules, 429 → 442 KB JS / 118 → 121 KB gzip. **20 / ~25 domain types now have dedicated renderers.** Agent slice B: `railbase test` CLI subcommand (closes §3.12.1) — cobra wrapper over `go test` with flag composition (`--short`, `--race`, `--coverage[+--coverage-out]`, `--only`, `--timeout`, `--tags`, `--verbose`, `--integration`, `--embed-pg`); pure `buildTestArgv()` helper extracted for testability; 8 unit tests under `-race`; `PreRunE` auto-flips `--coverage` when `--coverage-out` is set alone. Watch / combined JS-coverage merge deferred per v1 SHIP scope (heavier integrations: fsnotify across .go+.js paths; coverage merging across runtimes). Details: [progress.md#v1728](progress.md#v1728--ci-binary-size-gate-per-pr--field-renderer-extensions-slice-4--railbase-test-cli-subcommand-parallel-slices). |
| **v1.7.29 — Field renderer extensions slice 5 (final batch — language/locale/cron/markdown/qr_code) + `make verify-release` end-to-end validation (parallel slices)** | ✅ | **Field-renderer track is now functionally complete (25 / ~25 §3.8 domain types).** Claude critical-path: ran `make verify-release` end-to-end (vet + test-race + cross-compile + check-size) as the operator pre-tag smoke. Confirmed all gates green and all 6 cross-compiled binaries under the 30 MB ceiling — largest 26.43 MB (Windows amd64), smallest 24.06 MB (linux arm64), **3.57 MB headroom** (down 0.18 MB from v1.7.24's 3.75 MB headroom, attributable to the +14 KB admin JS bundle from slices 1-4 being embedded via `admin/embed.go`). Operator can now run ONE command pre-`git tag` and trust the green output. Agent slice: field renderers slice 5 — `admin/src/fields/{language,locale,cron,markdown,qr_code}.tsx` (language uppercase 2-letter badge + embedded 20-entry common-language label map; locale BCP-47 regex w/ lang-lowercase + region-uppercase auto-normalisation; cron canonical single-space separators + subdued `· hourly/· daily/· weekly/· monthly` labels for the 4 common patterns; markdown ~30-line embedded MD→HTML preview covering H1-H3 / bold / italic / code / lists with HTML-escape-then-transform invariant — no markdown dep added; qr_code 40-char mono preview + textarea w/ `n/4096` counter + hint icon, no client-side QR lib to keep bundle lean). 153 → 158 modules, 442 → 449 KB JS / 121 → 123 KB gzip. **Field-renderer expansion complete — every §3.8 domain type now has a dedicated cell + edit affordance in the admin UI.** Details: [progress.md#v1729](progress.md#v1729--field-renderer-extensions-slice-5--make-verify-release-end-to-end-validation-parallel-slices). |
| **v1.7.30 — `send_email_async` job builtin + YAML fixtures in testapp (parallel slices, v1.x bonus)** | ✅ | First v1.x-bonus pair post v1 SHIP gates closed. Claude critical-path: `send_email_async` builtin in `internal/jobs/builtins.go` — `RegisterMailerBuiltins(reg, sender, log)` entry point with a `MailerSender` interface (decouples jobs from internal/mailer; mailer.Mailer satisfies it via `mailerSendAdapter` in `pkg/railbase/mailer_wiring.go`). Payload schema `{"template": "name", "to": [{"email", "name"}], "data": {...}}` mirrors mailer.SendTemplate. Validation rejects empty template + empty recipients synchronously (mailer NOT called); transient errors propagate to the retry engine; malformed payload errors NOT yet flagged permanent (deferred until jobs package exposes a public permanent-error sentinel). 6 unit tests (no embed_pg needed — pure unit test via fake mailer). Wired in `app.go` post-mailerSvc construction; nil-mailer case is a no-op (kind not registered → unknown-kind permanent failure if enqueued). Useful for: cron-scheduled emails, Go-side hooks fire-and-forget, future `$app.mailer.send()` JS binding (deferred). Agent slice: YAML fixtures in `pkg/railbase/testapp` (closes §3.12.2 "YAML deferred") — `LoadFixtures` now reads `__fixtures__/<name>.{json,yaml,yml}` with JSON-wins precedence; both-present case logs a `t.Logf` warning. Parse-YAML-then-marshal-JSON approach keeps the existing JSON pipeline byte-for-byte unchanged. `gopkg.in/yaml.v3` promoted from indirect → direct dep. 4 new YAML subtests added inside the existing shared-PG `TestTestApp` (13/13 subtests pass in ~47s). Details: [progress.md#v1730](progress.md#v1730--send_email_async-job-builtin--yaml-fixtures-in-testapp-parallel-slices-v1x-bonus). |
| **v1.7.31 — `scheduled_backup` + Mailer hooks dispatcher + Filter/RBAC cache wiring + `goja` stack-cap + `jobs.ErrPermanent` (5 parallel sub-slices, v1.x bonus burndown)** | ✅ | Aggressive 5-slice tick (3 agents + 2 Claude) burning down §3.7.5 + §3.1.6 + §3.9.4 + §3.4.3 + new v1.7.30 follow-up. (a) §3.7.5.2 **scheduled_backup builtin** — `RegisterBackupBuiltins(reg, runner, outDir, log)` with `BackupRunner` interface (parallel pattern to v1.7.30's `MailerSender`); payload `{"out_dir","retention_days"}` w/ retention sweep that walks `backup-*.tar.gz` (corrected from spec — actual on-disk pattern from v1.7.7) and removes old files; backup adapter in `pkg/railbase/backup_wiring.go`; NOT auto-enabled in `DefaultSchedules()` (backups touch entire DB — operators must `cron upsert` explicitly); 5 unit tests. (b) §3.1.6 **Mailer hooks dispatcher** — `mailer.before_send` (SYNC publish — subscribers can mutate `*Message` + set `Reject = true` to abort) + `mailer.after_send` (async observer fire); eventbus extended w/ `SubscribeSync` + `PublishSync` (additive, async path bit-for-bit unchanged); 6 new tests + buildMailer threads Bus. (c) §3.9.4 **Cache wiring slice 1** — `internal/cache.Cache[K,V]` now wired to TWO hot paths: filter AST cache (4096 entries, no TTL — AST is pure-functional; `Parse` split into `parseUncached` + cache-fronting wrapper w/ singleflight) and RBAC resolver cache (1024 entries, 5min TTL; composite `resolverKey` struct avoids `fmt.Sprintf` alloc per lookup); both registered via `cache.Register("filter.ast", ...)` + `cache.Register("rbac.resolver", ...)` so the v1.7.24b admin Cache inspector NOW renders live entries. RBAC bus-driven invalidation deferred — no `roles.changed` event exists yet; `PurgeResolverCache()` exported as a manual hook. 11 new tests. (d) §3.4.3 **goja stack-cap** (Claude) — partial closure of "memory limit deferred" — goja has no per-VM memory limit API, but recursive runaway is the most common runaway-memory pattern. `applyStackCap(vm, n)` w/ `DefaultMaxCallStackSize=128` applied to both VM construction sites (NewRuntime primary VM + loader.go per-reload VM); `Options.MaxCallStackSize` operator override; -1 sentinel disables. 4 tests under `-race` proving cap fires on `function f(n) { return f(n+1); }` and shallow recursion (fib(10)) still works. (e) **`jobs.ErrPermanent` sentinel** (Claude, follow-up from v1.7.30) — `var ErrPermanent = errors.New("jobs: permanent failure")`; `runner.process` now checks `errors.Is(err, ErrPermanent)` and forces terminal `failed` status via `Fail(j.MaxAttempts, j.MaxAttempts, ...)` (same shape as unknown-kind path); send_email_async malformed-payload + missing-template/recipients paths NOW wrap ErrPermanent; 2 new test assertions in `mailer_builtin_test.go` (`errors.Is(err, ErrPermanent)` on both paths). Combined: `go build ./...` + `go vet ./...` clean; full suite green under `-race` across 8 affected packages. Details: [progress.md#v1731](progress.md#v1731--scheduled_backup--mailer-hooks-dispatcher--filterrbac-cache-wiring--goja-stack-cap--jobserrpermanent-5-parallel-sub-slices). |
| **v1.7.32 — `audit_seal` Ed25519 chain + Cache wiring slice 2 (i18n + settings) + RBAC bus invalidation + cross-package `ErrPermanent` promotion + flaky-base64 fix (6 parallel sub-slices, v1.x bonus burndown)** | ✅ | Heaviest tick of the v1.x bonus burndown. 3 agents + 3 Claude sub-slices closing §3.7.5.3 + §3.9.4 (next 2 hot paths) + the RBAC invalidation deferred from v1.7.31c + the cross-package permanent-error chain deferred from v1.7.31e — plus a pre-existing flaky-test fix. (a) **§3.7.5.3 `audit_seal` Ed25519 chain** (agent, pulls part of §4.10 / v1.1 audit sealing into v1.x): migration 0022 adds `_audit_seals (range_start, range_end, row_count, chain_head BYTEA, signature BYTEA, public_key BYTEA)`. New `internal/audit/seal.go` w/ `Sealer.SealUnsealed(ctx)` walking `_audit_log` rows since the last seal's `range_end`, computing chain head from persisted `hash` column (no recomputation), `ed25519.Sign(privateKey, chainHead)`, INSERT into `_audit_seals` w/ inline `public_key` per-seal (so `seal-keygen --force` rotation doesn't invalidate historical seals). `Sealer.Verify` re-checks every seal's `ed25519.Verify` — pairs w/ existing `Writer.Verify` for "pre-seal chain tamper vs post-seal seal-table tamper" decomposition. Key file `<dataDir>/.audit_seal_key` (raw 64-byte ed25519.PrivateKey, chmod 0600); dev autocreate / production refuse. CLI `railbase audit seal-keygen [--force]`; existing `railbase audit verify` now also calls `Sealer.Verify` + reports seal status. `audit_seal` added to `DefaultSchedules()` at `0 5 * * *` (after all other cleanups). 18 audit tests pass under `-race -tags embed_pg` (5 new sealer e2e + 2 new key-loader unit + 11 pre-existing) + 4 new jobs unit tests. (b) **§3.9.4 Cache wiring slice 2: i18n bundles** (agent): `internal/i18n/cache.go` wraps the disk-read step of `LoadDir`/`LoadFS` w/ `cache.New[string, Bundle](64 entries, 30s TTL)`. Catalog's authoritative `map[Locale]Bundle` preserved — cache only sits between loaders + filesystem so fsnotify hot-reload still works. Path-based keys (not locale-named) because `LoadDir`/`LoadFS` may run against multiple roots per process. Registered as `"i18n.bundles"`. 4 new tests under `-race`. (c) **RBAC bus-driven invalidation** (agent, closes v1.7.31c "no roles.changed event" deferral): new `internal/rbac/events.go` ships 5 topic constants (`rbac.role_{granted,revoked,assigned,unassigned,deleted}`) + `RoleEvent` payload. `Store.Grant/Revoke/Assign/Unassign/DeleteRole` now publish via injected `*eventbus.Bus`; `NewStoreWithOptions` is the bus-aware constructor (existing `NewStore(pool)` retained for 9 CLI callers w/ nil-bus passthrough). `Assign` only publishes on new-row insert (idempotent already-exists path is silent). `CreateRole` deliberately skips publish — fresh role w/ no assignees can't affect cached `Resolved`. `cache.go` adds `SubscribeInvalidation(bus)` w/ one subscriber per topic + coarse `Purge()` on every event (reverse-mapping a Grant to specific `resolverKey` entries needs multi-query state tracking — not worth the complexity). 7 new tests + 21/21 pre-existing rbac tests still green. (d) **Settings cache as StatsProvider** (Claude): `internal/settings.Manager` now exposes `Stats()` (returns `cache.Stats` from atomic Hits/Misses + Size from map len under RLock) + `Clear()` (drops entries + zeros counters); registers itself as `"settings"` on construction. 4th production cache in the admin Cache inspector. 2 new unit tests. (e) **`mailer.ErrPermanent` → `jobs.ErrPermanent` cross-package promotion** (Claude, closes v1.7.31e deferral): `mailerSendAdapter.SendTemplate` now checks `errors.Is(err, mailer.ErrPermanent)` and returns `fmt.Errorf("%w (%w)", err, jobs.ErrPermanent)` — `errors.Is` walks the chain so this catches mailer-permanent regardless of fmt.Errorf wrap depth. (f) **`scheduled_backup` ErrPermanent wrapping** (Claude, follow-up): malformed-payload + missing-out_dir paths now wrap `ErrPermanent`; 2 new test assertions in `backup_builtin_test.go`. **Bonus**: fixed pre-existing flaky `TestStateTamperRejected` (~33% failure rate under `-count=3`) — `flipLast` was mutating the LAST base64url char which has 2 padding bits silently dropped by `RawURLEncoding.DecodeString`; for originals 'B'/'C'/'D' (useful bits = 0000) flipping to 'A' yielded identical decoded bytes → HMAC still verified → test FAILED. Fixed by flipping FIRST char (6 useful bits, no padding); 10/10 green under `-count=10`. Combined: `go build ./...` + `go vet ./...` clean; **full repo test suite zero failures under `-race -count=1`** (53 packages green). 4 production caches now registered: filter.ast / rbac.resolver / i18n.bundles / settings. Details: [progress.md#v1732](progress.md#v1732--audit_seal-ed25519-chain--cache-wiring-slice-2--rbac-bus-invalidation--cross-package-errpermanent--flaky-base64-fix-6-parallel-sub-slices). |
| **v1.7.33 — Anti-bot middleware + `orphan_reaper` builtin + `MockHookRuntime` JS hook test harness + gofakeit mock data generator (4 parallel sub-slices, v1.x bonus burndown continues)** | ✅ | 3 agents + 1 Claude sub-slice closing the four most-bounded v1.x deferrals. (a) **§3.9.5 Anti-bot middleware** (agent): `internal/security/antibot.go` ships `AntiBot` with atomic-swap config (HSTS / IPFilter / RateLimiter pattern) + honeypot field check (`form` POSTs only — JSON-API surface is SDK-driven, honeypots there would footgun legit clients) + UA sanity check (`bot`/`crawler`/`spider`/`curl/`/`python-requests`/`Go-http-client/` rejected on auth+oauth paths). Production-default ON, dev-default OFF (matches v1.4.14 `secHeaders`). 4 settings keys w/ env fallbacks + JSON-or-CSV parsing. `internal/server.Config` field + `r.Use()` between RateLimiter and routes. 12 unit tests. (b) **§3.6.13 `orphan_reaper` builtin** (agent): `RegisterFileBuiltins(reg, pool, filesDir, log)` adds `orphan_reaper` job kind. Two-direction sweep: (1) DB orphans — `_files` rows whose `collection`-named table is gone OR whose owner row is missing → delete row + best-effort `os.Remove(storage_key)`. Discovers candidate tables via `information_schema.tables` (cleanup_tree_integrity pattern). (2) FS orphans — files under `<filesDir>` not referenced by any `_files.path` row → `os.Remove`. Added to `DefaultSchedules()` at `"0 5 * * 0"` (Sunday 05:00 UTC — weekly; orphan accumulation is slow). Embed_pg tests. (c) **§3.12.8 `MockHookRuntime` JS hook test harness** (Claude): `pkg/railbase/testapp/hookmock.go` — `NewMockHookRuntime(t).WithHook(filename, source).FireHook(ctx, collection, event, record)`. Writes inline JS to `t.TempDir()` so `hooks.Runtime.Load()` parses it via the production loader path. NO StartWatcher (fsnotify isn't useful for unit tests + saves inotify FDs under parallel test packages). Operators can now unit-test individual hook files without spinning up TestApp / embedded PG / REST mux. 5 tests cover BeforeCreate mutation, throw-rejection, AfterCreate fire-and-forget contract (throws DO NOT propagate), multi-file load, no-handler noop. (d) **§3.12.5 gofakeit mock data generator** (agent): `pkg/railbase/testapp/mockdata.go` — `NewMockData(collection).Seed(s).Set(k,v).Generate(n) → []map[string]any` (+ `GenerateAndInsert(actor, n) → []string` IDs). 19 field types covered (email/tel/text/bool/number/date/url/select/person_name/country/currency/color/tags/finance/percentage/status/priority/rating + richtext/markdown via text generator); structured-JSONB + check-digit types (json/address/iban/bic/quantity/etc.) skipped with `.Set` as the override escape hatch. `github.com/brianvoe/gofakeit/v7` added as direct dep (was absent); test-binary +500KB, production binary +0 (testapp is `//go:build embed_pg`-tagged + only imported from `_test.go`). 4 new subtests in shared-PG harness. Combined: **full repo test suite zero failures under `-race -count=1`** (48 packages green). Details: [progress.md#v1733](progress.md#v1733--anti-bot-middleware--orphan_reaper--mockhookruntime--gofakeit-mock-data-4-parallel-sub-slices). |
| **v1.7.34 — WebSocket realtime + onAuth events + ICU plurals + Translatable fields + `webhook.delivered` topic + Notifications quiet-hours/digests (5 parallel sub-slices, v1.x bonus burndown round 3)** | ✅ | 3 agents + 2 Claude sub-slices closing §3.5.3 + §3.4.5 partial + §3.9.3 + §3.9.1 + a bonus webhook event topic. (a) **§3.5.3 WebSocket transport** (agent): `internal/realtime/ws.go` w/ `github.com/coder/websocket v1.8.14` (new direct dep). PB-compat newline-separated JSON frame protocol; `railbase.v1` subprotocol; ping/pong heartbeat 25s; dynamic `{action: subscribe|unsubscribe|ping}` frames let clients change topics WITHOUT reconnect (an SSE limitation v1.3.0 didn't solve). Resume via `{since: <event_id>}` cursor on first subscribe frame — mirrors v1.7.5b SSE resume. **Auth happens BEFORE upgrade** (401 + no `Upgrade: websocket` header on unauthenticated requests). Mounted at `/api/realtime/ws` alongside SSE at `/api/realtime`. Both transports coexist; clients pick. 6 tests green. (b) **§3.4.5 onAuth event publishing** (Claude, partial): `AuditHook` extended w/ `Bus *eventbus.Bus` + `WithBus(b) *AuditHook` constructor + `AuthEvent` payload. Topics `auth.{signin,signup,refresh,logout,lockout}` published alongside audit-row writes. nil-bus = no-op (test path). 4 tests green. onMailer ✅ already in v1.7.31b; onRequest still 📋 v1.2.x (needs middleware integration). (c) **§3.9.3 ICU plurals + `.Translatable()` field marker** (agent): `internal/i18n/plural.go` ships 5 rule families covering ~30 base languages: English (one/other), Russian-East-Slavic (one/few/many), Polish (one/few/many — exact-1 only), Arabic (zero/one/two/few/many/other), CJK (always-other), + rulePass fallback + `SetPluralRule(loc, fn)` operator override. **End-to-end `.Translatable()` field wiring**: `Translatable bool` on FieldSpec → Text/RichText/Markdown gain the chainable modifier → DDL emits `JSONB` w/ `jsonb_typeof = 'object'` CHECK + GIN index → validator rejects non-BCP-47 locale keys + non-string values + empty maps → REST handler picks request locale via `i18n.FromContext` w/ exact → base language → alphabetical-first fallback. `requestLocaleFor` short-circuits when no Translatable field exists so non-translatable collections see zero overhead. 37 new tests covering plural rules + builder + DDL + REST round-trip. NO `golang.org/x/text/feature/plural` dep (saves ~2 MB binary; hand-rolled rules cover the docs/14 v1 surface). (d) **`webhook.delivered` event topic** (Claude bonus): `internal/webhooks.HandlerDeps.Bus` field + `emitTerminal(DeliveryEvent)` helper. Publishes `webhook.delivered` on every terminal outcome (success/dead — retries are silent). Payload carries `DeliveryID / WebhookID / Webhook name / Event / Outcome / StatusCode / Attempt / Error`. 3 tests + wired in app.go. (e) **§3.9.1 Notifications quiet-hours + digests** (agent): migration 0023 adds `_notification_user_settings` (separate from `_notification_preferences` whose PK is `(user_id, kind, channel)` — per-row quiet-hours would be 3-dimensional nonsense; user-keyed table is the right shape) + `digested_at` on `_notifications`. Quiet-hours: `quiet_hours_{start,end,tz}` w/ wrap-midnight handling; in-window sends → `_notification_deferred` row + flush via `flush_deferred_notifications` cron (every 5min). Digest: `digest_mode ∈ {off, daily, weekly}` + `digest_hour` + `digest_dow`; on cron fire, group by user_id, send ONE templated email via mailer w/ `digest_summary.md`. **Quiet-hours wins precedence** when user has both ("don't disturb me right now" is a stronger contract than digest scheduling). `priority = 'urgent'` bypasses both (operator override). `RegisterNotificationBuiltins` matches the v1.7.30-33 per-service registrar pattern. 13 tests (6 PG-backed + 7 pure helpers) all green under `-tags embed_pg`. Combined: **full repo test suite zero failures under `-race -count=1`** (48 packages green + 13 notification embed_pg tests pass in 269s). Details: [progress.md#v1734](progress.md#v1734--websocket-realtime--onauth-events--icu-plurals--translatable-fields--webhookdelivered--quiet-hoursdigests-5-parallel-sub-slices). |
| **v1.7.35 — Admin notifications prefs editor + `_email_events` table + `railbase coverage` CLI + shared-PG TestMain hardening (4 parallel sub-slices, v1.x bonus burndown round 4)** | ✅ | 3 agents + 1 Claude sub-slice closing §3.9.1 admin-side prefs editor + §3.1.4 `_email_events` + §3.12.7 combined coverage + a TestMain `os.Exit` defers leak fix discovered en route. (a) **Admin notifications prefs editor** (agent): `internal/api/adminapi/notifications_prefs.go` (597 lines) + test (323 lines, 7 funcs / 25 subtests under `-race`) ships 3 admin-authenticated endpoints — `GET /api/_admin/notifications/users` (paginated user list w/ email substring filter, 300ms debounce), `GET /api/_admin/notifications/users/{user_id}/prefs` (prefs[] + settings; 404 only when neither table has a row), `PUT /api/_admin/notifications/users/{user_id}/prefs` (UPSERTs both tables in one round-trip + emits `notifications.admin_prefs_changed` audit event). Admin screen `admin/src/screens/notifications-prefs.tsx` (609 lines) is master-detail: left pane paginated user list w/ filter, right pane two cards (per-kind channel toggles + quiet-hours/digest settings form) saved together. Routes wired in `app.tsx` + nested sidebar link under Notifications + command palette entry. NO `app.go` change — `d.Pool` already wired. (b) **`_email_events` table + mailer instrumentation §3.1.4** (agent): migration 0024 adds `_email_events (id, occurred_at, event, driver, message_id, recipient, subject, template, bounce_type, error_code, error_message, metadata JSONB)`. `internal/mailer/events_store.go` ships `EventStore.{Write,ListRecent,ListByRecipient}` + `recordSendOutcome(...)` method on `*Mailer`. ONE row per (Send, recipient) — To+CC+BCC fanned out via `flattenRecipients` so the operator-question "did alice@ get her reset email?" resolves with a single row, not an inner-join. `mailer.Options.EventStore` lets the wiring pass nil for legacy/test paths. `buildMailer` in `pkg/railbase/mailer_wiring.go` now takes `*pgxpool.Pool` + lazy-constructs the store; 1-line patch in `app.go` threads `p.Pool`. CLI extended: `railbase mailer events list [--recipient EMAIL] [--limit N]`. 5 new embed_pg tests; eventbus topics from v1.7.31b remain the in-process observer path — EventStore is the durable record. (c) **`railbase coverage` CLI §3.12.7** (agent): `pkg/railbase/cli/coverage.go` (~327 lines) + `coverage_test.go` (12 tests) ship `railbase coverage [--go PATH] [--js PATH] [--out PATH]` — hand-rolled Go coverprofile parser (5-field line format) + c8 JSON parser (Vitest's `coverage-final.json`) + `html/template`-rendered single-file HTML w/ inline CSS (~30 lines, no JS, no external assets). Either source optional; at least one must exist or friendly error. No new deps — avoided `golang.org/x/tools/cover` by hand-rolling the ~20-line state machine. (d) **Shared-PG TestMain leak fix** (Claude): both `internal/notifications/quiet_hours_test.go` (v1.7.35 refactor) AND `internal/mailer/events_store_test.go` (v1.7.35b agent) used `os.Exit(m.Run())` at the top of TestMain — but `os.Exit` BYPASSES deferred calls in its own frame, so the embedded postgres `stopPG()` never fired, leaking PG past the test process + binding port 54329 forever, breaking the next embed_pg run. Fix: wrap `m.Run()` in `runTests(m) int` helper so its defers (pool.Close, stopPG, RemoveAll) flush BEFORE caller's `os.Exit`. Identical pattern applied to both files; `lsof -i :54329` post-run shows no leftover. **Combined**: `go build ./...` + `go vet ./...` clean; full repo `go test -race -count=1 ./...` zero failures (48 packages green); per-package embed_pg sweeps green (`notifications` 49s / `mailer` 44.7s / `adminapi` 45.9s). Details: [progress.md#v1735](progress.md#v1735--admin-notifications-prefs-editor--_email_events-table--railbase-coverage-cli--shared-pg-testmain-hardening-4-parallel-sub-slices). |
| **v1.7.36 — `_email_events` admin browser + PB SDK strict-mode SSE handshake (2 parallel agents, v1.x bonus burndown round 5)** | ✅ | 2 agents closing v1.7.35b "_email_events admin UI deferred" follow-up + §3.5.9 "PB SDK drop-in compat in strict mode" (the last 📋 in the §3.5 realtime track). (a) **`_email_events` admin browser screen** (agent): `internal/api/adminapi/email_events.go` ships `GET /api/_admin/email-events` with 8 query-param filters (page/perPage/recipient/event/template/bounce_type/since/until). Added `mailer.EventStore.{ListFilter, List, Count, buildWhere}` — these go beyond the slice-(b) v1.7.35 ListRecent/ListByRecipient, so future CLI surfaces + plugins can reuse the same filter shape. Admin screen `admin/src/screens/email-events.tsx` pattern-matches `logs.tsx`: filter bar (debounced 300ms recipient + dropdowns + datetime-locals) + Pager + color-coded event pills (sent=green / failed=red / bounced=yellow / opened=blue / clicked=indigo) + click-to-expand inline detail w/ error_message + metadata JSON pretty-print. **Hardening bonus**: agent noticed `trash_e2e_test.go` was booting its own embedded PG on port 54329 (would have collided w/ the new shared TestMain pool) and refactored it to share the package-level pool — same pattern as v1.7.35d's `os.Exit(runTests(m))` shape. `since`/`until` malformed return **400 validation** (typed envelope) rather than silently drop like `logs` did — the spec called this out. 6 top-level Test funcs + 4 subtests; embed_pg suite 44.8s. Bundle: 460.58 KB → 467.99 KB (+7.41 KB / 1.2% gzip). (b) **§3.5.9 PB SDK drop-in compat (SSE)** (agent): `internal/realtime/pb_compat.go` (NEW) ships `ClientRegistry` (UUIDv7 clientId↔subscription map), `toPBShape` (RecordEvent → `{action, record}` re-marshaller), `SubscribeHandler` (POST `/api/realtime` body `{clientId, subscriptions}`), `writePBConnectFrame`, `newClientID`. SSE handler gated on `compat.From(ctx) == ModeStrict && registry != nil`: emits `event: PB_CONNECT` pre-frame w/ `id: <clientId>` per PB v0.23 spec, allows empty `?topics=` (PB clients subscribe via POST), re-shapes both replay + live event payloads. Native + both modes are bit-for-bit unchanged. 3 new tests in `pb_compat_test.go`: full SDK handshake under `-race` (open SSE → read PB_CONNECT → POST subscribe → publish → assert PB-shape) + unknown-clientId 404 + native-mode regression. Touched `pkg/railbase/app.go` (surgical wiring: declare `realtimeClients := realtime.NewClientRegistry()` + thread into `realtime.Handler` + mount `r.Post("/api/realtime", realtime.SubscribeHandler(...))`). Updated 3 existing test call-sites to pass `nil` for the new registry parameter (preserves native behaviour). **Known v1.x-bonus gaps** (not blockers, all flagged for future slices): WS transport still emits native payload shape under strict mode (~30-45min to close), `?expand=` on subscribe bodies (1-2 days — bigger feature), per-record `<collection>/<recordId>` topic format (~2-3h), `?token=` query-param auth for raw EventSource (~1h). The PB SDK SSE handshake itself — `PB_CONNECT` pre-frame + POST subscribe + clientId routing + `{action, record}` shape — **is closed**. Combined: `go build ./...` + `go vet ./...` clean; full repo `go test -race -count=1` zero failures (48 packages green); embed_pg sweep on touched packages green (adminapi 44.8s / realtime 3.1s). Details: [progress.md#v1736](progress.md#v1736--_email_events-admin-browser--pb-sdk-strict-mode-sse-handshake-2-parallel-agents). |
| **v1.7.37 — Realtime PB-compat follow-ups: WS strict-mode payload reshape + per-record `<collection>/<recordId>` topics + `?token=` query-param auth + auth/middleware shared-PG TestMain (4 sub-slices)** | ✅ | 2 agents + 2 Claude sub-slices closing the three v1.7.36b-flagged realtime follow-ups + a TestMain leak/timeout fix discovered en route. (a) **WS strict-mode payload reshape** (Claude): `internal/realtime/ws.go::writeRecordFrame` gained a `pbCompat bool` parameter; when set, the broker's native RecordEvent is re-marshalled via `toPBShape` (the same helper SSE uses since v1.7.36b). `WSHandler` signature gained `registry *ClientRegistry` (mirrors SSE) — nil registry keeps native shape forever (the v1.3.0 contract), non-nil + `compat.ModeStrict` activates PB-compat. Why the registry-nil gate matters: `compat.From` defaults to `ModeStrict` for unstamped contexts (safe-default policy inherited from the resolver) — without the registry gate, tests that don't run the compat middleware would silently reshape. Updated `app.go` wiring (`r.Get("/api/realtime/ws", realtime.WSHandler(realtimeBroker, realtimeClients, ...))`) + 2 existing test call-sites (`wsTestServer` + `TestWS_Resume`) to pass nil. (b) **Per-record `<collection>/<recordId>` topic format** (agent): Option A (dual-fan at publish time) with a critical refinement — `realtime.go::fanOut` rewritten so the per-record fan reuses the SAME broker event id and the SAME ring buffer slot as the primary; only the delivery topic differs. This avoids doubling resume-buffer pressure + event-id usage AND dedups so wildcard subscribers (`posts/*`) get one frame per publish, not two. Added `isVerbTopic` helper + factored backpressure into `enqueueOrDrop`. 5 new tests in `per_record_topic_test.go`: dual fan-out to both topic shapes, no-fan-out without id, no-infinite-fan-out (depth-1 termination + dedup), record-id-equals-verb collision guard, tenant-filter inheritance. **Bug-hunt finding (separate slice)**: agent flagged a latent `Unsubscribe` race — `Broker.Unsubscribe` calls `close(sub.queue)` while `fanOut` may still be sending on it from another goroutine. Final design sidesteps the race, but the underlying bug remains; documented as a follow-up. (c) **`?token=` query-param auth for raw EventSource** (agent): `internal/auth/middleware/middleware.go` extended w/ variadic `Option` pattern + `WithQueryParamFallback(name string)` — existing 2-arg / 3-arg call-sites compile unchanged (backward-compatible). `extractToken` refactored into `extractTokenWithOpts(r, options)`; gating helper `queryParamFallbackAllowed(r)` enforces `r.Method == GET && compat.From(ctx) == ModeStrict`. Bearer + Cookie still beat the query param (header precedence preserved). Query-extracted token flows through the SAME `sessions.Lookup` (or `rbat_` → `apiStore.Authenticate`) path — no shortcut. 11 unit tests in `query_token_test.go` + 5 e2e in `query_token_e2e_test.go` (under `//go:build embed_pg`, full session.Store + embedded PG pipeline). app.go wired: `authmw.NewWithAPI(sessions, apiTokens, a.log, authmw.WithQueryParamFallback("token"))`. Combined: `go build ./...` + `go vet ./...` clean; full repo `go test -race -count=1` zero failures (48 packages green). Details: [progress.md#v1737](progress.md#v1737--realtime-pb-compat-follow-ups-3-parallel-sub-slices). |
| **v1.7.38 — Plan-completion push: Unsubscribe race + flake fix + digest preview + `Deps.Mailer` wire + prefs DELETE + onRequest + Go-side hooks + admin hook test panel + auth origins (9 sub-slices, "доведи до 100%" round)** | ✅ | 5 agents + 4 Claude sub-slices burning through the largest single-tick round of the v1.x bonus burndown. Closes §3.4.5 onRequest, §3.4.10 Go-side typed hooks, §3.4.11 admin-UI hook test panel, §3.2.10 auth origins + new-device email, §3.9.1 digest preview + admin prefs DELETE, plus a latent realtime race + a flaky-test fix. (a) **Unsubscribe race fix** (Claude, flagged by v1.7.36b + v1.7.37b agents independently): `Subscription` gained a `done chan struct{}` channel; `Unsubscribe` closes `done` once (via `closed.CompareAndSwap`) instead of `close(sub.queue)` — the queue is NEVER closed. `enqueueOrDrop` selects on `done` so in-flight sends past the `s.Closed()` check drop the event instead of panicking. SSE + WS readers also select on `sub.Done()` to exit cleanly. 4 regression tests including `TestBroker_UnsubscribeRace_QueueFullWhileTeardown` — passes under `-race -count=20`. (b) **Flaky `TestSignURL_RejectsTamper` fix** (Claude): single-char tamper at first byte (hex char 0-9a-f) had ~1/16 chance of collision when original first char was '0' and replacement was '0' → identical bytes, HMAC verified, test failed. Fixed by picking a deterministic-differ char. Pattern matches v1.7.32 oauth base64-padding fix. (c) **Revert socket auto-pick + `DetectLocalPostgresSockets()`** (Claude, operator pushback): a v1.7.38 mid-sketch that auto-built `postgres://$USER@/railbase?host=/tmp&sslmode=disable` was reverted — hard-codes 2 operator-owned decisions (db name + auth identity). Replaced with `LocalPostgresSocket{Dir, Path, Distro}` + `DetectLocalPostgresSockets() []LocalPostgresSocket` — pure observation surfaced to the v1.7.39 setup wizard. (d) **Wire `Deps.Mailer`** (Claude): single line in `app.go` line 355 — `adminDeps.Mailer = notificationsMailerAdapter{mailerSvc}` so the v1.7.38 digest preview endpoint goes from 503-stubbed to live. (e) **Prefs DELETE endpoint** (Claude): `DELETE /api/_admin/notifications/users/{user_id}/prefs` resets both tables in one round-trip. `notifications.Store` gained `DeleteAllPreferences(ctx, userID) (count, err)` + `DeleteUserSettings(ctx, userID) (bool, err)`. 404 only when both tables are already empty. Audit event `notifications.admin_prefs_reset` with full before-snapshot. (f) **`chi` middleware-order fix** (Claude, caught at boot): app.go's main Group had `r.Use(...)` calls AFTER `r.Get(...)` registrations — chi v5.1.0 panics "all middlewares must be defined before routes on a mux". Public discovery routes (`/api/csrf-token`, `/api/i18n/locales`, `/api/i18n/{lang}`, `/api/_compat-mode`) moved AFTER the full middleware chain — they ride the auth/tenant/rbac stack but never call `rbac.Require` so anonymous access still works. Boot panic gone; first real `./railbase serve` smoke now hits "http server listening" cleanly. (g) **Digest preview button** (agent, §3.9.1 follow-up): `POST /api/_admin/notifications/users/{user_id}/digest-preview` synthesises a sample digest email (real queued items OR 3 fakes for empty users) + sends via `digest_preview.md` template w/ `[Preview]` subject prefix. Admin UI: `DigestPreviewControls` under digest card. 5 e2e tests. (h) **§3.4.5 `$app.onRequest` middleware hook** (agent): `internal/hooks/on_request.go` ships SYNC dispatcher; handlers call `e.next()` or `e.abort(status, body)`. Per-handler 500ms watchdog via existing `goja.Interrupt` infra (tighter than the 5s on*-event default — onRequest fires on every request). Atomic-pointer swap on hot-reload. `/_/*` admin-UI paths bypass entirely (fast-path string-prefix). 12 tests. Wired at app.go line 767 between compat.Middleware + authmw. (i) **§3.4.10 Go-side typed hooks** (agent): `internal/hooks/go_hooks.go` ships `GoHooks` with `OnRecord{Before,After}{Create,Update,Delete}` (collection="" = wildcard) + `ErrReject` sentinel + sync Before / async After w/ per-handler panic recovery. JS dispatcher gained a parallel Go phase: wildcards-first then per-collection in registration order. Integration tests confirm Go-hook ErrReject short-circuits BOTH Go AND JS phases. 9 tests. `App.GoHooks() *hooks.GoHooks` lazy-init getter. (j) **§3.4.11 Admin UI hook test panel** (agent): `POST /api/_admin/hooks/test-run` builds a FRESH isolated runtime per request (no DB writes possible — `$app.dao` is undefined; realtime Bus is nil → silent no-op). Console capture via custom `slog.Handler` routing `hook console` records into a per-request buffer. Frontend: collapsible `<TestPanel/>` under Monaco editor. 6 tests. Bundle delta +8 KB. (k) **§3.2.10 Auth origins + new-device email** (agent): migration 0025 adds `_auth_origins(user_id, collection, ip_class, ua_hash, first_seen, last_seen, remembered_until)` with `UNIQUE(user_id, collection, ip_class, ua_hash)`. `IPClass(ip)` normalises IPv4→/24, IPv6→/48 (catches mobile NAT churn as same-origin). `UAHash(ua)` sha256 of version-stripped User-Agent (Chrome 120↔121 silent updates don't re-notify). `Touch(ctx, ...) (isNew bool, ...)` UPSERT via Postgres `xmax=0` trick. On `isNew=true` signin handler enqueues `send_email_async` w/ `new_device_signin.md` template. CLI: `railbase auth origins list/delete`. 11 tests (9 unit/PG + 2 e2e). Combined: `go build ./...` + `go vet ./...` clean; full repo `go test -race -count=1` zero failures; demo binary boots cleanly on brew PG (`postgres://ali@/railbase?host=/tmp`). Details: [progress.md#v1738](progress.md#v1738--plan-completion-push-9-sub-slices). |
| **v1.7.39 — First-run DB setup wizard (extends v0.8 Bootstrap)** | ✅ | 1 agent + 1 Claude wiring slice closing the "Railbase is a universal backend — operators of clones shouldn't have boot guess db-name+user" gap. (a) **`internal/api/adminapi/setup_db.go`** (agent): 3 PUBLIC endpoints mounted in `/api/_admin/_setup/*` BEFORE the RequireAdmin sub-group — operator can't be admin until DB is configured. `GET _setup/detect` returns `{configured, current_mode, sockets:[{dir,path,distro}], suggested_username}`. `POST _setup/probe-db` builds a DSN from `{driver, socket_dir, username, password, database, sslmode}` (or `external_dsn` for the external branch) + opens pgx.Connect w/ 5s timeout + returns `{ok, dsn, version, db_exists, can_create_db, error?, hint?}`. `POST _setup/save-db` persists DSN to `<DataDir>/.dsn` (chmod 0600) + optionally runs `CREATE DATABASE <quoted>` via admin connection to `postgres` db w/ `pgx.Identifier.Sanitize()` injection-safety; responds `{ok, restart_required: true}`. `setupProbeHint(errMsg)` maps libpq errors to one-sentence operator-actionable hints (connection refused / auth failed / db missing / TLS / timeout). (b) **`config.readPersistedDSN`** (agent): `Load()` consults `<DataDir>/.dsn` BEFORE the embedded-fallback policy + AFTER env vars; env still wins, so `RAILBASE_DSN` overrides persisted file. Silent failure on read errors — permissions glitch doesn't brick boot. 3 new config tests cover persisted-file pickup, env-overrides, absent-file-falls-through-to-embedded. (c) **Frontend wizard** (agent): `admin/src/screens/bootstrap.tsx` refactored from 1-step to 2-step (`DatabaseStep` → `AdminStep`). Database step: detect-on-mount + driver radio (Local socket / External DSN / Embedded) + per-driver fields + inline Probe + Save controls. Save tells operator to Ctrl-C + restart; next boot reads `.dsn` + bypasses embedded. 18 new tests (12 default + 3 embed_pg live-PG + 3 config). (d) **Mount wiring** (Claude, 1 line): `d.mountSetupDB(r)` added in `adminapi.Mount` immediately after `_bootstrap` + before the RequireAdmin Group. Verified live: `GET /api/_admin/_setup/detect` returns the detected brew-PG socket `{dir:"/tmp", path:"/tmp/.s.PGSQL.5432", distro:"homebrew"}` w/ `suggested_username: "ali"`. Bundle: 470 → 485.32 KB (+15 KB, gzip 129.98 KB). Combined: `go build ./...` + `go vet ./...` clean; full repo race tests green; demo binary boots cleanly on brew PG (`/tmp/railbase-demo/` data dir + admin UI at http://localhost:8090/_/). Details: [progress.md#v1739](progress.md#v1739--first-run-db-setup-wizard). |

**Отгружено**: v0 (~17.9k LOC Go + ~1.5k LOC TS) + v1 milestones (~21k+ LOC Go). **48 пакетов с тестами**, все green под `-race` (53 пакета в репо, включая no-test directories). v0 released; v1.0–v1.7.39 shipped through autonomous loop slices — see milestone table above and [progress.md](progress.md) for per-slice deltas. **Post v1.7.39 honest completion of plan.md v1 scope: ~99.8%** (~155 of ~155 currently-scoped v1 line items shipped). v1.7.38-39 closed §3.2.10 Auth origins, §3.4.5 onRequest, §3.4.10 Go-side typed hooks, §3.4.11 hook test panel + the first-run DB setup wizard (new architecture, "universal backend, не зашивай db-name+user в boot"). §3.5.9 PB-SDK realtime compat closes 8/9 sub-items. Remaining `📋` markers are explicit v1.x-bonus / v1.1+ / v1.2+ / dependency-blocked items the original roadmap put out of v1 scope. Recently closed in v1.7.31-v1.7.35 bonus burndown (**~26 sub-slices in single autonomous session**): §3.1.4 `_email_events` durable bounce/delivery store, §3.1.6 Mailer hooks dispatcher, §3.4.3 goja stack-cap (partial — goja has no true mem limit), §3.4.5 onAuth event topics (partial — onMailer ✅, onAuth ✅; onRequest still 📋), §3.5.3 WebSocket transport alongside SSE, §3.6.13 orphan_reaper, §3.7.5.2 scheduled_backup, §3.7.5.3 audit_seal Ed25519 chain (pulls part of §4.10/v1.1 into v1.x), §3.9.1 quiet-hours + digests + admin-side prefs editor, §3.9.3 ICU plurals + `.Translatable()` end-to-end, §3.9.4 Cache wiring slices 1+2 (4 caches: filter.ast / rbac.resolver / i18n.bundles / settings), §3.9.5 Anti-bot middleware (honeypot+UA), §3.12.1 `railbase test` CLI, §3.12.2 YAML fixtures, §3.12.5 gofakeit mock data, §3.12.7 `railbase coverage` combined Go+JS HTML report, §3.12.8 MockHookRuntime, plus `jobs.ErrPermanent` sentinel + mailer→jobs cross-package permanent-error promotion + RBAC bus-driven invalidation + `webhook.delivered` topic + TestMain os.Exit-defers hardening + pre-existing oauth flaky-base64-padding test fix.

§3.8 Domain types 8/9 groups fully closed (Hierarchies 4/7); **field-renderer admin UI track functionally complete at 25/~25 types** (slices 1-5 v1.7.25b through v1.7.29b). §3.9 Notifications + webhooks + i18n + cache + soft-delete + CSRF + security headers — functionally complete. §3.10 Document generation 8/8 ✅. §3.13 PB-compat track 9/9 ✅ — feature parity complete. §3.5.x SSE resume tokens shipped (WS deferred to v1.x). **§3.11 Admin UI track at 17/20 listed screens shipped** (~85%): Audit list+filters v0.8/v1.7.14 / Logs v1.7.6 / Jobs v1.7.9a / API tokens v1.7.9b / Shared Pager v1.7.9c / Backups + Notifications v1.7.11 / Trash v1.7.14 / Mailer templates v1.7.15 / Realtime monitor v1.7.16 / Webhooks management v1.7.17 / Command palette ⌘K v1.7.18 / Hooks editor v1.7.20b / Translations editor v1.7.22b / Health dashboard v1.7.23c / Cache inspector v1.7.24b / Field renderers v1.7.25b-v1.7.29b. **3 remaining 📋 are all blocked-by-design**: Documents browser (blocked on §3.6 Documents track — out of v1) / Hierarchical tree-DAG viz (blocked on §3.8 Hierarchies tail — deferred v1.6.x) / Realtime collab indicators (v1.1+). §3.12 Testing infrastructure ✅ Go-side (v1.7.20a `testapp` JSON + v1.7.30b YAML fixtures + v1.7.28c `railbase test` CLI) — JS-side `mockApp().fireHook()` deferred. §3.14 verification gate — **✅ test-debt + ⏭ SHIP-gate empty post-v1.7.24; CI per-PR binary-size enforcement added v1.7.28a; `make verify-release` end-to-end smoke validated v1.7.29a**. **v1 SHIP fully unblocked + operator release artifacts + per-PR size gate + pre-tag one-command gate all green.** Honest completion of v1 plan.md scope: **~87% (~130 of ~150 line items shipped)**; remaining ~20 split between dependency-blocked / explicit v1.1+ / agent-friendly v1.x-bonus. Continuing burndown via parallel slices.

Детали по каждому milestone — [progress.md](progress.md).

---

## 1. Критический путь до v0 ship → v1 ship

Каждый узел блокирует следующий. Параллелится только то, что обозначено `→║`.

```
# v0 path → SHIP

v0.3.1 ✅
   │
   ▼
v0.3.2 Auth core ✅ ─────────────┐
   │                              │
   ▼                              │
v0.3.3 Filter parser + rules ✅   ▼
   │                       v0.5 Settings + admin CLI ✅ (║)
   ▼                              │
v0.4  Tenant middleware ✅        │
   │                              │
   ▼                              │
v0.6 Eventbus LISTEN/NOTIFY + audit ✅ ◄──┘
   │
   ▼
v0.7 TS SDK gen ✅ ─────────────► (║) v0.8 Admin UI v0 ✅
   │                                       │
   └──────► v0.9 verification gate ✅ ─────┘
                       │
                       ▼
                  v0 SHIP ✅ 🚢

# v1 path → SHIP. Стержневой путь — слева; справа side-branches,
# которые НЕ блокируют v1 SHIP (полируются параллельно).

v1.0 Mailer ✅
   │
   ▼
v1.1 Auth flows (record tokens) ✅
   │
   ▼
v1.1.1 OAuth/OIDC ✅ ──► v1.1.2 TOTP+MFA ✅ ──► v1.1.3 WebAuthn ✅
                                                       │
                                                       ▼
                                              v1.1.4 RBAC core ✅
                                                       │
                                                       ▼
                                              v1.2.0 Hooks core ✅
                                                       │
                                                       ▼
                                              v1.3.0 Realtime SSE ✅ ─────► v1.3.x WS + resume tokens 📋
                                                       │
                                                       ▼
                                              v1.3.1 Files inline ✅ ─────► v1.3.2 Documents + thumbnails 📋
                                                       │
                                                       ▼
                                              v1.4.0 Jobs + cron ✅ ──────► v1.4.1 Jobs CLI + recovery ✅
                                                       │
                                                       ▼
                                              v1.4.2–v1.5.12 Domain field types ✅ (8/9 groups closed; Hierarchies 4/7)
                                                       │
                                                       ▼
                                              v1.4.12–v1.5.5 Notifications + webhooks + i18n + cache + soft-delete + CSRF ✅ (§3.9 closed)
                                                       │
                                                       ▼
                                              v1.6.0–v1.6.6 Document generation XLSX/PDF/MD/CLI/async ✅ + v1.7.6c audit rows + v1.7.7c JS hooks (§3.10 8/8 ✅)
                                                       │
                                                       ▼
                                              v1.7.0–v1.7.8 PB compat / import / OpenAPI / backup / rate limit ✅ 9/9
                                                       │   (auth-methods, OpenAPI, rate-limit, API tokens,
                                                       │    compat-modes, streaming helpers, logs-as-records,
                                                       │    backup/restore, PB schema import — feature parity complete)
                                                       ▼
                                              v1.7.9–v1.7.21 §3.14 verification 📋 burn-down ✅ + Admin UI agents (parallel)
                                                       │   (audit doc / 5-min smoke / audit retention / hooks bindings
                                                       │    (realtime+routerAdd+cronAdd) / pagination cursor / cmd palette
                                                       │    / CSV import / testing infra (testapp) / hooks editor /
                                                       │    translations editor / BETWEEN parser)
                                                       ▼ 🔥
                                              v1.7.22 perf benchmark suite slice 1 (realtime / DB / hooks)
                                                       │
                                                       ▼
                                              v1.7.23 perf benchmark suite slice 2 (RLS overhead 2.53% / FS upload concurrency)
                                                       │   → §3.14 📋 critical-path test-debt empty ✅
                                                       ▼                          ║
                                              v1 verification gate (docs/17)      ║ ←— Admin UI 6 screens left (растягивается)
                                              📋 0 items — UNBLOCKED ✅           ║    (Documents browser / Cache inspector /
                                                       │                          ║    Tree-DAG viz / Realtime collab /
                                                       │                          ║    Field renderers / deeper Translations
                                                       │                          ║    — none gate SHIP per docs/16)
                                                       ▼                          ║
                                                v1 SHIP (PB parity + improvements) 🚢 unblocked
```

Прогноз сроков (1 разработчик):
- **v0 ship**: ✅ done (2026-05-10)
- **v1 ship — что осталось** (по состоянию на 2026-05-12, post v1.7.23):
  1. ~~§3.8 Domain field types~~ — ✅ 8/9 групп закрыто; Hierarchies tail (closure/DAG/nested_set) — отложено в v1.x как отдельный slice
  2. ~~§3.9 Notifications + webhooks + soft-delete + cache + i18n + CSRF + security headers~~ — ✅ functionally complete
  3. ~~§3.10 Document generation (XLSX/PDF/MD/CLI/async)~~ — ✅ 8/8 sub-tasks closed
  4. ~~§3.13 PB compat / import / OpenAPI / backup / rate limiting~~ — ✅ 9/9 done
  5. ~~§3.14 v1 verification gate~~ — ✅ **critical-path 📋 list empty** ([docs/17-verification-audit.md](docs/17-verification-audit.md): ✅ **111/175** (63%); 📋 0 SHIP-blocking; explicitly deferrable items #34/#55/#148 do not gate per docs/16; ⏸ 52 out of v1)
  6. §3.11 Admin UI screens — **16/22 ✅** (v0.8 baseline + v1.7.x slices). Осталось 6 — Documents browser (зависит от §3.6 documents — out of v1), Cache inspector, Hierarchical tree/DAG visualizers (зависит от Hierarchies tail — отложено), Realtime collaboration indicators (v1.1+), Field renderer extensions для domain types (XL), deeper Translations coverage. **Ни один из них не блокирует SHIP** per docs/16 — все растягиваются параллельно через агентов как post-SHIP polish или в v1.1.
- **Чистая дистанция до SHIP**: 🚢 **0 critical-path days** — v1 SHIP функционально + verification-gate-wise UNBLOCKED. Технический ship-acceptance шаг (cross-compile sweep + release notes + tag) — ~1 день operator работы. Post-SHIP polish (admin UI tail) идёт параллельно через autonomous-loop агентов.
- **v1.1 (cluster, S3, OTel, audit sealing)**: +6 недель после v1
- **v1.2 (enterprise plugins, mobile SDKs)**: +2-3 месяца

---

## 2. v0 — single-tenant skeleton, 5-min smoke

**Цель**: `railbase init demo && demo serve --embed-postgres` → TodoMVC за 5 минут. Источник: docs/16-roadmap.md §v0; docs/17-verification.md tests 1-15.

### 2.1 ✅ Foundation (v0.1)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 2.1.1 | Config loader (env + YAML stub) | ✅ | M |
| 2.1.2 | pgxpool wrapper (`internal/db/pool`) | ✅ | M |
| 2.1.3 | Embedded Postgres opt-in (`embed_pg` build tag) | ✅ | M |
| 2.1.4 | Migration runner + `_migrations` + hash-tracking | ✅ | L |
| 2.1.5 | System migrations (`extensions`, `_schema_snapshots`) | ✅ | S |
| 2.1.6 | `internal/errors` — typed Code + WriteJSON envelope | ✅ | S |
| 2.1.7 | slog logger (JSON/text) + request_id middleware | ✅ | S |
| 2.1.8 | chi router skeleton + /healthz + /readyz | ✅ | S |
| 2.1.9 | Graceful shutdown (30s drain) | ✅ | S |
| 2.1.10 | `internal/clock` + `internal/id` (UUIDv7) | ✅ | S |

### 2.2 ✅ Schema DSL + scaffold (v0.2)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 2.2.1 | Builder ядро (`CollectionBuilder`, `FieldSpec`, `RuleSet`) | ✅ | M |
| 2.2.2 | 15 PB-paritet field types | ✅ | L |
| 2.2.3 | Validator (identifiers, reserved names, per-type правила) | ✅ | M |
| 2.2.4 | Registry (global, alphabetical iteration) | ✅ | S |
| 2.2.5 | SQL DDL generator (CREATE TABLE + индексы + RLS + триггер `updated`) | ✅ | L |
| 2.2.6 | Snapshot+diff (JSONB в `_schema_snapshots`) | ✅ | L |
| 2.2.7 | `railbase init` — embed.FS templates → text/template | ✅ | M |
| 2.2.8 | CLI: `migrate up/down/status/diff <slug>` | ✅ | M |
| 2.2.9 | `--railbase-source` для replace directive | ✅ | S |

### 2.3 Generic CRUD (v0.3) — поэтапно

#### 2.3.1 ✅ CRUD endpoints (v0.3.1)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 2.3.1.1 | `internal/api/rest/router.go` — Mount + 5 PB-compat роутов | ✅ | S |
| 2.3.1.2 | Record JSON marshalling (PB-shape) | ✅ | M |
| 2.3.1.3 | Query builders (SELECT/INSERT/UPDATE/DELETE) | ✅ | M |
| 2.3.1.4 | 5 handlers + PgError classification | ✅ | M |
| 2.3.1.5 | Tests + e2e smoke | ✅ | S |

#### 2.3.2 ✅ Auth core (v0.3.2)

| ID | Задача | Статус | Эффорт | Deps |
|---|---|---|---|---|
| 2.3.2.1 | `internal/auth/password` Argon2id | ✅ | S | — |
| 2.3.2.2 | `internal/auth/token` + `session` (opaque + HMAC-SHA-256) | ✅ | M | — |
| 2.3.2.3 | `_sessions` migration (sliding + soft revoke) | ✅ | S | 2.3.2.2 |
| 2.3.2.4 | `schema.AuthCollection("users")` + system fields | ✅ | M | 2.2 |
| 2.3.2.5 | Middleware: Bearer + cookie → Principal | ✅ | M | 2.3.2.2 |
| 2.3.2.6–10 | 5 endpoints (signup/auth-with-password/refresh/logout/me) | ✅ | S×5 | 2.3.2.* |
| 2.3.2.11 | Generic CRUD блокирует auth-collections (`/records` → 403) | ✅ | S | 2.3.2.5 |
| 2.3.2.12 | `internal/auth/lockout` (in-process counter) | ✅ | S | 2.3.2.7 |
| 2.3.2.13 | Cookie: HttpOnly/SameSite=Lax/Secure(prod) | ✅ | S | 2.3.2.5 |
| 2.3.2.14 | `internal/auth/secret` master key из `pb_data/.secret` | ✅ | S | — |
| 2.3.2.15 | 30+ unit tests + e2e smoke | ✅ | M | * |

#### 2.3.3 ✅ Filter parser + rules (v0.3.3)

| ID | Задача | Статус | Эффорт | Deps |
|---|---|---|---|---|
| 2.3.3.1 | `internal/filter/lexer.go` + PositionedError | ✅ | M | — |
| 2.3.3.2 | recursive-descent parser → AST | ✅ | L | 2.3.3.1 |
| 2.3.3.3 | AST validator (deny на JSON/files/multiselect/relations/password) | ✅ | S | 2.3.3.2 |
| 2.3.3.4 | AST → parameterized SQL | ✅ | M | 2.3.3.2 |
| 2.3.3.5 | Magic vars: `@request.auth.id`, `@me`, `@request.auth.collectionName` | ✅ | M | 2.3.3.4 |
| 2.3.3.6 | Operators: `= != > < >= <= ~ !~ && \|\| () IN IS [NOT] NULL` | ✅ | M | 2.3.3.4 |
| 2.3.3.7 | `?filter=` wired в list (с `details.position` в 400) | ✅ | S | 2.3.3.4 |
| 2.3.3.8 | `?sort=±field` (default `-created,-id`) | ✅ | S | 2.3.3.4 |
| 2.3.3.9–10 | ListRule + ViewRule/UpdateRule/DeleteRule enforcement | ✅ | M | 2.3.3.4 |
| 2.3.3.11 | 25+ unit tests + e2e smoke | ✅ | M | * |

Deferred к v0.4+: CreateRule evaluator, dotted paths, BETWEEN, `?=`, magic times, `?expand=` / `?fields=`.

#### 2.3.4 ✅ Tenant middleware (v0.4)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 2.3.4.1 | Header `X-Tenant: <uuid>` resolution | ✅ | M |
| 2.3.4.2 | Middleware: `pool.Acquire` + `set_config('railbase.tenant', ...)` | ✅ | M |
| 2.3.4.3 | `tenant.WithSiteScope(ctx, reason)` API stub | ✅ | S |
| 2.3.4.4 | Generic CRUD на tenant-collections все 5 verb'ов | ✅ | S |
| 2.3.4.5 | App-layer `tenant_id = $N` injection (defense-in-depth) | ✅ | M |
| 2.3.4.6 | Server-side tenant_id force на INSERT | ✅ | S |
| 2.3.4.7 | `0004_tenants.up.sql` | ✅ | S |
| 2.3.4.8 | Pool-validate header против `tenants(id)` | ✅ | S |
| 2.3.4.9 | E2E smoke: 2 tenants, cross-tenant 404, forged tenant_id ignored | ✅ | M |

### 2.4 ✅ Settings + admin CLI + eventbus (v0.5)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 2.4.1 | `0005_settings_admins.up.sql` | ✅ | S |
| 2.4.2 | `internal/eventbus` (in-process, wildcards, async) | ✅ | M |
| 2.4.3 | `internal/settings.Manager` (Get/Set/Delete/List + change events) | ✅ | M |
| 2.4.4 | `internal/admins.Store` (Create/List/Delete/Authenticate) | ✅ | M |
| 2.4.5 | CLI `railbase admin create/list/delete` | ✅ | M |
| 2.4.6 | CLI `railbase tenant create/list/delete` | ✅ | S |
| 2.4.7 | CLI `railbase config get/set/delete/list` | ✅ | S |
| 2.4.8 | App wires eventbus + settings.Manager on boot | ✅ | S |
| 2.4.9 | 16 CLI smoke scenarios | ✅ | M |

### 2.5 ✅ Eventbus + audit writer (v0.6)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 2.5.1 | Eventbus in-process (✅ v0.5) | ✅ | M |
| 2.5.2 | LISTEN/NOTIFY bridge `pgbridge.go` (loop avoidance) | ✅ | M |
| 2.5.3 | `_audit_log` migration с hash chain columns | ✅ | S |
| 2.5.4 | Audit writer через bare pool + per-Writer mutex | ✅ | M |
| 2.5.5 | Auto-log auth.* events + redactJSON allow-list | ✅ | M |
| 2.5.6 | Hash chain `sha256(prev_hash \|\| canonical_json)` + `Verify()` | ✅ | S |
| 2.5.7 | CLI `railbase audit verify` + tamper detection smoke | ✅ | S |
| 2.5.8 | 5 unit tests + e2e smoke | ✅ | S |

### 2.6 📋 OIDC single provider (v0.6.5)

Полностью покрыто в v1.1.1 (OAuth + OIDC) — этот milestone больше не нужен как отдельный.

### 2.7 ✅ TS SDK gen (v0.7)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 2.7.1 | Schema → TypeScript types (`types.go`) | ✅ | M |
| 2.7.2 | Schema → Zod (`zod.go`) | ✅ | M |
| 2.7.3 | Collection CRUD wrappers (`collections.go`) | ✅ | M |
| 2.7.4 | Auth flow wrappers (`auth.go`) | ✅ | S |
| 2.7.5 | RailbaseError discriminated union (`errors.go`) | ✅ | S |
| 2.7.6 | `_meta.json` drift detection + `--check` exit code | ✅ | S |
| 2.7.7 | CLI `railbase generate sdk` | ✅ | S |
| 2.7.8 | `tsc --noEmit` clean + live round-trip | ✅ | M |
| 2.7.9 | 5 unit tests + SchemaHash | ✅ | S |

### 2.8 ✅ Embedded admin UI v0 (v0.8)

| ID | Задача | Phase | Статус | Эффорт |
|---|---|---|---|---|
| 2.8.A.1 | `0007_admin_sessions.up.sql` | A | ✅ | S |
| 2.8.A.2 | `admins.SessionStore` (HMAC-SHA-256 tokens) | A | ✅ | M |
| 2.8.A.3 | `internal/api/adminapi/middleware` + cookie | A | ✅ | S |
| 2.8.A.4 | Admin auth API (auth/refresh/logout/me) + audit hooks | A | ✅ | M |
| 2.8.A.5 | Admin data API (schema/settings/audit) | A | ✅ | M |
| 2.8.A.6 | Wire в app.go + 12-check curl smoke | A | ✅ | S |
| 2.8.B.1 | Vite + React 19 + Tailwind 4 + wouter + TanStack scaffold | B | ✅ | S |
| 2.8.B.2 | API client + APIError union + localStorage | B | ✅ | S |
| 2.8.B.3 | Login + AuthProvider/useAuth | B | ✅ | M |
| 2.8.B.4 | Records list (schema-driven, basic table — не QDataTable) | B | ✅ | M |
| 2.8.B.5 | Field editor registry (15 types, basic) | B | ✅ | M |
| 2.8.B.6 | Record editor (single create+update screen) | B | ✅ | M |
| 2.8.B.7 | Schema viewer | B | ✅ | S |
| 2.8.B.8 | Settings panel | B | ✅ | S |
| 2.8.B.9 | Audit log viewer (bonus) | B | ✅ | S |
| 2.8.B.10 | `admin/embed.go` + SPA fallback + mount `/_/` | B | ✅ | S |
| 2.8.B.11 | Bootstrap wizard + backend endpoint | B | ✅ | M |
| 2.8.B.12 | 9-check UI smoke + bundle measurement | B | ✅ | S |

Deferred к v1+: QDataTable virtualization, Tiptap, file upload UI, relation picker, RBAC editor, 22 экранов из docs/12, 2FA mandatory.

### 2.9 ✅ v0 verification gate

5 gates (см. progress.md для деталей):
- [x] (1) 5-min smoke (11 checks)
- [x] (2) TodoMVC e2e через TS SDK (15 checks)
- [x] (3) Schema-to-SDK round-trip + drift
- [x] (4) RLS smoke (HTTP 11 + DB-layer non-superuser)
- [x] (5) Cross-platform 6 binaries (13.06–15.19 MB)

**v0 SHIP** = ✅ 2026-05-10.

---

## 3. v1 — PB feature parity + improvements

**Цель**: PB-проекты мигрируют командой, JS-клиенты работают, native API готово. Источник: docs/16-roadmap.md §v1; docs/17 tests 16-100.

Внутри v1 порядок не строго последовательный — где помечено `(║)` параллелится.

### 3.1 ✅ Mailer (v1.0)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.1.1 | SMTP client (stdlib `net/smtp`, STARTTLS/implicit/off) | ✅ | M |
| 3.1.2 | Markdown→HTML engine + frontmatter + `{{ }}` interp | ✅ | M |
| 3.1.3 | Template loader (re-read on send; fsnotify deferred v1.0.1) | ✅ | S |
| 3.1.4 | `_email_events` (bounce/delivery) | ✅ (v1.7.35b — migration 0024 + `internal/mailer/events_store.go` `EventStore.{Write,ListRecent,ListByRecipient}` + `recordSendOutcome` per-recipient fan-out via `flattenRecipients`; `mailer.Options.EventStore` opt-in; CLI `railbase mailer events list [--recipient EMAIL] [--limit N]`; bounce/open/click tracking surface ready for SES/Postmark plugin in v1.x) | S |
| 3.1.5 | 8 built-in templates | ✅ | M |
| 3.1.6 | Mailer hooks dispatcher (`mailer.before_send` sync + `mailer.after_send` async via eventbus) | ✅ (v1.7.31b — `internal/mailer/events.go` + `SubscribeSync`/`PublishSync` on eventbus; subscribers can mutate `*Message` + set `Reject=true` pre-driver) | S |
| 3.1.7 | Rate limiter (global + per-recipient sliding window) | ✅ | S |
| 3.1.8 | `railbase mailer test` CLI | ✅ | S |
| 3.1.9 | Wire в app.go из settings + env | ✅ | S |
| 3.1.10 | 18 unit + 6 CLI smoke | ✅ | S |

Deferred: SES driver (v1.0.1), fsnotify, hooks dispatcher, i18n, per-tenant templates — см. progress.md.

### 3.2 🔄 Auth providers + flows (v1.1)

Phase split: **Phase A** (record-token flows) — ✅; **Phase B** (OAuth/TOTP/WebAuthn/MFA/RBAC) — sub-milestones v1.1.1–v1.1.4 ✅.

| ID | Задача | Phase | Статус |
|---|---|---|---|
| 3.2.12 | Record tokens (6 purposes, HMAC-hashed, single-use) | A | ✅ |
| 3.2.3 | Email verification flow | A | ✅ |
| 3.2.4 | Password reset flow (revokes all sessions) | A | ✅ |
| 3.2.5 | Email change flow (revokes all sessions) | A | ✅ |
| 3.2.8 | OTP code + magic links | A | ✅ |
| 3.2.1 | OAuth2 generic + Google + GitHub + Apple — **v1.1.1** | B | ✅ |
| 3.2.2 | Apple client_secret rotation CLI — v1.1.1 | B | ✅ |
| 3.2.6 | TOTP 2FA (hand-rolled RFC 6238) — **v1.1.2** | B | ✅ |
| 3.2.7 | WebAuthn passkeys (hand-rolled, ES256 + "none") — **v1.1.3** | B | ✅ |
| 3.2.9 | MFA state machine — v1.1.2 | B | ✅ |
| 3.2.10 | Auth origins (new device email) | B | ✅ (v1.7.38k — migration 0025 + `_auth_origins` + IPClass(/24,/48) + UAHash(version-stripped) + `Touch` UPSERT + signin handler enqueues `send_email_async` w/ `new_device_signin.md` template + CLI `auth origins list/delete`; 11 tests) |
| 3.2.11 | Devices, invites, impersonation | B | 📋 v1.1.x |

### 3.3 ✅ Per-tenant RBAC (v1.1.4)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.3.1 | `_roles` + `_role_actions` + `_user_roles` (migration 0012) | ✅ | S |
| 3.3.2 | Action-key catalog (typed Go constants); codegen из registry — deferred | ✅ (partial) | M |
| 3.3.3 | `rbac.Require` + `Resolved` + lazy Middleware | ✅ | S |
| 3.3.4 | Seed: 8 roles + ~40 grants (0013); 38-role port deferred | ✅ (minimum) | M |
| 3.3.5 | Site vs tenant scope + partial-unique + bypass logic | ✅ | M |
| 3.3.6 | Admin UI: RBAC editor | 📋 v1.1.x | L |

CLI: `railbase role list/show/create/delete/grant/revoke/assign/unassign/list-for` ✅.

### 3.4 🔄 Hooks (goja JS) (v1.2)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.4.1 | goja runtime (single VM; pool deferred до профайлинга) | ✅ | M |
| 3.4.2 | fsnotify watcher → registry replace (<1s, 150ms debounce) | ✅ | M |
| 3.4.3 | Watchdog: per-handler `goja.Interrupt` ✅ (v1.2.0) + stack-cap `SetMaxCallStackSize(128)` ✅ (v1.7.31d — `applyStackCap` helper on both VM construction sites; partial closure of true memory limit which goja doesn't expose) | ✅ | M |
| 3.4.4 | Hook dispatcher: 6 record events (Before/After × CUD) | ✅ | M |
| 3.4.5 | Hook dispatcher: onAuth* ✅ (v1.7.34b — `auth.{signin,signup,refresh,logout,lockout}` topics via AuditHook.WithBus(bus); 4 tests) + onMailer* ✅ (v1.7.31b — `mailer.{before_send,after_send}` topics; before-send is SYNC w/ Reject support) + onRequest ✅ (v1.7.38h — `$app.onRequest((e) => ...)` SYNC dispatcher in `internal/hooks/on_request.go`; per-handler 500ms watchdog; `e.next()` / `e.abort(status, body)`; `/_/*` bypass; atomic-pointer hot-reload; 12 tests; wired in app.go between compat + authmw) | ✅ (3/3) | M |
| 3.4.6 | `routerAdd` (v1.7.17 — chi-backed atomic-swap, path-param + query + headers + body + watchdog) | ✅ | S |
| 3.4.7 | `cronAdd` (cron runtime есть с v1.4.0; v1.7.18 wires JS handlers — in-memory snapshot atomic-swap, minute-aligned ticker, throw-isolated, hot-reload) | ✅ | S |
| 3.4.8 | JSVM bindings: $app + console + $export (v1.7.7c) + $app.realtime().publish (v1.7.15); полный $api surface deferred | ✅ (partial) | XL |
| 3.4.9 | Module system (require + vendor) | 📋 v1.2.x | M |
| 3.4.10 | Go-side typed hooks | ✅ (v1.7.38i — `internal/hooks/go_hooks.go` `GoHooks` w/ `OnRecord{Before,After}{Create,Update,Delete}` + `ErrReject` sentinel + sync Before / async After w/ panic recovery + wildcards-first ordering; JS dispatcher gained parallel Go phase that short-circuits on ErrReject; 9 tests; `App.GoHooks()` lazy-init getter) | M |
| 3.4.11 | Admin UI test panel | ✅ (v1.7.38j — `POST /api/_admin/hooks/test-run` builds isolated runtime per request (no DB writes possible, Bus=nil for realtime); console capture via custom slog.Handler; collapsible `<TestPanel/>` under Monaco editor; 6 tests; +8 KB bundle) | M |

### 3.5 🔄 Realtime (v1.3.0 SSE отгружено)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.5.1 | LocalBroker (eventbus single topic `record.changed`) | ✅ | S |
| 3.5.2 | LISTEN/NOTIFY bridge (auto via PGBridge) | ✅ | M |
| 3.5.3 | WebSocket transport (`coder/websocket`) | ✅ (v1.7.34a — `internal/realtime/ws.go`; PB-compat frame shape; `railbase.v1` subprotocol; ping/pong heartbeat 25s; dynamic subscribe/unsubscribe acks; resume via `since` cursor; auth-BEFORE-upgrade — unauthenticated requests get 401 w/o `Upgrade: websocket` header; 6 tests green) | M |
| 3.5.4 | SSE transport (heartbeat 25s, subscribed frame, X-Accel-Buffering) | ✅ | S |
| 3.5.5 | Subscription protocol — `?topics=` + `*` wildcard | ✅ (SSE) | M |
| 3.5.6 | Per-event RBAC + tenant filter (full row-rule integration deferred) | ✅ (auth+tenant) | M |
| 3.5.7 | Resume tokens (1000-event window) | ✅ (v1.7.5b) | M |
| 3.5.8 | Backpressure (cap 64, drop-oldest, Dropped() counter) | ✅ | S |
| 3.5.9 | PB SDK drop-in compat в strict mode | ✅ SSE (v1.7.36b — `internal/realtime/pb_compat.go` ClientRegistry + PB_CONNECT pre-frame + POST `/api/realtime` subscribe + `{action, record}` payload reshape) + WS strict-mode payload reshape ✅ (v1.7.37a — `writeRecordFrame` pbCompat threading w/ registry-nil gate) + per-record `<collection>/<recordId>` topic format ✅ (v1.7.37b — dual-fan w/ single-event-id refinement + wildcard-sub dedup) + `?token=` query-param auth ✅ (v1.7.37c — variadic `WithQueryParamFallback` option, GET+strict gated, Bearer+Cookie precedence preserved); `?expand=` row resolution — 📋 v1.x (bigger feature, 1-2 day slice) | M |

### 3.6 🔄 Files + documents + thumbnails (v1.3.1 inline отгружено)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.6.1 | Multipart upload streaming | ✅ (v1.3.1) | M |
| 3.6.2 | MIME validator (AcceptMIME wildcards) + SHA-256 reader | ✅ (v1.3.1) | S |
| 3.6.3 | FS storage driver (content-addressed, atomic) | ✅ (v1.3.1) | S |
| 3.6.4 | Thumbnails (disintegration/imaging) | 📋 v1.3.2 | M |
| 3.6.5 | Signed URLs (HMAC + expiry, constant-time compare) | ✅ (v1.3.1) | S |
| 3.6.6 | Documents: logical entity + versions + polymorphic owner | 📋 v1.3.2 | L |
| 3.6.7 | `_documents`, `_document_versions`, `_document_access_log` | 📋 v1.3.2 | M |
| 3.6.8 | Quotas (per-user / per-tenant / system) | 📋 v1.3.2 | M |
| 3.6.9 | Retention enforcer (depends on §3.7) | 📋 v1.4.x | S |
| 3.6.10 | FTS на title (Postgres tsvector) | 📋 v1.3.2 | S |
| 3.6.11 | Admin UI: documents browser | 📋 v1.3.2 | L |
| 3.6.12 | S3 / GCS storage drivers | 📋 v1.3.x | M |
| 3.6.13 | Orphan reaper job | ✅ (v1.7.33b — `RegisterFileBuiltins` adds `orphan_reaper`; 2-direction sweep DB+FS; per-collection table-exists check via information_schema; weekly `0 5 * * 0` in DefaultSchedules; 5 e2e tests) | S |

### 3.7 🔄 Jobs queue + cron (v1.4.0 core отгружено)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.7.1 | `_jobs` table + `SELECT…FOR UPDATE SKIP LOCKED` claim | ✅ (v1.4.0) | M |
| 3.7.2 | Worker pool (polling; LISTEN/NOTIFY tickler deferred) | ✅ (poll) | M |
| 3.7.3 | Retry engine (exp backoff 30s→1h, unknown-kind = permanent fail) | ✅ (v1.4.0) | S |
| 3.7.4 | Cron scheduler (hand-rolled 5-field, `_cron` persisted) | ✅ (v1.4.0) | M |
| 3.7.5.1 | Builtins: cleanup_sessions, cleanup_record_tokens, cleanup_admin_sessions | ✅ (v1.4.0) | S |
| 3.7.5.2 | scheduled_backup | ✅ (v1.7.31a — `RegisterBackupBuiltins(reg, runner, outDir, log)`; payload + retention sweep; NOT in `DefaultSchedules()` — operators enable explicitly via `cron upsert`) | M |
| 3.7.5.3 | audit_seal (Ed25519 chain) | ✅ (v1.7.32a — migration 0022 + `internal/audit/seal.go` Sealer + CLI `seal-keygen` + `verify` extension; per-seal inline public_key supports key rotation; daily `0 5 * * *` in DefaultSchedules) | M |
| 3.7.5.4 | document_retention | 📋 v1.3.2 | S |
| 3.7.5.5 | thumbnail_generate | 📋 v1.3.2 | S |
| 3.7.5.6 | send_email_async (✅ v1.7.30) / cleanup_logs (✅ v1.7.6) / export_async (✅ v1.6.5) / text_extract (📋 v1.x) | 🔄 (3/4) | M |
| 3.7.6 | Admin UI: jobs queue viewer | 📋 v1.4.x | M |
| 3.7.7 | CLI: `railbase jobs list/show/cancel/run-now/reset/recover/enqueue` + `cron list/show/upsert/delete/enable/disable/run-now` | ✅ (v1.4.1) | S |
| 3.7.8 | Stuck-job recovery (lock-expired sweep) — Cron tick auto-recovers; CLI `jobs recover` | ✅ (v1.4.1) | S |

### 3.8 🔄 🔥 Domain field types (v1.4) — Railbase differentiator

| Группа | Типы | Статус | Эффорт |
|---|---|---|---|
| Communication | tel ✅ / person_name ✅ / address ✅ | ✅ (v1.4.2 + v1.5.7) | M |
| Money | finance ✅ / percentage ✅ / currency ✅ / money_range ✅ | ✅ (v1.4.6 + v1.5.9) | L |
| Banking | iban ✅ / bic ✅ / bank_account ✅ | ✅ (v1.4.8 + v1.5.11) | M |
| Identifiers | slug ✅ / sequential_code ✅ / tax_id ✅ / barcode ✅ | ✅ (v1.4.4 + v1.5.8) | L |
| Locale | country ✅ / timezone ✅ / language ✅ / locale ✅ / coordinates ✅ | ✅ (v1.4.7 + v1.5.6) | M |
| Quantities | quantity ✅ / duration ✅ / date_range ✅ / time_range ✅ | ✅ (v1.4.9 + v1.5.10) | M |
| Workflow | status ✅ / priority ✅ / rating ✅ | ✅ (v1.4.10) | M |
| Hierarchies | tags ✅ / tree_path ✅ (LTREE + GIST) / adjacency_list ✅ / ordered_children ✅ / nested_set 📋 / closure_table 📋 / DAG 📋 | 🔄 (v1.4.11 + v1.5.12) | XL |
| Content | color ✅ / cron ✅ / markdown ✅ / qr_code ✅ | ✅ (v1.4.5 + v1.5.11) | M |

**Эффорт оставшегося**: **~2-3 дня** в §3.8 Hierarchies tail (Closure + DAG + Nested Set — каждый нужен companion `_closure_<col>` таблица + insertion/move triggers + helpers в `pkg/railbase/tree` или `pkg/railbase/dag`). Тройка отложена в v1.6.x как отдельный slice — это XL отдельно от §3.8 mainline closure.

### 3.9 📋 Notifications + webhooks + i18n + caching + security (v1.4-v1.5)

| Подсистема | Источник | Эффорт |
|---|---|---|
| 3.9.1 Notifications ✅ (v1.5.3) — inapp + email channels; quiet-hours ✅ (v1.7.34e — `_notification_user_settings` table + wrap-midnight time math) + digests ✅ (v1.7.34e — daily/weekly via `flush_deferred_notifications` cron + `digest_summary.md` template) + admin-side preferences editor ✅ (v1.7.35a — `internal/api/adminapi/notifications_prefs.go` 3 endpoints + master-detail screen + audit-logged updates); push channel deferred to plugin track | docs/20 | L |
| 3.9.2 Outbound webhooks ✅ (v1.5.0) — HMAC + retry + anti-SSRF; admin UI deferred | docs/21 | L |
| 3.9.3 i18n core ✅ (v1.5.5) + ICU plurals ✅ (v1.7.34c — 5 rule families: English/Russian-East-Slavic/Polish/Arabic/CJK + rulePass; SetPluralRule operator override) + `.Translatable()` field marker ✅ (v1.7.34c — JSONB+jsonb_typeof CHECK+GIN index; REST handler picks request-locale w/ exact→base-language→alphabetical fallback; full validator + per-locale-key BCP-47 shape check); per-tenant deferred | docs/22 | L |
| 3.9.4 Cache primitive ✅ (v1.5.1) + slice 1 wiring ✅ (v1.7.31c — filter AST + RBAC resolver) + slice 2 wiring ✅ (v1.7.32b — i18n bundles + v1.7.32d settings); **4 production caches now registered** (filter.ast / rbac.resolver / i18n.bundles / settings); RBAC bus-driven invalidation ✅ (v1.7.32c — `rbac.role_{granted,revoked,assigned,unassigned,deleted}` topics); template-render cache + records-list-page cache deferred to future slices | docs/14 | M |
| 3.9.5 Security headers + IP allow/deny ✅ (v1.4.14) + CSRF ✅ (v1.5.4) + Anti-bot ✅ (v1.7.33a — honeypot + UA sanity; production-gated; tier-3 IP CIDR list for Tor/scrape ranges deferred pending CIDR feed source decision) | docs/14 | M |
| 3.9.6 Soft delete `.SoftDelete()` ✅ (v1.4.12) — admin trash UI + 30d retention cron deferred | docs/03 | M |
| 3.9.7 Batch ops ✅ (v1.4.13) — atomic pgx.Tx + 207 Multi-Status | docs/03 | M |
| 3.9.8 Streaming responses ✅ (v1.5.2) — SSE + NDJSON + ChunkedWriter helpers; WS in §3.5.x | docs/14 | S |

### 3.10 🔄 Document generation (v1.5-v1.6)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.10.1 | XLSX через `xuri/excelize` (streaming, 256MB ceiling) | ✅ (v1.6.0) | M |
| 3.10.2 | PDF native (`signintech/gopdf`) + embedded Roboto TTF | ✅ (v1.6.1) | M |
| 3.10.3 | Markdown→PDF (gomarkdown + frontmatter) | ✅ (v1.6.2) | M |
| 3.10.4 | Schema-declarative `.Export()` + `/export.xlsx` / `/export.pdf` | ✅ (v1.6.3 + v1.6.4) | M |
| 3.10.5 | Async mode через jobs (>100k rows) | ✅ (v1.6.5) | S |
| 3.10.6 | RBAC re-use ListRule + audit | ✅ (v1.6.0 ListRule + v1.7.6c audit rows: export.xlsx/pdf/enqueue/complete events on every sync + async path; OutcomeSuccess/Failed/Denied/Error split) | S |
| 3.10.7 | JS hooks `$export.xlsx` / `$export.pdf` / `$export.pdfFromMarkdown` | ✅ (v1.7.7c) | M |
| 3.10.8 | CLI `railbase export collection/query/pdf` | ✅ (v1.6.6) | S |

### 3.11 🔄 Admin UI 22 screens (v1.5-v1.6) — растягивается на весь v1

| Группа экранов | Статус | Эффорт | Deps |
|---|---|---|---|
| Hooks editor (Monaco, auto-save, test panel) | ✅ (v1.7.20b — `admin/src/screens/hooks.tsx` Monaco editor + file tree; `@monaco-editor/react` ~+30 KB gzip; 800ms-debounced auto-save w/ status pill; backend `GET/PUT/DELETE /api/_admin/hooks/files[/{path}]` path-traversal-safe; 503 unavailable until `HooksDir` wired in `app.go` v1.7.21+) | L | 3.4 |
| Realtime monitor (event stream, topic filter) | ✅ (v1.7.16b — active SSE subscriptions snapshot via `GET /api/_admin/realtime`, polls every 5s; per-sub User/Tenant/Topics/Created/Dropped table with red+bold drop counters when >0; no unsubscribe by design) | M | 3.5 |
| Audit log browser (filters, export) | ✅ (v0.8 baseline + v1.7.14 filter bar: event/outcome/user_id/since/until/error_code; export deferred) | M | 2.5 |
| Documents browser (versions, archive, search) | 📋 | L | 3.6 |
| Notifications log + preferences editor | ✅ (v1.7.11d log + v1.7.35a prefs editor — master-detail screen at `/notifications/prefs` w/ user list + per-kind channel toggles + quiet-hours/digest settings; audit-logged updates via `notifications.admin_prefs_changed`) | M | 3.9.1 |
| Webhooks management (timeline, dead-letter replay) | ✅ (v1.7.17b — 7-endpoint CRUD via `GET/POST /api/_admin/webhooks` + per-row pause/resume/delete + delivery timeline w/ dead-letter replay; display-once HMAC secret on create) | L | 3.9.2 |
| Translations editor (coverage %, missing keys) | ✅ (v1.7.22b — `admin/src/screens/i18n.tsx` locale dropdown + coverage badges + Key/Translation edit table w/ embedded-value hints; backend `GET/PUT/DELETE /api/_admin/i18n/{files,locales}[/{locale}]` reading from `Deps.I18nDir`; reference bundle = embedded `en.json`; empty values count as "not translated"; 10 test funcs / 19 subtests) | L | 3.9.3 |
| Cache inspector | ✅ (v1.7.24b — `admin/src/screens/cache.tsx` polling `GET /api/_admin/cache` every 5s; `internal/cache.Registry` sync.Map index w/ `StatsProvider` interface; per-row Clear action via `POST /api/_admin/cache/{name}/clear` audit-logged as `cache.cleared`; awaiting `cache.Register()` callsites on hot paths to surface live entries) | M | 3.9.4 |
| Trash / soft-deleted browser | ✅ (v1.7.14d — cross-collection list / per-row restore / collection filter; permanent purge intentionally NOT exposed to UI) | M | 3.9.6 |
| Mailer template editor | ✅ (v1.7.15b — list view with override-status badges / per-kind raw markdown ↔ rendered HTML preview; editing surface deferred — operator workflow stays "edit on disk + hot-reload via existing mailer loader") | M | 3.1 |
| Backup/restore UI | ✅ (v1.7.11c — list / create with manifest summary; restore deliberately CLI-only) | M | 3.13 |
| Logs viewer | ✅ (v1.7.6 — filter bar / level badges / expandable attrs) | M | docs/14 |
| Jobs queue viewer | ✅ (v1.7.8 — status+kind filters / expandable last_error) | M | 3.7 |
| Health/metrics dashboard | ✅ (v1.7.23c — `admin/src/screens/health.tsx` polling `GET /api/_admin/health` every 5s; backend aggregates pool/runtime/jobs/audit/logs/realtime/backups/schema via nil-guarded Deps reads; `StartedAt` wired in `app.go` v1.7.25a) | M | 3.13 |
| API token manager | ✅ (v1.7.9 — list / create with display-once banner / revoke / rotate; filter by owner + include-revoked) | M | docs/14 |
| Hierarchical tree/DAG visualizers | 📋 | L | 3.8 |
| Command palette ⌘K | ✅ (v1.7.18b — Cmd/Ctrl+K opens overlay; fuzzy substring search across pages + collections; ArrowUp/Down + Enter to navigate via wouter; Escape/click-outside closes; ⌘K header badge dispatches synthetic keydown; UI-only, zero new deps) | M | 2.8.1 |
| Realtime collaboration indicators | 📋 | M | 3.5 |
| Field renderer extensions для domain types | ✅ (25 / ~25 — slices 1-5 v1.7.25b+v1.7.26b+v1.7.27c+v1.7.28b+v1.7.29b cover every §3.8 domain type) | XL | 3.8 |
| Shared Pager component | ✅ (v1.7.8 — `admin/src/layout/pager.tsx`; audit + logs + jobs all migrated) | S | — |

### 3.12 🔄 Testing infrastructure (v1.7.20 Go-side shipped)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.12.1 | `railbase test` CLI (cobra wrapper over `go test`) | ✅ (v1.7.28c — flag composition: --short/--race/--coverage/--only/--timeout/--tags/--verbose/--integration/--embed-pg; pure buildTestArgv() + 8 unit tests; watch + combined JS-coverage merge deferred to v1.x) | M |
| 3.12.2 | Fixture loader (`LoadFixtures(...)`) — JSON + YAML (.yaml/.yml; JSON wins precedence) | ✅ (v1.7.20a JSON + v1.7.30b YAML — `pkg/railbase/testapp/fixtures.go`; gopkg.in/yaml.v3 promoted to direct dep) | M |
| 3.12.3 | Actor abstractions (`AsUser` / `AsAnonymous`; `AsAdmin` deferred) | ✅ (v1.7.20a `pkg/railbase/testapp/actor.go`) | S |
| 3.12.4 | HTTP assertion helpers (`Status` / `StatusIn` / `JSON` / `DecodeJSON` / `Header`) | ✅ (v1.7.20a `pkg/railbase/testapp/response.go`) | S |
| 3.12.5 | Mock data generator (gofakeit) | ✅ (v1.7.33d — `NewMockData(coll).Seed(s).Set(k,v).Generate(n)` + `GenerateAndInsert(actor, n)`; 19 field types auto-faked; gofakeit/v7 direct dep; production binary +0 since testapp is embed_pg-gated) | M |
| 3.12.6 | Snapshot testing (Playwright) | 📋 v1.x | M |
| 3.12.7 | Combined Go + JS coverage report | ✅ (v1.7.35c — `railbase coverage [--go PATH] [--js PATH] [--out PATH]` hand-rolled Go coverprofile parser + c8 JSON parser + `html/template` single-file HTML w/ inline CSS; no new deps; 12 tests under `-race`) | S |
| 3.12.8 | JS-side hook unit test harness (`MockHookRuntime.FireHook()`) | ✅ (v1.7.33c — `pkg/railbase/testapp/hookmock.go`: tempdir-backed runtime, no fsnotify watcher for fast per-test iteration; 5 tests cover BeforeCreate mutation + reject-via-throw + AfterCreate fire-and-forget contract + multi-file + no-handler noop) | M |

### 3.13 🔄 PB compat + import + observability finals (v1.6-v1.7)

| ID | Задача | Статус | Эффорт |
|---|---|---|---|
| 3.13.1 | Compat modes (strict / native / both) | ✅ (v1.7.4) | M |
| 3.13.2 | `/api/collections/{name}/auth-methods` | ✅ (v1.7.0) | S |
| 3.13.3 | `railbase import schema --from-pb <url>` | ✅ (v1.7.8) | L |
| 3.13.4 | OpenAPI spec generator | ✅ (v1.7.1 + v1.7.6b export/realtime/upload) | M |
| 3.13.5 | Backup/restore (manual + scheduled) | ✅ (v1.7.7 manual CLI; scheduled cron + S3 deferred to v1.7.8) | L |
| 3.13.6 | Logs as records (PB feature) | ✅ (v1.7.6) | M |
| 3.13.7 | Rate limiting (per-IP/per-user/per-tenant) | ✅ (v1.7.2) | M |
| 3.13.8 | API token manager (scoped, 30d rotation) | ✅ (v1.7.3) | M |
| 3.13.9 | Streaming responses helpers | ✅ (v1.5.2) | S |

### 3.14 🔄 v1 verification gate

Подробный worklist в [docs/17-verification-audit.md](docs/17-verification-audit.md) (v1.7.10).

**Coverage снапшот (post v1.7.23)**:
- ✅ **111/175** items (63%) covered by existing Go tests under `-race -tags embed_pg`
- 🔄 **6** partial — has the underlying feature, docs/17 sub-assertion gap
- 📋 **0** v1 SHIP-blocking — **critical-path test-debt list is empty** ✅
- ⏸ **52** out of v1 scope (plugins / v1.1+ / mobile SDKs)
- ⏭ **9** CI shell-scripts, not Go tests (goreleaser smoke etc.)

**Critical-path 📋 items gating v1 SHIP**: **none**.

*Explicitly deferrable (do not gate SHIP per docs/16)*: hooks memory limit (#55, depends on hooks v1.2.0 polish); devices trust 30d + revoke (#34, deferred to v1.1.x per plan); #148 JS hook unit-test harness (Go-side `testapp` covers hook testing today).

**v1 SHIP is unblocked**. Remaining work is parallel polish (~6 Admin UI screens via agents) that does NOT gate the release per the original roadmap.

After zero 📋 items remain in the audit, v1 SHIP unblocks.

---

## 4. v1.1 — production-ready (cluster, S3, OTel, sealing)

**Срок**: +6 недель после v1.

| Подсистема | Эффорт | Источник |
|---|---|---|
| 4.1 Plugin host (custom gRPC, manifest, distribution) | XL | docs/15 |
| 4.2 `railbase-cluster` (NATS distributed broker) | L | docs/15 |
| 4.3 `railbase-orgs` (organizations, invites, seats) | XL | docs/15 |
| 4.4 `railbase-billing` (Stripe primary) | XL | docs/15 |
| 4.5 `railbase-authority` (approval engine) | L | docs/15 |
| 4.6 `railbase-fx` (FX rates) | M | docs/15 |
| 4.7 S3 storage adapter | M | docs/07 |
| 4.8 OpenTelemetry (OTLP/HTTP) | M | docs/14 |
| 4.9 Prometheus `/metrics` | S | docs/14 |
| 4.10 Audit sealing (Ed25519 hash chain) | M | docs/14 |
| 4.11 Postgres production hardening | L | docs/03 |
| 4.12 Schema-per-tenant escape hatch | M | docs/03 |
| 4.13 Encryption at rest (AES-256-GCM field-level + KMS) | L | docs/14 |
| 4.14 Self-update mechanism (`railbase update` / `rollback`) | L | docs/14 |

---

## 5. v1.2 — экосистема (enterprise, mobile, AI)

**Срок**: +2-3 месяца после v1.1.

Все позиции — отдельные плагины (docs/15) либо мобильные SDK (docs/11):
- 5.1 `railbase-saml`, `railbase-scim`, `railbase-workflow`, `railbase-push`
- 5.2 `railbase-doc-ocr`, `railbase-doc-office`, `railbase-pdf-preview`, `railbase-esign`
- 5.3 `railbase-mcp` (LLM agents)
- 5.4 `railbase-postmark / sendgrid / mailgun` (mailer providers)
- 5.5 `railbase-analytics`, `railbase-cms`, `railbase-compliance`
- 5.6 `railbase-payment-manual`, Paddle/LemonSqueezy
- 5.7 `railbase-search-meili / typesense`
- 5.8 `railbase-accounting` (GL, cost centers, period close)
- 5.9 `railbase-vehicle`, `railbase-id-validators`, `railbase-bank-directory`, `railbase-qr-payments`, `railbase-geocode`
- 5.10 Mobile SDKs: Swift, Kotlin, Dart
- 5.11 `--template ai` (pgvector), `--template enterprise` (SAML+SCIM+sealing)

---

## 6. v2 — расширения (после стабилизации экосистемы)

- `railbase-wasm` (wazero hook runtime)
- Federated/multi-region replication
- Plugin marketplace
- Local-first sync engine (rxdb-style)
- Python SDK
- BPMN authoring в admin UI
- White-label theming plugin
- Module federation для plugin admin UI

---

## 7. Cross-cutting нефункциональные требования

| Требование | Статус |
|---|---|
| Бинарник ≤ 30 MB (stripped + UPX) | ✅ держим (13-15 MB) |
| Cross-compile linux/darwin/windows × amd64/arm64 | ✅ настроен в v0.9 |
| Pure Go (без CGo): SQLite не используется, embedded PG только под build tag | ✅ держим |
| Strict semver с v1; deprecation cycle ≥ 2 minor | вводим перед v1 ship |
| Open governance (RFC процесс) | вводим перед v1 ship |

---

## 8. Открытые вопросы (требуют решения до или во время v1)

См. docs/18-risks-questions.md.

1. ~~Tenant resolution priority~~ — ✅ закрыто в v0.4.
2. ~~Session cookie attributes~~ — ✅ закрыто в v0.3.2.
3. ~~Audit retention policy~~ — ✅ закрыто в v0.6; sealing — v1.1.
4. **Plugin RPC choice** (custom gRPC vs go-plugin vs WASI) — закрыть до 4.1 (v1.1).
5. **OpenAPI vs gRPC service surface** для plugins — закрыть до 4.1.

---

## 9. Quick links — навигация по docs

- [README.md](README.md) — overview
- [progress.md](progress.md) — что отгружено по milestones, архитектурные решения, deferred
- [docs/00-context.md](docs/00-context.md) — позиционирование, принципы
- [docs/01-pb-audit.md](docs/01-pb-audit.md) — PB feature parity matrix
- [docs/02-architecture.md](docs/02-architecture.md) — interfaces, modules, packages
- [docs/03-data-layer.md](docs/03-data-layer.md) — DB, schema DSL, migrations, filter, pagination, batch, view collections
- [docs/04-identity.md](docs/04-identity.md) — auth flows, OAuth, OTP/MFA, devices, tokens, RBAC, tenant
- [docs/05-realtime.md](docs/05-realtime.md) — subscriptions, transport, broker
- [docs/06-hooks.md](docs/06-hooks.md) — JSVM bindings, isolation, Go hooks
- [docs/07-files-documents.md](docs/07-files-documents.md) — file fields, thumbnails, document mgmt, retention
- [docs/08-generation.md](docs/08-generation.md) — XLSX, PDF, markdown templates
- [docs/09-mailer.md](docs/09-mailer.md) — providers, templates, flows, i18n
- [docs/10-jobs.md](docs/10-jobs.md) — queue, cron, workflow
- [docs/11-frontend-sdk.md](docs/11-frontend-sdk.md) — TS SDK gen, multi-language
- [docs/12-admin-ui.md](docs/12-admin-ui.md) — 22 screens, UX, extensions
- [docs/13-cli.md](docs/13-cli.md) — все команды
- [docs/14-observability.md](docs/14-observability.md) — config, errors, logging, audit, telemetry, lifecycle, backup, security, caching, encryption, self-update
- [docs/15-plugins.md](docs/15-plugins.md) — plugin system + 30+ плагинов
- [docs/16-roadmap.md](docs/16-roadmap.md) — v0/v1/v1.1/v1.2/v2 phases
- [docs/17-verification.md](docs/17-verification.md) — 131+ end-to-end test items
- [docs/17-verification-audit.md](docs/17-verification-audit.md) — v1 SHIP gate worklist: every docs/17 item → coverage status (✅/🔄/📋/⏸); refreshed post v1.7.9
- [docs/18-risks-questions.md](docs/18-risks-questions.md) — risks + open questions
- [docs/19-glossary.md](docs/19-glossary.md) — terminology
- [docs/20-notifications.md](docs/20-notifications.md) — unified notifications
- [docs/21-webhooks.md](docs/21-webhooks.md) — outbound webhooks с HMAC
- [docs/22-i18n.md](docs/22-i18n.md) — full-stack i18n
- [docs/23-testing.md](docs/23-testing.md) — testing infrastructure

Старая монолитная версия плана: [plan-v1-archive.md](plan-v1-archive.md).
