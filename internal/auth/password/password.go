// Package password hashes and verifies user passwords.
//
// Algorithm: Argon2id with parameters from docs/04-identity.md:
//
//	memory:      64 MiB (m=64*1024)
//	iterations:  3
//	parallelism: 4
//	salt:        16 random bytes
//	tag:         32 bytes
//
// Encoded form follows the standard PHC string:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<base64(salt)>$<base64(hash)>
//
// This matches what `golang.org/x/crypto/argon2` and most other Argon2
// libraries emit, so a Railbase-hashed password is portable to any
// Argon2id verifier and vice-versa.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters fixed at compile time. Bumping these is a
// compatibility event — old hashes still verify, but new hashes use
// the new parameters; consider rehashing on next successful signin.
const (
	argonMemKiB     uint32 = 64 * 1024 // 64 MiB
	argonIterations uint32 = 3
	argonParallel   uint8  = 4
	argonTagLen     uint32 = 32
	argonSaltLen           = 16
)

// ErrMismatch indicates a verification call where the password did
// not match the stored hash. Callers typically return a generic
// "invalid credentials" so timing/error-shape can't distinguish
// "wrong password" from "user doesn't exist".
var ErrMismatch = errors.New("password: mismatch")

// Hash returns the encoded PHC string for plaintext. Generates a
// fresh random salt; never call with a caller-supplied salt.
func Hash(plaintext string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: rand: %w", err)
	}
	tag := argon2.IDKey([]byte(plaintext), salt, argonIterations, argonMemKiB, argonParallel, argonTagLen)

	encSalt := base64.RawStdEncoding.EncodeToString(salt)
	encTag := base64.RawStdEncoding.EncodeToString(tag)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemKiB, argonIterations, argonParallel,
		encSalt, encTag), nil
}

// Verify returns nil iff plaintext re-hashes to the same tag as
// encoded. Returns ErrMismatch on a mismatch, a wrapped error on
// malformed input.
//
// Constant-time comparison via subtle.ConstantTimeCompare guards
// against timing attacks on the tag comparison itself.
func Verify(plaintext, encoded string) error {
	params, salt, tag, err := parsePHC(encoded)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(plaintext), salt, params.iter, params.mem, params.par, uint32(len(tag)))
	if subtle.ConstantTimeCompare(got, tag) != 1 {
		return ErrMismatch
	}
	return nil
}

// parsePHC decodes a $argon2id$ string into its components. Returns
// an error for any algorithm other than argon2id and any malformed
// segments.
type phcParams struct {
	mem  uint32
	iter uint32
	par  uint8
}

func parsePHC(s string) (phcParams, []byte, []byte, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 {
		return phcParams{}, nil, nil, fmt.Errorf("password: malformed PHC: want 6 parts, got %d", len(parts))
	}
	if parts[1] != "argon2id" {
		return phcParams{}, nil, nil, fmt.Errorf("password: unsupported algorithm %q", parts[1])
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return phcParams{}, nil, nil, fmt.Errorf("password: unsupported argon2 version %q", parts[2])
	}
	var p phcParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.mem, &p.iter, &p.par); err != nil {
		return phcParams{}, nil, nil, fmt.Errorf("password: bad params %q: %w", parts[3], err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return phcParams{}, nil, nil, fmt.Errorf("password: bad salt: %w", err)
	}
	tag, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return phcParams{}, nil, nil, fmt.Errorf("password: bad tag: %w", err)
	}
	return p, salt, tag, nil
}
