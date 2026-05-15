-- v3.x — unified audit log: split into _audit_log_site +
-- _audit_log_tenant, both partitioned by month.
--
-- Design: docs/19-unified-audit.md.
--
-- The legacy `_audit_log` (migration 0006) stays read-only — existing
-- rows keep their chain-v1 verify, no data migration. New writes go
-- to the two tables below via the new `audit.Store`. The admin UI's
-- Timeline view UNION-reads all three sources.
--
-- Why split site vs tenant:
--
--   * Tenant offboarding: dropping rows for tenant T must not break
--     other tenants' chain verify. Per-tenant chains make this
--     native — each tenant_id's hash chain is independent.
--
--   * RLS on read: tenant audit is naturally scoped via the
--     `railbase.tenant` session var (same pattern as tenant
--     collections). Site audit (system + admin actions) deliberately
--     bypasses RLS — only operators with `audit.read` see it.
--
--   * Write contention: a single global chain serialises every
--     audit write process-wide. Split tables + per-tenant prev_hash
--     cache let writes for different tenants proceed in parallel.
--
-- Partitioning: declarative range-partitioning by `at` (monthly).
-- The initial migration creates the current + next month's
-- partitions; the `audit_partition` cron (Phase 2) keeps the window
-- rolling. DROP PARTITION is O(1) for the archive flow.

-- ─────────────────────────────────────────────────────────────────
-- Shared enums
-- ─────────────────────────────────────────────────────────────────

-- audit_actor_type: WHO performed the action. Distinct types so the
-- UI can chip-filter "show only system events" without grep'ing
-- actor_id IS NULL. Site + tenant tables share most values but tenant
-- adds 'user' (a logged-in user-collection principal) which doesn't
-- apply on the site surface.
CREATE TYPE audit_actor_type AS ENUM (
    'system',     -- cron, bootstrap, migration runner; actor_id IS NULL
    'admin',      -- _admins row; actor_id is _admins.id
    'api_token',  -- _api_tokens row; actor_id is the token's owner_id
    'job',        -- background worker (cleanup, audit_seal, etc.)
    'user'        -- only valid in _audit_log_tenant
);

-- audit_outcome: standard tri-state. 'denied' = RBAC / business rule
-- said no; 'error' = unexpected internal failure (panic, DB error).
-- 'failed' from legacy _audit_log is folded into 'error' for the new
-- tables.
CREATE TYPE audit_outcome AS ENUM ('success', 'denied', 'error');

-- ─────────────────────────────────────────────────────────────────
-- _audit_log_site — system + admin actions
-- ─────────────────────────────────────────────────────────────────

CREATE TABLE _audit_log_site (
    id              UUID         NOT NULL DEFAULT gen_random_uuid(),
    seq             BIGSERIAL    NOT NULL,
    at              TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- Actor identity. NULL actor_id ⇒ system action (cron, migration).
    actor_type      audit_actor_type NOT NULL,
    actor_id        UUID         NULL,
    actor_email     TEXT         NULL,      -- denormalised for cheap UI display
    actor_collection TEXT        NULL,      -- '_admins' | '_api_tokens' | NULL

    -- Action: dotted name. Stable; clients filter on prefix.
    event           TEXT         NOT NULL,

    -- Optional entity reference. CRUD-shaped events fill these; pure
    -- actor events (auth.signin) leave them NULL. The Store API
    -- separates Entity vs ActorOnly writers so misuse trips a
    -- compile-time error.
    entity_type     TEXT         NULL,
    entity_id       TEXT         NULL,      -- TEXT supports non-UUID PKs (Stripe IDs, etc.)

    outcome         audit_outcome NOT NULL,

    -- Diff payload. before NULL on create; after NULL on delete; both
    -- NULL on actor-only events. PII redaction happens in the Store
    -- before insert via internal/security.IsSensitiveKey.
    before          JSONB        NULL,
    after           JSONB        NULL,
    -- Free-form context: request_id source, hook trigger, etc.
    -- Hashed alongside before/after so any tampering is caught.
    meta            JSONB        NULL,

    error_code      TEXT         NULL,
    error_data      JSONB        NULL,
    ip              TEXT         NULL,
    user_agent      TEXT         NULL,
    request_id      TEXT         NULL,      -- correlates with structured logger

    -- Hash chain v2. prev_hash references the prior row's hash within
    -- this table (independent of legacy _audit_log and of _audit_log_tenant).
    -- Genesis = 32 zero bytes.
    prev_hash       BYTEA        NOT NULL,
    hash            BYTEA        NOT NULL,
    chain_version   SMALLINT     NOT NULL DEFAULT 2,

    PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);

-- Initial partitions: current month + next month. Subsequent
-- partitions created by the audit_partition cron (Phase 2).
CREATE TABLE _audit_log_site_default PARTITION OF _audit_log_site DEFAULT;

