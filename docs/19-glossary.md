# 19 — Glossary

Терминология Railbase — что значит что.

## Идентичность

**System admin (superuser)** — администратор инсталляции Railbase. Доступ к admin UI, schema, миграциям, plugins, audit. Хранится в `_system_admins`. Не subject of application RBAC.

**Application user** — конечный пользователь приложения, построенного на Railbase. Хранится в auth collections (`users`, `sellers`, etc.).

**Service account** — машинная идентичность через API token. Принадлежит admin / user / org.

**Auth collection** — collection с auth-методами (password, OAuth, OTP, etc.). Можно несколько (`users` + `sellers`). PB feature.

**API token** — long-lived bearer token, scoped к action keys, expirable, revocable.

**Record token** — short-lived signed token для specific operation (verify, reset, file access, magic link). Отдельно от sessions.

**Session** — opaque token (hashed storage), httpOnly cookie + bearer support, sliding window refresh.

**Device** — fingerprint-tracked client (UA + IP subnet hash). Trust 30 days.

**Auth origin** — track WHERE user authenticated (IP, geo, time). Anomaly detection.

**External auth** — link user ↔ OAuth provider. Multiple providers per user возможно.

**Impersonation** — admin может действовать от имени user'а (audited start/stop + every action).

## RBAC

**Action key** — строковая константа permission (`posts.create`, `auth.signin`). Generated literal-union в Go для compile-time safety.

**Role** — collection of action keys. Site scope или tenant scope.

**Site role** — глобальная роль (admin, user, guest). User имеет одну site role.

**Tenant role** — роль в контексте конкретного tenant. User может иметь разные tenant roles в разных tenants.

**Permission grant** — связка `(role, action_key)` в `_role_actions` table.

**Site scope** — global authorization (system admin actions, settings).

**Tenant scope** — per-tenant authorization (records, members, billing).

## Multi-tenancy

**Tenant** — изолированный workspace в multi-tenant системе. Default: row-level isolation через `tenant_id` column.

**`.Tenant()`** — DSL modifier на коллекции, делает её tenant-scoped (требует `tenant_id`).

**TenantContext** — `context.Context` value с current tenant ID. Подставляется в queries автоматически.

**`WithSiteScope(ctx)`** — explicit bypass tenant filter (admin tooling). Audited.

**Organization** — first-class entity в `railbase-orgs` plugin. Wraps tenant с invites/seats/billing.

## Data layer

**Collection** — table-equivalent, declared в Go DSL (`schema.Collection(...)`).

**View collection** — read-only collection from SQL view. PB feature.

**Auth collection** — collection с встроенным auth (password hash field, sessions).

**Field** — column в коллекции. Typed (text, number, file, relation, json, ...).

**Computed field** — derived from other fields. Stored или non-stored.

**Relation** — FK reference на другую коллекцию. Single или multi.

**Expand** — relation fetch в query response. RBAC-checked.

**Filter expression** — string DSL для query filters (`status='published' && @me=author`).

**API rules** — filter expressions на коллекции (ListRule, ViewRule, CreateRule, UpdateRule, DeleteRule).

**Magic var** — special syntax в filter (`@request.auth.id`, `@me`, `@tenant`, `@now`, `@yesterday`).

**Cursor pagination** — base64-encoded cursor `{sort_field, id}`. Stable.

### Domain-specific field types

**Tel** — телефонный номер. Storage: E.164 normalized (`+15551234567`). Validation: `nyaruka/phonenumbers` (libphonenumber pure-Go). Modifiers: region, mobile/landline filter.

**Finance** — decimal precision money type. **Не float**. Storage: native `NUMERIC(20, N)` Postgres, exact arithmetic. Go internal: `decimal.Decimal`. Для accounting/payroll/billing.

**Currency** — composite type: amount (decimal) + ISO 4217 code. JSON wire `{ amount: "1234.56", currency: "USD" }`. Mixed-currency arithmetic запрещено по умолчанию. Optional FX conversion через `railbase-fx` plugin.

