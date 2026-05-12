// Package id is the Railbase identifier primitive.
//
// Format: UUIDv7 (RFC 9562) — Unix-millisecond-prefixed, time-ordered,
// 122 bits of entropy after the timestamp. Decision recorded in
// docs/00-context.md (option A: UUIDv7 везде, не PB-style 15-char base32).
//
// Why UUIDv7:
//   - Time-ordered → btree-friendly inserts (no random-page-write
//     amplification that v4 produces).
//   - Native Postgres `UUID` column type → 16 bytes, indexed.
//   - Wire format is the canonical 36-char hyphenated string; PB JS
//     SDK does not validate id-string shape, so drop-in compat works.
//
// Generation lives in Go (not Postgres `gen_random_uuid()`) because
// we often need the id BEFORE the INSERT — for hooks, for returning
// to the caller without a round-trip, for outbound webhooks signed
// with the id, etc. Schema-level DEFAULT clauses still use
// `gen_random_uuid()` as a safety net for raw SQL inserts; that path
// emits v4. Prefer always passing id.New() from Go.
package id

import (
	"github.com/google/uuid"
)

// New returns a fresh UUIDv7. Panics on RNG failure (the only error
// uuid.NewV7 returns) — Railbase cannot proceed without a usable RNG,
// so failing fast is the right behaviour.
func New() uuid.UUID {
	u, err := uuid.NewV7()
	if err != nil {
		panic("id: UUIDv7 generation failed: " + err.Error())
	}
	return u
}

// NewString returns New() formatted as the canonical 36-char
// hyphenated string. Convenience for places that just need a
// stringly-typed id (URL params, log fields, JSON wire).
func NewString() string {
	return New().String()
}

// Parse converts a string to a UUID. Accepts the canonical 36-char
// hyphenated form. Returns an error for malformed input — never panics.
func Parse(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// MustParse panics on parse failure. Use only with trusted input
// (constants, generated code).
func MustParse(s string) uuid.UUID {
	return uuid.MustParse(s)
}
