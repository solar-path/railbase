-- v1.1.1 — external (OAuth2 / OIDC) auth provider links.
--
-- One row per (provider, provider_user_id) pair. A single user record
-- can have multiple links (Google + GitHub + Apple), but a given
-- provider identity can be claimed by only one user — UNIQUE(provider,
-- provider_user_id). The owner side (collection_name + record_id) also
-- has UNIQUE per-provider so a user can't accidentally end up with two
-- Google links pointing at different Google accounts.
--
-- We deliberately do NOT FK to the underlying auth-collection table:
--
--   - The collection table is user-defined and may not exist at
--     migration time.
--   - Multi-tenant deployments may stage a single _external_auths row
--     against any of several auth collections (users, customers, ...).
--
-- Cleanup of orphaned rows on user-delete is handled by the auth
-- delete path (or a future scheduled GC job).
--
-- Storage notes:
--   - provider:         lower-case literal — "google", "github", "apple", ...
--   - provider_user_id: the provider's stable subject ID (Google: sub
--                       claim; GitHub: "id"; Apple: sub claim). Stored as
--                       TEXT because providers use mixed formats
--                       (integers, hex, opaque strings).
--   - email:            cached at link time for admin UI; the canonical
--                       email lives on the user row.
--   - raw_user_info:    JSONB blob from the provider's userinfo endpoint,
--                       useful for debugging unfamiliar providers; admins
--                       see it in the UI to confirm what came back.

CREATE TABLE _external_auths (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_name   TEXT         NOT NULL,
    record_id         UUID         NOT NULL,
    provider          TEXT         NOT NULL,
    provider_user_id  TEXT         NOT NULL,
    email             TEXT         NULL,
    raw_user_info     JSONB        NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Lookup #1: "find user from provider callback".
-- Returns at most one row by design.
CREATE UNIQUE INDEX _external_auths_provider_uid_idx
    ON _external_auths (provider, provider_user_id);

-- Lookup #2: "is this user already linked to provider X?"
-- Prevents accidental double-link from re-running the start flow.
CREATE UNIQUE INDEX _external_auths_owner_idx
    ON _external_auths (collection_name, record_id, provider);

-- Lookup #3: admin UI "show all links for user X".
CREATE INDEX _external_auths_record_idx
    ON _external_auths (collection_name, record_id);
