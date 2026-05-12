package totp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/auth/password"
)

// RecoveryCodeCount is the number of recovery codes generated per
// enrollment. 8 is the de-facto standard (GitHub, Google, etc. all
// use 8 or 10). They're meant for the "lost my phone" emergency
// path — used and discarded.
const RecoveryCodeCount = 8

// RecoveryCode is the wire-format string handed to the user.
// Shape: "xxxx-xxxx-xxxx" (12 lowercase hex chars in three groups
// separated by hyphens). Hyphens are pure UX; users can type with
// or without them.
type RecoveryCode string

// HashedRecoveryCode is the persisted form: Argon2id hash + an
// optional used_at timestamp. Stored as JSONB in
// `_totp_enrollments.recovery_codes`.
type HashedRecoveryCode struct {
	Hash   string     `json:"hash"`
	UsedAt *time.Time `json:"used_at,omitempty"`
}

// GenerateRecoveryCodes returns RecoveryCodeCount fresh codes (raw,
// to send to the user) and their Argon2id-hashed counterparts (to
// persist).
//
// Operator MUST surface the raw codes exactly ONCE — typically
// in the TOTP enroll-confirm response with a "save these
// somewhere safe" warning. After that, only the hashed form
// remains on the server.
func GenerateRecoveryCodes() ([]RecoveryCode, []HashedRecoveryCode, error) {
	codes := make([]RecoveryCode, RecoveryCodeCount)
	hashed := make([]HashedRecoveryCode, RecoveryCodeCount)
	for i := 0; i < RecoveryCodeCount; i++ {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		codes[i] = c
		// Normalise to no-hyphens before hashing so user input
		// matches regardless of how they type it back.
		hash, err := password.Hash(normaliseRecoveryCode(string(c)))
		if err != nil {
			return nil, nil, err
		}
		hashed[i] = HashedRecoveryCode{Hash: hash}
	}
	return codes, hashed, nil
}

// VerifyRecoveryCode walks the hashed-list and returns the index of
// the first match (already-used codes are skipped). Returns -1 when
// no match. Caller is responsible for stamping `.UsedAt` and
// persisting after a successful check.
func VerifyRecoveryCode(input string, stored []HashedRecoveryCode) int {
	candidate := normaliseRecoveryCode(input)
	if candidate == "" {
		return -1
	}
	for i, h := range stored {
		if h.UsedAt != nil {
			continue
		}
		if err := password.Verify(candidate, h.Hash); err == nil {
			return i
		}
	}
	return -1
}

// newRecoveryCode returns a single code: 6 random bytes → 12 hex
// chars → grouped "xxxx-xxxx-xxxx". 6 bytes = 48 bits of entropy
// per code, ample for "must guess one of 8 within a short window".
func newRecoveryCode() (RecoveryCode, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	hex := hex.EncodeToString(raw[:])
	return RecoveryCode(hex[0:4] + "-" + hex[4:8] + "-" + hex[8:12]), nil
}

// normaliseRecoveryCode strips hyphens and lower-cases. Returns ""
// when the result isn't 12 hex chars (defends against gibberish
// inputs that might otherwise hit the constant-time hash compare).
func normaliseRecoveryCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	if len(s) != 12 {
		return ""
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return s
}

// ErrInvalidRecovery is exported for callers that want to surface
// "wrong code" distinct from "no enrollment".
var ErrInvalidRecovery = errors.New("totp: invalid recovery code")
