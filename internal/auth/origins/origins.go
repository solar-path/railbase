// Package origins persists the per-user device/location fingerprints
// that drive "new-device signin" notifications (v1.7.36 §3.2.10).
//
// One row per (user_id, collection, ip_class, ua_hash) tuple in
// `_auth_origins`. The Touch method is the only write path the auth
// signin handler needs — it UPSERTs the tuple and reports `isNew=true`
// the FIRST time a particular tuple is seen, which is the cue the
// caller uses to enqueue a `send_email_async` job with the
// `new_device_signin` template.
//
// Granularity decision (see also: migration 0025_auth_origins.up.sql):
//
//   - `ip_class` collapses to the /24 (IPv4) or /48 (IPv6) prefix so
//     mobile/NAT clients don't trigger a fresh notification every
//     time DHCP hands out a new lease.
//   - `ua_hash` is sha256 over a version-stripped User-Agent so silent
//     Chrome auto-updates don't trigger fresh notifications either.
//
// Both helpers (IPClass, UAHash) are exported so callers / tests can
// reach them without going through the DB.
package origins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultRemembered is the lifetime baked into a freshly recorded
// origin's `remembered_until`. After this elapses an operator-side
// sweep (deferred) would prune the row so a re-signin from the same
// origin re-triggers the new-device notification. The current Touch
// path UPSERTs forever; the column is informational until the sweep
// ships.
const DefaultRemembered = 30 * 24 * time.Hour

// Origin is the materialised view of a `_auth_origins` row. Returned
// by Touch + ListForUser. `RememberedUntil` is a *time.Time because
// the column is nullable — `nil` means "no expiry stamped".
type Origin struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Collection      string
	IPClass         string
	UAHash          string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	RememberedUntil *time.Time
}

// ErrNotFound is returned by Delete when the target id does not match
// any row. Other errors propagate the pgx error directly.
var ErrNotFound = errors.New("origins: not found")

// Store is the persistence handle. Goroutine-safe; share for the
// lifetime of the process. The pool is the only dependency — no
// secret material is involved because the ua_hash is intentionally
// NOT keyed (the contents are non-sensitive and the table is queried
// by indexed UPSERT, not by attacker-controlled inputs).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store. Pass the same pool the rest of the auth
// surface uses.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Touch normalises (ip, ua) and UPSERTs the corresponding row. On the
// first call for a given tuple it INSERTs (isNew=true); on every
// subsequent call it UPDATEs `last_seen_at = now()` (isNew=false).
//
// Callers should check the returned `isNew` to decide whether to fire
// a "new-device signin" notification. Errors propagate; an empty
// (ip, ua) pair is accepted (some test environments don't populate
// the header) — the normalised values just collapse to a known
// constant so the row count stays bounded.
func (s *Store) Touch(ctx context.Context, userID uuid.UUID, collection, ip, ua string) (isNew bool, origin Origin, err error) {
	if userID == uuid.Nil {
		return false, Origin{}, fmt.Errorf("origins: user_id required")
	}
	if collection == "" {
		return false, Origin{}, fmt.Errorf("origins: collection required")
	}
	class := IPClass(ip)
	uaH := UAHash(ua)
	remembered := time.Now().UTC().Add(DefaultRemembered)

	// xmax = 0 in the RETURNING expression iff the row was freshly
	// inserted. Postgres-flavoured trick that lets a single round-trip
	// UPSERT distinguish "new row" from "existing row" without a
	// separate SELECT — see https://stackoverflow.com/q/34708509.
	const q = `
        INSERT INTO _auth_origins
            (user_id, collection, ip_class, ua_hash, remembered_until)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (user_id, collection, ip_class, ua_hash) DO UPDATE
            SET last_seen_at = now()
        RETURNING id, first_seen_at, last_seen_at, remembered_until,
                  (xmax = 0) AS inserted
    `
	var o Origin
	var inserted bool
	err = s.pool.QueryRow(ctx, q, userID, collection, class, uaH, remembered).Scan(
		&o.ID, &o.FirstSeenAt, &o.LastSeenAt, &o.RememberedUntil, &inserted,
	)
	if err != nil {
		return false, Origin{}, fmt.Errorf("origins: upsert: %w", err)
	}
	o.UserID = userID
	o.Collection = collection
	o.IPClass = class
	o.UAHash = uaH
	return inserted, o, nil
}

