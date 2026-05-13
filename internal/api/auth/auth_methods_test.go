package auth

// v1.7.0 — unit tests for the auth-methods helpers. Pure functions, no
// DB. The full-stack route-level smoke lives in auth_methods_e2e_test.go.

import (
	"context"
	"testing"

	"github.com/railbase/railbase/internal/auth/recordtoken"
)

func TestProviderDisplayName_KnownProviders(t *testing.T) {
	cases := map[string]string{
		"google":    "Google",
		"github":    "GitHub",
		"apple":     "Apple",
		"microsoft": "Microsoft",
		"discord":   "Discord",
		"twitter":   "X (Twitter)",
		"x":         "X (Twitter)",
		"gitlab":    "GitLab",
		"linkedin":  "LinkedIn",
	}
	for in, want := range cases {
		if got := providerDisplayName(in); got != want {
			t.Errorf("providerDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProviderDisplayName_UnknownFallsBackToTitleCase(t *testing.T) {
	// Operators wiring keycloak / zitadel / authentik / a custom name
	// should see a button label that's at least readable. We title-case
	// the first rune and pass the rest through — better than "google"
	// and far cheaper than maintaining an exhaustive lookup table.
	cases := map[string]string{
		"keycloak":  "Keycloak",
		"zitadel":   "Zitadel",
		"authentik": "Authentik",
		"x":         "X (Twitter)", // re-check: known wins over fallback
	}
	for in, want := range cases {
		if got := providerDisplayName(in); got != want {
			t.Errorf("providerDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProviderDisplayName_EmptyIsEmpty(t *testing.T) {
	// Defensive — the registry rejects empty provider names at boot,
	// but a future regression shouldn't crash this helper.
	if got := providerDisplayName(""); got != "" {
		t.Errorf("providerDisplayName(\"\") = %q, want empty", got)
	}
}

func TestBuildPasswordBlock_AlwaysEnabled(t *testing.T) {
	// v1.7.47: signature added ctx + *Deps. Default (nil Settings) still
	// reports enabled=true so the v1.7.0 capability-baseline contract
	// holds for every test path that doesn't wire Settings.
	b := buildPasswordBlock(context.Background(), &Deps{})
	if b["enabled"] != true {
		t.Errorf("password.enabled = %v, want true", b["enabled"])
	}
	fields, ok := b["identityFields"].([]string)
	if !ok {
		t.Fatalf("password.identityFields not []string: %T", b["identityFields"])
	}
	if len(fields) != 1 || fields[0] != "email" {
		t.Errorf("identityFields = %v, want [email]", fields)
	}
}

func TestBuildOAuth2Block_NilRegistry_EmptySlice(t *testing.T) {
	// Empty SLICE (not omitted, not nil) so the JS SDK can do
	//   resp.oauth2.length === 0
	// without optional-chaining the field. JSON encoders treat nil
	// and empty []any differently — we want the empty form.
	d := &Deps{OAuth: nil}
	out := buildOAuth2Block(context.Background(), d)
	if out == nil {
		t.Fatal("oauth2 = nil, want empty slice")
	}
	if len(out) != 0 {
		t.Errorf("oauth2 = %d entries, want 0", len(out))
	}
}

func TestBuildOTPBlock_DurationMatchesRecordTokenDefault(t *testing.T) {
	// If the recordtoken default TTL changes, the discovery shape
	// should track it automatically. Don't hard-code 600 here.
	want := int(recordtoken.DefaultTTL(recordtoken.PurposeOTP).Seconds())
	d := &Deps{}
	b := buildOTPBlock(context.Background(), d)
	if b["duration"] != want {
		t.Errorf("otp.duration = %v, want %d", b["duration"], want)
	}
}

func TestBuildOTPBlock_RequiresBothMailerAndStore(t *testing.T) {
	// Mailer alone or RecordTokens alone — both must be wired for
	// passwordless OTP to actually work end-to-end.
	if buildOTPBlock(context.Background(), &Deps{})["enabled"] != false {
		t.Error("otp.enabled with no deps should be false")
	}
}

func TestBuildMFABlock_DefaultDuration300(t *testing.T) {
	// 5 minutes — matches mfa.DefaultChallengeTTL. Hard-coded here as
	// a guard: if the MFA package bumps the default, this test should
	// fail loudly so we update both sides.
	ctx := context.Background()
	if buildMFABlock(ctx, &Deps{})["duration"] != 300 {
		t.Errorf("mfa.duration = %v, want 300", buildMFABlock(ctx, &Deps{})["duration"])
	}
}

func TestBuildMFABlock_RequiresBothEnrollmentAndChallenge(t *testing.T) {
	if buildMFABlock(context.Background(), &Deps{})["enabled"] != false {
		t.Error("mfa.enabled with no deps should be false")
	}
}

func TestBuildWebAuthnBlock_EnabledTracksVerifier(t *testing.T) {
	// Verifier is the load-bearing dep — the discovery block reflects
	// its presence. Store can be wired alone (admin-API enumeration)
	// without making the signin path advertise readiness.
	if buildWebAuthnBlock(context.Background(), &Deps{})["enabled"] != false {
		t.Error("webauthn.enabled with nil verifier should be false")
	}
}
