// Tests for the short-lived download-token package. FEEDBACK #35.
package dltoken

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestSignVerify_RoundTrip(t *testing.T) {
	path := "/api/collections/orders/export.xlsx"
	token, exp, err := Sign(testSecret, path, SignOptions{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.Contains(token, ".") {
		t.Errorf("token must contain `.` separator: %q", token)
	}
	if exp.Before(time.Now()) {
		t.Errorf("expiry %v is in the past", exp)
	}
	if err := Verify(testSecret, path, token); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestSign_RespectsCustomTTL(t *testing.T) {
	_, exp, err := Sign(testSecret, "/x", SignOptions{TTL: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	delta := time.Until(exp)
	if delta < 25*time.Second || delta > 35*time.Second {
		t.Errorf("expected ~30s TTL, got delta=%v", delta)
	}
}

func TestSign_ClampsHugeTTL(t *testing.T) {
	_, exp, err := Sign(testSecret, "/x", SignOptions{TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Must be clamped to MaxTTL (5 minutes).
	if time.Until(exp) > MaxTTL+5*time.Second {
		t.Errorf("TTL not clamped to MaxTTL=%v: actual delta=%v", MaxTTL, time.Until(exp))
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	// Sign with a tiny TTL, wait past it.
	token, _, err := Sign(testSecret, "/x", SignOptions{TTL: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// Sleep past the second boundary — Unix() resolves to seconds, so
	// a sub-second sleep won't tick the clock for Verify.
	time.Sleep(1100 * time.Millisecond)
	if err := Verify(testSecret, "/x", token); !errors.Is(err, ErrExpired) {
		t.Errorf("expected ErrExpired, got %v", err)
	}
}

func TestVerify_RejectsWrongPath(t *testing.T) {
	token, _, err := Sign(testSecret, "/api/x/export.xlsx", SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Verifier asks for a different path — must reject as Invalid
	// (NOT Expired; the token is still time-valid).
	if err := Verify(testSecret, "/api/y/export.xlsx", token); !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for path mismatch, got %v", err)
	}
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	token, _, err := Sign(testSecret, "/x", SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	other := []byte("DIFFERENT-32-BYTES-FOR-HMACSECRET")
	if err := Verify(other, "/x", token); !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for wrong secret, got %v", err)
	}
}

func TestVerify_RejectsTampered(t *testing.T) {
	token, _, err := Sign(testSecret, "/x", SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Flip a character in the MIDDLE of the digest. The last base64
	// char encodes only trailing-low-bits and flipping it might decode
	// to the same byte sequence — middle position is unambiguous.
	dot := strings.IndexByte(token, '.')
	if dot < 0 || len(token)-dot < 10 {
		t.Fatalf("unexpected token shape: %q", token)
	}
	idx := dot + 5 // 5 chars into the digest portion
	r := token[idx]
	flip := byte('A')
	if r == 'A' {
		flip = 'B'
	}
	tampered := token[:idx] + string(flip) + token[idx+1:]
	if err := Verify(testSecret, "/x", tampered); !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for tampered sig, got %v", err)
	}
}

func TestVerify_RejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		".",
		"foo",
		"no-dot",
		"123.notbase64!!",
	} {
		if err := Verify(testSecret, "/x", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("malformed %q: expected ErrInvalid, got %v", bad, err)
		}
	}
}

func TestSign_RejectsEmptyInputs(t *testing.T) {
	if _, _, err := Sign(nil, "/x", SignOptions{}); err == nil {
		t.Error("expected error for empty secret")
	}
	if _, _, err := Sign(testSecret, "", SignOptions{}); err == nil {
		t.Error("expected error for empty path")
	}
}

// TestVerify_StableAcrossSign — repeated Sign calls produce the same
// digest for the same (path, expiry, secret). This is a sanity check
// on the canonical-format helper, not a security claim.
func TestVerify_StableHMACForFixedExpiry(t *testing.T) {
	// Compute HMAC twice for the same inputs; they must be byte-equal.
	d1 := computeHMAC(testSecret, "/api/x", 1715000000)
	d2 := computeHMAC(testSecret, "/api/x", 1715000000)
	if string(d1) != string(d2) {
		t.Errorf("computeHMAC not stable for fixed inputs")
	}
	// Different path → different digest.
	d3 := computeHMAC(testSecret, "/api/y", 1715000000)
	if string(d1) == string(d3) {
		t.Errorf("computeHMAC collided across different paths")
	}
}
