# Railbase

Universal Go backend поверх PostgreSQL. PocketBase-class DX, production-grade фундамент.

> **Статус**: v0 released (2026-05-10); **v1 SHIP unblocked** (2026-05-12). Все критические-path gate'ы (§3.14 verification audit, `.goreleaser.yml` + 32 MB binary budget (поднят с 30 MB в v1.7.48 под 10-locale admin SPA), 6-target cross-compile, `make verify-release` one-shot pre-tag chain) ✅. Release tag — единственный оставшийся operator-шаг.
>
> **v1.x bonus-burndown в работе** (post-SHIP polish без блокировок). **v1.7.45 (2026-05-13) переместил Enterprise SSO в ядро** — SAML 2.0 SP (IdP/SP-initiated + ACS + SLO + signed AuthnRequests + group→role mapping) + LDAP (TLS/STARTTLS/LDAPS) + SCIM 2.0 (RFC 7643/7644 Users+Groups + filter parser + token mgmt) — это были v1.1+ плагины, теперь in-tree во всех 6 cross-compile binaries. **v1.7.46** закрыл admin UI gaps тремя параллельными агентами: System tables / Backend metrics dashboard / Color tokens ESLint enforcement + admin recovery flows + mailer template editor + sidebar shadcn alignment + HMR dev mode (`make dev`). **v1.7.47** закрыл три gap'а "Что НЕ покрыто": SCIM group→role reconciliation (`role_mapping.go` + 6 e2e) + SAML browser flow против live Keycloak (8 шагов) + OAuth/OIDC Keycloak (8 шагов). **v1.7.48–v1.7.49** закрыли два внешних embedder-feedback batch'а (shopper + blogger): `app.{Mailer,Stripe,Jobs,Realtime,Settings,Audit}()` accessors через `atomic.Pointer`, `migrate diff` detects `Collection ↔ AuthCollection` toggle, `.EntityDoc(...)` schema-declarative per-entity PDFs (invoice + line items), `pkg/railbase/{export,dltoken}` re-exports (writer + short-lived HMAC URLs, no more `internal/`-grovelling), ALTER NOT NULL three-step backfill, reserved-keyword rename suggestions, `RAILBASE_EMBED_PG_PORT` + sticky port persistence, `PublicProfile()` opt-in для AuthCollections, auth-response merges custom fields, optional URL/Email/Tel/Slug/Color CHECK accept `''`, currency/str template helpers, Stripe webhook-secret warning banner, `_stripe_products.external_id` unique index, scaffold ships `Makefile` с `-tags embed_pg` defaulted. **Admin SPA локализован в 10 языков** (1334 ключей через 4 параллельных агента + `scripts/i18n-translate.ts`). За предыдущие циклы (v1.7.30-44) закрыто ~30 sub-слайсов: `send_email_async` / `scheduled_backup` / `audit_seal` (Ed25519 chain) / `orphan_reaper` builtins; mailer + auth hooks dispatchers; **WebSocket transport**; **ICU plurals + `.Translatable()`** end-to-end; cache wiring slices 1-2 (4 production caches); RBAC bus-driven invalidation; anti-bot middleware; `MockHookRuntime` + `MockData` testing harness; foreign-DB safety gate; mandatory email on admin creation; UI kit (`/api/_ui/*` registry) + `railbase ui` CLI; react-hook-form across admin; default port `:8090` → `:8095`. **Honest v1 plan.md completion: ~99.9%** (~155 of ~155 line items + Enterprise SSO promoted INTO v1). Оставшийся остаток — explicit v1.1+ scope (S3/OTel/encryption/cluster) и blocked-by-design Documents track. См. [plan.md §3.15](plan.md) для полного inventory неимплементированного.
>
> **v2.0 направление (2026-05-16, design proposal)**: два новых core
> primitive'а — **Delegation of Authority** (DoA) и **Tasks** —
> зафиксированы в [docs/26-authority.md](docs/26-authority.md) и
> [docs/27-tasks.md](docs/27-tasks.md). DoA был запланирован как
> plugin (`railbase-authority`), но reclassified в core по
> результатам двух независимых embedder feedback'ов (blogger
> editorial approval + sentinel ERP financial JE materiality):
> bypass discipline и audit-chain integration невозможны из
> plugin'а. v2.0 — major bump (новый API surface, schema DSL,
> admin UI, SDK namespace); API-additive к v1.x. См. [plan.md §6](plan.md).
>
> См. [plan.md](plan.md) для статус-снапшота + [progress.md](progress.md) для per-milestone деталей. Краткий обзор v1: [docs/RELEASE_v1.md](docs/RELEASE_v1.md).

## Что это

Railbase — backend-runtime, который ставится одной командой, начинает работать за минуту и не упирается в архитектурный потолок при росте проекта.

**Стек**: один Go-бинарник + PostgreSQL 14+. Для dev-experience — `--embed-postgres` flag запускает встроенный Postgres subprocess (одна команда, без `docker run`); для production — managed Postgres (RDS / Cloud SQL / Supabase / Neon) или self-hosted.

