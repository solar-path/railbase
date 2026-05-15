# 16 — Roadmap

## v0 (4-6 недель) — single-tenant skeleton

**Цель**: TodoMVC за 5 минут с нуля; LLM может сгенерировать схему через JSON-export.

### Core

- DB layer (`pgx/v5` pool, retry logic, RLS context propagation, `LISTEN/NOTIFY` listener)
- Embedded Postgres opt-in для dev (`--embed-postgres` flag)
- Required Postgres extensions auto-install: `pgcrypto`, `ltree`
- Schema DSL (basic field types, relations, rules)
- Migrations: auto-discover, hash-tracking, up/down (один SQL файл per migration)
- Settings model (runtime-mutable через UI)
- Errors, slog logger, ULID, clock injection
- Eventbus (in-process channels + Postgres `LISTEN/NOTIFY` для cross-process)
- Bootstrap flow + first-run wizard
- Basic auth (sessions + password + OIDC)
- Generic CRUD endpoint (PB-compat URLs)
- Embedded admin UI (read/write коллекций, basic RBAC editor)
- TS SDK gen (главный артефакт)
- `schema export --json`
- Module architecture (layered)
- Lifecycle (graceful shutdown, healthz/readyz)
- Configuration & secrets
- Error model

### Verification

- 5-minute smoke test (`railbase init demo && railbase serve --embed-postgres`)
- TodoMVC working
- Schema-to-SDK round-trip
- RLS smoke: tenant A cannot see tenant B records даже при попытке прямого запроса

---

## v1 (3-4 месяца) — PB feature parity + improvements

**Цель**: PB-проекты мигрируют командой, JS-клиенты работают, native API готово к долгой жизни.

### Auth & identity

- All auth providers (35+ OAuth: Apple/GitHub/Google/Microsoft/Discord/...)
- OIDC, OAuth2, SAML (через plugin)
- 2FA TOTP, WebAuthn passkeys
- MFA model (multi-step challenges state machine)
- OTP / magic links
- External auth model (multiple providers per user)
- Auth origins (new device detection)
- Devices, invites (single-tenant), impersonation
- Identity model полная (multiple auth collections, view collections)
- Record tokens (verification, reset, file access, magic link)
- Apple sign-in с client_secret rotation
- Auth methods endpoint
- Per-tenant RBAC в core

### Data layer

- All PB field types (text/number/bool/date/email/url/select/file/relation/json/richtext/password)
- Domain-specific types для B2B/fintech/ERP/CRM:
  - Communication: **tel** (E.164), **address** (structured + country validation), **person_name** (composite, culturally-aware)
  - Money: **finance** (decimal precision), **currency** (amount + ISO 4217), **percentage**, **money_range**
  - Banking: **iban** (mod-97), **bic**, **bank_account** (composite)
  - Identifiers: **tax_id** (multi-country: ИНН/EIN/VAT/...), **slug** (auto-gen), **sequential_code** (`PO-{YYYY}-{NNNN}` с atomic counter), **barcode** (UPC/EAN/ISBN/GTIN)
  - Locale: **country**, **language**, **timezone**, **locale**, **coordinates**
  - Quantities: **quantity** (amount + UOM с conversion), **duration** (ISO 8601), **date_range**, **time_range**
  - Workflow: **status** (state machine с allowed transitions), **priority**, **rating**
  - Hierarchies: **5 patterns** — adjacency list (recursive CTE helpers) / **tree_path** (materialized path) / nested set / closure table / DAG; **tags** (multi-string + autocomplete); ordered children через `.Ordered()`; cycle prevention
  - Content: **markdown**, **color**, **cron**, **qr_code** (encode-from-field, PNG/SVG/PDF, ECC levels, optional logo)
- Computed fields
- View collections (read-only SQL views)
- Multiple auth collections
- Filter expression language (strict PB-compat + native AST-based)
- Pagination (offset PB-compat + cursor native)
- Batch operations (atomic multi-record)
- Field-level resolvers
- Compile-time tenant enforcement

### Hooks

- Goja JS hooks с full sandbox (timeouts, memory watchdog, recycling)
- All PB-compat hook names
- JSVM extensive bindings ($app, $apis, $http, $os, $security, $template, $tokens, $filesystem, $mailer, $dbx, $inflector)
- `pb_hooks/` watcher с hot reload
- Custom HTTP routes из JS (`routerAdd`)
- Cron из JS (`cronAdd`)
- Go-side hooks
- Test panel в admin UI

### Realtime

