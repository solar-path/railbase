package recordtoken

import (
	"testing"
	"time"
)

// recordtoken needs Postgres for full coverage; the e2e flows
// exercise it end-to-end through the auth API. These tests cover
// the pure surface that doesn't need a DB.

func TestDefaultTTL_KnownPurposes(t *testing.T) {
	cases := map[Purpose]time.Duration{
		PurposeVerify:      24 * time.Hour,
		PurposeReset:       time.Hour,
		PurposeEmailChange: 24 * time.Hour,
		PurposeMagicLink:   15 * time.Minute,
		PurposeOTP:         10 * time.Minute,
		PurposeFileAccess:  time.Hour,
	}
	for p, want := range cases {
		if got := DefaultTTL(p); got != want {
			t.Errorf("DefaultTTL(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDefaultTTL_UnknownPurpose(t *testing.T) {
	if got := DefaultTTL("nope"); got != time.Hour {
		t.Errorf("unknown purpose ttl = %v, want 1h fallback", got)
	}
}

func TestPurpose_StringStable(t *testing.T) {
	// Renaming any of these breaks tokens already issued in
	// production. Lock the wire values down.
	cases := map[Purpose]string{
		PurposeVerify:      "verify",
		PurposeReset:       "reset",
		PurposeEmailChange: "email_change",
		PurposeMagicLink:   "magic_link",
		PurposeOTP:         "otp",
		PurposeFileAccess:  "file_access",
	}
	for p, want := range cases {
		if string(p) != want {
			t.Errorf("Purpose(%q) wire form drifted to %q", want, string(p))
		}
	}
}
