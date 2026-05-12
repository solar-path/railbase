// Package externalauths persists OAuth2 / OIDC links — the mapping
// from (provider, provider_user_id) → (collection, user_id).
//
// One row per linked account. Schema lives in 0009_external_auths
// migration. The store is a thin CRUD layer; provisioning policy
// (link-existing-by-email vs create-new) lives in internal/api/auth.
package externalauths

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound signals "no row" for any lookup that misses. Callers
// must distinguish this from real errors and translate to 404 / branch
// to a create path.
var ErrNotFound = errors.New("externalauths: not found")

// Record is the materialised row. RawUserInfo is the JSONB blob from
// the provider's userinfo endpoint at link time — admin UI surfaces
// it; auth flow doesn't read it.
type Record struct {
	ID             uuid.UUID
	CollectionName string
	RecordID       uuid.UUID
	Provider       string
	ProviderUserID string
	Email          string
	RawUserInfo    map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store is the persistence handle. Goroutine-safe.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store. One per process.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// FindByProviderUID locates a link by (provider, provider_user_id) —
// the index we consult on every callback to answer "is this provider
// account already linked to a Railbase user?".
func (s *Store) FindByProviderUID(ctx context.Context, provider, providerUserID string) (*Record, error) {
	const q = `
        SELECT id, collection_name, record_id, provider, provider_user_id,
               COALESCE(email, ''), raw_user_info, created_at, updated_at
          FROM _external_auths
         WHERE provider = $1 AND provider_user_id = $2
         LIMIT 1
    `
	row := s.pool.QueryRow(ctx, q, provider, providerUserID)
	var r Record
	var raw []byte
	if err := row.Scan(&r.ID, &r.CollectionName, &r.RecordID, &r.Provider,
		&r.ProviderUserID, &r.Email, &raw, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("externalauths: lookup: %w", err)
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r.RawUserInfo)
	}
	return &r, nil
}

// ListForRecord returns every link belonging to a single user — admin
// UI uses this to render "connected accounts: [Google] [GitHub]".
func (s *Store) ListForRecord(ctx context.Context, collectionName string, recordID uuid.UUID) ([]Record, error) {
	const q = `
        SELECT id, collection_name, record_id, provider, provider_user_id,
               COALESCE(email, ''), raw_user_info, created_at, updated_at
          FROM _external_auths
         WHERE collection_name = $1 AND record_id = $2
         ORDER BY provider ASC
    `
	rows, err := s.pool.Query(ctx, q, collectionName, recordID)
	if err != nil {
		return nil, fmt.Errorf("externalauths: list: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var raw []byte
		if err := rows.Scan(&r.ID, &r.CollectionName, &r.RecordID, &r.Provider,
			&r.ProviderUserID, &r.Email, &raw, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &r.RawUserInfo)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LinkInput captures what Link needs to persist (or update) a row.
type LinkInput struct {
	CollectionName string
	RecordID       uuid.UUID
	Provider       string
	ProviderUserID string
	Email          string
	RawUserInfo    map[string]any
}

// Link inserts a new (collection, record, provider) row or updates the
// existing one when the user re-authenticates (refreshes the cached
// email + raw_user_info).
//
// The upsert key is the owner index (collection_name, record_id,
// provider) — same user, same provider, only one row. Provider-side
// uniqueness (provider, provider_user_id) is enforced by the DB
// constraint and surfaces as 23505 if someone tries to claim an
// already-linked provider identity for a different user.
func (s *Store) Link(ctx context.Context, in LinkInput) (*Record, error) {
	var raw []byte
	if in.RawUserInfo != nil {
		b, err := json.Marshal(in.RawUserInfo)
		if err != nil {
			return nil, fmt.Errorf("externalauths: marshal raw_user_info: %w", err)
		}
		raw = b
	}
	const q = `
        INSERT INTO _external_auths
            (collection_name, record_id, provider, provider_user_id, email, raw_user_info)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (collection_name, record_id, provider) DO UPDATE
            SET provider_user_id = EXCLUDED.provider_user_id,
                email            = EXCLUDED.email,
                raw_user_info    = EXCLUDED.raw_user_info,
                updated_at       = now()
        RETURNING id, collection_name, record_id, provider, provider_user_id,
                  COALESCE(email, ''), raw_user_info, created_at, updated_at
    `
	row := s.pool.QueryRow(ctx, q,
		in.CollectionName, in.RecordID, in.Provider, in.ProviderUserID,
		nullableEmail(in.Email), nullableJSON(raw))
	var r Record
	var rawOut []byte
	if err := row.Scan(&r.ID, &r.CollectionName, &r.RecordID, &r.Provider,
		&r.ProviderUserID, &r.Email, &rawOut, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, fmt.Errorf("externalauths: upsert: %w", err)
	}
	if len(rawOut) > 0 {
		_ = json.Unmarshal(rawOut, &r.RawUserInfo)
	}
	return &r, nil
}

// Unlink deletes a single (collection, record, provider) link — used
// by the admin UI's "disconnect provider" action. Idempotent: zero
// rows affected returns nil (the link is gone whether we deleted it
// or it never existed).
func (s *Store) Unlink(ctx context.Context, collectionName string, recordID uuid.UUID, provider string) error {
	const q = `
        DELETE FROM _external_auths
         WHERE collection_name = $1 AND record_id = $2 AND provider = $3
    `
	_, err := s.pool.Exec(ctx, q, collectionName, recordID, provider)
	if err != nil {
		return fmt.Errorf("externalauths: unlink: %w", err)
	}
	return nil
}

// nullableEmail returns nil for empty so PG stores NULL — GitHub users
// who hide their email shouldn't have an "" sentinel row.
func nullableEmail(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableJSON returns nil for empty payloads.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
