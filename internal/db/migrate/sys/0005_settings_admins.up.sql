-- v0.5 — settings + system admins.
--
-- _settings: runtime-mutable configuration. Spec lives in
-- docs/14-observability.md "Settings model". Keys follow a
-- dotted namespace (`site.name`, `auth.password_min_len`,
-- `mailer.from_address`) so admin UI can group them by section.
-- The value column is JSONB so each setting can carry whatever
-- shape it wants — primitive, object, array — without per-key
-- column definitions.
--
-- _admins: system-level administrators, distinct from auth
-- collections (`users`, `sellers`, …). docs/04-identity.md states:
--
--     "Создаются через CLI: `railbase admin create <email>`. Это
--      site-scope identity, не часть пользовательской схемы."
--
-- Admins bypass tenant RLS and own site-scope tooling; user-facing
-- auth collections never get those privileges.

CREATE TABLE _settings (
    key         TEXT         PRIMARY KEY,
    value       JSONB        NOT NULL,
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE _admins (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT         NOT NULL,
    password_hash TEXT         NOT NULL,
    created       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ  NULL
);

-- Lower-case unique index — case-insensitive admin emails. Same
-- pattern auth collections use for `email`.
CREATE UNIQUE INDEX _admins_email_idx ON _admins (lower(email));
