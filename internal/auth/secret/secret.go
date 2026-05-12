// Package secret loads the Railbase master secret key.
//
// The 32-byte secret seeds:
//   - HMAC-SHA-256 hashing of session tokens before storage
//   - Cookie signing
//   - Audit log hash-chain seal (v1.1)
//   - Field-level encryption KEK (v1.1)
//
// Source: `pb_data/.secret` (created by `railbase init`). The file contains
// the 64-char hex-encoded form of 32 random bytes — same shape that
// `internal/scaffold` writes during scaffolding.
//
// Why a file on disk, not config: rotating the secret means re-encrypting
// every dependent value. Until v1.1 ships KMS integration we want the
// secret tied to the data directory, not to env vars where it can leak.
package secret

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyLen is the size of the master secret in raw bytes. 32 bytes →
// 256-bit HMAC key, well above SHA-256 block size minimum.
const KeyLen = 32

// Key is the loaded master secret. Treat as opaque; never log.
type Key [KeyLen]byte

// HMAC returns a 32-byte slice view backed by k. Convenience for code
// that needs []byte for hmac.New.
func (k Key) HMAC() []byte { return k[:] }

// LoadFromDataDir reads `<dataDir>/.secret`. The file content must be
// 64 hex characters (32 bytes); anything else is a configuration error.
//
// Returns an explicit error when the file is missing — callers MUST
// not silently generate a new secret here. The scaffold creates the
// file at `railbase init` time; if it's missing the user is running
// against a pristine data directory and should re-init or copy the
// secret from a backup.
func LoadFromDataDir(dataDir string) (Key, error) {
	path := filepath.Join(dataDir, ".secret")
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Key{}, fmt.Errorf("secret: %s missing — run `railbase init` or restore from backup", path)
	}
	if err != nil {
		return Key{}, fmt.Errorf("secret: read %s: %w", path, err)
	}
	return parseHex(body)
}

// LoadOrCreate reads `<dataDir>/.secret`; if absent AND allowCreate is true,
// generates a fresh 32-byte random key, writes it atomically (0600), and
// returns it. The second return value reports whether a new secret was
// created — callers can log it so operators notice.
//
// Use `allowCreate=false` in production: missing secret there is a fatal
// configuration error, not a green-field bootstrap. Use `allowCreate=true`
// for zero-config dev mode (`./railbase serve` with no env).
func LoadOrCreate(dataDir string, allowCreate bool) (Key, bool, error) {
	path := filepath.Join(dataDir, ".secret")
	body, err := os.ReadFile(path)
	if err == nil {
		k, err := parseHex(body)
		return k, false, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Key{}, false, fmt.Errorf("secret: read %s: %w", path, err)
	}
	if !allowCreate {
		return Key{}, false, fmt.Errorf("secret: %s missing — run `railbase init` or restore from backup", path)
	}
	// Generate + write atomically.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return Key{}, false, fmt.Errorf("secret: mkdir %s: %w", dataDir, err)
	}
	var raw [KeyLen]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Key{}, false, fmt.Errorf("secret: rand: %w", err)
	}
	hexed := make([]byte, 2*KeyLen+1)
	hex.Encode(hexed, raw[:])
	hexed[2*KeyLen] = '\n'
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, hexed, 0o600); err != nil {
		return Key{}, false, fmt.Errorf("secret: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Key{}, false, fmt.Errorf("secret: rename %s: %w", path, err)
	}
	return Key(raw), true, nil
}

func parseHex(body []byte) (Key, error) {
	// Tolerate trailing whitespace from text editors.
	for len(body) > 0 && (body[len(body)-1] == '\n' || body[len(body)-1] == '\r' || body[len(body)-1] == ' ' || body[len(body)-1] == '\t') {
		body = body[:len(body)-1]
	}
	if len(body) != 2*KeyLen {
		return Key{}, fmt.Errorf("secret: expected %d hex chars, got %d", 2*KeyLen, len(body))
	}
	var k Key
	if _, err := hex.Decode(k[:], body); err != nil {
		return Key{}, fmt.Errorf("secret: invalid hex: %w", err)
	}
	return k, nil
}
