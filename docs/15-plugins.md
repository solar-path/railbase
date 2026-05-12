# 15 — Plugins: system + catalog

## Plugin system

### Цель

Расширять Railbase без раздувания core бинарника. Plugins:

- Опциональны
- Изолированы от core (crash plugin → core продолжает работать)
- Имеют свой lifecycle и hot-update path
- Регистрируют HTTP routes, hooks, schema migrations через стандартный `Module` интерфейс

### Plugin RPC mechanism

**Решение до v1.1**. Три варианта:

1. **`hashicorp/go-plugin`** — battle-tested (Terraform, Vault); subprocess + gRPC; чуть тяжёлый
2. **Custom gRPC subprocess protocol** — полный контроль, lighter; писать самим
3. **WASI** — sandbox через wazero; modern; sealed; но overhead и сложнее debug

**Рекомендация**: starting с **custom gRPC subprocess protocol**. Простой, контролируемый, легко debug.

### Plugin manifest

Каждый plugin — отдельный бинарник + manifest:

```yaml
# plugins/railbase-billing/plugin.yaml
name: railbase-billing
version: 1.0.0
railbase_version: ">=1.0 <2.0"
description: Stripe / Paddle / LemonSqueezy billing integration
author: railbase-team
license: MIT
binary: railbase-billing
config_schema: ...                  # JSON schema для plugin settings
provides:
  - http_routes: /api/billing/*
  - admin_screens: /_/plugins/billing
  - hooks: $billing.*
  - tables: billing_*
  - eventbus_topics: billing.*
```

### Lifecycle

```
Core startup:
  1. Discover installed plugins (pb_data/plugins/)
  2. Verify manifest + signature
  3. Spawn plugin subprocess
  4. Establish gRPC connection
  5. Plugin registers routes/hooks/tables
  6. Run plugin migrations
  7. Plugin OnStart()

Core shutdown:
  1. Plugin OnStop() with timeout
  2. Kill subprocess if not graceful
```

### Plugin API (через `pkg/railbase/`)

```go
import "github.com/railbase/railbase/pkg/railbase/plugin"

func main() {
    p := plugin.New(plugin.Config{
        Name: "railbase-billing",
        Version: "1.0.0",
    })

    p.Schema(billing.Schema())
    p.Routes(billing.Routes())
    p.Hooks(billing.Hooks())
    p.OnStart(billing.OnStart)

    p.Serve()
}
```

Plugin импортирует только `pkg/railbase/` (public API), никогда `internal/`.

### Plugin distribution

- **GitHub releases** с manifest файлами (рекомендация)
- `railbase plugin install <github-url>` → fetches latest release matching railbase version
- Optional registry в v2 (если экосистема растёт)

### Plugin sandbox

- Subprocess isolation
- Resource limits (memory, CPU) configurable per-plugin
- Plugin не имеет direct DB access — только через core API (rate-limited)
- Plugin can't access core internals — gRPC contract only
- Crash → core логирует, restart with backoff

---

## Plugin catalog

### railbase-cluster

**Назначение**: distributed realtime / eventbus через embedded NATS server для крупных multi-instance deploys.

**Что делает**:
- Замена `LocalBroker + LISTEN/NOTIFY` на `NATSBroker`
- Embedded `nats-server/v2` — peer discovery через `RAILBASE_CLUSTER_PEERS` env
- Cross-instance event delivery с back-pressure / persistent streams
- JetStream optional для persistent realtime

**Tables**: ничего своего; piggybacks core eventbus

**Когда нужен**:
- Десятки реплик за load balancer
- Cross-region delivery
- Persistent streams (events не должны теряться при restart)
- Throughput > 50k events/sec на realtime

**Когда не нужен** (default path):
- 1-3 реплики на single Postgres → core LocalBroker + Postgres `LISTEN/NOTIFY` справляется
- Single-instance deploy

