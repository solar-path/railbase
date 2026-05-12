# Railbase

Universal Go backend поверх PostgreSQL. PocketBase-class DX, production-grade фундамент.

> **Статус**: v0 released (2026-05-10); **v1 SHIP unblocked** (2026-05-12). Все критические-path gate'ы (§3.14 verification audit, `.goreleaser.yml` + 30 MB binary budget, 6-target cross-compile, `make verify-release` one-shot pre-tag chain) ✅. Release tag — единственный оставшийся operator-шаг.
>
> **v1.x bonus-burndown в работе** (post-SHIP polish без блокировок): за один автономный цикл закрыто ~20 sub-слайсов в v1.7.30-v1.7.34 — `send_email_async` / `scheduled_backup` / `audit_seal` (Ed25519 chain — pulled from v1.1) / `orphan_reaper` builtins; mailer hooks + auth hooks dispatchers; **WebSocket transport** alongside SSE; **ICU plurals + `.Translatable()`** end-to-end (5 rule families); cache wiring slices 1-2 (4 production caches registered); RBAC bus-driven invalidation; anti-bot middleware; `MockHookRuntime` + `MockData` (gofakeit) testing harness; goja stack-cap; cross-package `ErrPermanent` chain; pre-existing oauth flaky-base64-padding bug fix. **Honest v1 plan.md completion: ~95%** (~143 of ~150 line items). Оставшийся остаток — explicit v1.1+ scope (S3/OTel/encryption/cluster) и blocked-by-design Documents track (v1.3.2).
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
