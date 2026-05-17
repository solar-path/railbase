# Unified Audit Log — Design (Phase 1)

> Status: design draft for review. Implements the «one journal, all actions»
> requirement for corporate/SaaS Railbase deployments.
>
> Replaces the four-tab Logs UI (Audit / App logs / Email events / Notifications)
> with a single timeline backed by two append-only hash-chained tables:
> `_audit_log_site` (system + admin actions) and `_audit_log_tenant`
> (per-tenant actions under RLS).

## 1. Goals & non-goals

### Goals
- **Single timeline UI** for all business / security / system events.
  Operator answers «что произошло с X» without tab-switching.
- **`(actor, action, when, before, after)` shape** everywhere — including
  CRUD diffs from collection records, mailer.Send, jobs, settings changes.
- **Multi-tenant native**: per-tenant chain so tenant offboarding exports
  + drops the tenant's audit without breaking other tenants' verify.
- **Tamper-evident**: SHA-256 chain + periodic Ed25519 seal (existing
  `_audit_seals` mechanism extended to two tables).
- **Retention**: hot 14 days in PG, cold (older) archived to
  `<dataDir>/audit/YYYY-MM/*.jsonl.gz` with seal manifest.
- **Volume-ready**: declarative partitioning by month, indexed on the
  filters the UI uses.

### Non-goals (this phase)
- Pluggable archive target (S3 Object Lock, GCS, Azure) — Phase 3.
- External KMS-signed seals — Phase 4, opt-in per deployment.
- Merkle-tree verify across archive segments — Phase 2.
- Per-record granular access log (every SELECT) — out of scope.

## 2. Why two tables, not one

