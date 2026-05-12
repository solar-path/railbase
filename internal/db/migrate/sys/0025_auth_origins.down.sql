-- v1.7.36 — Roll back §3.2.10 auth_origins. Dropping the table also
-- drops the UNIQUE constraint + the (user_id, last_seen_at) index.
DROP TABLE IF EXISTS _auth_origins;
