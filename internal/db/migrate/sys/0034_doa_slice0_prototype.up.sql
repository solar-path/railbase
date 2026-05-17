-- v2.0-alpha — DoA (Delegation of Authority) Slice 0 prototype.
--
-- *** PROTOTYPE — NOT FOR PRODUCTION USE ***
--
-- This is the bounded architecture-probe slice for v2.0 DoA. Goal:
-- validate hybrid schema+matrix-data design against real implementation
-- BEFORE committing to the full 32-task v2.0 build (see plan.md §6.1).
--
-- Slice 0 scope (explicit limits):
--   - 5 tables: matrices + matrix_levels + matrix_approvers + workflows + workflow_decisions
--   - NO delegation table (deferred to subsequent slice if Slice 0 validates design)
--   - NO _authority_audit chain (deferred — audit-seal integration is its own slice)
--   - NO escalation reaper, no delegation expirer, no tasks integration
--   - Role+user approver types only (position/department_head — v2.x with org-chart primitive)
--
-- After Slice 0 review, this migration is EITHER:
--   - extended in-place with delegation + audit tables for Slice 1
--   - replaced wholesale with a new migration if design needs rework
--   - dropped entirely if Slice 0 reveals fatal architecture issue
--
-- Until first stable release: schema may break between versions. Operators
-- running pre-release v2.0-alpha builds MUST be prepared to wipe and reseed.
--
-- Full spec: docs/26-authority.md (rev2). Open questions closed in
-- plan.md §6.4 (2026-05-16).
--
-- =============================================================================


-- ============================================================================
-- 1. _doa_matrices — approval matrix declarations (runtime data, edit-time).
-- ============================================================================
--
-- key       — matrix identifier, references schema-side `.Authority({Matrix})`
--             declaration. Namespace ownership rule (см. docs/26 §Matrix key
--             namespacing): `system.*` reserved for core, `<plugin-id>.*`
--             reserved per plugin install, bare names default for embedder.
-- version   — every approve creates a new version; old versions retained for
--             audit / in-flight workflow snapshotting.
-- status    — draft -> approved -> archived | revoked. Approved is IMMUTABLE
--             (no PATCH); to edit, create version+1 in draft and approve.
-- effective_from/to — time window during which this version applies. The
--             selection algorithm prefers tenant-specific over site-scope and
--             higher min_amount over lower (more-specific match wins).
-- condition_expr — opt-in filter-expression for fine-grained matrix selection
--             beyond amount range. NULL = always applicable. Not in Slice 0
--             fast-path eval, but column is reserved for forward-compat.

CREATE TABLE _doa_matrices (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID         NULL,                                  -- NULL = site-scope
    key                 TEXT         NOT NULL,                              -- e.g. "articles.publish"
    version             INTEGER      NOT NULL DEFAULT 1,
    name                TEXT         NOT NULL,
    description         TEXT         NULL,

    status              TEXT         NOT NULL DEFAULT 'draft',
    revoked_reason      TEXT         NULL,
    approved_by         UUID         NULL,
    approved_at         TIMESTAMPTZ  NULL,

    effective_from      TIMESTAMPTZ  NULL,
    effective_to        TIMESTAMPTZ  NULL,

    -- Selection criteria (combined in WHERE — see Slice 0 matrix selector).
    min_amount          BIGINT       NULL,                                  -- minor units (cents)
    max_amount          BIGINT       NULL,                                  -- NULL = open-ended
    currency            TEXT         NULL,                                  -- matched if not null
    condition_expr      TEXT         NULL,                                  -- opt-in filter expression

    on_final_escalation TEXT         NOT NULL DEFAULT 'expire',             -- expire | reject

    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_by          UUID         NULL,
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT _doa_matrices_status_chk
        CHECK (status IN ('draft','approved','archived','revoked')),
    CONSTRAINT _doa_matrices_final_escalation_chk
        CHECK (on_final_escalation IN ('expire','reject')),
    CONSTRAINT _doa_matrices_window_chk
        CHECK (effective_to IS NULL OR effective_from IS NULL OR effective_from < effective_to),
    CONSTRAINT _doa_matrices_amount_chk
        CHECK (min_amount IS NULL OR max_amount IS NULL OR min_amount <= max_amount),
    CONSTRAINT _doa_matrices_revoked_reason_chk
        CHECK (status != 'revoked' OR revoked_reason IS NOT NULL)
);

