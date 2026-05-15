-- v0.4.3 Sprint 5 — device labelling on sessions.
--
-- Air/rail expose a "devices" tab on the account screen where the
-- user can rename a session ("Alice's iPhone") and mark it trusted.
-- Trust enforcement (e.g. "skip 2FA on trusted devices for N days")
-- is a v0.5 follow-up; v0.4.3 only persists the user's intent.
--
-- Why columns on _sessions rather than a separate _devices table:
-- without a stable cross-signin device fingerprint (cookie / TPM /
-- WebAuthn handle), a "device" IS one session. Threading a synthetic
-- ID through every signin code path (password / OAuth / OTP / SAML /
-- LDAP / WebAuthn) for an abstraction with no extra capability would
-- be churn for churn's sake. When v0.5 adds real fingerprinting we
-- can split _devices off and add the FK with a backfill — cheap.

ALTER TABLE _sessions
    ADD COLUMN device_name TEXT NULL,
    ADD COLUMN is_trusted  BOOLEAN NOT NULL DEFAULT FALSE;

-- No index on is_trusted yet — the account-page query is a small
-- per-user list (typically <10 rows). When trust enforcement gates
-- the 2FA bypass at signin time, an index on (collection_name,
-- user_id, is_trusted) becomes worth adding.
