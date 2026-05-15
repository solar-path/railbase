// Regression tests for FEEDBACK #15 — the admin Stripe settings page
// should make a missing webhook secret obvious. Without this, an
// operator turns Stripe on, doesn't paste a `whsec_…` value, and
// silently rejects every incoming /api/stripe/webhook call with 503
// — the failure mode the shopper hit in production.
//
// The fix is a structured `warnings` field on the GET/PUT response.
// computeStripeWarnings is pure — these tests exercise every branch.
package adminapi

import (
	"strings"
	"testing"
)

func TestComputeStripeWarnings_AllGood(t *testing.T) {
	got := computeStripeWarnings(stripeConfigStatus{
		Enabled:          true,
		SecretKeySet:     true,
		WebhookSecretSet: true,
	})
	if len(got) != 0 {
		t.Errorf("fully-configured Stripe should produce no warnings, got: %#v", got)
	}
}

func TestComputeStripeWarnings_Disabled_NoWarnings(t *testing.T) {
	// Stripe disabled — even with no secrets the operator's intent is
	// clear (Stripe off), so don't nag them with warnings.
	got := computeStripeWarnings(stripeConfigStatus{
		Enabled:          false,
		SecretKeySet:     false,
		WebhookSecretSet: false,
	})
	if len(got) != 0 {
		t.Errorf("Stripe disabled should produce no warnings, got: %#v", got)
	}
}

func TestComputeStripeWarnings_EnabledNoWebhook(t *testing.T) {
	got := computeStripeWarnings(stripeConfigStatus{
		Enabled:          true,
		SecretKeySet:     true,
		WebhookSecretSet: false,
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 warning (webhook_secret_missing), got: %#v", got)
	}
	if got[0].Code != "webhook_secret_missing" {
		t.Errorf("expected code 'webhook_secret_missing', got %q", got[0].Code)
	}
	// The message must point at the exact recovery command — the
	// shopper-class operator should not have to grep docs.
	if !strings.Contains(got[0].Message, "stripe listen --forward-to") {
		t.Errorf("warning message must include the recovery command, got: %q", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "whsec_") {
		t.Errorf("warning message must mention the whsec_ prefix, got: %q", got[0].Message)
	}
}

func TestComputeStripeWarnings_EnabledNoSecretKey(t *testing.T) {
	got := computeStripeWarnings(stripeConfigStatus{
		Enabled:          true,
		SecretKeySet:     false,
		WebhookSecretSet: true,
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 warning (secret_key_missing), got: %#v", got)
	}
	if got[0].Code != "secret_key_missing" {
		t.Errorf("expected code 'secret_key_missing', got %q", got[0].Code)
	}
}

func TestComputeStripeWarnings_EnabledBothMissing(t *testing.T) {
	got := computeStripeWarnings(stripeConfigStatus{
		Enabled:          true,
		SecretKeySet:     false,
		WebhookSecretSet: false,
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 warnings (both missing), got: %#v", got)
	}
	// Order matters for stable UI rendering: webhook secret first
	// (that's the harder-to-diagnose silent failure).
	if got[0].Code != "webhook_secret_missing" {
		t.Errorf("first warning should be webhook_secret_missing, got %q", got[0].Code)
	}
	if got[1].Code != "secret_key_missing" {
		t.Errorf("second warning should be secret_key_missing, got %q", got[1].Code)
	}
}

