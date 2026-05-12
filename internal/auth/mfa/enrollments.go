// Package mfa is the persistence layer for v1.1.2 multi-factor auth:
//
//	- TOTPEnrollmentStore: per-user TOTP secret + hashed recovery
//	  codes. Lives in `_totp_enrollments` (one row per (collection,
//	  record)).
//
//	- ChallengeStore: short-lived MFA challenge state. Lives in
//	  `_mfa_challenges`. A challenge is created when auth-with-
//	  password sees a user with TOTP enrolled; the client then posts
//	  factor solves (auth-with-totp / auth-with-otp) carrying the
//	  challenge token until factors_solved ⊇ factors_required, at
//	  which point a session is issued.
//
// Both stores share the master key with sessions / record tokens so a
// single secret rotation invalidates everything.
package mfa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/totp"
)

// ErrNotFound is returned by both stores on lookup miss. Always
// collapsed with "expired" / "already consumed" so the surface can't
// be probed.
var ErrNotFound = errors.New("mfa: not found")

// --- TOTP enrollments ---

// TOTPEnrollment is the materialised `_totp_enrollments` row.
// Secret is the raw base32 string — needed at signin to compute
// HMAC(counter). v1.2 will encrypt this field via the same KMS
// path field-level encryption uses.
type TOTPEnrollment struct {
	ID             uuid.UUID
	CollectionName string
	RecordID       uuid.UUID
	Secret         string                    // base32, no padding
	RecoveryCodes  []totp.HashedRecoveryCode // append-update'd as codes are consumed
	CreatedAt      time.Time
	ConfirmedAt    *time.Time // NULL when pending (QR shown but no code verified)
}

// Active reports whether this enrollment is the "active" 2FA factor.
// Pending enrollments (QR shown but never confirmed) DO NOT count
// for auth-with-password's MFA branch.
func (e *TOTPEnrollment) Active() bool {
	return e != nil && e.ConfirmedAt != nil
}

// TOTPEnrollmentStore is the persistence handle. Goroutine-safe.
type TOTPEnrollmentStore struct {
	pool *pgxpool.Pool
}

// NewTOTPEnrollmentStore returns a store. One per process.
func NewTOTPEnrollmentStore(pool *pgxpool.Pool) *TOTPEnrollmentStore {
	return &TOTPEnrollmentStore{pool: pool}
}

// CreatePending upserts a row in the "pending" state — replaces any
// existing enrollment for (collection, record) so a user who hits
// "Enable 2FA" twice can re-scan a fresh QR. Returns the materialised
// row (sans recovery_codes, which are surfaced separately on
// Confirm).
//
// The raw recovery codes are NOT persisted here — only the hashed
// forms. Callers MUST capture the raw codes from totp.GenerateRecovery
// Codes and return them in the response.
func (s *TOTPEnrollmentStore) CreatePending(ctx context.Context, collectionName string, recordID uuid.UUID, secret string, recoveryHashed []totp.HashedRecoveryCode) (*TOTPEnrollment, error) {
	body, err := json.Marshal(recoveryHashed)
	if err != nil {
		return nil, fmt.Errorf("mfa: marshal recovery codes: %w", err)
	}
	const q = `
        INSERT INTO _totp_enrollments
            (collection_name, record_id, secret_base32, recovery_codes, created_at, confirmed_at)
        VALUES ($1, $2, $3, $4, now(), NULL)
        ON CONFLICT (collection_name, record_id) DO UPDATE
            SET secret_base32 = EXCLUDED.secret_base32,
                recovery_codes = EXCLUDED.recovery_codes,
                created_at    = now(),
                confirmed_at  = NULL
        RETURNING id, collection_name, record_id, secret_base32,
                  recovery_codes, created_at, confirmed_at
    `
	row := s.pool.QueryRow(ctx, q, collectionName, recordID, secret, body)
	return scanEnrollment(row)
}

