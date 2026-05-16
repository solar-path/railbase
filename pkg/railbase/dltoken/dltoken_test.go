// Smoke test for the public dltoken re-export.
package dltoken_test

import (
	"errors"
	"testing"

	"github.com/railbase/railbase/pkg/railbase/dltoken"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestSignVerify_RoundTripThroughReExport(t *testing.T) {
	path := "/api/collections/orders/export.xlsx"
	tok, exp, err := dltoken.Sign(secret, path, dltoken.SignOptions{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if exp.IsZero() {
		t.Errorf("Sign returned zero expiry")
	}
	if err := dltoken.Verify(secret, path, tok); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestSentinelErrors_AreReachable(t *testing.T) {
	// Verifying with an obviously-bad token must return ErrInvalid
	// (not the internal error type — the embedder's errors.Is must
	// resolve against the public sentinel).
	err := dltoken.Verify(secret, "/x", "garbage")
	if !errors.Is(err, dltoken.ErrInvalid) {
		t.Errorf("expected re-exported ErrInvalid, got %v", err)
	}
}

func TestConstants_Reasonable(t *testing.T) {
	// Tests use the constants — refactors that change their type
	// (e.g. drop time.Duration) would fail to compile this file.
	if dltoken.DefaultTTL <= 0 {
		t.Errorf("DefaultTTL must be positive, got %v", dltoken.DefaultTTL)
	}
	if dltoken.MaxTTL <= dltoken.DefaultTTL {
		t.Errorf("MaxTTL %v must exceed DefaultTTL %v", dltoken.MaxTTL, dltoken.DefaultTTL)
	}
}
