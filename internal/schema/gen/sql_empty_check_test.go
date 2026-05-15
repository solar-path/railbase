// Regression test for FEEDBACK #B7 — CHECK regexes for typed
// fields (URL, Email, Tel, Slug, Color, Text+Pattern) used to reject
// the empty string. JSON forms POST `field: ""` rather than `null`
// for unset optional inputs, so the embedder got `400 "check
// constraint failed"` on every optional URL field whose user left it
// blank. The blogger project hit this on `hero_image`.
//
// Fix: for optional (non-Required) fields, wrap the CHECK predicate
// to also accept the empty string. Required fields keep the strict
// regex — an empty value in a required field IS a real validation
// failure that should fail loudly.
package gen

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
)

func TestCheckClauses_OptionalURL_AcceptsEmpty(t *testing.T) {
	f := builder.FieldSpec{
		Name:     "hero_image",
		Type:     builder.TypeURL,
		Required: false,
	}
	got := checkClauses(f)
	if len(got) == 0 {
		t.Fatalf("expected at least one CHECK clause, got 0")
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `hero_image = ''`) {
		t.Errorf("optional URL CHECK should also accept '': %v", got)
	}
	// The original regex predicate must still be present.
	if !strings.Contains(joined, `^https?://`) {
		t.Errorf("URL CHECK lost its regex: %v", got)
	}
}

func TestCheckClauses_RequiredURL_KeepsStrict(t *testing.T) {
	f := builder.FieldSpec{
		Name:     "homepage",
		Type:     builder.TypeURL,
		Required: true,
	}
	got := checkClauses(f)
	joined := strings.Join(got, " ")
	// Required URL must NOT have the empty-string escape — an empty
	// value in a required field should fail the check.
	if strings.Contains(joined, `homepage = ''`) {
		t.Errorf("required URL CHECK must NOT accept empty: %v", got)
	}
	if !strings.Contains(joined, `^https?://`) {
		t.Errorf("required URL CHECK lost its regex: %v", got)
	}
}

func TestCheckClauses_OptionalEmail_AcceptsEmpty(t *testing.T) {
	f := builder.FieldSpec{Name: "secondary_email", Type: builder.TypeEmail}
	got := checkClauses(f)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `secondary_email = ''`) {
		t.Errorf("optional email CHECK should accept '': %v", got)
	}
}

func TestCheckClauses_OptionalTel_AcceptsEmpty(t *testing.T) {
	f := builder.FieldSpec{Name: "phone", Type: builder.TypeTel}
	got := checkClauses(f)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `phone = ''`) {
		t.Errorf("optional tel CHECK should accept '': %v", got)
	}
}

func TestCheckClauses_OptionalSlug_AcceptsEmpty(t *testing.T) {
	f := builder.FieldSpec{Name: "permalink", Type: builder.TypeSlug}
	got := checkClauses(f)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `permalink = ''`) {
		t.Errorf("optional slug CHECK should accept '': %v", got)
	}
}

func TestCheckClauses_OptionalColor_AcceptsEmpty(t *testing.T) {
	f := builder.FieldSpec{Name: "accent", Type: builder.TypeColor}
	got := checkClauses(f)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `accent = ''`) {
		t.Errorf("optional color CHECK should accept '': %v", got)
	}
}

func TestCheckClauses_OptionalTextPattern_AcceptsEmpty(t *testing.T) {
	f := builder.FieldSpec{
		Name:    "code",
		Type:    builder.TypeText,
		Pattern: "^[A-Z]{3}$",
	}
	got := checkClauses(f)
	// MinLen/MaxLen don't get the wrap (length CHECKs of 0 work fine),
	// but the Pattern CHECK should be wrapped.
	hasPatternWrap := false
	for _, c := range got {
		if strings.Contains(c, "~ '^[A-Z]{3}$'") && strings.Contains(c, `"code" = ''`) {
			hasPatternWrap = true
		}
	}
	if !hasPatternWrap {
		t.Errorf("optional Text pattern CHECK should accept '': %v", got)
	}
}
