-- v1.7.13 — audit-log retention archive flag.
--
-- docs/17 #93. Adds an `archived_at` column to `_audit_log` so the
-- `cleanup_audit_archive` cron builtin can mark rows past
-- `audit.retention_days` without deleting them. The chain is
-- preserved (verifier walks every row regardless of archived_at) and
-- admin UI / API listings default to `WHERE archived_at IS NULL`.
--
-- Why archive vs delete: the hash chain links every row's `hash` to
-- the previous row's `prev_hash`. Deleting old rows would break
-- `railbase audit verify` for any sequence touching the deleted slice.
-- Operators wanting a true purge (e.g. legal-hold-driven minimisation)
-- need a separate "snapshot-and-truncate" tool that lives outside
-- the chain — out of scope for v1.7.

ALTER TABLE _audit_log
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ NULL;

-- Partial index on the hot listing path: admin UI defaults to
-- `WHERE archived_at IS NULL ORDER BY at DESC`. The full-index
-- `_audit_log_at_idx` is now slower than this one for typical
-- listings but still useful for `?include_archived=true`.
CREATE INDEX IF NOT EXISTS _audit_log_active_at_idx
    ON _audit_log (at DESC)
    WHERE archived_at IS NULL;
