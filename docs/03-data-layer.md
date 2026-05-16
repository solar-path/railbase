# 03 — Data layer: DB, schema, migrations, transactions, filter, pagination

## DB layer

### Driver

**`jackc/pgx/v5`** — pure Go Postgres driver. Native protocol, не database/sql wrapper (хотя `pgx/v5/stdlib` доступен где-то нужен `*sql.DB`). `pgxpool` для connection pooling.

### Минимальная версия Postgres

**PG 14+** — required для:
- `MERGE` statement (PG 15+ для full syntax; PG 14 — через INSERT ... ON CONFLICT)
- Multirange types (PG 14+)
- SQL standard JSON path operators (`@?`, `@@`)
- `pg_stat_statements` enhancements
- Logical replication v2

**PG 16+** recommended для production multi-tenant с большим volume:
- Logical replication для standby DBs
- SQL/JSON simplifications
- `EXPLAIN (GENERIC_PLAN)` для plan analysis

### Connection management

```go
// internal/db/pool/pool.go
type Pool struct {
    *pgxpool.Pool
}

// Каждый request acquires connection из pool, устанавливает RLS context:
//   SET LOCAL railbase.tenant = '<uuid>'
//   SET LOCAL railbase.user = '<uuid>'
//   SET LOCAL railbase.role = 'app_user'  -- vs 'app_admin' для bypass
// На commit / release — connection возвращается в pool (settings локальны к tx).
```

**Pool defaults** (configurable):
- `pool_max_conns = max(4, GOMAXPROCS × 2)`
- `pool_min_conns = 1`
- `pool_max_conn_lifetime = 1h`
- `pool_max_conn_idle_time = 30m`
- `health_check_period = 1m`

### Retry logic

`internal/db/retry/` — auto-retry на:
- `40001` `serialization_failure` (SERIALIZABLE conflicts)
- `40P01` `deadlock_detected`
- `08006` `connection_failure` (network blip)

Exponential backoff с jitter, max 5 retries. Configurable per-query через `WithRetry(...)`. Не retry'ит non-idempotent operations без явного opt-in.

### LISTEN/NOTIFY

Для **single-instance realtime** и **internal eventbus fan-out** core использует Postgres `LISTEN/NOTIFY`:

```go
// internal/db/listen/listener.go
type Listener struct { /* dedicated long-lived connection */ }

func (l *Listener) Listen(channel string, handler func(payload string)) error
```

Один dedicated `*pgx.Conn` (не из pool) держит `LISTEN railbase_events`. NOTIFY publishing — изнутри обычных tx после commit (deferred). На multi-instance — переключается на NATS broker через `railbase-cluster` plugin (LISTEN/NOTIFY scales до сотен тысяч events/sec на одном инстансе, но cross-instance не делит).

### Embedded Postgres (dev mode)

`railbase serve --embed-postgres` запускает встроенный Postgres binary через `fergusstrange/embedded-postgres`:

- Скачивает PG binary (~50 MB) под `<DataDir>/postgres/runtime/` (по умолчанию `pb_data/postgres/runtime/`, configurable через `RAILBASE_DATA_DIR`); PG cluster data — отдельной папкой `<DataDir>/postgres/data/` чтобы пережить рестарты (библиотека `fergusstrange/embedded-postgres` чистит runtime path на каждом `Start()`, поэтому дата вынесена за его пределы)
- Запускает как subprocess на фиксированном dev-порту (54329 для стабильного DSN между рестартами)
- На graceful shutdown — `pg_ctl stop` с timeout
- При падении PG subprocess — Railbase тоже останавливается с clear error
- Refused при `RAILBASE_PROD=true` (defence in depth, дублирует config validation)

**Не для production**. Только для:
- `railbase init demo && railbase serve --embed-postgres` workflow (5-минутный старт)
- Локальная разработка без `docker run postgres`
- Integration tests
- Distribution для desktop / single-team self-hosted

---

## Schema DSL

Fluent builder в Go-коде. Источник истины — `schema/*.go` файлы.

> **Reserved namespace.** Имена таблиц с префиксом `_` (дандер) **зарезервированы за системой**. Schema-validator (`internal/schema/builder/validate.go`) отказывает создавать user-collection с таким именем — это гарантирует что app-коллекция `_users`/`_admins`/`_jobs` физически невозможна, и системные таблицы (`_admins`, `_admin_sessions`, `_settings`, `_audit_log`, `_sessions`, `_record_tokens`, `_jobs`, `_cron`, `_files`, `_notifications`, `_notification_preferences`, `_notification_user_settings`, `_webhooks`, `_webhook_deliveries`, `_logs`, `_exports`, `_email_events`, `_audit_seals`, `_auth_origins`, `_migrations`, `_schema_snapshots`, `_admin_collections`, `_tenants`, `_roles`, `_role_actions`, `_user_roles`) живут в защищённом пространстве имён по построению. Полный список + назначение каждой — `docs/04-identity.md` (auth-track) и `docs/14-observability.md` (operational-track).

### Базовый пример

```go
// schema/posts.go
var Posts = schema.Collection("posts").
    Field("title", schema.Text().Required().MinLen(3).MaxLen(120)).
    Field("body",  schema.Text().FTS()).                  // dialect-explicit
    Field("slug",  schema.Text().Unique().Pattern(`^[a-z0-9-]+$`)).
    Field("author", schema.Relation("users").CascadeDelete()).
    Field("status", schema.Enum("draft", "published").Default("draft")).
    Field("metadata", schema.JSON()).
    Index("idx_posts_status", "status").
    ListRule(rbac.Authed()).
    CreateRule(rbac.Authed().And(rbac.OwnsField("author"))).
    UpdateRule(rbac.OwnsRecord()).
    DeleteRule(rbac.OwnsRecord()).
    Realtime(realtime.AuthedOnly()).
    Hook(schema.BeforeCreate, computeSlug).
    AllowsDocuments(...)              // optional document attachments

// для multi-tenant
var TenantPosts = schema.Collection("tenant_posts").
    Tenant().                         // injects tenant_id; required для всех queries
    Field(...)
```

Из одного объявления генерируются:
- SQL миграция (Postgres)
- sqlc-типы для типизированных queries
- JSON-schema для admin UI form rendering
- TypeScript SDK с zod-схемами
- OpenAPI spec
- Go-side model struct

> **Reserved keyword renames.** SQL reserved words (`user`, `order`,
> `group`, `select`, `primary`, …) на уровне столбца отвергаются
> валидатором с **именованной рекомендацией**:
> `field name "user" is a SQL reserved keyword — use "customer", or "user_id"/"user_ref" if the relation semantics matter`.
> Curated table (`reservedKeywordRenames` в
> `internal/schema/builder/validate.go`) покрывает ~20 распространённых
> случаев; для остального — generic `_id` / `_ref` fallback.

> **CHECK constraints на optional regex-полях принимают `''`.**
> URL / Email / Tel / Slug / Color / Text+Pattern на `!Required` поле
> генерируются как `CHECK (col = '' OR col ~ pattern)` — JSON-форма,
> постящая `hero_image: ""` для незаполненного поля, не упадёт на 400.
> Required-поля сохраняют строгий regex (пустое значение в required —
> валидная ошибка валидации).

> **`migrate diff` отслеживает `Collection ↔ AuthCollection` toggle.**
> Переключение `schema.Collection("authors")` →
> `schema.AuthCollection("authors")` (или обратно) теперь эмитирует
> миграцию: трёхшаговый ADD (nullable → backfill TODO → SET NOT NULL)
> для `email`/`password_hash`/`token_key` плюс single-line ADD для
> `verified` (DEFAULT FALSE) и `last_login_at` (NULL). На toggle-off —
> `DROP COLUMN IF EXISTS … CASCADE` × 5. См. `gen.AuthToggleSQL` в
> `internal/schema/gen/sql.go`.

### Runtime collections (admin-managed, v0.9)

Code-defined коллекции выше — основной путь и единственный источник
истины для codegen. Но v0.9 добавил **второй класс коллекций**,
создаваемых из admin UI без деплоя (см. `docs/12-admin-ui.md` ADR
«runtime collection management»).

| | Code-defined | Admin-managed (runtime) |
|---|---|---|
| Объявление | `schema/*.go` (этот DSL) | admin UI collection editor |
| Источник истины | Go-исходник | строка в `_admin_collections` |
| Регистрация в `registry` | `init()` при старте процесса | `live.Hydrate` после системных миграций (+ `live.Create` в рантайме) |
| Codegen (sqlc / SDK / OpenAPI) | да | нет |
| Редактируется из UI | нет | да |

