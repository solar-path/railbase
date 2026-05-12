package totp

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateRecoveryCodes(t *testing.T) {
	raw, hashed, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != RecoveryCodeCount || len(hashed) != RecoveryCodeCount {
		t.Errorf("expected %d codes, got raw=%d hashed=%d",
			RecoveryCodeCount, len(raw), len(hashed))
	}
	// Shape: xxxx-xxxx-xxxx (lowercase hex).
	for _, c := range raw {
		s := string(c)
		if len(s) != 14 {
			t.Errorf("code %q wrong length: %d", s, len(s))
		}
		if !strings.HasPrefix(s[4:], "-") || s[9] != '-' {
			t.Errorf("code %q missing hyphens", s)
		}
	}
	// Hashes should all differ.
	seen := map[string]bool{}
	for _, h := range hashed {
		if seen[h.Hash] {
			t.Errorf("duplicate hash %q", h.Hash)
		}
		seen[h.Hash] = true
		if h.UsedAt != nil {
			t.Errorf("fresh code should not be used")
		}
	}
}

func TestVerifyRecoveryCodeHappy(t *testing.T) {
	raw, hashed, _ := GenerateRecoveryCodes()
	for i, c := range raw {
		got := VerifyRecoveryCode(string(c), hashed)
		if got != i {
			t.Errorf("expected match at index %d, got %d (code=%q)", i, got, c)
		}
	}
}

func TestVerifyRecoveryCodeAcceptsNoHyphens(t *testing.T) {
	raw, hashed, _ := GenerateRecoveryCodes()
	// Strip hyphens.
	no := strings.ReplaceAll(string(raw[0]), "-", "")
	if idx := VerifyRecoveryCode(no, hashed); idx != 0 {
		t.Errorf("expected match for no-hyphen input, got %d", idx)
	}
}

func TestVerifyRecoveryCodeAcceptsUppercase(t *testing.T) {
	raw, hashed, _ := GenerateRecoveryCodes()
	upper := strings.ToUpper(string(raw[0]))
	if idx := VerifyRecoveryCode(upper, hashed); idx != 0 {
		t.Errorf("expected uppercase to match")
	}
}

func TestVerifyRecoveryCodeRejectsWrong(t *testing.T) {
	_, hashed, _ := GenerateRecoveryCodes()
	if idx := VerifyRecoveryCode("0000-0000-0000", hashed); idx != -1 {
		t.Errorf("random code should not match, got %d", idx)
	}
}

func TestVerifyRecoveryCodeRejectsBadShape(t *testing.T) {
	_, hashed, _ := GenerateRecoveryCodes()
	if idx := VerifyRecoveryCode("not-hex-here!", hashed); idx != -1 {
		t.Errorf("invalid shape should not match")
	}
	if idx := VerifyRecoveryCode("", hashed); idx != -1 {
		t.Errorf("empty should not match")
	}
}

func TestVerifyRecoveryCodeSkipsUsed(t *testing.T) {
	raw, hashed, _ := GenerateRecoveryCodes()
	// Mark first code used.
	now := time.Now().UTC()
	hashed[0].UsedAt = &now
	if idx := VerifyRecoveryCode(string(raw[0]), hashed); idx != -1 {
		t.Errorf("used code should not match, got %d", idx)
	}
	// Second still works.
	if idx := VerifyRecoveryCode(string(raw[1]), hashed); idx != 1 {
		t.Errorf("second code should still match, got %d", idx)
	}
}
