# 04 — Identity & access: users, auth flows, OAuth providers, OTP/MFA, devices, tokens, RBAC, tenant enforcement

## Identity model — как разделены пользователи

PB смешивал system-admins и application-users; это путало. Railbase разделяет их явно.

### Три типа идентичности

#### 1. System admins (superusers)

Администраторы инсталляции Railbase. Доступ к admin UI, schema, миграциям, plugins, audit.

- **Таблица**: `_admins` (префикс `_` = system table; в более ранних драфтах документации называлась `_system_admins` — фактическое имя в миграциях v0.5+ короче)
- Создаются:
  - через bootstrap wizard (`POST /api/_admin/_bootstrap` — первый админ)
  - через CLI: `railbase admin create <email>` (любой последующий)
- **Не** видны через REST API, **не** имеют tenant-context, **не** subject of RBAC application-rules
- 2FA рекомендуется с v1.1 (опционально для v1)
- В PB это `_superusers` collection (раньше `_admins`)
- v1.7.43 — **welcome email обязателен** при создании администратора. Bootstrap-handler + CLI оба отказываются работать пока не настроен mailer (или операторски не помечен как «настрою позже» через мастер). См. docs/14-observability.md «Setup wizard safety model» для детальной модели.

#### 2. Application users

Конечные пользователи приложения, построенного на Railbase.

- **Таблица**: `users` (часть user-defined schema, может быть кастомизирована через `.Field()`)
- **Multiple auth collections** — может быть несколько pools (`users`, `sellers`, `members`, etc.)
- Управляются через `auth/*` модули
- Subject of RBAC, могут быть members organizations (с `railbase-orgs` plugin)
- Все auth-providers применяются здесь

#### 3. Service accounts / API tokens

Машинные идентичности.

- **Таблица**: `_api_tokens`
- Принадлежат либо system admin, либо application user, либо организации (с plugin)
- Scope-ограниченные (action keys), expiration, rotation
- Не имеют пароля, не делают signin — только bearer auth

### System tables

```
_admins                — Railbase-managed (Railbase-instance operators; v1.7.43 welcome emails fire on insert)
_admin_sessions        — Railbase-managed (admin UI session storage)
_api_tokens            — Railbase-managed
_sessions              — Railbase-managed, opaque tokens (hashed)
_devices               — Railbase-managed, device trust (port из rail)
_origins               — Railbase-managed, auth origin tracking
_external_auth         — Railbase-managed, multiple OAuth providers per user
_otps                  — Railbase-managed, one-time passwords
_mfa_challenges        — Railbase-managed, MFA state machine
_record_tokens         — Railbase-managed, signed tokens (verification, reset, file access)
_audit_log             — Railbase-managed, append-only
_migrations            — Railbase-managed, migration history
_settings              — Railbase-managed, runtime-mutable settings
_logs                  — Railbase-managed, application logs
_railbase_meta         — Railbase-managed, версия, init state, schema hash

users                  — user-controlled через schema.AuthCollection("users")
                          Railbase даёт базовые поля (id, email, password_hash, verified, etc.)
                          Пользователь может extend через .Field()
```

Префикс `_` зарезервирован за Railbase. Пользовательская коллекция с именем на `_` — ошибка валидации schema.

---

## Multiple auth collections

PB поддерживает несколько auth-коллекций (например `users` + `sellers` + `admins` как разные signin pools). Railbase повторяет:

```go
var Users = schema.AuthCollection("users").
    Field("name", schema.Text()).
    AuthMethods(schema.Password(), schema.OIDC("google"), schema.OAuth("github"))

var Sellers = schema.AuthCollection("sellers").
    Field("company", schema.Text()).
    AuthMethods(schema.Password())

var Admins = schema.AuthCollection("admins").
    Field("department", schema.Text()).
    AuthMethods(schema.Password(), schema.SAML("okta"))
```

Каждая auth-коллекция имеет:

- Свой signin endpoint (`/api/collections/{name}/auth-with-password`, `/auth-with-oauth2`, `/auth-with-otp`)
- Свой sessions namespace (`_sessions.collection_name`)
- Свои rules для emails (одинаковый email может существовать в `users` и `sellers` — разные сущности)
- Опционально свой mailer template set
- Свои RBAC roles

System admins (`_admins`) — отдельная сущность, не auth-коллекция. См. таблицу ниже.

### System vs Domain accounts — quick reference

