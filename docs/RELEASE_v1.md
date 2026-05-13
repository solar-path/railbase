# Railbase v1 — Release Notes

**Status**: SHIP-unblocked 2026-05-12. Tag invocation pending operator approval.
**Audience**: operators evaluating or upgrading to Railbase v1.

Quick links:
- Per-milestone deltas: [progress.md](../progress.md)
- v0 → v1 work breakdown: [plan.md](../plan.md)
- v1 SHIP gate audit: [docs/17-verification-audit.md](17-verification-audit.md)

---

## What is Railbase v1

A single static Go binary that runs as a production backend on PostgreSQL — PocketBase-class developer experience, with the architecture, observability, and feature surface to outgrow the hobby phase without re-platforming.

**One sentence**: `./railbase serve` → live REST + realtime + admin UI + typed SDK in under a minute.

### Stack (frozen for v1)

- **Runtime**: Go 1.26+, pure-Go (`CGO_ENABLED=0`), single static binary ≤30 MB stripped.
- **Database**: PostgreSQL 14/15/16 via `jackc/pgx/v5`. Required extensions: `pgcrypto`, `ltree`. Optional: `pg_trgm`, `btree_gist`, `pgvector` (when collections use them).
- **Embedded PG**: opt-in via `RAILBASE_EMBED_POSTGRES=true` (build tag `embed_pg`); zero-config dev DX. Production runs against managed Postgres (RDS / Cloud SQL / Supabase / Neon / self-hosted).
- **Frontend**: React 19 + Vite + Tailwind 4 admin UI embedded under `/_/`.
- **Hooks**: `dop251/goja` JavaScript runtime with fsnotify hot-reload, atomic-swap registries, per-handler watchdog.

### Platforms (binary release matrix)

6 cross-compiled targets — all under the 30 MB budget (measured 2026-05-12, host-independent ratio):

| OS      | arch  | Size    |
|---------|-------|---------|
| linux   | amd64 | 25.55 MB |
| linux   | arm64 | 23.88 MB |
| darwin  | amd64 | 26.18 MB |
| darwin  | arm64 | 24.64 MB |
| windows | amd64 | 26.26 MB |
| windows | arm64 | 24.23 MB |

Tarballs + checksum file produced by `goreleaser release --clean` (see `.goreleaser.yml`).

---

## Feature surface

### Functionally complete (✅ in v1)

