-- v0.4.3 — Tenants + per-tenant membership.
--
-- Two-table workspace model (mirrors rail's `company` + `company_members`):
--
--   _tenants         is the workspace identity. Soft-deletable so an
--                    accidentally-dropped row is recoverable until the
--                    operator wipes via SQL.
--
--   _tenant_members  joins (tenant, auth user) with a role. PK is
--                    (tenant_id, collection_name, user_id) so the
--                    same user can have memberships in multiple
--                    tenants AND the same auth collection's user_id
--                    space doesn't collide with a future second auth
--                    collection.
--
-- The model is INDEPENDENT of `tenant_id` columns on user collections.
-- The existing `.Tenant()` modifier + RLS continues to scope CRUD
-- against a user collection by an X-Tenant header; this table records
-- the metadata + membership that backs that header (who is allowed to
-- send which tenant id).

CREATE TABLE _tenants (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT         NOT NULL,
    slug        TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- Soft-delete sentinel. List queries default to `deleted_at IS NULL`;
    -- the audit / migration path can still recover the row by ID.
    deleted_at  TIMESTAMPTZ  NULL,
    CONSTRAINT _tenants_slug_format CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$')
);

-- Slug is the URL-safe identifier (`/t/<slug>/dashboard`). Unique among
-- live rows; deleted rows are excluded so a slug can be reused after
-- the original tenant is purged.
CREATE UNIQUE INDEX _tenants_slug_idx
    ON _tenants (slug) WHERE deleted_at IS NULL;

-- Membership table. `role` is a string so operators can grow custom
-- roles later (`'custom:42'`) without an enum migration; the
-- well-known values are `'owner'`, `'admin'`, `'member'`.
CREATE TABLE _tenant_members (
    tenant_id        UUID         NOT NULL REFERENCES _tenants(id) ON DELETE CASCADE,
    collection_name  TEXT         NOT NULL,
    user_id          UUID         NOT NULL,
    role             TEXT         NOT NULL DEFAULT 'member',
    -- Pending invites carry the invitee's email until they accept; on
    -- accept the row is upserted with the real user_id + invited_email
    -- cleared. v0.4.3 ships the schema only — accept-flow lives in
    -- Sprint 2 (user-management API).
    invited_email    TEXT         NULL,
    invited_at       TIMESTAMPTZ  NULL,
    accepted_at      TIMESTAMPTZ  NULL,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, collection_name, user_id)
);

-- Reverse index: "which tenants is user X a member of?" — the GET
-- /api/tenants list query.
CREATE INDEX _tenant_members_user_idx
    ON _tenant_members (collection_name, user_id) WHERE accepted_at IS NOT NULL;

-- Pending-invite lookup: "who do we email when they accept?"
CREATE INDEX _tenant_members_invite_idx
    ON _tenant_members (invited_email) WHERE invited_email IS NOT NULL AND accepted_at IS NULL;
