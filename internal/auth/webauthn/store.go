package webauthn

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

// ErrNotFound is returned on lookup miss.
var ErrNotFound = errors.New("webauthn: credential not found")

// Stored is the persisted shape — _webauthn_credentials row + the
// owning collection/record metadata the auth handlers need.
type Stored struct {
	ID             uuid.UUID
	CollectionName string
	RecordID       uuid.UUID
	UserHandle     []byte
	Credential     Credential
	Name           string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

// Store is the persistence handle. Goroutine-safe.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store; share for the process lifetime.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// SaveInput captures what Save needs.
type SaveInput struct {
	CollectionName string
	RecordID       uuid.UUID
	UserHandle     []byte
	Credential     Credential
	Name           string
}

// Save persists a credential. credential_id is UNIQUE — a duplicate
// returns a wrapped pgx unique-violation, which the caller surfaces
// as a 409.
func (s *Store) Save(ctx context.Context, in SaveInput) (*Stored, error) {
	transportsJSON, err := json.Marshal(in.Credential.Transports)
	if err != nil {
		return nil, fmt.Errorf("webauthn: marshal transports: %w", err)
	}
	const q = `
        INSERT INTO _webauthn_credentials
            (collection_name, record_id, credential_id, public_key,
             sign_count, aaguid, transports, user_handle, name)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        RETURNING id, created_at
    `
	var id uuid.UUID
	var createdAt time.Time
	if err := s.pool.QueryRow(ctx, q,
		in.CollectionName, in.RecordID, in.Credential.ID,
		in.Credential.PublicKey, int64(in.Credential.SignCount),
		in.Credential.AAGUID, transportsJSON, in.UserHandle,
		nullableText(in.Name),
	).Scan(&id, &createdAt); err != nil {
		return nil, fmt.Errorf("webauthn: insert: %w", err)
	}
	return &Stored{
		ID:             id,
		CollectionName: in.CollectionName,
		RecordID:       in.RecordID,
		UserHandle:     in.UserHandle,
		Credential:     in.Credential,
		Name:           in.Name,
		CreatedAt:      createdAt,
	}, nil
}

// FindByCredentialID is the auth-ceremony lookup: "who owns this
// credential the browser just used?". Returns ErrNotFound on miss.
func (s *Store) FindByCredentialID(ctx context.Context, credID []byte) (*Stored, error) {
	const q = `
        SELECT id, collection_name, record_id, credential_id, public_key,
               sign_count, aaguid, transports, user_handle,
               COALESCE(name, ''), created_at, last_used_at
          FROM _webauthn_credentials
         WHERE credential_id = $1
         LIMIT 1
    `
	return s.scanOne(s.pool.QueryRow(ctx, q, credID))
}

// ListForRecord returns every credential a user has registered.
// Admin UI uses this; subsequent register-start calls also use it
// to populate excludeCredentials (don't enroll the same authenticator
// twice).
func (s *Store) ListForRecord(ctx context.Context, collectionName string, recordID uuid.UUID) ([]Stored, error) {
	const q = `
        SELECT id, collection_name, record_id, credential_id, public_key,
               sign_count, aaguid, transports, user_handle,
               COALESCE(name, ''), created_at, last_used_at
          FROM _webauthn_credentials
         WHERE collection_name = $1 AND record_id = $2
         ORDER BY created_at ASC
    `
	rows, err := s.pool.Query(ctx, q, collectionName, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stored
	for rows.Next() {
		st, err := s.scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// UpdateSignCount stamps a successful authentication: new sign count
// + last_used_at.
func (s *Store) UpdateSignCount(ctx context.Context, id uuid.UUID, newSignCount uint32) error {
	const q = `
        UPDATE _webauthn_credentials
           SET sign_count = $2, last_used_at = now()
         WHERE id = $1
    `
	_, err := s.pool.Exec(ctx, q, id, int64(newSignCount))
	return err
}

// Delete removes a credential — used by admin UI / user "remove this
// device" UX. ErrNotFound when nothing matched.
func (s *Store) Delete(ctx context.Context, collectionName string, recordID, id uuid.UUID) error {
	const q = `
        DELETE FROM _webauthn_credentials
         WHERE id = $1 AND collection_name = $2 AND record_id = $3
    `
	tag, err := s.pool.Exec(ctx, q, id, collectionName, recordID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LookupOrIssueUserHandle returns the stable per-user handle.
// Called by register-start: if the user has any existing credential,
// reuse its handle; otherwise generate a new 64-byte handle and
// the caller will persist it on the first credential save.
//
// We don't persist the handle separately — it lives only inside
// _webauthn_credentials rows, copied on every save.
func (s *Store) LookupUserHandle(ctx context.Context, collectionName string, recordID uuid.UUID) ([]byte, error) {
	const q = `
        SELECT user_handle FROM _webauthn_credentials
         WHERE collection_name = $1 AND record_id = $2
         LIMIT 1
    `
	var h []byte
	if err := s.pool.QueryRow(ctx, q, collectionName, recordID).Scan(&h); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return h, nil
}

// --- helpers ---

func (s *Store) scanOne(row pgx.Row) (*Stored, error) {
	st, err := s.scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return st, err
}

// scanRow is shared between QueryRow.Scan and rows.Scan paths.
func (s *Store) scanRow(row pgx.Row) (*Stored, error) {
	var st Stored
	var transportsBytes []byte
	var signCount int64
	if err := row.Scan(
		&st.ID, &st.CollectionName, &st.RecordID,
		&st.Credential.ID, &st.Credential.PublicKey,
		&signCount, &st.Credential.AAGUID,
		&transportsBytes, &st.UserHandle,
		&st.Name, &st.CreatedAt, &st.LastUsedAt,
	); err != nil {
		return nil, err
	}
	st.Credential.SignCount = uint32(signCount)
	if len(transportsBytes) > 0 {
		_ = json.Unmarshal(transportsBytes, &st.Credential.Transports)
	}
	return &st, nil
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