-- Indexes: created on the parent so they propagate to every partition.
-- (Postgres 11+ behaviour: indexes on partitioned parents are
-- "partitioned indexes" — automatically attached to child partitions.)
CREATE INDEX _audit_log_site_seq_idx     ON _audit_log_site (seq);
CREATE INDEX _audit_log_site_event_idx   ON _audit_log_site (event);
CREATE INDEX _audit_log_site_actor_idx   ON _audit_log_site (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX _audit_log_site_entity_idx  ON _audit_log_site (entity_type, entity_id) WHERE entity_id IS NOT NULL;
-- Partial index on non-success outcomes — the most common forensic
-- filter is "show me the denials / errors", and they're a small
-- fraction of total rows.
CREATE INDEX _audit_log_site_outcome_idx ON _audit_log_site (outcome) WHERE outcome <> 'success';
CREATE INDEX _audit_log_site_request_idx ON _audit_log_site (request_id) WHERE request_id IS NOT NULL;

-- ─────────────────────────────────────────────────────────────────
-- _audit_log_tenant — per-tenant actions, RLS-scoped
-- ─────────────────────────────────────────────────────────────────

CREATE TABLE _audit_log_tenant (
    id              UUID         NOT NULL DEFAULT gen_random_uuid(),
    seq             BIGSERIAL    NOT NULL,        -- monotonic across tenants
    tenant_seq      BIGINT       NOT NULL,        -- monotonic WITHIN one tenant; backs per-tenant verify
    at              TIMESTAMPTZ  NOT NULL DEFAULT now(),

    tenant_id       UUID         NOT NULL,

    actor_type      audit_actor_type NOT NULL,
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
    -- the SAME tenant_id. Dropping rows for tenant T doesn't
    -- invalidate any other tenant's chain.
    prev_hash       BYTEA        NOT NULL,
    hash            BYTEA        NOT NULL,
    chain_version   SMALLINT     NOT NULL DEFAULT 2,

    PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);

CREATE TABLE _audit_log_tenant_default PARTITION OF _audit_log_tenant DEFAULT;

-- RLS: every read scoped to the railbase.tenant session var. Writes
-- go through the application-level Store which sets the session var
-- inside a short-lived transaction (see internal/audit/store.go).
ALTER TABLE _audit_log_tenant ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON _audit_log_tenant
    USING (tenant_id = NULLIF(current_setting('railbase.tenant', true), '')::uuid);

-- Indexes. (tenant_id, tenant_seq) is the canonical sort key for
-- per-tenant verify; the planner picks it for the chain-walk query.
CREATE INDEX _audit_log_tenant_tenant_seq_idx ON _audit_log_tenant (tenant_id, tenant_seq);
CREATE INDEX _audit_log_tenant_event_idx      ON _audit_log_tenant (event);
CREATE INDEX _audit_log_tenant_actor_idx      ON _audit_log_tenant (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX _audit_log_tenant_entity_idx     ON _audit_log_tenant (entity_type, entity_id) WHERE entity_id IS NOT NULL;
CREATE INDEX _audit_log_tenant_outcome_idx    ON _audit_log_tenant (outcome) WHERE outcome <> 'success';
CREATE INDEX _audit_log_tenant_request_idx    ON _audit_log_tenant (request_id) WHERE request_id IS NOT NULL;

-- ─────────────────────────────────────────────────────────────────
-- Extend _audit_seals to distinguish seal targets
-- ─────────────────────────────────────────────────────────────────
--
-- The existing _audit_seals table (migration 0022) holds Ed25519
-- signatures over chain_head values. We add `target` + `tenant_id` so
-- the same table covers seals for all three chains: legacy _audit_log,
-- _audit_log_site, and per-tenant _audit_log_tenant ranges.
--
-- target values:
--   'legacy'        — seal over the legacy _audit_log chain
--   'site'          — seal over _audit_log_site
--   'tenant'        — seal over _audit_log_tenant for one tenant_id
--                     (tenant_id NOT NULL)

ALTER TABLE _audit_seals ADD COLUMN target TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE _audit_seals ADD COLUMN tenant_id UUID NULL;

CREATE INDEX _audit_seals_target_idx ON _audit_seals (target, range_end);
CREATE INDEX _audit_seals_tenant_idx ON _audit_seals (tenant_id, range_end) WHERE tenant_id IS NOT NULL;

-- Sanity constraint: tenant target ⇔ tenant_id populated.
ALTER TABLE _audit_seals ADD CONSTRAINT _audit_seals_tenant_target_chk
    CHECK ((target = 'tenant' AND tenant_id IS NOT NULL)
        OR (target <> 'tenant' AND tenant_id IS NULL));
