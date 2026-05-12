-- v1.1.3 — WebAuthn / passkey credentials.
--
-- One row per registered authenticator. A single user (collection +
-- record_id) can have multiple credentials (phone passkey + YubiKey
-- + Mac Touch ID) and we list/delete them individually.
--
-- Shape decisions:
--
--   - credential_id:  the raw ID the authenticator returns (variable
--                     length, typically 16-128 bytes). UNIQUE so a
--                     stolen credential can't be re-claimed by a
--                     different user.
--   - public_key:     COSE-encoded raw bytes as returned by the
--                     authenticator. v1.1.3 ships ES256 (P-256 ECDSA)
--                     only; the stored bytes still preserve full
--                     COSE so a future RS256 / EdDSA expansion is a
--                     parse-side change, not a migration.
--   - sign_count:     monotonic counter the authenticator increments
--                     each use. Replay protection: a candidate
--                     assertion's signCount MUST be > stored.
--   - aaguid:         authenticator model ID. Always present; "none"
--                     attestation reports all-zeros, which we store
--                     verbatim — no validation against the FIDO MDS.
--   - transports:     JSONB array of strings ("usb", "nfc", "ble",
--                     "internal", "hybrid"). Hint to the browser on
--                     subsequent authentication ceremonies so it
--                     filters available authenticators. Optional.
--   - user_handle:    64-byte random handle generated at first
--                     enrollment, stable across credentials for the
--                     SAME (collection, record). Passwordless
--                     discoverable-credential signin: authenticator
--                     returns user_handle in the assertion, we map
--                     it back to record_id.
--   - name:           operator-friendly label ("MacBook Touch ID",
--                     "Yubikey blue"). Set at register-finish via
--                     optional body field; admin UI shows it.
--
-- Like _external_auths and _record_tokens we do NOT FK to the
-- collection table (user-defined; may not exist at migration time).

CREATE TABLE _webauthn_credentials (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_name TEXT         NOT NULL,
    record_id       UUID         NOT NULL,
    credential_id   BYTEA        NOT NULL,
    public_key      BYTEA        NOT NULL,
    sign_count      BIGINT       NOT NULL DEFAULT 0,
    aaguid          BYTEA        NOT NULL,
    transports      JSONB        NOT NULL DEFAULT '[]'::jsonb,
    -- user_handle is the same value for every credential a single
    -- user registers — populated by the auth-collection layer at
    -- first enroll and copied on subsequent enrolls.
    user_handle     BYTEA        NOT NULL,
    name            TEXT         NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_used_at    TIMESTAMPTZ  NULL
);

-- Lookup #1: "find user by credential_id" (auth ceremony).
CREATE UNIQUE INDEX _webauthn_credentials_credid_idx
    ON _webauthn_credentials (credential_id);

-- Lookup #2: "find user by user_handle" (discoverable credentials —
-- authenticator returns user_handle, we map to record).
CREATE INDEX _webauthn_credentials_userhandle_idx
    ON _webauthn_credentials (user_handle);

-- Lookup #3: admin UI "show this user's authenticators".
CREATE INDEX _webauthn_credentials_owner_idx
    ON _webauthn_credentials (collection_name, record_id);