| Слой | Имя таблицы | Кто заводит | Доступ | Email-уведомления (v1.7.43) |
|---|---|---|---|---|
| **System** (Railbase-instance operators) | `_admins` | bootstrap wizard (1-й) / `railbase admin create` CLI / admin UI (v1.x+) | Argon2id + `_admin_sessions`; виден только через `/api/_admin/*` | **Обязательны** на каждую creation: welcome новому + broadcast notice всем существующим (compromise detection) |
| **App domain** (бизнес-юзеры) | `users`, `customers`, `employees`, любое имя без `_` | разработчик через Schema DSL (`schema.AuthCollection("users")`) | REST `/api/collections/{name}/*` + RBAC + tenant scope | `signup_verification.md` при self-signup (v1.1); welcome для admin-created users — 📋 v1.x candidate |
| **Service accounts** | `_api_tokens` | `railbase token create` + admin UI v1.7.9 | Bearer-only, нет signin path | Нет — машинные идентичности; одноразовый display-once token banner на create |

Дандер-префикс (`_*`) **зарезервирован** за системой — validator схемы (`internal/schema/builder/validate.go`) отказывает создавать user-collection с именем, начинающимся на `_`. Это значит app-коллекция `_users` физически невозможна, и соответствующий namespace-конфликт между «railbase-managed» и «app-managed» таблицами исключён by construction.

---

## Auth flows

Rail's auth — самая зрелая часть, и Railbase следует ей буквально.

### Сессии

`internal/auth/session/`:

- Opaque tokens (32-byte URL-safe, base64url-encoded)
- Stored hashed (SHA-256 + HMAC ключ из `_railbase_meta.secret_key`)
- httpOnly cookie + bearer header support
- Refresh-on-use sliding window (default 8h, configurable)
- Concurrent sessions allowed; revoke individually или all
- `POST /auth-refresh` extends session

### Password auth

- Argon2id (`golang.org/x/crypto/argon2`): m=64MB, t=3, p=4
- Migration shim для bcrypt rehash on next login (для PB import)
- Rate limit per email (configurable, default 5/15min)
- Password policies: min length, require special chars, etc. (configurable per auth collection)

### Email verification

- При signup → token в `_record_tokens` (purpose=`verify`)
- Email с link `https://app/verify?token=...`
- Click → POST `/auth-with-password/verify-confirm` → mark `verified=true`
- Optional `--auth-require-verified` блокирует unverified логин

### Password reset

- POST `/auth-with-password/request-password-reset` → token в `_record_tokens` (purpose=`reset`)
- Email с link
- POST `/auth-with-password/confirm-password-reset` → token verify + new password

### Email change

- Authenticated user → POST `/auth-with-password/request-email-change` → token (purpose=`email-change`)
- Email на NEW address с confirmation link
- Click → POST `/auth-with-password/confirm-email-change` → email updated, **all sessions revoked** (security)

### OAuth2

- Per-provider config в settings (client_id/secret, redirect_url, scopes)
- Flow: `GET /auth-with-oauth2/{provider}` → redirect to provider
- Callback: `GET /auth-with-oauth2/{provider}/callback?code=...` → token exchange → user provisioning
- **External auth linking**: existing user может connect multiple providers (`_external_auth` table)
- New user provisioning: configurable per-provider (auto-create vs require existing user)

### OIDC

- Generic OIDC через `coreos/go-oidc/v3`
- Per-provider config: discovery URL, client_id/secret, scopes, claim mappings
- Same flow как OAuth2

### OTP (magic links / SMS / email codes)

PB feature `core/otp_model.go`. Passwordless auth.

- POST `/auth-with-otp/request` с email/phone → server генерирует 6-digit OTP, store в `_otps` с TTL (default 5 min)
- Channel: email (always) / SMS (через provider plugin) / authenticator app
- POST `/auth-with-otp/confirm` с code → session
- Rate limit: 3 requests / 15 min per identifier

**Magic links** = OTP с link delivery: `https://app/auth-magic?token=...`. Click = single-step signin.

