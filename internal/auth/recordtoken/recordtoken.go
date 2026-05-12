// Package recordtoken is the persistence layer for `_record_tokens`.
//
// A record token is a short-lived single-use credential tied to a
// specific row of a specific collection. v1.1 ships these flows:
//
//   - email verification  (purpose=PurposeVerify, TTL 24h)
//   - password reset      (purpose=PurposeReset, TTL 1h)
//   - email change confirm (purpose=PurposeEmailChange, TTL 24h)
//   - magic-link signin   (purpose=PurposeMagicLink, TTL 15min)
//   - one-time code (OTP) (purpose=PurposeOTP, TTL 10min, payload carries hashed code)
//
// File-access tokens land in v1.3 with the storage drivers.
//
// Token format:
//
//	raw = base64url(32 random bytes)        — sent to the user (email link)
//	hash = HMAC-SHA-256(raw, master_secret) — stored as token_hash
//
// Single-use enforcement is by row-level lock + UPDATE in a single
// transaction so two parallel Consume attempts on the same token can't
// both succeed.
package recordtoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/token"
	"github.com/railbase/railbase/internal/clock"
)

// Purpose discriminates the row's intent. Stringly typed so the audit
// log and admin UI can render values without a lookup table. Adding
// purposes is fine; renaming breaks issued tokens in the wild.
type Purpose string

const (
	PurposeVerify      Purpose = "verify"
	PurposeReset       Purpose = "reset"
	PurposeEmailChange Purpose = "email_change"
	PurposeMagicLink   Purpose = "magic_link"
	PurposeOTP         Purpose = "otp"
	PurposeFileAccess  Purpose = "file_access" // reserved for v1.3
)

// DefaultTTL returns the typical lifetime for each purpose. Callers
// can pass an explicit TTL to Create when they need a non-default
// (e.g. admin-issued reset link with a custom expiry).
func DefaultTTL(p Purpose) time.Duration {
	switch p {
	case PurposeVerify, PurposeEmailChange:
		return 24 * time.Hour
	case PurposeReset:
		return 1 * time.Hour
	case PurposeMagicLink:
		return 15 * time.Minute
	case PurposeOTP:
		return 10 * time.Minute
	case PurposeFileAccess:
		return 1 * time.Hour
	default:
		return 1 * time.Hour
	}
}

// ErrNotFound is returned by Consume / Get when no row matches —
// either the token doesn't exist, has expired, or has already been
// consumed. All three collapse into one error so the API surface
// can't be probed.
var ErrNotFound = errors.New("recordtoken: not found or expired")

// Record is the persisted token row. Raw token is NEVER stored;
// only the HMAC hash. Get/List return Record without RawToken so
// admin UI can browse without leaking.
type Record struct {
	ID             uuid.UUID
	Purpose        Purpose
	CollectionName string
	RecordID       uuid.UUID
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	Payload        map[string]any
}

// Store is the persistence handle. Pass the same master Key the
// rest of the auth surface uses so a single secret rotation
// invalidates every issued token. Goroutine-safe.
type Store struct {
	pool   *pgxpool.Pool
	secret secret.Key
}

// NewStore returns a Store. Call once on boot, share for the
// lifetime of the process.
func NewStore(pool *pgxpool.Pool, key secret.Key) *Store {
	return &Store{pool: pool, secret: key}
}

// CreateInput captures what Create needs. TTL=0 → DefaultTTL(purpose).
// Payload is optional — flows that need to carry extra context (e.g.
// the new email for an email-change flow, the hashed OTP for OTP
// purpose) stash it here.
type CreateInput struct {
	Purpose        Purpose
	CollectionName string
	RecordID       uuid.UUID
	TTL            time.Duration
	Payload        map[string]any
}

