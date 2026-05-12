# 18 — Risks & open questions

## Главные риски и митигации

### Архитектурные

| Риск | Митигация |
|---|---|
| **SQL parity ад** | Portable subset, dialect-explicit features, golden tests, documented divergences |
| **PB compat burden** | Modes (strict/native/both); deprecate `both` в v2; native — основа |
| **Module coupling degradation** | Layered architecture с CI lint на imports; module boundaries enforced |
| **Plugin runtime complexity** | Решение по go-plugin/gRPC/WASI до v1.1; начать с одного простого plugin (`railbase-cluster`) |
| **API versioning разнобой** | Strict semver с v1; deprecation cycle min 2 minor versions; native versioning через URL prefix |

### Безопасность

| Риск | Митигация |
|---|---|
| **Tenant leakage** | Compile-time enforcement через type-разделённые executors; CI gates на raw SQL; runtime double-check |
| **Hook sandbox leaks** | Жёсткие лимиты с дня 1, метрики, recycling, kill-on-OOM |
| **Filter parser injection** | AST-based parser; никогда не concat в SQL; field validation против schema registry; все values параметризованы |
| **Mailer template injection (XSS, header injection)** | Redact в slog; sanitize via bluemonday для HTML output; template engine не выполняет arbitrary code |
| **Image processing memory bomb (zip-bomb-style)** | Max input image dimension cap (default 10000x10000); decode budget; reject если превышено |
| **Stripe webhook replay attacks** | Idempotency-key + signature verification + webhook event ID replay-protection (stored hash table) |
| **Multiple auth collections путаница** | Sessions namespaced по collection; signin endpoints явные `/api/collections/{name}/auth-with-password` |
| **Document access leakage cross-tenant** | Tenant scope enforcement через документы owner_type/owner_id chain; admin oversight явный crossWorkspace flag с audit |
| **Identity confusion (system vs app users)** | Жёсткое разделение таблиц с `_` префиксом |

### Reliability

| Риск | Митигация |
|---|---|
| **Hooks побеждающие data integrity** | EventBus deferred-publish; AfterCreate выполняется после commit |
| **Realtime fan-out blow up при 10k subscribers** | Per-event filter-eval cap, expand-cache per event, backpressure drops, cluster-mode через NATS |
| **Realtime presence overhead в admin UI** | Per-route presence channel с throttle; auto-disconnect после 5 min inactivity |
| **Audit storage explode** | Retention policy (default 1 year), opt-in archival to S3 |
| **Document storage growth неконтролируемый** | Per-tenant quotas с hard limits; admin UI usage dashboard; auto-archival job; retention policies enforcement |
| **Export съедает RAM на больших коллекциях** | Streaming writers (excelize.StreamWriter, cursor iter), per-request memory ceiling, async mode > 100k rows |

### Compliance

| Риск | Митигация |
|---|---|
| **Document hard-delete случайно** | NO DELETE по дизайну (immutable repository); только soft archive; permanent purge только manual CLI с confirmation; legal hold блокирует archive |
| **Document title collision (vendor_v2_FINAL_FINAL.pdf)** | Unique constraint (tenant, owner_type, owner_id, title) → новая версия вместо дубликата; UI явно показывает «creating version N» при re-upload |

### Authority

| Риск | Митигация |
|---|---|
| **Authority policies overlap (silent ambiguity)** | Engine throws при multiple matches; admin UI policy editor warns при перекрытии conditions перед save |
| **Authority self-approve loophole** | R22a hardcoded в engine: initiator≠approver всегда; covered тестом |
| **Authority deadlock (никто не может approve)** | Validation при создании policy: каждый chain step имеет ≥1 user в этой role; runtime warning если step empty |

### Domain-specific field types

