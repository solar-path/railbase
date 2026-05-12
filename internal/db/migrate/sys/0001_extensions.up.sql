-- 0001_extensions.up.sql
--
-- Enable PostgreSQL extensions Railbase relies on. CREATE EXTENSION
-- IF NOT EXISTS is idempotent and safe on every boot.
--
-- pgcrypto: gen_random_uuid() for record IDs, pgp_sym_encrypt for
--           field-level encryption, digest() for audit hash chain.
-- ltree:    materialized-path hierarchies (schema.TreePath()).
--
-- pg_trgm, btree_gist, pgvector are auto-enabled later by the schema
-- migration generator only when a collection actually needs them.
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "ltree";