// Create issues a new record token. Returns the RAW token (send to
// user, never persist) and the materialised Record for audit-side
// callers.
func (s *Store) Create(ctx context.Context, in CreateInput) (token.Token, *Record, error) {
	if in.Purpose == "" {
		return "", nil, fmt.Errorf("recordtoken: purpose is required")
	}
	if in.CollectionName == "" {
		return "", nil, fmt.Errorf("recordtoken: collection_name is required")
	}
	if in.RecordID == uuid.Nil {
		return "", nil, fmt.Errorf("recordtoken: record_id is required")
	}

	tok, err := token.Generate()
	if err != nil {
		return "", nil, fmt.Errorf("recordtoken: generate: %w", err)
	}
	hash := token.Compute(tok, s.secret)

	ttl := in.TTL
	if ttl <= 0 {
		ttl = DefaultTTL(in.Purpose)
	}
	now := clock.Now()
	expires := now.Add(ttl)
	id := uuid.Must(uuid.NewV7())

	var payloadBytes []byte
	if in.Payload != nil {
		payloadBytes, err = json.Marshal(in.Payload)
		if err != nil {
			return "", nil, fmt.Errorf("recordtoken: marshal payload: %w", err)
		}
	}

	const q = `
        INSERT INTO _record_tokens
            (id, token_hash, purpose, collection_name, record_id,
             created_at, expires_at, payload)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, hash, string(in.Purpose), in.CollectionName, in.RecordID,
		now, expires, nullableJSON(payloadBytes),
	); err != nil {
		return "", nil, fmt.Errorf("recordtoken: insert: %w", err)
	}
	return tok, &Record{
		ID:             id,
		Purpose:        in.Purpose,
		CollectionName: in.CollectionName,
		RecordID:       in.RecordID,
		CreatedAt:      now,
		ExpiresAt:      expires,
		Payload:        in.Payload,
	}, nil
}

// Consume atomically validates the raw token and marks it consumed.
// Returns the Record on success. ErrNotFound means missing /
// expired / already-consumed — the caller should NOT distinguish.
//
// Caller MUST pass the same Purpose the token was issued with;
// passing the wrong purpose is treated as ErrNotFound so a verify
// token can't be replayed as a reset token (defence-in-depth).
func (s *Store) Consume(ctx context.Context, tok token.Token, purpose Purpose) (*Record, error) {
	hash := token.Compute(tok, s.secret)
	now := clock.Now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("recordtoken: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const sel = `
        SELECT id, purpose, collection_name, record_id, created_at,
               expires_at, payload
          FROM _record_tokens
         WHERE token_hash = $1
           AND consumed_at IS NULL
           AND expires_at > $2
         FOR UPDATE
    `
	var r Record
	var purposeStr string
	var payloadBytes []byte
	if err := tx.QueryRow(ctx, sel, hash, now).Scan(
		&r.ID, &purposeStr, &r.CollectionName, &r.RecordID,
		&r.CreatedAt, &r.ExpiresAt, &payloadBytes,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("recordtoken: select: %w", err)
	}
	r.Purpose = Purpose(purposeStr)
	if r.Purpose != purpose {
		// Different purpose → behave as if the token doesn't exist.
		// We deliberately do NOT consume the row so the legitimate
		// flow can still complete.
		return nil, ErrNotFound
	}
	if len(payloadBytes) > 0 {
		var pl map[string]any
		if err := json.Unmarshal(payloadBytes, &pl); err == nil {
			r.Payload = pl
		}
	}

	const upd = `UPDATE _record_tokens SET consumed_at = $2 WHERE id = $1`
	if _, err := tx.Exec(ctx, upd, r.ID, now); err != nil {
		return nil, fmt.Errorf("recordtoken: mark consumed: %w", err)
	}
	consumed := now
	r.ConsumedAt = &consumed

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("recordtoken: commit: %w", err)
	}
	return &r, nil
}

// RevokeAllFor invalidates every unconsumed token of the given
// purpose for (collection, record). Used by password-reset flows to
// kill old reset links when a new one is requested.
//
// Returns the number of rows revoked. Idempotent — calling twice
// returns 0 the second time.
func (s *Store) RevokeAllFor(ctx context.Context, purpose Purpose, collectionName string, recordID uuid.UUID) (int64, error) {
	const q = `
        UPDATE _record_tokens
           SET consumed_at = $4
         WHERE purpose = $1
           AND collection_name = $2
           AND record_id = $3
           AND consumed_at IS NULL
    `
	tag, err := s.pool.Exec(ctx, q, string(purpose), collectionName, recordID, clock.Now())
	if err != nil {
		return 0, fmt.Errorf("recordtoken: revoke: %w", err)
	}
	return tag.RowsAffected(), nil
}

// nullableJSON returns nil for empty payloads so PG stores NULL
// rather than a `null` JSONB literal — keeps the column readable in
// admin UI and SELECT * dumps.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
