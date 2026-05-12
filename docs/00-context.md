# 00 — Context, позиционирование, принципы, решения

## Зачем Railbase

PocketBase задал жанр: через 5 минут есть auth + БД + realtime + files + admin UI. Lightning-fast старт. Но архитектурные болезни не позволяют использовать его для serious продуктов:

- **Single-writer SQLite ceiling** — деградация на write-heavy нагрузках; от этой болезни Railbase отказывается **на уровне фундамента**: Postgres-only с дня 1
- **Нет horizontal scaling** — realtime in-memory, single-node
- **User-centric, не organization-centric** — invite/RBAC/multi-tenant болят
- **Слабая RBAC/IAM** — rules превращаются в spaghetti, нет SSO/SCIM
- **Нет audit/compliance** — критично для enterprise
- **Migration cliff** — рано или поздно требуется Postgres + queue + observability; Railbase убирает migration cliff, потому что Postgres — стартовая точка, не конечная
- **Schema-as-JSON в admin UI** — слабая type-safety для клиента
- **Pre-1.0 mindset** — backward compat не гарантируется
- **Bus factor = 1** — single maintainer

**Trade-off, который мы осознанно принимаем**: Railbase **не** «zero-deps single binary» как PocketBase. Railbase = «single binary + Postgres». Для dev-experience: `railbase serve --embed-postgres` запускает встроенный Postgres binary (через `fergusstrange/embedded-postgres`), сохраняя 5-минутный init без `docker run`. В production пользователь приносит свой managed Postgres.

## Позиционирование

**Production-safe vibe backend** — закрывает разрыв «AI помог сделать backend за вечер, а через месяц всё разваливается»

**LLM-native runtime** — schema/hooks/SDK structured так, чтобы агенты могли генерировать, читать, изменять контракты безопасно

**Universal backend для любых проектов** — MVP, internal tool, mobile backend, AI app, side project, B2B SaaS, ERP. Не enterprise-заточка.

## Принципы

1. **PocketBase-class DX как baseline** — `./railbase serve`, открыл UI, создал коллекцию, получил REST + realtime + типизированный SDK
2. **Любая фича сложнее CRUD должна быть opt-in** — core минималистичен
3. **Postgres как фундамент, не как escape hatch** — все advanced features (RLS, ranges, intervals, LTREE, composite types, `LISTEN/NOTIFY`, `SKIP LOCKED`, `pgvector`) используются first-class. Single↔cluster переключение через env, без replatform
4. **Type-safety первым классом, end-to-end** — schema-as-Go-code → миграции → sqlc типы → TS SDK с zod
5. **PB-compat через modes** (`strict`/`native`) — мост для миграции, не цемент
6. **Rail как референс паттернов**, не как продукт — берём `tx.ts`, `migrate.ts`, audit-через-bare-pool — не берём ERP-домены

## Целевая аудитория

| Аудитория | Что получает |
|---|---|
| Solo dev / indie SaaS | Lightning-fast старт, type-safe SDK, realtime, admin UI |
| AI tooling / vibe coders | LLM-friendly schema, MCP server, safe mutation layer |
| Internal tools / admin | RBAC, audit, multi-user, document management |
| Mobile backends | Native SDK (Swift/Kotlin/Dart), push, file uploads |
| B2B SaaS | Multi-tenant, organizations, billing, SSO (через plugins) |
| Self-hosted | Single binary deploy, no infra, optional cluster mode |

## Цели по продукту

- **5-minute start**: `railbase init demo && railbase serve --embed-postgres` → работающий backend с admin UI и встроенным Postgres
- **PB drop-in compat**: existing PB JS SDK работает в strict mode без изменений (с поправкой на то, что storage backend под капотом — Postgres, не SQLite; URL/wire format идентичны)
- **Migration path**: `railbase import schema --from-pb <url>` за минуту (схема портируется; данные — через CSV/JSON dump)
- **No replatform**: managed Postgres (RDS/Neon/Supabase/Cloud SQL) → on-prem Postgres через env var; single → cluster через env var
- **AI-native**: `railbase generate schema-json` для агентов; MCP server plugin

## Решения, утверждённые с пользователем

| Развилка | Выбор |
|---|---|
| Стек | Go (single static binary, cross-compile) |
| Schema | Fluent builder в Go-коде + auto-gen TypeScript SDK |
| Hooks | Embedded JavaScript через `goja` |
| БД | **PostgreSQL 14+ only** (через `jackc/pgx/v5`). SQLite не поддерживается ни как default, ни как production option |
| Dev mode | Optional embedded Postgres через `fergusstrange/embedded-postgres` (downloads binary в `~/.railbase/pg/` на первом запуске) |
| PB совместимость | Modes `strict`/`native`/`both` (URL/wire format; не storage layer) |
| Auth playbook | Rail's auth — бенчмарк (sessions, devices, 2FA, invite lifecycle, impersonation) |
| Multi-tenancy | Opt-in per-collection через `.Tenant()` в DSL |
| RBAC | Site + tenant scope в core (см. [04-identity.md](04-identity.md)) |
| Document management | First-class в core (см. [07-files-documents.md](07-files-documents.md)) |
| Document generation | XLSX + native PDF в core; HTML→PDF в plugin (см. [08-generation.md](08-generation.md)) |
| Permission engine (approvals) | Plugin `railbase-authority` (см. [15-plugins.md](15-plugins.md)) |
| Billing | Plugin `railbase-billing` Stripe primary |
| Push notifications | Plugin `railbase-push` (v1.2+) |

## Стек (полная картина)

См. [02-architecture.md](02-architecture.md#зависимости-фиксированный-список-core).

## Что Railbase НЕ делает

### Core philosophy

- Не пытается быть Firebase/Supabase replacement (другая ниша)
- Не пытается быть Retool/Metabase для admin (good enough scope)
- Не делает enterprise core (SAP) — слишком тяжело для single-binary

### Конкретные out-of-scope items

- **HTML→PDF в core** (Chrome 200 MB ломает single-binary contract; есть `railbase-pdf-html` plugin)
- **Schema editor с миграциями из admin UI** (конфликт с schema-as-code source-of-truth)
- **Crypto payments** (regulatory complexity; out of scope для general-purpose backend)
- **Video transcoding** (too heavy; через external service)
- **Video / image editing UI** (Lightroom/Photoshop territory)
- **SSR / static site generation** — Railbase не frontend framework; пользователи используют Next.js/Astro/SvelteKit поверх
- **Time-series данные высокой плотности** (>100M rows/day metrics) — рекомендуем external (TimescaleDB, ClickHouse, InfluxDB); Railbase может быть metadata layer перед ними
- **Real-time chat / messaging как домен** — primitives есть (realtime + collections), но full chat module (rooms, typing indicators, read receipts, threads) — plugin при необходимости (community)
- **Email open / click tracking pixel** в core (privacy concern; через provider integrations)
- **Newsletter / list management** (leave to пользователю или Mailchimp/etc)
- **Spam filtering / inbound mail** (out of scope)
- **AI training / fine-tuning** (Railbase exposes data через MCP plugin для AI tools; не сам ML platform)
- **Translation memory / sentence-level i18n workflows** (Lokalise/Crowdin/Phrase territory)

### Что в plugins (не в core)

См. [15-plugins.md](15-plugins.md). Major plugins: orgs, billing, authority, cluster, workflow, saml, scim, push, mcp, fx, doc-tools, esign, analytics, cms, compliance.

См. также [01-pb-audit.md](01-pb-audit.md) для детального coverage matrix vs PocketBase.