// ListForUser returns every recorded origin for one user, most-
// recently-seen first. `collection` may be empty to list across all
// collections (useful for admin tooling that doesn't track which
// auth-collection the user belongs to).
func (s *Store) ListForUser(ctx context.Context, userID uuid.UUID, collection string) ([]Origin, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("origins: user_id required")
	}
	var rows pgx.Rows
	var err error
	if collection == "" {
		rows, err = s.pool.Query(ctx, `
            SELECT id, user_id, collection, ip_class, ua_hash,
                   first_seen_at, last_seen_at, remembered_until
              FROM _auth_origins
             WHERE user_id = $1
             ORDER BY last_seen_at DESC
        `, userID)
	} else {
		rows, err = s.pool.Query(ctx, `
            SELECT id, user_id, collection, ip_class, ua_hash,
                   first_seen_at, last_seen_at, remembered_until
              FROM _auth_origins
             WHERE user_id = $1 AND collection = $2
             ORDER BY last_seen_at DESC
        `, userID, collection)
	}
	if err != nil {
		return nil, fmt.Errorf("origins: query: %w", err)
	}
	defer rows.Close()

	var out []Origin
	for rows.Next() {
		var o Origin
		if err := rows.Scan(&o.ID, &o.UserID, &o.Collection, &o.IPClass, &o.UAHash,
			&o.FirstSeenAt, &o.LastSeenAt, &o.RememberedUntil); err != nil {
			return nil, fmt.Errorf("origins: scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Delete removes one origin row by id. Returns ErrNotFound if no row
// matches. Use case: operator revokes a recognised origin so the next
// signin from that (ip_class, ua_hash) re-triggers the notification.
func (s *Store) Delete(ctx context.Context, originID uuid.UUID) error {
	if originID == uuid.Nil {
		return fmt.Errorf("origins: origin_id required")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM _auth_origins WHERE id = $1`, originID)
	if err != nil {
		return fmt.Errorf("origins: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- helpers (exported for the wiring layer + tests) ---

// IPClass normalises an address string to its /24 (IPv4) or /48
// (IPv6) prefix string. Empty or unparseable inputs collapse to the
// constant "unknown" so the UPSERT still has a deterministic key —
// repeated test requests without a RemoteAddr won't fan out into a
// row per call.
//
// IPv4: 198.51.100.42  →  198.51.100.0/24
// IPv6: 2001:db8::1234 →  2001:db8::/48
func IPClass(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "unknown"
	}
	// Strip brackets / port if a RemoteAddr-style value slipped in.
	if strings.HasPrefix(ip, "[") {
		if end := strings.Index(ip, "]"); end >= 0 {
			ip = ip[1:end]
		}
	} else if strings.Count(ip, ":") == 1 {
		// IPv4:port (a colon-less IPv6 has zero or 2+ colons).
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	if v4 := parsed.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	// IPv6 — mask to /48.
	mask := net.CIDRMask(48, 128)
	masked := parsed.Mask(mask)
	return masked.String() + "/48"
}

// uaVersionRE matches a `Token/1.2.3` style version suffix. Used by
// normaliseUA to drop version numbers so Chrome 120 and Chrome 121
// hash to the same value.
var uaVersionRE = regexp.MustCompile(`/[0-9][0-9A-Za-z.+-]*`)

// uaSpaceRE collapses multi-space gaps left behind by version stripping.
var uaSpaceRE = regexp.MustCompile(`\s+`)

// normaliseUA strips version tokens from a User-Agent string so that
// trivial browser auto-updates don't change the hash. Implementation
// detail of UAHash; exported case is the hash itself.
func normaliseUA(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "unknown"
	}
	ua = uaVersionRE.ReplaceAllString(ua, "")
	ua = uaSpaceRE.ReplaceAllString(ua, " ")
	return strings.TrimSpace(ua)
}

// UAHash returns sha256(normaliseUA(ua)) as a 64-char hex string.
// Empty / whitespace-only inputs collapse to a stable "unknown" hash.
func UAHash(ua string) string {
	norm := normaliseUA(ua)
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}
