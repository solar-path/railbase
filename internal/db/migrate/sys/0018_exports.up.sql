-- v1.6.5 async export tracking. Each row is one async export request:
-- the matching `_jobs` row carries the worker-execution state, this
-- table carries the user-facing metadata (format, status, file path
-- when complete, error string when failed). The `id` is identical to
-- the `_jobs.id` so the API can use a single lookup.
--
-- Lifecycle: INSERT at POST /api/exports time (status='pending') →
-- UPDATE to 'running' on claim → UPDATE to 'completed' with file_path
-- + size + row_count, or to 'failed' with error. `expires_at` is set
-- on completion so a cleanup cron can sweep aged rows + files.

CREATE TABLE _exports (
    id           UUID PRIMARY KEY,

    -- 'xlsx' or 'pdf'. CHECK keeps the kind enum tight; new formats
    -- need a migration alongside the handler registration.
    format       TEXT NOT NULL
                  CHECK (format IN ('xlsx', 'pdf')),

    collection   TEXT NOT NULL,

    status       TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),

    row_count    INTEGER,
    file_path    TEXT,
    file_size    BIGINT,

    -- Populated on failure. NULL on completed/pending/running rows.
    error        TEXT,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ
);

-- The status-filtered "recent" view used by the admin UI + GET /list
-- (deferred to a follow-up).
CREATE INDEX idx__exports_status_created
    ON _exports (status, created_at DESC);