| Concern | Single `_audit_log` + RLS | Split site/tenant (chosen) |
|---|---|---|
| Tenant offboarding | Can't DELETE rows (chain breaks) | Drop tenant chain entirely |
| `tenant_id = NULL` (system events) | Visible to every tenant via RLS bypass | Lives in `_audit_log_site`, no tenant context |
| Per-tenant export | RLS-scoped SELECT for each tenant | Native — chain belongs to tenant |
| Write contention | Single global mutex | Per-tenant + per-site mutexes |
| Verify | One chain | Two independent chains |
| Cross-tenant forensic view («who's the admin that created tenant Y») | Native | Joins site → tenant by tenant_id |

**Decision**: split is chosen. The marginal cost (two verifiers, two
chain heads in memory) is small; the operational benefit (clean tenant
lifecycle, smaller per-tenant chains, parallel writes) is large.

## 3. Schema

### 3.1 `_audit_log_site` — system + admin actions

Migration: `0030_unified_audit.up.sql`

```sql
CREATE TABLE _audit_log_site (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    seq             BIGSERIAL    NOT NULL,
    at              TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- Actor: who did this. NULL actor_id ⇒ system (cron, bootstrap,
    -- migration). actor_type is a small enum the UI groups by.
    actor_type      audit_actor_type NOT NULL,  -- 'system'|'admin'|'api_token'|'job'
    actor_id        UUID         NULL,
    actor_email     TEXT         NULL,          -- denormalised for cheap UI display
    actor_collection TEXT        NULL,          -- '_admins' | '_api_tokens' | NULL for system/job

    -- Action: WHAT happened. Dotted: 'admin.backup.create',
    -- 'auth.signin', 'system.migration.applied', 'mailer.send'.
    event           TEXT         NOT NULL,

    -- Optional entity reference. Lets «show everything about vendor X»
    -- filter index-hit without JSONB grepping. Writer API REQUIRES
    -- these for entity-bound events (vendor.update); only system-wide
    -- events (audit.seal.completed) may omit them.
    entity_type     TEXT         NULL,
    entity_id       TEXT         NULL,          -- TEXT (not UUID) — supports composite / non-UUID PKs

    -- Outcome of the action.
    outcome         audit_outcome NOT NULL,     -- 'success'|'denied'|'error'

    -- Diff payload. before NULL for create-events; after NULL for
    -- delete-events. Both NULL for actor-only events (login, etc.).
    before          JSONB        NULL,
    after           JSONB        NULL,
    -- Free-form context: request_id, client info, hook trigger source,
    -- whatever the writer wants to enrich the row with. NOT part of
    -- before/after diff and not hashed differently — just a side
    -- channel for forensic context.
    meta            JSONB        NULL,

    -- Wire enrichments.
    error_code      TEXT         NULL,
    error_data      JSONB        NULL,
    ip              TEXT         NULL,
    user_agent      TEXT         NULL,
    request_id      TEXT         NULL,          -- correlates with logger / hooks

    -- Hash chain.
    prev_hash       BYTEA        NOT NULL,
    hash            BYTEA        NOT NULL,
    chain_version   SMALLINT     NOT NULL DEFAULT 2  -- v1 = legacy _audit_log, v2 = this table
) PARTITION BY RANGE (at);

CREATE TYPE audit_actor_type AS ENUM ('system', 'admin', 'api_token', 'job');
CREATE TYPE audit_outcome AS ENUM ('success', 'denied', 'error');

-- Indexes (per partition):
CREATE INDEX _audit_log_site_seq_idx    ON _audit_log_site (seq);
CREATE INDEX _audit_log_site_event_idx  ON _audit_log_site (event);
CREATE INDEX _audit_log_site_actor_idx  ON _audit_log_site (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX _audit_log_site_entity_idx ON _audit_log_site (entity_type, entity_id) WHERE entity_id IS NOT NULL;
CREATE INDEX _audit_log_site_outcome_idx ON _audit_log_site (outcome) WHERE outcome <> 'success';
CREATE INDEX _audit_log_site_request_idx ON _audit_log_site (request_id) WHERE request_id IS NOT NULL;

-- Monthly partitions: created on the fly by audit_partition cron.
-- Initial partition for the current month is created in this migration;
-- subsequent ones are created by the job 1 day before the boundary.
```

### 3.2 `_audit_log_tenant` — per-tenant actions

Same shape, plus `tenant_id` (NOT NULL) and `actor_type` enum gains
`'user'`:

```sql
CREATE TYPE audit_actor_type_tenant AS ENUM ('user', 'admin', 'api_token', 'system', 'job');

CREATE TABLE _audit_log_tenant (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    seq             BIGSERIAL    NOT NULL,           -- monotonic ACROSS tenants (single sequence)
    tenant_seq      BIGINT       NOT NULL,           -- monotonic WITHIN one tenant; backs per-tenant verify
    at              TIMESTAMPTZ  NOT NULL DEFAULT now(),

    tenant_id       UUID         NOT NULL,

    actor_type      audit_actor_type_tenant NOT NULL,
    actor_id        UUID         NULL,
    actor_email     TEXT         NULL,
    actor_collection TEXT        NULL,

    event           TEXT         NOT NULL,
    entity_type     TEXT         NULL,
    entity_id       TEXT         NULL,
    outcome         audit_outcome NOT NULL,

    before          JSONB        NULL,
    after           JSONB        NULL,
    meta            JSONB        NULL,

    error_code      TEXT         NULL,
    error_data      JSONB        NULL,
    ip              TEXT         NULL,
    user_agent      TEXT         NULL,
    request_id      TEXT         NULL,

    -- Per-tenant hash chain. prev_hash links to the previous row of
    -- the SAME tenant_id, not the previous row globally. This is what
    -- makes tenant-offboard-and-export work: dropping rows for tenant
    -- T doesn't invalidate any other tenant's chain.
    prev_hash       BYTEA        NOT NULL,
    hash            BYTEA        NOT NULL,
    chain_version   SMALLINT     NOT NULL DEFAULT 2
) PARTITION BY RANGE (at);

-- RLS: every read scoped to railbase.tenant session var (existing
-- pattern, mirrors tenant collections).
ALTER TABLE _audit_log_tenant ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON _audit_log_tenant
    USING (tenant_id = current_setting('railbase.tenant', true)::uuid);

-- Indexes (per partition):
CREATE INDEX _audit_log_tenant_tenant_seq_idx ON _audit_log_tenant (tenant_id, tenant_seq);
CREATE INDEX _audit_log_tenant_event_idx      ON _audit_log_tenant (event);
CREATE INDEX _audit_log_tenant_actor_idx      ON _audit_log_tenant (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX _audit_log_tenant_entity_idx     ON _audit_log_tenant (entity_type, entity_id) WHERE entity_id IS NOT NULL;
CREATE INDEX _audit_log_tenant_outcome_idx    ON _audit_log_tenant (outcome) WHERE outcome <> 'success';
CREATE INDEX _audit_log_tenant_request_idx    ON _audit_log_tenant (request_id) WHERE request_id IS NOT NULL;
```

### 3.3 Seals extension

Existing `_audit_seals` (migration 0022) gains a `target` column
distinguishing seals over `_audit_log` (legacy v1), `_audit_log_site`,
`_audit_log_tenant` per-tenant ranges, **and** subsystem-owned chains
like `_authority_audit`:

```sql
ALTER TABLE _audit_seals ADD COLUMN target TEXT NOT NULL DEFAULT 'legacy';
-- target ∈ ('legacy', 'site', 'tenant:<uuid>', 'authority')
--   'legacy'         — pre-v3.x `_audit_log` chain (read-only after migration)
--   'site'           — `_audit_log_site` (system + admin actions)
--   'tenant:<uuid>'  — `_audit_log_tenant` per-tenant chain
--   'authority'      — `_authority_audit` (v2.0+; DoA workflow / matrix /
--                       delegation events, см. 26-authority.md)

ALTER TABLE _audit_seals ADD COLUMN tenant_id UUID NULL;
-- Populated when target LIKE 'tenant:%'. Backs per-tenant verify.
```

Subsystem chains extend через same Ed25519 seal pattern. `railbase
audit verify --target authority` walks `_authority_audit` row-by-row,
re-computes SHA-256 hash chain, validates Ed25519 signatures из
`_audit_seals WHERE target='authority'`. Tamper на
`_doa_matrices` / `_doa_workflow_decisions` ловится через audit
chain mismatch — application-level CRUD пишет каждое state change
как audit event с linked prev_hash; manual UPDATE bypasses audit
writer, hash diverges от next seal'а, verify fails.

**Status: DoA chain integration delivered Slice 2.3 (2026-05-17).**
`internal/authority/audit_hook.go` определяет `AuditHook` (обёртка над
`audit.Writer`) с девятью типизированными методами эмиссии под одной
chain `target='authority'`:

| Event method | When fired | Source site |
|---|---|---|
| `MatrixCreated` | Admin creates matrix (status='draft') | `adminapi/authority.go` |
| `MatrixApproved` | Admin transitions draft→approved | `adminapi/authority.go` |
| `MatrixRevoked` | Admin transitions approved→revoked | `adminapi/authority.go` |
| `WorkflowCreated` | Mutation hits gate, triggers workflow | `authorityapi/workflow.go` |
| `WorkflowDecision` | Approver signs (approve or reject) | `authorityapi/workflow.go` |
| `WorkflowCancelled` | Initiator cancels running workflow | `authorityapi/workflow.go` |
| `WorkflowConsumed` | UPDATE+`MarkConsumed` tx commits | `rest/handlers.go` (post-tx) |
| `DelegationCreated` | User publishes delegation | `adminapi/delegations.go` |
| `DelegationRevoked` | User/reaper revokes delegation | `adminapi/delegations.go` |

Все 9 точек эмиссии **nil-safe** — когда `AuthorityAudit` не выставлен
на `adminapi.Deps`/`rest.handlerDeps`, бизнес-операции работают
молча. Audit emission failures **не** валят бизнес-операции
(fire-and-forget pattern, тот же что у `audit.Writer`). Один особый
случай — `WorkflowConsumed` — эмитируется AFTER tx commit, чтобы
phantom row не утёк в chain если transaction откатится.

Регрессионное покрытие: `TestAuditHook_EmitsDoALifecycle` вызывает
все 9 методов + проверяет chain integrity (`prev_hash[N] ==
hash[N-1]`, без gaps). `TestAuditHook_NilSafe` — no-panic с nil
hook / nil writer.

## 4. Writer API

### 4.1 Go surface (canonical)

```go
package audit

// Store is the platform-wide audit handle. Constructed once on boot,
// shared via Deps. Internally holds two Writers (site + tenant) +
// per-tenant prev_hash cache.
type Store struct { /* ... */ }

// SiteEvent is the input shape for system + admin events written to
// _audit_log_site. Caller provides actor + action; Store fills
// id/seq/at/prev_hash/hash and (optionally) actor_email by lookup.
type SiteEvent struct {
    ActorType  ActorType        // 'system' | 'admin' | 'api_token' | 'job'
    ActorID    uuid.UUID        // zero ⇒ system
    Event      string           // 'admin.backup.create', 'system.migration.applied'
    EntityType string           // 'backup' | 'collection' | 'role' | ''
    EntityID   string           // primary key as string; '' allowed for actor-only events
    Outcome    Outcome
    Before     any              // optional; redacted before persist
    After      any
    Meta       map[string]any
    ErrorCode  string
    Error      error            // captured into error_data
    IP         string
    UserAgent  string
    RequestID  string           // from ctx; correlates with logger
}

// TenantEvent is the input shape for tenant-scoped events. tenant_id
// is required and must match the ctx tenant; mismatch is a fatal write
// (we don't allow cross-tenant audit writes from a request bound to
// tenant X).
type TenantEvent struct {
    TenantID   uuid.UUID        // required, ctx-validated
    ActorType  TenantActorType  // 'user' | 'admin' | 'api_token' | 'system' | 'job'
    ActorID    uuid.UUID
    Event      string
    EntityType string
    EntityID   string
    Outcome    Outcome
    Before, After any
    Meta       map[string]any
    ErrorCode  string
    Error      error
    IP, UserAgent, RequestID string
}

// Write methods. Two flavours per surface: -Entity (entity_type/id
// required, panic if missing — for CRUD-style events) and -ActorOnly
// (entity unset by design — login, signout, cron tick).
func (s *Store) WriteSiteEntity(ctx context.Context, e SiteEvent) (uuid.UUID, error)
func (s *Store) WriteSiteActorOnly(ctx context.Context, e SiteEvent) (uuid.UUID, error)
func (s *Store) WriteTenantEntity(ctx context.Context, e TenantEvent) (uuid.UUID, error)
func (s *Store) WriteTenantActorOnly(ctx context.Context, e TenantEvent) (uuid.UUID, error)

// Helpers that pull actor + tenant + request_id out of ctx. The 90%
// path uses these, not the explicit struct.
func (s *Store) Action(ctx context.Context, event string) Builder
// builder.Entity("vendor", id).Before(b).After(a).Outcome(...).Write(ctx)

// Verify walks the chain for one target.
func (s *Store) VerifySite(ctx context.Context) (rows int64, err error)
func (s *Store) VerifyTenant(ctx context.Context, tenantID uuid.UUID) (rows int64, err error)
func (s *Store) VerifyAll(ctx context.Context) (perTarget map[string]int64, errs map[string]error)
```

### 4.2 JS-hooks bridge

```js
// In a $app.routerAdd / $app.onRecordAfterCreate hook:
$app.audit.write({
    event: 'document.uploaded',
    entity_type: 'document',
    entity_id: ctx.record.id,
    before: null,
    after: ctx.record,
    meta: { source: 'web-upload' },
});
// actor / tenant / request_id auto-extracted from hook ctx
```

### 4.3 CRUD auto-audit

CollectionSpec gains an opt-in flag:

```go
type CollectionSpec struct {
    // ... existing fields ...
    // Audit: when true, every Create/Update/Delete on a record of this
    // collection auto-writes a tenant or site audit event with the
    // computed before/after diff. Off by default — audit-heavy
    // collections (sessions, ephemerals) shouldn't pay the cost.
    Audit bool `json:"audit,omitempty"`
}
```

Implementation hook: `rest` package's CUD path checks
`collection.Audit && tenant.HasID(ctx)` and writes
`<collection>.{created,updated,deleted}` with the record diff.

## 5. Legacy `_audit_log` strategy

**Decision: legacy table stays read-only, NOT migrated.**

Rationale:
- 19 rows on the user's dev install — empty in fresh deploys.
- Migrating preserves chain v1 verify (already works), gives a clean
  cutover point in the timeline.
- New writes go to `_audit_log_site` / `_audit_log_tenant`.
- UI Timeline UNIONs three sources (legacy + site + tenant), showing a
  `chain_version` badge.
- `railbase audit verify` runs all three verifiers; legacy verify is
  no-op-fast once the table stops growing.

A future migration (Phase 1.5) can EXPORT the legacy rows into the
archive format (jsonl.gz under `audit/legacy/`) and DROP the table.
Not required now.

## 6. Read path: single timeline UI

### 6.1 New endpoint
`GET /api/_admin/audit/timeline` — UNION-reads across `_audit_log_site`
(always) + `_audit_log_tenant` (filtered by current admin's
visibility) + legacy `_audit_log` (paginated catch-up). Response:

```ts
interface AuditTimelineResponse {
    items: Array<{
        source: 'site' | 'tenant' | 'legacy';
        chain_version: 1 | 2;
        seq: number;
        id: string;
        at: string;
        tenant_id: string | null;
        actor: { type: string; id: string | null; email: string | null };
        event: string;
        entity: { type: string | null; id: string | null };
        outcome: 'success' | 'denied' | 'error';
        before: unknown | null;
        after: unknown | null;
        meta: Record<string, unknown> | null;
        error_code: string | null;
        ip: string | null;
        user_agent: string | null;
        request_id: string | null;
    }>;
    page: number;
    perPage: number;
    totalItems: number;
}
```

Filters: `actor_type`, `event` substring, `entity_type`, `entity_id`,
`tenant_id` (visible only to site admin), `outcome`, `since`, `until`,
`request_id`. Default sort: `at DESC`.

### 6.2 UI shape

`admin/src/screens/logs.tsx` — single Timeline screen:
- Removes 4-tab horizontal switcher (Audit / App logs / Email events / Notifications).
- Adds `actor_type` chip-filter (System | Admin | User | API token | Job).
- Adds `entity_type` + `entity_id` filter fields.
- Adds `tenant_id` filter (visible only if current admin = site_admin).
- Adds `request_id` field with «show siblings» helper that finds all
  rows in the same request.
- Row click opens drawer with raw before/after JSON diff (uses
  existing `react-diff-viewer` or similar — see decision below).

### 6.3 Where Email events / Notifications / App logs go

- **Email events** → `Settings → Mailer → Deliveries` (existing
  `_email_events` browser, unchanged. Triggered from a `mailer.send`
  audit row via «show delivery state» link in row drawer).
- **Notifications** → `Settings → Notifications → Log` (existing
  `_notifications` browser, unchanged).
- **App logs** → `Health → Process logs` (new screen, same QDatatable
  shape, retention 14 days unchanged, slog ↔ `_logs` table unchanged).

All three remain as **deep-dive surfaces** for their respective state
machines. The Timeline UI links into them from relevant rows.

## 7. Writer migration map

| Source | Current state | New state |
|---|---|---|
| `auth.signin` / `auth.signup` / `auth.signout` / `auth.refresh` / `auth.password_reset` | `audit.Write` → `_audit_log` | `Store.WriteSiteActorOnly` for admin auth; `Store.WriteTenantActorOnly` for user auth |
| `admin.bootstrap` | `_audit_log` | `_audit_log_site`, actor=system |
| `admin.metrics.read` | `_audit_log` — **delete, this is noise** | not written (metrics reads are not audit-worthy) |
| `admin.backup.*` / `admin.backup_restore.*` | `_audit_log` | `_audit_log_site`, actor=admin |
| `rbac.*` | `_audit_log` | `_audit_log_site` (role changes) + `_audit_log_tenant` (tenant-role changes) |
| `settings.changed` | `_audit_log` | `_audit_log_site`, entity_type='setting', entity_id=key |
| `mailer.send` | only `_email_events` | adds `_audit_log_site` (admin-triggered) or `_audit_log_tenant` (user-triggered) write; `_email_events` remains for delivery state machine |
| `notifications.create` | only `_notifications` | adds tenant audit row |
| `jobs.<kind>.completed/failed` | only `_jobs` row | adds `_audit_log_site` with outcome + error |
| `schema.migration.applied` | nothing | adds `_audit_log_site`, actor=system |
| CRUD on collections with `audit: true` | nothing | auto-writes `<collection>.{created,updated,deleted}` via REST handler hook |

## 8. Retention (Phase 2 preview)

Not implemented in Phase 1. Phase 2 adds:
1. `audit_partition` cron — pre-creates next month's partition.
2. `audit_archive` cron — for each completed sealed range older than
   14 days:
   - Stream rows to `<dataDir>/audit/<target>/YYYY-MM/<from>--<to>.jsonl.gz`
   - Write `.seal.json` next to it with chain_head + signature + range
   - Verify the archive (re-walk + Ed25519 check)
   - DROP partition once verified
3. `railbase audit verify` learns `--include-archive` flag.

Phase 1 ships with **no archive** — partitions accumulate indefinitely.
This is fine for the first 6-12 months on dev installs; the cron lands
before that becomes a problem.

## 9. Volume budget (sanity)

Per `_audit_log_tenant` per tenant:
- 10K DAU × 30 audit-worthy events/user/day = 300K rows/day
- Avg row 1.5 KB (50% have before/after JSONB) = 450 MB/day raw
- Indexed overhead ~30% = 600 MB/day per tenant

At 100 tenants × 600 MB/day = 60 GB/day platform-wide. Without
partitioning + archive, hot 14 days = 840 GB. **This is why Phase 2
retention is non-optional for prod multi-tenant deployments**, but
Phase 1 still ships usable because partitioning is in place from day 1
(DROP partition is O(1)).

## 10. Migration call sites (Phase 1 work breakdown)

Concrete files I'll touch in Phase 1, in order:

1. `internal/db/migrate/sys/0030_unified_audit.up.sql` + `.down.sql` — schema
2. `internal/audit/store.go` (new) — `Store` with two Writers + per-tenant prev_hash cache
3. `internal/audit/store_test.go` — chain stability, parallel-tenant writes, verify
4. `internal/audit/legacy.go` — Verify for legacy `_audit_log` (kept)
5. `internal/audit/audit.go` — current `Writer` becomes internal `siteWriter` / `tenantWriter`
6. `pkg/railbase/app.go` — replace `audit.NewWriter(...)` with `audit.NewStore(...)`, plumb to Deps
7. `internal/api/auth/*.go` — switch writer calls to `Store.WriteSiteActorOnly` / `WriteTenantActorOnly`
8. `internal/api/adminapi/{backups,backups_restore,settings,rbac}.go` — switch to Store
9. `internal/mailer/send.go` — add audit write alongside `_email_events`
10. `internal/notifications/*.go` — add tenant audit write
11. `internal/jobs/runner.go` — emit `job.<kind>.{success,failed}` audit
12. `internal/db/migrate/migrate.go` — emit `system.migration.applied` audit
13. `internal/api/adminapi/audit_timeline.go` (new) — `GET /audit/timeline` endpoint
14. `admin/src/screens/logs.tsx` — replace 4-tab with Timeline + filters
15. `admin/src/screens/health.tsx` — add Process logs sub-screen (move App logs here)
16. `admin/src/screens/settings_mailer.tsx` — add Deliveries sub-screen (move Email events here)
17. `admin/src/screens/settings_notifications.tsx` — add Log sub-screen (move Notifications here)
18. `pkg/railbase/cli/audit.go` — `railbase audit verify` walks site + tenant + legacy
19. Tests, docs, before/after migration check

Estimated: ~10-15 days of focused work for one engineer.

## 11. Open questions

1. **`tenant_seq` vs `seq` in `_audit_log_tenant`** — keep both?
   Global `seq` (BIGSERIAL across all tenants) is convenient for
   pagination + UI sort. Per-tenant `tenant_seq` is required for
   correct per-tenant chain verify. Decision: keep both. Storage
   overhead 8 bytes/row, negligible.

2. **`entity_id` as TEXT vs UUID** — current Railbase uses UUID
   everywhere. But user collection records may have composite or
   non-UUID PKs in the future (Stripe customer IDs, external system
   refs). Decision: TEXT, document that for UUID entities the value
   is the canonical UUID string.

3. **Redaction allow-list scope** — current `redactJSON` covers
   passwords / tokens via `security.IsSensitiveKey`. Should `meta`
   also be redacted? Decision: yes, apply the same allow-list to
   `meta` for safety.

4. **`actor_collection` denormalisation** — useful or noise? When
   actor is admin, we can derive collection from `actor_type`. But
   external IDP integrations might add `oauth_users`, `scim_users`,
   etc. Decision: keep the column. Costs ~10 bytes, future-proofs.

## 12. Rollout plan

1. Land migration 0030 — both tables created, no writes yet.
2. Land `Store` package + `NewStore` constructor — wired into Deps but
   no callers.
3. Land Timeline endpoint + UI as **additive** (new screen Logs → All).
   Old Audit / App logs / Email events / Notifications tabs unchanged.
4. Switch writers one by one, mark each commit with the migration map
   entry it covers. Each writer switch keeps the old tab populated +
   adds rows to the new Timeline.
5. Once all writers are switched: hide the old 4-tab UI, leave the
   endpoints + handlers for one release as a deprecation cushion.
6. Next release: remove old tabs entirely.

Reversibility: every step before #5 is fully reversible — the new
tables can be dropped, the old writers keep working.
