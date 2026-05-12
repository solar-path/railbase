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

- Audit log (no sealing yet)
- DB retry logic
- File watcher hot reload
- Rate limiting (per-IP, per-user, per-tenant)
- Logs as records (PB feature)
- Backup/restore (manual + scheduled)

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
