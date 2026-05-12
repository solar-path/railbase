// Package admins is the system-administrator store.
//
// Admins are distinct from auth-collection users: they're created
// via CLI (`railbase admin create`), not via signup; they have
// site-scope access (bypass tenant RLS, manage `_settings`, run
// migrations from the admin UI in v0.8); and there's typically only
// a handful per deployment.
//
// docs/04-identity.md mandates the separation:
//
//	"Application users" в auth collections — created via signup
//	endpoints. "System admins" (_admins) — separate, created via
//	CLI, never part of user-defined schema.
//
// v0.5 surface:
//
//   - Create / Get / List / Delete via Go API
//   - Password verification helper for the future admin signin
//     endpoint (v0.7) and CLI workflows
//
// v0.7 will add HTTP endpoints (`POST /api/admins/auth-with-password`),
// admin sessions (separate `_admin_sessions` table), and 2FA.
package admins

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/password"
)

// ErrNotFound is returned when no admin matches the lookup criteria.
var ErrNotFound = errors.New("admin: not found")

// Admin is one row from `_admins`. PasswordHash is exported only so
// the admins package itself can run Verify; outside callers should
// stick to the Authenticate helper.
type Admin struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Created      time.Time
	Updated      time.Time
	LastLoginAt  *time.Time
}

// Store is the persistence handle. Holds *pgxpool.Pool only — no
// caching yet because admin counts are small (<100 per deployment).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store. Call once on boot, share for process
// lifetime.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create inserts a fresh admin row. Returns the persisted Admin
// with id/created/updated populated. Plaintext password is hashed
// via Argon2id before storage.
//
// Email uniqueness (case-insensitive) is enforced by the
// `_admins_email_idx` index — duplicates surface as a wrapped
// pg conflict (23505) the caller maps to a friendly message.
func (s *Store) Create(ctx context.Context, email, plaintextPW string) (*Admin, error) {
	if email == "" {
		return nil, errors.New("admin: email is required")
	}
	if len(plaintextPW) < 8 {
		return nil, errors.New("admin: password must be at least 8 chars")
	}
	hash, err := password.Hash(plaintextPW)
	if err != nil {
		return nil, fmt.Errorf("admin: hash: %w", err)
	}
	id := uuid.Must(uuid.NewV7())
	const q = `
        INSERT INTO _admins (id, email, password_hash)
        VALUES ($1, $2, $3)
        RETURNING id, email, password_hash, created, updated, last_login_at
    `
	var a Admin
	if err := s.pool.QueryRow(ctx, q, id, email, hash).Scan(
		&a.ID, &a.Email, &a.PasswordHash, &a.Created, &a.Updated, &a.LastLoginAt,
	); err != nil {
		return nil, fmt.Errorf("admin: insert: %w", err)
	}
	return &a, nil
}

// GetByEmail does a case-insensitive lookup. Returns ErrNotFound
// when no row matches.
func (s *Store) GetByEmail(ctx context.Context, email string) (*Admin, error) {
	const q = `
        SELECT id, email, password_hash, created, updated, last_login_at
          FROM _admins
         WHERE lower(email) = lower($1)
    `
	var a Admin
	err := s.pool.QueryRow(ctx, q, email).Scan(
		&a.ID, &a.Email, &a.PasswordHash, &a.Created, &a.Updated, &a.LastLoginAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetByID looks up an admin by primary key. Used by the admin API's
// `/me` and `/auth-refresh` handlers to materialise a record from the
// admin session row.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*Admin, error) {
	const q = `
        SELECT id, email, password_hash, created, updated, last_login_at
          FROM _admins
         WHERE id = $1
    `
	var a Admin
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&a.ID, &a.Email, &a.PasswordHash, &a.Created, &a.Updated, &a.LastLoginAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// List returns every admin ordered by email. v1's admin UI will
// paginate; for v0.5 we expect at most a handful so the full read
// is fine.
func (s *Store) List(ctx context.Context) ([]Admin, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT id, email, password_hash, created, updated, last_login_at
          FROM _admins
         ORDER BY lower(email)
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		var a Admin
		if err := rows.Scan(
			&a.ID, &a.Email, &a.PasswordHash, &a.Created, &a.Updated, &a.LastLoginAt,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Delete removes the admin by id. Returns ErrNotFound if no row
// matched, so callers can surface a clear message instead of a
// silent success.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM _admins WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns how many admin rows exist. Useful for the bootstrap
// flow: when the count is zero, the CLI hints "run `railbase admin
// create` to create the first administrator."
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM _admins`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Authenticate verifies a plaintext password against the stored hash
// for the given email. Returns the Admin on success, ErrNotFound
// when no row matches OR the password is wrong (timing-safe
// collapse). Stamps last_login_at as a side effect.
func (s *Store) Authenticate(ctx context.Context, email, plaintextPW string) (*Admin, error) {
	a, err := s.GetByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		// Run a dummy verify so the response time is similar to the
		// success path — same trick the auth-collection signin uses.
		_ = password.Verify(plaintextPW, dummyHash)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := password.Verify(plaintextPW, a.PasswordHash); err != nil {
		return nil, ErrNotFound
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE _admins SET last_login_at = now() WHERE id = $1`, a.ID); err != nil {
		// Non-fatal — login still succeeds.
	}
	return a, nil
}

// dummyHash is generated once at init so timing-safe paths don't
// pay the Argon2id construction cost on every call. Same trick
// internal/api/auth uses; copied here to keep the package self-
// contained.
var dummyHash string

func init() {
	h, err := password.Hash("__railbase_admins_dummy_constant_time__")
	if err != nil {
		panic("admins: dummy hash: " + err.Error())
	}
	dummyHash = h
}
