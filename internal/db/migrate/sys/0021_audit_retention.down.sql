-- v1.7.13 — undo audit-log retention archive flag.
DROP INDEX IF EXISTS _audit_log_active_at_idx;
ALTER TABLE _audit_log DROP COLUMN IF EXISTS archived_at;
