-- v1.4.0 — Jobs queue + cron schedules.
--
-- Two tables:
--   _jobs       — one row per work unit. Claimed by workers via
--                 SELECT … FOR UPDATE SKIP LOCKED.
--   _cron       — persisted cron schedules; the cron loop materialises
--                 due rows into `_jobs` on every tick.
--
-- Design notes:
--   - status: enum-as-text to keep migrations cheap. Values:
--     pending / running / completed / failed / cancelled.
--   - kind: opaque TEXT key into the handler registry. Workers refuse
--     to process kinds with no registered handler (mark fail).
--   - payload: JSONB blob, schema is handler-specific. Hooks/CLI never
--     introspect it — only the handler does.
--   - attempts/max_attempts: classic retry budget. Workers compute
--     next_attempt_at on failure (exp backoff in code, not SQL).
--   - locked_by / locked_until: cooperative lock so a crashed worker
--     can be detected via lock-expired sweep (recovery in v1.4.x).
--   - run_after: when the row becomes eligible. NOW() for "ASAP";
--     future time for delayed jobs / cron materialisations.
--   - Indexes:
--       (status, run_after) WHERE status='pending'  — claim hot path
--       (status, scheduled_for) lets cron loop find due schedules

CREATE TABLE _jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    queue           TEXT        NOT NULL DEFAULT 'default',
    kind            TEXT        NOT NULL,
    payload         JSONB       NOT NULL DEFAULT '{}'::jsonb,

    status          TEXT        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending','running','completed','failed','cancelled')),

    attempts        INT         NOT NULL DEFAULT 0,
    max_attempts    INT         NOT NULL DEFAULT 5,
    last_error      TEXT,

    -- Earliest time this job may be claimed.
    run_after       TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Cooperative lock — set on claim, cleared on complete/fail.
    locked_by       TEXT,
    locked_until    TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,

    -- Optional origin: filled when the row was materialised by cron.
    cron_id         UUID
);

-- Hot path: claim queries scan a tiny partial index of pending rows
-- whose run_after has elapsed, ordered by run_after to be FIFO-ish.
CREATE INDEX idx__jobs_pending_run_after
    ON _jobs (run_after) WHERE status = 'pending';

-- Status + created_at supports the admin "recent jobs" view.
CREATE INDEX idx__jobs_status_created
    ON _jobs (status, created_at DESC);

-- Cron schedules.
CREATE TABLE _cron (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Human-readable name; UNIQUE so operators can refer to it
    -- without juggling UUIDs.
    name            TEXT        NOT NULL UNIQUE,

    -- 5-field crontab expression (m h dom mon dow). Validated at
    -- write time by the cron parser — bad expr → 400 from CLI/admin.
    expression      TEXT        NOT NULL,

    -- Kind + payload the cron should enqueue on each tick. The
    -- handler must already be registered or the materialised job
    -- will fail; this is intentional ("can't schedule what you don't
    -- have a worker for").
    kind            TEXT        NOT NULL,
    payload         JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- Enabled toggle so operators can pause without delete.
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,

    last_run_at     TIMESTAMPTZ,
    next_run_at     TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx__cron_enabled_next_run
    ON _cron (next_run_at) WHERE enabled = TRUE;
