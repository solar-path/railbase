package audit

// Ed25519 hash-chain sealing for `_audit_log` (plan.md §3.7.5.3, pulls
// part of v1.1 §4.10 forward).
//
// The v0.6 SHA-256 chain (`prev_hash` / `hash` on every row) is the
// integrity primitive: tampering with any row breaks `Verify` because
// the recomputed hash no longer matches the persisted one. Sealing
// hardens that primitive against an attacker who can rewrite the WHOLE
// chain (every `hash` AND every `prev_hash`) — without an external
// anchor that attack is undetectable.
//
// A seal is an Ed25519 signature of the latest chain head (the last
// row's `hash`) plus the row count + timestamp range it covers,
// persisted into `_audit_seals` (migration 0022). To rewrite history
// undetectably an attacker would now also need the seal-signing
// private key. That key sits at `<dataDir>/.audit_seal_key` (chmod
// 0600), distinct from the master `.secret` so operators can rotate
// it independently and back it up separately (e.g. into an HSM /
// offline keystore).
//
// Sealing runs as a cron-driven job builtin (`audit_seal`, registered
// in internal/jobs/builtins.go). The handler calls Sealer.SealUnsealed
// which walks rows whose `at > <last_seal.range_end>` and emits a
// single `_audit_seals` row for the range. Re-running with no new
// rows is a no-op (returns 0, no row inserted). Re-running mid-day
// after an earlier same-day seal sees only the rows added since.
//
// Key management posture:
//   - Dev (Production=false): auto-create the keypair on first call to
//     NewSealer if the file is missing. Same UX as the master secret.
//   - Production (Production=true): refuse to auto-create. Operator
//     must explicitly run `railbase audit seal-keygen` (which writes
//     the same file shape) or restore a backup. NewSealer returns an
//     error; the app.go wiring logs a warning and disables the
//     builtin, so the audit chain still works but no new seals get
//     written until the operator provides a key.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SealerOptions configures a Sealer.
//
// KeyPath is the absolute path to the Ed25519 private key file. The
// file holds the raw 64-byte ed25519.PrivateKey form (32-byte seed
// followed by 32-byte public key — that's the exact layout
// crypto/ed25519 returns from GenerateKey). chmod 0600 on creation.
//
// Production toggles auto-create on missing-key: false => create one
// (dev mode); true => return an error so the operator notices.
//
// Signer, when set, REPLACES the local-keyfile path entirely. Used by
// regulated deployments that hold the seal key in AWS KMS / Cloud HSM
// (Phase 4 — see signer.go). When Signer != nil, KeyPath and
// Production are ignored. The Signer's PublicKey() is captured for
// each emitted seal row.
type SealerOptions struct {
	Pool       *pgxpool.Pool
	KeyPath    string
	Production bool
	Signer     Signer
}

// Sealer signs successive ranges of the audit-log hash chain.
// Goroutine-safe: the cron-driven `audit_seal` builtin invokes
// SealUnsealed sequentially, but Verify is read-only and safe to call
// concurrently from `railbase audit verify`.
type Sealer struct {
	pool   *pgxpool.Pool
	signer Signer
}

// NewSealer constructs a Sealer from opts. It loads (or, in dev,
// generates) the Ed25519 keypair from disk — UNLESS opts.Signer is
// non-nil, in which case it adopts that signer directly. Returns an
// error if:
//   - opts.Pool is nil
//   - opts.Signer is nil AND opts.KeyPath is empty
//   - the key file is missing and opts.Production is true
//   - the key file exists but has wrong size / can't be read
func NewSealer(opts SealerOptions) (*Sealer, error) {
	if opts.Pool == nil {
		return nil, errors.New("audit: sealer: pool is nil")
	}
	if opts.Signer != nil {
		return &Sealer{pool: opts.Pool, signer: opts.Signer}, nil
	}
	if opts.KeyPath == "" {
		return nil, errors.New("audit: sealer: key path empty")
	}
	priv, err := loadOrCreateSealKey(opts.KeyPath, !opts.Production)
	if err != nil {
		return nil, err
	}
	signer, err := newLocalSigner(priv)
	if err != nil {
		return nil, err
	}
	return &Sealer{pool: opts.Pool, signer: signer}, nil
}

// PublicKey returns the active public key. Exposed so admin tooling
// can print it for backup / out-of-band publication.
func (s *Sealer) PublicKey() ed25519.PublicKey { return s.signer.PublicKey() }

