-- v1.x — bridge the `_admins` table into RBAC.
--
-- Before this migration the `/api/_admin/*` chain authenticated admins
-- via the AdminAuthMiddleware (cookie / Bearer → `_admin_sessions` →
-- AdminPrincipal) but never called `rbac.Require(...)` on anything,
-- so EVERY authenticated admin had full access to every admin endpoint.
-- The RBAC infrastructure existed for user-collection auth only and was
-- effectively dead code on the admin surface.
--
-- This migration:
--
--   1. Adds a `system_readonly` site role — the natural pair to
--      `system_admin`. Holds the *.read action keys plus
--      audit.verify (read-only verification), but NOT settings.write /
--      rbac.write / admins.* / users.update etc. Operators who want a
--      "view-only auditor" account assign this role instead of granting
--      a fresh admin session.
--
--   2. Backfills every existing row in `_admins` with the
--      `system_admin` site role at `_user_roles(collection_name='_admins',
--      record_id=<admin_id>, role=system_admin)`. Keeps the current
--      behaviour (every existing admin is still all-powerful) but makes
--      that authority EXPLICIT and downgradable — operators can revoke
--      and assign a lesser role through the normal Store.Assign API.
--
-- After this migration the admin chain still works identically. The
-- handlers themselves need a follow-up edit to call `rbac.Require(...)`
-- before they actually consult the resolved set — see the matching
-- middleware wiring in adminapi.

-- system_readonly: read-only admin.
INSERT INTO _roles (name, scope, description, is_system) VALUES
    ('system_readonly', 'site', 'Read-only admin (audit/settings/schema view)', TRUE)
ON CONFLICT (name, scope) DO NOTHING;

-- Action grants for system_readonly. Deliberately narrow:
-- the *.read / *.list family + audit.verify. NO writes, NO admin
-- lifecycle, NO rbac mutations.
INSERT INTO _role_actions (role_id, action_key)
SELECT r.id, a.key FROM _roles r
CROSS JOIN (VALUES
    ('admins.list'),
    ('users.list'),
    ('tenants.list'),
    ('audit.read'),
    ('audit.verify'),
    ('settings.read'),
    ('schema.read'),
    ('rbac.read')
) AS a(key)
WHERE r.name = 'system_readonly' AND r.scope = 'site'
ON CONFLICT DO NOTHING;

-- Backfill every existing admin as system_admin so today's deployments
-- don't lose access on the next boot. The follow-up handler-side
-- gating will deny their requests if and only if their assignment is
-- subsequently downgraded — which is the entire point.
INSERT INTO _user_roles (collection_name, record_id, role_id)
SELECT '_admins', a.id, r.id
  FROM _admins a
 CROSS JOIN _roles r
 WHERE r.name = 'system_admin' AND r.scope = 'site'
ON CONFLICT DO NOTHING;
