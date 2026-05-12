// Package apitoken implements long-lived bearer tokens for service-
// to-service authentication. Distinct from session tokens (short-
// lived, browser-issued) and record tokens (single-purpose, single-
// use, email-link payloads).
//
// Wire format: `rbat_<43-char-base64url>` — the `rbat_` prefix is a
// recognisable marker so the auth middleware can route the lookup
// without a wasted session-table query. Stored as HMAC-SHA-256 of
// the raw token under the master key — leaked DB dumps can't be
// used to forge tokens.
//
// Display-once contract: the raw token is returned exactly once
// from Create() (CLI surfaces it to the operator). Subsequent List
// / Get operations expose only metadata + a fingerprint hash so the
// admin UI can tell tokens apart without exposing them.
//
// Rotation: Rotate() creates a successor token linked by
// `rotated_from`. Operators distribute the successor, then revoke
// the predecessor. While both are alive, request handlers MAY emit
// a `Deprecation: true` header on the predecessor's traffic (the
// column exists; the handler-side wiring is tracked as v1.x polish).

package apitoken

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/token"
	"github.com/railbase/railbase/internal/clock"
)

// Prefix is the literal wire-format marker that distinguishes API
// tokens from session tokens. Constant so middleware can compare
// with strings.HasPrefix in O(1).
const Prefix = "rbat_"

// ErrNotFound is returned by Authenticate / Get when no row matches:
// the token doesn't exist, has expired, was revoked, or was rotated
// out. All four collapse into one error so the API surface can't be
// probed.
var ErrNotFound = errors.New("apitoken: not found or expired")

// Record is the persisted token row. RawToken is NEVER stored;
// only the HMAC hash. Get / List return Record without the token
// string so admin UI can browse without leaking.
type Record struct {
	ID              uuid.UUID
	Name            string
	OwnerID         uuid.UUID
	OwnerCollection string
	Scopes          []string
	CreatedAt       time.Time
	ExpiresAt       *time.Time // nil = never expires
	LastUsedAt      *time.Time
	RevokedAt       *time.Time
	RotatedFrom     *uuid.UUID
}

// Store is the persistence handle. Pass the same master Key the rest
// of the auth surface uses — rotating it invalidates every issued
// token in one operation. Goroutine-safe.
type Store struct {
	pool   *pgxpool.Pool
	secret secret.Key
}

// NewStore returns a Store. Call once on boot, share for the lifetime
// of the process.
func NewStore(pool *pgxpool.Pool, key secret.Key) *Store {
	return &Store{pool: pool, secret: key}
}

// CreateInput captures what Create needs.
type CreateInput struct {
	// Name is the human-readable label (e.g. "CI deploy bot").
	// Required — it's the only way an operator can tell tokens apart
	// in the list view.
	Name string
	// OwnerID is the user (or admin) the token impersonates. The
	// token's permissions are bounded by the owner's permissions
	// (token can never exceed its owner).
	OwnerID uuid.UUID
	// OwnerCollection is the auth-collection the owner belongs to.
	// Typically "users"; can be "admins" or any other auth-collection.
	OwnerCollection string
	// Scopes are advisory action-key strings (see internal/rbac). For
	// v1 the middleware doesn't enforce per-scope checks — the token
	// authenticates as the owner with full owner permissions. Scope
	// enforcement is tracked as a v1.x polish slice; the data is
	// captured here so the enforcement layer can land without
	// migration. Empty = "all owner permissions".
	Scopes []string
	// TTL bounds the token's lifetime. Zero = never expires (revoke
	// is the only way to invalidate). Operators following security
	// best practice pass 30*24h.
	TTL time.Duration
}