Реализация — пакет `internal/schema/live`: `Create` / `Update` /
`Delete` оборачивают DDL-генераторы из `internal/schema/gen` плюс
upsert в `_admin_collections`, всё в одной транзакции; in-memory
`registry` мутируется только после успешного commit'а. `FromSpec` в
`internal/schema/builder` реконструирует `CollectionBuilder` из
JSON-спеки (обратное к `Spec()`), что нужно и для API-входа, и для
boot-гидратации. `Update` диффит старую/новую спеку через `gen.Compute`
и **отказывает** в несовместимых изменениях (смена типа колонки,
переключение `Tenant()`) — без молчаливой потери данных.

Запись данных в runtime-коллекцию работает сразу после создания:
record-CRUD маршруты параметрические (`/api/collections/{name}/…`) и
резолвят коллекцию из `registry` пер-реквест, ремонтировать роуты не
нужно.

### Field types

DSL signatures user-facing — стабильны. Storage column всегда Postgres-native.

| Type | DSL | Postgres column | Index strategy | Notes |
|---|---|---|---|---|
| Text | `schema.Text()` | `TEXT` | btree, GIN с pg_trgm для fuzzy | + `.MinLen()`, `.MaxLen()`, `.Pattern(regex)`, `.FTS()` (создаёт `tsvector` generated col + GIN) |
| Number | `schema.Number()` | `DOUBLE PRECISION` | btree | + `.Min()`, `.Max()`, `.Int()` для `BIGINT` |
| Bool | `schema.Bool()` | `BOOLEAN` | partial btree (`WHERE col`) | |
| Date | `schema.Date()` | `TIMESTAMPTZ` | btree, BRIN для time-series | + `.AutoCreate()`, `.AutoUpdate()` через `DEFAULT now()` / триггер |
| Email | `schema.Email()` | `TEXT` (CITEXT через extension опционально) | unique btree | RFC5322 validator |
| URL | `schema.URL()` | `TEXT` | btree | URL validator |
| **Tel** | `schema.Tel()` | `TEXT` (E.164) | btree | libphonenumber validation; см. ниже |
| **Finance** | `schema.Finance()` | `NUMERIC(20, N)` | btree | exact decimal arithmetic; см. ниже |
| **Currency** | `schema.Currency()` | composite `(amount NUMERIC, currency CHAR(3))` или JSONB | btree on currency | composite default; JSONB через `.AsJSONB()` |
| **Address** | `schema.Address()` | JSONB | GIN on `(country, postal_code)` paths | structured address с country-aware validation |
| **PersonName** | `schema.PersonName()` | composite | btree on `(last_name, first_name)` | composite name с culturally-aware formatting |
| **Percentage** | `schema.Percentage()` | `NUMERIC(7, N)` | btree | 0-100 scale, semantic precision |
| **MoneyRange** | `schema.MoneyRange()` | composite `(min currency_t, max currency_t)` или JSONB | btree | min/max |
| **IBAN** | `schema.IBAN()` | `TEXT` + CHECK (mod-97) | unique btree | country format |
| **BIC** | `schema.BIC()` | `TEXT` + CHECK | btree | SWIFT format |
| **BankAccount** | `schema.BankAccount()` | composite или JSONB | btree | (IBAN/account + BIC + bank + currency) |
| **TaxID** | `schema.TaxID()` | composite `(country, type, value)` | unique btree | multi-country (ИНН/КПП/EIN/VAT/...) |
| **Slug** | `schema.Slug()` | `TEXT` | unique btree | URL-safe с auto-gen |
| **SequentialCode** | `schema.SequentialCode()` | `TEXT` (atomic counter в `_sequence_counters`) | unique btree | auto-numbered с pattern |
| **Barcode** | `schema.Barcode()` | `TEXT` + format CHECK | btree | UPC/EAN-13/ISBN/GTIN |
| **Country** | `schema.Country()` | `CHAR(2)` | btree | ISO 3166-1 alpha-2 |
| **Language** | `schema.Language()` | `CHAR(2)` | btree | ISO 639-1 |
| **Timezone** | `schema.Timezone()` | `TEXT` | btree | IANA TZ |
| **Locale** | `schema.Locale()` | `TEXT` | btree | BCP 47 |
| **Coordinates** | `schema.Coordinates()` | `POINT` (или JSONB через `.AsJSONB()`) | GiST | lightweight geo; full PostGIS — opt-in |
| **Quantity** | `schema.Quantity()` | composite `(amount NUMERIC, unit TEXT)` | btree on unit | amount + UOM с conversion |
| **Duration** | `schema.Duration()` | `INTERVAL` | btree | native interval, date arithmetic в SQL работает |
| **DateRange** | `schema.DateRange()` | `DATERANGE` | GiST (`USING gist (period)`) | native overlap operator `&&`, `EXCLUDE` constraints |
| **TimeRange** | `schema.TimeRange()` | `TSRANGE` или `TSTZRANGE` | GiST | native overlap |
| **Status** | `schema.Status()` | `TEXT` (или ENUM type через `.AsEnum()`) | btree | state machine с allowed transitions |
| **Priority** | `schema.Priority()` | `TEXT` | btree | levels с metadata |
| **Rating** | `schema.Rating()` | `SMALLINT` | btree | 1-N stars |
| **TreePath** | `schema.TreePath()` | `LTREE` | GiST | native materialized path; `<@` / `~` / `?` operators |
| **Tags** | `schema.Tags()` | `TEXT[]` | GIN | native arrays, не JSONB |
| **Markdown** | `schema.Markdown()` | `TEXT` | tsvector + GIN для FTS | long-form markdown (не WYSIWYG) |
| **Color** | `schema.Color()` | `TEXT` (`#RRGGBB`) + CHECK | btree | hex/RGB/HSL с picker |
| **Cron** | `schema.Cron()` | `TEXT` + CHECK (cron parser) | btree | cron expression с UI builder |
| **QRCode** | `schema.QRCode()` | `TEXT` (encoded value); image generated on-demand | btree | render PNG/SVG/PDF on-demand, ECC levels, optional logo |
| Select (single) | `schema.Enum("a", "b")` | native `ENUM` type или `TEXT + CHECK` | btree | |
| Select (multi) | `schema.MultiSelect("a", "b")` | `TEXT[]` | GIN | + min/max selections |
| File (single) | `schema.File()` | `TEXT` (path) | btree | + size/mime/thumbnails в metadata table |
| File (multi) | `schema.Files()` | `JSONB` (array of file refs) | GIN | |
| Relation (single) | `schema.Relation("users")` | `UUID` или `TEXT` (FK) | btree, FK constraint | + `.CascadeDelete()`, `.Required()` |
| Relation (multi) | `schema.Relations("tags")` | через junction table (`m2m`) или `UUID[]` | GIN если array, btree если junction | |
| JSON | `schema.JSON()` | `JSONB` | GIN | typed via Go generic: `schema.JSONOf[MyType]()` |
| RichText | `schema.RichText()` | `TEXT` | tsvector + GIN | sanitized HTML (bluemonday), FTS на plain text |
| Password | `schema.Password()` | `TEXT` (Argon2id hash) | — | only для auth collections; no read API |
| Vector | `schema.Vector(1536)` | `vector(1536)` (pgvector) | HNSW или IVFFlat | first-class через extension |
| Geo | `schema.GeoPoint()` | `geography(POINT, 4326)` (PostGIS) | GiST | opt-in via `railbase-geo` plugin |

### Computed fields

```go
schema.Field("full_name", schema.Computed[string](
    func(r *Record) any { return r.GetString("first") + " " + r.GetString("last") },
))
```

Хранится / не хранится — выбор пользователя (`.Stored()` для materialized).

---

## Domain-specific field types

PB не имеет этих типов из коробки — пользователь городит через `text` + custom validators + ручное форматирование. Railbase делает их first-class. Критично для B2B SaaS / fintech / ERP / CRM / HR / e-commerce проектов.

### Tel field

Телефонный номер с E.164 storage и libphonenumber validation.

```go
schema.Field("phone", schema.Tel().
    Region("US").                  // default region для parsing user input
    MobileOnly().                  // accept только mobile numbers
    Required())

schema.Field("contact", schema.Tel().
    Region("RU").
    AnyType())                     // mobile или landline
```

**Storage**: E.164 normalized string (`+15551234567`). Никаких форматированных вариаций в БД — единый формат.

**Validation library**: `nyaruka/phonenumbers` (pure Go port of Google libphonenumber). No CGo.

**Modifiers**:
- `.Region("US"|"RU"|...)` — default country для parsing когда user не указал country code
- `.MobileOnly()` / `.LandlineOnly()` / `.AnyType()` — type filter
- `.RequireValid()` — fail validation если parser не может распознать
- `.Required()` / `.Unique()`

**Input handling**: API принимает любой формат (`(555) 123-4567`, `+1 555 123 4567`, `5551234567` с `Region("US")`); парсится в E.164 для storage.

**Output**: API возвращает E.164. SDK предоставляет helpers для форматирования (`formatNational`, `formatInternational`, `formatRFC3966`).