| Track | What ships |
|---|---|
| **Data layer** | 31 field types (15 PB-paritet + 16 domain types); recursive-descent filter parser → parameterized SQL with `BETWEEN` / `IN` / `IS NULL` / `~` ILIKE / magic vars; sort with secondary `id` tie-breaker; pagination cursor stable; batch ops (atomic + 207 Multi-Status); soft-delete with admin Trash UI; tenant-scoped tables with RLS + 2.53% measured overhead. |
| **Identity** | Password auth (Argon2id), HMAC-signed opaque sessions, OAuth2 (Google/GitHub/Apple/generic OIDC), TOTP 2FA + MFA challenges, WebAuthn passkeys (hand-rolled CBOR + COSE + ES256), record-token flows (verify / reset / email-change / OTP / magic-link), API tokens (display-once, 30d rotation), RBAC (site + tenant scopes, lazy middleware). |
| **Realtime** | SSE + **WebSocket** transports (v1.7.34 — `coder/websocket`, `railbase.v1` subprotocol, PB-compat frame shape), `?topics=` with `*` wildcards, per-sub queue cap 64 with drop-oldest, **1000-event resume buffer** via `Last-Event-ID` (SSE) / `since` cursor (WS), dynamic subscribe/unsubscribe acks on WS, in-process eventbus + LISTEN/NOTIFY cross-replica bridge. 10k-subscriber fan-out: 2.3 ms p50 / 3.9 ms p99 (sub-linear scaling). |
| **Files** | Content-addressed FSDriver with atomic-rename writes; multipart upload streaming; MIME validator; signed download URLs (HMAC + TTL, constant-time compare). 50-goroutine concurrent upload: zero corruption (SHA256-verified, atomic-rename invariant). |
| **Jobs** | `_jobs` table with `SELECT … FOR UPDATE SKIP LOCKED` claim; hand-rolled 5-field cron; exp-backoff retries; **`ErrPermanent` sentinel** for non-retryable errors (v1.7.31); stuck-job recovery. 8 built-in handlers: cleanup_sessions / cleanup_record_tokens / cleanup_admin_sessions / cleanup_exports / cleanup_logs / cleanup_audit_archive / cleanup_tree_integrity / **scheduled_backup** (v1.7.31) / **audit_seal** (Ed25519 chain, v1.7.32) / **send_email_async** (v1.7.30) / **orphan_reaper** (v1.7.33). 5000+ jobs/sec sustained at 4 workers; zero double-execution under concurrency. 7-command jobs CLI + 7-command cron CLI. |
| **Mailer** | SMTP + console drivers; Markdown→HTML templates with frontmatter; 8 built-in templates; global + per-recipient rate limiting; **`mailer.before_send` + `mailer.after_send` eventbus topics** (v1.7.31 — subscribers can mutate `*Message` + set `Reject = true` to abort); `mailer test` CLI. |
| **Hooks** | 6 record events (Before/After × CUD); `$app.onRecord*` / `$app.realtime().publish()` / `$app.routerAdd()` / `$app.cronAdd()` bindings; <1s fsnotify hot-reload (150ms debounce); per-handler watchdog + **stack-cap 128** (v1.7.31 — partial mem-limit closure; goja has no true heap limit). **Hook concurrency**: 304k dispatches/sec under 100 goroutines, no deadlocks. |
| **Notifications** | In-app + email channels, partial unread-only index, per-user preferences, 7 REST endpoints with cross-user isolation. |
| **Webhooks** | HMAC-SHA-256 signing (`t=<unix>,v1=<hex>`); exp-backoff retries via the jobs framework; anti-SSRF URL validator (prod blocks loopback / RFC 1918 / RFC 4193); 7-cmd CLI; admin UI with dead-letter replay. |
| **i18n** | Catalog with 3-step lookup fallback, `{name}` interpolation, `Plural` helper, Accept-Language q-quality negotiation, `?lang=` override, RTL flag. Embedded `en.json` + `ru.json` + runtime overrides via `pb_data/i18n/`. Admin Translations editor with coverage tracking. |
| **Document generation** | XLSX (1M rows constant-memory streaming via `excelize.StreamWriter`); PDF (`signintech/gopdf` with embedded Roboto, pure-Go); Markdown → PDF; `.Export()` schema-declarative; async via jobs for >100k rows; 12-cmd CLI; JS hooks `$export.xlsx/.pdf/.pdfFromMarkdown`. |
| **CSV import** | `railbase import data <collection> --file <path.csv|.csv.gz>` via Postgres `COPY FROM STDIN` (50× faster than INSERT-per-row); header validation; gzip auto-detect. |
| **Backup/restore** | Pure-Go pgx COPY dump/restore; gzipped tar with manifest.json; SERIALIZABLE READ ONLY DEFERRABLE snapshot; restore via `session_replication_role = 'replica'`. CLI + admin UI. |
| **PB compatibility** | `strict` / `native` / `both` modes; `/auth-methods` discovery; `railbase import schema --from-pb <url>`; OpenAPI 3.1 spec generator. |
| **Security** | HSTS + X-Frame-Options + X-Content-Type-Options + Referrer-Policy default-on in production; CIDR-based IP allowlist (live-reloaded via settings bus); double-submit-cookie CSRF; three-axis rate limiter (per-IP / per-user / per-tenant); **anti-bot middleware** (v1.7.33 — honeypot form fields + UA sanity check on auth/oauth paths, production-gated); **audit hash-chain Ed25519 sealing** (v1.7.32 — daily `audit_seal` cron with per-seal inline public_key for rotation safety; `railbase audit seal-keygen` + `railbase audit verify`). |
| **Admin UI** | 17/20 listed screens (3 remaining are blocked-by-design: Documents browser depends on §3.6 Documents track / Hierarchical tree-DAG viz on §3.8 Hierarchies tail / Realtime collab indicators on v1.1+). Shipped: Dashboard, Schema, Records (bulk-ops + inline-edit + domain-aware cell renderers for **25/25 §3.8 types** v1.7.25-29), Settings, Audit (filters + browser), Logs, Jobs, API tokens, Backups, Notifications, Trash, Mailer templates, Realtime monitor, Webhooks, Hooks editor (Monaco), Translations editor, Command palette ⌘K, Cache inspector (filter.ast / rbac.resolver / i18n.bundles / settings registered v1.7.31-32), Health dashboard. |
| **CLI** | 13 top-level commands (admin / audit / auth / backup / config / cron / export / generate / import / init / jobs / mailer / migrate / serve / token / tenant). |
| **Observability** | slog → `_logs` persisted records (batched COPY); `_audit_log` hash-chained with `audit verify`; structured request_id middleware; 5-min smoke script (13 HTTP probes). |
| **Testing infrastructure** | `pkg/railbase/testapp` — `New(t, WithCollection(...))` boots PG + migrations + REST + auth in one call; `AsAnonymous` / `AsUser` actors; **JSON + YAML fixtures** via `LoadFixtures` (v1.7.30 — JSON-wins precedence); **`MockHookRuntime`** for fast JS hook unit tests without spinning up the full TestApp (v1.7.33); **`MockData` generator** auto-fakes 19 field types via gofakeit (v1.7.33); **`railbase test` CLI** wrapper over `go test` (v1.7.28). |

