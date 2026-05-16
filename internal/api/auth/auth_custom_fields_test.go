// Regression tests for FEEDBACK #B8 — the auth response previously
// returned only the system fields (id/email/verified/last_login_at/…)
// even when the AuthCollection had custom profile fields. The
// blogger project hit this on `authors.{name,title,bio,avatar_url}` —
// every byline required a second GET.
//
// The fix layers an extra SELECT onto writeAuthResponse to pull
// the non-secret user-declared columns and merge them into the
// returned Record. These tests cover the pure name-filter helper;
// the DB-touching part is covered by the existing auth e2e tests
// once they assert the merged shape.
package auth

import (
	"reflect"
	"sort"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestAuthCustomFieldNames_IncludesUserFields(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	registry.Register(
		builder.NewAuthCollection("authors").
			Field("display_name", builder.NewText()).
			Field("bio", builder.NewText()).
			Field("avatar_url", builder.NewURL()),
	)

	got := authCustomFieldNames("authors")
	sort.Strings(got)
	want := []string{"avatar_url", "bio", "display_name"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("authCustomFieldNames: got %v, want %v", got, want)
	}
}

func TestAuthCustomFieldNames_ExcludesSystemColumns(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	registry.Register(
		builder.NewAuthCollection("users").
			Field("display_name", builder.NewText()),
	)
	got := authCustomFieldNames("users")
	for _, banned := range []string{
		"id", "email", "password_hash", "token_key", "verified", "last_login_at",
		"created", "updated",
	} {
		for _, name := range got {
			if name == banned {
				t.Errorf("system column %q leaked into custom fields: %v", banned, got)
			}
		}
	}
}

func TestAuthCustomFieldNames_NotAuthCollection_ReturnsNil(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	// Regular collection — must not yield any custom-field set because
	// non-auth collections don't go through writeAuthResponse anyway,
	// but a stale callsite shouldn't get back a list either.
	registry.Register(
		builder.NewCollection("posts").
			Field("title", builder.NewText()),
	)
	got := authCustomFieldNames("posts")
	if got != nil {
		t.Errorf("regular collection should yield nil custom-field set, got %v", got)
	}
}

func TestAuthCustomFieldNames_UnknownCollection_ReturnsNil(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	if got := authCustomFieldNames("does_not_exist"); got != nil {
		t.Errorf("unknown collection should yield nil, got %v", got)
	}
}

func TestAuthCustomFieldNames_SkipsM2MRelations(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	// Register the "tags" collection M2M points at — registry validates
	// the target exists when the relation is declared.
	registry.Register(
		builder.NewCollection("tags").
			Field("label", builder.NewText()),
	)
	registry.Register(
		builder.NewAuthCollection("authors").
			Field("display_name", builder.NewText()).
			Field("tags", builder.NewRelations("tags")), // M2M
	)
	got := authCustomFieldNames("authors")
	for _, name := range got {
		if name == "tags" {
			t.Errorf("M2M relations must not be SELECTed as a column: %v", got)
		}
	}
	// display_name should still be there.
	found := false
	for _, name := range got {
		if name == "display_name" {
			found = true
		}
	}
	if !found {
		t.Errorf("non-relation field missing: %v", got)
	}
}