**Phone-based OTP** требует `tel` field в auth collection (см. [03-data-layer.md](03-data-layer.md#tel-field)) — гарантирует E.164 storage и mobile-only validation для SMS delivery:

```go
var Users = schema.AuthCollection("users").
    Field("phone", schema.Tel().MobileOnly().RequireValid()).
    AuthMethods(schema.Password(), schema.OTP(schema.OTPViaSMS))
```

### WebAuthn / Passkeys

- `go-webauthn/webauthn` library
- Registration: authenticated user adds passkey
- Signin: passwordless через passkey
- Multiple passkeys per user
- Stored в `_external_auth` (provider=`webauthn`)

### 2FA TOTP

- `pquerna/otp` library
- User enables 2FA → server генерирует secret + QR code (provisioning URI)
- Recovery codes (8 кодов, hashed) — для случая потери authenticator
- Login flow: password verify → 2FA challenge → session
- Optional `require-2fa` enforce per-collection или per-role

### MFA model (multi-step challenges)

PB feature `core/mfa_model.go`. State machine для multi-factor:

```
1. POST /auth-with-password   → если 2FA enabled → returns mfaId + remaining factors
2. POST /auth-with-otp        → solve email OTP factor → returns mfaId + remaining
3. POST /auth-with-totp       → solve TOTP factor → all done → session
```

`_mfa_challenges` table: `id, user_id, factors_required, factors_solved, expires_at`. Каждый factor invalidates после use.

Configurable required factors per role: e.g. `system_admin` requires password + TOTP + email OTP.

---

## OAuth providers (35+ shipped)

Прямой порт PB's `tools/auth/`:

| Provider | File | Notes |
|---|---|---|
| Apple | `apple.go` + `forms/apple_client_secret_create.go` | JWT-style client_secret rotation |
| Bitbucket | `bitbucket.go` | |
| Box | `box.go` | |
| Discord | `discord.go` | |
| Facebook | `facebook.go` | |
| Gitea | `gitea.go` | self-hosted git |
| Gitee | `gitee.go` | |
| GitHub | `github.go` | |
| GitLab | `gitlab.go` | self-hosted variant via custom URL |
| Google | `google.go` | |
| Instagram | `instagram.go` | |
| Kakao | `kakao.go` | |
| Lark | `lark.go` | |
| Linear | `linear.go` | |
| LiveChat | `livechat.go` | |
| Mailcow | `mailcow.go` | |
| Microsoft | `microsoft.go` | Azure AD, Outlook |
| Monday | `monday.go` | |
| Notion | `notion.go` | |
| OIDC (generic) | `oidc.go` | configurable |
| Patreon | `patreon.go` | |
| Planning Center | `planningcenter.go` | |
| Spotify | `spotify.go` | |
| Strava | `strava.go` | |
| Trakt | `trakt.go` | |
| Twitch | `twitch.go` | |
| Twitter / X | `twitter.go` | |
| VK | `vk.go` | |
| Wakatime | `wakatime.go` | |
| Yandex | `yandex.go` | |

`internal/auth/oauth/<provider>.go` для каждого. Common base через `internal/auth/oauth/base.go`.

### Apple quirks

Apple Sign-In требует JWT-style client_secret (генерируется из private key + key_id + team_id). Railbase auto-generates через CLI:

```bash
railbase auth apple-secret \
  --team-id <id> \
  --key-id <id> \
  --private-key apple_key.p8 \
  --client-id com.example.app
```

Stored в settings; auto-rotation каждые 6 месяцев (Apple max 6 months validity).

### Auth methods endpoint

```
GET /api/collections/{name}/auth-methods
→ {
  "password": { "enabled": true, "identityFields": ["email", "username"] },
  "oauth2": [
    { "name": "google", "displayName": "Google", "state": "..." },
    { "name": "github", "displayName": "GitHub", "state": "..." }
  ],
  "otp": { "enabled": true, "duration": 300 },
  "mfa": { "enabled": true, "duration": 1800 }
}
```

Клиент использует для динамического UI (показать кнопки только для enabled providers).

---

## External auth linking

PB feature `core/external_auth_model.go`. Один user может иметь несколько OAuth providers connected.

```
_external_auth
  id | user_id | collection_id | provider | provider_user_id | created_at
```

Используется:

- При OAuth signin: lookup existing link → match → existing user (не дубликат)
- Authenticated user может connect/disconnect providers через UI
- Account merging: если user signs up email A, потом OAuth с email A — auto-link или prompt

### Endpoints

```
GET    /api/collections/{name}/records/{id}/external-auths      # list connected
DELETE /api/collections/{name}/records/{id}/external-auths/{provider}
POST   /api/collections/{name}/auth-with-oauth2                  # signin OR link existing
```

---

## Devices & auth origins

Rail's device trust pattern + PB's auth origin model.

### Devices (`_devices`)

```
id | user_id | collection_id | fingerprint | name | trusted | trust_expires_at | created_at | last_seen_at
```

`fingerprint` = hash(user_agent + IP subnet + screen resolution + ...). Stable across sessions.

- Login from new device → email notification («new device sign-in»)
- Optional require step-up MFA для new device
- Trust 30 days (configurable); expired → re-prompt
- User может revoke device → all sessions on device → forced logout

### Auth origins (`_origins`)

PB feature `core/auth_origin_model.go`. Tracks WHERE user authenticated (IP, geo, time):

```
id | user_id | collection_id | ip | user_agent | country | city | created_at
```

Использование:
- Display в admin UI и user profile («recent activity»)
- Anomaly detection: signin from unusual country → email alert
- Compliance: audit trail по IP

---

## Tokens

Несколько типов signed tokens с разными purposes. **Все отдельно от sessions**.

### 1. Session tokens (`_sessions`)

Long-lived, sliding window, hashed storage. См. выше.

### 2. API tokens (`_api_tokens`)

Long-lived, scoped, manual revoke.

```go
{
  id, name, hashed_token, owner_id, owner_type, // user или admin или service
  scopes []string,        // action keys subset
  expires_at,
  last_used_at,
  created_at,
}
```

Display once at creation («Save this — you won't see it again»).

### 3. Record tokens (`_record_tokens`)

PB feature `core/record_tokens.go`. Short-lived, single-use signed tokens для specific operations.

```go
type RecordToken struct {
  Purpose string  // "verify" | "reset" | "email-change" | "file-access" | "magic-link"
  RecordID string
  CollectionID string
  ExpiresAt time.Time
  UsedAt *time.Time  // single-use
  Payload map[string]any
}
```

Используются для:

- Email verification links
- Password reset links
- Email change confirmation
- File access (signed URLs)
- Magic links

JWT-encoded (HS256), signed `_railbase_meta.secret_key`. Verified server-side, marked used после consumption.

### 4. CSRF tokens

Per-session CSRF tokens для form-based mutations (admin UI). Auto-injected.

---

## RBAC — site + tenant scope в core (REVISED)

> **Архитектурный пересмотр**: ранее per-tenant RBAC планировался в plugin `railbase-orgs`. После анализа — это слишком базовая capability для plugin-only. Multi-tenant Slack/Linear-style проекты должны работать из коробки. **Per-tenant RBAC теперь в core**, но **organizations entity (с invites/seats/billing/ownership transfer) остаётся в plugin**.

### Гибрид: hardcoded base + table extensions

**Hardcoded в Go (compile-time, immutable)**:

- Системные роли: `system_admin` (всё могут), `system_readonly` (audit/health/metrics, ничего не пишут)
- Action keys catalog — generated из codegen (`internal/rbac/actionkeys/keys.go`):
  ```go
  const (
      ActionPostsList   ActionKey = "posts.list"
      ActionPostsCreate ActionKey = "posts.create"
      ActionAuthSignin  ActionKey = "auth.signin"
      ActionAdminBackup ActionKey = "admin.backup"
      // ...
  )
  ```
  Это даёт IDE-completion, refactor-safety, compile-time проверки. PB-style строки тоже доступны для JS-rules, но Go-код использует константы.

**В БД (runtime, mutable через admin UI)**:

- Кастомные роли (созданные пользователем): таблица `_roles`
- Связи user→role: таблица `_user_roles` с tenant_id (NULL = site role; non-NULL = tenant role)
- Per-role action grants: таблица `_role_actions(role_id, action_key)`

### Scope в DSL

```go
schema.Role("admin").Scope(schema.SiteScope).Grants(
    rbac.ActionAdminBackup,
    rbac.ActionAdminUsersManage,
)

schema.Role("workspace_admin").Scope(schema.TenantScope).Grants(
    rbac.ActionPostsCreate,
    rbac.ActionMembersInvite,
)

schema.Role("editor").Scope(schema.TenantScope).Grants(
    rbac.ActionPostsCreate,
    rbac.ActionPostsUpdate,
)
```

User может иметь:
- Site role: одна, глобальная (admin / user / guest / custom)
- Tenant roles: разные в разных tenants (Bob — `workspace_admin` в Acme, `editor` в Beta)

### Минимальный seed

При первом старте Railbase создаёт минимальный набор:

```
Site roles:    system_admin, admin, user, guest
Tenant roles:  owner, admin, member, viewer

Grants:
  system_admin → все system action keys (bypass)
  admin        → все application action keys
  user         → CRUD на own records (через @me filter)
  guest        → read on public collections

  owner        → все tenant action keys (per-tenant bypass)
  admin        → most tenant actions кроме delete tenant
  member       → CRUD на own records в tenant
  viewer       → read only
```

Пользователь может полностью переопределить через UI или DSL.

### Где RBAC живёт в потоке запроса

```
HTTP request
  → middleware.Auth          (resolve actor: user / api_token / admin)
  → middleware.LoadTenant    (resolve current tenant context, если есть)
  → middleware.LoadActions   (загрузить action keys для actor: site role + tenant role intersection;
                              кеш per-request)
  → handler
      → rbac.Require(ctx, ActionPostsCreate)   // 1 indexed query или cache hit
      → ... business logic ...
```

Кэш в context: `loadActions` запускается один раз за request, результат живёт в `context.Value`.

### Ownership pattern

Common rule «user может read/update только own records» — built-in helpers:

```go
schema.ListRule(rbac.OwnsField("author"))   // WHERE author = @me
schema.UpdateRule(rbac.OwnsRecord())        // implicit: WHERE author = @me OR via group membership
```

Эти helpers компилируются в filter expressions, добавляются к queries автоматически.

### Что в plugin `railbase-orgs`

Plugin focuses on **organization as domain entity** (не RBAC capability):

- `organizations` table (name, slug, settings, billing context)
- `organization_members` table (user_id, org_id, role_id, status, invited_by)
- Invite lifecycle (pending → accepted/expired/revoked) с email links
- Seat counting
- Ownership transfer
- Member management UI
- 38-ролевой каталог из rail (Owner, GL Accountant, CFO, Sales Director, Treasury Manager, etc.) **как seed-template** — `railbase init --template saas-erp` подгружает; не плагин-обязаловка
- Optional integration с `railbase-billing` для seat-based subscriptions

См. [15-plugins.md](15-plugins.md#railbase-orgs).

---

## Tenant enforcement: PostgreSQL Row-Level Security как основа

Tenant leakage — catastrophic vulnerability. Runtime middleware («не забыл ли я добавить WHERE tenant_id = ?») недостаточно. Railbase использует **RLS policies в БД** как ground truth + дополнительные application-layer слои как defense in depth.

### Подход (4 слоя)

1. **RLS policies на каждой tenant-таблице** (ground truth) — `CREATE POLICY` с `USING (tenant_id = current_setting('railbase.tenant')::uuid)` и `WITH CHECK` тем же предикатом. `ENABLE ROW LEVEL SECURITY` + `FORCE ROW LEVEL SECURITY` (даже table owner проходит через policies). См. [03-data-layer.md](03-data-layer.md#multi-tenancy-postgresql-row-level-security).

2. **Per-request session vars** — middleware устанавливает `SET LOCAL railbase.tenant = ...`, `railbase.user = ...`, `railbase.role = ...` на acquired connection. Settings tx-scoped, очищаются при release в pool.

3. **Filter expression auto-injection** (defense in depth) — native mode auto-добавляет `tenant_id = @tenant` в любой query на tenant-collection, чтобы query plan был оптимальным (RLS добавляет ту же проверку, но explicit filter помогает planner'у выбрать tenant-prefixed index).

4. **Sanity check после read** — `record.tenant_id == ctx.tenant_id` после CRUD read; mismatch → panic в dev, audit + 500 в prod (это «не должно случиться никогда» — если случилось, RLS broken).

### `WithSiteScope` — explicit, audited bypass

Единственный путь сделать cross-tenant query — переключить role на `app_admin`, что требует:

```go
err := db.WithSiteScope(ctx, "reason: admin tooling — backup", func(ctx context.Context) error {
    // Внутри: SET LOCAL railbase.role = 'app_admin'
    // RLS policies для admin role либо отсутствуют, либо `USING (true)`
    return adminTask(ctx)
})
```

Каждое использование пишется в audit с reason, stack trace, actor. Сам `WithSiteScope` доступен только из `internal/admin/...` пакетов; CI-gate блокирует import из request-handler кода.

### Multi-tenancy strategy

**Default: row-level isolation через RLS** с обязательным `tenant_id UUID NOT NULL REFERENCES tenants(id)`:

- Single migration applies to all tenants instantly
- Trivial to back up (один dump покрывает всех tenants)
- Warm buffer pool packing
- RLS гарантирует isolation на DB level

**Escape hatch**: `schema.Isolated(schema.SchemaPerTenant)` — tenant получает свою Postgres-схему (`tenant_<id>.posts`, `tenant_<id>.users`, ...). Query routing — через `SET search_path = tenant_<id>, public` per-request. Migrations применяются `FOR EACH tenant` (slower deploys). Trade-off: операционная сложность, но полный physical isolation для compliance кейсов (HIPAA, regulated finance).

### Tenant context propagation

```go
ctx = tenant.With(ctx, "acme-corp-id")
// Все queries автоматически фильтруются
posts.List(ctx)   // WHERE tenant_id = 'acme-corp-id' AND ...

// Cross-tenant — explicit
ctx = tenant.WithSiteScope(ctx, "admin-action")  // audit reason required
allPosts.List(ctx)   // no filter, but audited
```