| Риск | Митигация |
|---|---|
| **Float drift в finance** | `decimal.Decimal` Go-side, `decimal.js` client-side; storage `NUMERIC(20, N)` Postgres native; operations через generated helpers, не raw `+/-/*` |
| **Currency mixing silent bugs** | Mixed-currency arithmetic запрещено по умолчанию; explicit FX conversion required; runtime panic в dev, audit + 400 в prod |
| **Tel parsing inconsistencies** | E.164 storage canonical; UI showing formatted variants; libphonenumber update path documented (rare breaking changes в country rules) |
| **ISO 4217 catalog stale** | Embedded catalog updated с каждым release; deprecated currencies (e.g. ZWL) flagged warning при use; crypto opt-in flag |
| **FX rates downtime / staleness** | Cache с TTL + stale-on-error fallback; admin UI alert если rate older than threshold; never silent-convert с unknown rate |
| **Postgres version skew** | Min PG 14 enforced на startup (`SELECT current_setting('server_version_num')::int >= 140000` или fail-fast); features требующие PG 15/16 (`MERGE`, simplified JSON path) fall back или error gracefully |
| **Tax ID catalog stale** | Per-country format rules могут меняться; embedded catalog updated с каждым release; warning при use deprecated формата |
| **IBAN country format drift** | New countries добавляются в IBAN registry; embedded catalog updated с каждым release |
| **Person name cultural assumptions** | CLDR data — best-effort; users могут override через `.PreferredOnly()` если их culture не covered |
| **Quantity unit conversion edge cases** | Cross-domain (mass↔length) — explicit error; same-domain — built-in factor table; floating-point precision использует decimal arithmetic |
| **State machine race conditions** | Optimistic concurrency через `version` field; concurrent transition attempts → second one fails с `ErrVersionConflict` |
| **Tree path corruption (cycles, orphans)** | Insert/move validates parent existence; depth column maintained; periodic integrity check job |
| **SequentialCode counter contention** | Atomic `UPDATE ... RETURNING` per-tenant; high contention rare (PO numbers ~ low frequency); если bottleneck — sharded counters |
| **Address geocoding cost** | Geocoding opt-in (plugin), cached; user pays для high-volume |
| **QR rendering CPU pressure** | Generated lazy + cached; per-request rate limit; admin может pre-warm для known records |
| **QR logo overlay corruption** | Logo overlay требует ECC ≥ Medium; admin UI warning при попытке ECC=Low с logo |
| **National QR standard drift** | Per-standard validation testable via reference vectors; embedded validators updated с plugin releases |
| **Tree pattern wrong choice** | Documentation table comparing trade-offs; admin UI hint про cost при first hierarchy creation |
| **Materialized path move expensive** | Async option для large subtrees (>1000 descendants); UI confirmation modal предупреждает |
| **Nested set lock contention** | Insert/move acquires range lock; для high-write trees recommend closure table |
| **DAG cycle introduction** | Pre-validate edge addition (run BFS); transactional check; `PreventCycles()` modifier |
| **Closure table size explosion** | Storage cost = ~depth × node count; recommend max depth 10 для практичности |
| **Recursive CTE performance** | Helpers benchmark на типичные tree sizes; depth limits enforced; `LTREE` GiST index recommended over recursive CTE для materialized-path use cases |
| **RLS bypass через bug** | `FORCE ROW LEVEL SECURITY` обязателен; integration test проверяет что записи tenant B недоступны при подмене `railbase.tenant` setting; `WithSiteScope` обязательно audit'ится |
| **`set_config` not propagated через connection release** | `is_local=true` (third arg) делает settings tx-scoped; integration test проверяет что после `pgxpool.Release` следующий acquire не видит prior settings |

### Generation

| Риск | Митигация |
|---|---|
| **PDF templates превращаются в шаблонизатор-монстра** | Жёсткий scope: markdown + Go-template helpers (date/money/each), без if-else логики; сложное → programmatic Go API |
| **HTML→PDF tempting in core** | Vendored Chrome ~200 MB ломает single-binary contract; жёстко в plugin |

### Admin UI

| Риск | Митигация |
|---|---|
| **Admin UI scope creep** | Зафиксированный список 22 screens с явным «не делает»; всё сложное → plugins |
| **Admin UI dogfooding break** | CI gate: admin UI build против generated SDK; каждый PR который меняет SDK shape должен пройти admin UI build |
| **Admin UI bundle size** | 3 MB gzipped budget; CI fail если превышено; lazy-load monaco/tiptap/charts |
| **Admin UI security (XSS через user data)** | Все user-content через React text rendering (auto-escape); richtext через bluemonday-sanitized HTML; Content-Security-Policy strict |

### SDK

| Риск | Митигация |
|---|---|
| **SDK drift между runtime** | `_meta.json` hash в SDK; client warning при mismatch |

### Project / governance

| Риск | Митигация |
|---|---|
| **Bus factor 1** | Open governance с самого начала; multi-maintainer GH org до v1; RFC процесс; public roadmap |

---

## Open questions

### Архитектурные

- **Plugin runtime**: `hashicorp/go-plugin` vs custom gRPC vs WASI. Решение до v1.1. Рекомендация: custom gRPC (полный контроль, lighter), если боль — мигрировать на go-plugin.
- **MCP server timing**: v1.2 или раньше? Может быть killer feature для AI-era позиционирования.
- **CLI library**: `cobra` vs `kong` vs `urfave/cli`. Cobra — стандарт, но громоздкий. Низкий приоритет.

### Платформа

