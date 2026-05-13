-- v1.7.51 — SCIM 2.0 (System for Cross-domain Identity Management).
--
-- SCIM is an inbound provisioning protocol — an external IdP (Okta /
-- Azure AD / OneLogin) POSTs `Users` and `Groups` to Railbase so that
-- when HR de-provisions an employee, their Railbase account follows
-- the same lifecycle without a human operator clicking through the
-- admin UI.
--
-- This migration ships THREE new tables:
--
--   _scim_tokens         — bearer credentials the IdP authenticates
--                          with on every /scim/v2/* request. Per-token
--                          rotation; HMAC-SHA-256 hash (master key)
--                          identical pattern to v1.7.3 API tokens. The
--                          IdP-provided "client" gets ONE token; the
--                          operator can rotate / revoke without
--                          disturbing the rest of the install.
--
--   _scim_groups         — directory-style groups the IdP creates +
--                          syncs. Distinct from `_roles` (RBAC
--                          permissions) — a SCIM Group represents an
--                          AD security group; the operator maps each
--                          SCIM Group to one or more RBAC roles via
--                          `_scim_group_role_map`. This lets ops set
--                          up "Finance" in Azure AD, map it once to
--                          {site_admin, finance_viewer}, and every
--                          new Finance-team member gets both roles
--                          on first SCIM sync.
--
--   _scim_group_members  — many-to-many (group, user) — synced by the
--                          IdP via PATCH operations on the group
--                          resource. Pure join table; no SCIM
--                          metadata on it.
--
-- We deliberately do NOT add a _scim_users table. The provisioned
-- users live in `users` (or whichever auth-collection the operator
-- configured for SCIM in the wizard); SCIM-managed rows are
-- distinguished by:
--
--   * `external_id`   — the IdP's stable id for the user (sub or oid)
--   * `scim_managed`  — boolean, TRUE when SCIM created/updated it
--
-- Both columns are added to ALL existing auth-collections via a
-- companion migration that runs per-collection. v1.7.51 only adds
-- them to the `users` table by default; collections created via
-- `schema.AuthCollection(...)` after this point pick them up via the
-- builder.

CREATE TABLE _scim_tokens (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT         NOT NULL,           -- operator-readable label e.g. "okta-prod"
    token_hash      BYTEA        NOT NULL,           -- HMAC-SHA-256(token, masterKey)
    collection      TEXT         NOT NULL,           -- target auth-collection
    -- Optional fine-grained scopes. Empty array = full SCIM access
    -- (Users + Groups, read + write). Future: ["users:read", ...].
    scopes          TEXT[]       NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_by      UUID,                            -- admin who minted; NULL = system
    last_used_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,                     -- NULL = no expiry; default = 1y
    revoked_at      TIMESTAMPTZ
);

CREATE UNIQUE INDEX _scim_tokens_hash_idx ON _scim_tokens (token_hash);
CREATE INDEX _scim_tokens_alive_idx ON _scim_tokens (collection, revoked_at)
    WHERE revoked_at IS NULL;

CREATE TABLE _scim_groups (
    -- SCIM `id` is the canonical resource id — we use UUIDs.
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- SCIM `externalId` — the IdP's stable identifier. Optional per
    -- spec; some IdPs (Okta) always set it.
    external_id     TEXT,
    -- SCIM `displayName` — human-readable name. Required by the spec.
    display_name    TEXT         NOT NULL,
    -- Which auth-collection's users this group's members come from.
    -- Multi-collection installs may have multiple SCIM Groups across
    -- collections (uncommon but valid).
    collection      TEXT         NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX _scim_groups_external_idx
    ON _scim_groups (collection, external_id)
    WHERE external_id IS NOT NULL;

CREATE INDEX _scim_groups_display_idx
    ON _scim_groups (collection, lower(display_name));

CREATE TABLE _scim_group_members (
    group_id        UUID         NOT NULL REFERENCES _scim_groups(id) ON DELETE CASCADE,
    user_id         UUID         NOT NULL,
    user_collection TEXT         NOT NULL,
    added_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX _scim_group_members_user_idx
    ON _scim_group_members (user_collection, user_id);

-- SCIM group → RBAC role mapping. One SCIM Group can map to multiple
-- RBAC roles; one RBAC role can be mapped to from multiple SCIM
-- Groups. On every SCIM PATCH that updates a user's group memberships,
-- we resolve the union of all mapped roles for the user's current
-- groups and reconcile their `_user_roles` rows accordingly.
CREATE TABLE _scim_group_role_map (
    scim_group_id   UUID         NOT NULL REFERENCES _scim_groups(id) ON DELETE CASCADE,
    role_id         UUID         NOT NULL REFERENCES _roles(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (scim_group_id, role_id)
);

-- Add SCIM-tracking columns to the default `users` collection IFF the
-- table already exists. Sys-migrations run BEFORE user-defined
-- auth-collections (the `users` table is created by the application's
-- schema builder via the operator's `schema.AuthCollection(...)`
-- call, which runs AFTER `Apply()` in app.go). For installs that
-- never define `users` we silently skip; for installs that do, the
-- application boot path applies these columns through the schema
-- builder's SCIM-aware DDL generator. Both columns are nullable +
-- indexed so existing rows are untouched + external_id lookups stay
-- O(log n).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'users' AND schemaname = current_schema()) THEN
        EXECUTE 'ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id TEXT';
        EXECUTE 'ALTER TABLE users ADD COLUMN IF NOT EXISTS scim_managed BOOLEAN NOT NULL DEFAULT FALSE';
        EXECUTE 'CREATE UNIQUE INDEX IF NOT EXISTS users_external_id_idx ON users (external_id) WHERE external_id IS NOT NULL';
    END IF;
END $$;