### Out of v1 (deferred)

| Item | Where it lands |
|---|---|
| Cluster broker (NATS), distributed realtime | v1.1 `railbase-cluster` plugin |
| Organizations / invites / seats | v1.1 `railbase-orgs` plugin |
| Billing (Stripe) | v1.1 `railbase-billing` plugin |
| FX rate conversion | v1.2 `railbase-fx` plugin |
| Hierarchies tail (closure-table / DAG / nested-set) | v1.x — adjacency_list + ordered_children + LTREE + tags ship in v1 |
| S3 / GCS storage drivers | v1.1 |
| OpenTelemetry / Prometheus | v1.1 |
| ~~Audit hash-chain Ed25519 sealing~~ | **✅ shipped v1.7.32** (`audit_seal` daily cron + per-seal inline public_key + CLI) |
| Encryption at rest (field-level + KMS) | v1.1 |
| Self-update (`railbase update` / `rollback`) | v1.1 |
| ~~WebSocket realtime transport~~ | **✅ shipped v1.7.34** (`coder/websocket`, `railbase.v1` subprotocol, PB-compat frames; SSE remains alongside) |
| Documents track (logical entity + versions + polymorphic owner + thumbnails) | v1.3.2 |
| ~~JS hook unit test harness~~ | **✅ shipped v1.7.33** (`MockHookRuntime` in `pkg/railbase/testapp`) |
| Devices trust 30d + revoke | v1.1.x |
| ~~Hook memory limit~~ | **partial ✅ v1.7.31** (`SetMaxCallStackSize(128)` — partial closure; true heap-limit not exposed by goja, deferred) |
| Cache wiring (filter AST / RBAC resolver / i18n bundles / settings) | **✅ shipped v1.7.31-32** (4 caches registered; admin Cache inspector lists live entries) |
| RBAC bus-driven invalidation | **✅ shipped v1.7.32** (`rbac.role_{granted,revoked,assigned,unassigned,deleted}` topics) |
| Mailer hooks dispatcher | **✅ shipped v1.7.31** (`mailer.before_send` sync + `mailer.after_send` async via eventbus) |
| Auth hooks dispatcher | **✅ shipped v1.7.34** (`auth.{signin,signup,refresh,logout,lockout}` topics via eventbus) |
| Anti-bot middleware | **✅ shipped v1.7.33** (honeypot form fields + UA sanity, production-gated) |
| `scheduled_backup` cron builtin | **✅ shipped v1.7.31** |
| `orphan_reaper` cron builtin | **✅ shipped v1.7.33** (weekly Sunday 05:00 UTC, two-direction sweep DB+FS) |
| `send_email_async` cron builtin | **✅ shipped v1.7.30** (mailer→jobs `ErrPermanent` cross-package promotion v1.7.32) |
| 35+ enterprise / mobile / AI plugins | v1.2+ |

---

## Performance baselines (M2, native Go 1.26.1)

Numbers are measurable + reproducible — every one ships with a `Benchmark*` or `Test*` invariant under `go test -race [-tags embed_pg]`. See the `_bench_test.go` files in `internal/jobs/`, `internal/realtime/`, `internal/api/rest/`, `internal/hooks/`, `internal/files/`.

