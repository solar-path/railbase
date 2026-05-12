-- v1.3.1 — Files (inline attachments).
--
-- One row per uploaded blob. Records reference uploads by storage_key
-- through their file/files columns; this table carries the metadata
-- (mime, size, sha256, original_filename) that the record column
-- alone can't.
--
-- Scope (v1.3.1):
--   - Inline file fields on collections (avatar on user, cover on post).
--   - FS driver only — S3 / GCS lands in v1.3.x as a driver swap (no
--     schema change).
--
-- Deliberately deferred to v1.3.2+:
--   - _documents / _document_versions (logical document with version
--     history + polymorphic owner) — separate table family.
--   - Thumbnails (cached pre-rendered images) — addressed by deriving
--     key from (storage_key, variant); no schema column needed in v1.3.1.
--   - SHA-256 dedup across records — column exists; UNIQUE constraint
--     deferred until quotas land (otherwise re-uploads cross-record
--     would collide on the same blob).

CREATE TABLE _files (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Where the blob is referenced. Once a file row is created, the
    -- (collection, record_id, field) triple binds it to the column on
    -- the user table. (collection, record_id) might point to a row
    -- that was later deleted — orphan reaping is a v1.4 job.
    collection      TEXT        NOT NULL,
    record_id       UUID        NOT NULL,
    field           TEXT        NOT NULL,

    -- Optional ownership for RBAC / quotas. owner_user is the auth
    -- principal who uploaded the file. tenant_id mirrors the owning
    -- record's tenant when the collection is tenant-scoped.
    owner_user      UUID,
    tenant_id       UUID,

    -- Original-filename + content metadata. The storage_key is the
    -- driver-specific path / object key. For FSDriver: relative path
    -- from the storage root, e.g. "ab/abcdef…hex/file.png".
    filename        TEXT        NOT NULL,
    mime            TEXT        NOT NULL,
    size            BIGINT      NOT NULL,
    sha256          BYTEA       NOT NULL,
    storage_key     TEXT        NOT NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Lookup by record+field for serving + cleanup.
CREATE INDEX idx__files_record_field
    ON _files (collection, record_id, field);

-- Lookup by sha256 enables content-addressed reuse — when v1.3.2 adds
-- dedup, the upload handler probes this index before writing to disk.
CREATE INDEX idx__files_sha256
    ON _files (sha256);

-- Tenant scan for quota aggregation (v1.3.2).
CREATE INDEX idx__files_tenant
    ON _files (tenant_id) WHERE tenant_id IS NOT NULL;
