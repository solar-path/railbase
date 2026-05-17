-- v2.0-alpha — DoA delegation primitive (Slice 1 hardening of Slice 0).
--
-- *** PROTOTYPE — NOT FOR PRODUCTION USE (Slice 0/1 transition). ***
--
-- Delegation: actor X explicitly delegates their approval authority on
-- a set of action_keys to actor Y for a time-bounded window. When the
-- workflow runtime resolves "is principal Y in the qualified pool for
-- this level?", it joins through this table to expand the effective
-- set.
--
-- Slice 0/1 boundary:
--   - Slice 0 had upstream-trust qualification; Slice 0 hardening
--     replaced it with a strict pool-membership check. Delegation
--     widens that pool transparently — the gate code doesn't need to
--     change, only ResolveApprovers reads from the delegation join.
--   - For Slice 1 we DO NOT enforce delegator-must-be-qualified-too.
--     Real-world delegation chains can cross levels (CFO delegates
--     finance approval to a deputy who's only an editor in RBAC). The
--     delegation table is the source of truth — embedders that want
--     RBAC-equivalent constraints can layer them via /admin/api/
--     authority/delegations validation.
--
-- Forward-compat columns reserved (not used in Slice 1):
--   - source_action_keys[] — whitelist specific action keys (NULL = all)
--   - max_amount — cap on amounts this delegation covers (NULL = unlimited)
--
-- Audit chain integration: every insert/update on this table should
-- emit an _authority_audit row when that table lands (Slice 2). For now
-- the row's created_by + revoked_by columns are the only audit trail.
-- =============================================================================

CREATE TABLE _doa_delegations (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- The actor whose authority is being delegated.
    delegator_id        UUID        NOT NULL,

    -- The actor receiving the authority.
    delegatee_id        UUID        NOT NULL,

    -- Tenant scope. NULL = site-scope delegation; non-NULL = applies
    -- only when the workflow is in this tenant.
    tenant_id           UUID        NULL,

    -- Action key whitelist. NULL = ALL action keys (broad delegation).
    -- Use to scope a delegation to e.g. just {"expenses.approve"}.
    -- Stored as text array; query uses && for overlap test.
    source_action_keys  TEXT[]      NULL,

    -- Materiality cap. NULL = no cap. When set, the workflow runtime's
    -- qualification check rejects this delegation for workflows whose
    -- amount exceeds the cap.
    max_amount          BIGINT      NULL,

    -- Time window. effective_from defaults to creation time; effective_to
    -- NULL = open-ended (active until revoked).
    effective_from      TIMESTAMPTZ NOT NULL DEFAULT now(),
    effective_to        TIMESTAMPTZ NULL,

    -- Lifecycle: active | revoked.
    status              TEXT        NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),

    -- Revocation audit trail.
    revoked_reason      TEXT        NULL,
    revoked_by          UUID        NULL,
    revoked_at          TIMESTAMPTZ NULL,

    -- Creation audit trail.
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by          UUID        NULL,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Operator notes / paper trail (free text).
    notes               TEXT        NULL,

    -- Sanity: delegator cannot self-delegate (would be a no-op +
    -- accidentally widens audit confusion).
    CHECK (delegator_id <> delegatee_id),
    -- Sanity: if revoked, status MUST be 'revoked' (atomic flip).
    CHECK ((revoked_at IS NULL AND status = 'active') OR
           (revoked_at IS NOT NULL AND status = 'revoked')),
    -- Sanity: effective_to must be after effective_from when set.
    CHECK (effective_to IS NULL OR effective_to > effective_from)
);

-- Hot path: "given delegatee Y, what active delegations apply to them
-- right now?" — runs at every RecordDecision call when delegation
-- resolution is enabled. Composite index covers status + delegatee for
-- selectivity.
CREATE INDEX _doa_delegations_delegatee_idx
    ON _doa_delegations (delegatee_id, status)
    WHERE status = 'active';

-- Audit path: "given delegator X, what authority have they given out?"
-- Less hot but used by the admin UI for delegator dashboards.
CREATE INDEX _doa_delegations_delegator_idx
    ON _doa_delegations (delegator_id, status);

-- Tenant scoping lookup — used when resolving delegations for a
-- tenant-scoped workflow.
CREATE INDEX _doa_delegations_tenant_idx
    ON _doa_delegations (tenant_id)
    WHERE tenant_id IS NOT NULL;

COMMENT ON TABLE _doa_delegations IS
    'v2.0-alpha — Delegation of approval authority between actors. '
    'Joined into ResolveApprovers expansion to widen the qualified pool.';
