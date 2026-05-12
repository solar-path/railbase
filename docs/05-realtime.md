# 05 — Realtime & subscriptions

Realtime — главная killer-фича PocketBase. Railbase делает её мощнее, не теряя простоту.

## Subscription targets — что можно слушать

```js
// 1. Все события коллекции
pb.collection("posts").subscribe("*", (e) => { ... })

// 2. Конкретная запись
pb.collection("posts").subscribe("RECORD_ID", (e) => { ... })

// 3. Несколько записей (extension over PB)
pb.collection("posts").subscribe(["id1", "id2"], (e) => { ... })

// 4. Фильтр (native-mode extension; в PB compat — нет filter)
pb.collection("posts").subscribe("*", (e) => {...}, {
  filter: "status = 'published' && author = @auth.id",
  expand: ["author", "tags"],
})

// 5. Per-user inbox (auth-aware shorthand)
pb.collection("notifications").subscribe("@me", (e) => { ... })

// 6. Custom topic (через hooks)
$app.realtime().publish("orders.shipped", payload)
pb.realtime.subscribe("orders.shipped", (e) => { ... })
```

## Event envelope

```ts
type RealtimeEvent<T> = {
  action: "create" | "update" | "delete"
  record: T                      // typed record (type-gen из коллекции)
  expand?: { [relation: string]: any }  // если subscribe был с expand
  topic: string                  // полное имя топика
  ts: string                     // ISO8601 timestamp
  event_id: string               // monotonic; для resume tokens
}
```

## Транспорт

### WebSocket primary

- `coder/websocket` library
- Endpoints: `/api/realtime` (strict-mode, PB-shape) или `/v1/realtime` (native)
- Bi-directional: subscribe/unsubscribe через WS messages
- Auto-reconnect с exponential backoff в SDK

### SSE fallback

- Для environments без WS support (corporate proxies)
- Same broker subscription
- Subscribe через `POST /v1/realtime/subscribe`, events stream через GET с EventSource

### Resume tokens (extension over PB)

PB events lost on reconnect. Railbase improves:

- На каждое event сервер выдаёт monotonic `event_id`
- Server keeps last N events per topic в memory (default 1000, configurable; ~5 min retention в типичной нагрузке)
- Reconnect: client шлёт `last_event_id` → server replays missed events
- Window expired (event evicted) → client получает `replay_lost` flag, должен resync

```ts
const sub = rb.collections.posts.subscribe("*", (e) => {...}, {
  resumeFrom: lastEventId,    // или auto через SDK persistence
})
sub.onReplayLost(() => fullResync())
```

## RBAC & безопасность

### Subscribe-time check

При попытке subscribe вызывается `ListRule` коллекции с actor context. Если deny → connection refused, audit-row.

### Per-event filter pass

При каждом publish сервер фильтрует по `ListRule` с подставленным record. `@request.auth.id` резолвится в actor подписки. Незахваченные denies pass через filter — событие не доставляется.

### Filter rules для subscribe

Строгое ограничение: subscriber видит только то, что мог бы прочитать через REST list. Никаких leaks через realtime.

### Expand на realtime

Те же rules, что и REST: relation-fetch проходит RBAC check для каждой expanded коллекции.

### Critical правило

**Subscriber не должен видеть данные, которые через REST list ему не доступны.** Это инвариант, проверяется в integration tests.

## Архитектура брокера

```
Hook / DB write → eventbus.Publish(RecordCreated{...})
                       ↓
            realtime.Broker.Receive
                       ↓
       per-subscriber filter pass (ListRule + filter expr)
                       ↓
       per-subscriber expand resolution (lazy, cached per-event)
                       ↓
        deliver через WS connection / SSE stream
```

### Single-node: LocalBroker (default) + Postgres LISTEN/NOTIFY

- Channels + sharded topic map в-памяти
- Per-subscription filter goroutine
- In-memory event window для resume tokens
- **Cross-process fan-out через Postgres LISTEN/NOTIFY** — даже если Railbase запущен в нескольких репликах за load balancer на single Postgres, события доходят до всех инстансов: после tx commit, AFTER trigger делает `pg_notify('railbase_events', payload::text)`; dedicated listener connection в каждой реплике принимает и diffuses в local subscribers
- 0 external dependencies (Postgres уже есть)
- Limit: payload < 8000 bytes (Postgres NOTIFY limit) — для больших событий публикуется ID + ленивая дозагрузка через `findRecord`

