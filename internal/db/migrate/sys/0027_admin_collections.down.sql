-- Reverses 0027_admin_collections.up.sql.
--
-- Drops only the spec-tracking table — the per-collection data tables
-- created by the admin UI are NOT touched here (they're not owned by
-- this migration). An operator rolling this back is expected to have
-- already dropped or hand-migrated those tables.
DROP TABLE IF EXISTS _admin_collections;
