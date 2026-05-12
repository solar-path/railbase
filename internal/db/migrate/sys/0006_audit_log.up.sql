-- v0.6 — append-only audit log with hash chain.
--
-- Spec: docs/14-observability.md "Audit log".
--
-- Append-only by convention: only INSERT goes through the writer
-- (`internal/audit`). v0.6 doesn't enforce that with revokes — we
-- rely on the railbase application user being non-superuser in
-- production. v1.1 adds the Ed25519 sealer + a separate revoke-
-- everything-but-INSERT migration.
--
-- bare-pool rule: writes happen on a connection acquired outside
-- any request transaction so a request rollback never erases the
-- denial / failure record. The Writer in internal/audit enforces
-- this; this migration just defines storage.
--
-- hash chain: each row carries `prev_hash` (the previous row's
-- `hash`) and `hash = sha256(prev_hash || canonical_json(row_minus_hash))`.
-- The `at` column ties the chain to a wall-clock so an attacker
-- replaying a partial slice still fails verification.

CREATE TABLE _audit_log (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    seq         BIGSERIAL    NOT NULL,        -- monotonically increasing within process
    at          TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- Identity context (nullable: not every event has a user/tenant).
    user_id           UUID         NULL,
    user_collection   TEXT         NULL,      -- "users", "_admins", etc.
    tenant_id         UUID         NULL,

    -- Event kind: dotted name like "auth.signin", "rbac.deny",
    -- "settings.changed". Keep stable; clients filter on prefix.
    event             TEXT         NOT NULL,
    outcome           TEXT         NOT NULL,  -- success | denied | failed | error

    -- Optional structured payload. PII-redaction happens at the
    -- Writer layer; columns store whatever the writer hands us.
    before            JSONB        NULL,
    after             JSONB        NULL,

    error_code        TEXT         NULL,
    ip                TEXT         NULL,
    user_agent        TEXT         NULL,

    -- Hash chain. prev_hash references the prior row's hash; the
    -- very first row uses 32 zero bytes so verifiers always fold a
    -- known starting value into sha256.
    prev_hash         BYTEA        NOT NULL,
    hash              BYTEA        NOT NULL
);

-- Sequence-ordered scans for the verifier and the admin UI feed.
CREATE INDEX _audit_log_seq_idx     ON _audit_log (seq);
CREATE INDEX _audit_log_at_idx      ON _audit_log (at);
CREATE INDEX _audit_log_event_idx   ON _audit_log (event);

-- Per-user / per-tenant filters are common in the admin UI.
CREATE INDEX _audit_log_user_idx    ON _audit_log (user_id)   WHERE user_id   IS NOT NULL;
CREATE INDEX _audit_log_tenant_idx  ON _audit_log (tenant_id) WHERE tenant_id IS NOT NULL;
