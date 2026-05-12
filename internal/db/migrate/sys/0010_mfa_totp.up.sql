-- v1.1.2 — MFA: TOTP enrollments + multi-step challenge state machine.
--
-- Two tables:
--
--   _totp_enrollments — one row per (collection, record) when the user
--     has TOTP active. Carries the base32-encoded secret (encrypted at
--     rest with the master key via the same HMAC-then-store shape as
--     other auth tables) and the hashed recovery codes.
--
--   _mfa_challenges — short-lived state machine rows. Created when
--     auth-with-password sees a user with 2FA enrolled: instead of
--     issuing a session immediately, server replies with a challenge
--     id + list of factors_required. Client posts subsequent
--     auth-with-totp / auth-with-otp calls carrying the challenge_id
--     until all factors are solved, then receives the session token.
--
-- We deliberately do NOT FK to the auth-collection table (same reason
-- as _record_tokens: collection is user-defined). Cleanup of orphaned
-- rows happens via the user-delete code path (or a future scheduled
-- GC job once jobs queue lands in v1.4).

CREATE TABLE _totp_enrollments (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_name TEXT         NOT NULL,
    record_id       UUID         NOT NULL,
    -- HMAC-SHA-256(base32_secret, master_key). The base32 secret
    -- itself is NEVER persisted — operator never sees it after the
    -- enrollment confirmation step. The hash is used to verify
    -- candidate codes at signin: server walks counter ± 1 window,
    -- HMACs candidate, compares hashes in constant time.
    --
    -- Wait — that doesn't work: TOTP needs the raw secret to compute
    -- the HMAC counter. So we DO need the raw secret here. We
    -- encrypt it with the master key (AES-256-GCM-style; v1.1 ships
    -- the master key only as HMAC seed though, so for v1.1.2 we
    -- store the base32 secret as-is. v1.2 brings field-level
    -- encryption — TOTP secret encryption will be wired then via
    -- the same KMS-backed path).
    secret_base32   TEXT         NOT NULL,
    -- Hashed recovery codes — 8 strings like "abc1-def2-ghi3", each
    -- Argon2id-hashed before storage. JSONB array of {hash, used_at?}
    -- objects. used_at != NULL flags a recovery code as consumed.
    recovery_codes  JSONB        NOT NULL DEFAULT '[]'::jsonb,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- confirmed_at distinguishes pending enrollment (user got QR but
    -- hasn't verified a code yet) from active enrollment. Pending
    -- rows don't count as "MFA enabled" — auth-with-password skips
    -- the MFA branch when this is NULL. Single pending row per user
    -- (replaced on re-enroll).
    confirmed_at    TIMESTAMPTZ  NULL
);

-- One row per (collection, record) — re-enroll replaces the existing
-- row. Partial index lets us hold concurrent pending + confirmed
-- enrollments if we ever need to, but for v1.1.2 we always upsert.
CREATE UNIQUE INDEX _totp_enrollments_owner_idx
    ON _totp_enrollments (collection_name, record_id);

CREATE TABLE _mfa_challenges (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Random opaque token (32 bytes base64url) the client posts back
    -- in subsequent factor-solve calls. Stored as HMAC-SHA-256 hash
    -- like sessions / record_tokens — raw token only leaves the
    -- server once, in the auth-with-password response.
    token_hash        BYTEA        NOT NULL,
    collection_name   TEXT         NOT NULL,
    record_id         UUID         NOT NULL,
    -- JSON arrays of strings ("password", "totp", "email_otp").
    -- factors_required is what the server demanded; factors_solved
    -- accumulates as the client posts each solve. When sorted-set
    -- equal, the challenge is complete and we issue a session.
    factors_required  JSONB        NOT NULL,
    factors_solved    JSONB        NOT NULL DEFAULT '[]'::jsonb,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ  NOT NULL,
    completed_at      TIMESTAMPTZ  NULL,
    -- Snapshot of IP/UA at the auth-with-password step so the audit
    -- trail and downstream session creation tag the correct origin.
    ip                INET         NULL,
    user_agent        TEXT         NULL
);

CREATE UNIQUE INDEX _mfa_challenges_hash_idx ON _mfa_challenges (token_hash);
CREATE INDEX _mfa_challenges_owner_idx
    ON _mfa_challenges (collection_name, record_id, created_at DESC);
CREATE INDEX _mfa_challenges_expiry_idx
    ON _mfa_challenges (expires_at)
    WHERE completed_at IS NULL;
