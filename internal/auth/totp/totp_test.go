package totp

import (
	"strings"
	"testing"
)

func TestGenerateSecretLength(t *testing.T) {
	s, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	// 20 raw bytes → base32 no-pad → 32 chars.
	if len(s) != 32 {
		t.Errorf("expected 32-char base32 secret, got %d (%q)", len(s), s)
	}
}

func TestGenerateSecretRandom(t *testing.T) {
	a, _ := GenerateSecret()
	b, _ := GenerateSecret()
	if a == b {
		t.Error("two GenerateSecret calls should not collide")
	}
}

func TestCodeKnownVector(t *testing.T) {
	// RFC 6238 test vector for SHA-1 (mode SHA-1 truncated to 6 digits):
	// secret = "12345678901234567890" (ASCII).
	// At t=59 (counter=1), expected = 287082.
	// We feed the ASCII secret as base32-encoded form.
	asciiSecret := []byte("12345678901234567890")
	// Build base32 of asciiSecret using the same encoder.
	b32Secret := b32.EncodeToString(asciiSecret)
	got := Code(b32Secret, 59)
	if got != "287082" {
		t.Errorf("RFC 6238 vector failed: got %q, want 287082", got)
	}
}

func TestCodeShape(t *testing.T) {
	s, _ := GenerateSecret()
	c := Code(s, 1700000000)
	if len(c) != 6 {
		t.Errorf("code should be 6 digits, got %q", c)
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			t.Errorf("code has non-digit: %q", c)
		}
	}
}

func TestVerifyAcceptsCurrent(t *testing.T) {
	s, _ := GenerateSecret()
	now := int64(1700000000)
	c := Code(s, now)
	if !Verify(s, c, now, 1) {
		t.Errorf("current code should verify")
	}
}

func TestVerifyAcceptsPreviousStep(t *testing.T) {
	s, _ := GenerateSecret()
	now := int64(1700000000)
	prev := Code(s, now-30)
	if !Verify(s, prev, now, 1) {
		t.Errorf("previous-step code should verify within ±1 window")
	}
}

func TestVerifyRejectsOutsideWindow(t *testing.T) {
	s, _ := GenerateSecret()
	now := int64(1700000000)
	stale := Code(s, now-90)
	if Verify(s, stale, now, 1) {
		t.Errorf("stale code 90s in the past should NOT verify with window=1")
	}
}

func TestVerifyRejectsWrongDigit(t *testing.T) {
	s, _ := GenerateSecret()
	now := int64(1700000000)
	c := Code(s, now)
	// Bump the last digit.
	last := c[len(c)-1]
	if last == '9' {
		last = '0'
	} else {
		last++
	}
	wrong := c[:len(c)-1] + string(last)
	if Verify(s, wrong, now, 1) {
		t.Errorf("wrong digit should be rejected")
	}
}

func TestVerifyRejectsBadLength(t *testing.T) {
	s, _ := GenerateSecret()
	if Verify(s, "12345", 1700000000, 1) { // 5 digits — too short
		t.Errorf("short code should be rejected")
	}
	if Verify(s, "1234567", 1700000000, 1) { // 7 digits — too long
		t.Errorf("long code should be rejected")
	}
}

func TestProvisioningURI(t *testing.T) {
	uri := ProvisioningURI("Railbase", "alice@example.com", "ABCD1234EFGH5678IJKL9012MNOP3456")
	if !strings.HasPrefix(uri, "otpauth://totp/Railbase:alice@example.com?") {
		t.Errorf("unexpected URI prefix: %s", uri)
	}
	for _, want := range []string{"secret=ABCD", "issuer=Railbase", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI missing %q: %s", want, uri)
		}
	}
}

func TestVerifyLowerCaseSecret(t *testing.T) {
	s, _ := GenerateSecret()
	now := int64(1700000000)
	c := Code(s, now)
	if !Verify(strings.ToLower(s), c, now, 1) {
		t.Errorf("lower-cased secret should still verify")
	}
}

