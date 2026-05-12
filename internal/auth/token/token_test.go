package token

import (
	"encoding/base64"
	"testing"

	"github.com/railbase/railbase/internal/auth/secret"
)

func TestGenerate_FormatAndUniqueness(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate()
	if a == b {
		t.Errorf("two tokens should differ")
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(a))
	if err != nil {
		t.Errorf("not base64url: %v", err)
	}
	if len(raw) != RawTokenBytes {
		t.Errorf("decoded len = %d, want %d", len(raw), RawTokenBytes)
	}
}

func TestCompute_Deterministic(t *testing.T) {
	var key1, key2 secret.Key
	for i := range key1 {
		key1[i] = byte(i)
	}
	copy(key2[:], key1[:])

	tok := Token("the-token")
	h1 := Compute(tok, key1)
	h2 := Compute(tok, key2)
	if !Equal(h1, h2) {
		t.Errorf("same key, same token → different hashes")
	}
	if len(h1) != HashLen {
		t.Errorf("hash len = %d, want %d", len(h1), HashLen)
	}
}

func TestCompute_DifferentKeys(t *testing.T) {
	var k1, k2 secret.Key
	k1[0] = 1
	k2[0] = 2
	if Equal(Compute("t", k1), Compute("t", k2)) {
		t.Errorf("different keys must produce different hashes")
	}
}

func TestEqual_ConstantTime(t *testing.T) {
	a := Hash([]byte{1, 2, 3, 4})
	b := Hash([]byte{1, 2, 3, 4})
	c := Hash([]byte{1, 2, 3, 5})
	if !Equal(a, b) {
		t.Errorf("equal slices reported unequal")
	}
	if Equal(a, c) {
		t.Errorf("unequal slices reported equal")
	}
}
