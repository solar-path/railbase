-- v0.8 — embedded admin UI bearer-token sessions.
--
-- Why a separate table from `_sessions`: admins sit in `_admins`
-- (NOT in any auth collection), so the `_sessions.collection_name`
-- discriminator doesn't fit. Mirroring the layout keeps the lookup
-- code identical, just typed against admin_id instead of user_id.
--
-- Sliding window: 8h idle (last_active_at), hard cap 30d (expires_at).
-- Same parameters as application user sessions.

CREATE TABLE _admin_sessions (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id        UUID         NOT NULL REFERENCES _admins(id) ON DELETE CASCADE,
    token_hash      BYTEA        NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_active_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ  NOT NULL,
    revoked_at      TIMESTAMPTZ  NULL,
    ip              TEXT         NULL,
    user_agent      TEXT         NULL
);

CREATE UNIQUE INDEX _admin_sessions_token_hash_idx ON _admin_sessions (token_hash);
CREATE INDEX _admin_sessions_owner_idx ON _admin_sessions (admin_id);
CREATE INDEX _admin_sessions_expiry_idx ON _admin_sessions (expires_at) WHERE revoked_at IS NULL;