// Create issues a fresh API token. Returns the RAW token (display
// once to operator, never persist) and the materialised Record for
// audit-side callers.
func (s *Store) Create(ctx context.Context, in CreateInput) (string, *Record, error) {
	if strings.TrimSpace(in.Name) == "" {
		return "", nil, fmt.Errorf("apitoken: name is required")
	}
	if in.OwnerID == uuid.Nil {
		return "", nil, fmt.Errorf("apitoken: owner_id is required")
	}
	if strings.TrimSpace(in.OwnerCollection) == "" {
		return "", nil, fmt.Errorf("apitoken: owner_collection is required")
	}

	rawInner, err := token.Generate()
	if err != nil {
		return "", nil, fmt.Errorf("apitoken: generate: %w", err)
	}
	rawTok := Prefix + string(rawInner)
	hash := computeHash(rawTok, s.secret)

	now := clock.Now()
	id := uuid.Must(uuid.NewV7())
	var expires *time.Time
	if in.TTL > 0 {
		t := now.Add(in.TTL)
		expires = &t
	}

	scopes := in.Scopes
	if scopes == nil {
		scopes = []string{}
	}

	const q = `
        INSERT INTO _api_tokens
            (id, name, token_hash, owner_id, owner_collection,
             scopes, expires_at, created_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, in.Name, hash, in.OwnerID, in.OwnerCollection,
		scopes, expires, now,
	); err != nil {
		return "", nil, fmt.Errorf("apitoken: insert: %w", err)
	}
	return rawTok, &Record{
		ID:              id,
		Name:            in.Name,
		OwnerID:         in.OwnerID,
		OwnerCollection: in.OwnerCollection,
		Scopes:          scopes,
		CreatedAt:       now,
		ExpiresAt:       expires,
	}, nil
}

// Authenticate looks up an active token by its raw value and returns
// the bound Record. Side effect: bumps `last_used_at`. Returns
// ErrNotFound if the token doesn't exist, was revoked, or expired —
// all four collapsed into one error so probes can't distinguish.
//
// Constant-time hash compare is implicit: the DB lookup is by exact
// `token_hash = $1` (a hash collision in HMAC-SHA-256 is
// computationally infeasible, and the UNIQUE constraint makes the
// lookup deterministic).
func (s *Store) Authenticate(ctx context.Context, raw string) (*Record, error) {
	if !strings.HasPrefix(raw, Prefix) {
		return nil, ErrNotFound
	}
	hash := computeHash(raw, s.secret)
	const q = `
        SELECT id, name, owner_id, owner_collection, scopes,
               created_at, expires_at, last_used_at, revoked_at, rotated_from
          FROM _api_tokens
         WHERE token_hash = $1
           AND revoked_at IS NULL
           AND (expires_at IS NULL OR expires_at > now())
         LIMIT 1
    `
	row := s.pool.QueryRow(ctx, q, hash)
	var r Record
	if err := row.Scan(
		&r.ID, &r.Name, &r.OwnerID, &r.OwnerCollection, &r.Scopes,
		&r.CreatedAt, &r.ExpiresAt, &r.LastUsedAt, &r.RevokedAt, &r.RotatedFrom,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("apitoken: scan: %w", err)
	}
	// Best-effort last-used bump. Ignored on error — we're not going
	// to fail the request just because the metadata update raced.
	_, _ = s.pool.Exec(ctx,
		`UPDATE _api_tokens SET last_used_at = now() WHERE id = $1`, r.ID)
	return &r, nil
}

// Get returns a single token by id. Used by the CLI / admin UI for
// the detail view. Does NOT filter by revoked_at — operators
// listing revoked tokens for audit need to see them.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Record, error) {
	const q = `
        SELECT id, name, owner_id, owner_collection, scopes,
               created_at, expires_at, last_used_at, revoked_at, rotated_from
          FROM _api_tokens
         WHERE id = $1
    `
	row := s.pool.QueryRow(ctx, q, id)
	var r Record
	if err := row.Scan(
		&r.ID, &r.Name, &r.OwnerID, &r.OwnerCollection, &r.Scopes,
		&r.CreatedAt, &r.ExpiresAt, &r.LastUsedAt, &r.RevokedAt, &r.RotatedFrom,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("apitoken: get: %w", err)
	}
	return &r, nil
}

// List returns tokens for an owner. Includes revoked tokens so audit
// surfaces can show the full lifecycle. Sorted newest-first.
func (s *Store) List(ctx context.Context, ownerCollection string, ownerID uuid.UUID) ([]*Record, error) {
	const q = `
        SELECT id, name, owner_id, owner_collection, scopes,
               created_at, expires_at, last_used_at, revoked_at, rotated_from
          FROM _api_tokens
         WHERE owner_collection = $1 AND owner_id = $2
         ORDER BY created_at DESC
    `
	rows, err := s.pool.Query(ctx, q, ownerCollection, ownerID)
	if err != nil {
		return nil, fmt.Errorf("apitoken: list: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(
			&r.ID, &r.Name, &r.OwnerID, &r.OwnerCollection, &r.Scopes,
			&r.CreatedAt, &r.ExpiresAt, &r.LastUsedAt, &r.RevokedAt, &r.RotatedFrom,
		); err != nil {
			return nil, fmt.Errorf("apitoken: scan: %w", err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ListAll returns every token across the system. Admin surface only;
// includes revoked. Sorted by owner_collection, owner_id, created_at.
func (s *Store) ListAll(ctx context.Context) ([]*Record, error) {
	const q = `
        SELECT id, name, owner_id, owner_collection, scopes,
               created_at, expires_at, last_used_at, revoked_at, rotated_from
          FROM _api_tokens
         ORDER BY owner_collection, owner_id, created_at DESC
    `
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("apitoken: list-all: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(
			&r.ID, &r.Name, &r.OwnerID, &r.OwnerCollection, &r.Scopes,
			&r.CreatedAt, &r.ExpiresAt, &r.LastUsedAt, &r.RevokedAt, &r.RotatedFrom,
		); err != nil {
			return nil, fmt.Errorf("apitoken: scan: %w", err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// Revoke marks the token revoked. Idempotent — revoking an already-
// revoked token is a no-op (no error). Returns ErrNotFound if the
// id doesn't exist.
func (s *Store) Revoke(ctx context.Context, id uuid.UUID) error {
	const q = `
        UPDATE _api_tokens
           SET revoked_at = COALESCE(revoked_at, now())
         WHERE id = $1
    `
	cmd, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("apitoken: revoke: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Rotate creates a successor token linked to the predecessor via
// `rotated_from`. The predecessor is NOT revoked — operators
// distribute the successor first, then revoke explicitly. Inherits
// name, owner, scopes, and TTL from the predecessor.
//
// Returns the new raw token + the new Record. Returns ErrNotFound
// if the predecessor doesn't exist or is already revoked.
func (s *Store) Rotate(ctx context.Context, predecessorID uuid.UUID, ttl time.Duration) (string, *Record, error) {
	prev, err := s.Get(ctx, predecessorID)
	if err != nil {
		return "", nil, err
	}
	if prev.RevokedAt != nil {
		return "", nil, ErrNotFound
	}

	// Inherit TTL semantics: caller-supplied wins; otherwise reuse the
	// predecessor's remaining TTL; if predecessor never expired, the
	// successor never expires.
	effectiveTTL := ttl
	if effectiveTTL <= 0 && prev.ExpiresAt != nil {
		effectiveTTL = time.Until(*prev.ExpiresAt)
		if effectiveTTL < time.Hour {
			effectiveTTL = 30 * 24 * time.Hour
		}
	}

	rawInner, err := token.Generate()
	if err != nil {
		return "", nil, fmt.Errorf("apitoken: generate: %w", err)
	}
	rawTok := Prefix + string(rawInner)
	hash := computeHash(rawTok, s.secret)

	now := clock.Now()
	id := uuid.Must(uuid.NewV7())
	var expires *time.Time
	if effectiveTTL > 0 {
		t := now.Add(effectiveTTL)
		expires = &t
	}

	const q = `
        INSERT INTO _api_tokens
            (id, name, token_hash, owner_id, owner_collection,
             scopes, expires_at, created_at, rotated_from)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, prev.Name, hash, prev.OwnerID, prev.OwnerCollection,
		prev.Scopes, expires, now, prev.ID,
	); err != nil {
		return "", nil, fmt.Errorf("apitoken: rotate insert: %w", err)
	}
	return rawTok, &Record{
		ID:              id,
		Name:            prev.Name,
		OwnerID:         prev.OwnerID,
		OwnerCollection: prev.OwnerCollection,
		Scopes:          prev.Scopes,
		CreatedAt:       now,
		ExpiresAt:       expires,
		RotatedFrom:     &prev.ID,
	}, nil
}

// computeHash is HMAC-SHA-256(rawToken, masterKey). Distinct from
// `token.Compute` only because the input is a *string* (API tokens
// carry a prefix that's not part of the session-token type). Output
// shape is identical so the storage column type is shared.
func computeHash(raw string, key secret.Key) []byte {
	mac := hmac.New(sha256.New, key.HMAC())
	mac.Write([]byte(raw))
	return mac.Sum(nil)
}

// Fingerprint returns the first 8 chars of a token's hash hex — a
// short label that uniquely identifies a token in the admin UI
// without exposing its content. Stable across the token's lifetime.
func Fingerprint(rawToken string, key secret.Key) string {
	h := computeHash(rawToken, key)
	// 4 bytes hex = 8 chars; UUID-style separator after 4 chars for readability.
	return fmt.Sprintf("%02x%02x%02x%02x", h[0], h[1], h[2], h[3])
}

// Fingerprint is a method-form of the package-level Fingerprint that
// reuses the Store's master key. Lets admin-API handlers compute the
// label without threading secret.Key through every Deps surface.
// Output is identical to Fingerprint(raw, store-key).
func (s *Store) Fingerprint(rawToken string) string {
	return Fingerprint(rawToken, s.secret)
}
