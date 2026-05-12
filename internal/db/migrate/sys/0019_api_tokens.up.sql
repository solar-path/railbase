-- v1.7.3 — API tokens for service-to-service authentication.
--
-- Distinct from _sessions because the lifecycle is different:
--   - Sessions are short-lived, sliding-window, browser-issued.
--   - API tokens are long-lived (30+ days), explicit-revoke, machine-
--     issued (CLI or admin UI). No password flow, no rotation cookie.
--
-- Tokens are bearer credentials displayed ONCE at creation time. The
-- table stores only `token_hash` (HMAC-SHA-256 of the opaque token,
-- keyed on the master secret) — leaked DB dumps don't leak tokens.
--
-- `rotated_from` lets operators stage a rotation: create a successor
-- token, distribute it, then revoke the predecessor. While both are
-- alive, the predecessor responds with a `Deprecation: true` header
-- to nudge clients to migrate (v1.x polish — deprecation header
-- wiring deferred; the column exists so the data is ready when the
-- handler ships).

CREATE TABLE _api_tokens (
    id              UUID PRIMARY KEY,
    name            TEXT NOT NULL,
    token_hash      BYTEA NOT NULL UNIQUE,
    owner_id        UUID NOT NULL,
    owner_collection TEXT NOT NULL,    -- which auth-collection the owner belongs to
    scopes          TEXT[] NOT NULL DEFAULT '{}',
    expires_at      TIMESTAMPTZ,        -- NULL = never expires
    last_used_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ,
    rotated_from    UUID REFERENCES _api_tokens(id) ON DELETE SET NULL
);

-- Token lookup is the hot path: every API request with a Bearer
-- credential hits this. UNIQUE on token_hash already creates an
-- index — explicit secondary indexes below cover the listing /
-- audit paths.
CREATE INDEX idx_api_tokens_owner ON _api_tokens (owner_collection, owner_id);
CREATE INDEX idx_api_tokens_active_lookup
    ON _api_tokens (token_hash)
    WHERE revoked_at IS NULL;