### Cluster: NATSBroker (plugin `railbase-cluster`)

Когда LISTEN/NOTIFY перестаёт хватать (десятки реплик, миллионы events/sec, нужен JetStream для persistent realtime):

- Events bridge'атся в NATS subject через `eventbus → nats publish`
- Каждая instance держит свои WS-connections
- NATSBroker forwards events которые match'ат локальным subscriptions
- Embedded NATS server (`nats-io/nats-server/v2`) — peer discovery через env `RAILBASE_CLUSTER_PEERS`
- JetStream off by default (events ephemeral); включается через flag для persistent realtime

**Decision tree**:
- 1-3 реплики, < 50k events/sec → LocalBroker + LISTEN/NOTIFY (built-in)
- 5+ реплик, persistent guarantees, cross-region → NATS plugin

## Performance & limits

- **Default**: 10k concurrent subscriptions per instance (memory-bound; configurable)
- **Filter expression compiled & cached** per subscription (zero per-event compile cost)
- **Expand-cache per event**: если 100 subscribers ждут `expand=author`, сервер делает 1 query на author, не 100
- **Backpressure**: если подписчик отстаёт > 1 MB queued — drop connection, audit `realtime.backpressure_disconnect`
- **Per-tenant quotas** (multi-tenant): max subscriptions/tenant; max events/sec/tenant
- **Resume window**: default 1000 events per topic (configurable; tune by traffic)

## Hooks API для realtime

```js
// Custom topics (не привязанные к коллекции)
$app.realtime().publish("system.alert", { level: "warn", message: "..." })

// В onRecordAfterCreate можно публиковать кастомные события
onRecordAfterCreate("orders", (e) => {
  if (e.record.status === "shipped") {
    $app.realtime().publish(`orders.${e.record.id}.shipped`, e.record)
    $app.realtime().publish(`users.${e.record.customer}.notifications`, {
      type: "order_shipped",
      order: e.record.id,
    })
  }
})

// Inspect / kick subscribers (admin)
$app.realtime().subscribers("posts.*").forEach(s => {
  if (s.actor === "abuse_user") s.kick("rate_limit_exceeded")
})
```

## Отличия от PB

| Аспект | PocketBase | Railbase |
|---|---|---|
| Subscribe target | `*`, recordId | `*`, recordId, array, filter expression, `@me` |
| Expand | нет | да (с RBAC check) |
| Custom topics | нет | `$app.realtime().publish(topic, ...)` |
| Resume tokens | нет (теряются on reconnect) | да (1000-event window) |
| Cluster mode | single-node only | NATS broker через plugin |
| Per-tenant quotas | нет | да (multi-tenant) |
| Backpressure | в зависимости от Go-runtime | explicit drop с audit |
| Transport | SSE only | WS primary + SSE fallback |

PB-compat mode `strict` оставляет первые две колонки PB-shape; native добавляет filter/expand/`@me`/resume/WS.

## Topic naming conventions

```
collections.{name}.*                    — collection-level (PB shape)
collections.{name}.{record_id}          — record-level (PB shape)
auth.users.{user_id}                    — per-user channel (для notifications)
system.alert                            — custom (через hooks)
orders.{order_id}.shipped               — custom domain event
```

Native mode: dotted hierarchy, NATS-compatible (subjects).

## SDK API

```ts
import { createRailbaseClient } from "./client"

const rb = createRailbaseClient({ baseURL: "..." })

// Single subscription
const unsub = rb.collections.posts.subscribe("*", (event) => {
  console.log(event.action, event.record)
}, {
  filter: "status='published'",
  expand: ["author"],
  resumeFrom: lastEventId,
})

// Cleanup
unsub()

// Custom topic
const unsub2 = rb.realtime.subscribe("orders.shipped", (event) => {...})

// Connection state
rb.realtime.onConnect(() => {...})
rb.realtime.onDisconnect((reason) => {...})
rb.realtime.onReplayLost(() => fullResync())
```

## Testing realtime

См. [17-verification.md](17-verification.md):

- WS-клиент подписывается на `collections.posts.*`, второй клиент создаёт запись — событие приходит за < 100ms
- Resume after reconnect: client disconnect 3 sec, reconnect → пропущенные events доставляются
- Backpressure: медленный subscriber > 1MB queue → drop с audit
- RBAC leak test: user без `posts.list` permission не получает events о posts
- Cluster test: 2 instances + NATS plugin, instance1 publishes → instance2 delivers
