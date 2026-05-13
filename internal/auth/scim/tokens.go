// Package scim implements RFC 7643 + 7644 — SCIM 2.0 for inbound
// provisioning from external IdPs (Okta / Azure AD / OneLogin /
// Auth0).
//
// SCIM is one-way: the IdP is the source of truth, Railbase is the
// downstream consumer. The IdP POSTs / PATCHes /scim/v2/Users and
// /scim/v2/Groups, Railbase translates each operation into:
//
//	Users  → INSERT/UPDATE/DELETE in the configured auth-collection
//	Groups → INSERT/UPDATE/DELETE in _scim_groups + sync of
//	         _scim_group_members + reconcile of _user_roles via
//	         _scim_group_role_map (set by the operator separately;
//	         SCIM doesn't carry role information)
//
// Authentication: every /scim/v2/* request carries an
// `Authorization: Bearer rbsm_<token>` header. The token format
// mirrors v1.7.3 API tokens (rbat_-prefix): a 43-char base64url
// secret stored as HMAC-SHA-256 under the master key. The prefix is
// distinct (`rbsm_`) so middleware can route to the SCIM token store
// without poking the apitoken store and vice-versa.
//
// Why a separate token store from apitoken:
//
//   - Different audience. API tokens authenticate as a user with that
//     user's RBAC permissions. SCIM tokens authenticate as a SCIM
//     client — the operations they can perform are bounded by the
//     SCIM protocol, NOT a Railbase user's role set. An apitoken
//     belonging to a user with `users:read` could in theory list
//     users, but SCIM clients need to CREATE / DELETE users, which
//     requires admin-level RBAC. Conflating the two would require
//     either escalating every SCIM token to admin (over-broad) or
//     adding SCIM-specific scope checks to the apitoken store
//     (complicates a clean primitive).
//
//   - Different rotation cadence. SCIM tokens are owned by an external
//     IdP that has its own rotation policy. Operators rotate them by
//     wizard-clicking "Rotate" in the SCIM panel; the IdP picks up
//     the new value out-of-band. API tokens are owned by Railbase
//     users + rotated via CLI.
//
// This file is the token store. The HTTP surface lives in
// internal/api/scim.

package scim

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/clock"
)

// TokenPrefix marks the wire format of a SCIM bearer credential.
// Distinct from apitoken's `rbat_` so the auth middleware can route
// without ambiguity.
const TokenPrefix = "rbsm_"

// DefaultTokenTTL bounds a fresh SCIM token's lifetime to 1 year. The
// operator can pass zero TTL for "never expires" but that's rarely
// the right call — IdPs that lose their token but never get rotated
// silently fail provisioning for weeks.
const DefaultTokenTTL = 365 * 24 * time.Hour

// ErrTokenNotFound collapses every "this token can't authenticate"
// case — missing, expired, revoked. Callers MUST NOT distinguish
// (prevents probing the token table for existence).
var ErrTokenNotFound = errors.New("scim: token not found, expired, or revoked")

