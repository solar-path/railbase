-- v1.x — audit-log Ed25519 hash-chain sealing.
--
-- Spec: plan.md §3.7.5.3 (audit_seal job builtin) + pulls part of v1.1
-- §4.10 "audit sealing" forward. The v0.6 audit log already maintains
-- a SHA-256 hash chain (`prev_hash` / `hash` columns). Sealing layers
-- a periodic Ed25519 signature over the latest chain head into a
-- separate `_audit_seals` table so operators can later verify "no row
-- was ever silently rewritten" by re-walking the chain and checking
-- each seal's signature.
--
-- Why a separate table (vs. extending `_audit_log`): the chain row
-- itself MUST NOT contain its own signature — that's circular. A
-- separate table also lets operators rotate the signing key
-- (`public_key` is per-row) without re-signing history.
--
-- Verification flow (`railbase audit verify`):
--   1. Re-walk `_audit_log` ordered by `seq` — confirms the SHA-256
--      chain is intact (v0.6 behaviour, unchanged).
--   2. For each `_audit_seals` row, re-fetch the audit row whose
--      timestamp == range_end, recompute its hash, then call
--      ed25519.Verify(public_key, chain_head, signature). A mismatch
--      proves the chain was tampered with AFTER sealing.

CREATE TABLE _audit_seals (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sealed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    range_start  TIMESTAMPTZ NOT NULL,
    range_end    TIMESTAMPTZ NOT NULL,
    row_count    BIGINT NOT NULL,
    chain_head   BYTEA NOT NULL,  -- SHA-256 of the last row in this seal range
    signature    BYTEA NOT NULL,  -- Ed25519 signature of chain_head
    public_key   BYTEA NOT NULL   -- Ed25519 public key (32 bytes); operator-rotatable
);

-- Most reads are "give me the most recent seal" (incremental sealing
-- runs every night and asks the DB where the previous run stopped).
CREATE INDEX _audit_seals_range_end_idx ON _audit_seals (range_end);
