-- v3.x — undo unified audit log split.

-- Drop _audit_seals additions first (indexes + columns + constraint).
ALTER TABLE _audit_seals DROP CONSTRAINT IF EXISTS _audit_seals_tenant_target_chk;
DROP INDEX IF EXISTS _audit_seals_tenant_idx;
DROP INDEX IF EXISTS _audit_seals_target_idx;
ALTER TABLE _audit_seals DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE _audit_seals DROP COLUMN IF EXISTS target;

-- Drop tenant table (CASCADE drops the RLS policy + partitions).
DROP TABLE IF EXISTS _audit_log_tenant CASCADE;

-- Drop site table.
DROP TABLE IF EXISTS _audit_log_site CASCADE;

-- Drop enums last — TABLE drops above reference them.
DROP TYPE IF EXISTS audit_outcome;
DROP TYPE IF EXISTS audit_actor_type;
