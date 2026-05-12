# 21 — Outbound webhooks

Стандарт для SaaS-интеграций (Zapier, n8n, Make, custom backends). PB не имеет; Railbase делает first-class в core.

Не путать с inbound webhooks (Stripe webhooks → Railbase) — те описаны в `railbase-billing` plugin. Здесь — **outbound**: Railbase shipит events наружу.

## Конфигурация

Через admin UI или config file:

```yaml
webhooks:
  - name: zapier_payment
    url: https://hooks.zapier.com/...
    events: ["record.created.payments", "record.updated.payments"]
    secret: env:ZAPIER_WEBHOOK_SECRET
    retry:
      attempts: 5
      backoff: exponential               # initial 1s, ×2 каждый retry
      max_delay: 1h
    timeout: 30s
    filter: "amount.amount > '100'"     # optional condition
    headers:                              # optional custom headers
      X-Custom: value
```

## Подписка на events

Events sourcing — internal eventbus topics:
- `record.created.{collection}` / `record.updated.{collection}` / `record.deleted.{collection}`
- `auth.signin` / `auth.signup` / `auth.signout`
- `document.uploaded` / `document.archived`
- `authority.approved` / `authority.rejected` (с plugin)
- Custom events через `$webhooks.dispatch(name, payload)` в JS hooks

## Wire format

```http
POST https://hooks.zapier.com/... HTTP/1.1
Content-Type: application/json
X-Railbase-Event: record.created.payments
X-Railbase-Webhook: zapier_payment
X-Railbase-Delivery: 01HQ...                    (UUID v7 для idempotency)
X-Railbase-Signature: t=1700000000,v1=abc123... (HMAC-SHA256)
X-Railbase-Tenant: tenant_id                    (если multi-tenant)
User-Agent: Railbase-Webhook/1.0

{ "event": "record.created.payments", "data": {...record...}, "ts": "2026-05-09T..." }
```

## HMAC signature

```
t = current unix timestamp
signed_payload = t + "." + raw_body
signature = HMAC_SHA256(secret, signed_payload)
header = "t=" + t + ",v1=" + hex(signature)
```

Receiver verifies:
1. Parse `t` and `v1` из header
2. Reject если `now - t > 5 min` (replay protection window)
3. Compute expected signature; compare via constant-time

## Retry policy

- Exponential backoff: `1s → 2s → 4s → 8s → ... → max_delay`
- Max attempts (configurable, default 5)
- Считается успешным: HTTP 2xx
- Считается retry: HTTP 5xx, network errors, timeout
- Считается dead: HTTP 4xx (кроме 408, 429) — клиент явно отверг
- Dead-letter queue в `_webhook_dead_letters` table; admin UI manual replay

## Tables

```
_webhooks                   — конфигурация (name, url, events, secret, retry policy)
_webhook_deliveries         — каждая попытка delivery (status, response code, retry count, ts)
_webhook_dead_letters       — failed после max attempts; manual replay
```

## JS hooks API

```js
// Programmatic dispatch (custom event)
$webhooks.dispatch("custom.event", payload)

// Get delivery status
const deliveries = $webhooks.deliveries({ name: "zapier_payment", since: "-1h" })

// Manual retry dead letter
$webhooks.retry(deliveryId)

// Hook на event (для customization)
onWebhookBeforeDispatch((e) => {
  // Mutate payload, add headers, cancel
  e.payload.extra = "..."
})

onWebhookAfterDelivery((e) => {
  // Log custom analytics
})
```

## REST endpoints (admin)

```
GET    /api/webhooks                    — list configured
POST   /api/webhooks                    — create
PATCH  /api/webhooks/{id}
DELETE /api/webhooks/{id}
POST   /api/webhooks/{id}/test          — send test ping
GET    /api/webhooks/{id}/deliveries    — recent attempts
POST   /api/webhooks/dead-letters/{id}/retry
```

## Admin UI screen — Webhooks

- Webhook subscriptions list с status (active / paused / failing)
- Per-webhook delivery history (timeline view: success/retry/dead)
- Filter event types selector
- Test ping button (отправляет dummy payload)
- Dead-letter queue with bulk replay
- Stats: success rate, p95 latency, total deliveries last 24h
- Code samples generator для receiver-side verification (Node.js, Python, Go, PHP)

## Per-tenant webhooks

Multi-tenant: каждый tenant может настраивать свои webhooks независимо. Tenant admin видит только свои; system admin видит all + cross-tenant aggregations.

## Rate limiting

- Per-webhook rate limit (default 100/sec) — защита от runaway loops
- Global outbound rate limit (default 1000/sec) — защита от resource exhaustion

## Security

- HMAC signing — клиент **обязан** verify
- Replay protection через timestamp window
- TLS verification (configurable: strict / skip-self-signed для dev)
- Outbound URL validation: deny private IP ranges (anti-SSRF) кроме `localhost` в dev mode
- Audit row на каждое delivery attempt

## Что НЕ делает

- Inbound webhooks parsing (Stripe / GitHub) — это в plugins (billing, custom)
- Webhook transformation pipelines — leave to пользователю или Zapier/Make
- Retry storms detection (exponential growth) — basic limits, advanced нужно external monitoring

## Open questions

- **Per-event vs single-event filter**: фильтр на event types сразу + filter expression на payload — два разных уровня. Сейчас оба в config; UX правильный?
- **Webhook chains** (output one webhook → input другого) — out of scope, через external orchestrator
- **Async delivery acknowledgement**: receiver может не успеть обработать в timeout window. Async mode (200 immediately + later confirm) — сложно, leave для v2.