**Search**: exact match по E.164; FTS на formatted variants (если включено).

**Admin UI**: country-code dropdown + national number input; live validation как user печатает.

**Use cases**:
- CRM contacts (mobile + work + home)
- 2FA SMS delivery (тут tel должен быть mobile)
- Verified phone для auth
- Logistics shipping contacts

---

### Finance field

Decimal precision money type. **НЕ float** — float ломается на округлениях (`0.1 + 0.2 ≠ 0.3` в финансах не приемлемо).

```go
schema.Field("amount", schema.Finance().
    Precision(2).                  // 2 decimal places (default)
    NonNegative())

schema.Field("rate", schema.Finance().
    Precision(8).                  // высокая точность для FX rates / crypto
    Min("0").
    Max("1000000"))

schema.Field("salary", schema.Finance().
    Precision(2).
    Positive())
```

**Internal Go type**: `decimal.Decimal` (`shopspring/decimal` library — de-facto standard для Go decimal arithmetic; используется для wire format и SDK-side math).

**Storage**: `NUMERIC(20, N)` — native Postgres decimal, exact arithmetic, indexable, sortable, aggregatable без CAST.

**Modifiers**:
- `.Precision(N)` — decimal places (default 2; common: 2 для money, 4 для FX rates, 8 для crypto)
- `.Min(string)` / `.Max(string)` — границы (передаются как strings чтобы не было float-ambiguity)
- `.NonNegative()` / `.Positive()` — sign constraints
- `.Required()` / `.Index()`

**Operations** (через generated Go-side helpers):

```go
total := record.GetFinance("amount").Add(other.GetFinance("amount"))
record.SetFinance("total", total)

// Comparison без float ambiguity
if record.GetFinance("amount").GreaterThan(decimal.RequireFromString("100.00")) { ... }
```

**SDK side**:
```ts
// stored как string в JSON wire format ("123.45")
import Decimal from "decimal.js"
const total = new Decimal(record.amount).plus(other.amount)
```

SDK ships с `decimal.js` peer dependency — те же гарантии на клиенте.

**Aggregation**: native — `SELECT SUM(amount) FROM payments`. Generated query helpers экспонируют typed API:

```go
total, err := payments.SumAmount(ctx, db, payments.SumAmountFilter{Status: "paid"})
// returns decimal.Decimal
```

**Use cases**:
- Accounting GL entries (debits / credits)
- Payroll / salary
- Tax computations (rates с высокой precision)
- Invoice line items (amount × quantity без drift)
- Inventory cost basis
- Crypto holdings (8-9 precision)

---

### Currency field

Composite type: **amount (decimal) + ISO 4217 currency code**. Standard money representation.

```go
schema.Field("price", schema.Currency())                            // any currency

schema.Field("subscription_price", schema.Currency().
    AllowedCurrencies("USD", "EUR", "GBP").
    DefaultCurrency("USD"))

schema.Field("balance", schema.Currency().
    AllowedCurrencies("USD", "EUR", "RUB", "CNY", "JPY").
    AutoPrecision())                                                // USD=2, JPY=0, etc.
```

**Wire format** (JSON):
```json
{ "amount": "1234.56", "currency": "USD" }
```

**Storage**: native composite type по умолчанию:

```sql
CREATE TYPE currency_value AS (amount NUMERIC(20,8), code CHAR(3));
ALTER TABLE products ADD COLUMN price currency_value;

CREATE INDEX ON products ((price).code);          -- index on currency code
CREATE INDEX ON products ((price).amount);        -- index on amount
```

Filter работает по полям композита: `WHERE (price).code = 'USD' AND (price).amount > 100`. SDK переводит filter expression `price.currency = 'USD'` в этот SQL.

Опционально через `.AsJSONB()` — JSONB представление (когда нужен schema flexibility, e.g. mixed-currency aggregations с tagged amount).

**Modifiers**:
- `.AllowedCurrencies("USD", "EUR", ...)` — whitelist (по умолчанию все ISO 4217 валидны)
- `.DefaultCurrency("USD")` — fallback если не указана
- `.Precision(N)` — fixed precision overriding per-currency default
- `.AutoPrecision()` — использовать ISO 4217 minor unit (USD=2, JPY=0, BHD=3, BTC=8 для crypto extension)
- `.Required()` / `.Index()` (индексируется по `currency` part)

**Validation**:
- Currency code: 3 ASCII letters, в ISO 4217 list (built-in catalog включая crypto: BTC, ETH, USDC и т.д. опционально)
- Amount: validated как Finance (decimal)

**Currency catalog** (built-in):
- Все ISO 4217 fiat codes (~180 currencies)
- Crypto opt-in через `--with-crypto-currencies` flag (BTC, ETH, USDC, USDT, BNB, ...)

**Per-currency precision** (auto):
```
USD, EUR, GBP, ...     2 decimals
JPY, KRW, ISK, ...     0 decimals
BHD, JOD, KWD, ...     3 decimals
BTC                    8 decimals
ETH                    18 decimals (но обычно truncated)
```

**Operations**:

```go
price := record.GetCurrency("price")        // returns CurrencyValue{Amount, Code}
total := price.Add(other)                    // panics или error если currencies different
totalConv, err := price.AddWithFX(other, fxProvider)  // auto-convert
```

**Mixed-currency arithmetic** запрещено по умолчанию (защита от silent bugs). Для conversion — explicit FX provider.

