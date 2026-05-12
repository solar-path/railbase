package mfa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/secret"
	authtoken "github.com/railbase/railbase/internal/auth/token"
)

// Factor names. Stable wire values — renaming breaks in-flight
// challenges across deploys.
type Factor string

const (
	FactorPassword Factor = "password"
	FactorTOTP     Factor = "totp"
	FactorEmailOTP Factor = "email_otp"
	// FactorRecovery is conceptually "TOTP via recovery code" — it
	// solves the same slot as FactorTOTP in factors_required, but
	// the audit log distinguishes them so admins can see which path
	// the user took.
	FactorRecovery Factor = "recovery"
)

// DefaultChallengeTTL bounds how long the user has to complete all
// factors. Five minutes is generous for "open the authenticator app
// and type the 6 digits"; longer would let a stolen password+
// challenge-token pair hang around too long.
const DefaultChallengeTTL = 5 * time.Minute

// Challenge is the materialised row. RawToken is set only on
// Create — never read back from disk.
type Challenge struct {
	ID              uuid.UUID
	CollectionName  string
	RecordID        uuid.UUID
	FactorsRequired []Factor
	FactorsSolved   []Factor
	CreatedAt       time.Time
	ExpiresAt       time.Time
	CompletedAt     *time.Time
	IP              string
	UserAgent       string
}

// Complete reports whether every required factor has been solved.
// Sorted-set comparison — duplicates and order don't matter.
func (c *Challenge) Complete() bool {
	if c == nil {
		return false
	}
	have := map[Factor]bool{}
	for _, f := range c.FactorsSolved {
		have[f] = true
	}
	for _, f := range c.FactorsRequired {
		// FactorTOTP and FactorRecovery satisfy the same slot —
		// recovery is "TOTP via emergency code".
		if f == FactorTOTP && (have[FactorTOTP] || have[FactorRecovery]) {
			continue
		}
		if !have[f] {
			return false
		}
	}
	return true
}

// ChallengeStore persists `_mfa_challenges`. Goroutine-safe.
type ChallengeStore struct {
	pool   *pgxpool.Pool
	secret secret.Key
}

// NewChallengeStore returns a store. Share the master key with
// sessions / record_tokens.
func NewChallengeStore(pool *pgxpool.Pool, key secret.Key) *ChallengeStore {
	return &ChallengeStore{pool: pool, secret: key}
}

// CreateInput bundles what Create needs.
type CreateInput struct {
	CollectionName  string
	RecordID        uuid.UUID
	FactorsRequired []Factor
	FactorsSolved   []Factor // typically [FactorPassword] when auth-with-password seeded the challenge
	TTL             time.Duration
	IP              string
	UserAgent       string
}