// Confirm marks the pending enrollment active. Caller must have
// already verified that the user typed a valid TOTP code against
// the stored secret — Confirm does NOT re-verify.
func (s *TOTPEnrollmentStore) Confirm(ctx context.Context, collectionName string, recordID uuid.UUID) error {
	const q = `
        UPDATE _totp_enrollments
           SET confirmed_at = now()
         WHERE collection_name = $1 AND record_id = $2
           AND confirmed_at IS NULL
    `
	tag, err := s.pool.Exec(ctx, q, collectionName, recordID)
	if err != nil {
		return fmt.Errorf("mfa: confirm: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns the enrollment for a user, or ErrNotFound. Returns
// pending enrollments too — call .Active() to filter.
func (s *TOTPEnrollmentStore) Get(ctx context.Context, collectionName string, recordID uuid.UUID) (*TOTPEnrollment, error) {
	const q = `
        SELECT id, collection_name, record_id, secret_base32,
               recovery_codes, created_at, confirmed_at
          FROM _totp_enrollments
         WHERE collection_name = $1 AND record_id = $2
    `
	row := s.pool.QueryRow(ctx, q, collectionName, recordID)
	e, err := scanEnrollment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// Disable hard-deletes the enrollment. Used when the user explicitly
// turns 2FA off. Returns ErrNotFound when nothing was deleted so
// the API surface can 404.
func (s *TOTPEnrollmentStore) Disable(ctx context.Context, collectionName string, recordID uuid.UUID) error {
	const q = `
        DELETE FROM _totp_enrollments
         WHERE collection_name = $1 AND record_id = $2
    `
	tag, err := s.pool.Exec(ctx, q, collectionName, recordID)
	if err != nil {
		return fmt.Errorf("mfa: disable: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRecoveryCodeUsed stamps the i-th recovery code as consumed
// and persists the whole array. Atomic at the row level (one
// pool.Exec per call).
func (s *TOTPEnrollmentStore) MarkRecoveryCodeUsed(ctx context.Context, enrollmentID uuid.UUID, codeIndex int) error {
	// Fetch, mutate in-Go, write back. A SQL-native jsonb_set update
	// is possible but the index-based addressing makes it ugly; a
	// short read-modify-write inside a tx is simpler and the table
	// is tiny.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const sel = `SELECT recovery_codes FROM _totp_enrollments WHERE id = $1 FOR UPDATE`
	var body []byte
	if err := tx.QueryRow(ctx, sel, enrollmentID).Scan(&body); err != nil {
		return err
	}
	var codes []totp.HashedRecoveryCode
	if err := json.Unmarshal(body, &codes); err != nil {
		return fmt.Errorf("mfa: unmarshal recovery: %w", err)
	}
	if codeIndex < 0 || codeIndex >= len(codes) {
		return fmt.Errorf("mfa: code index out of range")
	}
	now := time.Now().UTC().Truncate(time.Second)
	codes[codeIndex].UsedAt = &now
	out, err := json.Marshal(codes)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE _totp_enrollments SET recovery_codes = $2 WHERE id = $1`,
		enrollmentID, out); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RegenerateRecoveryCodes replaces the recovery_codes array with a
// fresh set. Returns the raw codes (to surface to the user once) and
// persists the hashed forms. Used when the user has exhausted codes
// or fears compromise.
func (s *TOTPEnrollmentStore) RegenerateRecoveryCodes(ctx context.Context, collectionName string, recordID uuid.UUID) ([]totp.RecoveryCode, error) {
	raw, hashed, err := totp.GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(hashed)
	if err != nil {
		return nil, err
	}
	const q = `
        UPDATE _totp_enrollments
           SET recovery_codes = $3
         WHERE collection_name = $1 AND record_id = $2
    `
	tag, err := s.pool.Exec(ctx, q, collectionName, recordID, body)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return raw, nil
}

// scanEnrollment is the shared row → struct mapper.
func scanEnrollment(row pgx.Row) (*TOTPEnrollment, error) {
	var e TOTPEnrollment
	var body []byte
	if err := row.Scan(&e.ID, &e.CollectionName, &e.RecordID, &e.Secret,
		&body, &e.CreatedAt, &e.ConfirmedAt); err != nil {
		return nil, err
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &e.RecoveryCodes)
	}
	return &e, nil
}
