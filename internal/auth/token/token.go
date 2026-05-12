// Package token generates and hashes session tokens.
//
// Per docs/04-identity.md:
//
//	"Opaque tokens (32-byte URL-safe, base64url-encoded), stored hashed
//	 (SHA-256 + HMAC из _railbase_meta.secret_key)"
//
// Wire form is a 43-character base64url string (32 bytes encoded). The
// raw token is given to the client exactly once (in the auth response);
// the database only stores HMAC-SHA-256(token, secret_key) so a database
// dump alone can't be used to forge sessions.
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"

	"github.com/railbase/railbase/internal/auth/secret"
)

// RawTokenBytes is the random-bytes length of a token before encoding.
const RawTokenBytes = 32

// HashLen is the byte length of the HMAC-SHA-256 output we store.
const HashLen = sha256.Size // 32

// Token is the wire-format opaque session string. Treat as
// secret-equivalent: never log, never include in URLs.
type Token string

// Hash is the HMAC-SHA-256 digest stored in `_sessions.token_hash`.
// Always exactly HashLen bytes.
type Hash []byte

// Generate produces a fresh random Token using crypto/rand.
func Generate() (Token, error) {
	buf := make([]byte, RawTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("token: rand: %w", err)
	}
	return Token(base64.RawURLEncoding.EncodeToString(buf)), nil
}

// Compute returns the HMAC-SHA-256 hash of t under key. The same
// (token, key) pair always produces the same hash, so we can use it
// as the lookup key in the sessions table without reversibility.
func Compute(t Token, key secret.Key) Hash {
	mac := hmac.New(sha256.New, key.HMAC())
	mac.Write([]byte(t))
	return mac.Sum(nil)
}

// Equal returns true iff a and b are identical, in constant time.
// Hash comparisons happen on the auth hot path; using bytes.Equal
// would leak length-prefix timing info for malformed inputs.
func Equal(a, b Hash) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
