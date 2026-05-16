// Regression test for FEEDBACK #B9 — the `railbase admin create`
// help text now explicitly documents the positional-email shape with
// runnable examples. Earlier blogger-class users read the short help
// and reached for `--email X --password Y` (the wrong invocation),
// failed with "accepts 1 arg(s), received 0", and had to grep source
// to figure out the right shape.
package cli

import (
	"strings"
	"testing"
)

func TestAdminCreate_HelpShowsPositionalEmail(t *testing.T) {
	cmd := newAdminCreateCmd()
	long := cmd.Long
	// Must call out the positional shape explicitly.
	if !strings.Contains(long, "POSITIONAL") {
		t.Errorf("Long help must call out 'POSITIONAL' email arg, got:\n%s", long)
	}
	// Must include a runnable interactive example.
	if !strings.Contains(long, "railbase admin create ops@example.com") {
		t.Errorf("Long help must include a runnable example, got:\n%s", long)
	}
	// Must mention --no-email for the first-run-without-mailer path.
	if !strings.Contains(long, "--no-email") {
		t.Errorf("Long help must document --no-email, got:\n%s", long)
	}
}

func TestAdminCreate_UseStringIsPositional(t *testing.T) {
	cmd := newAdminCreateCmd()
	if !strings.Contains(cmd.Use, "<email>") {
		t.Errorf("Use string must show positional shape `<email>`, got: %q", cmd.Use)
	}
	// Defensive — if someone refactors to `--email`, this test rejects it.
	if strings.Contains(cmd.Use, "--email") {
		t.Errorf("Use string must NOT advertise --email (positional only): %q", cmd.Use)
	}
}