// Token is the in-memory representation of a SCIM bearer credential
// row. Raw value is NEVER persisted — only the HMAC hash.
type Token struct {
	ID         uuid.UUID
	Name       string
	Collection string
	Scopes     []string
	CreatedAt  time.Time
	CreatedBy  *uuid.UUID
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// TokenStore persists + authenticates SCIM tokens. Use NewTokenStore
// on boot.
type TokenStore struct {
	pool   *pgxpool.Pool
	secret secret.Key
}

// NewTokenStore returns a TokenStore. Master key MUST be the same
// secret.Key the rest of the auth surface uses.
func NewTokenStore(pool *pgxpool.Pool, key secret.Key) *TokenStore {
	return &TokenStore{pool: pool, secret: key}
}

// CreateInput captures everything Create needs.
type CreateInput struct {
	Name       string     // operator-readable label e.g. "okta-prod"
	Collection string     // target auth-collection (e.g. "users")
	Scopes     []string   // empty = full SCIM access
	CreatedBy  *uuid.UUID // admin id; nil for system / setup-wizard
	TTL        time.Duration
}

// Create issues a new SCIM token. Returns the RAW token (display
// once; persist on operator side only — Railbase forgets it after
// this call). Subsequent operations work via the in-memory Token
// struct.
func (s *TokenStore) Create(ctx context.Context, in CreateInput) (string, *Token, error) {
	if strings.TrimSpace(in.Name) == "" {
		return "", nil, fmt.Errorf("scim: token name is required")
	}
	if strings.TrimSpace(in.Collection) == "" {
		return "", nil, fmt.Errorf("scim: collection is required")
	}
	raw, err := generateRawToken()
	if err != nil {
		return "", nil, err
	}
	hash := s.hash(raw)

	now := clock.Now()
	var expiresAt *time.Time
	if in.TTL > 0 {
		t := now.Add(in.TTL)
		expiresAt = &t
	}
	id := uuid.Must(uuid.NewV7())
	scopes := in.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	const q = `
        INSERT INTO _scim_tokens (id, name, token_hash, collection, scopes,
                                  created_at, created_by, expires_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, in.Name, hash, in.Collection, scopes, now, in.CreatedBy, expiresAt,
	); err != nil {
		return "", nil, fmt.Errorf("scim: insert token: %w", err)
	}
	return raw, &Token{
		ID:         id,
		Name:       in.Name,
		Collection: in.Collection,
		Scopes:     scopes,
		CreatedAt:  now,
		CreatedBy:  in.CreatedBy,
		ExpiresAt:  expiresAt,
	}, nil
}

// Authenticate resolves a wire-format token. Bumps last_used_at on
// success — used for "stale token" auditing in the admin UI.
func (s *TokenStore) Authenticate(ctx context.Context, raw string) (*Token, error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return nil, ErrTokenNotFound
	}
	hash := s.hash(raw)
	now := clock.Now()
	const q = `
        UPDATE _scim_tokens
           SET last_used_at = $2
         WHERE token_hash    = $1
           AND revoked_at    IS NULL
           AND (expires_at   IS NULL OR expires_at > $2)
        RETURNING id, name, collection, scopes,
                  created_at, created_by, last_used_at, expires_at, revoked_at
    `
	var t Token
	err := s.pool.QueryRow(ctx, q, hash, now).Scan(
		&t.ID, &t.Name, &t.Collection, &t.Scopes,
		&t.CreatedAt, &t.CreatedBy, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scim: authenticate: %w", err)
	}
	return &t, nil
}

// List returns every (alive + revoked) SCIM token for a collection,
// most-recent first. For the admin UI's listing view.
func (s *TokenStore) List(ctx context.Context, collection string, includeRevoked bool) ([]*Token, error) {
	var q string
	if includeRevoked {
		q = `SELECT id, name, collection, scopes, created_at, created_by,
		            last_used_at, expires_at, revoked_at
		       FROM _scim_tokens
		      WHERE collection = $1
		      ORDER BY created_at DESC`
	} else {
		q = `SELECT id, name, collection, scopes, created_at, created_by,
		            last_used_at, expires_at, revoked_at
		       FROM _scim_tokens
		      WHERE collection = $1 AND revoked_at IS NULL
		      ORDER BY created_at DESC`
	}
	rows, err := s.pool.Query(ctx, q, collection)
	if err != nil {
		return nil, fmt.Errorf("scim: list tokens: %w", err)
	}
	defer rows.Close()
	out := []*Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Collection, &t.Scopes,
			&t.CreatedAt, &t.CreatedBy, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// Revoke marks a token revoked. Returns ErrTokenNotFound when no row
// matches the id; the caller's handler should respond 204 either way
// — the goal (token unusable) is achieved.
func (s *TokenStore) Revoke(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE _scim_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, id, clock.Now())
	if err != nil {
		return fmt.Errorf("scim: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// Rotate revokes the current token + creates a successor. Returns the
// new raw token string. The operator distributes it to the IdP +
// confirms switchover; both old + new authenticate during the
// overlap so SCIM ops don't fail mid-rotation.
func (s *TokenStore) Rotate(ctx context.Context, id uuid.UUID, ttl time.Duration) (string, *Token, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("scim: rotate begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var old Token
	const sel = `SELECT id, name, collection, scopes, created_at, created_by, expires_at, revoked_at
	               FROM _scim_tokens WHERE id = $1`
	if err := tx.QueryRow(ctx, sel, id).Scan(
		&old.ID, &old.Name, &old.Collection, &old.Scopes,
		&old.CreatedAt, &old.CreatedBy, &old.ExpiresAt, &old.RevokedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, ErrTokenNotFound
		}
		return "", nil, err
	}
	if old.RevokedAt != nil {
		return "", nil, ErrTokenNotFound
	}

	raw, err := generateRawToken()
	if err != nil {
		return "", nil, err
	}
	hash := s.hash(raw)
	now := clock.Now()
	var expiresAt *time.Time
	if ttl > 0 {
		t := now.Add(ttl)
		expiresAt = &t
	}
	newID := uuid.Must(uuid.NewV7())
	const ins = `
        INSERT INTO _scim_tokens (id, name, token_hash, collection, scopes,
                                  created_at, created_by, expires_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    `
	if _, err := tx.Exec(ctx, ins,
		newID, old.Name+" (rotated)", hash, old.Collection, old.Scopes,
		now, old.CreatedBy, expiresAt,
	); err != nil {
		return "", nil, err
	}
	// Schedule old to expire in 1h so overlap is bounded.
	overlap := now.Add(1 * time.Hour)
	const updOld = `UPDATE _scim_tokens SET expires_at = $2 WHERE id = $1`
	if _, err := tx.Exec(ctx, updOld, id, overlap); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}
	return raw, &Token{
		ID:         newID,
		Name:       old.Name + " (rotated)",
		Collection: old.Collection,
		Scopes:     old.Scopes,
		CreatedAt:  now,
		CreatedBy:  old.CreatedBy,
		ExpiresAt:  expiresAt,
	}, nil
}

// hash is the keyed HMAC over the raw token. We use the master key
// (same as session tokens) so DB exfiltration alone doesn't yield
// forgeable tokens.
func (s *TokenStore) hash(raw string) []byte {
	mac := hmac.New(sha256.New, s.secret.HMAC())
	mac.Write([]byte(raw))
	return mac.Sum(nil)
}

// generateRawToken returns a `rbsm_<43-char-base64url>` string. 32
// random bytes = 256 bits of entropy.
func generateRawToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("scim: random: %w", err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
