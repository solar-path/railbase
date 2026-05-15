// Regression test for FEEDBACK #8 — the error returned when an
// embedder tries `POST /api/collections/users/records` against an
// auth collection used to read:
//
//   "collection \"users\" is an auth collection; use /api/collections/users/auth-* endpoints"
//
// Technically correct — actually unhelpful. A first-time integrator
// doesn't know which `auth-*` endpoints exist or which one to call
// for the first user. They grep the source.
//
// The improved message names the four common endpoints inline:
// auth-signup, auth-with-password, auth-me, auth-otp / auth-magic-link.
// This test pins the new wording so a refactor can't silently regress
// to the cryptic old form.
package rest

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestResolveCollection_AuthCollectionError_NamesEndpoints(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	// AuthCollection auto-injects email/password_hash/verified/token_key;
	// add a single custom field so it has at least one user-defined column.
	registry.Register(
		builder.NewAuthCollection("users").
			Field("display_name", builder.NewText()),
	)

	_, err := resolveCollection("users")
	if err == nil {
		t.Fatalf("expected error for auth collection, got nil")
	}
	msg := err.Error()

	// The new message must reference at least the three first-touch
	// endpoints. An integrator reading the error in a curl response
	// should know what to call next without grep-ing the source.
	for _, want := range []string{
		"auth-signup",
		"auth-with-password",
		"auth-me",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("auth-collection error doesn't name %q endpoint:\n%s", want, msg)
		}
	}
	// And the collection name must still be quoted so the operator
	// can grep their own schema for it.
	if !strings.Contains(msg, `"users"`) {
		t.Errorf("auth-collection error doesn't quote the collection name:\n%s", msg)
	}
}

// TestResolveCollection_NonAuth_NoError — a regular collection must
// still resolve without an error; the new auth-collection branch
// shouldn't accidentally trip on non-auth collections.
func TestResolveCollection_NonAuth_NoError(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	registry.Register(
		builder.NewCollection("posts").
			Field("title", builder.NewText().Required()),
	)

	spec, err := resolveCollection("posts")
	if err != nil {
		t.Fatalf("resolveCollection(posts): %v", err)
	}
	if spec.Auth {
		t.Errorf("posts collection should not be flagged Auth")
	}
}