См. [05-realtime.md](05-realtime.md#cluster-natsbroker-plugin-railbase-cluster).

---

### railbase-orgs

**Назначение**: organizations entity для B2B SaaS.

**Архитектурный пересмотр**: per-tenant RBAC теперь в **core** (см. [04-identity.md](04-identity.md#rbac-—-site--tenant-scope-в-core-revised)). Plugin focuses на:

- `organizations` table (name, slug, settings, billing context)
- `organization_members` table (user_id, org_id, role_id, status, invited_by)
- **Invite lifecycle** (pending → accepted/expired/revoked) с email links
- **Seat counting** + integration с `railbase-billing`
- **Ownership transfer**
- **Member management UI**
- 38-ролевой каталог из rail (Owner, GL Accountant, CFO, Sales Director, Treasury Manager, etc.) **как seed-template** — `railbase init --template saas-erp` подгружает; не plugin-обязаловка

**Endpoints**:
- `POST /api/orgs` (create)
- `GET /api/orgs` (list — those user is member of)
- `POST /api/orgs/{id}/invites` (invite member)
- `POST /api/orgs/invites/{token}/accept`
- `DELETE /api/orgs/{id}/members/{userId}`
- `POST /api/orgs/{id}/transfer` (ownership)

**Admin UI**: Organizations screen

**Когда нужен**: multi-tenant B2B SaaS

**Когда не нужен**: single-tenant projects, simple multi-tenant без orgs

---

### railbase-billing

**Назначение**: subscription billing.

**Providers**:
- Stripe (primary)
- Paddle (v1.2+)
- LemonSqueezy (v1.2+)
- Braintree (community)

**Возможности**:
- **Subscriptions**: plans/pricing tiers, trial period, proration, upgrade/downgrade
- **Checkout sessions**: Stripe Checkout / Customer Portal — wrapped endpoints
- **Webhooks**: signature verification, idempotency-key handling, automatic retry на failures
- **Usage-based billing**: metered usage (запросы, storage, seats) — push в Stripe
- **Invoice generation**: использует core PDF generation; PDF attached к Stripe invoice
- **Tax**: Stripe Tax integration (auto-compute), VAT handling

**Tables**: plans, subscriptions, invoices, usage_events

**Endpoints**:
- `POST /api/billing/checkout` → Stripe Checkout session
- `POST /api/billing/portal` → Customer Portal
- `GET /api/billing/subscription` → current subscription
- `POST /api/billing/webhook` → webhook receiver
- `POST /api/billing/usage` → record usage event

**JS hooks**: `$billing.checkout()`, `$billing.recordUsage()`, `onBillingEvent()`

**Integration с `railbase-orgs`**: subscription owner = organization, seat-based pricing, plan limits enforce'атся через RBAC quotas

**Templates**: `railbase init --template saas` подключает `railbase-billing` с pre-defined plans

---

### railbase-authority

**Назначение**: approval engine — multi-step authorization чейны с conditions, delegations.

Прямой порт rail's authority module ([authority.engine.ts](src/modules/tenant/authority/server/authority.engine.ts) + [authority.evaluator.ts](src/modules/tenant/authority/authority.evaluator.ts)).

**Концепции**:
- **Policy** — «для resource/action с condition matching payload, нужна approval-chain [step1, step2, ...]»
- **Condition** — typed predicate: `{ field: "amount", op: "gt", value: 50000 }`
- **Chain step** — `{ step: N, roleId: "controller" }`
- **Request** — instance approval-запроса; status pending/approved/rejected/cancelled
- **Decision** — approve/reject с rationale, audit
- **Delegation** — user A → user B на period с optional per-policy

**Critical rules** (из rail):
- Single matching policy (overlapping → error)
- **R22a — initiator MUST NOT self-approve** (защита от multi-hat)
- Delegation resolution через role membership AT TIME of approval
- On-behalf-of attribution в audit

**API**:
- Schema DSL `.AuthorityGate(...)` декларативно для коллекций
- REST endpoints (`/requests/mine`, `/requests/pending`, `approve`, `reject`, `delegate`)
- JS hooks `$authority.checkOrSubmit()` + `onAuthorityApproved/Rejected`

**Admin UI screens**: My Requests, Pending Approvals (с rationale), Policies editor (visual builder), Delegations management, Authority audit

**Integration**: RBAC roles в chain steps; core audit для всех decisions; realtime events для requesters/approvers; mailer pre-configured templates; metrics (time-to-approval p50/p95)

**Не делает в v1**: branching chains, escalation rules, quorum (M-of-N), bulk approval

**Когда нужен**: B2B SaaS / ERP с spending limits, document review, multi-stage approvals

---

### railbase-saml

**Назначение**: SAML 2.0 SP (Service Provider).

**Library**: `crewjam/saml`

**Возможности**:
- Per-tenant IdP metadata config
- SP metadata endpoint
- SAML SSO flow (POST + Redirect bindings)
- Just-in-time user provisioning
- Attribute mapping (email, name, roles)
- Signed assertions verification

**Integration**: подключается как auth method к auth-collection: `schema.AuthCollection("users").AuthMethods(schema.SAML("okta"))`

---

### railbase-scim

**Назначение**: SCIM 2.0 provisioning endpoint для enterprise IdPs.

**Endpoints**:
- `/scim/v2/Users` (CRUD)
- `/scim/v2/Groups` (CRUD)
- `/scim/v2/ServiceProviderConfig`
- `/scim/v2/Schemas`
- Filters (eq, sw, ew, co, gt, ge, lt, le)

**Auth**: bearer token (issued via `railbase auth scim-token --tenant <id>`)

**Mapping**: SCIM User → Railbase user; SCIM Group → role assignments

**Async provisioning queue** — slow client не блокирует mass-create

---

### railbase-workflow

**Назначение**: saga / workflow engine для long-running multi-step processes.

См. [10-jobs.md](10-jobs.md#saga--workflow-engine--plugin-railbase-workflow).

---

### railbase-push

**Назначение**: mobile push notifications.

**Providers**:
- FCM (Firebase Cloud Messaging) — Android, web, iOS
- APNs (Apple Push Notification service) — iOS native

**API**:
- `POST /api/push/devices` — register device token
- `POST /api/push/send` — send to user/topic
- JS hooks: `$push.send({ user, title, body, data })`

**Integration с mailer**: одинаковый template engine (markdown) для push payloads

---

### railbase-pdf-html

**Назначение**: HTML → PDF rendering через headless Chrome.

**Library**: `chromedp/chromedp`

**Why plugin**: Chromium binary ~200 MB — нельзя вшивать в core single-binary contract.

**Альтернатива**: docs sidecar контейнер с weasyprint/playwright (для пользователей, которые не хотят Chrome).

**API**: `$export.pdfFromHTML(html, options)` через JS hooks; CLI `railbase export pdf-html ...`

---

### railbase-doc-ocr

**Назначение**: OCR для scanned PDFs / images в documents repository.

**Sidecar**: Tesseract OCR

**Triggers**: `onDocumentUploaded` где mime=`image/*` или PDF без extracted text → OCR job → result stored в `_document_extracted_text`

---

### railbase-doc-office

**Назначение**: extraction text из DOCX/XLSX/PPTX uploaded as documents.

**Sidecar**: LibreOffice headless

**Use case**: full-text search across Office documents; AI summarization через MCP

---

### railbase-pdf-preview

**Назначение**: page-1 image preview для PDF documents.

**Sidecar**: poppler / `pdftoppm`

**Triggers**: lazy on first preview request; cached

---

### railbase-esign

**Назначение**: e-signature workflows.

**Integrations**: DocuSign, HelloSign / Dropbox Sign, Adobe Sign

**Flow**:
1. Document uploaded в Railbase
2. `$esign.sendForSignature(documentId, recipients)` → opens envelope в provider
3. Provider webhook → status update в Railbase
4. Signed copy attached как new version

---

### railbase-docx

**Назначение**: DOCX export (Word documents).

**Library**: TBD — нет хороших pure-Go writers; possibly через LibreOffice headless или sidecar service.

---

### railbase-mcp

**Назначение**: Model Context Protocol server для LLM agents (Claude, ChatGPT, etc.).

**Возможности**:
- Schema introspection (`schema_get`, `schema_list_collections`)
- Safe data mutations (`record_create`, `record_update`, `record_query` с RBAC)
- Hook editing (`hook_list`, `hook_get`, `hook_save` с safety checks)
- Documents API exposure
- Realtime tap для agents

**Auth**: agent uses scoped API token

**Use case**: AI agents (Cursor, Claude Code, etc.) могут безопасно interact с Railbase backend через MCP

**Killer feature**: Railbase становится first-class citizen для AI-era development

---

### railbase-wasm (v2+)

**Назначение**: alt-runtime для hooks через `wazero`. Sandboxed, fast, multi-language (Rust/AssemblyScript/Go compile to WASM).

**Use case**: production-critical hooks где goja overhead неприемлем; multi-language hooks

---

### railbase-ghupdate

**Назначение**: auto-update Railbase через GitHub releases (PB feature `plugins/ghupdate/`).

**Возможности**:
- Periodic check для new releases
- Admin UI «update available» banner
- One-click update (downloads binary, replaces, restarts)
- Rollback option

---

### railbase-postmark / railbase-sendgrid / railbase-mailgun

**Назначение**: дополнительные mailer providers через REST API.

**Возможности**: send + bounce/open/click webhooks → `_email_events` table

---

### railbase-geo

**Назначение**: full geo support через PostGIS extension (basic `POINT` тип уже в core через `schema.Coordinates()`; этот plugin добавляет полигоны, distance queries, spatial joins).

**API**: `schema.GeoPoint()`, `schema.GeoPolygon()`, geo queries (within, distance, intersects)

---

### railbase-fx

**Назначение**: foreign-exchange rates для currency-field conversions.

**Providers** (adapter-based):
- ECB (European Central Bank) — free, daily fiat rates
- Open Exchange Rates — free tier; currencies + crypto
- Fixer — paid, historical
- CoinGecko — crypto rates
- Custom provider — user-defined webhook

**API**:
```js
const usd = $fx.convert({ from: "EUR", amount: "100", to: "USD" })
// { amount: "108.50", currency: "USD" }

const historical = $fx.convertAt({ from: "USD", to: "RUB", amount: "1000", at: "2025-01-15" })
// uses historical rate at given date

$fx.rate("USD", "EUR")                                            // current
$fx.rate("USD", "EUR", { at: "2025-01-15" })                      // historical
$fx.rates("USD")                                                   // all rates from base
```

**Cache**: rates cached с TTL (default 1h для current; permanent для historical).

**Auto-conversion в schema**:
```go
schema.Field("price", schema.Currency().
    AllowedCurrencies("USD", "EUR", "RUB").
    AutoConvertTo("USD"))                                          // generates virtual field price_usd
```

**Use cases**:
- Multi-currency invoicing с conversion to functional currency для accounting
- E-commerce regional pricing
- Treasury balance в reporting currency
- Historical rate lookup для accounting compliance (rate at transaction time)

---

### railbase-sql-playground

**Назначение**: raw SQL playground в admin UI (admin-only, opt-in).

**Why plugin**: raw SQL обходит RBAC; дополнительные safety guards и audit обязательны.

**Features**: query history, result table view, export, save queries

---

### railbase-analytics

**Назначение**: events tracking, funnels, cohorts, retention metrics поверх core data.

Не путать с health/metrics admin UI (тот для оператора, описан в [14-observability.md](14-observability.md)). Этот — **бизнес-аналитика** для product teams.

**Возможности**:
- Event ingestion: `$analytics.track(userId, "post_created", { category: "..." })`
- Funnels: signup → first post → return next day, с conversion rates
- Cohort analysis: retention curves по signup date
- A/B test attribution
- Per-tenant scoping
- Export to CSV / API для downstream BI tools

**Storage**: `_analytics_events` table, declarative partitioning by date (`PARTITION BY RANGE (created_at)`) с monthly partitions; auto-detach старых partitions через retention job.

**Не делает**: replacement Mixpanel/Amplitude — это lightweight in-house для self-hosted.

---

### railbase-cms

**Назначение**: CMS layer — page builder с blocks, multi-version content, scheduled publishing.

**Возможности**:
- Page builder с composable blocks (hero, text, image-gallery, embed, etc.)
- Block library extensible через user-defined blocks
- Multi-version content с draft/preview/published states
- Scheduled publishing (publish_at)
- A/B variants
- SEO metadata (Open Graph, Twitter cards, schema.org)
- Sitemap generation
- Multi-language content (через core i18n)

**Use case**: marketing pages, blogs, knowledge bases, documentation sites поверх Railbase.

**Не делает**: SSR / static generation — leave to Next.js/Astro/SvelteKit с consuming Railbase API.

---

### railbase-compliance

**Назначение**: enterprise compliance helpers.

**Возможности**:
- **GDPR**: data export для user (machine-readable JSON dump всех related data); right-to-erasure flow с PII redaction
- **SOC2**: audit report generators (access logs, change logs, security events) — pre-formatted для auditor review
- **HIPAA**: BAA helpers, PHI tagging, encryption-at-rest enforcement, access controls templates
- **Data classification**: tag fields с `Classification("pii"|"phi"|"financial")`; reports по data inventory
- **PII discovery scanner**: автоматический scan коллекций для potential PII patterns (emails, phones, SSN regexes); manual review flow
- **Retention enforcement**: automatic data lifecycle с legal-hold integration

**Не заменяет**: legal review, auditor work — это helpers для compliance officer.

---

### railbase-payment-manual

**Назначение**: invoicing без Stripe для проектов где Stripe не подходит (regulatory, geo, B2B with bank transfers).

**Возможности**:
- Invoice generation (PDF через core PDF gen)
- Customer ledger
- Payment recording (manual entry: bank transfer, cash, check)
- Reconciliation flow с bank statements
- Reminder emails
- Aging reports (30/60/90 days outstanding)
- Multi-currency support

**Use case**: B2B SaaS в странах где Stripe не работает; invoicing вместо subscription billing; manual reconciliation workflows.

**Можно использовать совместно** с `railbase-billing` (Stripe для one segment, manual для другого).

---

### railbase-accounting

**Назначение**: accounting domain types и validation поверх core finance/currency fields.

**Field types**:
- `gl_account` — Chart of Accounts hierarchy с `tree_path` под капотом + GAAP/IFRS account number validation
- `cost_center` — Cost allocation hierarchy
- `fiscal_period` — FY-aware period (Q1 FY2026), close states (open/draft/closed/locked)
- `journal_entry` — debit/credit lines с balance check (Σ debits = Σ credits)

**Capabilities**:
- Period close workflow с lock states
- GL posting validation (closed periods reject, unbalanced entries reject)
- Trial balance / general ledger reports
- Multi-book (cash basis vs accrual) support

**Use cases**: built-in accounting для SaaS / ERP проектов без external accounting system.

---

### railbase-vehicle

**Назначение**: vehicle-domain field types.

**Field types**:
- `vin` — Vehicle Identification Number (17 chars + checksum)
- `license_plate` — per-country format validation
- `vehicle_class` — passenger/commercial/motorcycle/etc.

**Use cases**: fleet management, transportation, insurance, parking systems.

---

### railbase-id-validators

**Назначение**: per-country PII / national identifier validation.

**Field types**:
- `passport` — passport number per-country
- `national_id` — national ID per-country (e.g. РФ паспорт серия+номер, US driver license)
- `drivers_license` — per-country format

**Compliance**: ships с `Encrypted()` automatically (PII), audit-логирование read access.

**Use cases**: KYC flows, employee onboarding, government services, insurance.

---

### railbase-bank-directory

**Назначение**: BIC → bank lookup catalog для validation и UX.

**Возможности**:
- BIC → bank name / address resolution
- Lookup на основе IBAN's country + bank code
- Periodic refresh от SWIFT registry

**Не shipping**: full SWIFT directory (proprietary); freemium adapter pattern.

---

### railbase-qr-payments

**Назначение**: composite field types для national payment QR standards.

**Standards**:
- **СБП** (Россия) — Система Быстрых Платежей. MerchantID + amount + currency + reference. Validates per ЦБ РФ spec.
- **EPC QR Code** (EU SEPA) — IBAN + BIC + beneficiary + amount + remittance info. ECC Level M required.
- **PIX** (Brasil) — PIX key (CPF/CNPJ/email/phone/random) + merchant + amount. CRC16 checksum.
- **Swiss QR-bill** (Switzerland) — IBAN + creditor + debtor + amount in CHF/EUR.
- **Bancontact** (Belgium) — payment QR.
- **Giro** (Germany) — SEPA-based.

**API**:
```go
schema.Field("invoice_pay_qr", payments.SBP().
    MerchantID().
    Amount("total", "currency").
    Reference("invoice_no"))

schema.Field("epc_qr", payments.EPC().
    BankAccount("our_iban", "our_bic").
    Beneficiary("our_company_name").
    Amount("total").
    Reference("invoice_no"))
```

**Use cases**: invoice PDFs с встроенным payment QR (customer scan → pay), POS receipts, SaaS subscription billing в countries с national QR systems.

**Compliance**: per-standard validation; rejected payloads с invalid format → 400.

---

### railbase-geocode

**Назначение**: geocoding для Address fields через external providers.

**Providers**:
- Nominatim (OpenStreetMap, free)
- Google Geocoding API
- Mapbox Geocoding
- Yandex Geocoder

**Capabilities**:
- Address → lat/lng resolution
- Reverse geocoding (lat/lng → address)
- Address validation / standardization
- Address autocomplete suggestions

**Use cases**: shipping address verification, store locator, delivery routing.

---

### railbase-search-meili / railbase-search-typesense

**Назначение**: external search engine adapters при коллекциях > 1M rows.

**Why plugin**: Postgres `tsvector` + GIN покрывает миллионы records без проблем; за этим порогом (десятки миллионов, faceting, typo tolerance, multilingual stemming pipelines) выгоднее dedicated search engine.

**Возможности**:
- Auto-sync schema → search index (faceted, weighted)
- Real-time updates через eventbus subscription
- Search API через Railbase (clients не видят прямой meilisearch/typesense API)
- Admin UI search analytics
- Hybrid search: SQL filter + relevance scoring

**Adapter-pattern**: same API в Railbase, swap engine.

---

---

## Plugin templates

`railbase init --template <name>` использует комбинации plugins:

| Template | Plugins included |
|---|---|
| `basic` | none (PB-equivalent) |
| `saas` | railbase-orgs, railbase-billing, railbase-authority, railbase-fx |
| `saas-erp` | + 38-ролевой каталог seed, railbase-fx, multi-currency examples |
| `mobile` | railbase-push |
| `ai` | railbase-mcp, --with-vec flag |
| `enterprise` | + railbase-saml, railbase-scim, audit sealing |
| `fintech` | railbase-fx, railbase-billing, railbase-authority, audit sealing, --with-crypto-currencies |

См. [16-roadmap.md](16-roadmap.md) для phasing plugins.