**ISO 4217** — стандарт currency codes (USD, EUR, JPY, RUB, ...). Built-in catalog ~180 fiat + opt-in crypto.

**E.164** — международный стандарт phone format (`+<country><national>`).

**Per-currency precision** — стандартные decimals per ISO 4217 (USD=2, JPY=0, BHD=3, BTC=8). `.AutoPrecision()` использует.

**FX rate** — exchange rate между валютами. Plugin `railbase-fx` для conversions с historical rates.

### ERP-specific field types

**Address** — structured address (street/city/state/country/zip) с country-aware validation. JSONB storage.

**TaxID** — multi-country tax identifier. Catalog включает ИНН/КПП/ОГРН (RU), EIN/SSN (US), VAT (EU), GSTIN (IN), CNPJ (BR), и т.д. Per-country format validation.

**IBAN** — International Bank Account Number. Mod-97 checksum, country-specific length.

**BIC** — Business Identifier Code (SWIFT). 8 или 11 chars.

**BankAccount** — composite (IBAN/national-account + BIC + bank name + currency + holder).

**PersonName** — composite name (prefix/first/middle/last/suffix + preferred). Culturally-aware ordering и formatting.

**Percentage** — decimal с UI %. Internal scale 0-100. Semantic precision для tax/discount/margin.

**Quantity** — composite (amount + unit-of-measure). Built-in catalog (mass/length/volume/area/time/pieces/energy/currency). Unit conversion supported.

**Duration** — ISO 8601 duration (`P1Y6M`). Period arithmetic с dates.

**DateRange / TimeRange** — start + end pair. Overlap detection helpers.

**Status** — explicit state machine с allowed transitions. Отличается от plain Enum: имеет `.Transition()` rules + `OnEnter/OnExit` callbacks.

**Priority** — levels (low/medium/high/critical) с color/icon metadata.

**TreePath** — materialized path для иерархий (`/root/eng/backend`). Helpers: ancestors/descendants/children/siblings/depth.

**Tags** — multi-string field с autocomplete по existing values. Side-table indexing.

**Slug** — URL-safe identifier (`hello-world`) с auto-generation от другого field, конфликт-резолюция (`-2`, `-3`).

**SequentialCode** — auto-numbered с pattern (`PO-{YYYY}-{NNNN}`). Atomic counter с reset modes (yearly/monthly/daily/never).

**Barcode** — UPC/EAN-13/ISBN/GTIN с checksum validation.

**Country / Language / Timezone / Locale** — typed enums с embedded ISO catalogs (3166-1, 639-1, IANA, BCP 47).

**Coordinates** — lightweight `{lat, lng}` для simple geo (без full PostGIS).

**Markdown** — long-form markdown text (отличается от RichText, который WYSIWYG HTML).

**Color** — hex/RGB/HSL color value.

**Cron** — cron expression string для scheduling fields.

**MoneyRange** — min/max currency для price ranges, salary bands.

**Materialized path** — pattern для tree storage в SQL: путь как string с separator. Альтернатива nested set / adjacency list.

**State machine** — pattern для Status field: explicit list states + allowed transitions + lifecycle callbacks.

**Unit-of-measure (UOM)** — единица измерения для Quantity field. Catalog: mass/length/volume/area/time/pieces/energy.

**QR code field** — text-stored value с on-demand image rendering (PNG/SVG/PDF). Optional logo overlay. National payment QR standards через plugin (`railbase-qr-payments`: СБП / EPC / PIX / Swiss QR-bill).

**ECC (Error Correction Code)** для QR — Low (~7%) / Medium (~15%) / High (~25%) / VeryHigh (~30%). Higher = более resistant к damage, но capacity меньше.

### Hierarchical patterns

**Adjacency list** — простейший pattern: self-referential `parent_id` FK. Recursive CTE для queries.

