// Package session is the persistence layer for `_sessions`.
//
// Lifecycle:
//
//	Create  — POST auth-with-password / OIDC callback / signup with
//	          auto-signin. Returns the raw token; only emit it once.
//	Lookup  — middleware on every authenticated request. Bumps
//	          last_active_at and slides expires_at on success.
//	Refresh — explicit POST auth-refresh. Rotates the token (issues a
//	          new one, revokes the old) so a leaked token has bounded
//	          window of usefulness.
//	Revoke  — POST auth-logout (current session) and admin tooling
//	          (sign out all devices for user X). Soft revocation —
//	          revoked_at gets stamped; the row stays for audit.
//
// Why HMAC-SHA-256 instead of bcrypt/argon2 for tokens: the token IS
// already 256 bits of entropy. We don't need a slow KDF — we need a
// keyed hash so a leaked DB doesn't expose forgable session IDs.
package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/token"
	"github.com/railbase/railbase/internal/clock"
)

// DefaultTTL is the sliding window length per docs/04-identity.md
// "Refresh-on-use sliding window (default 8h, configurable)".
const DefaultTTL = 8 * time.Hour

// HardCap bounds the *total* lifetime of a single session row,
// independent of how often it's refreshed. After 30 days the user
// must re-authenticate even on an otherwise-active session. Docs are
// silent on the exact value — 30 days is the v0.3.2 default; v1
// surfaces it via _settings.
const HardCap = 30 * 24 * time.Hour

// ErrNotFound is returned when no row matches the token hash, the
// session has expired, or it was revoked. Callers MUST collapse all
// three into a single "not authenticated" response — distinguishing
// them leaks information about the user base.
var ErrNotFound = errors.New("session: not found or expired")

// Session is one row from `_sessions`. We expose it instead of a
// reduced view so middleware can decide which fields to surface in
// downstream context.Value (auth.id, auth.collection are the
// minimum; ip/user_agent help debugging).
//
// v0.4.3 — DeviceName + IsTrusted added for the account-page
// "rename device" / "mark trusted" controls. Pre-migration sessions
// have DeviceName == "" + IsTrusted == false (column defaults).
// Trust enforcement at signin (skip 2FA on trusted devices, extend
// TTL, etc.) is deferred to v0.5; today these are persisted user
// intent only.
type Session struct {
	ID             uuid.UUID
	CollectionName string
	UserID         uuid.UUID
	CreatedAt      time.Time
	LastActiveAt   time.Time
	ExpiresAt      time.Time
	IP             string
	UserAgent      string
	DeviceName     string
	IsTrusted      bool
}

// Store is the session persistence handle. Holds the master secret
// in-memory so callers can pass plain *Token without re-binding the
// secret per call.
type Store struct {
	pool   *pgxpool.Pool
	secret secret.Key
}

// NewStore returns a Store. pool MUST be the same pgxpool the rest
// of the app uses — sessions ride on the same connection lifecycle.
func NewStore(pool *pgxpool.Pool, key secret.Key) *Store {
	return &Store{pool: pool, secret: key}
}

// CreateInput captures everything Create needs from an HTTP handler.
// Caller fills CollectionName, UserID, and the request-derived IP /
// UserAgent. TTL defaults to DefaultTTL when zero.
type CreateInput struct {
	CollectionName string
	UserID         uuid.UUID
	IP             string
	UserAgent      string
	TTL            time.Duration
}