- WS + SSE LocalBroker
- Resume tokens (extension over PB)
- Subscribe filter, expand, custom topics, `@me` shorthand
- Per-event RBAC + filter pass
- Backpressure handling
- PB SDK drop-in compat (strict mode)

### Files & documents

- File storage (FS) + image thumbnails (`disintegration/imaging`)
- Signed URLs
- Document management (logical docs + versions + polymorphic owner via `.AllowsDocuments()`)
- Immutable repository (no DELETE, archive only)
- Quotas, retention, legal hold
- FTS на title

### Document generation

- XLSX (excelize) + native PDF (gopdf) + markdown templates
- Schema-declarative `.Export()`
- REST endpoints `/export.xlsx`/`/export.pdf`
- JS hooks `$export.*`
- Streaming для больших датасетов
- Async mode через jobs (>100k rows)

### Mailer

- SMTP + markdown templates с pre-configured signup/reset/2fa/invite/email-change/otp flows
- Hot-reload templates
- i18n (per-language templates)

### Audit & ops

- Audit log (legacy `_audit_log`, chain v1, hash-only)
- DB retry logic
- File watcher hot reload
- Rate limiting (per-IP, per-user, per-tenant)
- Logs as records (PB feature) — `_logs` table, slog persistence, gated by `logs.persist`
- Backup/restore (manual + scheduled, см. v1.x § UI restore)

### v3.x Unified Audit Log — Phase 1 ✅

