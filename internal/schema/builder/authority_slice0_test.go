// v2.0-alpha — DoA Slice 0 prototype DSL tests.
//
// *** PROTOTYPE — NOT FOR PRODUCTION USE ***
//
// Validates the schema DSL surface for `.Authority(...)` builder
// method. Full HTTP / DB / matrix-selection round-trip needs the
// admin REST handlers + gate middleware (next Slice 0 ticks).
package builder

import "testing"

func TestAuthority_BasicShape(t *testing.T) {
	c := NewCollection("articles").
		Field("title", NewText().Required()).
		Field("status", NewSelect("draft", "in_review", "published")).
		Authority(AuthorityConfig{
			Name:   "publish",
			Matrix: "articles.publish",
			On: AuthorityOn{
				Op:    "update",
				Field: "status",
				To:    []string{"published"},
			},
			ProtectedFields: []string{"title", "body"},
			Required:        true,
		})

	spec := c.Spec()
	if len(spec.Authorities) != 1 {
		t.Fatalf("Authorities len: got %d, want 1", len(spec.Authorities))
	}
	a := spec.Authorities[0]
	if a.Name != "publish" {
		t.Errorf("Name: got %q, want %q", a.Name, "publish")
	}
	if a.Matrix != "articles.publish" {
		t.Errorf("Matrix: got %q, want %q", a.Matrix, "articles.publish")
	}
	if a.On.Op != "update" || a.On.Field != "status" {
		t.Errorf("On: got %+v, want Op=update Field=status", a.On)
	}
	if len(a.On.To) != 1 || a.On.To[0] != "published" {
		t.Errorf("On.To: got %v, want [published]", a.On.To)
	}
	if len(a.ProtectedFields) != 2 {
		t.Errorf("ProtectedFields: got %v, want 2 entries", a.ProtectedFields)
	}
	if !a.Required {
		t.Error("Required: got false, want true")
	}
}

func TestAuthority_MultipleGatePoints(t *testing.T) {
	// Multiple Authority() calls on the same collection are legitimate —
	// each declares a distinct gate point (different transition or different
	// matrix). Per docs/26 rev2 — multi-Authority is the normal case for
	// any non-trivial collection.
	c := NewCollection("articles").
		Field("status", NewSelect("draft", "published", "archived")).
		Authority(AuthorityConfig{
			Name:   "publish",
			Matrix: "articles.publish",
			On:     AuthorityOn{Op: "update", Field: "status", To: []string{"published"}},
		}).
		Authority(AuthorityConfig{
			Name:   "archive",
			Matrix: "articles.archive",
			On:     AuthorityOn{Op: "update", Field: "status", To: []string{"archived"}},
		}).
		Authority(AuthorityConfig{
			Name:   "takedown",
			Matrix: "articles.takedown",
			On:     AuthorityOn{Op: "delete"},
		})

	spec := c.Spec()
	if len(spec.Authorities) != 3 {
		t.Fatalf("Authorities len: got %d, want 3", len(spec.Authorities))
	}
	wantNames := []string{"publish", "archive", "takedown"}
	for i, want := range wantNames {
		if spec.Authorities[i].Name != want {
			t.Errorf("Authorities[%d].Name: got %q, want %q",
				i, spec.Authorities[i].Name, want)
		}
	}
	// Check Op discriminator preservation.
	if spec.Authorities[2].On.Op != "delete" {
		t.Errorf("takedown Op: got %q, want delete", spec.Authorities[2].On.Op)
	}
	if spec.Authorities[2].On.Field != "" {
		t.Errorf("delete Op should have empty Field, got %q", spec.Authorities[2].On.Field)
	}
}

func TestAuthority_AmountFieldMateriality(t *testing.T) {
	// AmountField + Currency enable materiality-based matrix selection —
	// at runtime the gate looks up _doa_matrices WHERE
	// min_amount <= record.<AmountField> < max_amount AND currency = Currency.
	c := NewCollection("expenses").
		Field("amount_cents", NewNumber().Int()).
		Field("status", NewSelect("draft", "submitted", "approved")).
		Authority(AuthorityConfig{
			Name:        "approve",
			Matrix:      "expenses.approve",
			On:          AuthorityOn{Op: "update", Field: "status", To: []string{"approved"}},
			AmountField: "amount_cents",
			Currency:    "USD",
		})

	spec := c.Spec()
	if len(spec.Authorities) != 1 {
		t.Fatalf("Authorities len: got %d, want 1", len(spec.Authorities))
	}
	a := spec.Authorities[0]
	if a.AmountField != "amount_cents" {
		t.Errorf("AmountField: got %q, want amount_cents", a.AmountField)
	}
	if a.Currency != "USD" {
		t.Errorf("Currency: got %q, want USD", a.Currency)
	}
}

func TestAuthority_EmptyConfigPersists(t *testing.T) {
	// Zero-value AuthorityConfig must round-trip through the builder.
	// Useful for testing the absence path (collection without DoA) —
	// we want zero-value to be detectable as "no authorities declared".
	c := NewCollection("posts").
		Field("title", NewText().Required())

	spec := c.Spec()
	if len(spec.Authorities) != 0 {
		t.Errorf("collection without Authority() should have 0 Authorities, got %d",
			len(spec.Authorities))
	}
}
