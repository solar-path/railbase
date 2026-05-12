-- v0.4 — tenant resolution.
--
-- The schema generator already emits FK references to `tenants(id)`
-- on every .Tenant() collection (see internal/schema/gen/sql.go). This
-- migration creates the table so those references resolve.
--
-- Minimal fields for v0.4. Per-tenant settings, plan, billing status,
-- impersonation flags etc. are layered in by:
--   - v0.5 (settings model)
--   - v1.1 (railbase-orgs plugin)
--
-- Note the deliberate NON-underscore name: `tenants`, not `_tenants`.
-- User code references the column via `.Tenant()`, the SQL generator
-- emits FK to it, the admin UI manages rows directly. It's part of
-- the user-visible surface even though Railbase ships it.

CREATE TABLE tenants (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT         NOT NULL,
    created     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX tenants_name_idx ON tenants (lower(name));
