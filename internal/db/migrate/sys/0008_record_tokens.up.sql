-- v1.1 — record tokens.
--
-- Short-lived single-use tokens for record-scoped operations:
-- email verification, password reset, email change confirmation,
-- magic-link signin, file-access signing.
--
-- Same hash-then-store pattern as _sessions:
--   raw token (32 random bytes, base64url) → HMAC-SHA-256(token, master_key)
--   → stored as token_hash bytea. Caller never persists raw; user
--   receives raw via email link, server hashes-and-looks-up.
--
-- Single-use enforced via consumed_at column: the consume call wraps
-- a SELECT-FOR-UPDATE + UPDATE in a transaction so two parallel
-- consume attempts can't both succeed.
--
-- collection_name + record_id together uniquely identify the
-- principal the token belongs to. We don't FK to the underlying
-- collection table — that table is user-defined and may not exist
-- when the migration runs.

CREATE TABLE _record_tokens (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash      BYTEA        NOT NULL,
    purpose         TEXT         NOT NULL,
    collection_name TEXT         NOT NULL,
    record_id       UUID         NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ  NOT NULL,
    consumed_at     TIMESTAMPTZ  NULL,
    payload         JSONB        NULL
);

-- Lookup is by hash; uniqueness prevents accidental duplicate inserts.
CREATE UNIQUE INDEX _record_tokens_hash_idx ON _record_tokens (token_hash);

-- Per-record listing (admin UI: «show pending tokens for user X»).
CREATE INDEX _record_tokens_owner_idx
    ON _record_tokens (collection_name, record_id, purpose);

-- Expiry sweep — cleanup job will scan unrevoked unexpired rows
-- and hard-delete the rest.
CREATE INDEX _record_tokens_expiry_idx
    ON _record_tokens (expires_at)
    WHERE consumed_at IS NULL;