**Materialized path (TreePath)** — path как string `/root/eng/backend`. Fast reads, expensive subtree moves.

**Nested set** — left/right integers через pre-order traversal. Best read для subtree aggregations; expensive writes.

**Closure table** — separate `(ancestor, descendant, depth)` table. Balanced read/write; supports DAG.

**DAG (Directed Acyclic Graph)** — multi-parent allowed. Closure-based. Use for BOM, dependency graphs.

**Ordered children** — children с explicit `sort_index`. Drag-drop UI support через `.Ordered()`.

**Topological sort** — linear ordering of DAG nodes such that parents precede children. Helper для DAG.

**Cycle prevention** — built-in validation для adjacency list / DAG; для materialized path enforced uniqueness.

**Recursive CTE** — Postgres SQL feature (`WITH RECURSIVE ...`) для traversing adjacency lists. Используется в core tree helpers как fallback к LTREE для нестрого-иерархических случаев.

**LTREE** — Postgres extension для materialized path с GiST index. Native operators: `<@` (descendant), `@>` (ancestor), `~` (lquery pattern match). Используется как storage backend для `schema.TreePath()`.

**RLS (Row-Level Security)** — Postgres feature, enforce'ит фильтр на уровне БД через `CREATE POLICY`. В Railbase используется как ground truth для tenant isolation: `tenant_id = current_setting('railbase.tenant')::uuid`. `FORCE ROW LEVEL SECURITY` гарантирует что даже table owner проходит через policies.

**LISTEN/NOTIFY** — Postgres pub/sub mechanism. `LISTEN channel_name` подписывается, `NOTIFY channel_name, payload` публикует. Railbase использует для cross-process realtime fan-out на single Postgres (без external broker).

**SKIP LOCKED** — Postgres feature `SELECT ... FOR UPDATE SKIP LOCKED`. Позволяет multiple workers efficiently claim rows из jobs queue без contention. Используется в `internal/jobs/queue/`.

**Offset pagination** — `?page=N&perPage=M`. PB-compat.

**Batch operation** — atomic multi-record API в одной транзакции.

**Migration** — SQL file `NNN_<slug>.up.sql` / `.down.sql`. Auto-discovered.

**Schema drift** — Go DSL hash != applied migration hash. Warning at startup.

## Realtime

**Subscribe target** — что слушать: `*` / recordId / array / filter / `@me` / custom topic.

**Event envelope** — `{ action, record, expand?, topic, ts, event_id }`.

**Resume token** — last `event_id` для replay missed events on reconnect.

**Topic** — channel name (`collections.posts.*`, `auth.users.{id}`, custom).

**Local broker** — in-process channels + sharded topic map. Default.

**NATS broker** — distributed via embedded NATS server. Через `railbase-cluster` plugin.

**Backpressure** — slow subscriber detection (>1MB queued → drop).

## Hooks

**Hook** — callback на event (record CRUD, auth, mailer, etc.). Go-side или JS via goja.

**JS hook** — `pb_hooks/*.pb.js` файлы. Hot-reload via fsnotify.

**Hook event** — strongly-typed event passed в hook (e.g. `RecordCreateEvent`).

**Hook isolation** — sandbox с timeout, memory ceiling, runtime recycling, panic isolation.

**Custom route** — HTTP endpoint defined в JS hook через `routerAdd()`.

**Cron hook** — scheduled JS function через `cronAdd()`.

**JSVM bindings** — globals доступные в JS hooks: `$app`, `$apis`, `$http`, etc.

## Files & documents

**File field** — inline file attachment на коллекции (avatar, cover). PB-style.

**Thumbnail** — auto-generated image variant (`100x100`, `400x`, etc.). Lazy.

**Logical document** — entity в `_documents` table. Один conceptual file.

**Document version** — physical bytes в `_document_versions`. N версий per document.

**Polymorphic owner** — document attached к любой записи через `(owner_type, owner_id)`.