-- Unique per (tenant, key, version). COALESCE collapses NULL tenant_id to ''
-- so site-scope matrices can't collide with each other on the same key+version.
CREATE UNIQUE INDEX uniq__doa_matrices_key_version
    ON _doa_matrices (COALESCE(tenant_id::text, ''), key, version);

-- Hot-path: matrix selection on every gated write. Filters on status+window.
CREATE INDEX idx__doa_matrices_selection
    ON _doa_matrices (key, status, effective_from, effective_to)
    WHERE status = 'approved';


-- ============================================================================
-- 2. _doa_matrix_levels — per-level approval configuration.
-- ============================================================================
--
-- mode       — any (one qualifying approver enough) | all (every approver in
--              set must sign) | threshold (>= min_approvals approveds needed).
-- escalation_hours — nullable; NULL = no auto-escalation. Reaper job (Slice 1+)
--              promotes workflow to next level if no decision in this window.

CREATE TABLE _doa_matrix_levels (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    matrix_id         UUID         NOT NULL REFERENCES _doa_matrices(id) ON DELETE CASCADE,
    level_n           INTEGER      NOT NULL,
    name              TEXT         NOT NULL,

    mode              TEXT         NOT NULL,                              -- any | all | threshold
    min_approvals     INTEGER      NULL,                                  -- required when mode='threshold'

    escalation_hours  INTEGER      NULL,                                  -- NULL = no escalation

    CONSTRAINT _doa_matrix_levels_mode_chk
        CHECK (mode IN ('any','all','threshold')),
    CONSTRAINT _doa_matrix_levels_threshold_chk
        CHECK (mode != 'threshold' OR (min_approvals IS NOT NULL AND min_approvals > 0)),
    CONSTRAINT _doa_matrix_levels_level_n_chk
        CHECK (level_n > 0),
    CONSTRAINT _doa_matrix_levels_unique_level
        UNIQUE (matrix_id, level_n)
);


-- ============================================================================
-- 3. _doa_matrix_approvers — who's qualified per level.
-- ============================================================================
--
-- approver_type — role | user. Slice 0 stops here.
--                 v2.x will add position | department_head once org-chart
--                 primitive lands (see docs/26-org-structure-audit.md).
-- approver_ref  — role key (for type=role) OR user UUID as text (type=user).
-- auto_resolve  — placeholder for department_head ("requester's own dept");
--                 unused in Slice 0 (always FALSE for role/user).

CREATE TABLE _doa_matrix_approvers (
    id             UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    level_id       UUID    NOT NULL REFERENCES _doa_matrix_levels(id) ON DELETE CASCADE,
    approver_type  TEXT    NOT NULL,
    approver_ref   TEXT    NOT NULL,
    auto_resolve   BOOLEAN NOT NULL DEFAULT FALSE,

    CONSTRAINT _doa_matrix_approvers_type_chk
        CHECK (approver_type IN ('role','user','position','department_head'))
);
-- Slice 0 enforcement constraint — application-level rejects position/
-- department_head until v2.x. Constraint allows the values in the column for
-- forward compatibility; admin REST handlers reject them.

CREATE INDEX idx__doa_matrix_approvers_level
    ON _doa_matrix_approvers (level_id);


-- ============================================================================
-- 4. _doa_workflows — runtime workflow instances.
-- ============================================================================
--
-- Created by gate middleware at request time. Snapshotted matrix_version
-- pinned at creation — matrix archive/revoke does NOT affect in-flight
-- workflows.
-- requested_diff  — normative content of the sanction. Consume validation
--                   field-by-field checks `ProtectedFields` against this diff
--                   AFTER all BeforeUpdate hooks ran, BEFORE commit.

