-- v0.3.2 — auth core.
--
-- One sessions table for every auth collection, discriminated by
-- collection_name. token_hash is HMAC-SHA-256(token, secret_key) so
-- a database dump cannot forge sessions without also exfiltrating
-- the master secret from pb_data/.secret.
--
-- Soft revocation (revoked_at IS NOT NULL) keeps the row for audit;
-- a v1 cleanup job will hard-delete rows older than the retention
-- window. Until that job ships, manual cleanup via SQL is fine — the
-- (collection_name, user_id) and expires_at indexes make it cheap.

CREATE TABLE _sessions (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_name TEXT         NOT NULL,
    user_id         UUID         NOT NULL,
    token_hash      BYTEA        NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_active_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ  NOT NULL,
    revoked_at      TIMESTAMPTZ  NULL,
    ip              TEXT         NULL,
    user_agent      TEXT         NULL
);

-- token_hash is the lookup key — must be unique even across collections.
CREATE UNIQUE INDEX _sessions_token_hash_idx ON _sessions (token_hash);

-- "all sessions for user X in collection Y" is the second-most-common
-- query (sign out other devices, list active sessions).
CREATE INDEX _sessions_owner_idx ON _sessions (collection_name, user_id);

-- Cleanup job will scan by expires_at; partial index keeps it small.
CREATE INDEX _sessions_expiry_idx ON _sessions (expires_at) WHERE revoked_at IS NULL;