// Create issues a new session. Returns the raw token (to send to the
// client) and the persisted Session. Only this call can produce a
// raw token; subsequent lookups return Session without it.
func (s *Store) Create(ctx context.Context, in CreateInput) (token.Token, *Session, error) {
	tok, err := token.Generate()
	if err != nil {
		return "", nil, err
	}
	hash := token.Compute(tok, s.secret)

	ttl := in.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := clock.Now()
	expires := now.Add(ttl)

	id := uuid.Must(uuid.NewV7())
	const q = `
        INSERT INTO _sessions (id, collection_name, user_id, token_hash,
                               created_at, last_active_at, expires_at,
                               ip, user_agent)
        VALUES ($1, $2, $3, $4, $5, $5, $6, $7, $8)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, in.CollectionName, in.UserID, hash,
		now, expires,
		nullIfEmpty(in.IP), nullIfEmpty(in.UserAgent),
	); err != nil {
		return "", nil, fmt.Errorf("session: insert: %w", err)
	}
	return tok, &Session{
		ID:             id,
		CollectionName: in.CollectionName,
		UserID:         in.UserID,
		CreatedAt:      now,
		LastActiveAt:   now,
		ExpiresAt:      expires,
		IP:             in.IP,
		UserAgent:      in.UserAgent,
	}, nil
}

// Lookup resolves a wire-format token to a Session, sliding the
// expires_at window forward by DefaultTTL on success. The slide is
// done in the same UPDATE that fetches the row so we don't need a
// transaction.
//
// Returns ErrNotFound for: missing row, expired session, revoked
// session, exceeded HardCap.
func (s *Store) Lookup(ctx context.Context, tok token.Token) (*Session, error) {
	hash := token.Compute(tok, s.secret)
	now := clock.Now()
	newExpires := now.Add(DefaultTTL)

	// make_interval(secs => $4) is the unambiguous form. The earlier
	// `($4 || ' seconds')::interval` only works if $4 is a string;
	// passing an int64 makes Postgres reject the concat.
	const q = `
        UPDATE _sessions
           SET last_active_at = $2,
               expires_at     = LEAST(
                   $3,
                   created_at + make_interval(secs => $4)
               )
         WHERE token_hash    = $1
           AND revoked_at    IS NULL
           AND expires_at    > $2
           AND created_at    > $2 - make_interval(secs => $4)
        RETURNING id, collection_name, user_id, created_at,
                  last_active_at, expires_at,
                  COALESCE(ip, ''), COALESCE(user_agent, '')
    `
	var sess Session
	err := s.pool.QueryRow(ctx, q, hash, now, newExpires, int64(HardCap.Seconds())).Scan(
		&sess.ID, &sess.CollectionName, &sess.UserID,
		&sess.CreatedAt, &sess.LastActiveAt, &sess.ExpiresAt,
		&sess.IP, &sess.UserAgent,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session: lookup: %w", err)
	}
	return &sess, nil
}

// Refresh rotates the token: revokes the row matching `oldTok` and
// inserts a fresh one bound to the same user. Returns the new raw
// token and the new Session. Atomic via a single tx so a crash
// mid-refresh leaves either the old session intact OR the new one in
// place — never both, never neither.
func (s *Store) Refresh(ctx context.Context, oldTok token.Token, ip, userAgent string) (token.Token, *Session, error) {
	oldHash := token.Compute(oldTok, s.secret)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("session: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := clock.Now()
	const findOld = `
        UPDATE _sessions
           SET revoked_at = $2
         WHERE token_hash = $1
           AND revoked_at IS NULL
           AND expires_at > $2
        RETURNING collection_name, user_id, created_at
    `
	var collName string
	var userID uuid.UUID
	var oldCreated time.Time
	if err := tx.QueryRow(ctx, findOld, oldHash, now).Scan(&collName, &userID, &oldCreated); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, ErrNotFound
		}
		return "", nil, fmt.Errorf("session: revoke old: %w", err)
	}

	// Honour the hard cap: refusing to refresh a session that's been
	// alive longer than HardCap forces a real re-authentication.
	if now.Sub(oldCreated) > HardCap {
		return "", nil, ErrNotFound
	}

	newTok, err := token.Generate()
	if err != nil {
		return "", nil, err
	}
	newHash := token.Compute(newTok, s.secret)
	newID := uuid.Must(uuid.NewV7())
	expires := now.Add(DefaultTTL)
	const ins = `
        INSERT INTO _sessions (id, collection_name, user_id, token_hash,
                               created_at, last_active_at, expires_at,
                               ip, user_agent)
        VALUES ($1, $2, $3, $4, $5, $5, $6, $7, $8)
    `
	if _, err := tx.Exec(ctx, ins,
		newID, collName, userID, newHash,
		now, expires, nullIfEmpty(ip), nullIfEmpty(userAgent),
	); err != nil {
		return "", nil, fmt.Errorf("session: insert refreshed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("session: commit: %w", err)
	}
	return newTok, &Session{
		ID: newID, CollectionName: collName, UserID: userID,
		CreatedAt: now, LastActiveAt: now, ExpiresAt: expires,
		IP: ip, UserAgent: userAgent,
	}, nil
}

// Revoke marks a session revoked. Returns ErrNotFound if no row
// matches; the handler should respond 204 anyway since the goal
// (token unusable) is achieved either way.
func (s *Store) Revoke(ctx context.Context, tok token.Token) error {
	hash := token.Compute(tok, s.secret)
	const q = `UPDATE _sessions SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, hash, clock.Now())
	if err != nil {
		return fmt.Errorf("session: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAllFor marks EVERY live session for the (collection, user)
// tuple revoked. Returns the count revoked. v1.7.50.2 — backs the
// SAML SLO (Single Logout) endpoint: when the IdP tells us a user
// has signed out globally, we drop every session we issued for them
// so they have to re-authenticate on next access.
//
// Idempotent: a second call returns 0 + nil; rows already revoked
// are skipped by the `revoked_at IS NULL` filter.
func (s *Store) RevokeAllFor(ctx context.Context, collectionName string, userID uuid.UUID) (int64, error) {
	const q = `UPDATE _sessions SET revoked_at = $3 WHERE collection_name = $1 AND user_id = $2 AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, collectionName, userID, clock.Now())
	if err != nil {
		return 0, fmt.Errorf("session: revoke-all-for: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListFor returns every LIVE session (not revoked, not expired) for
// the (collection, user) tuple, sorted by last_active_at DESC so the
// "most recently used" device sits at the top of the list. Used by
// the user-facing `GET /api/auth/sessions` endpoint (v0.4.3 — closes
// Sentinel FEEDBACK G4 follow-up: parity with air/rail's "active
// sessions" account screen).
//
// The token hash is intentionally NOT included in the result — surfacing
// it would let a UI bug or hostile XSS exfiltrate the lookup key and
// forge requests against any listed session. The session.ID is the
// stable opaque handle the caller passes back to RevokeByID.
func (s *Store) ListFor(ctx context.Context, collectionName string, userID uuid.UUID) ([]*Session, error) {
	const q = `
        SELECT id, collection_name, user_id, created_at, last_active_at, expires_at,
               COALESCE(ip, '') AS ip, COALESCE(user_agent, '') AS user_agent,
               COALESCE(device_name, '') AS device_name, is_trusted
          FROM _sessions
         WHERE collection_name = $1 AND user_id = $2
           AND revoked_at IS NULL
           AND expires_at > $3
         ORDER BY last_active_at DESC`
	rows, err := s.pool.Query(ctx, q, collectionName, userID, clock.Now())
	if err != nil {
		return nil, fmt.Errorf("session: list-for: %w", err)
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		s := &Session{}
		if err := rows.Scan(&s.ID, &s.CollectionName, &s.UserID, &s.CreatedAt,
			&s.LastActiveAt, &s.ExpiresAt, &s.IP, &s.UserAgent,
			&s.DeviceName, &s.IsTrusted); err != nil {
			return nil, fmt.Errorf("session: list-for scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: list-for rows: %w", err)
	}
	return out, nil
}

// UpdateMetadata writes the user-supplied per-device fields on a
// session row. Both fields are POINTERS — nil means "leave unchanged",
// non-nil means "write this value". Lets the same handler back the
// "rename" + "trust toggle" UI controls (and future ones) without a
// combinatorial endpoint explosion.
//
// Scoped to (collection, user, id) so a caller can't rename someone
// else's session by guessing UUIDs — same posture as RevokeByID.
// Returns ErrNotFound when no live row matches.
func (s *Store) UpdateMetadata(ctx context.Context, collectionName string, userID, sessionID uuid.UUID, deviceName *string, isTrusted *bool) error {
	if deviceName == nil && isTrusted == nil {
		// No-op call — degenerate but harmless. Return nil rather than
		// querying so the partial-update API is forgiving.
		return nil
	}
	// Build the dynamic SET clause. Parameter order: optional name,
	// optional trust, then the three identity keys.
	set := make([]string, 0, 2)
	args := make([]any, 0, 5)
	if deviceName != nil {
		args = append(args, *deviceName)
		set = append(set, fmt.Sprintf("device_name = $%d", len(args)))
	}
	if isTrusted != nil {
		args = append(args, *isTrusted)
		set = append(set, fmt.Sprintf("is_trusted = $%d", len(args)))
	}
	args = append(args, collectionName, userID, sessionID)
	q := fmt.Sprintf(`
        UPDATE _sessions
           SET %s
         WHERE collection_name = $%d AND user_id = $%d AND id = $%d
           AND revoked_at IS NULL`,
		joinSet(set), len(args)-2, len(args)-1, len(args))
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("session: update-metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// joinSet renders ["a = $1", "b = $2"] as `a = $1, b = $2`.
// Standalone helper because strings.Join with ", " is the obvious
// move; pinning it as a named function keeps the UpdateMetadata SQL
// builder readable.
func joinSet(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// RevokeByID soft-revokes a session by primary key, scoped to the
// (collection, user) tuple so a malicious caller can't kill someone
// else's session by guessing UUIDs. Returns ErrNotFound when no live
// row matches — either id is unknown, or it's already revoked, or it
// belongs to another user (all three collapse into one error for the
// same reason Lookup does: don't leak which case).
//
// Backs `DELETE /api/auth/sessions/{id}`.
func (s *Store) RevokeByID(ctx context.Context, collectionName string, userID, sessionID uuid.UUID) error {
	const q = `
        UPDATE _sessions
           SET revoked_at = $4
         WHERE id = $3 AND collection_name = $1 AND user_id = $2
           AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, collectionName, userID, sessionID, clock.Now())
	if err != nil {
		return fmt.Errorf("session: revoke-by-id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeOthers revokes every live session for (collection, user)
// EXCEPT the one whose ID is `keepSessionID`. Returns the count of
// rows revoked. Used by the "sign out everywhere else" account-page
// action and after a password change.
//
// keepSessionID = uuid.Nil revokes every session (identical to
// RevokeAllFor). The explicit overload makes the call site readable
// at the cost of one zero-uuid branch.
func (s *Store) RevokeOthers(ctx context.Context, collectionName string, userID, keepSessionID uuid.UUID) (int64, error) {
	const q = `
        UPDATE _sessions
           SET revoked_at = $4
         WHERE collection_name = $1 AND user_id = $2
           AND revoked_at IS NULL
           AND id <> $3`
	tag, err := s.pool.Exec(ctx, q, collectionName, userID, keepSessionID, clock.Now())
	if err != nil {
		return 0, fmt.Errorf("session: revoke-others: %w", err)
	}
	return tag.RowsAffected(), nil
}

// IPFromRequest extracts a best-effort client IP. Trusts X-Forwarded-For
// only when the immediate peer is loopback (assume reverse proxy);
// otherwise it falls back to RemoteAddr. A v1 trusted-proxy config will
// replace this stub.
func IPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	// Loopback peer = trust the forwarded header (dev / single-host
	// production behind nginx).
	host := r.RemoteAddr
	if i := lastColon(host); i >= 0 {
		host = host[:i]
	}
	if host == "127.0.0.1" || host == "::1" || host == "[::1]" {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			// Take the leftmost entry: the original client.
			for i := 0; i < len(xf); i++ {
				if xf[i] == ',' {
					return trimSpace(xf[:i])
				}
			}
			return trimSpace(xf)
		}
	}
	return host
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
