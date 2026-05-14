-- v0.9 — runtime (admin-UI-created) collections.
--
-- Railbase's schema is primarily code-defined: collections declared in
-- the Go DSL register at process init() and are the source of truth
-- for SDK codegen + migration history. This table is the persistence
-- layer for the OTHER kind of collection — ones an operator creates
-- live from the admin UI, which have no Go source file.
--
-- One row per admin-created collection. `spec` is the full serialised
-- builder.CollectionSpec (same JSON shape as a _schema_snapshots
-- entry's per-collection element). At boot, after system migrations,
-- the app hydrates the in-memory registry from these rows so the
-- collections survive a restart.
--
-- The actual data table (e.g. `posts`) is created by the same gen.*
-- DDL the migration CLI uses — this table only stores the spec, not
-- the rows. Dropping a row here does NOT drop the data table; the
-- admin DELETE path runs DROP TABLE in the same transaction.
--
-- Code-defined collections are NEVER written here: they own their
-- definition in source. A name present in the registry but absent
-- from this table is read-only in the admin UI.

CREATE TABLE _admin_collections (
    name       TEXT        PRIMARY KEY,
    spec       JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
