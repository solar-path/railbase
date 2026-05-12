-- v1.1.4 — minimum RBAC role seed.
--
-- Site roles:
--   system_admin     — everything (bypass marker; LoadActions short-circuits)
--   admin            — everything except site-system actions
--   user             — default for signups; CRUD on own records via @me rules
--   guest            — read-only on public-marked collections
--
-- Tenant roles:
--   owner            — everything within a tenant
--   admin            — most tenant actions except tenant.delete
--   member           — CRUD on own records within tenant
--   viewer           — read-only within tenant
--
-- Specific action grants are deliberately MINIMAL here — operators
-- expand via admin UI / CLI / DSL. Seed is "the smallest working
-- catalog any deployment needs", not "every action your app could
-- ever want".

INSERT INTO _roles (name, scope, description, is_system) VALUES
    ('system_admin', 'site',   'Full bypass; cannot be denied any action',  TRUE),
    ('admin',        'site',   'Site-wide administrator',                    TRUE),
    ('user',         'site',   'Default authenticated user',                 TRUE),
    ('guest',        'site',   'Unauthenticated reader',                     TRUE),
    ('owner',        'tenant', 'Tenant owner — full bypass within tenant',  TRUE),
    ('admin',        'tenant', 'Tenant administrator',                       TRUE),
    ('member',       'tenant', 'Default tenant member',                      TRUE),
    ('viewer',       'tenant', 'Read-only within tenant',                    TRUE)
ON CONFLICT (name, scope) DO NOTHING;

-- system_admin & owner are bypass roles — they're checked by name in
-- LoadActions (return immediately as "has every action"). No need to
-- enumerate every action key here. Other roles get explicit grants
-- below.

-- site:admin baseline: everything except system-only actions.
-- Operator can revoke specific entries via admin UI.
INSERT INTO _role_actions (role_id, action_key)
SELECT r.id, a.key FROM _roles r
CROSS JOIN (VALUES
    ('admins.list'),
    ('admins.create'),
    ('admins.delete'),
    ('users.list'),
    ('users.create'),
    ('users.update'),
    ('users.delete'),
    ('tenants.list'),
    ('tenants.create'),
    ('tenants.update'),
    ('tenants.delete'),
    ('audit.read'),
    ('audit.verify'),
    ('settings.read'),
    ('settings.write'),
    ('schema.read'),
    ('mailer.test'),
    ('rbac.read'),
    ('rbac.write')
) AS a(key)
WHERE r.name = 'admin' AND r.scope = 'site'
ON CONFLICT DO NOTHING;

-- site:user baseline: very narrow — just signin + read your own profile.
INSERT INTO _role_actions (role_id, action_key)
SELECT r.id, a.key FROM _roles r
CROSS JOIN (VALUES
    ('auth.signin'),
    ('auth.refresh'),
    ('auth.signout'),
    ('auth.me'),
    ('totp.enroll'),
    ('totp.disable'),
    ('webauthn.enroll'),
    ('webauthn.delete')
) AS a(key)
WHERE r.name = 'user' AND r.scope = 'site'
ON CONFLICT DO NOTHING;

-- site:guest: only the unauthenticated-allowed actions.
INSERT INTO _role_actions (role_id, action_key)
SELECT r.id, a.key FROM _roles r
CROSS JOIN (VALUES
    ('auth.signin'),
    ('auth.signup'),
    ('auth.password_reset'),
    ('webauthn.login')
) AS a(key)
WHERE r.name = 'guest' AND r.scope = 'site'
ON CONFLICT DO NOTHING;

-- tenant:admin baseline.
INSERT INTO _role_actions (role_id, action_key)
SELECT r.id, a.key FROM _roles r
CROSS JOIN (VALUES
    ('tenant.members.list'),
    ('tenant.members.invite'),
    ('tenant.members.remove'),
    ('tenant.records.list'),
    ('tenant.records.create'),
    ('tenant.records.update'),
    ('tenant.records.delete'),
    ('tenant.settings.read'),
    ('tenant.settings.write')
) AS a(key)
WHERE r.name = 'admin' AND r.scope = 'tenant'
ON CONFLICT DO NOTHING;

-- tenant:member baseline.
INSERT INTO _role_actions (role_id, action_key)
SELECT r.id, a.key FROM _roles r
CROSS JOIN (VALUES
    ('tenant.records.list'),
    ('tenant.records.create'),
    ('tenant.records.update_own'),
    ('tenant.records.delete_own')
) AS a(key)
WHERE r.name = 'member' AND r.scope = 'tenant'
ON CONFLICT DO NOTHING;

-- tenant:viewer: read only.
INSERT INTO _role_actions (role_id, action_key)
SELECT r.id, a.key FROM _roles r
CROSS JOIN (VALUES
    ('tenant.records.list'),
    ('tenant.records.read')
) AS a(key)
WHERE r.name = 'viewer' AND r.scope = 'tenant'
ON CONFLICT DO NOTHING;
