# 02 — Architecture: interfaces, modules, packages, dependencies

## Public interfaces — что видит пользователь

Railbase предоставляет **семь точек входа**, каждая со своим контрактом и аудиторией:

### 1. Go API — `pkg/railbase/`

Public Go-пакет для пользователей, которые встраивают Railbase в свой Go-код или пишут collections в Go.

```go
import "github.com/railbase/railbase/pkg/railbase"
import "github.com/railbase/railbase/pkg/railbase/schema"
import "github.com/railbase/railbase/pkg/railbase/rbac"
import "github.com/railbase/railbase/pkg/railbase/hooks"
import "github.com/railbase/railbase/pkg/railbase/documents"
```

**Контракт**: semver-stable от v1. Breaking changes только в major.

### 2. JS Hooks API — `pb_hooks/*.js`

Surface для бизнес-логики, vibe-coding, AI-генерации. Goja runtime.

**Контракт**: PB-compatible имена (`onRecordCreate`, `routerAdd`, `cronAdd`); native-mode добавляет typed helpers через `.d.ts`.

См. [06-hooks.md](06-hooks.md).

### 3. REST API — `/api/*`

Wire-протокол для клиентов (browser, mobile, server-to-server).

**Контракт**: два mode (`strict` PB-shape `/api/collections/...` vs `native` `/v1/...`). OpenAPI spec эмитится через CLI.

### 4. Realtime API — WebSocket / SSE

Подписки на изменения коллекций.

**Контракт**: PB SDK работает в strict; native — typed envelope с discriminated unions.

См. [05-realtime.md](05-realtime.md).

### 5. Admin UI — `/_/`

Embedded React SPA для админки. Не внешний контракт — UI может меняться.

См. [12-admin-ui.md](12-admin-ui.md).

### 6. CLI — `railbase ...`

Lifecycle commands. **Контракт**: stable от v1.

См. [13-cli.md](13-cli.md).

### 7. MCP Server (plugin v1.2+)

`railbase-mcp` plugin: Model Context Protocol server для LLM-агентов. Schema introspection, safe data mutations, hook editing. Делает Railbase first-class цитизеном AI-era.

---

## Module architecture — как соприкасаются модули

### Иерархия зависимостей (строгая, enforced на уровне Go-imports)

```
[ Layer 0 — primitives ]
    config, errors, slog/logger, ulid, clock, security, store, routine
        ↑
[ Layer 1 — infrastructure ]
    db (pgx/v5 pool, retry, RLS context, LISTEN/NOTIFY)
    storage (FS/S3)
    eventbus (internal — channels + opt-in NOTIFY backend)
    audit (writer)
    notify (file watcher)
        ↑
[ Layer 2 — domain primitives ]
    schema (registry, DSL, field-resolvers)
    migrate (auto-discover + dialect)
    identity (users, system admins, service accounts — без RBAC деталей)
    rbac (site + tenant scope)
    tenant (context, compile-time enforcement)
    settings (runtime-mutable)
        ↑
[ Layer 3 — capabilities ]
    auth (session/password/totp/webauthn/oidc/oauth/otp/mfa/apitokens/devices/origins/impersonate/recordtokens)
    realtime (broker iface, ws/sse, resume)
    jobs (queue, cron)
    hooks (goja runtime, sandbox, bridges, watcher)
    files (file fields, thumbnails)
    documents (logical repository)
    mailer (smtp/ses, templates)
    export (xlsx, pdf, templates)
    filter (expression parser, AST)
    logs (как records)
        ↑
[ Layer 4 — surface ]
    api/rest, api/pbcompat
    server (chi assembly, middleware: logging/cors/ratelimit/bodylimit/gzip/auth/audit)
    admin (embedded UI)
    plugin (host)
        ↑
[ Layer 5 — entrypoint ]
    cmd/railbase
```

**Правила импортов** (enforced через `go-arch-lint` или вручную в CI):