- **Vector search**: `pgvector` extension auto-enable при first `schema.Vector(N)` или explicit opt-in? Recommend: auto-enable, fail если no `CREATE EXTENSION` rights.
- **Local-first / offline-first sync** — отдельный plugin или v2-feature?
- **Encryption at rest**: field-level через `pgcrypto.pgp_sym_encrypt` (key in app config), Postgres TDE на managed providers (RDS/Cloud SQL handle), или not at all? Recommend: app-level field encryption через `.Encrypted()` modifier, infrastructure-level — let users manage.
- **Embedded Postgres production hazard**: `--embed-postgres` явно «only dev» — нужен ли hard guard (refuse start с `--embed-postgres` если `RAILBASE_ENV=production`)? Recommend: yes, plus warning banner в admin UI если detected.
- **Postgres min version drift**: PG 14 EOL Nov 2026 — bump min до PG 15 в Railbase v2? Document EOL alignment policy.
- **Time/clock injection**: `clock.Now()` через interface для тестов — обязательно с v0?
- **Multi-region read replicas**: managed Postgres providers handle (Supabase, Neon, RDS Multi-AZ) или Railbase должен иметь explicit read-replica routing helper? Recommend: helper `ReadOnly(ctx)` который routing'ит queries к replica connection string если configured.
- **Image processing с CGo**: `disintegration/imaging` pure-Go, но slower; `vipsgen` через libvips даёт 10x скорость + WebP/AVIF — стоит plugin?
- **Filter parser**: писать с нуля или взять `expr-lang/expr`? Custom даёт полный контроль над security и magic vars; expr — battle-tested, но придётся ограничивать функционал.

### Realtime

- **Realtime resume window**: default 5 min — достаточно? Хранение event log в memory или persisted в `_realtime_events` table для recovery после restart?
- **View collections с realtime**: triggers на underlying tables → `pg_notify` → broker dispatches; альтернатива polling. Recommend: triggers (real-time semantics), polling fallback для view'ов с complex aggregations где trigger logic нетривиален.
- **`pg_notify` payload limit (8000 bytes)**: если событие крупнее — публикуется ID + lazy fetch. Когда автоматический fallback vs explicit warning у пользователя?

### EventBus

- **EventBus persistence**: at-least-once для важных событий через jobs queue, или отдельный механизм?

### Identity

- **Multiple auth collections в strict mode**: PB поддерживает с 0.20+; полная compat или only `users` collection в strict?
- **WebAuthn cross-device**: bluetooth / hybrid transport — supported в core или plugin?

### Documents

- **Hard-delete для dev**: `--documents-allow-hard-delete` flag (true в dev, false в prod) или strict immutable? Strict сложнее для dev iteration.
- **Text extraction в core или plugin**: pure-Go PDF parsers limited; commercial libs heavy. Рекомендация: opt-in flag в core с simple PDFs, OCR/Office в plugins.
- **GDPR erasure** — semantics: erase metadata vs erase bytes? Bytes erasure ломает hash chain audit; metadata erasure обычно достаточно.
- **Cross-tenant document references**: contract между tenants A и B — кто owns? В rail только single tenant scope. Add multi-tenant docs в v2.
- **Versioning после rename** — если user rename document, старые versions remember old title или migrate? Recommend: keep version history с per-version title (audit-friendly).

### Domain-specific field types