// SealUnsealed walks `_audit_log` rows whose `at` is strictly greater
// than the last seal's `range_end` (or all rows when no seals exist
// yet), computes the chain head (the LAST row's persisted hash — the
// chain itself already binds each prev to the cumulative state), signs
// it with Ed25519, and inserts a fresh `_audit_seals` row. Returns the
// number of audit rows covered by the new seal.
//
// Returns (0, nil) when there are no new rows to seal — re-running
// daily on a quiet system is safe and cheap (one timestamp lookup +
// one count(*)).
//
// Important: the seal's hash is read DIRECTLY from `_audit_log.hash`
// (not recomputed). The point of sealing is to anchor the
// chain-as-persisted; if a tamper has already happened pre-seal the
// chain verifier (Writer.Verify) is what catches it. Sealing's job is
// to anchor what's there NOW so future tampers become detectable.
func (s *Sealer) SealUnsealed(ctx context.Context) (int, error) {
	var rangeStart time.Time
	// Last seal's range_end becomes the new seal's range_start. NULL =
	// epoch, which means "this is the first seal, cover everything".
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(range_end), 'epoch'::timestamptz) FROM _audit_seals`).Scan(&rangeStart)
	if err != nil {
		return 0, fmt.Errorf("audit: sealer: lookup last seal: %w", err)
	}

	// Pull `at` and `hash` for every audit row past the previous seal.
	// We ORDER BY seq (the chain ordering column) so the last row read
	// is the cumulative chain head. Timestamps are recorded into the
	// new seal's range bounds.
	rows, err := s.pool.Query(ctx, `
		SELECT at, hash
		  FROM _audit_log
		 WHERE at > $1
		 ORDER BY seq ASC`, rangeStart)
	if err != nil {
		return 0, fmt.Errorf("audit: sealer: scan rows: %w", err)
	}
	defer rows.Close()

	var (
		firstAt   time.Time
		lastAt    time.Time
		chainHead []byte
		rowCount  int
	)
	for rows.Next() {
		var at time.Time
		var hash []byte
		if err := rows.Scan(&at, &hash); err != nil {
			return 0, fmt.Errorf("audit: sealer: scan: %w", err)
		}
		if rowCount == 0 {
			firstAt = at
		}
		lastAt = at
		chainHead = hash
		rowCount++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("audit: sealer: rows: %w", err)
	}
	if rowCount == 0 {
		// No new rows since the last seal — no-op. Caller logs this as
		// "0 rows sealed"; no `_audit_seals` row is created.
		return 0, nil
	}

	signature, err := s.signer.Sign(chainHead)
	if err != nil {
		return 0, fmt.Errorf("audit: sealer: sign: %w", err)
	}

	// range_start = previous seal's range_end (or firstAt's predecessor
	// when this is the very first seal — we use firstAt directly there
	// so the operator-facing range covers ONLY the rows actually sealed,
	// not the epoch).
	if rangeStart.Year() <= 1970 {
		rangeStart = firstAt
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO _audit_seals
			(range_start, range_end, row_count, chain_head, signature, public_key)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		rangeStart, lastAt, int64(rowCount), chainHead, signature, []byte(s.signer.PublicKey()),
	); err != nil {
		return 0, fmt.Errorf("audit: sealer: insert seal: %w", err)
	}
	return rowCount, nil
}

// SealVerificationError is returned by Verify when a seal's signature
// no longer validates against the persisted public_key + chain_head.
// Holds the offending seal id so operators can locate it in the table.
type SealVerificationError struct {
	SealID string
	Reason string
}

func (e *SealVerificationError) Error() string {
	return fmt.Sprintf("audit: seal verification failed (id=%s): %s", e.SealID, e.Reason)
}

// Verify re-reads every `_audit_seals` row and checks that
// ed25519.Verify(public_key, chain_head, signature) holds. Returns the
// number of seals verified.
//
// Vacuously true when the table is empty — fresh deployments + audit
// systems that have never crossed a seal-cron tick land here. The CLI
// reports "0 seals" so operators don't mistake "no error" for "all
// good — actually there are no seals at all".
func (s *Sealer) Verify(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, chain_head, signature, public_key
		  FROM _audit_seals
		 ORDER BY range_end ASC`)
	if err != nil {
		return 0, fmt.Errorf("audit: sealer: verify query: %w", err)
	}
	defer rows.Close()

	var n int
	for rows.Next() {
		var id string
		var head, sig, pub []byte
		if err := rows.Scan(&id, &head, &sig, &pub); err != nil {
			return n, fmt.Errorf("audit: sealer: verify scan: %w", err)
		}
		if len(pub) != ed25519.PublicKeySize {
			return n, &SealVerificationError{
				SealID: id,
				Reason: fmt.Sprintf("public_key wrong size: %d (want %d)", len(pub), ed25519.PublicKeySize),
			}
		}
		if !ed25519.Verify(ed25519.PublicKey(pub), head, sig) {
			return n, &SealVerificationError{SealID: id, Reason: "signature mismatch"}
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("audit: sealer: verify rows: %w", err)
	}
	return n, nil
}

// loadOrCreateSealKey reads the raw 64-byte ed25519 private key from
// path. When the file is missing AND allowCreate is true, generates a
// fresh keypair and writes it atomically with chmod 0600. Returns an
// error when the file is missing and allowCreate is false — that's
// the production gate: operators must run `railbase audit seal-keygen`
// or restore from backup.
func loadOrCreateSealKey(path string, allowCreate bool) (ed25519.PrivateKey, error) {
	body, err := os.ReadFile(path)
	if err == nil {
		if len(body) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("audit: seal key %s: wrong size %d (want %d)",
				path, len(body), ed25519.PrivateKeySize)
		}
		return ed25519.PrivateKey(body), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("audit: seal key %s: read: %w", path, err)
	}
	if !allowCreate {
		return nil, fmt.Errorf("audit: seal key %s missing — run `railbase audit seal-keygen` or restore from backup", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("audit: seal key: mkdir %s: %w", filepath.Dir(path), err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("audit: seal key: generate: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, priv, 0o600); err != nil {
		return nil, fmt.Errorf("audit: seal key: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("audit: seal key: rename %s: %w", path, err)
	}
	return priv, nil
}

// GenerateSealKey writes a fresh Ed25519 keypair to path. Refuses to
// overwrite unless force is true. Used by `railbase audit seal-keygen`.
//
// Returns the public key so the CLI can echo it for operator backup
// (the public key is recoverable from the private key alone, but
// showing it inline saves an `openssl pkey` round trip).
func GenerateSealKey(path string, force bool) (ed25519.PublicKey, error) {
	if _, err := os.Stat(path); err == nil && !force {
		return nil, fmt.Errorf("audit: seal key %s already exists — pass --force to overwrite", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("audit: seal key %s: stat: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("audit: seal key: mkdir %s: %w", filepath.Dir(path), err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("audit: seal key: generate: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, priv, 0o600); err != nil {
		return nil, fmt.Errorf("audit: seal key: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("audit: seal key: rename %s: %w", path, err)
	}
	return pub, nil
}

