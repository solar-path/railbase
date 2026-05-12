// Package files is the v1.3.1 inline-attachment subsystem.
//
// One row in `_files` per uploaded blob — metadata (filename, mime,
// size, sha256, storage_key) carried alongside the user's record
// column (which only stores the storage_key string).
//
// Architecture:
//
//	HTTP multipart POST → handler streams to Driver.Put + SHA-256 →
//	  Store.Insert _files row → updates user-record column (single-file
//	  field = TEXT, multi-file = JSONB array) → record marshalling
//	  emits {key, name, size, mime, url} where url is HMAC-signed.
//
// Storage drivers (Driver interface): FSDriver in v1.3.1, S3 + GCS
// land as plugin / v1.3.x drop-in. Driver is content-addressed:
// FSDriver writes to `<root>/<sha256[0:2]>/<sha256>/<filename>`. Same
// content uploaded twice produces the same storage_key — dedup is
// trivial when the upload handler probes by sha256 first (v1.3.2).
//
// Signed URLs:
//
//	GET /api/files/{collection}/{record_id}/{field}/{filename}
//	    ?token=<HMAC>&expires=<unix>
//
// HMAC keyed on the master secret (`pb_data/.secret`). 5-min default
// TTL for inline access; longer windows opt-in via SignURL ttl arg.
//
// Out of scope v1.3.1:
//   - Thumbnails (image variants) — v1.3.2
//   - Documents entity (logical document w/ versions, polymorphic owner)
//   - Per-tenant quotas — v1.3.2
//   - Orphan reaper job — v1.4 jobs queue
//   - Virus scanning webhook — out-of-tree
package files

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Store.Get / Driver.Open when no blob
// exists at the requested key. Handlers translate to 404.
var ErrNotFound = errors.New("files: not found")

// ErrInvalidSignature is returned when a signed-URL token doesn't
// match the computed HMAC, or the URL has expired. Handlers translate
// to 403.
var ErrInvalidSignature = errors.New("files: invalid signature")

// File is the metadata row persisted in `_files`. Mirrors the table
// columns 1:1 — handlers / record marshalling rely on these names.
type File struct {
	ID         uuid.UUID
	Collection string
	RecordID   uuid.UUID
	Field      string
	OwnerUser  *uuid.UUID
	TenantID   *uuid.UUID
	Filename   string
	MIME       string
	Size       int64
	SHA256     []byte
	StorageKey string
	CreatedAt  time.Time
}

// Driver is the storage backend interface. v1.3.1 ships FSDriver;
// S3 / GCS / Azure are future drop-ins.
//
// All methods take a context so callers can cancel slow uploads.
// Drivers should honour ctx.Done().
type Driver interface {
	// Put streams `body` into the backing store under `key`. Returns
	// the number of bytes written. Implementations MUST NOT trust the
	// caller-supplied size — they verify by counting bytes written.
	Put(ctx context.Context, key string, body io.Reader) (int64, error)

	// Open returns a ReadSeekCloser for the blob at `key`. http.ServeContent
	// needs Seek for Range requests (video / large image streaming).
	// Caller closes the returned reader.
	Open(ctx context.Context, key string) (ReadSeekCloser, error)

	// Delete removes the blob at `key`. Idempotent — deleting a
	// missing key returns nil, not ErrNotFound (orphan-clean code
	// shouldn't have to special-case races).
	Delete(ctx context.Context, key string) error
}

// ReadSeekCloser is the read side of `os.File`. Drivers return one
// to support http.ServeContent's Range handling.
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// StorageKey computes the content-addressed path for a blob.
// Layout: `<sha256[0:2]>/<full sha256>/<sanitised filename>`. Storing
// the filename alongside the hash keeps Content-Disposition rendering
// trivial for download handlers.
//
// The 2-char fan-out folder caps directory size on FS drivers: with
// 256 prefixes, even at 1M files you hit ~4k entries per dir, which
// every modern filesystem handles comfortably.
func StorageKey(shaHex, filename string) string {
	if len(shaHex) < 64 {
		// Defensive: never produce a path with a truncated digest.
		return ""
	}
	return shaHex[:2] + "/" + shaHex + "/" + filename
}

// SHA256Hex hex-encodes the digest. Wrapper around encoding/hex so
// callers don't have to import it just for this one conversion.
func SHA256Hex(digest []byte) string { return hex.EncodeToString(digest) }

// SignURL produces a query-string-ready (`token`, `expires`) pair for
// the file URL. The HMAC is over `<collection>|<record_id>|<field>|<filename>|<expires>`
// — binding the token to the full path prevents an operator who
// guesses a similar URL from substituting a different file.
//
// `ttl` is the absolute lifetime from now. Typical: 5 min for embed
// in record JSON, longer (1h) for download buttons.
func SignURL(key []byte, collection, recordID, field, filename string, ttl time.Duration) (token, expires string) {
	exp := time.Now().Add(ttl).Unix()
	expires = strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s|%s|%s|%s|%s", collection, recordID, field, filename, expires)
	token = hex.EncodeToString(mac.Sum(nil))
	return token, expires
}

// VerifySignature confirms the token+expires were minted by SignURL
// with `key`. Constant-time compare to avoid leaking the HMAC.
//
// Returns ErrInvalidSignature for either tamper or expiry — handlers
// should NOT distinguish (don't tell attacker which check failed).
func VerifySignature(key []byte, collection, recordID, field, filename, token, expires string) error {
	exp, err := strconv.ParseInt(expires, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	if time.Now().Unix() > exp {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s|%s|%s|%s|%s", collection, recordID, field, filename, expires)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(token)) {
		return ErrInvalidSignature
	}
	return nil
}

// SanitiseFilename strips path separators + control bytes from a
// browser-supplied filename. Storage drivers receive a key that is
// safe to interpolate into a filesystem path without traversal.
//
// Behaviour: keep [a-zA-Z0-9._-] verbatim; replace everything else
// with `_`. Multiple underscores collapse. Leading dots stripped to
// prevent dotfiles + ".." traversal. Empty result → "file".
func SanitiseFilename(in string) string {
	if in == "" {
		return "file"
	}
	out := make([]byte, 0, len(in))
	lastUnderscore := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_'
		if !ok {
			if !lastUnderscore {
				out = append(out, '_')
				lastUnderscore = true
			}
			continue
		}
		out = append(out, c)
		lastUnderscore = false
	}
	for len(out) > 0 && out[0] == '.' {
		out = out[1:]
	}
	if len(out) == 0 {
		return "file"
	}
	return string(out)
}