| Benchmark | Result | docs/17 budget | Headroom |
|---|---|---|---|
| Realtime fan-out, 10k subs | 2.3 ms p50 / 3.9 ms p99 | < 100 ms | ~25× |
| DB write throughput (INSERT) | 21 k rows/sec serial | ≥ 10 k/sec | 2.1× |
| DB write throughput (COPY) | 258 k rows/sec | ≥ 10 k/sec | 25× |
| Hook dispatch, no handlers | 58 ns/op | n/a (fast-path gate) | — |
| Hook dispatch, 100 goroutines | 304 k disp/sec | (no-deadlock) | ✅ |
| RLS overhead (100k rows) | 2.53% | < 5% | ~2× under budget |
| FS upload concurrency (50) | 9.5 k uploads/sec, 0 corruption | (atomic-rename) | ✅ |
| Jobs queue throughput | 5000+ jobs/sec at 4 workers, 0 double-claim | ≥ 5000/sec | ✅ |

---

## Upgrade from PocketBase

1. Export your PB schema: `railbase import schema --from-pb https://your-pb.example.com`
2. Review the generated Go schema files (13 PB field types translated; system + view collections skipped; dangling relations → TODO).
3. Run migrations: `railbase migrate up`
4. Set `RAILBASE_COMPAT_MODE=strict` for drop-in PB JS SDK compatibility.

Modes:
- **`strict`** (default for v1 SHIP) — every PB v0.22+ public endpoint shape matched.
- **`native`** — Railbase-only routes, useful for greenfield projects.
- **`both`** — both surface mounted; live setting toggleable via the bus.

---

## Installation

### Pre-built binary

```bash
# linux amd64
curl -L https://github.com/railbase/railbase/releases/download/v1.0.0/railbase_1.0.0_linux_x86_64.tar.gz | tar xz
./railbase serve

# macOS arm64
curl -L https://github.com/railbase/railbase/releases/download/v1.0.0/railbase_1.0.0_macos_arm64.tar.gz | tar xz
./railbase serve
```

(URLs become live when the v1.0.0 tag is published.)

### From source

```bash
git clone https://github.com/railbase/railbase
cd railbase
make build           # production binary, no embedded PG
make build-embed     # dev binary, with embedded PG (-tags embed_pg)
```

### Docker

```bash
docker run -p 8095:8095 ghcr.io/railbase/railbase:1.0.0
```

(Image becomes live with the tag.)

---

## Configuration

Zero-config defaults work for local development. Production needs:

- `RAILBASE_DSN` — Postgres connection string (required in production mode).
- `RAILBASE_DATA_DIR` — where `.secret`, `pb_hooks/`, `pb_data/i18n/`, `storage/`, `email_templates/`, `pdf_templates/` live. Default: `./pb_data/`.
- `.secret` — 32-byte hex master key. Auto-generated in dev; must pre-exist in production.

Full config surface: see `internal/config/config.go` (env-prefixed `RAILBASE_*`) and `docs/14-observability.md`.

---

## Smoke validation

After install:

```bash
./railbase init demo
cd demo
./demo serve --embed-postgres
# wait for the ready banner; URL printed to stdout

# in another terminal
make smoke    # runs scripts/smoke-5min.sh — 13 HTTP probes
```

Expected: every probe returns 2xx in < 90s warm.

---

## Acknowledgements

PocketBase set the bar for single-binary DX. Railbase aims to be the next-tier-up choice — same boot speed, broader feature surface, production-grade architecture from day one.

Built and shipped through ~25 autonomous-loop milestones from v1.0 through v1.7.25. See [progress.md](../progress.md) for the per-milestone history.

---

## Next: v1.1 (target ~6 weeks post v1 SHIP)

- Plugin host (custom gRPC + manifest + distribution)
- `railbase-cluster` (NATS distributed broker)
- `railbase-orgs` / `railbase-billing` / `railbase-authority`
- S3 / GCS storage adapters
- OpenTelemetry (OTLP/HTTP) + Prometheus `/metrics`
- Audit Ed25519 sealing
- Postgres production hardening guide
- Self-update mechanism

Full v1.1 / v1.2 / v2 roadmap: [docs/16-roadmap.md](16-roadmap.md).
