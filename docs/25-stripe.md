# 25 — Stripe billing: подписки и разовые продажи

Единое решение для приёма платежей: **подписки** (recurring) и **разовые продажи** (one-time — как из каталога, так и ad-hoc на произвольную сумму). Каталог продуктов/цен ведётся в БД Railbase и **пушится вверх** в Stripe; данные о клиентах, подписках и платежах зеркалируются обратно через подписанный webhook.

Не путать с `21-webhooks` — то **outbound** (Railbase шлёт события наружу). Здесь — интеграция со Stripe: часть вызовов исходящие (создание продуктов, PaymentIntent'ов), часть входящие (Stripe → `/api/stripe/webhook`).

## Хранение настроек

Ключи Stripe лежат в таблице `_settings` под namespace `stripe.*` (тот же plaintext-JSONB store, что у мейлера), а **не** в env — оператор правит их из admin UI в рантайме без рестарта:

| Ключ | Назначение |
|---|---|
| `stripe.secret_key` | `sk_test_…` / `sk_live_…` — режим (test/live) выводится из префикса |
| `stripe.publishable_key` | `pk_test_…` / `pk_live_…` — безопасен для браузера |
| `stripe.webhook_secret` | `whsec_…` — проверка подписи входящих webhook'ов |
| `stripe.enabled` | мастер-выключатель: при `false` любой вызов Stripe — no-op |

Секреты редактируются по контракту **keep-if-empty**: пустое поле при сохранении оставляет сохранённый ключ нетронутым. `GET` конфига никогда не возвращает секрет — только `*_set` флаги + короткий hint (`sk_test_FAK…7890`).

### Structured warnings в `GET /api/_admin/stripe/config`

Ответ содержит `warnings: []{code, message}` — admin UI рендерит их
баннером над формой. Чтобы оператор не оставлял включённый Stripe без
рабочего webhook'а:

| Code | Условие | Message hint |
|---|---|---|
| `webhook_secret_missing` | `enabled && !webhook_secret_set` | Указывает на `stripe listen --forward-to localhost:8095/api/stripe/webhook` + место для вставки `whsec_…` |
| `secret_key_missing` | `enabled && !secret_key_set` | Outbound calls будут падать — поставить `sk_test_…` / `sk_live_…` |

`computeStripeWarnings` — pure функция в
`internal/api/adminapi/stripe.go`. FEEDBACK #15.

## Модель данных

Миграция `0028_stripe` — шесть `_`-таблиц:

- **`_stripe_products` / `_stripe_prices`** — *локальный source of truth*. Оператор создаёт их в admin UI, сервис пушит в Stripe и проставляет обратно `stripe_*_id`. Цена с пустым `stripe_price_id` — "не запушена"; видно в UI, чинится кнопкой **Push catalog**.
- **`_stripe_customers` / `_stripe_subscriptions` / `_stripe_payments`** — *зеркало*. Строки создаются checkout-эндпоинтами и поддерживаются в актуальном состоянии webhook-обработчиком.
- **`_stripe_events`** — лог идемпотентности webhook'ов (ключ — Stripe event id, `ON CONFLICT DO NOTHING`).

Деньги хранятся как Stripe — целые minor units (центы) в `amount` / `unit_amount`, никаких float.

### `_stripe_products.external_id` (миграция 0033)

Embedder'ы, которые ведут собственную `products`-коллекцию ниже по
стеку (shop / catalog / SKU) и хук'ом пушат каталог в Railbase,
получают опциональный `external_id TEXT` на `_stripe_products` плюс
**partial unique index**:

```sql
CREATE UNIQUE INDEX uniq__stripe_products_external_id
    ON _stripe_products (external_id)
    WHERE external_id IS NOT NULL;
```

NULL-значения не конфликтуют → строки, созданные через admin UI
(без `external_id`), не ограничены. Embedder, который штампует свой
ID, получает идемпотентный upsert по этому ключу:

```go
tx.Exec(ctx, `
    INSERT INTO _stripe_products (name, external_id, metadata)
    VALUES ($1, $2, $3)
    ON CONFLICT (external_id) WHERE external_id IS NOT NULL
    DO UPDATE SET name = EXCLUDED.name, metadata = EXCLUDED.metadata,
                  updated_at = now()`,
    prod.Name, prod.ShopProductID, prod.Metadata)
```

Без этого ON CONFLICT не имел `WHERE external_id IS NOT NULL` индекса
и каждый update продукта плодил дубликат строки в `_stripe_products`.
FEEDBACK #23.

## Архитектура (`internal/stripe`)

```
config.go   — Config + чтение/запись stripe.* в _settings; Mode() из префикса ключа
stripe.go   — модели + Store (CRUD по шести таблицам)
client.go   — тонкая обёртка над stripe-go SDK (единственный файл, знающий про SDK)
service.go  — бизнес-логика: push каталога, checkout, подписки, ensureCustomer
webhook.go  — верификация подписи + проекция события на зеркальные таблицы
```

`Service` строит свежий SDK-клиент из DB-конфига **на каждый вызов** (дёшево, без сети) — правки кредов в UI действуют сразу.

**Push каталога — best-effort.** Локальная строка пишется первой; провал пуша в Stripe (плохой ключ, outage) логируется, но не фатален — строка остаётся "не запушенной". Только явный `POST /stripe/push-catalog` падает громко.

## API

### Admin (`/api/_admin/stripe/*`, за `RequireAdmin`)

| Метод + путь | Назначение |
|---|---|
| `GET/PUT /stripe/config` | статус кредов (редактированный) / сохранение |
| `GET/POST /stripe/products`, `PATCH/DELETE …/{id}` | каталог: продукты |
| `GET/POST /stripe/prices`, `POST …/{id}/archive\|restore` | каталог: цены (в Stripe immutable — только archive) |
| `POST /stripe/push-catalog` | реконсиляция незапушенного каталога вверх |
| `GET /stripe/customers\|subscriptions\|payments\|events` | read-only браузеры зеркала |
| `POST /stripe/subscriptions/{id}/cancel` | немедленная отмена |

### Публичный / app-facing (`/api/stripe/*`)

| Метод + путь | Auth | Назначение |
|---|---|---|
| `POST /api/stripe/webhook` | нет (проверка подписи) | Stripe → Railbase; идемпотентно |
| `GET /api/stripe/config` | нет | publishable key + mode для инициализации Stripe.js |
| `POST /api/stripe/payment-intents` | принципал | разовая продажа: `price_id` (каталог) **или** `amount`+`currency` (ad-hoc); опциональный `metadata: {…}` (FEEDBACK #4) |
| `POST /api/stripe/subscriptions` | принципал | подписка на recurring-цену |

### PaymentIntent metadata passthrough

`POST /api/stripe/payment-intents` принимает опциональное поле
`metadata` (`map[string]string`) — embedder протаскивает свой
`order_id` / `cart_id` через Stripe и обратно в webhook'е:

```json
POST /api/stripe/payment-intents
{
  "price_id": "<railbase price uuid>",
  "email": "alice@example.com",
  "metadata": { "order_id": "<embedder uuid>" }
}
```

Серверная валидация по Stripe-лимитам: 50 ключей, ключи до 40
символов, значения до 500 символов. Ключи `railbase_kind` /
`railbase_price_id` зарезервированы — Railbase их всегда выставляет
сам (audit / correlation не обсуждается), любые такие caller-entries
перезаписываются.

`CreateCatalogPaymentWithOptions` / `CreateAdhocPaymentWithOptions` —
Go-сторона, в `internal/stripe/service.go::CheckoutOptions`. Старые
`CreateCatalogPayment` / `CreateAdhocPayment` forward'ятся с пустым
options для backward compat.

Webhook и config — намеренно без аутентификации (Stripe не носит токен Railbase; publishable key и так публичен). Два checkout-эндпоинта требуют аутентифицированного принципала — они создают реальные списания.

## Поток checkout (Embedded Elements)

1. Фронт читает `GET /api/stripe/config` → инициализирует Stripe.js с publishable key.
2. `POST /api/stripe/payment-intents` или `/subscriptions` → бэкенд создаёт PaymentIntent / Subscription (`payment_behavior=default_incomplete`) и возвращает `client_secret`.
3. Фронт подтверждает платёж через Stripe Elements по `client_secret` — карта не касается Railbase.
4. Stripe шлёт `payment_intent.succeeded` / `customer.subscription.*` на webhook → зеркальные таблицы обновляются.

## Генерируемый TS SDK

`railbase generate sdk` эмитит `stripe.ts` — schema-независимый модуль (эндпоинты Stripe фиксированы, не выводятся из `CollectionSpec`). Downstream Vite-приложение получает типизированный клиент из коробки:

```ts
const rb = createRailbaseClient({ baseURL, token });
const { publishable_key } = await rb.stripe.config();
const { client_secret } = await rb.stripe.createPaymentIntent({ price_id, email });
// → client_secret передаётся в Stripe.js Elements для подтверждения
const sub = await rb.stripe.createSubscription({ price_id, email });
```

Эмиттер — `internal/sdkgen/ts/stripe.go` (`EmitStripe()`), по образцу `auth.go`, но без аргументов. Webhook в SDK намеренно отсутствует — он server-to-server.

## Webhook-обработчик

`POST /api/stripe/webhook`: верификация по `stripe.webhook_secret` → запись в `_stripe_events` (дубль = no-op) → диспатч по типу:

- `payment_intent.*` → обновление статуса в `_stripe_payments`
- `customer.created|updated` → upsert `_stripe_customers`
- `customer.subscription.*` → проекция в `_stripe_subscriptions`
- `product.*` / `price.*` / `invoice.*` — **не** синкаются вниз: каталог — source of truth в БД

Провал верификации → `400`. Провал диспатча → `200` + ошибка записывается в строку события (видно в admin UI как "failed"), чтобы Stripe не ретраил вечно непреходящий баг проекции.

## Admin UI

`Settings → Stripe` (`/settings/stripe`) — один экран с табами:

- **Configuration** — форма кредов (keep-if-empty секреты), статус, адрес webhook-эндпоинта.
- **Catalog** — продукты + цены, создание/архивация, кнопка **Push catalog to Stripe**.
- **Customers / Subscriptions / Payments / Webhook events** — read-only браузеры зеркала; отмена подписки.

## SDK

`github.com/stripe/stripe-go/v82`. API-версия фиксируется SDK. Webhook-подпись — `webhook.ConstructEvent`.

## Не входит в этот milestone

- Stripe Customer Portal (отмена/смена карты вынесена в свой UI вместо портала).
- Customer ↔ Railbase-user привязка: `email` берётся из тела запроса, не из принципала.
- Outbox-паттерн для исходящих вызовов (push сейчас синхронный, best-effort).
- Налоги (Stripe Tax), купоны, trial-периоды, проративка при смене плана.
- Sync *вниз* (Stripe → каталог) — каталог намеренно односторонний.
