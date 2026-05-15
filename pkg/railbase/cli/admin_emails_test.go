// Unit test for FEEDBACK #10 — `railbase admin create` should warn
// the operator up-front when `mailer.from` isn't set, instead of
// silently enqueueing a welcome email that will never deliver.
//
// We can't run the full `admin create` path here without a Postgres
// pool, so we test the pure decision function — proves the three
// branches: configured / unconfigured / explicit-skip.
package cli

import (
	"context"
	"errors"
	"testing"
)

// stubGetter is an in-memory settings getter for the unit test.
type stubGetter map[string]string

func (g stubGetter) GetString(_ context.Context, key string) (string, bool, error) {
	v, ok := g[key]
	return v, ok, nil
}

func TestMailerUnconfiguredFrom_Configured(t *testing.T) {
	g := stubGetter{
		"mailer.from": "admin@example.com",
	}
	if mailerUnconfiguredFrom(context.Background(), g) {
		t.Errorf("with mailer.from set, helper must return false (configured)")
	}
}

func TestMailerUnconfiguredFrom_Empty(t *testing.T) {
	g := stubGetter{} // nothing set
	if !mailerUnconfiguredFrom(context.Background(), g) {
		t.Errorf("with nothing set, helper must return true (unconfigured)")
	}
}

func TestMailerUnconfiguredFrom_ExplicitSkip(t *testing.T) {
	g := stubGetter{
		"mailer.setup_skipped_at": "2026-01-01T00:00:00Z",
	}
	// Operator explicitly opted out — the helper must NOT yell at
	// them about an unset `mailer.from`, because they already know.
	if mailerUnconfiguredFrom(context.Background(), g) {
		t.Errorf("with setup_skipped_at set, helper must return false (operator opted out)")
	}
}

func TestMailerUnconfiguredFrom_WhitespaceFrom(t *testing.T) {
	g := stubGetter{
		"mailer.from": "   ",
	}
	// "  " is not a valid From — must be treated as unset.
	if !mailerUnconfiguredFrom(context.Background(), g) {
		t.Errorf("whitespace-only mailer.from must be treated as unconfigured")
	}
}

// erroringGetter — proves the helper is robust against settings-layer
// errors. We don't want the admin-create command to refuse to run
// because the settings table is briefly unreadable.
type erroringGetter struct{}

func (erroringGetter) GetString(_ context.Context, _ string) (string, bool, error) {
	return "", false, errors.New("settings unavailable")
}

func TestMailerUnconfiguredFrom_GetterError(t *testing.T) {
	if !mailerUnconfiguredFrom(context.Background(), erroringGetter{}) {
		// Errors are equivalent to "not set" — better to emit the
		// (potentially redundant) note than to swallow the warning.
		t.Errorf("on getter error, helper must default to true (we'd rather warn than miss)")
	}
}
