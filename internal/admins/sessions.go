package admins

import (
	"context"
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

// SessionTTL and SessionHardCap match the application-user defaults.
// docs/12-admin-ui.md says:
//
//	"Session timeout: 8 hours sliding window (refresh on activity)"
//
// We keep the values aligned with internal/auth/session so admins
// don't get a more lenient surface than regular users.
const (
	SessionTTL     = 8 * time.Hour
	SessionHardCap = 30 * 24 * time.Hour
)

// ErrSessionNotFound is returned when no admin session row matches
// the lookup token (missing, expired, revoked, or past the hard cap).
// All four collapse into one error so the API can't be probed.
var ErrSessionNotFound = errors.New("admin session: not found or expired")

// Session is one row from `_admin_sessions`. AdminID is the foreign
// key into `_admins`; the rest mirrors application sessions.
type Session struct {
	ID           uuid.UUID
	AdminID      uuid.UUID
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	IP           string
	UserAgent    string
}

// SessionStore is the persistence handle for admin sessions. Held
// alongside (NewSessionStore) the regular admins.Store on app boot
// so the admin auth middleware can look up tokens without indirecting
// through a higher-level service.
type SessionStore struct {
	pool   *pgxpool.Pool
	secret secret.Key
}

// NewSessionStore returns a store. Pass the same master Key the
// application sessions use — admins ride on the same HMAC secret so
// rotating it invalidates BOTH user and admin sessions in lockstep.
func NewSessionStore(pool *pgxpool.Pool, key secret.Key) *SessionStore {
	return &SessionStore{pool: pool, secret: key}
}

// CreateSessionInput captures everything Create needs. TTL=0 means
// SessionTTL.
type CreateSessionInput struct {
	AdminID   uuid.UUID
	IP        string
	UserAgent string
	TTL       time.Duration
}

// Create issues a new admin session. Returns the raw token (send to
// client; never persisted) and the persisted Session row.
func (s *SessionStore) Create(ctx context.Context, in CreateSessionInput) (token.Token, *Session, error) {
	tok, err := token.Generate()
	if err != nil {
		return "", nil, err
	}
	hash := token.Compute(tok, s.secret)

	ttl := in.TTL
	if ttl <= 0 {
		ttl = SessionTTL
	}
	now := clock.Now()
	expires := now.Add(ttl)
	id := uuid.Must(uuid.NewV7())

	const q = `
        INSERT INTO _admin_sessions (id, admin_id, token_hash,
                                     created_at, last_active_at, expires_at,
                                     ip, user_agent)
        VALUES ($1, $2, $3, $4, $4, $5, $6, $7)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, in.AdminID, hash,
		now, expires,
		nullIfEmpty(in.IP), nullIfEmpty(in.UserAgent),
	); err != nil {
		return "", nil, fmt.Errorf("admin session: insert: %w", err)
	}
	return tok, &Session{
		ID:           id,
		AdminID:      in.AdminID,
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    expires,
		IP:           in.IP,
		UserAgent:    in.UserAgent,
	}, nil
}

// Lookup resolves a wire-format token to a Session and slides the
// expiry forward by SessionTTL. Single UPDATE...RETURNING — no tx
// required.
func (s *SessionStore) Lookup(ctx context.Context, tok token.Token) (*Session, error) {
	hash := token.Compute(tok, s.secret)
	now := clock.Now()
	newExpires := now.Add(SessionTTL)

	const q = `
        UPDATE _admin_sessions
           SET last_active_at = $2,
               expires_at     = LEAST(
                   $3,
                   created_at + make_interval(secs => $4)
               )
         WHERE token_hash    = $1
           AND revoked_at    IS NULL
           AND expires_at    > $2
           AND created_at    > $2 - make_interval(secs => $4)
        RETURNING id, admin_id, created_at, last_active_at, expires_at,
                  COALESCE(ip, ''), COALESCE(user_agent, '')
    `
	var sess Session
	err := s.pool.QueryRow(ctx, q, hash, now, newExpires, int64(SessionHardCap.Seconds())).Scan(
		&sess.ID, &sess.AdminID,
		&sess.CreatedAt, &sess.LastActiveAt, &sess.ExpiresAt,
		&sess.IP, &sess.UserAgent,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("admin session: lookup: %w", err)
	}
	return &sess, nil
}

// Refresh rotates the token. Mirrors session.Store.Refresh — same tx
// shape, same hard-cap enforcement.
func (s *SessionStore) Refresh(ctx context.Context, oldTok token.Token, ip, userAgent string) (token.Token, *Session, error) {
	oldHash := token.Compute(oldTok, s.secret)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("admin session: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := clock.Now()
	const findOld = `
        UPDATE _admin_sessions
           SET revoked_at = $2
         WHERE token_hash = $1
           AND revoked_at IS NULL
           AND expires_at > $2
        RETURNING admin_id, created_at
    `
	var adminID uuid.UUID
	var oldCreated time.Time
	if err := tx.QueryRow(ctx, findOld, oldHash, now).Scan(&adminID, &oldCreated); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, ErrSessionNotFound
		}
		return "", nil, fmt.Errorf("admin session: revoke old: %w", err)
	}
	if now.Sub(oldCreated) > SessionHardCap {
		return "", nil, ErrSessionNotFound
	}

	newTok, err := token.Generate()
	if err != nil {
		return "", nil, err
	}
	newHash := token.Compute(newTok, s.secret)
	newID := uuid.Must(uuid.NewV7())
	expires := now.Add(SessionTTL)
	const ins = `
        INSERT INTO _admin_sessions (id, admin_id, token_hash,
                                     created_at, last_active_at, expires_at,
                                     ip, user_agent)
        VALUES ($1, $2, $3, $4, $4, $5, $6, $7)
    `
	if _, err := tx.Exec(ctx, ins,
		newID, adminID, newHash,
		now, expires, nullIfEmpty(ip), nullIfEmpty(userAgent),
	); err != nil {
		return "", nil, fmt.Errorf("admin session: insert refreshed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("admin session: commit: %w", err)
	}
	return newTok, &Session{
		ID: newID, AdminID: adminID,
		CreatedAt: now, LastActiveAt: now, ExpiresAt: expires,
		IP: ip, UserAgent: userAgent,
	}, nil
}

// RevokeAllFor soft-deletes every live session belonging to the admin.
// Used after a successful password reset: invalidates any cookie that
// might be in the hands of an attacker who phished the old password.
// Returns the number of sessions revoked (zero is fine — no live
// session is not an error).
func (s *SessionStore) RevokeAllFor(ctx context.Context, adminID uuid.UUID) (int64, error) {
	const q = `UPDATE _admin_sessions SET revoked_at = $2 WHERE admin_id = $1 AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, adminID, clock.Now())
	if err != nil {
		return 0, fmt.Errorf("admin session: revoke-all: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Revoke soft-deletes a session. Idempotent — the handler should
// return 204 either way.
func (s *SessionStore) Revoke(ctx context.Context, tok token.Token) error {
	hash := token.Compute(tok, s.secret)
	const q = `UPDATE _admin_sessions SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, hash, clock.Now())
	if err != nil {
		return fmt.Errorf("admin session: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
