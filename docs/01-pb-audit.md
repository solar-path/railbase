# 01 — PocketBase feature parity audit

Полный coverage matrix всех PB capabilities из исходников `pocketbase/pocketbase` (apis/, core/, tools/, plugins/).

## Data model

| PB feature | Railbase | Где |
|---|---|---|
| Base collections (CRUD) | ✅ core | [03-data-layer.md](03-data-layer.md) |
| **Auth collections** (multiple user pools) | ✅ core | [04-identity.md](04-identity.md#multiple-auth-collections) |
| **View collections** (SQL views as read-only) | ✅ core | [03-data-layer.md](03-data-layer.md#view-collections) |
| Records CRUD with API rules | ✅ core | [03-data-layer.md](03-data-layer.md#schema-dsl) |
| **Filter expression language** (`@request.auth.id`, etc.) | ✅ core | [03-data-layer.md](03-data-layer.md#filter-expression-language) |
| **Pagination** (page+perPage + cursor) | ✅ core | [03-data-layer.md](03-data-layer.md#pagination) |
| **Batch operations** (atomic multi-record) | ✅ core | [03-data-layer.md](03-data-layer.md#batch-operations) |
| **Field-level resolvers** (для filter/expand с RBAC checks) | ✅ core | [03-data-layer.md](03-data-layer.md#filter-expression-language) |
| Computed fields | ✅ core | [03-data-layer.md](03-data-layer.md#computed-fields) |

## Field types

### PB-paritет

| PB field | Railbase | Notes |
|---|---|---|
| text | ✅ | + FTS via Postgres `tsvector` + GIN |
| number | ✅ | + Int/Float distinction |
| bool | ✅ | |
| email | ✅ | + RFC5322 validator |
| url | ✅ | |
| date | ✅ | + autocreate/autoupdate |
| select (single/multi) | ✅ | |
| file (single/multi) | ✅ | + thumbnails |
| relation (single/multi) | ✅ | + cascade delete |
| json | ✅ | + typed via Go generic `JSONOf[T]` |
| editor (richtext) | ✅ | + sanitization (bluemonday) |
| password | ✅ | Argon2id; не отдельный field type, а built-in для auth collections |
| autodate | ✅ | через `.AutoCreate()`/`.AutoUpdate()` modifiers |
| **geo point** | ✅ core (basic) / 🟡 plugin (full) | `schema.Coordinates()` → `POINT` для basics; `railbase-geo` plugin для PostGIS (полигоны, distance queries, spatial joins) |
| **vector** | ✅ via pgvector | `schema.Vector(N)` → native `vector(N)` тип; HNSW/IVFFlat indexes |

### Domain-specific types (Railbase extensions)

Эти типы критичны для B2B SaaS / fintech / ERP / CRM / HR / inventory / accounting проектов; в PB пришлось бы городить вручную через `text` + custom validators + ручное форматирование на клиенте. Railbase делает их first-class.

#### Communication & contact

| Field | Railbase | Notes |
|---|---|---|
| **tel** | ✅ core | E.164 normalized storage, libphonenumber validation, country-code picker UI |
| **address** | ✅ core | Structured (street/city/state/country/zip), country-aware validation, optional geocoding |
| **person_name** | ✅ core | Composite (prefix/first/middle/last/suffix), culturally-aware ordering и formatting |

#### Money & finance

| Field | Railbase | Notes |
|---|---|---|
| **finance** | ✅ core | Decimal precision (НЕ float), no rounding loss |
| **currency** | ✅ core | Composite (amount + ISO 4217 code), locale-aware formatting, FX-ready |
| **percentage** | ✅ core | Decimal с UI %, semantic precision (tax rates, discounts, margins) |
| **money_range** | ✅ core | Min/max currency для price ranges, salary bands |

#### Banking (treasury, payroll, AP/AR)

| Field | Railbase | Notes |
|---|---|---|
| **iban** | ✅ core | International Bank Account Number, mod-97 checksum, country format validation |
| **bic** | ✅ core | SWIFT/BIC код, формат + optional bank-directory lookup |
| **bank_account** | ✅ core | Composite (IBAN/national-account + BIC + bank name + currency) |

#### Identifiers

| Field | Railbase | Notes |
|---|---|---|
| **tax_id** | ✅ core | Multi-country: ИНН/КПП/ОГРН (РФ), EIN/SSN (US), VAT/CIF (EU), TIN, etc.; per-country format validation |
| **slug** | ✅ core | URL-safe identifier с auto-generation от другого field |
| **sequential_code** | ✅ core | Auto-numbered с pattern (`PO-{YYYY}-{NNNN}`), atomic counter, per-tenant scope |
| **barcode** | ✅ core | UPC / EAN-13 / ISBN / GTIN с checksum validation |

#### Locale & geography

| Field | Railbase | Notes |
|---|---|---|
| **country** | ✅ core | ISO 3166-1 alpha-2 enum, ~250 codes, native name + flag display |
| **language** | ✅ core | ISO 639-1 enum, для user preferences и i18n routing |
| **timezone** | ✅ core | IANA TZ database (`Europe/Moscow`, `America/New_York`), для scheduling, payroll periods |
| **locale** | ✅ core | RFC 5646 (`en-US`, `ru-RU`, `fr-FR-u-cu-eur`) для formatting |
| **coordinates** | ✅ core | Lightweight lat/lng (без full geo plugin); для map pins, store locations |

#### Quantities & measurement

| Field | Railbase | Notes |
|---|---|---|
| **quantity** | ✅ core | Amount + unit-of-measure (kg/pcs/m³/hours/...) с unit conversion catalog |
| **duration** | ✅ core | ISO 8601 duration (`P3M`=3 months, `P14D`=14 days) |
| **date_range** | ✅ core | Start + end pair (contracts, leave, fiscal periods) |
| **time_range** | ✅ core | Start + end time of day (business hours, schedules) |

#### State & workflow

| Field | Railbase | Notes |
|---|---|---|
| **status** | ✅ core | Explicit state machine с allowed transitions (отличается от plain Enum) |
| **priority** | ✅ core | Levels (low/medium/high/critical) с color/icon mapping |
| **rating** | ✅ core | 1-5 / 1-10 stars с UI control |

#### Hierarchies & tagging

| Field | Railbase | Notes |
|---|---|---|
| **tree_path** | ✅ core | Иерархия через materialized path (`/root/dept/subdept`) — для chart of accounts, departments, categories |
| **tags** | ✅ core | Multi-string с autocomplete по existing values, color labels |

#### Content

| Field | Railbase | Notes |
|---|---|---|
| **markdown** | ✅ core | Long-form markdown с preview (отличается от richtext WYSIWYG HTML) |
| **color** | ✅ core | Hex/RGB/HSL с picker, для theming/labeling |
| **cron** | ✅ core | Cron expression с UI builder (для scheduling fields в коллекциях) |
| **qr_code** | ✅ core | Encode-from-field или manual; PNG/SVG/PDF rendering, ECC levels, optional logo overlay |

#### National payment QR — plugin

| Field | Где |
|---|---|
| `qr_payment_sbp` | plugin `railbase-qr-payments` (СБП Россия, MerchantID validation) |
| `qr_payment_epc` | plugin `railbase-qr-payments` (EPC QR Code SEPA EU) |
| `qr_payment_pix` | plugin `railbase-qr-payments` (PIX Brasil) |

#### Specialized — plugins

| Field | Где |
|---|---|
| **gl_account** | plugin `railbase-accounting` (chart of accounts hierarchy + GAAP/IFRS validation) |
| **cost_center** | plugin `railbase-accounting` |
| **fiscal_period** | plugin `railbase-accounting` |
| **signature** | plugin `railbase-esign` (digital signatures, audit trail) |
| **vin** | plugin `railbase-vehicle` (Vehicle Identification Number, 17 chars validation) |
| **license_plate** | plugin `railbase-vehicle` (per-country format) |
| **passport / national_id** | plugin `railbase-id-validators` (per-country PII) |
| **public_key** | plugin `railbase-security` (SSH/PGP keys) |

См. также:
- Детальная спецификация: [03-data-layer.md](03-data-layer.md#domain-specific-field-types)
- TypeScript SDK helpers: [11-frontend-sdk.md](11-frontend-sdk.md#domain-specific-types)
- Admin UI field editors: [12-admin-ui.md](12-admin-ui.md#3-record-editor--modal-или-full-page)
- Терминология: [19-glossary.md](19-glossary.md#domain-specific-field-types)

## Auth & identity

| PB feature | Railbase | Где |
|---|---|---|
| Email + password | ✅ core | [04-identity.md](04-identity.md#auth-flows) |
| Email verification | ✅ core | |
| Password reset | ✅ core | |
| **OAuth2 providers** (35+ shipped) | ✅ core | [04-identity.md](04-identity.md#oauth-providers) |
| **Apple sign-in** (с client_secret rotation) | ✅ core | [04-identity.md](04-identity.md#apple-quirks) |
| 2FA TOTP | ✅ core | |
| **MFA model** (multi-step challenges) | ✅ core | [04-identity.md](04-identity.md#mfa-model) |
| **OTP** (magic links / SMS / email codes) | ✅ core | [04-identity.md](04-identity.md#otp-magic-links) |
| **External auth model** (multiple providers per user) | ✅ core | [04-identity.md](04-identity.md#external-auth-linking) |
| **Auth origin model** (track devices/locations) | ✅ core | [04-identity.md](04-identity.md#devices--auth-origins) |
| Impersonation (admin → user) | ✅ core | |
| **Auth methods endpoint** (`GET /auth-methods`) | ✅ core | для динамического UI |
| **Record tokens** (verification/reset/file-access, отдельно от sessions) | ✅ core | [04-identity.md](04-identity.md#tokens) |
| Session refresh | ✅ core | |
| **Auth refresh endpoint** (`POST /auth-refresh`) | ✅ core | |
| API rules per-collection | ✅ core | через `.ListRule()`, `.ViewRule()`, etc. |
| Superusers (system admins) | ✅ core | отдельная сущность от auth-collections |
| WebAuthn / passkeys | ✅ core | (PB добавил в 0.23+) |
| **SAML / SCIM** | 🟡 plugin | `railbase-saml`, `railbase-scim` |

## Realtime

| PB feature | Railbase | Notes |
|---|---|---|
| Subscribe `*` (collection-wide) | ✅ core | |
| Subscribe by recordId | ✅ core | |
| Subscribe array of IDs | ✅ core (extension over PB) | |
| **Subscribe with filter** | ✅ core (native mode) | PB не имеет |
| **Subscribe with expand** | ✅ core (native mode) | PB не имеет |
| **`@me` shorthand** | ✅ core (native mode) | |
| **Custom topics** (`$app.realtime().publish()`) | ✅ core | PB не имеет |
| **Resume tokens** (reconnect доставит missed events) | ✅ core | PB не имеет (events lost on reconnect) |
| WebSocket transport | ✅ core | (PB только SSE) |
| SSE transport | ✅ core | для PB compat |
| RBAC on subscribe | ✅ core | |
| **Cluster-mode realtime** | 🟡 plugin `railbase-cluster` | PB single-node only |
| **Backpressure handling** | ✅ core | explicit drop с audit |
| **Per-tenant quotas** (max subs, max events/sec) | ✅ core (multi-tenant) | |

См. [05-realtime.md](05-realtime.md).

## Hooks

| PB feature | Railbase |
|---|---|
| `onRecordCreate/Update/Delete` (Before/After) | ✅ core |
| `onRecordAuthRequest`, etc. | ✅ core |
| `routerAdd` (custom HTTP routes из JS) | ✅ core |
| `cronAdd` (cron jobs из JS) | ✅ core |
| **JSVM bindings**: `$app`, `$apis`, `$http`, `$os`, `$security`, `$template`, `$tokens`, `$filesystem`, `$mailer`, `$dbx`, `$inflector` | ✅ core |
| Hot reload через fsnotify | ✅ core |
| Sandbox: timeout, memory, recycling | ✅ core (улучшено) |
| Go-side hooks | ✅ core |
| **WASM hooks** (alt runtime) | 🟡 plugin v2+ |

См. [06-hooks.md](06-hooks.md).

## Storage & files

| PB feature | Railbase |
|---|---|
| File fields (single/multi) | ✅ core |
| **Image thumbnails** (auto, lazy) | ✅ core |
| Local FS storage | ✅ core |
| S3-compatible storage | ✅ core |
| Signed URLs | ✅ core |
| MIME / size validation | ✅ core |
| **Logical document repository** (versions, polymorphic owner) | ✅ core (extension) |
| **Retention / legal hold** | ✅ core (extension) |
| **PDF text extraction** | ✅ opt-in flag |
| **OCR / Office docs extraction** | 🟡 plugins |
| **PDF preview generation** | 🟡 plugin |

См. [07-files-documents.md](07-files-documents.md).

## Mailer

| PB feature | Railbase |
|---|---|
| SMTP | ✅ core |
| Custom templates с variables | ✅ core |
| **Markdown templates с frontmatter** | ✅ core (extension) |
| Built-in flows: signup verify, password reset, email change | ✅ core |
| **2FA recovery, magic link, invite templates** | ✅ core (extension) |
| **i18n (`signup.ru.md`, `signup.en.md`)** | ✅ core (extension) |
| **Hot reload templates** | ✅ core (extension) |
| SES / Postmark / Sendgrid / Mailgun | 🟡 SES в core, остальные plugins |

См. [09-mailer.md](09-mailer.md).

## Settings & operations

| PB feature | Railbase |
|---|---|
| **Runtime-mutable settings** (через admin UI) | ✅ core |
| **Settings model** в БД (`_settings` table) | ✅ core |
| Health endpoint (`/api/health`) | ✅ core |
| **Backup admin UI** (manual + scheduled) | ✅ core |
| **Backup auto-upload to S3** | ✅ core |
| **Logs as records** (`_logs` table, queryable) | ✅ core |
| Logs viewer в admin UI | ✅ core |
| **DB retry logic** (auto-retry on busy) | ✅ core |
| **File watcher** (notify_watcher) для hot reload | ✅ core |
| Rate limiting middleware | ✅ core |
| CORS middleware | ✅ core |
| Body limit middleware | ✅ core |
| Gzip middleware | ✅ core |
| **GhUpdate** (auto-update via GitHub releases) | 🟡 plugin `railbase-ghupdate` |

См. [14-observability.md](14-observability.md).

## Migrations

| PB feature | Railbase |
|---|---|
| **JS migrations** runner | ✅ core |
| **Go migrations** runner | ✅ core |
| **Auto-migrations diff** (при schema change через UI генерит migration JS) | ✅ core (через DSL diff) |
| Migration history | ✅ core |
| Migration content hash | ✅ core (extension) |
| **Up/down migrations** | ✅ core (extension; PB только up) |

См. [03-data-layer.md](03-data-layer.md#migrations).

## Admin UI

| PB feature | Railbase |
|---|---|
| Login screen (с superuser + 2FA) | ✅ core |
| Dashboard | ✅ core |
| Collections CRUD | ✅ core |
| Schema editor | 🟡 read-only viewer (конфликт с schema-as-code) |
| Records CRUD | ✅ core |
| Auth users management (sessions/devices/2FA/impersonate) | ✅ core |
| Logs viewer | ✅ core |
| Backups page | ✅ core |
| Settings page | ✅ core |
| **Hooks editor (Monaco)** | ✅ core |
| **Realtime monitor** | ✅ core (extension) |
| **Audit log viewer** | ✅ core (extension) |
| **RBAC matrix editor** | ✅ core (extension) |
| **Documents browser** | ✅ core (extension) |
| **Approvals (с authority plugin)** | 🟡 plugin |
| Command palette ⌘K | ✅ core (extension) |
| Realtime collaboration indicators | ✅ core (extension) |
| Theming / white-label | 🟡 v2 plugin |

См. [12-admin-ui.md](12-admin-ui.md).

## CLI

| PB feature | Railbase |
|---|---|
| `serve` | ✅ |
| `superuser create/update/delete` | ✅ (`railbase admin create/...`) |
| `migrate up/down/list/history` | ✅ |
| `migrate collections` (sync from JSON) | ✅ через DSL |
| `migrate create` (scaffold) | ✅ |

См. [13-cli.md](13-cli.md).

## Tools (PB internal helpers)

| PB tool | Railbase эквивалент | Где |
|---|---|---|
| `tools/auth` (35+ OAuth providers) | ✅ core `internal/auth/oauth/` | [04-identity.md](04-identity.md#oauth-providers) |
| `tools/cron` | ✅ через `robfig/cron/v3` | [10-jobs.md](10-jobs.md) |
| `tools/dbutils` | ✅ через `internal/db/` | [03-data-layer.md](03-data-layer.md) |
| `tools/filesystem` | ✅ `internal/storage/` | [07-files-documents.md](07-files-documents.md) |
| `tools/hook` | ✅ через `internal/eventbus` | [06-hooks.md](06-hooks.md) |
| `tools/inflector` | ✅ `pkg/railbase/inflector` (pluralize, snake_case, и т.д.) | |
| `tools/list` | ✅ stdlib generics | |
| `tools/logger` | ✅ stdlib `slog` | [14-observability.md](14-observability.md) |
| `tools/mailer` | ✅ `internal/mailer/` | [09-mailer.md](09-mailer.md) |
| `tools/picker` | ✅ stdlib | |
| `tools/router` | ✅ через `chi` | [02-architecture.md](02-architecture.md) |
| `tools/routine` (panic-safe goroutine wrapper) | ✅ `internal/routine/` | |
| `tools/search` | ✅ `internal/filter/` | [03-data-layer.md](03-data-layer.md) |
| `tools/security` (crypto helpers) | ✅ `internal/security/` | |
| `tools/store` (in-memory cache) | ✅ `internal/store/` | |
| `tools/subscriptions` (realtime broker) | ✅ `internal/realtime/` | [05-realtime.md](05-realtime.md) |
| `tools/template` | ✅ stdlib `text/template` | [08-generation.md](08-generation.md) |
| `tools/tokenizer` | ✅ часть `internal/filter/` | |
| `tools/types` | ✅ `pkg/railbase/types/` | |

## Plugins (PB) → Railbase

| PB plugin | Railbase |
|---|---|
| `plugins/jsvm` | в core (JSVM hooks — главная фича) |
| `plugins/migratecmd` | в core CLI |
| `plugins/ghupdate` | плагин `railbase-ghupdate` |

## Что Railbase добавляет сверх PB

| Capability | Где |
|---|---|
| Type-safe TS SDK с zod-схемами | [11-frontend-sdk.md](11-frontend-sdk.md) |
| Postgres adapter (тот же бинарник) | [03-data-layer.md](03-data-layer.md) |
| Multi-tenancy first-class (opt-in) | [04-identity.md](04-identity.md) |
| Tenant-scope RBAC в core | [04-identity.md](04-identity.md) |
| Distributed realtime (NATS plugin) | [05-realtime.md](05-realtime.md) + plugin |
| Document management (logical docs vs file fields) | [07-files-documents.md](07-files-documents.md) |
| XLSX + PDF generation core | [08-generation.md](08-generation.md) |
| Authority — approval engine plugin | plugin |
| Stripe billing plugin | plugin |
| MCP server для LLM agents | plugin |
| Audit hash-chain (opt-in для compliance) | [14-observability.md](14-observability.md) |
| Resume tokens для realtime | [05-realtime.md](05-realtime.md) |
| Subscribe filter + expand | [05-realtime.md](05-realtime.md) |
| Markdown email templates с i18n | [09-mailer.md](09-mailer.md) |
| Vector search opt-in | [03-data-layer.md](03-data-layer.md) |
| Domain-specific field types: `tel` (E.164 + libphonenumber), `finance` (decimal precision), `currency` (amount + ISO 4217) | [03-data-layer.md](03-data-layer.md#domain-specific-field-types) |

## Что Railbase делает иначе

| Аспект | PocketBase | Railbase |
|---|---|---|
| Schema source-of-truth | JSON в БД (через UI) | Go DSL (через `schema/*.go`) |
| Schema migrations | Auto-generated JS из UI changes | DSL diff → Postgres SQL миграции |
| Migrations storage | JS файлы в `pb_migrations/` | SQL файлы в `migrations/` (один target — Postgres) |
| API rules | Filter expression strings | Go-typed builders (rec) + filter expressions для PB-compat |
| Pre-1.0 stability | Breaking changes допустимы | Strict semver с v1, deprecation cycles |
| Bus factor | 1 maintainer | Open governance с самого начала |
