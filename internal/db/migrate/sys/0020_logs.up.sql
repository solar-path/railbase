-- v1.7.6 — Logs as records (PB feature).
--
-- Operators want to browse application logs through the admin UI
-- without SSHing into the host. Mirrors PB's "logs collection"
-- model: every slog.Record above the configured threshold gets a
-- row in `_logs`. Read access via /api/_logs (admin-only).
--
-- Storage shape: structured columns for the common filter axes
-- (level / created / request_id), plus a JSONB blob for the rest
-- of slog.Attrs. Indexes cover the three most-likely admin queries:
-- recent (by created DESC), by-level (errors / warnings), by request.
--
-- Retention: rows past `logs.retention_days` (default 14d) swept by
-- the `cleanup_logs` cron builtin. Operators can tune retention up
-- (compliance) or down (disk pressure).
--
-- The writer batches inserts via a background flusher so the hot
-- path (slog.Handle) never hits the DB synchronously. Drops on
-- overflow if the buffer fills — the application doesn't deadlock
-- waiting for the persistence layer. Operators see dropped-count
-- via Stats.

CREATE TABLE _logs (
    id          UUID PRIMARY KEY,
    level       TEXT NOT NULL,    -- "debug" | "info" | "warn" | "error"
    message     TEXT NOT NULL,
    attrs       JSONB NOT NULL DEFAULT '{}'::jsonb,
    source      TEXT,             -- source file:line for debug; NULL otherwise
    request_id  TEXT,              -- correlation id from middleware
    user_id     UUID,              -- principal at log time, NULL for system events
    created     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hot-path query: "most recent N entries" (admin landing tab).
CREATE INDEX idx_logs_created ON _logs (created DESC);

-- Errors/warnings filter — bounded result set with partial-style
-- index restricted to the levels admins actually filter on.
CREATE INDEX idx_logs_level_created ON _logs (level, created DESC);

-- Request correlation: "show all logs for this request_id" jumps
-- from one error log to the surrounding context. Partial index
-- keeps it small (most logs lack request_id — system events).
CREATE INDEX idx_logs_request ON _logs (request_id, created DESC)
    WHERE request_id IS NOT NULL;