- Слой N может импортировать только слои < N
- Никаких циклов (Go enforce'ит компилятором, но cross-module зависимости проверяем линтером)
- Plugins импортируют **только** `pkg/railbase/` (public API), никогда `internal/`

### Inter-module communication — три механизма

#### 1. Прямые вызовы через интерфейсы

Для синхронных операций между core-модулями. Каждый core-модуль экспонирует Go-интерфейс, конкретные имплементации связываются в `cmd/railbase/main.go` через DI (вручную, без фреймворка).

```go
// internal/auth/api.go
type Service interface {
    Signin(ctx, email, password) (*Session, error)
    Signup(ctx, input) (*User, error)
    // ...
}

// в main.go
authSvc := auth.New(db, mailer, audit, eventbus, ...)
```

#### 2. Internal event bus (`internal/eventbus`)

Для асинхронных fan-out. Это **не** realtime broker (тот публичный, для клиентов); это **внутренний** pub/sub для cross-module reactions.

```go
eventbus.Publish(ctx, RecordCreated{Collection: "posts", ID: "..."})
eventbus.Subscribe[RecordCreated](handler)
```

**Использование внутри core**:
- `audit-writer` подписан на: `record.created`, `record.updated`, `record.deleted`, `auth.signin`, `auth.signout`, `rbac.deny`, ...
- `realtime-broker` подписан на: `record.created/updated/deleted` → форвардит на WebSocket/SSE подписчиков
- `hooks-dispatcher` подписан на: `record.*` → запускает соответствующие JS hooks
- `jobs-scheduler` подписан на: `schema.changed` → пересинхронизирует cron jobs
- `documents` подписан на: `record.deleted` → optional cascade-archive

**Backends**:
- **In-process** (default) — channels + goroutine pool
- **NATS** (с `railbase-cluster` plugin) — events форвардятся в NATS subject; cross-instance subscribers получают

**Деливери семантика**:
- At-most-once in-process, default
- At-least-once с NATS в cluster-mode
- Подписчик может opt-in на persistent (jobs queue) для важных событий

**Important rules**:
- Internal eventbus НЕ используется для cross-tenant data flow — каждое event carries `tenant_id`, и подписчик решает фильтрацию
- EventBus-publishes deferred: `eventbus.Publish` внутри transaction queued; published только после commit. Иначе подписчики реагируют на uncommitted state

#### 3. Plugin RPC

gRPC subprocess protocol для plugins. Изоляция: plugin крашится → core продолжает работать.

См. [15-plugins.md](15-plugins.md#plugin-rpc).

### Module boundaries

Каждый core-модуль:

- Экспонирует один публичный интерфейс через `internal/<module>/api.go`
- Скрывает реализацию в подпакетах
- Регистрирует свои HTTP routes через `Module.Mount(router)` (если применимо)
- Регистрирует свои миграции через `Module.Migrations() []Migration`
- Декларирует subscriptions на eventbus в `Module.OnStart(bus)`

Это даёт **зеркальную структуру для plugins** — plugin реализует тот же `Module` интерфейс через RPC stub.

### Module lifecycle

Каждый модуль проходит через стандартные lifecycle phases:

```go
type Module interface {
    Name() string
    Migrations() []Migration              // Layer 1+2 modules
    Mount(r chi.Router)                   // Layer 4 modules
    OnStart(ctx, bus *eventbus.Bus)       // подписка на events, запуск worker'ов
    OnStop(ctx) error                     // graceful drain
    Health(ctx) HealthStatus               // for /readyz
}
```

В `main.go`:
1. Создаются все модули в порядке Layer 0 → 5
2. Применяются миграции (атомарно по модулю)
3. `Mount(router)` для всех Layer 4 модулей
4. `OnStart(bus)` для всех (порядок важен — некоторые подписываются на events других)
5. HTTP server start
6. На SIGINT → reverse order `OnStop`

---

## Структура пакетов

```
cmd/railbase/main.go

internal/
  # Layer 0 — primitives
  config/  errors/  logger/  ulid/  clock/  security/  store/  routine/  inflector/

  # Layer 1 — infrastructure
  db/                                 # один драйвер: jackc/pgx/v5
    pool/                             # pgxpool wrapper
    rls/                              # SET LOCAL railbase.tenant per-tx; helpers для policy management
    listen/                           # LISTEN/NOTIFY consumer для realtime/eventbus
    retry/                            # auto-retry on serialization_failure / deadlock
    embedded/                         # opt-in fergusstrange/embedded-postgres для dev
  storage/  fs/  s3/
  eventbus/                           # in-process channels; LISTEN/NOTIFY backend для cross-process в single-instance; NATS — для cluster
  audit/  writer/  sealer/
  notify/                             # fsnotify watcher (для pb_hooks/ hot reload)

  # Layer 2 — domain primitives
  schema/  builder/  registry/  gen/  resolver/
  migrate/  runner/  history/
  identity/  users/  admins/  serviceaccounts/
  rbac/  actionkeys/  scope/          # site + tenant scope в core
  tenant/  context/                   # tenant_id propagation; enforcement реально живёт как RLS policies на стороне БД
  settings/                           # runtime-mutable settings
  filter/                             # AST-based filter expression parser → SQL для Postgres
  logs/                               # logs-as-records

  # Layer 3 — capabilities
  auth/
    session/  password/  totp/  webauthn/
    oidc/  oauth/                     # 35+ providers
    otp/                              # magic links, SMS OTP, email OTP
    mfa/                              # multi-factor challenges state machine
    apitokens/  recordtokens/         # отдельные signed tokens (file access, verify, reset)
    externalauth/                     # multiple providers per user
    devices/  origins/                # auth origins (new device detection)
    impersonate/  invites/  flows/
  realtime/  broker/  ws/  sse/  resume/
  jobs/  queue/  cron/
  hooks/  goja/  bridges/  watcher/  sandbox/
  files/  thumbs/  imaging/           # file fields + thumbnails
  documents/                          # logical documents repository
    versions/  storage/  quota/
    extract/  retention/  legal/
  mailer/  smtp/  ses/  templates/
  export/  xlsx/  pdf/  templates/

  # Layer 4 — surface
  api/
    rest/
    pbcompat/
  server/
    middleware/
  admin/                              # go:embed admin SPA
  plugin/                             # plugin host

pkg/railbase/                         # public API для пользователей
  schema/  rbac/  hooks/  errors/  documents/  mailer/  realtime/  inflector/  types/

plugins/                              # отдельные бинарники
  railbase-cluster/                   # NATS distributed realtime
  railbase-orgs/                      # organizations entity (без per-tenant RBAC — теперь в core)
  railbase-billing/                   # Stripe / Paddle / LemonSqueezy
  railbase-authority/                 # approval engine
  railbase-saml/                      # SAML SP
  railbase-scim/                      # SCIM 2.0
  railbase-workflow/                  # saga
  railbase-push/                      # FCM/APNs
  railbase-pdf-html/                  # chromedp HTML→PDF (heavy)
  railbase-doc-ocr/                   # Tesseract sidecar
  railbase-doc-office/                # LibreOffice sidecar
  railbase-pdf-preview/               # poppler/pdftoppm
  railbase-esign/                     # DocuSign/HelloSign
  railbase-docx/                      # DOCX export
  railbase-mcp/                       # Model Context Protocol для LLM
  railbase-wasm/                      # WASM hook runtime (v2)
  railbase-postmark/  railbase-sendgrid/  railbase-mailgun/
  railbase-ghupdate/                  # auto-update via GitHub releases
  railbase-geo/                       # PostGIS / geo extension
  railbase-sql-playground/            # raw SQL playground (admin only, opt-in)
```

---

## Зависимости (фиксированный список core)

| Слой | Библиотека | Обоснование |
|---|---|---|
| Routing | `go-chi/chi/v5` | Stdlib-совместимая, без custom Context |
| Postgres driver | `jackc/pgx/v5` | De-facto стандарт; native protocol; `pgxpool` |
| Postgres-native types | `jackc/pgx/v5/pgtype` | Native LTREE / NUMERIC / INTERVAL / DATERANGE / arrays |
| Embedded Postgres (dev) | `fergusstrange/embedded-postgres` | Скачивает PG binary, запускает subprocess; для `--embed-postgres` flag |
| SQL → Go | `sqlc` (codegen, postgresql engine) | Compile-time type-safety, один target |
| WebSocket | `coder/websocket` | Современный API, маленький |
| JS hooks | `dop251/goja` | PB-compat, без CGo |
| File watch | `fsnotify/fsnotify` | Hot-reload |
| OIDC / OAuth | `coreos/go-oidc/v3` + `golang.org/x/oauth2` | Стандарт |
| WebAuthn | `go-webauthn/webauthn` | Без CGo |
| 2FA | `pquerna/otp` | TOTP + recovery codes |
| Argon2 | `golang.org/x/crypto/argon2` | Argon2id |
| Cron | `robfig/cron/v3` | Стандартный парсер |
| Telemetry | `go.opentelemetry.io/otel` (OTLP/HTTP) | Без gRPC-веса |
| CLI | `spf13/cobra` | Стандарт |
| S3 | `minio-go/v7` | Лёгкий, S3-совместимый |
| XLSX | `xuri/excelize/v2` | Pure Go, активно поддерживается |
| PDF (native) | `signintech/gopdf` | Pure Go, легкий |
| Markdown | `gomarkdown/markdown` | Stdlib-style API |
| Image processing | `disintegration/imaging` | Pure Go |
| HTML sanitization | `microcosm-cc/bluemonday` | XSS protection для richtext |
| SMTP | `net/smtp` stdlib + `mhale/smtpd` (testing) | Минимальные deps |
| Filter parser | custom (AST-based) → Postgres SQL | Контроль над security и magic vars |
| Phone (tel field) | `nyaruka/phonenumbers` | Pure Go libphonenumber port |
| Decimal (Go-side) | `shopspring/decimal` | Wire format / SDK-side. На стороне БД — native `NUMERIC` |
| ISO 4217 catalog | embedded | Currency codes + per-currency precision |
| ISO 3166-1 / 639-1 | embedded | Country / language catalogs |
| IANA TZ database | embedded | Timezone catalog (`time/tzdata` package) |
| BCP 47 / RFC 5646 | embedded | Locale catalog |
| IBAN validation | custom + embedded country format catalog | Mod-97 checksum |
| Tax ID validators | custom + embedded per-country catalog | RU/US/EU/UK/CA/AU/CN/IN/BR + ~20 more |
| Unit catalog (Quantity) | embedded + extensible | Mass/length/volume/area/time/pieces/energy |
| Person name (CLDR) | embedded | Culturally-aware formatting |
| Tree patterns | native Postgres `LTREE` (materialized path), recursive CTE (adjacency), explicit columns (nested set), closure tables (closure / DAG) | All 5 patterns — native PG features |
| Vector search | native `pgvector` extension | First-class, не plugin |
| Fuzzy search | native `pg_trgm` extension | Optional, через extension toggle |
| Geo (lite) | native `POINT` type / opt-in PostGIS | Coordinates field |
| QR code rendering | `skip2/go-qrcode` (pure Go) + `yeqown/go-qrcode` (logo overlay) | PNG / SVG / PDF output |
| Plugin RPC | TBD: `hashicorp/go-plugin` vs custom gRPC vs WASI | См. [15-plugins.md](15-plugins.md) |
| Routine helpers | custom `internal/routine/` | Panic-safe goroutine wrappers |
| Inflector | custom `internal/inflector/` | Pluralize, snake_case, и т.д. |
| In-memory store | custom `internal/store/` | Generic cache |
| Security helpers | custom `internal/security/` | Hash, encrypt, randomString, JWT |

**Целевой размер core-бинарника**: ~25 MB stripped + UPX (с document generation, без embedded-postgres). С `--embed-postgres` flag — ещё ~50 MB на скачиваемый PG binary (один раз, лежит в `~/.railbase/pg/`).

### Требуемые Postgres extensions

Включаются автоматически при первом запуске, через `CREATE EXTENSION IF NOT EXISTS`:

| Extension | Назначение | Required / Optional |
|---|---|---|
| `pgcrypto` | gen_random_uuid(), digest() для audit hash chain | Required |
| `ltree` | TreePath field, materialized path queries | Required |
| `pg_trgm` | Fuzzy filter (`field ~* 'foo'`), partial-match indexes | Optional, auto-enabled при `.FTS()` или fuzzy filter |
| `btree_gist` | EXCLUDE constraints для DateRange overlap | Optional, auto-enabled при `EXCLUDE` constraint в DSL |
| `pgvector` | Vector field | Optional, auto-enabled при `schema.Vector(N)` |
| `postgis` | Full geo через `railbase-geo` plugin | Plugin-only |

При отсутствии прав на `CREATE EXTENSION` (managed PG, hardened envs) — startup error с инструкцией для DBA.

---

## SQL strategy: Postgres-native, без abstraction tax

Railbase **не** пытается быть DB-agnostic. Один target — PostgreSQL 14+ — позволяет использовать native features без compromises:

| Feature | Реализация |
|---|---|
| FTS | `tsvector` + GIN index, generated columns |
| JSON | `JSONB` с GIN, path queries (`@>`, `?`, `#>`) |
| Money | `NUMERIC(20, N)` |
| Composite types | `Currency`, `Address`, `BankAccount` — native composite types или JSONB (выбор по полю; см. [03-data-layer.md](03-data-layer.md)) |
| Ranges | `DATERANGE`, `TSTZRANGE`, `INT4RANGE` + GiST index + `&&` overlap operator + `EXCLUDE` constraints |
| Intervals | `INTERVAL` для Duration; date arithmetic в SQL |
| Hierarchies | `LTREE` (materialized path) с GiST + `<@` ancestor / `~` lquery; recursive CTE для adjacency |
| Tree path | `LTREE` first-class, не TEXT с LIKE |
| Arrays | `TEXT[]` для Tags вместо JSONB |
| Tenant enforcement | **Row-Level Security policies** — `CREATE POLICY ... USING (tenant_id = current_setting('railbase.tenant')::uuid)`. Application-layer enforcement дублируется (defense in depth) |
| Realtime fan-out | `LISTEN/NOTIFY` для single-instance; NATS plugin — для multi-instance cluster |
| Job queue | `SELECT ... FOR UPDATE SKIP LOCKED` |
| Vector search | `pgvector` first-class |
| Fuzzy search | `pg_trgm` |
| Generated columns | `GENERATED ALWAYS AS (...) STORED` для computed/derived fields |
| Concurrency | MVCC; configurable isolation (`READ COMMITTED` default, `SERIALIZABLE` через `WithIsolation`) |
| Read replicas | Postgres logical replication / streaming replication (managed PG providers handle это) |

**Один SQL file per migration** (`migrations/0042_*.sql`); один sqlc target; один driver (`pgx/v5`).

См. [03-data-layer.md](03-data-layer.md) для деталей по каждому field type.

---

## PB-compat через modes

```
RAILBASE_PBCOMPAT=strict   # PB-shape (URL, auth, filters, realtime)
RAILBASE_PBCOMPAT=native   # Railbase-shape
RAILBASE_PBCOMPAT=both     # обе схемы (default v1; deprecated в v2)
```

Documented divergences между native и strict — для тех, кто мигрирует и хочет потом native.

---

## API versioning & evolution

### Wire API versioning

- Strict mode: PB API "frozen" — никогда не меняем shape, только добавляем
- Native mode: версионируется через URL prefix (`/v1/...`, `/v2/...`)
- Breaking changes: deprecation cycle минимум 2 minor versions; deprecated routes возвращают warning header `Deprecation: true; sunset=...`

### Schema migrations при upgrade Railbase

- System migrations (для `_*` таблиц) — embedded в бинарник; авто-применяются на старте, atomic per migration
- User migrations — owned пользователем, auto-discovered из `migrations/` или генерятся из schema DSL
- Если upgrade Railbase требует migration на user-data (редко) — bin предупреждает: «run `railbase migrate user-upgrade` после backup»

### Go API versioning

- `pkg/railbase/` — semver, breaking changes только в major
- `internal/` — не contract, может меняться

---

## Distribution

- `goreleaser` matrix: `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}`
- `pgx/v5` — pure Go, no CGo — keeps cross-compile тривиальным
- Embedded admin SPA target ~3 MB gzipped; goja ~2 MB; total binary target **~25 MB** stripped + `upx --lzma`. Embedded-postgres binary (опциональный) — ещё ~50 MB, скачивается на первом `--embed-postgres` запуске
- Pre-built binaries via GitHub Releases
- Docker images (Alpine + scratch flavors)
- Homebrew formula для `brew install railbase`
- Auto-update через `railbase-ghupdate` plugin

См. [16-roadmap.md](16-roadmap.md) для phasing.
