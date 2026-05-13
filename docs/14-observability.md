# 14 — Observability & operations

Объединяет: configuration, runtime settings, errors, logging, audit, telemetry, lifecycle, backup, rate limiting.

## Configuration

### Sources (precedence: ниже = выше priority)

1. Built-in defaults
2. `.env` file (`./.env` then `<DataDir>/.env`, see v1.7.45 below)
3. Config file `railbase.yaml`
4. Environment variables (`RAILBASE_*`)
5. CLI flags (`--db-url`, etc.)
6. **Admin UI overrides** (для runtime-изменяемых; пишутся в `_settings` table)

#### `.env` file (v1.7.45)

Railbase reads `.env` BEFORE anything else, so operators don't have to
pass `RAILBASE_HTTP_ADDR=:8095 ./railbase serve` on every shell line.
Two locations consulted, in order:

1. `./.env` — alongside the binary / working directory
2. `<DataDir>/.env` — alongside `pb_data/`, useful when one host runs
   multiple Railbase instances from a shared binary

Format is the standard one:

```dotenv
# Comments tolerated
RAILBASE_HTTP_ADDR=:8095            # bare values
RAILBASE_DSN="postgres://u:p@h/db"  # double-quoted: \n \r \t \\ \"
RAILBASE_SECRET='literal \value'    # single-quoted: literal, no escapes
export RAILBASE_PROD=true           # `export ` prefix tolerated
```

**Precedence**: process env wins over `.env`. You can shadow a stored
DSN with `RAILBASE_DSN=... ./railbase serve` for a one-off run without
editing the file. CLI flags win over both.

The repo ships `.env.example` as a starter template with every known
`RAILBASE_*` key commented out — `cp .env.example .env` is the
operator workflow. `.env` itself is gitignored.

### Config file пример

```yaml
db:
  dsn: postgres://railbase:secret@localhost:5432/railbase?sslmode=disable
  pool:
    max_conns: 16
    min_conns: 1
    max_conn_lifetime: 1h
  embed_postgres: false               # true → spawn embedded PG subprocess (dev only)

auth:
  oidc:
    google:
      client_id: env:GOOGLE_OIDC_CLIENT_ID
      client_secret: env:GOOGLE_OIDC_CLIENT_SECRET

realtime:
  broker: local                     # or nats (требует railbase-cluster plugin)
  resume_window: 1000
  max_subscriptions: 10000

storage:
  driver: fs
  root: pb_data/storage

mailer:
  driver: smtp
  smtp:
    host: smtp.example.com
    port: 587
```

### Secrets

- `env:VAR_NAME` syntax — config резолвит из environment
- File-based secrets (`/run/secrets/...`) для Docker/K8s через `file:/path` syntax
- Optional integration с `direnv`, `1password-cli` через CLI команды (`railbase config set` спрашивает интерактивно)

### Bootstrap secrets

При первом `railbase init`:

- Генерируется `RAILBASE_SECRET_KEY` (32 байта) — для подписи cookies, JWT, hash-chain seal
- Сохраняется в `pb_data/.secret` (chmod 0600)
- Backup-warning при первом старте: «эта keys необходима для расшифровки sessions; backup её отдельно от данных»

---

## Settings (runtime-mutable)

PB feature `core/settings_model.go`. Settings хранятся в БД и могут меняться через admin UI без рестарта.

### `_settings` table

```
id (singleton row)
data JSONB                           — все settings serialized
updated_at, updated_by
```

### Sections

```jsonc
{
  "site": { "name": "...", "url": "...", "logo": "..." },
  "auth": {
    "passwordPolicy": { "minLength": 8 },
    "lockout": { "maxAttempts": 5, "windowMinutes": 15 },
    "oauth2": { "google": {...}, "github": {...} }
  },
  "mailer": {...},
  "storage": {...},
  "realtime": {...},
  "rate_limits": {...},
  "audit": { "retention": "1y", "sealing": false },
  "tenant": {...}
}
```

### Reload semantics

- Изменение setting через UI → eventbus `settings.changed` event
- Subscribers (auth, mailer, storage, и т.д.) подписаны → reload свой config
- Heavy changes (DB driver, port) — требуют рестарта; UI предупреждает

### Encryption sensitive values

