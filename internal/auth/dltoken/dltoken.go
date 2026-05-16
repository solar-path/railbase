// Package dltoken issues and validates short-lived, path-scoped
// download tokens. FEEDBACK #35.
//
// Problem: the SPA renders <a href="/api/collections/orders/export.xlsx?token={authToken}"
// download>...</a> so the browser can fetch the export through the
// existing query-param-fallback auth (WithQueryParamFallback("token")).
// The full session token then lands in:
//
//   - browser history (clipboard, screenshot, screen-sharing leaks)
//   - reverse-proxy access logs (nginx logs the full query string)
//   - any URL share (the user emails the link, paste it in chat, ...)
//
// Solution: a stateless one-shot URL. The SPA calls
//
//   POST /api/exports/sign  { "path": "/api/collections/orders/export.xlsx" }
//
// and gets back
//
//   { "download_url": "/api/collections/orders/export.xlsx?dt=<token>",
//     "expires_at":   "2026-05-16T01:14:58Z" }
//
// The token is HMAC-SHA256(secret, path|expiry) + base64. The
// middleware decodes it, checks expiry, recomputes the HMAC, and
// allows the request iff the recomputed digest matches AND the
// request path equals the bound path.
//
// Defaults:
//
//   - TTL: 60 seconds (configurable per-call via Sign(opts.TTL))
//   - Replay: NOT prevented at this layer (would require shared state).
//     The 60 s TTL combined with HTTPS provides the practical security
//     property — an attacker who intercepts the link has at most one
//     minute to use it before it expires.
package dltoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTTL is the default lifetime of a download token. Tunable
// per-call via Sign(opts), but kept short by design.
const DefaultTTL = 60 * time.Second

// MaxTTL is the upper bound enforced on Sign(opts.TTL). 5 minutes is
// plenty for "the user clicks the download link"; longer windows
// undermine the short-lived property.
const MaxTTL = 5 * time.Minute

// ErrExpired is returned when the token's bound expiry has passed.
var ErrExpired = errors.New("dltoken: token expired")

// ErrInvalid is returned when the token is malformed or its HMAC
// doesn't match. Same error for both so a probing attacker can't
// distinguish "wrong path" from "wrong signature".
var ErrInvalid = errors.New("dltoken: token invalid")

// SignOptions configures Sign. Zero value uses the defaults.
type SignOptions struct {
	TTL time.Duration // default DefaultTTL; clamped to MaxTTL
}

// Sign issues a short-lived token bound to `path`. The returned
// string is URL-safe (base64.RawURLEncoding) and includes the expiry
// inline so verification is stateless. `secret` must be the master
// key (auth/secret.Key.HMAC()).
//
// Format on the wire: `<expiry-unix>.<base64-hmac>` — period as
// separator so the token is one token visually but easy to split.
func Sign(secret []byte, path string, opts SignOptions) (string, time.Time, error) {
	if len(secret) == 0 {
		return "", time.Time{}, fmt.Errorf("dltoken: empty secret")
	}
	if path == "" {
		return "", time.Time{}, fmt.Errorf("dltoken: empty path")
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	expiry := time.Now().UTC().Add(ttl)
	digest := computeHMAC(secret, path, expiry.Unix())
	token := strconv.FormatInt(expiry.Unix(), 10) + "." +
		base64.RawURLEncoding.EncodeToString(digest)
	return token, expiry, nil
}

// Verify checks that token is valid right now for `path`. Returns nil
// on success; ErrExpired if past expiry; ErrInvalid for any other
// reason (malformed, wrong signature, path mismatch).
//
// The verification is constant-time on the HMAC compare to avoid
// timing-oracle leakage.
func Verify(secret []byte, path, token string) error {
	if len(secret) == 0 || path == "" || token == "" {
		return ErrInvalid
	}
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot >= len(token)-1 {
		return ErrInvalid
	}
	expUnix, err := strconv.ParseInt(token[:dot], 10, 64)
	if err != nil {
		return ErrInvalid
	}
	gotDigest, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return ErrInvalid
	}
	// HMAC validation BEFORE expiry check: a malformed/forged token
	// shouldn't be allowed to differentiate "wrong sig" from "wrong
	// time" via the response — both must look like ErrInvalid.
	wantDigest := computeHMAC(secret, path, expUnix)
	if !hmac.Equal(gotDigest, wantDigest) {
		return ErrInvalid
	}
	if time.Now().UTC().Unix() > expUnix {
		return ErrExpired
	}
	return nil
}

// computeHMAC is the canonical input format for the HMAC. We
// separate path and expiry with a single newline — any embedded
// newline in path would just produce a different digest, not a
// collision (the receiver's path can't contain newlines anyway:
// chi normalises URL paths).
func computeHMAC(secret []byte, path string, expiry int64) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(path))
	h.Write([]byte{'\n'})
	h.Write([]byte(strconv.FormatInt(expiry, 10)))
	return h.Sum(nil)
}
