-- v1.1.4 — RBAC: site + tenant roles, action grants, user assignments.
--
-- Architecture (see docs/04 §"RBAC — site + tenant scope"):
--
--   _roles            one row per role name + scope
--                     (scope = 'site' | 'tenant'; system roles flagged)
--   _role_actions     per-role grant rows (one row per (role_id, action_key))
--   _user_roles       user→role assignments (tenant_id NULL for site, set
--                     for tenant-scoped role memberships)
--
-- Action keys are NOT stored centrally — they're string constants
-- defined in Go (`internal/rbac/actionkeys`) and used as opaque
-- identifiers here. New action keys appear simply by inserting a
-- _role_actions row referencing them; the catalog in Go provides
-- IDE completion + compile-time checking but isn't a foreign key.
-- This lets plugin code register actions without DB migrations.
--
-- System roles (`is_system = TRUE`):
--   - immutable: can't be renamed / deleted / re-scoped
--   - grants ARE mutable (operators tune them in admin UI)
--   - seeded by v1.1.4 itself (see 0013_rbac_seed)

CREATE TABLE _roles (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT         NOT NULL,                -- "system_admin", "owner", ...
    scope       TEXT         NOT NULL CHECK (scope IN ('site', 'tenant')),
    description TEXT         NOT NULL DEFAULT '',
    is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- One (name, scope) pair per role. A site role and a tenant role
-- can both be named "admin" — they're different rows.
CREATE UNIQUE INDEX _roles_name_scope_idx ON _roles (name, scope);

CREATE TABLE _role_actions (
    role_id     UUID         NOT NULL REFERENCES _roles(id) ON DELETE CASCADE,
    action_key  TEXT         NOT NULL,
    granted_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, action_key)
);

CREATE INDEX _role_actions_action_idx ON _role_actions (action_key);

CREATE TABLE _user_roles (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Mirrors _external_auths shape: collection_name + record_id
    -- identifies the user without an FK to a user-defined table.
    collection_name TEXT         NOT NULL,
    record_id       UUID         NOT NULL,
    role_id         UUID         NOT NULL REFERENCES _roles(id) ON DELETE CASCADE,
    -- tenant_id NULL ⇔ site assignment. Non-null ⇔ tenant assignment;
    -- caller is responsible for FK-equivalent integrity (we don't FK
    -- to tenants(id) so embedded test setups without the tenants
    -- table still migrate cleanly).
    tenant_id       UUID         NULL,
    granted_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    granted_by      UUID         NULL          -- admin record_id who granted
);

-- One user can hold a given role per (tenant or site) once. Two
-- partial indexes — one for site, one for tenant — because NULL
-- doesn't deduplicate in a normal unique index.
CREATE UNIQUE INDEX _user_roles_site_idx
    ON _user_roles (collection_name, record_id, role_id)
    WHERE tenant_id IS NULL;

CREATE UNIQUE INDEX _user_roles_tenant_idx
    ON _user_roles (collection_name, record_id, role_id, tenant_id)
    WHERE tenant_id IS NOT NULL;

-- Lookup hot path: "actions for user X (optionally in tenant Y)".
CREATE INDEX _user_roles_lookup_idx
    ON _user_roles (collection_name, record_id, tenant_id);
