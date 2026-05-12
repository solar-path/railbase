package apitoken

// v1.7.3 — unit tests for the pure helpers (no DB). Full-stack
// integration lives in apitoken_e2e_test.go under embed_pg.

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/auth/secret"
)

func TestPrefix_Stable(t *testing.T) {
	// Wire-format prefix is part of the contract — middleware
	// branches on it. If this changes, the middleware test breaks
	// too. Pinning here documents the contract.
	if Prefix != "rbat_" {
		t.Errorf("Prefix = %q, want \"rbat_\"", Prefix)
	}
}

func TestFingerprint_Stable_Across_Calls(t *testing.T) {
	var k secret.Key
	for i := range k {
		k[i] = byte(i)
	}
	a := Fingerprint("rbat_abc", k)
	b := Fingerprint("rbat_abc", k)
	if a != b {
		t.Errorf("fingerprint not deterministic: %q vs %q", a, b)
	}
	if len(a) != 8 {
		t.Errorf("fingerprint length = %d, want 8", len(a))
	}
}

func TestFingerprint_Differs_For_Different_Tokens(t *testing.T) {
	var k secret.Key
	for i := range k {
		k[i] = byte(i)
	}
	a := Fingerprint("rbat_one", k)
	b := Fingerprint("rbat_two", k)
	if a == b {
		t.Errorf("collision on small input space: both produced %q", a)
	}
}

func TestFingerprint_Differs_Per_Key(t *testing.T) {
	// Same token, different key → different fingerprint. Confirms
	// the HMAC is keyed — leaked DB dumps without the master key
	// can't recompute fingerprints.
	var k1, k2 secret.Key
	for i := range k1 {
		k1[i] = 0xAA
		k2[i] = 0xBB
	}
	a := Fingerprint("rbat_same", k1)
	b := Fingerprint("rbat_same", k2)
	if a == b {
		t.Errorf("fingerprint not keyed: %q == %q under different keys", a, b)
	}
}

func TestComputeHash_Same_Token_Same_Hash(t *testing.T) {
	var k secret.Key
	for i := range k {
		k[i] = 7
	}
	h1 := computeHash("rbat_xyz", k)
	h2 := computeHash("rbat_xyz", k)
	if !bytesEqual(h1, h2) {
		t.Errorf("hash not deterministic")
	}
}

func TestComputeHash_Prefix_Matters(t *testing.T) {
	// Mistakenly hashing the un-prefixed inner string would let an
	// attacker who learned the inner randomness compute the stored
	// hash. The full prefixed string goes into the HMAC.
	var k secret.Key
	for i := range k {
		k[i] = 1
	}
	with := computeHash("rbat_abc123", k)
	without := computeHash("abc123", k)
	if bytesEqual(with, without) {
		t.Error("hash treats prefix as significant — failure")
	}
}

func TestComputeHash_Length(t *testing.T) {
	// HMAC-SHA-256 is always 32 bytes. Storage column is BYTEA;
	// length is a sanity check that we didn't accidentally swap
	// algorithms.
	var k secret.Key
	h := computeHash("rbat_anything", k)
	if len(h) != 32 {
		t.Errorf("hash length = %d, want 32", len(h))
	}
}

func TestPrefix_HasPrefix_Matches(t *testing.T) {
	if !strings.HasPrefix("rbat_abc", Prefix) {
		t.Error("HasPrefix on Prefix failed for sanity case")
	}
	if strings.HasPrefix("rbas_abc", Prefix) {
		t.Error("HasPrefix matched a wrong-prefix token")
	}
}

// --- helpers ---

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