CREATE TABLE _doa_workflows (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID         NULL,

    matrix_id         UUID         NOT NULL REFERENCES _doa_matrices(id),
    matrix_version    INTEGER      NOT NULL,                              -- snapshot for audit

    collection        TEXT         NOT NULL,
    record_id         UUID         NOT NULL,
    action_key        TEXT         NOT NULL,
    requested_diff    JSONB        NOT NULL,
    amount            BIGINT       NULL,
    currency          TEXT         NULL,
    initiator_id      UUID         NOT NULL,
    notes             TEXT         NULL,

    status            TEXT         NOT NULL DEFAULT 'running',             -- running | completed | rejected | cancelled | expired
    current_level     INTEGER      NULL,                                   -- valid only when status='running'

    terminal_reason   TEXT         NULL,
    terminal_by       UUID         NULL,
    terminal_at       TIMESTAMPTZ  NULL,

    consumed_at       TIMESTAMPTZ  NULL,

    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ  NOT NULL,

    CONSTRAINT _doa_workflows_status_chk
        CHECK (status IN ('running','completed','rejected','cancelled','expired')),
    CONSTRAINT _doa_workflows_current_level_chk
        CHECK ((status = 'running' AND current_level IS NOT NULL)
               OR (status != 'running' AND (current_level IS NULL OR current_level IS NOT NULL))),
    CONSTRAINT _doa_workflows_terminal_chk
        CHECK ((status IN ('rejected','cancelled','expired') AND terminal_at IS NOT NULL)
               OR status NOT IN ('rejected','cancelled','expired'))
);

-- One running workflow per (collection, record, action) at a time.
-- Terminal states (completed/rejected/cancelled/expired) don't block.
CREATE UNIQUE INDEX uniq__doa_workflows_active
    ON _doa_workflows (collection, record_id, action_key)
    WHERE status = 'running';

CREATE INDEX idx__doa_workflows_initiator
    ON _doa_workflows (initiator_id, status, created_at DESC);

-- Inbox query: "workflows where current user is qualified approver".
CREATE INDEX idx__doa_workflows_inbox
    ON _doa_workflows (tenant_id, status, current_level, expires_at)
    WHERE status = 'running';


-- ============================================================================
-- 5. _doa_workflow_decisions — per-approver decisions per level.
-- ============================================================================
--
-- UNIQUE (workflow_id, level_n, approver_id) — one decision per approver per
-- level. If reject is desired after recycle (NOT in Slice 0 — reject is
-- terminal), would require clearing prior decisions.
--
-- approver_position / org_path / acting — forward-compat columns for v2.x
-- org-aware approvers. NULL in Slice 0; populated when position/dept_head
-- approver types ship alongside org-chart primitive.

CREATE TABLE _doa_workflow_decisions (
    id                   UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id          UUID         NOT NULL REFERENCES _doa_workflows(id) ON DELETE CASCADE,
    level_n              INTEGER      NOT NULL,
    approver_id          UUID         NOT NULL,
    approver_role        TEXT         NULL,                                 -- snapshot of role at sign time
    approver_resolution  TEXT         NULL,                                 -- "role:editor" | "user:abc" | "delegate_of:xyz"

    -- Forward-compat v2.x (see docs/26-org-structure-audit.md).
    approver_position    TEXT         NULL,                                 -- v2.x snapshot of position key
    approver_org_path    TEXT         NULL,                                 -- v2.x snapshot of department path
    approver_acting      BOOLEAN      NULL,                                 -- v2.x: signed via acting assignment?

    decision             TEXT         NOT NULL,                             -- approved | rejected
    memo                 TEXT         NULL,                                 -- approval memo
    decided_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT _doa_workflow_decisions_decision_chk
        CHECK (decision IN ('approved','rejected')),
    CONSTRAINT _doa_workflow_decisions_unique_approver_per_level
        UNIQUE (workflow_id, level_n, approver_id)
);

CREATE INDEX idx__doa_workflow_decisions_workflow
    ON _doa_workflow_decisions (workflow_id);
