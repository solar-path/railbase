// Regression tests for FEEDBACK #B5 — `railbase dev` was ignoring
// $RAILBASE_HTTP_ADDR. The blogger project set `:8096` in .env to
// avoid a port collision and still got `:8095` from the cobra flag
// default. The resolveDevAddr helper encodes the new precedence.
package cli

import "testing"

func TestResolveDevAddr_ExplicitFlagWins(t *testing.T) {
	got := resolveDevAddr(":9000", true, ":7777")
	if got != ":9000" {
		t.Errorf("explicit --addr must win over env, got %q", got)
	}
}

func TestResolveDevAddr_EnvFallback(t *testing.T) {
	got := resolveDevAddr(":8095", false, ":8096")
	if got != ":8096" {
		t.Errorf("env should override default when --addr unspecified, got %q", got)
	}
}

func TestResolveDevAddr_DefaultWhenNoEnv(t *testing.T) {
	got := resolveDevAddr(":8095", false, "")
	if got != ":8095" {
		t.Errorf("empty env → default kept, got %q", got)
	}
}

func TestResolveDevAddr_WhitespaceEnv(t *testing.T) {
	got := resolveDevAddr(":8095", false, "   ")
	if got != ":8095" {
		t.Errorf("whitespace-only env must be treated as unset, got %q", got)
	}
}

func TestResolveDevAddr_ExplicitFlagOverridesEnvEvenWhenEqualToDefault(t *testing.T) {
	// Edge: operator deliberately passes `--addr :8095` (the cobra
	// default) — they want :8095 even though env says :8096.
	got := resolveDevAddr(":8095", true, ":8096")
	if got != ":8095" {
		t.Errorf("flagChanged=true must win regardless of value parity, got %q", got)
	}
}
