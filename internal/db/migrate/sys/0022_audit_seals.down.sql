-- v1.x — undo audit-log Ed25519 sealing table.
DROP INDEX IF EXISTS _audit_seals_range_end_idx;
DROP TABLE IF EXISTS _audit_seals;
