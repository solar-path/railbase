-- Reverse of 0029_rbac_admin_bridge.up.sql.
--
-- We remove the `system_readonly` role and the bootstrap assignments
-- we created. Hand-edited assignments (operators who downgraded an
-- admin's role through the UI) are NOT touched — only the exact
-- shape this migration created.

-- Drop assignments where collection_name='_admins' and role is
-- system_admin: these are the rows we bulk-inserted. If an operator
-- has since added other rows (system_readonly assignments, manual
-- grants), those survive.
DELETE FROM _user_roles ur
 USING _roles r
 WHERE ur.role_id = r.id
   AND ur.collection_name = '_admins'
   AND r.name = 'system_admin'
   AND r.scope = 'site';

-- system_readonly role: cascades to _role_actions and _user_roles
-- via the FK on role_id.
DELETE FROM _roles WHERE name = 'system_readonly' AND scope = 'site';
