// Package stripe is Railbase's Stripe billing integration: a local
// product/price catalog that is pushed up to Stripe, plus read-side
// mirror tables for customers, subscriptions and one-time payments
// kept in sync by a signature-verified webhook handler.
//
// Layout:
//
//	config.go   — Stripe credentials, stored in `_settings` (this file)
//	stripe.go   — DB models + Store (CRUD over the six _stripe_* tables)
//	client.go   — thin wrapper over the stripe-go SDK
//	service.go  — business logic: catalog push, checkout, subscriptions
//	webhook.go  — Stripe event verification + mirror-table dispatch
//
// Credentials live in `_settings` under the `stripe.*` namespace (same
// plaintext-JSONB store the mailer uses) rather than env vars, so they
// are operator-editable from the admin UI at runtime.
package stripe

import (
	"context"
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/settings"
)

// Settings keys. Dotted lower-case, JSONB values — the convention the
// settings.Manager enforces.
const (
	keySecretKey      = "stripe.secret_key"
	keyPublishableKey = "stripe.publishable_key"
	keyWebhookSecret  = "stripe.webhook_secret"
	keyEnabled        = "stripe.enabled"
)

// Mode is the Stripe environment a secret key targets, derived from
// its prefix. There is no separate "mode" setting — the key carries it.
type Mode string

const (
	ModeUnset Mode = ""     // no secret key configured
	ModeTest  Mode = "test" // sk_test_… / rk_test_…
	ModeLive  Mode = "live" // sk_live_… / rk_live_…
)

// Config is the full Stripe credential set. Secrets are plaintext here;
// the admin API redacts them on read (see adminapi/stripe.go).
type Config struct {
	SecretKey      string `json:"-"`
	PublishableKey string `json:"publishable_key"`
	WebhookSecret  string `json:"-"`
	// Enabled is the operator's master switch. Even with valid keys,
	// every Stripe call short-circuits when Enabled is false.
	Enabled bool `json:"enabled"`
}

// Mode reports test/live/unset from the secret key prefix.
func (c Config) Mode() Mode {
	switch {
	case strings.HasPrefix(c.SecretKey, "sk_live_"), strings.HasPrefix(c.SecretKey, "rk_live_"):
		return ModeLive
	case strings.HasPrefix(c.SecretKey, "sk_test_"), strings.HasPrefix(c.SecretKey, "rk_test_"):
		return ModeTest
	default:
		return ModeUnset
	}
}

// Ready reports whether Stripe calls can actually be made: enabled,
// with a secret key that parses as test or live.
func (c Config) Ready() bool {
	return c.Enabled && c.Mode() != ModeUnset
}

// LoadConfig reads the `stripe.*` keys out of the settings store. A
// missing key yields its zero value — an unconfigured install loads a
// blank, !Ready Config without error.
func LoadConfig(ctx context.Context, sm *settings.Manager) (Config, error) {
	if sm == nil {
		return Config{}, fmt.Errorf("stripe: settings manager is nil")
	}
	var c Config
	var err error
	if c.SecretKey, _, err = sm.GetString(ctx, keySecretKey); err != nil {
		return Config{}, fmt.Errorf("stripe: load secret key: %w", err)
	}
	if c.PublishableKey, _, err = sm.GetString(ctx, keyPublishableKey); err != nil {
		return Config{}, fmt.Errorf("stripe: load publishable key: %w", err)
	}
	if c.WebhookSecret, _, err = sm.GetString(ctx, keyWebhookSecret); err != nil {
		return Config{}, fmt.Errorf("stripe: load webhook secret: %w", err)
	}
	if c.Enabled, _, err = sm.GetBool(ctx, keyEnabled); err != nil {
		return Config{}, fmt.Errorf("stripe: load enabled flag: %w", err)
	}
	return c, nil
}

// SaveConfig writes the full Config back to the settings store. Callers
// that want keep-if-empty semantics for secrets (don't clobber a stored
// key when the admin leaves the field blank) must merge against a prior
// LoadConfig before calling this — SaveConfig writes verbatim.
func SaveConfig(ctx context.Context, sm *settings.Manager, c Config) error {
	if sm == nil {
		return fmt.Errorf("stripe: settings manager is nil")
	}
	if err := sm.Set(ctx, keySecretKey, c.SecretKey); err != nil {
		return fmt.Errorf("stripe: save secret key: %w", err)
	}
	if err := sm.Set(ctx, keyPublishableKey, c.PublishableKey); err != nil {
		return fmt.Errorf("stripe: save publishable key: %w", err)
	}
	if err := sm.Set(ctx, keyWebhookSecret, c.WebhookSecret); err != nil {
		return fmt.Errorf("stripe: save webhook secret: %w", err)
	}
	if err := sm.Set(ctx, keyEnabled, c.Enabled); err != nil {
		return fmt.Errorf("stripe: save enabled flag: %w", err)
	}
	return nil
}
