-- v1.7.51 — SCIM 2.0 rollback.
ALTER TABLE users DROP COLUMN IF EXISTS scim_managed;
ALTER TABLE users DROP COLUMN IF EXISTS external_id;
DROP INDEX IF EXISTS users_external_id_idx;

DROP TABLE IF EXISTS _scim_group_role_map;
DROP TABLE IF EXISTS _scim_group_members;
DROP TABLE IF EXISTS _scim_groups;
DROP TABLE IF EXISTS _scim_tokens;