**FX integration** (optional plugin `railbase-fx`):
- Live exchange rates через ECB / Fixer / Open Exchange Rates / custom provider
- Cached с TTL
- Historical rates для accounting (rate at transaction time, не today's)
- API: `$fx.convert({ from: "USD", to: "EUR", amount: "100", at: timestamp })`

**Display formatting** (locale-aware, в SDK):
```ts
formatCurrency(record.price)
// → "$1,234.56"  (en-US, USD)
// → "1 234,56 ₽" (ru-RU, RUB)
// → "￥1,235"    (ja-JP, JPY — no decimals)
```

**Aggregation across currencies**:
- Same currency: trivial sum
- Mixed currencies: требует explicit conversion target и FX rates
- Generated helpers: `payments.SumByCurrency()` returns map; `payments.SumIn("USD", fxAt: ...)` converts

**Use cases**:
- Multi-currency invoicing (B2B SaaS billing)
- Treasury / bank accounts (per-currency balances)
- E-commerce pricing (regional prices)
- Subscription tiers (USD primary, EUR/GBP localized)
- Expense reports (mixed currencies, converted at submission)
- Accounting (functional currency + transactional currency)

---

### Domain-specific types в filter expression

```
status = 'paid' && amount.amount > '100' && amount.currency = 'USD'
```

`Currency` field exposes `.amount` и `.currency` paths в filter (как embedded JSON).

```
phone ~ '+1555'    -- LIKE для tel (matches all +1555... numbers)
```

### Domain-specific types в TS SDK

См. [11-frontend-sdk.md](11-frontend-sdk.md#domain-specific-types).

### Domain-specific types в Admin UI

Field editors имеют specialized UX (country picker для tel, decimal input с currency dropdown, locale-aware formatting). См. [12-admin-ui.md](12-admin-ui.md#3-record-editor--modal-или-full-page).

### Зависимости (для domain-specific types)

| Тип | Library |
|---|---|
| Tel | `nyaruka/phonenumbers` (pure Go) |
| Finance / Currency amount | `shopspring/decimal` |
| Currency catalog | embedded ISO 4217 data + opt-in crypto list |
| FX rates (plugin) | `railbase-fx` plugin с adapters (ECB / Fixer / OXR / custom) |
| Country / Language | embedded ISO 3166-1, ISO 639-1 catalogs |
| Timezone | embedded IANA TZ database |
| Locale | embedded BCP 47 / RFC 5646 |
| IBAN | custom (mod-97 checksum) + embedded country format catalog |
| BIC | custom + optional bank-directory plugin |
| Address | custom (country-aware validation) + optional `railbase-geocode` plugin |
| Tax ID | custom + embedded per-country format catalog |
| Person name | custom (CLDR culturally-aware data) |
| Tree path | materialized path pattern via core SQL |

---

## ERP-specific field types — детально

### Address field

```go
schema.Field("billing_address", schema.Address().
    Required().
    Country("RU"))                       // restrict to Russia (validation)

schema.Field("shipping_address", schema.Address())  // any country
```

**Wire format**:
```json
{
  "country": "US",
  "state": "CA",
  "city": "San Francisco",
  "street1": "123 Main St",
  "street2": "Apt 4B",
  "postal_code": "94102",
  "lat": 37.7749,
  "lng": -122.4194
}
```

**Storage**: JSONB с GIN index на `(country, postal_code)` paths.

**Validation**:
- Country-aware: postal code regex per country (`^\d{5}(-\d{4})?$` для US, `^\d{6}$` для RU)
- State validation для countries with subdivisions (US states, RU subjects)
- `street1` required (когда address required)

**Modifiers**:
- `.Country(code)` — restrict
- `.RequireGeocode()` — fail если geocoding fails (через optional plugin)
- `.AllowedCountries(...)` — whitelist

**Use cases**: vendor master, customer addresses, shipping endpoints, employee residence

---

### TaxID field

```go
schema.Field("vendor_tax_id", schema.TaxID().Country("RU"))   // ИНН/КПП validation per RU
schema.Field("customer_vat", schema.TaxID().Country("DE"))   // German VAT
schema.Field("us_ein", schema.TaxID().Country("US").Type("EIN"))
```

**Wire format**:
```json
{ "country": "RU", "type": "INN", "value": "7707083893" }
```

**Per-country catalog** (built-in):
- **RU**: ИНН (10/12 digits с checksum), КПП (9 digits), ОГРН/ОГРНИП (13/15 digits)
- **US**: EIN (XX-XXXXXXX), SSN (XXX-XX-XXXX), ITIN
- **EU**: VAT по странам (DE: DE+9 digits, FR: FR+11 chars, IT: IT+11 digits, etc.) с VIES validation hook
- **UK**: VAT (GB+9 digits), UTR
- **CA**: BN (9 digits + RT/RP suffix)
- **AU**: ABN (11 digits с checksum)
- **CN**: USCC (18 chars unified social credit code)
- **IN**: GSTIN (15 chars), PAN (10 chars)
- **BR**: CNPJ (14 digits), CPF (11 digits)
- ~30 стран в initial catalog

**Modifiers**:
- `.Country(code)` — restrict country
- `.Type(code)` — specific format (когда страна имеет несколько типов)
- `.Verify()` — opt-in online verification (VIES для EU VAT, GSTIN portal для India) через plugin

---

### IBAN field

```go
schema.Field("iban", schema.IBAN())
schema.Field("eu_iban", schema.IBAN().AllowedCountries("DE", "FR", "IT", "ES"))
```

**Wire format**: canonical IBAN string (`DE89370400440532013000`).

**Storage**: TEXT (uppercase, без spaces).

**Validation**:
- Length per country (немецкий 22, французский 27, etc.)
- Mod-97 checksum (ISO 7064)
- Country code in IBAN registry

**Display**: SDK formatter — `DE89 3704 0044 0532 0130 00` (groups of 4).

---

### BIC field

```go
schema.Field("bic", schema.BIC())
```

**Wire format**: 8 или 11 chars (`DEUTDEFF` или `DEUTDEFFXXX`).

**Validation**: regex + optional bank directory lookup (через `railbase-bank-directory` plugin).

---

### BankAccount field

Composite — IBAN/account-number + BIC + bank name + currency.

```go
schema.Field("payroll_account", schema.BankAccount().
    RequireIBAN().                                // EU mode
    DefaultCurrency("EUR"))

schema.Field("us_account", schema.BankAccount().
    Format(schema.BankAccountUS).                  // routing + account number
    DefaultCurrency("USD"))
```

**Wire format (EU/IBAN)**:
```json
{
  "iban": "DE89370400440532013000",
  "bic": "DEUTDEFF",
  "bank_name": "Deutsche Bank",
  "currency": "EUR",
  "holder_name": "Acme GmbH"
}
```

**Wire format (US)**:
```json
{
  "routing_number": "021000021",
  "account_number": "12345678",
  "account_type": "checking",
  "bank_name": "JPMorgan Chase",
  "currency": "USD",
  "holder_name": "Acme Corp"
}
```

**Use cases**: payroll deposits, vendor payments, treasury accounts, refunds.

---

### Country / Language / Timezone / Locale fields

```go
schema.Field("primary_country", schema.Country())                                     // ISO 3166-1 alpha-2
schema.Field("user_language", schema.Language())                                      // ISO 639-1
schema.Field("default_tz", schema.Timezone())                                         // IANA
schema.Field("locale_pref", schema.Locale())                                          // BCP 47

schema.Field("supported_countries", schema.Countries())                                // multi-select
```

**Storage**: TEXT (codes); validated against embedded catalog.

**SDK helpers**: lookup display name, native name, flag emoji, currency для country, etc.

**Use cases**: user profiles, tenant settings, address validation, i18n routing.

---

### Person name field

```go
schema.Field("contact_name", schema.PersonName())
```

**Wire format**:
```json
{
  "prefix": "Dr.",
  "first": "Иван",
  "middle": "Сергеевич",
  "last": "Петров",
  "suffix": "PhD",
  "preferred": "Иван"
}
```

**Display**: SDK formatter culturally-aware:
- Western: `Dr. Ivan Petrov, PhD`
- Russian formal: `Петров Иван Сергеевич`
- Japanese: `[Family] [Given]`
- Etc.

**Modifiers**:
- `.RequireFirst()` / `.RequireLast()`
- `.PreferredOnly()` — single field, без composite

**Search**: FTS на all parts; admin UI sort by `last, first` for typical use.

**Use cases**: HR employee records, CRM contacts, customer accounts.

---

### Percentage field

```go
schema.Field("tax_rate", schema.Percentage().Precision(4))    // up to 0.0001%
schema.Field("discount", schema.Percentage().Min("0").Max("100"))
schema.Field("margin", schema.Percentage().Signed())          // can be negative
```

**Storage**: decimal как Finance (NUMERIC / TEXT). Internal: 0-100 scale (не 0-1 — менее ambiguous для humans). Convertible через `.AsRatio()` для math.

**UI**: input с % suffix; user types `15` → stored as `15.0000`. Display: `15%` или `15.00%` с precision.

**Use cases**: tax rates, commission %, discount, margin, growth rate, ROI.

---

### Quantity field

```go
schema.Field("inventory_qty", schema.Quantity().Unit("pcs"))            // strict pcs
schema.Field("weight", schema.Quantity().UnitGroup("mass"))              // any mass unit
schema.Field("flexible", schema.Quantity())                              // any unit
```

**Wire format**:
```json
{ "amount": "10.5", "unit": "kg" }
```

**Built-in unit catalog** (extensible):
- **Mass**: g / kg / t / oz / lb / mt
- **Length**: mm / cm / m / km / in / ft / yd / mi
- **Volume**: ml / L / m³ / cup / pint / gal
- **Area**: m² / km² / ft² / acre / hectare
- **Time**: sec / min / hour / day / week / month / year
- **Pieces**: pcs / dozen / case / pallet
- **Energy**: J / kJ / kWh / cal / kcal / BTU
- **Currency**: ISO 4217 (overlaps с Currency field)
- Custom units через DSL

**Conversion**: SDK + Go API делает conversion when comparing/aggregating across units (`5 kg + 500 g → 5.5 kg`).

**Use cases**: inventory items, BOM (bill of materials), shipping weights/dimensions, recipe ingredients, warehouse logistics.

---

### Duration field

```go
schema.Field("contract_term", schema.Duration())
schema.Field("vacation_days", schema.Duration().Unit("days"))
```

**Wire format**: ISO 8601 (`P1Y6M` = 1 year 6 months, `PT2H30M` = 2.5 hours).

**Display**: SDK formatter — humanized («1 year 6 months», «2.5 hours», locale-aware).

**Math**: add to date returns date; subtract dates returns duration.

**Use cases**: contract terms, leave entitlements, project timelines, billing intervals, SLA definitions.

---

### DateRange / TimeRange fields

```go
schema.Field("contract_period", schema.DateRange().Required())
schema.Field("business_hours", schema.TimeRange())
schema.Field("vacation", schema.DateRange().MaxDuration("P30D"))
```

**Wire format**:
```json
{ "start": "2026-01-01", "end": "2026-12-31" }
```

**Validation**: `end >= start`; max/min duration constraints.

**Conflict detection** (helper для booking/scheduling):
```go
overlapping := dates.RangeOverlap(ctx, "vacation", req.Start, req.End)
```

**Use cases**: contracts, leave requests, fiscal periods, project phases, booking systems, subscriptions.

---

### Status field (state machine)

Отличается от plain `Enum`: имеет **explicit allowed transitions**.

```go
schema.Field("status", schema.Status().
    States("draft", "submitted", "approved", "rejected", "paid", "cancelled").
    Initial("draft").
    Transition("draft", "submitted").
    Transition("submitted", "approved", "rejected").
    Transition("approved", "paid", "cancelled").
    Transition("rejected", "draft").                     // resubmit
    OnEnter("submitted", notifyApprovers).
    OnEnter("paid", recordPayment))
```

**Behaviour**:
- Update `status` → проверка allowed transition; illegal → 400 с message
- `OnEnter`/`OnExit` callbacks выполняются при transitions
- Audit row на каждое transition с from/to/actor
- Realtime event с transition info
- Admin UI shows visual state graph + current state highlighted

**Integration с authority** (если plugin): transitions могут require approvals.

**Use cases**: invoices, orders, requisitions, leave requests, hiring pipelines, support tickets.

---

### Hierarchical data — 4 patterns + DAG

Иерархические данные везде в ERP/CMS/HR: chart of accounts, departments, categories, BOM, comments, file trees, geo. Каждый pattern имеет trade-offs — нужно правильно выбирать.

**Railbase ships все 4 patterns** в core, плюс DAG support для multi-parent cases.

#### Сравнение patterns

| Pattern | Read children | Read subtree | Insert | Move subtree | Storage cost | Когда |
|---|---|---|---|---|---|---|
| **Adjacency list** | O(1) per level (recursive CTE для глубины) | recursive CTE | O(1) | O(1) | minimal (parent_id) | trees < 10k nodes; high write rate; simple cases |
| **Materialized path (TreePath)** | string-prefix | string-prefix | O(1) | O(N) for N descendants | TEXT path field | trees < 100k; mostly-stable structure; deep reads dominant |
| **Nested set** | range query | range query | O(N) shifts left/right | O(N) | left/right ints | read-heavy; rare moves; reports/aggregations across subtrees |
| **Closure table** | O(1) | O(1) | O(depth) inserts in closure | O(depth × subtree-size) | additional table (ancestor, descendant) | balanced read/write; multi-parent (DAG) support; large trees |
| **DAG (closure-based)** | O(1) | O(1) | O(depth × parents) | O(complex) | larger closure | multi-parent (BOM, dependencies) |

#### Pattern 1: Adjacency list (simplest)

Self-referential `Relation()` на ту же коллекцию. Работает out-of-box с PB-paritет field types.

```go
schema.Collection("comments").
    Field("body", schema.Text()).
    Field("parent", schema.Relation("comments").CascadeDelete())   // self-reference
```

**Helpers** (built-in core, через recursive CTE):
```go
import "github.com/railbase/railbase/pkg/railbase/tree"

children := tree.Children(ctx, "comments", commentID)             // direct (parent = ?)
descendants := tree.Descendants(ctx, "comments", commentID, 5)    // depth limit
ancestors := tree.Ancestors(ctx, "comments", commentID)
roots := tree.Roots(ctx, "comments")                              // parent IS NULL
depth := tree.Depth(ctx, "comments", commentID)
```

**Recursive CTE** generated для Postgres (`WITH RECURSIVE descendants AS (...)`).

**Pros**: simple, fast writes, low storage. **Cons**: queries требуют recursion → slower than direct lookup на больших trees.

**Use cases**: comments threads, simple categories, conversations.

---

#### Pattern 2: Materialized path (TreePath, native LTREE)

```go
schema.Collection("departments").
    Field("name", schema.Text()).
    Field("path", schema.TreePath())                       // 'root.engineering.backend'

schema.Collection("accounts").                             // chart of accounts
    Field("code", schema.Text()).
    Field("name", schema.Text()).
    Field("path", schema.TreePath())
```

**Storage**: native PostgreSQL `LTREE` тип с GiST индексом. Separator всегда `.` (LTREE convention; не настраивается).

**Queries** — native LTREE operators, не emulation:
```go
children := tree.PathChildren(ctx, "root.eng")             // path ~ 'root.eng.*{1}' (exactly one level deeper)
descendants := tree.PathDescendants(ctx, "root.eng")       // path <@ 'root.eng' (descendant operator)
ancestors := tree.PathAncestors(ctx, "root.eng.backend")   // 'root.eng.backend' @> path (ancestor)
siblings := tree.PathSiblings(ctx, "root.eng.backend")     // subpath(path, 0, -1) = 'root.eng'
match := tree.PathMatch(ctx, "*.eng.*")                     // lquery pattern matching
```

**Operations**: move subtree через `UPDATE departments SET path = 'newroot' || subpath(path, nlevel('oldroot')) WHERE path <@ 'oldroot'` — atomic single SQL. Restructure, depth limits (`nlevel(path) <= N`), max children.

**Pros**: very fast reads (GiST index covers all LTREE operators); subtree move = single UPDATE; native в Postgres. **Cons**: separator только `.` (label restriction: `[A-Za-z0-9_]+`); path labels не могут содержать произвольные symbols → нужна транслитерация для display values.

**Use cases**: chart of accounts (mostly stable), file system, geo hierarchies, product categories, organization tree.

---

#### Pattern 3: Nested set

Left/Right integer values via traversal pre-order. Read subtree = single range query.

```go
schema.Collection("categories").
    Field("name", schema.Text()).
    NestedSet()                                            // adds lft, rgt, depth columns
```

**Storage**: lft INTEGER, rgt INTEGER, depth INTEGER, все indexed.

**Queries**:
```go
descendants := tree.NestedDescendants(ctx, "categories", catID)
// → SELECT * WHERE lft > parent.lft AND rgt < parent.rgt
ancestors := tree.NestedAncestors(ctx, "categories", catID)
// → SELECT * WHERE lft < child.lft AND rgt > child.rgt
```

**Operations**: insert/move expensive (shift left/right values); read-heavy reports super fast.

**Pros**: best read performance для subtree aggregations (`SELECT SUM(amount) FROM accounts WHERE lft BETWEEN ?...?`). **Cons**: writes expensive.

**Use cases**: reporting hierarchies, nested categories с rare structure changes, GL aggregation.

---

#### Pattern 4: Closure table

Separate table со всеми (ancestor, descendant, depth) парами. Best read и write balance.

```go
schema.Collection("org_chart").
    Field("name", schema.Text()).
    Closure()                                              // creates _closure_org_chart table
```

**Storage**: main table (id, ...) + closure table `(ancestor, descendant, depth)`.

**Queries**:
```go
descendants := tree.ClosureDescendants(ctx, "org_chart", nodeID)   // JOIN closure WHERE ancestor = ?
ancestors := tree.ClosureAncestors(ctx, "org_chart", nodeID)
path := tree.ClosurePath(ctx, "org_chart", from, to)              // shortest path
```

**Operations**: insert = O(depth) closure rows; move subtree = O(depth × subtree size).

**Pros**: O(1) reads для children/descendants/ancestors; supports DAG. **Cons**: storage overhead (closure table может grow large для deep trees).

**Use cases**: large hierarchies с balanced read/write, dependency graphs, multi-parent structures.

---

#### Pattern 5: DAG (Directed Acyclic Graph)

Multi-parent allowed. Closure table — natural fit.

```go
schema.Collection("bom").                                  // bill of materials
    Field("part_number", schema.Text()).
    Field("name", schema.Text()).
    DAG().                                                  // closure-based, multiple parents OK
    PreventCycles()                                         // validates no cycle on insert
```

**Operations**:
```go
import "github.com/railbase/railbase/pkg/railbase/dag"

dag.AddEdge(ctx, "bom", parent, child)                     // can have multiple parents
dag.RemoveEdge(ctx, "bom", parent, child)
dag.HasCycle(ctx, "bom")                                    // integrity check
dag.TopologicalSort(ctx, "bom")                             // valid build order
```

**Use cases**: bill of materials (one part used in multiple assemblies), task dependencies, software dependencies, ML pipelines.

---

#### Common patterns across all hierarchies

**Ordered children** — children с explicit ordering (drag-drop):
```go
schema.Collection("nav_items").
    Field("title", schema.Text()).
    Field("parent", schema.Relation("nav_items")).
    Ordered()                                               // adds sort_index, auto-managed
```

**Depth limits**:
```go
schema.Collection("comments").
    Field("parent", schema.Relation("comments")).
    MaxDepth(5)                                             // reject deeper insertions
```

**Cycle prevention**: built-in для adjacency list / DAG; for materialized path enforced by path uniqueness.

**Integrity check job**: scheduled `_railbase.tree_integrity` runs nightly; verifies no orphans, no cycles, depth columns correct, closure table consistent.

#### Admin UI

См. [12-admin-ui.md](12-admin-ui.md):
- **Tree view** для adjacency / materialized / nested set: expand/collapse, drag-drop reordering (если `.Ordered()`), search-in-tree
- **Closure / DAG view**: graph visualization (d3-force / ELK layout), click node → expand
- **Move subtree** confirmation modal с preview of affected records (для expensive operations)

#### Choosing pattern

```
Тип данных              | Recommend
------------------------|---------
Comments / threads      | Adjacency list (simple, write-frequent)
File system             | Materialized path (read-heavy, deep trees)
Chart of accounts       | Materialized path (stable, aggregations)
Departments / org chart | Materialized path или Closure (depends on size)
Categories (CMS)        | Nested set (read-heavy reports, rare moves)
GL aggregation          | Nested set (range query)
Large org tree (10k+)   | Closure (balanced, scales)
BOM                     | DAG (closure)
Permissions inheritance | Closure (balanced)
Geo hierarchy           | Materialized path (stable)
```

**Use cases в ERP**: chart of accounts, departments hierarchy, product categories, BOM tree, file system, organization structure, comments threads, dependency graphs, geo hierarchies, permissions inheritance.

---

### Tags field

```go
schema.Field("tags", schema.Tags().
    MaxCount(10).
    AllowNew(true).                                       // user может создавать новые
    AutoComplete(true))                                   // suggest existing
```

**Storage**: JSON array of strings (tag values); side-table `_tags(collection, tag)` для autocomplete.

**Admin UI**: chip input с autocomplete; type-ahead suggestions из existing values.

**Search**: tag → records query через index.

**Use cases**: content tagging, customer segmentation, product attributes, ticket labels.

---

### Slug field

```go
schema.Field("slug", schema.Slug().From("title"))         // auto-generate из title
schema.Field("custom_slug", schema.Slug().Manual())       // manual entry
```

**Auto-generation**:
- `title="Hello World!"` → `hello-world`
- Unicode → ASCII via transliteration (ru → en, jp → en, etc.)
- Conflict resolution: `hello-world-2`, `hello-world-3`

**Validation**: pattern `^[a-z0-9]+(?:-[a-z0-9]+)*$` (configurable).

**Update behaviour**: на change source field, slug либо auto-updates (default off — breaks URLs) либо stays. Configurable.

**Use cases**: URL paths, identifiers visible to humans, file names, API keys.

---

### SequentialCode field

Auto-generated с pattern и atomic counter.

```go
schema.Field("po_number", schema.SequentialCode().
    Pattern("PO-{YYYY}-{NNNN}").                          // PO-2026-0042
    Tenant().                                              // counter per-tenant
    ResetYearly())                                         // counter resets каждый год

schema.Field("invoice_no", schema.SequentialCode().
    Pattern("INV-{YYYYMM}-{NNNNN}").
    PadZeros(5))
```

**Pattern tokens**:
- `{YYYY}` — 4-digit year
- `{YYYYMM}` — year+month
- `{NNNN}` — sequential counter, padded
- `{TENANT}` — short tenant code
- `{COLLECTION}` — collection prefix

**Counter storage**: `_sequence_counters(name, tenant_id, period_key, current)` — atomic UPDATE с RETURNING.

**Reset modes**: never / yearly / monthly / daily / on-demand (admin trigger).

**Use cases**: PO numbers, invoice numbers, ticket IDs, order numbers, employee IDs.

---

### Markdown field

Long-form markdown, не WYSIWYG (для technical docs, comments, descriptions).

```go
schema.Field("description", schema.Markdown())
schema.Field("article_body", schema.Markdown().FTS())     // FTS на rendered text
```

**Storage**: TEXT (raw markdown).

**SDK**: ships markdown renderer helper для display.

**Admin UI**: split editor (raw + preview) с Monaco или CodeMirror.

**FTS**: indexed на rendered plaintext (heading text, paragraph content).

**Use cases**: long descriptions, knowledge base articles, comments, release notes, README-style content.

---

### QRCode field

Critical для ERP: invoice payment QR (СБП/EPC/PIX), inventory labels, asset tracking, event tickets, vCard, login QR.

```go
schema.Field("payment_qr", schema.QRCode().
    EncodeFrom("payment_url").                             // encode value of another field
    Size(300).                                              // pixel size for raster
    ErrorCorrection(schema.QRMedium))                       // Low / Medium / High / VeryHigh

schema.Field("inventory_label", schema.QRCode().
    EncodeFrom("sku").
    Format(schema.QRSVG).                                   // PNG | SVG | PDF (vector for printing)
    Logo("/static/logo.png").                               // optional center logo
    Size(400))

schema.Field("manual_qr", schema.QRCode())                  // user types value, QR rendered
```

**Storage**: TEXT (encoded value). Image **не stored** — generated on-demand and cached.

**Library**: `skip2/go-qrcode` (pure Go, no CGo) для basic; `yeqown/go-qrcode` для logo overlay support.

**Modifiers**:
- `.EncodeFrom(field)` — value автоматически берётся из другого field (computed)
- `.Size(pixels)` — для PNG raster
- `.Format(QRPng | QRSVG | QRPdf | QREPS)` — output format
- `.ErrorCorrection(level)` — Low (~7%) / Medium (~15%) / High (~25%) / VeryHigh (~30%)
- `.Logo(path)` — center logo overlay (требует ECC ≥ Medium)
- `.Margin(modules)` — quiet zone
- `.ColorFG(hex) / .ColorBG(hex)` — custom colors

**Endpoints** (auto-mounted при QRCode field):
```
GET /api/files/{collection}/{record_id}/{field}.png        — raster
GET /api/files/{collection}/{record_id}/{field}.svg        — vector
GET /api/files/{collection}/{record_id}/{field}.pdf        — print-ready
GET /api/files/{collection}/{record_id}/{field}.png?size=600  — override size
```

С signed URLs (см. [07-files-documents.md](07-files-documents.md#signed-urls)) для controlled access.

**Cache**: rendered images cached в storage с key `qr:{record_id}:{field}:{format}:{size}:{hash(value)}`. Invalidated при value change.

**Use cases в ERP**:
- Invoice payment QR (национальные стандарты — см. plugin)
- Inventory item label с SKU
- Asset tracking (fixed assets, equipment)
- Event tickets / passes
- vCard contact share
- Login QR (mobile companion app — scan для desktop login)
- Restaurant menu QR
- Warehouse pick-list (route through warehouse)
- Document tracking (each generated PDF имеет QR с document ID)

### QR scan inverse

REST endpoint для receive scanned data:
```
POST /api/qr/scan
{ "value": "PO-2026-0042" }
→ resolves к record (если matches sequential_code или другие indexed fields)
```

JS hooks:
```js
onQRScanned((e) => {
  const record = $app.dao().findRecordsByFilter("orders", `code = '${e.value}'`).at(0)
  if (record) e.setResult(record)
})
```

### National payment QR — plugin `railbase-qr-payments`

Composite types для country-specific payment QR standards:

```go
import "github.com/railbase/railbase-qr-payments/payments"

// Россия — СБП (Система Быстрых Платежей)
schema.Field("sbp_qr", payments.SBP().
    MerchantID().                                           // required field в same collection
    Amount("amount", "currency"))

// EU — EPC QR Code (SEPA)
schema.Field("epc_qr", payments.EPC().
    BankAccount("iban", "bic").
    Beneficiary("company_name").
    Amount("amount").
    Reference("invoice_number"))

// Brasil — PIX
schema.Field("pix_qr", payments.PIX().
    Key("pix_key").                                         // PIX key (CPF/CNPJ/email/phone/random)
    Amount("amount").
    MerchantName("name"))
```

Plugin generates валидную encoded строку соответствующую national standard, с правильными checksums и formatting.

**Use cases**: payment-ready invoice PDFs, receipt printing, POS systems.

---

### Color, Rating, Priority, Coordinates, Barcode, Cron, MoneyRange — кратко

| Field | Wire | Use case |
|---|---|---|
| **color** | `"#FF5733"` или `{r,g,b}` | UI theming, label colors, calendar event colors |
| **rating** | int 1-N | reviews, satisfaction surveys, performance ratings |
| **priority** | enum (low/medium/high/critical) с metadata | tickets, tasks, alerts |
| **coordinates** | `{lat, lng}` | store locations, asset GPS, simple maps |
| **barcode** | string + format (UPC/EAN13/ISBN/GTIN) с checksum | inventory items, products, books |
| **cron** | cron expression string | schedule fields в коллекциях (когда users define schedules) |
| **money_range** | `{min: currency, max: currency}` | salary bands, price ranges, budgets |

---

### Specialized — plugins

Эти типы доменно-специфичны и shipped через plugins:

| Field | Plugin | Notes |
|---|---|---|
| `gl_account` | `railbase-accounting` | Chart of accounts, GAAP/IFRS validation |
| `cost_center` | `railbase-accounting` | Cost allocation hierarchy |
| `fiscal_period` | `railbase-accounting` | FY-aware periods (Q1 FY2026), close states |
| `signature` | `railbase-esign` | Digital signatures via DocuSign/HelloSign |
| `vin` | `railbase-vehicle` | 17-char Vehicle Identification Number с checksum |
| `license_plate` | `railbase-vehicle` | Per-country format |
| `passport` | `railbase-id-validators` | Passport number per-country |
| `national_id` | `railbase-id-validators` | National ID per-country |
| `public_key` | `railbase-security` | SSH/PGP keys с fingerprint |
| `pin_code` | `railbase-security` | Short numeric с masking |

### View collections

PB view collections (read-only collections from SQL views). Railbase их поддерживает:

```go
var ActiveUsersView = schema.ViewCollection("active_users").
    Query(`SELECT id, email, last_seen FROM users WHERE last_seen > now() - interval '30 days'`).
    Field("id", schema.Text()).
    Field("email", schema.Email()).
    Field("last_seen", schema.Date())

// Materialized views — для expensive aggregations
var DailyRevenueView = schema.MaterializedView("daily_revenue").
    Query(`SELECT date_trunc('day', created) AS day, currency, SUM(amount) AS total FROM payments GROUP BY 1, 2`).
    RefreshPolicy(schema.RefreshConcurrently).
    RefreshSchedule("@hourly")
```

Read-only; не имеет CRUD endpoints, только GET. Realtime subscribe работает через триггеры на underlying tables → `NOTIFY railbase_view_change` → broker dispatches.

### Multiple auth collections

См. [04-identity.md](04-identity.md#multiple-auth-collections).

---

## Migrations

### Auto-discover (порт rail's [migrate.ts](src/api/db/migrate.ts))

Файлы в `migrations/` именуются `NNN_<slug>.up.sql` / `NNN_<slug>.down.sql`.

```
migrations/
  0001_init.up.sql
  0001_init.down.sql
  0042_add_fts.up.sql
  0042_add_fts.down.sql
```

Один SQL target (Postgres) → один файл per migration. Никакой dialect-routing logic.

### Migration history

Таблица `_migrations`:
```
NNN | filename | content_hash | applied_at | applied_by
```

При старте:
1. Discover файлов из `migrations/`
2. Compare с applied
3. Apply pending в order
4. **Content hash check** — если уже applied migration был edited после → block startup с warning, требует `--allow-drift` flag

### Auto-migrations diff (PB feature)

`railbase migrate diff` — сравнивает текущий Go DSL schema с applied schema, генерирует **новую migration файл** автоматически. Это аналог PB's automigration через UI.

Для PB-compat: `railbase migrate jsdiff` генерирует JS migrations файлы.

**Три-шаговый backfill для `NOT NULL ADD COLUMN`.** Простой
`ALTER TABLE … ADD COLUMN col TYPE NOT NULL` падает на первой
существующей строке. `migrate diff` теперь эмитирует:

```sql
-- step 1: nullable add (safe на любой таблице)
ALTER TABLE posts ADD COLUMN slug TEXT;
-- step 2: backfill (оператор подставляет реальное выражение)
UPDATE posts SET slug = /* TODO: backfill expression */ NULL WHERE slug IS NULL;
-- step 3: финальный constraint
ALTER TABLE posts ALTER COLUMN slug SET NOT NULL;
```

Исключения (single-line ADD сохраняется): `HasDefault`,
`Computed != ""`, `Date + AutoCreate`, `SequentialCode`, `Status`
с values, и nullable-поля. Логика — `needsBackfillSplit()` в
`internal/schema/gen/sql.go`.

**`Collection ↔ AuthCollection` toggle.** Toggle спека ловится диффом
и эмитирует ALTER для каждой auth-injected колонки
(email / password_hash / verified / token_key / last_login_at) — см.
выше в §Schema DSL. Без этого `migrate diff` молча отдавал «schema
unchanged» и операторы писали миграцию вручную.

### Up + down

PB только up. Railbase добавляет down. `railbase migrate down [--steps N]`.

### Schema-as-code drift detection

При старте: hash текущего Go DSL ≠ hash последней applied migration → warning banner в admin UI + console:
```
⚠ Schema drift detected:
  - Field added: posts.featured (bool)
  Run `railbase migrate diff` to generate migration.
```

### Migrations are transactional

Каждая миграция = одна tx. Migration crash → rollback всей миграции. Нет «частично применённых» миграций.

### Module migrations

Каждый core-модуль регистрирует свои system migrations через `Module.Migrations() []Migration`. Embedded в бинарник через `embed.FS`. Применяются перед user migrations.

---

## Transactions & data consistency

### `WithTx` контракт (порт rail's [tx.ts](src/api/tx.ts))

```go
err := db.WithTx(ctx, func(tx *db.Tx) error {
    if err := posts.Create(tx, ...); err != nil {
        return err   // rollback автоматический
    }
    return nil       // commit
})
```

**Правила**:

1. **Actor required** — без authenticated context нельзя начать tx (защита от случайных system-level mutations)
2. **No nesting** — вложенный `WithTx` возвращает текущую tx, не создаёт savepoint (savepoints — opt-in через `WithSavepoint`)
3. **Timeout default 30s** — настраиваемо; deadlock detection через context cancel
4. **Audit-after-commit** — audit-writes из tx-context queued; flush'атся после commit. Если tx rollback — audit-writes исчезают **из tx-pool**, но critical denies (RBAC failures) пишутся через **bare pool** до tx commit, чтобы они выживали rollback (правило rail's [logs.service.ts](src/modules/shared/logs/server/logs.service.ts))
5. **EventBus-publishes deferred** — `eventbus.Publish` внутри tx queued; published только после commit
6. **Hook execution ordering** — `BeforeCreate` hooks run **внутри tx**; `AfterCreate` hooks run **после commit**

### Distributed transactions

Не делаем. Cross-system consistency через saga pattern (`railbase-workflow` plugin v1.1+).

### Concurrency control

- **MVCC** — concurrent readers + writers без блокировок read paths
- **Default isolation**: `READ COMMITTED` (Postgres default)
- **Stricter via opt-in**: `WithIsolation(db.RepeatableRead)` или `WithIsolation(db.Serializable)`. SERIALIZABLE auto-retry на `40001` через `internal/db/retry/`
- **Advisory locks** для cross-tx coordination (sequential code generation, scheduler leader election): `pg_advisory_xact_lock(key)`
- **Optimistic concurrency**: записи имеют `version` поле (auto-incremented через trigger); update требует `WHERE version = $expected`; conflict → `ErrVersionConflict`
- **Pessimistic via `SELECT FOR UPDATE`** — opt-in для row locks

---

## Multi-tenancy: PostgreSQL Row-Level Security

Tenant isolation enforced **в БД через RLS policies**, не в application layer. Application layer всё равно фильтрует (defense in depth), но **ground truth** — БД-level policies, которые невозможно обойти случайно.

### Setup

`.Tenant()` modifier в schema DSL:

```go
schema.Collection("posts").
    Tenant().                          // adds tenant_id UUID NOT NULL + FK + RLS policies
    Field("title", schema.Text())
```

Migration generator emits:

```sql
CREATE TABLE posts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    -- ...
);

ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
ALTER TABLE posts FORCE ROW LEVEL SECURITY;     -- даже table owner проходит через policies

CREATE POLICY posts_tenant_isolation ON posts
    USING (tenant_id = current_setting('railbase.tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('railbase.tenant', true)::uuid);

CREATE INDEX posts_tenant_id_idx ON posts (tenant_id);
```

### Per-request context propagation

Каждый authenticated request acquires connection из pool, middleware устанавливает session variables:

```go
// internal/server/middleware/tenant.go
func TenantContext(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()
        actor := identity.FromContext(ctx)
        tenant := tenant.FromContext(ctx)

        conn := pool.Acquire(ctx)
        defer conn.Release()

        // Set session vars (tx-local; reset on commit/rollback или connection release)
        conn.Exec(ctx, `SELECT set_config('railbase.tenant', $1, true)`, tenant.ID)
        conn.Exec(ctx, `SELECT set_config('railbase.user',   $1, true)`, actor.ID)
        conn.Exec(ctx, `SELECT set_config('railbase.role',   $1, true)`, actor.Role)

        next.ServeHTTP(w, r.WithContext(db.ContextWithConn(ctx, conn)))
    })
}
```

`true` третьим аргументом `set_config` = `is_local` (tx-scoped, не session-scoped). Когда connection возвращается в pool после `RELEASE`, settings очищаются автоматически.

### Bypass для admin / system operations

Site admin или migration runner запускается с другой role:

```sql
SET LOCAL railbase.role = 'app_admin';

-- Policy позволяет bypass для этой роли:
CREATE POLICY posts_admin_bypass ON posts
    FOR ALL TO PUBLIC
    USING (current_setting('railbase.role', true) = 'app_admin')
    WITH CHECK (current_setting('railbase.role', true) = 'app_admin');
```

Каждое использование admin bypass пишется в audit с явной отметкой `bypassed_rls = true`.

### Cross-tenant queries (admin tooling)

```go
db.WithSiteScope(ctx, func(ctx) error {
    // Connection acquired с railbase.role = 'app_admin'
    // Audit row автоматически
    return adminTask(ctx)
})
```

### Schema-per-tenant escape hatch

Для compliance кейсов с физической изоляцией:

```go
schema.Collection("medical_records").
    Tenant(schema.SchemaPerTenant)
```

Каждый tenant получает свою Postgres schema (`tenant_<id>.medical_records`). Query routing — через `SET search_path` per-request. Migrations применяются `FOR EACH tenant`. Trade-off: операционная сложность, но полный isolation.

### Defense in depth

Application layer **дублирует** проверку (RLS — last line, не only line):

1. Filter expression auto-injects `tenant_id = @tenant` (в native mode)
2. CRUD endpoints проверяют `record.tenant_id == ctx.tenant_id` after read (sanity check)
3. RBAC rules (`@request.tenant.id == record.tenant_id`) — third layer

RLS гарантирует, что даже при bug в (1) и (2) cross-tenant data leak невозможен.

---

## Filter expression language

PB has filter syntax (`status = 'published' && author = @request.auth.id`). Railbase supports two modes.

### Strict mode (PB-compat)

Полный PB filter syntax с известными quirks:

- Operators: `=`, `!=`, `>`, `<`, `>=`, `<=`, `~` (LIKE), `!~`
- Logic: `&&`, `||`, `()`
- Magic vars: `@request.auth.id`, `@request.auth.collectionName`, `@request.body`, `@collection.{name}.{field}`
- String literals: `'...'`
- Functions: `@now`, `@yesterday`, `@todayStart`, etc.

### Native mode

Same syntax + extensions:

- `@me` — shorthand для `@request.auth.id`
- `@tenant` — current tenant (multi-tenant only)
- Better operators: `IN (...)`, `BETWEEN`, `IS NULL`
- Typed comparison (no implicit string→number coercion)
- Compile-time validation в Go DSL: `rbac.Filter("status='published' && author=@me")` валидирует поля при build time

### AST-based parser

`internal/filter/` — парсер строит AST; никогда не concat'ает в SQL strings. Безопасность first-class.

- Поля проверяются по schema registry; неизвестные поля → 400
- Magic vars резолвятся **до** SQL генерации
- Параметризация через `$1`/`$2` placeholders (Postgres native)
- Field-level resolvers для relation expansion с RBAC checks (порт PB's `core/record_field_resolver.go`)

### Где используется

- REST list filter: `?filter=...`
- Subscribe filter (native mode)
- Hooks: `$app.dao().findRecordsByFilter("posts", "status='published'", ...)`
- API rules в schema DSL: `.ListRule("@request.auth.id != ''")`
- Saved filters в admin UI

---

## Pagination

Два mode (PB-compat + native):

### Offset (PB-compat)

```
GET /api/collections/posts/records?page=2&perPage=20
→ { items: [...], page: 2, perPage: 20, totalItems: 1234, totalPages: 62 }
```

### Cursor (native, рекомендованный для больших коллекций)

```
GET /v1/posts?limit=20&after=eyJpZCI6Ii4uLiJ9
→ { items: [...], next_cursor: "eyJpZCI6Ii4uLiJ9", has_more: true }
```

Cursor = base64(JSON({sort_field_value, id})). Stable across inserts/deletes.

---

## Soft delete / undo

PB не имеет первоклассно; админы постоянно «упс delete». Railbase делает opt-in feature.

```go
schema.Collection("posts").SoftDelete()  // adds deleted_at column
```

### Поведение

- Default queries auto-filter `WHERE deleted_at IS NULL`
- `WithDeleted()` modifier для admin views
- `OnlyDeleted()` для trash view
- DELETE через REST устанавливает `deleted_at = now()` вместо DROP
- 30-day grace period в admin UI с restore button (configurable)
- Hard purge через CLI или scheduled job после grace expiry
- Audit row на каждую operation

### REST endpoints (с soft delete enabled)

```
DELETE /api/collections/posts/records/{id}              # soft delete
POST   /api/collections/posts/records/{id}/restore      # undelete
DELETE /api/collections/posts/records/{id}?hard=true    # admin-only, audit обязательный
GET    /api/collections/posts/records?include=deleted   # include trash
GET    /api/collections/posts/trash                      # admin trash view
```

### Cascade behaviour

При soft-delete record:
- Relations с `.CascadeDelete()` → также soft-deleted (cascade)
- Relations с `.SetNullOnDelete()` → set null
- Relations без modifier → blocked если есть зависимости (FK constraint)

При restore:
- Cascade-deleted children восстанавливаются
- Если child был отдельно hard-deleted → не восстанавливается (warning)

### Per-tenant retention

С `railbase-orgs`: retention period configurable per subscription tier.

---

## Per-collection audit (v3.x)

PB пишет audit только на security-events. Railbase v3.x добавляет
**opt-in per-collection auto-audit** — каждый Create / Update / Delete
на коллекции с `.Audit()` automatically эмитит row в unified
timeline (см. `19-unified-audit.md`).

```go
schema.Collection("vendors").
    Audit().                         // ← opt-in CRUD auto-audit
    Field("name", schema.String().Required()).
    Field("status", schema.String())
```

### Поведение

REST CRUD handlers (`internal/api/rest/handlers.go`) после `tx.Commit`
вызывают `emitRecordAudit(r, d.audit, spec, verb, recordID, before, after)`
— см. `internal/api/rest/audit_record.go`. Эмит идёт через legacy
`audit.Writer` (с attached v3 Store), поэтому routing site/tenant
делается автоматически по `spec.Tenant`:

| spec.Tenant | Куда пишется | Chain |
|---|---|---|
| `true` | `_audit_log_tenant` (RLS scope) | per-tenant chain |
| `false` | `_audit_log_site` | global site chain |

Shape:

```
event:        "<collection>.created" | "<collection>.updated" | "<collection>.deleted"
entity_type:  "<collection>"
entity_id:    <record.id>
actor:        from ctx Principal (PrincipalFrom — admin / user / api_token / system)
before:       create=nil, update=nil (pre-image fetch — Phase 1.5), delete=nil
after:        create=row, update=post-image, delete=nil
outcome:      success (failures don't reach post-commit)
```

### Когда **не** включать

- **`sessions`, `_admin_sessions`, `_sessions`** — high-frequency churn, chain cost не оправдан.
- **Ephemeral lookup collections** — кешевые данные, дубль audit'а в общем потоке.
- **Soft-delete restore** — `restoreHandler` пишет свой явный audit; auto-CRUD на restore был бы дубль.

### Off by default

Без `.Audit()` REST handlers вообще не вызывают audit writer
(zero-cost short-circuit в `emitRecordAudit`). Включай явно для
business-critical коллекций: orders, vendors, contracts, settings.

### Auth + Tenant сочетания

| Combo | Что попадает в timeline |
|---|---|
| `.Audit()` | site events с `actor_type=admin` (если admin REST) |
| `.Audit().Tenant()` | tenant events с RLS, actor любого типа |
| `.Audit().SoftDelete()` | DELETE → `*.deleted` event с `actor_type=admin` или `user` |
| `.Audit().Auth()` | разрешено, но auth collections используют свои dedicated endpoints (`/auth-*`), которые пишут audit явно — `.Audit()` дублировал бы |

---

## Batch operations

PB feature `apis/batch.go`. Атомарный multi-record API.

### REST endpoint

```
POST /api/batch
[
  { "method": "POST", "url": "/api/collections/posts/records", "body": {...} },
  { "method": "PATCH", "url": "/api/collections/users/records/u1", "body": {...} },
  { "method": "DELETE", "url": "/api/collections/posts/records/p1" }
]
```

**Семантика**:

- Все операции в одной транзакции
- Если хоть одна fails → rollback всех
- Response: array of per-op results (или единая ошибка)
- RBAC: каждая операция проходит свой rule check
- Audit: каждая операция пишет свой row, плюс batch-row с aggregate
- Hooks: `BeforeCreate/Update/Delete` запускаются для каждой операции; `AfterCreate/...` — только после commit batch
- Realtime events: батчатся, публикуются после commit

### Native API extension

Native mode добавляет typed batch:
```ts
await rb.batch([
  rb.collections.posts.create({...}),
  rb.collections.users.update("u1", {...}),
])
```

SDK автоматически шлёт через `/v1/batch`.

### Limits

- Max 100 operations per batch (configurable)
- Total payload size limit (default 10 MB)
- Batch timeout (default 60s)

### Bulk endpoint per-collection (alternative)

Convenience endpoint для bulk operations на одной коллекции:

```
POST /api/collections/posts/records/bulk
{
  "operations": [
    { "op": "create", "data": {...} },
    { "op": "update", "id": "...", "data": {...} },
    { "op": "delete", "id": "..." }
  ]
}
```

**Семантика**:
- Atomic: всё в одной tx (default) или per-op (с `?atomic=false`)
- Per-op RBAC check
- Per-op error → rollback всех (atomic mode)
- Partial success в non-atomic mode → 207 Multi-Status response с per-op statuses
- Limit 1000 ops per request (configurable)
- JS hooks: per-op events (`onRecordBeforeCreate` для каждого create), не aggregated bulk-event

### Когда какой использовать

- `/api/batch` — multi-collection в одной tx (transfer money: deduct from A, credit to B)
- `/api/collections/{name}/records/bulk` — single-collection bulk (CSV import, batch update status)
