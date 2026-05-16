// Regression tests for FEEDBACK #B2 — the read-only public-profile
// endpoint. We unit-test the spec/column helpers; the DB-touching
// query path is covered by the broader REST e2e suite once the
// blogger-class consumer wires up .PublicProfile().
package rest

import (
	"sort"
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestPublicProfileSpec_OptedIn(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	registry.Register(
		builder.NewAuthCollection("authors").
			Field("display_name", builder.NewText()).
			PublicProfile(),
	)
	spec, err := publicProfileSpec("authors")
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if spec.Name != "authors" {
		t.Errorf("name: got %q", spec.Name)
	}
	if !spec.PublicProfile {
		t.Errorf("PublicProfile flag dropped")
	}
}

func TestPublicProfileSpec_AuthButNotOptedIn_404(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	registry.Register(
		builder.NewAuthCollection("users").
			Field("display_name", builder.NewText()),
	)
	_, err := publicProfileSpec("users")
	if err == nil {
		t.Errorf("expected 404 on non-opted-in auth collection, got nil")
	}
	// Probe-defence: the message must NOT betray that the collection
	// is auth-but-not-opted-in. Same shape as a missing collection.
	if err != nil && !strings.Contains(err.Error(), "not found") {
		t.Errorf("error must look like a generic 404, got: %v", err)
	}
}

func TestPublicProfileSpec_NonAuth_404(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	registry.Register(
		builder.NewCollection("posts").
			Field("title", builder.NewText()),
	)
	if _, err := publicProfileSpec("posts"); err == nil {
		t.Errorf("non-auth collection must 404, got nil error")
	}
}

func TestPublicProfileSpec_Unknown_404(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	if _, err := publicProfileSpec("nope"); err == nil {
		t.Errorf("unknown collection must 404, got nil error")
	}
}

func TestPublicProfileColumns_IncludesIDAndCustomFields(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	registry.Register(
		builder.NewAuthCollection("authors").
			Field("display_name", builder.NewText()).
			Field("bio", builder.NewText()).
			Field("avatar_url", builder.NewURL()).
			PublicProfile(),
	)
	spec, _ := publicProfileSpec("authors")
	got := publicProfileColumns(spec)
	sort.Strings(got)
	want := []string{"avatar_url", "bio", "display_name", "id"}
	if len(got) != len(want) {
		t.Fatalf("column count mismatch: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("col[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPublicProfileColumns_NeverLeaksSecretSystemColumns(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	registry.Register(
		builder.NewAuthCollection("authors").
			Field("display_name", builder.NewText()).
			PublicProfile(),
	)
	spec, _ := publicProfileSpec("authors")
	got := publicProfileColumns(spec)
	for _, banned := range []string{
		"email", "password_hash", "token_key", "verified",
		"last_login_at", "created", "updated",
	} {
		for _, name := range got {
			if name == banned {
				t.Errorf("public-profile columns leaked %q: %v", banned, got)
			}
		}
	}
}

func TestPublicProfileColumns_SkipsM2MRelations(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)
	// Target collection for the M2M.
	registry.Register(
		builder.NewCollection("tags").
			Field("label", builder.NewText()),
	)
	registry.Register(
		builder.NewAuthCollection("authors").
			Field("display_name", builder.NewText()).
			Field("tags", builder.NewRelations("tags")). // M2M
			PublicProfile(),
	)
	spec, _ := publicProfileSpec("authors")
	got := publicProfileColumns(spec)
	for _, name := range got {
		if name == "tags" {
			t.Errorf("M2M relation must not be SELECTed as a column: %v", got)
		}
	}
}

func TestQuoteColumns_BasicEscapes(t *testing.T) {
	got := quoteColumns([]string{"id", "display_name"})
	if got != `"id", "display_name"` {
		t.Errorf("simple cols: got %q", got)
	}
	// Defensive: even though field names are validated upstream, the
	// quoter handles embedded double-quotes via Postgres's `""` escape.
	got = quoteColumns([]string{`weird"name`})
	if got != `"weird""name"` {
		t.Errorf("embedded quote: got %q", got)
	}
}