- **Crypto currencies в core catalog**: BTC/ETH/USDC/etc — opt-in flag (`--with-crypto-currencies`) или нет вообще (только plugin)? Stable codes vs volatile (новые stablecoins появляются часто). Рекомендация: opt-in flag.
- **Currency precision strategy**: `.AutoPrecision()` vs `.Precision(N)` default? `.AutoPrecision()` правильный для money UI, но менее предсказуемый для accounting where precision должен быть fixed. Рекомендация: документация показывает оба паттерна.
- **Tel field в auth collection identifier**: можно ли использовать `tel` как login identifier (вместо/вместе с email)? PB поддерживает username; phone-only signin распространён в emerging markets.
- **Finance / Currency aggregation в filter**: можно ли `?filter=amount.amount > 100`? Path syntax для composite types.
- **FX rate caching strategy**: per-tenant rates vs global? Multi-tenant SaaS может использовать разные FX providers per-tenant.
- **Currency display localization**: server-side rendering (PDF, email) requires locale; куда хранить per-user locale preference — auth collection field или separate user_preferences?
- **Tax ID country coverage**: ~30 стран в initial catalog. Какие нужны критично для v1, какие — community contributions? Recommend: top-20 economies + common offshore (BVI, Cayman) первоначально.
- **Address autocomplete**: built-in без external geocoding или только через `railbase-geocode` plugin? Recommend: plugin only (avoid free-tier provider lock-in).
- **State machine guards беспорядок**: transitions могут require role / data validation / authority approval. Все три путаются. Recommend: clear separation — `.RequireRole()` для simple role check, `.RequireApproval()` для authority gate, `.Validate()` для data check.
- **Tree path move semantics**: subtree move (atomic) — переписывает paths всех descendants → expensive operation. Limit max subtree size? Async для large trees?
- **Quantity unit catalog extensibility**: user может add custom unit (e.g. industry-specific). Через DSL или admin UI? Storage / sharing?
- **Status visualization scale**: 50+ states overload visual graph. Auto-collapse далёких states? Или recommend keep states < 20?
- **SequentialCode reset semantics**: yearly reset с timezone aware (`Europe/Moscow` 31 December 23:00 UTC ≠ January 1 локально). Какая timezone — site-default vs tenant-default?
- **QR ECC level default**: Medium — sensible default? Higher = более robust но fewer modules.
- **QR storage strategy**: на disk (lazy generated, cached) vs в БД (always available, larger storage). Recommend: disk + signed URLs.
- **Tree pattern auto-suggest**: admin UI должен hint optimal pattern based на usage stats (write rate, tree size, query patterns)?
- **Recursive CTE depth limit**: Postgres has no hard limit; Railbase enforces logical max (configurable, default 100) для guard от infinite recursion bugs.
- **Tree move audit detail**: каждый affected descendant — отдельный audit row или один aggregated row для bulk move?
- **DAG vs tree migration**: если коллекция стартовала с adjacency list и нужен multi-parent — как migrate? Recommend: explicit `.DAG()` modifier change + migration generator.
- **National payment QR coverage**: какие страны критично для v1.2? Recommend: top-5 (RU/EU/Brasil/Switzerland/India UPI) + community contributions.

### Mailer

- **DKIM/SPF/DMARC docs**: помогать пользователю настраивать DNS records, или only docs?
- **Bulk send / newsletter mode**: отдельный flow для рассылок (с unsubscribe links, list management)? Или leave to пользователю / external services (Mailchimp/etc.)?

### Generation

- **HTML→PDF strategy**: plugin `railbase-pdf-html` через chromedp (~200 MB Chrome) — это правильный путь, или есть смысл поискать pure-Go HTML→PDF? Альтернатива: docs sidecar контейнер с weasyprint/playwright.
- **Template language**: markdown + Go-template (выбрано) vs Liquid vs Handlebars. Go-template — minimal deps, но менее знакомый. Если LLM-friendly — handlebars читается лучше.
- **Native chart rendering в PDF**: gopdf не имеет built-in charts; для PDF charts — render в PNG через `go-chart` и embed как image.

### Authority

- **Branching chains** (if amount > X then chain A, else chain B) — нужно ли в v1 или решать через multiple policies с conditions?
- **Escalation rules** — auto-escalate если no decision за timeout?
- **Quorum** — M-of-N approvers on single step?
- **Bulk approval** UI — approve несколько requests одной кнопкой?
- **Out-of-band notifications** — Slack/Teams/SMS интеграция через plugins?

### Billing

- **Stripe в template SaaS**: подключать `railbase-billing` сразу с дефолтными plans (free/pro/enterprise) или пустой scaffold?

### Admin UI

- **Admin UI build artifact в репо**: `dist/` коммитится (proximate, no build for users) или generated в CI (clean repo, but build step)? Рекомендация: коммитить — single binary философия требует «git clone && go build».
- **Admin UI Bun vs Node для build**: Bun уже в стэке rail; faster but extra dep. Node — universal. Рекомендация: Bun для dev, Node-compat для CI.
- **Admin UI extension API**: позволять plugins регистрировать свои screens (`/_/plugins/{name}/`) с iframe/module federation? Iframe простой и safe; module federation мощный но сложный. Рекомендация: iframe в v1.1, MF возможно в v2.
- **Admin UI offline support**: Service Worker для offline view of cached records? Полезно для read-heavy ops, сложно для realtime. Низкий приоритет.
- **Schema editor в admin UI**: read-only (рекомендую) vs allow-edit с auto-migration? Edit = удобно для vibe, но конфликт со schema-as-code source-of-truth. Рекомендация: read-only в v1, обсудить в v1.2.

### SDK

- **Auto-generated React hooks** в v1.2+ optional? Низкий приоритет — frameworks эволюционируют, churn высокий.
- **Codegen via `railbase generate sdk` vs runtime introspection**: codegen better DX, но требует rebuild. Default codegen, fallback runtime для quick prototyping?
- **Python SDK timing**: AI/ML use cases растут — может v1.2 vs v2?
