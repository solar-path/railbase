# 09 — Mailer: providers, templates, flows, i18n

PB имеет mailer с custom templates (signup verification, password reset). Railbase расширяет.

## Mailer providers

- **SMTP** (default) — `net/smtp` stdlib, поддерживает TLS, STARTTLS, auth
- **SES** — через AWS SDK (opt-in, в core)
- **Postmark** — REST API adapter (plugin `railbase-postmark`)
- **Sendgrid** — REST API adapter (plugin `railbase-sendgrid`)
- **Mailgun** — REST API adapter (plugin `railbase-mailgun`)
- **Console** (dev) — печатает email в stdout вместо отправки

```yaml
mailer:
  driver: smtp                  # smtp | ses | postmark | sendgrid | mailgun | console
  smtp:
    host: smtp.example.com
    port: 587
    username: env:SMTP_USER
    password: env:SMTP_PASS
    from: noreply@example.com
    tls: starttls
  rate_limit: 100/min            # global mailer rate limit
```

## Templates

Markdown с frontmatter (тот же engine как PDF templates):

```markdown
---
subject: "Welcome to {{site.name}}"
from: "{{site.from}}"
reply_to: "support@{{site.domain}}"
---

Hi {{user.name}},

Welcome! Please verify your email by clicking:

[Verify Email]({{verify_url}})

This link expires in 24 hours.
```

Templates в `pb_data/email_templates/` (PB-compat) или `railbase_data/email/templates/`. Hot-reload через fsnotify.

### Available variables

```
site.{name, url, from, domain}        — site settings
user.{id, email, name, ...}            — recipient (если auth context)
verify_url, reset_url, ...             — flow-specific links
data.*                                  — custom data passed via $mailer.send({data: ...})
```

### Render output

Output: HTML body + auto-generated text fallback (для email clients без HTML).

## Built-in flows

Pre-configured templates для common flows (override-able):

- `signup_verification.md` — email verification после signup
- `password_reset.md` — reset link
- `email_change.md` — confirm new email
- `2fa_recovery.md` — recovery codes shipping
- `invite.md` — organization invite (с `railbase-orgs` plugin)
- `magic_link.md` — passwordless signin (если enabled)
- `otp.md` — one-time code email
- `new_device.md` — new device sign-in alert
- `unusual_activity.md` — anomaly alerts

Override через создание файла с тем же именем в `pb_data/email_templates/`. Иначе используется embedded default.

## Custom emails из hooks

```js
onRecordAfterCreate("orders", (e) => {
  $mailer.send({
    template: "order_confirmation.md",
    to: e.record.customer.email,
    data: { order: e.record, customer: e.record.customer },
  })
})
```

```js
$mailer.send({
  to: ["a@example.com", "b@example.com"],
  subject: "Direct subject",
  html: "<p>Direct HTML body</p>",
  text: "Plain text fallback",
  attachments: [
    { filename: "report.pdf", content: pdfBytes },
    { filename: "data.xlsx", content: xlsxBytes },
  ],
})
```

## Per-tenant overrides (multi-tenant)

С `railbase-orgs` plugin: каждый tenant может override templates (branding, language).

Resolution order:
1. `tenants/{tenant_id}/templates/{name}.md` (per-tenant override)
2. `pb_data/email_templates/{name}.md` (site-wide custom)
3. Embedded default

## Internationalization

Templates могут быть `signup_verification.en.md`, `signup_verification.ru.md`, etc.

Resolver выбирает по:
1. `user.language` (если задан)
2. `Accept-Language` header
3. `site.default_language` (по умолчанию `en`)

Dev fallback на `.en.md` если specific language missing.

### Per-tenant + i18n

Order:
1. `tenants/{tenant_id}/templates/{name}.{lang}.md`
2. `tenants/{tenant_id}/templates/{name}.en.md`
3. `pb_data/email_templates/{name}.{lang}.md`
4. `pb_data/email_templates/{name}.en.md`
5. Embedded default

## Bounce / delivery tracking

Не в core. С плагинами `railbase-postmark`/`railbase-sendgrid`/etc.:

- Webhooks для bounce/open/click events
- Stored в `_email_events` table
- Visible в admin UI Mailer history
- Triggers eventbus `email.bounced`, `email.opened`, etc.

## Mailer hooks (PB-compat)

```js
onMailerBeforeRecordVerificationSend((e) => {
  // Modify email перед send
  e.message.subject = "Welcome to " + $app.settings().site.name
  e.next()
})

onMailerBeforeRecordPasswordResetSend((e) => {...})
onMailerBeforeRecordEmailChangeSend((e) => {...})
onMailerBeforeRecordOTPSend((e) => {...})
```

## Rate limiting

- Global mailer rate limit (configurable)
- Per-recipient rate limit (anti-spam): max 5 emails / hour / address
- Per-tenant quotas (multi-tenant): subscription-tied

## Test send

CLI:
```
railbase mailer test --to me@example.com --template signup_verification.md
```

Admin UI: «Send test» button per template.

## Open questions

- **DKIM/SPF/DMARC docs**: помогать пользователю настраивать DNS records, или only docs?
- **Bulk send / newsletter mode**: отдельный flow для рассылок (с unsubscribe links, list management)? Или leave to пользователю / external services (Mailchimp/etc.)?
- **Inline images / CID embedding**: для richer emails? Standard `<img src="...">` через signed URLs работает.

## Что НЕ делаем

- Newsletter / list management — leave to пользователю
- Email open tracking pixel — privacy concern; opt-in через provider
- Click tracking — same
- Spam filtering / inbound mail — out of scope