**Immutable repository** — NO DELETE; только `archivedAt` (soft).

**Legal hold** — block archive до снятия. Compliance.

**Retention** — `retention_until` timestamp; auto-archival.

**Storage driver** — backend для bytes (FS / S3 / Azure / GCS).

**Signed URL** — HMAC-signed URL с expiry для public access к private file.

## Generation

**Export** — generation файла (XLSX/PDF) из коллекции данных.

**Schema-declarative export** — `.Export()` modifier на коллекции, auto-mounts endpoints.

**Template** — markdown с frontmatter + Go template syntax. Shared engine для PDF и mailer.

**Helpers** — template functions: `date`, `money`, `truncate`, `default`, `each`, `if`.

**Streaming writer** — `excelize.StreamWriter` для memory-efficient large exports.

**Async export** — large exports → jobs queue → signed URL для download.

## Audit & observability

**Audit log** — append-only `_audit_log` table. Compliance events.

**Hash chain** — каждый row hash = sha256(prev_hash || canonical_json(row)). Tamper-evident.

**Sealing** — Ed25519 signature на latest hash. Daily job. Opt-in.

**Bare pool write** — audit pишется через separate connection pool, не через request-tx, чтобы не быть rolled back.

**slog** — structured logger (Go stdlib).

**Logs as records** — application logs хранятся в `_logs` table. PB feature.

**Telemetry** — OpenTelemetry traces + metrics + Prometheus.

**Correlation** — `request_id` UUID проходит через slog + audit + OTel.

**Redaction** — passwords/tokens/2FA/secrets не логируются.

## Authority (plugin)

**Policy** — rule «для resource/action с conditions нужна chain approvers».

**Condition** — predicate `{ field, op, value }`. Operators: eq/ne/lt/lte/gt/gte/in/nin.

**Chain** — sequence of approval steps. Каждый step = role.

**Request** — instance approval-запроса. Status pending/approved/rejected/cancelled.

**Decision** — approve/reject от конкретного user'а. С rationale.

**Delegation** — temporary authority transfer A→B на period. Optional per-policy.

**R22a** — initiator MUST NOT self-approve. Hardcoded rule.

## Billing (plugin)

**Subscription** — plan + customer + status + period.

**Plan** — pricing tier (free/pro/enterprise + интервал).

**Checkout session** — Stripe Checkout / Customer Portal flow.

**Webhook** — Stripe POST на `/api/billing/webhook`. Signature verified.

**Usage event** — metered billing event (API calls, storage, etc.).

**Seat** — billing unit для multi-seat plans (с `railbase-orgs`).

## Compat modes

**`strict`** — PB API shape (URLs, auth, filters, realtime SSE). PB JS SDK работает.

**`native`** — Railbase shape (`/v1/...`, WebSocket primary, typed envelopes).

**`both`** — обе схемы (default v1; deprecated v2).

## Plugin

**Plugin** — отдельный бинарник через subprocess + gRPC (или go-plugin / WASI).

**Plugin manifest** — `plugin.yaml` с name/version/permissions.

**Plugin registry** — distribution via GitHub releases (recommendation).

**Plugin sandbox** — subprocess isolation + resource limits.

## Misc

**`pb_data/`** — data directory (PB-compat name). Configurable.

**`pb_hooks/`** — hooks directory (PB-compat name).

**`.secret`** — 32-byte master key для cookies/JWT/sealing. chmod 0600.

**`_railbase_meta`** — internal metadata table (version, init state, schema hash).

**`_*` prefix** — system tables, reserved за Railbase.

**Embedded Postgres** — `fergusstrange/embedded-postgres` integration. Скачивает Postgres binary (~50 MB) в `~/.railbase/pg/<version>/` на первом запуске; запускает как subprocess для dev/test. Активируется через `--embed-postgres` flag. **Не для production**.

**MCP** — Model Context Protocol. Для LLM agents.

**Vibe coding** — AI-assisted rapid prototyping style.