Реализовано в этой ветке (см. [19-unified-audit.md](19-unified-audit.md) и
[14-observability.md](14-observability.md#2-unified-audit-log-v3x-две-таблицы)):

- Split `_audit_log_site` + `_audit_log_tenant` (chain v2), partition'ed by month.
- `audit.Store` API: `WriteSite/WriteTenant + Entity/ActorOnly` safety wrappers.
- Per-tenant chain isolation (одно offboarding не ломает чужой verify).
- Transparent dual-write: legacy `audit.Writer.Write()` автоматически зеркалит в v3 через `AttachStore` (миграция callsite'ов без code-changes).
- REST CRUD auto-audit через `CollectionSpec.Audit: true` (`<collection>.{created,updated,deleted}` events с entity).
- `_audit_seals` extended: `target ∈ ('legacy'|'site'|'tenant')` + `tenant_id` колонка.
- Unified Timeline UI (`/_/logs`): один экран, фильтры actor_type/event/entity/tenant_id/request_id/outcome, drawer с before/after JSON diff.
- Deep-dive views перенесены: App logs → Health → Process logs; Email events → Settings → Mailer → Deliveries; Notifications log → Settings → Notifications → Log.

### v0.4.1 — Sentinel integration feedback ✅

After Sentinel-v2 actually integrated v0.4, FEEDBACK.md (`/Users/work/apps/sentinel/FEEDBACK.md`) caught the gap between «surface объявлен» и «runtime wired + reachable from userland». Quote: *"surface добавили, чтобы пометить task закрытым, но не дошли до завершения — реэкспорта типов / wiring'а runtime'а."* — fair. Closed:

**P0 (хуже всего — фичи объявлены, но недоступны):**

- **P0/4 / dotted-path filter runtime** ✅ — `internal/api/rest/rules.go::filterCtx` теперь заполняет `filter.Context.Schema` (новый `schemaResolver` adapter над `registry.Get`). Async-export path в `async_export.go:610` тоже починен. Парсер + SQL emit были готовы со Sprint 2, но REST handlers создавали `filter.Context{}` без `Schema` — `project.owner = @request.auth.id` падало с "filter Context.Schema resolver not wired". **Самый позорный промах** — это поведение должен был ловить любой integration-тест Sentinel-стиля.
- **P0/1 / Config из userland** ✅ — `railbase.Config = config.Config` type alias + `railbase.LoadConfig()` + `railbase.DefaultConfig()` re-export. Плюс `cli.ExecuteWith(setup func(*App))` callback pattern для зарегистрированных хуков: CLI сам читает cfg, конструирует App, вызывает `setup(app)`, потом Run. Userland binary теперь пишется в 8 строк без единого `internal/*` import'а.
- **P0/2 / GoHooks типы** ✅ — новый публичный пакет `pkg/railbase/hooks` с aliases: `RecordEvent`, `Context`, `RecordHook`, `Principal`, `Registry`, event/action константы. Handler-литералы наконец-то можно писать без касания internal/hooks. Type aliases shared identity → `app.GoHooks().OnRecordBeforeCreate(coll, handler)` принимает handler декларированный через публичные types без явной конверсии.
- **P0/3 / Principal в кастомных роутах** ✅ — `railbase.PrincipalFrom(ctx) Principal` + публичная `railbase.Principal {UserID, Collection, APITokenID}` структура. Закрывает воркэраунд из FEEDBACK.md #3 (HMAC bearer + SELECT FROM _sessions, шесть SQL колонок).

**P1 (фичи работают но шероховатости):**

- **P1/5 / SDK system fields** ✅ — `internal/sdkgen/ts/types.go::writeInterface` добавляет `parent` / `sort_index` / `deleted` поля когда spec.AdjacencyList / Ordered / SoftDelete. До этого DB columns были, а TS типа не было → `as unknown as MyTask` всюду в Sentinel.
- **P1/6 / SDK JSON type** ✅ — `TypeJSON` теперь генерируется как `unknown` вместо `Record<string, unknown>`. JSON-колонка может быть array/scalar/null, не только объектом. Sentinel'овский `holidays: string[]` теперь работает без cast'а.
- **P1/7 / Realtime generic** ✅ — `subscribe<T>` теперь честно `yield ev as RealtimeEvent<T>` при passthrough от untyped `parseFrame`. Закрывает `error TS2322: Type RealtimeEvent<unknown> is not assignable to type RealtimeEvent<T>`.
- **P1/8 / scaffold go.mod валидная pseudo-version** ✅ — `internal/scaffold/scaffold.go::isValidGoModuleVersion` отбраковывает `b2a9eb7-dirty` и подставляет канонический `v0.0.0-00010101000000-000000000000`. `go mod tidy` теперь принимает scaffold'енный go.mod.
- **P1/9 / `@fontsource-variable/geist`** ✅ — `internal/api/uiapi/registry.go::stripGeistImport` удаляет admin-only font import при serve'е `styles.css` downstream-проектам. У operator'а больше не ломается Vite на старте.
- **P1/10 / default port** ✅ — `scaffold/templates/basic/railbase.yaml.tmpl` теперь эмитит `:8095` (matches binary default). Плюс комментарий объясняет precedence yaml < env < flag.

**Метаvalue:** один реальный consumer ловит больше gap'ов чем самый тщательный аудит кодовой базы. Sentinel'овский цикл feedback → fix → next feedback это единственный надёжный путь к production-grade DX.

### v3.x Sentinel-derived improvements ✅

Forensic audit of the Sentinel project (real consumer at /Users/work/apps/sentinel) revealed 14 concrete gaps where Railbase forced denormalisation, manual workarounds, or pivot-to-client. All addressed in this same branch:

**Sprint 0 — однодневные фиксы:**

- **A4 / topological migrate diff** ✅ — `internal/schema/gen/diff.go::topoSortCollections` walks the FK graph (Kahn) so CREATE TABLE ordering resolves dependencies on emit. Cycle case surfaces as an `IncompatibleChange`. Closes Sentinel's `migrations/1000_initial_schema.up.sql:1-3` ручная переставка.
- **B5 / SDK fetch.bind** ✅ — `internal/sdkgen/ts/index.go` binds `(opts.fetch ?? fetch).bind(globalThis)` so `this.fetchImpl(...)` doesn't lose receiver. Closes Sentinel's `lib/client.ts:38` «Illegal invocation» comment.
- **B7 / yaml config** ✅ — `internal/config/yaml.go` parses `railbase.yaml` (or `.yml`) with proper precedence (yaml < env < flag). Closes Sentinel's `railbase.yaml:4` "no-op" disclaimer.
- **B8 / redundant FK index** ✅ — `internal/schema/gen/sql.go::fieldIndexes` skips the user-`.Index()` btree on relation columns (FK index already covers). Closes Sentinel's `_owner_idx + _owner_fk_idx` duplication.

**Sprint 1 — closes «обрыв»:**

- **A1 / OnBeforeServe hook + App.Pool()** ✅ — `pkg/railbase/app.go` exposes `App.OnBeforeServe(func(chi.Router))` and `App.Pool() *pgxpool.Pool`. Custom HTTP routes (CPM compute, batch operations, file streaming) now mount through the standard chi router with full Railbase context access. THE foundational gap; closes Sentinel's "CPM client-side because no custom routes" pivot.
- **B1 / SDK type completeness** ✅ — Auth `signup()` now accepts user-defined fields (e.g. `name`); RealtimeEvent is generic `RealtimeEvent<T>` so payload typing flows through. Added `requestPasswordReset` / `confirmPasswordReset` / `requestVerification` / `confirmVerification` to the generated auth wrappers. Closes Sentinel's `as any` cast forest.

**Sprint 2 — schema & data-layer expressiveness:**

- **B2 / DefaultRequest** ✅ — `.DefaultRequest("auth.id")` builder method on Relation fields; REST CRUD's `applyRequestDefaults` substitutes `@request.auth.id` when client omits the field. Closes Sentinel's client-side `owner: authState.value.me.id` copy.
- **A2 / cross-collection dotted paths** ✅ — Filter lexer/parser/SQL compiler now accept `project.owner = @request.auth.id`. One-FK-hop limit (Sentinel's exact use case); deeper paths rejected. Closes the "denormalise owner onto every task" workaround.
- **A3 / M2M generic CRUD** ✅ — `TypeRelations` fields now generate junction tables (`<owner>_<field>`) at DDL time, surface as `[uuid, ...]` arrays on read (`array_agg ORDER BY sort_index`), and accept replace-mode writes on create. (Update-side handler wiring is the natural follow-on.) Closes Sentinel's `predecessors JSONB` workaround.
- **B6 / JSON validation rules** ✅ — `JSONField.ArrayOfUUIDReferences("tasks").SameValueAs("project")` declares both existence + peer-equality invariants for JSONB UUID arrays; REST CRUD validates server-side in the same tx as the INSERT. Migration aid for projects still on JSONB-array predecessors.

**Sprint 3 — DX:**

- **B3 / typed filter builder** ✅ — Generated SDK now emits `<collection>Filter.eq/ne/gt/...` helpers per collection with `encodeFilterLiteral` handling single-quote escaping. Closes Sentinel's `` filter: `project = '${projectId}'` `` injection risk.
- **B4 / realtime topic filters** ⏸ — Design TODO documented in `internal/realtime/realtime.go` (touch site identified, broker invariant change scoped). Full implementation deferred — substantive cross-cutting work; punted with a clear next-step note rather than shipped half-baked.
- **C1 / `railbase dev` command** ✅ — `pkg/railbase/cli/dev.go` orchestrates backend + frontend with one ^C lifecycle, prefixed logs, /readyz polling, cross-platform (no bash). Closes Sentinel's `dev.sh` papercut.
- **C6 / SDK auto-regen on schema change** ✅ — `railbase dev --watch-schema=./schema --sdk-pkg=./cmd/sentinel` watches `*.go` saves and re-runs `generate sdk` debounced 500ms.
- **C2 / SPA embed helper** ✅ — `App.ServeStaticFS("/", embeddedFS)` mounts a `go:embed`-backed filesystem with SPA-style index.html fallback. Single-binary deployment for Sentinel-shaped apps.

**Sprint 4 — продвинутые primitive'ы:**

- **C4 / Computed fields** ✅ — `Text/Number/Bool.Computed(expr)` builder emits `GENERATED ALWAYS AS (<expr>) STORED` columns; REST CRUD strips Computed fields from writable inputs. Server-side derived values without trigger plumbing.
- **C3 / Hook reentry guards** ✅ — `internal/hooks/hooks.go::Dispatch` carries a depth counter (max 5) via context. Closes Sentinel's "recursive hook reentry guards" gap from `schema/tasks.go:21` — runaway rollup loops surface as `ErrHookDepthExceeded`, not deadlocks.
- **B5 / SDK session manager** ✅ — `createRailbaseClient({ storage: localStorage })` hydrates the token on construction, persists on `setToken()`, clears on logout. Closes Sentinel's hand-rolled `lib/client.ts:4-54` localStorage boilerplate.

### v3.x Unified Audit Log — Phase 1.5 / 2 / 3 / 4 ✅

Все четыре фазы реализованы в этой же ветке:

- **Phase 1.5 carryover** ✅ — все callsite'ы adminapi перешли на `writeAuditEntity` helper с явным `entity_type`/`entity_id`. `writeAuditEvent` (signin/refresh/logout/bootstrap/metrics.read) теперь использует `AuditStore.WriteSite` напрямую (actor-only events). Legacy `forwardToStore` остаётся для bare-Deps unit-test'ов в качестве fallback'а.
- **Phase 1.5 #1 — Update/Delete before-image** ✅ — REST CRUD читает row PRE-UPDATE/DELETE через `fetchPreImage` helper и кладёт его в `before` для real diff в Timeline.
- **Phase 2 partition + archive** ✅ — `audit_partition` cron (23:55) пред-создаёт месячные partition'ы, `audit_archive` cron (06:00) выгребает sealed-and-old partition'ы в gzip JSONL под `<dataDir>/audit/<target>/YYYY-MM/`, пишет seal manifest, DROP partition. Retention default 14 дней.
- **Phase 2.1 full hash recompute** ✅ — `VerifyArchive` теперь rehydrate'ит canonical JSON из gzipped JSONL, пересчитывает SHA-256 chain (`site` или `tenant` форма), и для каждого seal'а в manifest'е делает `ed25519.Verify(public_key, chain_head, signature)` — структурная + криптографическая verify на disk без DB.
- **Phase 3 ArchiveTarget interface** ✅ — `internal/audit/archive_target.go` определяет `ArchiveTarget` (PutArchive / Walk). LocalFSTarget — default. S3Target — за build tag `aws` (Object Lock retention настраивается на bucket'е operator'ом, Railbase лишь uploads). Env-driven opt-in: `RAILBASE_AUDIT_ARCHIVE_TARGET=s3` + `RAILBASE_AUDIT_S3_BUCKET/REGION/PREFIX/SSE_KMS_KEY`.
- **Phase 4 KMSSigner** ✅ — `internal/audit/signer.go` определяет `Signer` interface. LocalSigner — default (читает `.audit_seal_key`). AWS KMSSigner — за build tag `aws` (Ed25519 ключ в KMS, private side never leaves). Env-driven opt-in: `RAILBASE_AUDIT_SEAL_SIGNER=aws-kms` + `RAILBASE_AUDIT_KMS_KEY_ID/REGION`.

S3 + KMS implementations поставляются как scaffolding с build tag `aws` — default binary не зависит от aws-sdk-go-v2 (~30 MB transitive). Operators, которым нужен hardware-enforced WORM/HSM, пересобирают `go build -tags aws ./...` и добавляют SDK в `go.mod`.

### PB compat

- Modes (strict/native/both)
- Auth methods endpoint
- Drop-in compat для PB JS SDK
- `railbase import schema --from-pb` migration tool

### Notifications, webhooks, i18n, caching

- **Notifications system** (см. [20-notifications.md](20-notifications.md)): unified entity, in-app + email channels (push в v1.2), user preferences, quiet hours, templates через mailer engine
- **Outbound webhooks** (см. [21-webhooks.md](21-webhooks.md)): HMAC signing, retry с exponential backoff, dead-letter queue, anti-SSRF
- **i18n full** (см. [22-i18n.md](22-i18n.md)): locale resolution chain, translatable record fields, server-side `t()`, ICU pluralization, RTL support
- **Caching layer** (см. [14-observability.md](14-observability.md#caching-layer)): sharded LRU для query/RBAC/filter/schema, stampede protection через singleflight

### Data flow

- **Data import** (CSV/XLSX/JSON, schema inference, dry-run, conflict policy, bulk via jobs queue)
- **Soft delete / undo** для коллекций (`.SoftDelete()` modifier, 30-day grace period)
- **Bulk operations API** (atomic transactional + non-atomic с 207 Multi-Status)
- **Streaming responses** (SSE/WS helpers для LLM use cases — `--template ai`)

### Security extended

- CSRF tokens (double-submit для cookie-auth)
- Security headers (CSP, HSTS, X-Frame-Options, etc.)
- IP allowlist/denylist per-route
- Account lockout (10 failed → 30 min)
- Trusted proxy config
- Anti-bot honeypots на signup
- Origin header validation

### Testing infrastructure

См. [23-testing.md](23-testing.md):
- `railbase test` CLI с in-memory test DB
- YAML fixtures
- API testing helpers (Go + JS hook tests)
- Mock data generator (schema-aware через gofakeit)
- Snapshot testing для admin UI (Playwright integration)
- Combined Go + JS coverage report

### Admin UI

- 22 screens (см. [12-admin-ui.md](12-admin-ui.md))
- Monaco hooks editor с auto-save
- Realtime monitor
- Audit log viewer
- Documents browser
- Notifications log + preferences editor
- Webhooks management screen
- Translations editor (i18n coverage %, missing keys)
- Cache inspector
- Trash / soft-deleted records browser
- Command palette ⌘K
- Realtime collaboration indicators
- Dogfooding: admin UI uses generated SDK

### CLI

- init с templates, serve, migrate, generate, import-from-pb, admin commands
- `railbase test` (с fixtures, helpers, coverage)
- `railbase import collection X --from file.csv` (data import)
- `railbase update` / `rollback` (self-update)

### Verification

- 60+ specific tests (см. [17-verification.md](17-verification.md))

---

## v1.1 — production-readiness без потолка

**Цель**: тот же бинарник держит cluster + Postgres production-нагрузку.

### Plugin host

- Plugin RPC mechanism choice (gRPC/go-plugin/WASI) — recommendation: custom gRPC
- Plugin manifest format
- Distribution через GitHub releases
- Hot-update path

### Plugins

- **railbase-cluster** (NATS distributed realtime)
- **railbase-orgs** (organizations entity, invites, seats, ownership transfer; per-tenant RBAC уже в core)
- **railbase-billing** (Stripe primary)
- **railbase-authority** (approval engine)
- **railbase-fx** (FX rates для currency conversions: ECB / OXR / Fixer / CoinGecko adapters)
- **railbase-pdf-preview** (poppler sidecar)

### Core enhancements

- Postgres-backed job queue (`SKIP LOCKED`) + cron
- S3 storage adapter
- OpenTelemetry (OTLP/HTTP)
- Prometheus `/metrics`
- Opt-in audit sealing (Ed25519 hash chain)
- Postgres production hardening (pool tuning, prepared statements, `pg_stat_statements` integration, slow query alerts, advisory lock helpers)
- Schema-per-tenant escape hatch для compliance кейсов
- Mailer SES adapter в core
- Document text extraction (opt-in `--documents-extract-text`)
- Auto-upload backups to S3
- **Encryption at rest** (field-level через `.Encrypted()`, transparent через `pgcrypto`; storage-level через managed PG provider TDE, key rotation CLI, KMS integration)
- **Self-update mechanism** (`railbase update` / `rollback`, staged updates в cluster)

---

## v1.2 — экосистема

**Цель**: enterprise readiness, AI integration, mobile native SDKs.

### Plugins

- **railbase-saml** (SAML SP via crewjam/saml)
- **railbase-scim** (SCIM 2.0 provisioning)
- **railbase-workflow** (saga engine)
- **railbase-push** (FCM/APNs)
- **railbase-doc-ocr** (Tesseract sidecar)
- **railbase-doc-office** (LibreOffice sidecar)
- **railbase-esign** (DocuSign/HelloSign)
- **railbase-mcp** (Model Context Protocol для LLM agents)
- **railbase-postmark / sendgrid / mailgun** (mailer providers)
- Paddle/LemonSqueezy adapters в `railbase-billing`
- **railbase-analytics** (events / funnels / cohorts)
- **railbase-cms** (page builder с blocks, scheduled publishing)
- **railbase-compliance** (GDPR / SOC2 / HIPAA helpers, PII scanner)
- **railbase-payment-manual** (invoicing без Stripe)
- **railbase-search-meili / railbase-search-typesense** (external search adapters)
- **railbase-accounting** (gl_account / cost_center / fiscal_period field types + period close workflow)
- **railbase-vehicle** (vin / license_plate field types)
- **railbase-id-validators** (passport / national_id с PII encryption)
- **railbase-bank-directory** (BIC → bank lookup)
- **railbase-geocode** (address → lat/lng через Nominatim/Google/Mapbox/Yandex)
- **railbase-qr-payments** (national payment QR standards: СБП / EPC SEPA / PIX / Swiss QR-bill)

### Multi-language SDKs

- Swift (iOS native)
- Kotlin (Android native)
- Dart (Flutter)

### Templates

- `--template ai` с vector search через `pgvector`
- `--template enterprise` с SAML+SCIM+audit sealing

---

## v2 — расширения

- **railbase-wasm** plugin (alt hook runtime через wazero)
- **Federated / multi-region replication** через Postgres logical replication + region-aware routing helper в SDK
- **Plugin marketplace**
- **Local-first sync engine** (rxdb-style для mobile/PWA)
- **Python SDK**
- **BPMN authoring** в admin UI (если railbase-workflow эволюционирует)
- **White-label theming plugin**
- **Module federation для plugin admin UI** (вместо iframe)

---

## Long-term vision

- Railbase как стандартный backend для AI-era development (vibe coding, LLM agents)
- Open governance с самого начала: RFC процесс, multi-maintainer GitHub org с v1
- Strict semver с v1; deprecation cycle минимум 2 minor versions
- Health metric: «can solo dev start project at 9am и иметь production-deployed at 5pm»