// Create persists a new challenge and returns (raw token, challenge).
// The raw token leaves the server exactly once (auth-with-password
// response); subsequent factor-solve calls re-derive the hash and
// look up by token_hash.
func (s *ChallengeStore) Create(ctx context.Context, in CreateInput) (authtoken.Token, *Challenge, error) {
	tok, err := authtoken.Generate()
	if err != nil {
		return "", nil, fmt.Errorf("mfa: gen token: %w", err)
	}
	hash := authtoken.Compute(tok, s.secret)

	ttl := in.TTL
	if ttl <= 0 {
		ttl = DefaultChallengeTTL
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	id := uuid.Must(uuid.NewV7())

	reqBytes, err := json.Marshal(factorsSorted(in.FactorsRequired))
	if err != nil {
		return "", nil, err
	}
	solvedBytes, err := json.Marshal(factorsSorted(in.FactorsSolved))
	if err != nil {
		return "", nil, err
	}

	const q = `
        INSERT INTO _mfa_challenges
            (id, token_hash, collection_name, record_id,
             factors_required, factors_solved, created_at, expires_at,
             ip, user_agent)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, hash, in.CollectionName, in.RecordID,
		reqBytes, solvedBytes, now, expires,
		nullableINET(in.IP), nullableText(in.UserAgent)); err != nil {
		return "", nil, fmt.Errorf("mfa: insert challenge: %w", err)
	}
	return tok, &Challenge{
		ID:              id,
		CollectionName:  in.CollectionName,
		RecordID:        in.RecordID,
		FactorsRequired: in.FactorsRequired,
		FactorsSolved:   in.FactorsSolved,
		CreatedAt:       now,
		ExpiresAt:       expires,
		IP:              in.IP,
		UserAgent:       in.UserAgent,
	}, nil
}

// Lookup loads a non-expired, not-yet-completed challenge by raw
// token. Returns ErrNotFound on miss / expiry / completion — all
// three collapse so the surface can't be probed.
func (s *ChallengeStore) Lookup(ctx context.Context, tok authtoken.Token) (*Challenge, error) {
	hash := authtoken.Compute(tok, s.secret)
	const q = `
        SELECT id, collection_name, record_id, factors_required,
               factors_solved, created_at, expires_at, completed_at,
               COALESCE(host(ip), ''), COALESCE(user_agent, '')
          FROM _mfa_challenges
         WHERE token_hash = $1
           AND completed_at IS NULL
           AND expires_at > now()
         LIMIT 1
    `
	row := s.pool.QueryRow(ctx, q, hash)
	c, err := scanChallenge(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// Solve records a factor as solved. Atomic — uses row-lock so two
// parallel solves can't both transition the same challenge from
// incomplete to complete. Returns the updated challenge; caller
// checks .Complete() to decide whether to issue a session.
func (s *ChallengeStore) Solve(ctx context.Context, tok authtoken.Token, factor Factor) (*Challenge, error) {
	hash := authtoken.Compute(tok, s.secret)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const sel = `
        SELECT id, collection_name, record_id, factors_required,
               factors_solved, created_at, expires_at, completed_at,
               COALESCE(host(ip), ''), COALESCE(user_agent, '')
          FROM _mfa_challenges
         WHERE token_hash = $1
           AND completed_at IS NULL
           AND expires_at > now()
         FOR UPDATE
    `
	row := tx.QueryRow(ctx, sel, hash)
	c, err := scanChallenge(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Add factor (dedupe).
	have := map[Factor]bool{}
	for _, f := range c.FactorsSolved {
		have[f] = true
	}
	if !have[factor] {
		c.FactorsSolved = append(c.FactorsSolved, factor)
	}
	out, err := json.Marshal(factorsSorted(c.FactorsSolved))
	if err != nil {
		return nil, err
	}

	const upd = `UPDATE _mfa_challenges SET factors_solved = $2 WHERE id = $1`
	if _, err := tx.Exec(ctx, upd, c.ID, out); err != nil {
		return nil, err
	}
	if c.Complete() {
		const done = `UPDATE _mfa_challenges SET completed_at = now() WHERE id = $1`
		if _, err := tx.Exec(ctx, done, c.ID); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		c.CompletedAt = &now
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// scanChallenge handles the JSONB decode for both factors arrays.
func scanChallenge(row pgx.Row) (*Challenge, error) {
	var c Challenge
	var reqBytes, solvedBytes []byte
	if err := row.Scan(&c.ID, &c.CollectionName, &c.RecordID,
		&reqBytes, &solvedBytes, &c.CreatedAt, &c.ExpiresAt,
		&c.CompletedAt, &c.IP, &c.UserAgent); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(reqBytes, &c.FactorsRequired); err != nil {
		return nil, fmt.Errorf("mfa: unmarshal req: %w", err)
	}
	if err := json.Unmarshal(solvedBytes, &c.FactorsSolved); err != nil {
		return nil, fmt.Errorf("mfa: unmarshal solved: %w", err)
	}
	return &c, nil
}

func factorsSorted(fs []Factor) []Factor {
	out := make([]Factor, len(fs))
	copy(out, fs)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func nullableINET(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
