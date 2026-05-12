-- 0002_schema_snapshots.up.sql
--
-- Snapshot of the user-declared schema at every applied migration.
-- The schema generator reads the latest row, diffs the in-memory Go
-- DSL against it, and writes a new row when `migrate up` succeeds.
--
-- migration_version FKs to _migrations: dropping a migration row
-- (rare; only via manual cleanup) cleanly drops its snapshot too.
CREATE TABLE _schema_snapshots (
    migration_version BIGINT       PRIMARY KEY REFERENCES _migrations(version) ON DELETE CASCADE,
    snapshot          JSONB        NOT NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- One row per applied migration; ordered queries take the latest by
-- migration_version DESC. Btree on PK is enough; no extra index.
