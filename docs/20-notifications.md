# 20 — Notifications system

PB не имеет first-class notifications; rail имеет `notifications` module — Railbase портирует.

Не путать с realtime: realtime — transport. Notifications — **unified entity** «информировать пользователя о событии», доставляется через **множественные channels** (in-app, email, push) с user preferences.

## Концепция

```go
schema.Notification("payment_approved").
    Channels(schema.InApp(), schema.Email("payment_approved.md"), schema.Push()).
    Audience(schema.User("requester_id")).
    OnEvent("authority.approved", schema.WhereResource("payments"))
```

Каждое доставленное уведомление = row в `_notifications` (audit + read/unread state).

## Channels

- **in-app** — запись в БД + realtime push на `users.{id}.notifications` topic
- **email** — через core mailer (markdown templates, i18n)
- **push** — через `railbase-push` plugin (FCM / APNs)

Пользовательские preferences в `_notification_preferences(user_id, kind, channel, enabled)` — UI в profile settings.

## Tables

```
_notifications                       — id, user_id, kind, title, body, data JSONB,
                                        read_at, created_at, expires_at
_notification_preferences            — user_id, kind, channel, enabled
_notification_templates              — kind, channel, template, locale (overrides)
```

## REST endpoints

```
GET    /api/notifications?unread=true&limit=50
POST   /api/notifications/{id}/read
POST   /api/notifications/mark-all-read
DELETE /api/notifications/{id}
GET    /api/notifications/preferences
PATCH  /api/notifications/preferences
```

## JS hooks

```js
$notifications.send({
  user: "user_id",
  kind: "payment_approved",
  data: { paymentId: "...", amount: 1000 },
})

// Subscribe для UI
pb.collection("notifications").subscribe("@me", (e) => { ... })

// Hooks для customization
onNotificationBeforeSend((e) => {
  // например, suppress в quiet hours
  if (isQuietHours(e.user)) e.cancel()
})
```

## Templates

Same engine что и mailer — markdown с frontmatter:

```markdown
---
title: "Payment approved"
priority: high
---
Your payment {{data.paymentId}} for {{data.amount | money}} has been approved.

[View payment]({{site.url}}/payments/{{data.paymentId}})
```

Per-channel variants: `payment_approved.email.md`, `payment_approved.push.md`, `payment_approved.inapp.md`. Default — single template используется для всех channels.

## Per-tenant + i18n overrides

Те же rules что и mailer (см. [09-mailer.md](09-mailer.md#per-tenant-overrides-multi-tenant)):

1. `tenants/{tenant_id}/notifications/{kind}.{lang}.md`
2. `pb_data/notifications/{kind}.{lang}.md`
3. Embedded default

## Admin UI

Notifications log + per-kind defaults editor + per-user delivery history. См. [12-admin-ui.md](12-admin-ui.md).

## Realtime delivery

Каждое создание notification publish'ится на `users.{id}.notifications` topic. Frontend SDK имеет `useNotifications()` hook (helper в docs).

## Quiet hours

Per-user setting в preferences: `{ start: "22:00", end: "08:00", timezone: "Europe/Moscow" }`. Notifications помечаются с priority; non-urgent buffer'ятся до конца quiet hours; urgent (security alerts) bypass.

## Что НЕ делает

- Сложный notification routing (Slack/Teams/SMS) — через `railbase-push` plugin extension
- Aggregation / digests («5 new comments» вместо 5 отдельных) — пользователь делает через cron
- Read receipts от других сторон — out of scope