OAuth secrets, SMTP passwords и т.д. encrypted (AES-256) с key из `_railbase_meta.secret_key` перед stored.

---

## Error handling

### Typed errors с error codes

```go
package errors

type Code string
const (
    CodeNotFound       Code = "not_found"
    CodeUnauthorized   Code = "unauthorized"
    CodeForbidden      Code = "forbidden"
    CodeValidation     Code = "validation"
    CodeConflict       Code = "conflict"
    CodeRateLimit      Code = "rate_limit"
    CodeInternal       Code = "internal"
    // ...
)

type Error struct {
    Code    Code
    Message string                  // human-readable, safe to expose
    Details map[string]any          // structured (e.g. validation field errors)
    Cause   error                   // unwrap-чейн, не exposed клиенту
}
```

### Wire format

```json
{
  "error": {
    "code": "validation",
    "message": "title is required",
    "details": { "field": "title", "rule": "required" }
  }
}
```

### HTTP mapping

Каждый code → fixed HTTP status. Никогда не угадываем по сообщению.

| Code | HTTP |
|---|---|
| not_found | 404 |
| unauthorized | 401 |
| forbidden | 403 |
| validation | 400 |
| conflict | 409 |
| rate_limit | 429 |
| internal | 500 |

### SDK

TypeScript SDK генерит discriminated union (см. [11-frontend-sdk.md](11-frontend-sdk.md#error-handling)).

### JS hooks

```js
throw new BadRequestError("...")  // PB-compat
throw new RailbaseError({ code: "validation", details: {...} })  // native
```

---

## Logging — три параллельных канала

### 1. Application logs (slog)

Что: операционные события для отладки.
Куда: stdout (JSON в prod, text в dev) + `_logs` table (PB-style, queryable через admin UI).
Уровни: debug/info/warn/error. Default `info`.
Формат: `{ time, level, msg, trace_id, span_id, request_id, actor_id, tenant_id, ... }`
Trace context: каждый log line carries `trace_id` если внутри trace.

#### Logs as records (PB feature)

`internal/logs/` имплементирует PB pattern: structured logs хранятся в `_logs` table:

```
_logs(id, level, message, data JSON, created_at, request_id, trace_id, ...)
```

Используется для:
- Logs viewer в admin UI с filtering
- Retention policy (default 30 days, configurable)
- Export для analysis

Не заменяет stdout slog — оба работают параллельно.

### 2. Audit log (БД)

Что: security/compliance события — кто, что, когда, с каким результатом.
Куда: `_audit_log` table, append-only, hash-chain optional.

Что **всегда** логируется:

- Все аутентифицированные mutations (success + failure)
- RBAC denies (`rbac.deny`)
- Auth events: signin, signup, signout, password change, 2FA enable/disable, device add, impersonation start/stop
- System admin actions: create admin, plugin install, schema migration applied
- Configuration changes (через admin UI)
- API token issue/revoke
- Document upload/version/archive/access (granular в `_document_access_log`)
- Authority decisions (с plugin)

Что **не** логируется (политика):

- Read-операции application users (слишком noise; включается флагом `--audit-reads`)
- Hook execution успехи (только failures)
- Telemetry samples

#### Hash chain (opt-in `--audit-seal`)

Каждая запись:
```
hash = sha256(prev_hash || canonical_json(row_minus_hash))
```

Sealer-job раз в сутки проверяет цепочку и подписывает хвост Ed25519-ключом. CLI: `railbase audit verify`.

#### Critical правило (из rail)

Audit пишется через **bare pool**, не через request-tx. Иначе rollback бизнес-транзакции стирает запись о денае.

### 3. Telemetry (OpenTelemetry)

Что: traces + metrics для performance.
Куда: OTLP/HTTP endpoint (env `OTEL_EXPORTER_OTLP_ENDPOINT`); Prometheus `/metrics`.
Auto-instrumented: HTTP requests, DB queries, hook executions, realtime subscriptions, jobs.

Custom metrics:
- `railbase_request_duration_seconds{route, method, status}`
- `railbase_db_query_duration_seconds{dialect, operation}`
- `railbase_hook_duration_seconds{collection, event, outcome}`
- `railbase_realtime_subscriptions_active`
- `railbase_jobs_pending`, `railbase_jobs_failed_total`
- `railbase_documents_storage_bytes{tenant}`
- `railbase_mailer_sent_total{provider, outcome}`

### Correlation

Единый `request_id` (UUID, генерируется в middleware) проходит через все три канала. Audit-row carries `request_id`; OTel spans carry; slog logs carry. Поиск «что произошло в этом запросе» — один grep.

### Что redacted

Никогда не логируется:
- Plain passwords (даже на debug)
- Bearer tokens (логгируются только prefix `tok_abc...`)
- 2FA codes
- Webhook signing secrets
- Любое поле с tag `rb:"secret"` в schema DSL

Redaction layer в slog handler — explicit allowlist, deny-by-default для unknown fields.

---

## Bootstrap & lifecycle

### First run flow

```
$ railbase serve                       # без init, без env — zero-config v1.4.3
  1. Load config (no .secret? → auto-create in dev, refuse in prod)
  2. Open DB:
     a. RAILBASE_DSN set     → use it
     b. <DataDir>/.dsn exists → use persisted (v1.7.39 setup wizard saved it)
     c. embed_pg build tag    → spin up embedded PG (dev convenience)
     d. otherwise             → enter setup mode (only /api/_admin/_setup/* mounted)
  3. Foreign-DB invariant (v1.7.42): if `public` non-empty AND no `_migrations`
     marker → fail with ErrForeignDatabase (RAILBASE_FORCE_INIT=1 overrides).
     Catches manual-`.dsn`-edit route bypassing the wizard.
  4. Run system migrations if first run (_admins, _audit_log, etc.)
  5. Discover user schema (Go DSL registry); diff with applied migrations
  6. If no admin → admin UI `/_/` shows bootstrap wizard (DB setup → admin)
  7. Start hook runtime pool, fsnotify watcher
  8. Mount HTTP server on configured port
  9. Print:
       Admin UI: http://localhost:8095/_/
       API:      http://localhost:8095/api/

$ railbase init demo                   # opt-in scaffold for code-first workflow
  1. Create directory structure (pb_data/, pb_hooks/, schema/)
  2. Generate .secret (32-byte key)
  3. Write railbase.yaml (defaults: port 8095 — IANA-unassigned, no default
     daemon on Linux/Windows/macOS, no collision with PB :8090; DB
     configured via setup wizard
     on first `serve` OR via RAILBASE_DSN env var)
  4. Write schema/main.go (helloworld: одна коллекция `posts`)
  5. Write pb_hooks/example.pb.js (одна hook, demonstration)
```

### Setup wizard safety model (v1.7.42)

The first-run setup wizard guards against two failure modes — neither is data-loss, both are schema pollution:

| Failure mode | First layer (wizard probe) | Second layer (boot invariant) |
|---|---|---|
| Operator typos DB name → hits a foreign app's DB | `is_existing_railbase=false && public_table_count>0` → amber warning + Save locked behind checkbox | `internal/db/migrate.checkForeignDatabase` returns `ErrForeignDatabase` before `bootstrap()` writes the `_migrations` marker |
| Operator hand-edits `<DataDir>/.dsn` to point at a foreign DB | (bypasses wizard entirely) | Same `internal/db/migrate` check catches it on next boot |

**No destructive operations.** The setup endpoints (`probe-db`, `save-db`) never execute `DROP DATABASE`, `DROP TABLE`, or `TRUNCATE`. The `Create the database if it doesn't exist` checkbox only fires `CREATE DATABASE <name>` against the `postgres` admin DB IF the target doesn't already exist — never against an existing one (checkbox is silently ignored in that case). Probe is read-only (`SELECT version()` + one `pg_tables` count).

**Escape hatch.** Legitimate co-location scenarios (Railbase alongside another app sharing a logical DB) are unblocked by `RAILBASE_FORCE_INIT=1` env var (boot side) + UI checkbox «I understand — install Railbase alongside the existing tables.» (wizard side). Env var is intentionally not a CLI flag — operator should feel the friction.

**What the marker is.** The `_migrations` table created by the first successful migration run doubles as the "this DB belongs to Railbase" fingerprint. Adding a separate `_railbase_instance` table with a UUID `instance_id` for distinguishing multiple Railbase installs against the same DB is a v1.x candidate when backup/restore + cross-instance scenarios appear; not blocking SHIP.

### Mandatory email on admin creation (v1.7.43)

Bootstrap wizard now has THREE steps: Database → **Mailer** → Admin account. The mailer step is required before any admin can be created.

**Server-side gate** (`internal/api/adminapi/bootstrap.go::mailerGateError`): `POST /api/_admin/_bootstrap` returns **412 Precondition Failed** if neither `mailer.configured_at` nor `mailer.setup_skipped_at` is set in `_settings`. The CLI `railbase admin create` bypasses the 412 gate on purpose (operator surface), but still honours the `mailer.setup_skipped_at` flag — if set, no welcome enqueues; otherwise a welcome lands like the handler path. The `--no-email` flag (CLI-only) lets operators explicitly suppress per-invocation when they're moving admin records around in scripts.

**On every successful admin creation** (provided mailer NOT skipped):

| Email | Template | Recipient | Purpose |
|---|---|---|---|
| Welcome | `admin_welcome.md` | the new admin | Login URL + onboarding (2FA, audit-log review) |
| Broadcast notice | `admin_created_notice.md` | every OTHER admin | Compromise detection. Empty fan-out on bootstrap (first admin); N-1 emails on subsequent creates. |

**Delivery durability**:

- Both emails ride `send_email_async` (v1.7.30) — exp-backoff retry 30s → 1h ceiling, MaxAttempts=24 (much higher than the 5 standard so welcomes survive ~24h of SMTP downtime via the standard retry layer alone)
- Failed sends after MaxAttempts → `_jobs` row with `status='failed'` + `last_error`, AND `_email_events` row with the outcome
- **`retry_failed_welcome_emails` cron** (every 30 min) picks up failed welcome rows older than 15 min and younger than 7 days, flips them back to `pending` so the next worker poll re-attempts. Welcome emails that landed in failed state hours before the operator fixed SMTP eventually arrive. **Welcome-only** — password_reset / signup_verification / other templates are NOT swept (stale content + likely-expired links).

**Skip path** (`POST /api/_admin/_setup/mailer-skip` with non-empty reason): stamps `mailer.setup_skipped_at` (timestamp) + `mailer.setup_skipped_reason` (free text) in `_settings`. Audit-event `settings.changed` fires automatically. With the flag set:

- bootstrap-handler + CLI admin-create both proceed without enqueueing any emails
- Mailer step renders an amber notice on return-visits ("Mailer skipped on YYYY-MM-DD, reason: ...")
- Re-saving a valid mailer config CLEARS the skip flag automatically — operator-intent has reversed

**Why mandatory by default**:

1. **Compromise detection** — broadcast notice goes to every existing admin when a new admin is added. If you didn't authorise it, you find out immediately rather than via after-the-fact audit-log review.
2. **Operational visibility** — multi-operator teams. "Alice created admin Bob" is visible to all admins, not just whoever pulls audit log.
3. **Welcome content** — new admin gets login URL + getting-started ссылки + 2FA reminder. Audit log doesn't deliver that.
4. **Industry parity** — Auth0 / Cognito / Supabase / Keycloak all send admin-creation emails by default.

**Endpoints**:

```
GET  /api/_admin/_setup/mailer-status — current configured/skipped state + masked snapshot
POST /api/_admin/_setup/mailer-probe  — test SMTP/console with a probe recipient
POST /api/_admin/_setup/mailer-save   — persist driver + SMTP creds + from_address to _settings
POST /api/_admin/_setup/mailer-skip   — record "I'll configure later" with mandatory reason
```

All four are PUBLIC (no RequireAdmin) by design — operator can't be admin until DB + mailer are resolved. Trust boundary is operator-grade access to the running process during cold-boot.

### Graceful shutdown

`SIGINT`/`SIGTERM`:

1. Stop accepting new HTTP requests (close listener)
2. Wait for inflight requests (timeout `SHUTDOWN_TIMEOUT`, default 30s)
3. Drain hook runtime pool (kill stuck runtimes after 5s)
4. Cancel scheduled jobs leases (workers re-claim после restart)
5. Flush audit-writer queue
6. Close DB pools
7. Close eventbus
8. Exit 0

### Health checks

- `/healthz` — всегда 200, just liveness
- `/readyz` — 200 если: DB reachable + migrations applied + admin exists + broker started; иначе 503

CLI alternative: `railbase health` для container probes без HTTP.

---

## Backup & restore

### Что backup'ится

Полный backup (`railbase backup --out file.tgz`):

- `pg_dump` (custom format `-Fc` для compression + parallel restore)
- `pb_data/storage/` (uploaded files и documents)
- `pb_data/.secret`
- `pb_hooks/`
- `railbase.yaml`
- Schema snapshot (`schema export --json`)
- Manifest файл с версией Railbase, schema hash, Postgres version, timestamp

### Restore

`railbase restore file.tgz` проверяет:

1. Версия Railbase compatible (semver match)
2. Schema hash в snapshot matches schema в коде (или fail с инструкцией миграции)
3. Атомарно swap'ает данные (rename old → backup-old, new → active)

`--force` — skip compat checks (advanced; для disaster recovery).

### Auto-upload to S3

PB feature `apis/backup_upload.go`. Configurable:

```yaml
backup:
  schedule: "0 3 * * *"
  retention_days: 30
  upload:
    driver: s3
    bucket: my-backups
    key: env:S3_KEY
    secret: env:S3_SECRET
```

Scheduled backup → upload → local copy retained per retention.

### Point-in-time recovery

Не в core (responsibility инфраструктуры). Подсказки в docs:
- Self-hosted Postgres: WAL archiving + `pg_basebackup`, или `pgBackRest` для production
- Managed Postgres (RDS / Cloud SQL / Supabase / Neon): провайдер делает PITR из коробки, обычно с retention windows 7-35 дней

---

## Rate limiting & quotas

### Per-IP (always on)

- `golang.org/x/time/rate` token bucket per IP, sharded
- Default: 60 req/sec, burst 120
- Configurable per-route в `railbase.yaml` или admin UI Settings

### Per-user (opt-in)

- Same library, key = `user.id`
- Configurable per-action key в RBAC: `rbac.Limit("posts.create", "10/min")`

### Per-tenant (opt-in, multi-tenant only)

- `railbase-orgs` plugin добавляет seat limits, storage quotas, request quotas tied to subscription plan

### Hook resource quotas

- Per-tenant CPU time budget для goja runtime (см. [06-hooks.md](06-hooks.md#hook-isolation-model))

### File upload limits

- Max size per request (default 32 MB, configurable)
- Allowed MIME types per collection field
- Optional virus scan integration

### Realtime quotas

- Max subscriptions per instance (default 10000)
- Max events/sec per tenant
- Backpressure threshold

### Mailer rate limits

- Global rate limit (configurable)
- Per-recipient rate limit (default 5 emails/hour/address — anti-spam)
- Per-tenant quotas (с orgs plugin)

---

## Caching layer

Critical для производительности; не было первоначально описано.

### In-memory sharded LRU

`internal/cache/` через `hashicorp/golang-lru/v2`:

- **Query result cache** — invalidated по collection writes; TTL configurable
- **RBAC permission lookups** — per-actor, TTL 60s, invalidated на role change
- **Compiled filter expressions** — keyed by expression string + schema hash
- **Schema metadata** — shared across requests
- **JSON schema renders для admin UI** — invalidated на schema migration
- **Resolved settings** — invalidated на settings.changed eventbus event

### HTTP response caching

Для read-heavy endpoints с `Cache-Control` headers:
- File downloads (immutable, hash-based URLs)
- Static admin assets (long-lived)
- `/api/health` (short TTL)

### Cache stampede protection

`singleflight` pattern: concurrent requests на same key — только один выполняет work, остальные ждут результат.

### Admin UI

Cache hit ratio per-cache, manual flush button, key inspector. См. [12-admin-ui.md](12-admin-ui.md).

### NOT external Redis в core

Добавление Redis ломает single-binary contract. Cluster-mode shared cache — через NATS KV в `railbase-cluster` plugin.

### Metrics

- `railbase_cache_hits_total{cache}`
- `railbase_cache_misses_total{cache}`
- `railbase_cache_evictions_total{cache, reason}`
- `railbase_cache_size_bytes{cache}`

---

## Encryption at rest

### Field-level

```go
schema.Field("ssn", schema.Text().Encrypted())
schema.Field("payment_token", schema.Text().Encrypted())
```

- AES-256-GCM
- Per-tenant DEK (data encryption key) derived from master key
- Master key через env (`RAILBASE_ENCRYPTION_KEY`) или KMS
- SDK дешифрует прозрачно для authorized actors
- Search на encrypted fields ограничен (нет partial match; equality OK через blind index)

### Storage-level (uploads / documents)

`--encrypt-storage` flag:
- Каждый file encrypted с unique DEK
- DEK хранится в file metadata, encrypted by master
- Origin bytes никогда не stored unencrypted

### Key rotation

```bash
railbase encryption rotate-key
```

- Re-encrypts metadata DEKs
- File bytes остаются (deferred re-encryption opt-in: `--rotate-bytes` runs background job)
- Old key retained для read-side access until full rotation completes
- Audit на каждое key rotation

### Postgres-level encryption strategy

- **Managed Postgres providers** (RDS, Cloud SQL, Supabase, Neon) предоставляют storage-level TDE автоматически
- **Self-hosted Postgres** — filesystem-level (LUKS, dm-crypt) на data volume, или PG 17+ TDE patches (когда стабилизируется)
- **Field-level через `pgcrypto`** (`pgp_sym_encrypt(...)`) для PII columns с `.Encrypted()` modifier — Railbase управляет ключами, hooks автоматически encrypt/decrypt
- **Audit log sealing** — Ed25519 signature сверху hash chain, никак не зависит от storage encryption

### Master key sources

- Env var: `RAILBASE_ENCRYPTION_KEY`
- File: `file:/run/secrets/encryption.key`
- KMS via `kms:` URL syntax:
  - `kms:vault:transit/keys/railbase` (HashiCorp Vault)
  - `kms:aws:arn:aws:kms:us-east-1:...:key/...` (AWS KMS)
  - `kms:gcp:projects/.../keys/...` (GCP KMS)

---

## API security extended

Базовые middleware (rate limit, CORS, body limit, gzip) в [02-architecture.md](02-architecture.md). Расширенно:

### CSRF tokens

Для cookie-based auth (admin UI, web apps):
- `GET /_/csrf-token` endpoint выдаёт token (session-bound)
- Double-submit pattern: cookie + header `X-CSRF-Token`
- State-changing requests (POST/PATCH/DELETE) с cookie-auth требуют header
- Bearer-auth (API tokens) обходит — это by-design (API не имеет cross-site context)

### Security headers (default)

- `Content-Security-Policy` strict для admin UI; configurable для public API
- `Strict-Transport-Security: max-age=31536000; includeSubDomains; preload`
- `X-Frame-Options: DENY`
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: strict-origin-when-cross-origin`
- `Permissions-Policy` для disabled browser features

### IP allowlist / denylist

Per-route или global через config или admin UI:

```yaml
ip_filter:
  - rule: allow
    cidrs: ["10.0.0.0/8", "192.168.0.0/16"]
  - rule: deny
    cidrs: ["1.2.3.0/24"]
  - rule: allow
    routes: ["/_/*"]
    cidrs: ["10.0.0.0/8"]                   # admin UI только из internal
```

### API key rotation

- Old key invalidated через N дней (configurable, default 30)
- Warning header `Deprecation: true; sunset=<date>` на use deprecated key
- New key issued before invalidation
- CLI `railbase auth rotate-token <id>`

### Anti-bot honeypots

На signup endpoints:
- Hidden field detection (если bot заполнил → reject)
- Optional CAPTCHA integration (hCaptcha / Turnstile / reCAPTCHA) через config
- Time-to-fill detection (< 1 sec → suspicious)

### Account lockout

Beyond rate limit:
- 10 failed signins за 1 hour → account locked на 30 min
- Audit + email user
- Configurable thresholds
- Manual unlock через admin UI

### Trusted proxy config

```
RAILBASE_TRUSTED_PROXIES=10.0.0.0/8,192.168.0.0/16
```

Для accurate `X-Forwarded-For` parsing. Без этого — IP spoofing risk (обход rate limit).

### Origin header validation

Для state-changing requests с cookie-auth: `Origin` matches site.url или allowed origins (CORS). Дополнительная защита в дополнение к CSRF token.

---

## Streaming responses (LLM-era)

PB не имеет; критично для AI use cases.

### HTTP streaming

Через `http.Flusher` для chunked responses. Работает для long-running operations (export, AI completions, log tail).

### SSE для streamed text

Token-level streaming для LLM completions:

```js
routerAdd("POST", "/ai/chat", async (c) => {
  const stream = $stream.sse(c)
  for await (const token of $ai.complete(prompt)) {
    stream.send(token)
  }
  stream.close()
})
```

### WebSocket bidirectional

Для interactive AI sessions (с `$stream.ws(c)` helper в hooks).

### Backpressure

- Client disconnect detection через context cancellation
- Server stops generating если context.Done()
- Important для AI cost control (не платить за tokens которые никто не получит)

### Use cases

- LLM completion streaming
- Long export progress (`POST /api/exports` → SSE prog updates)
- Live log tail (admin UI)
- Realtime collaboration (WS)

`--template ai` уже включает примеры этих паттернов.

---

## Update mechanism (для самой Railbase)

PB feature `pocketbase update`. Railbase портирует в core (отдельно от `railbase-ghupdate` plugin для admin UI).

### `railbase update` команда

```bash
railbase update                         # checks GitHub releases
railbase update --check                 # only check, no download
railbase update --version 1.2.5         # specific version
railbase update --force                 # skip compatibility checks
```

### Self-update safety

- Pre-update checks (DB compatibility, breaking changes)
- Breaking → abort с инструкцией manual upgrade
- Atomic swap (rename old → backup-old, new → active)
- Verify binary signature перед swap

### Rollback

- Previous binary в `pb_data/.railbase-prev`
- `railbase rollback` команда

### Staged updates в cluster

Через `railbase-cluster` plugin: rolling update one node at a time с health-check между nodes.

### Update notifications (admin UI)

«New version 1.2.5 available» banner для system admins. Через `railbase-ghupdate` plugin (см. [15-plugins.md](15-plugins.md)).

### Auto-update opt-in

```yaml
update:
  auto: patch                            # off | patch | minor (никогда не major)
  schedule: "weekly"
  channel: stable                         # stable | beta
```

Patch updates считаются безопасными (bug fixes, no breaking changes); minor — opt-in; major — всегда manual.

### Telemetry opt-in

Anonymous usage stats для maintainers (collections count, plugins installed, perf characteristics). Opt-in default off; включается через config:

```yaml
telemetry:
  enabled: false                         # default
  endpoint: https://telemetry.railbase.dev
```

Что собирается (если enabled):
- Railbase version
- OS, arch
- Plugins installed (names only)
- Collections count
- Active users count (rough buckets)
- Perf bottlenecks (anonymized)

Что **не** собирается:
- Никаких user data
- Никаких record content
- Никаких PII
- Ip address не отправляется

---

## Internal event bus

См. [02-architecture.md](02-architecture.md#inter-module-communication--три-механизма).

---

## API versioning

См. [02-architecture.md](02-architecture.md#api-versioning--evolution).

---

## Deployment

### Docker

```dockerfile
FROM scratch
COPY railbase /railbase
EXPOSE 8095
ENTRYPOINT ["/railbase", "serve"]
```

### Systemd

```ini
[Service]
Type=simple
ExecStart=/usr/local/bin/railbase serve --config /etc/railbase/config.yaml
Restart=on-failure
User=railbase
Group=railbase
LimitNOFILE=65536
```

### Kubernetes

- StatefulSet (с PV для pb_data)
- ConfigMap для config
- Secret для `.secret` и env vars
- Service + Ingress
- Liveness probe → `/healthz`
- Readiness probe → `/readyz`
- Horizontal scaling — managed Postgres (RDS / Cloud SQL / Supabase) + S3 storage; для очень крупных (десятки реплик) — `railbase-cluster` plugin (NATS broker) поверх

### Edge / single-VPS

- Single Railbase binary + Postgres process на той же VPS (managed `systemd` units), или container-pair через docker-compose
- Local FS storage (или S3 для объёмных uploads)
- Read replica — Postgres logical replication на second VPS если cluster нужен
- Cloudflare Tunnel / Tailscale Funnel для public access
- Auto-update через `railbase-ghupdate` plugin