**Позиционирование**:
- **Production-safe vibe backend** — закрывает разрыв «AI помог сделать backend за вечер, а через месяц всё разваливается»
- **LLM-native runtime** — schema/hooks/SDK structured так, чтобы агенты могли генерировать, читать, изменять контракты безопасно
- **Postgres-native** — RLS для tenancy, `LISTEN/NOTIFY` для realtime, `SKIP LOCKED` для job queue, `LTREE` для иерархий, `pgvector` для embeddings, `NUMERIC` для денег. Никаких abstraction-tax или dialect-router-compromises

## Документация

Документация разбита на тематические группы. Каждый файл самодостаточен, но cross-referenced.

### Старт

- [00 — Context, позиционирование, принципы, решения](docs/00-context.md)
- [01 — PocketBase feature parity audit](docs/01-pb-audit.md)
- [Glossary — терминология](docs/19-glossary.md)

### Архитектура

- [02 — Architecture: interfaces, modules, packages, dependencies](docs/02-architecture.md)

### Платформа (core capabilities)

- [03 — Data layer: DB, schema DSL, field types, migrations, transactions, filter, pagination, batch, view collections](docs/03-data-layer.md)
- [04 — Identity & access: users, auth flows, OAuth providers (35+), OTP/MFA, devices, tokens, RBAC, tenant enforcement](docs/04-identity.md)
- [05 — Realtime & subscriptions: API, transport (WS+SSE), broker (local+NATS), RBAC](docs/05-realtime.md)
- [06 — Hooks: JSVM bindings, isolation, Go hooks, internal eventbus](docs/06-hooks.md)
- [07 — Files & documents: file fields, thumbnails, document management, storage, retention](docs/07-files-documents.md)
- [08 — Document generation: XLSX, PDF, markdown templates](docs/08-generation.md)
- [09 — Mailer: providers, templates, flows, i18n](docs/09-mailer.md)
- [10 — Jobs: queue, cron, workflow](docs/10-jobs.md)
- [14 — Observability & operations: logging, audit, telemetry, settings, errors, lifecycle, backup, rate limiting, config, caching, encryption, security extended, update mechanism, streaming](docs/14-observability.md)
- [20 — Notifications system: in-app + email + push, preferences](docs/20-notifications.md)
- [21 — Outbound webhooks: HMAC-signed dispatch с retry / dead-letter](docs/21-webhooks.md)
- [22 — i18n full-stack: locale resolution, translatable fields, RTL, pluralization](docs/22-i18n.md)
- [23 — Testing infrastructure: `railbase test`, fixtures, helpers, mocks](docs/23-testing.md)
- [25 — Stripe billing: подписки + разовые продажи, каталог в БД, webhook-зеркало](docs/25-stripe.md)
- [26 — Authority: Delegation of Authority в ядре (v2.0 proposal — DoA gate, signed chains, no-bypass discipline)](docs/26-authority.md)
- [27 — Tasks: durable human-actionable work queue (v2.0 proposal — DoA-spawned tasks + embedder workflows)](docs/27-tasks.md)

### Поверхности

- [11 — Frontend SDK: TS generation, multi-language](docs/11-frontend-sdk.md)
- [12 — Admin UI: stack, 22 screens, UX, extension](docs/12-admin-ui.md)
- [13 — CLI: all commands](docs/13-cli.md)

### Расширения

- [15 — Plugins: system + catalog (orgs, billing, authority, cluster, workflow, saml, scim, push, doc-tools, mcp, etc)](docs/15-plugins.md)

### Планирование

- [16 — Roadmap: v0/v1/v1.1/v1.2/v2 phases](docs/16-roadmap.md)
- [17 — Verification: smoke + feature + load tests](docs/17-verification.md)
- [18 — Risks & open questions](docs/18-risks-questions.md)

## Принципы (короткая версия)

1. PocketBase-class DX как baseline — `./railbase serve`, открыл UI, получил REST + realtime + типизированный SDK
2. Фичи сложнее CRUD — opt-in модулями
3. Postgres как фундамент — все advanced features (RLS, ranges, intervals, LTREE, composite types, pgvector) используются first-class. Single↔cluster переключение через env, без replatform
4. Type-safety end-to-end — schema-as-Go-code → миграции → sqlc типы → TS SDK с zod
5. PB-compat через modes (`strict`/`native`), не цемент — JS клиенты PB работают drop-in
6. Rail (`/Users/work/apps/rail`) как референс паттернов, не продукт. Берём `tx`, `migrate`, `audit-через-bare-pool` — не берём ERP-домены

## Стек (короткая версия)

- Go 1.26+ (single static binary, cross-compile, no CGo)
- PostgreSQL 14+ (через `jackc/pgx/v5`); embedded-postgres optional для dev
- Required PG extensions: `pgcrypto`, `ltree`. Optional: `pg_trgm`, `btree_gist`, `pgvector`
- `dop251/goja` JS hooks
- `coder/websocket` realtime
- `xuri/excelize/v2` + `signintech/gopdf` для XLSX/PDF generation
- `disintegration/imaging` для thumbnails
- React 19 + Tailwind + Radix admin UI

См. [02-architecture.md](docs/02-architecture.md) для полного списка.
