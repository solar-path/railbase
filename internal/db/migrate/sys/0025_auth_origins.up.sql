-- v1.7.36 — Auth origins (§3.2.10).
--
-- Records each distinct (user, IP-class, browser-class) tuple from
-- which a successful signin has been observed. The first row per
-- tuple is what powers the "new device signin" notification — when
-- the UPSERT path in `internal/auth/origins.Store.Touch` reports a
-- fresh insertion, the signin handler enqueues a `send_email_async`
-- job using the `new_device_signin` template.
--
-- Granularity choices:
--
--   * `ip_class` (not raw IP) — IPv4 → /24 prefix, IPv6 → /48 prefix.
--     Mobile clients re-key DHCP / NAT pools constantly; per-raw-IP
--     would spam users with notifications every time they switched
--     coffee shops. /24 keeps "same building" same-origin, /48
--     keeps "same ISP allocation" same-origin. Operators wanting
--     stricter behaviour (per-IP) can substitute their own store —
--     the Touch API hides the normalisation.
--
--   * `ua_hash` (not raw User-Agent) — version-stripped sha256 so
--     `Chrome/120` and `Chrome/121` collide. The normaliser keeps
--     `Browser + OS` and strips version tokens. Same rationale as
--     /24 — Chrome auto-updates weekly; users shouldn't be re-
--     notified at every silent update.
--
--   * `remembered_until` defaults to NULL (caller stamps +30 days).
--     A future polish (deferred) sweeps expired origins so a user
--     who hasn't signed in from $home for 90 days gets re-prompted
--     when they come back. The current Touch path does NOT prune;
--     it just UPSERTs with last_seen_at = now().
--
-- The UNIQUE (user_id, collection, ip_class, ua_hash) constraint is
-- the UPSERT key. The (user_id, last_seen_at DESC) index powers the
-- per-user list/dashboard view ("sign-ins from these places").

CREATE TABLE _auth_origins (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID         NOT NULL,
    collection        TEXT         NOT NULL,
    ip_class          TEXT         NOT NULL,
    ua_hash           TEXT         NOT NULL,
    first_seen_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_seen_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    remembered_until  TIMESTAMPTZ,
    UNIQUE (user_id, collection, ip_class, ua_hash)
);

-- "List origins for this user, most-recent first" — the admin/CLI
-- drill-down query.
CREATE INDEX _auth_origins_user_idx
    ON _auth_origins (user_id, last_seen_at DESC);
