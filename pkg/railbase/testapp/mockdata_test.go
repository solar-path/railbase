//go:build embed_pg

// v1.7.20a — self-tests for the MockData helper.
//
// Shares the parent embedded-PG harness pattern from testapp_test.go.
// Each subtest registers its own collection name so row counts are
// independent and the cold boot (~12-25s) is amortised once.

package testapp

import (
	"reflect"
	"testing"

	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
)

func TestMockData(t *testing.T) {
	if testing.Short() {
		t.Skip("testapp: skipping in -short mode")
	}

	// Single TestApp shared across subtests. Re-uses the same shape as
	// TestTestApp; each subtest scopes itself to a fresh collection name.
	app := New(t)
	defer app.Close()

	t.Run("MockData_GeneratesValidRows", func(t *testing.T) {
		// 5-field collection mixing primitives + domain types. Generate
		// 5 rows; assert all 5 expected keys present on every row.
		spec := schemabuilder.NewCollection("mockdata_basic").
			Field("email", schemabuilder.NewEmail().Required()).
			Field("title", schemabuilder.NewText().Required()).
			Field("active", schemabuilder.NewBool()).
			Field("score", schemabuilder.NewNumber().Int()).
			Field("country", schemabuilder.NewCountry())

		md := NewMockData(spec).Seed(1)
		rows := md.Generate(5)
		if len(rows) != 5 {
			t.Fatalf("Generate(5): got %d rows", len(rows))
		}
		wantKeys := []string{"email", "title", "active", "score", "country"}
		for i, row := range rows {
			for _, k := range wantKeys {
				if _, ok := row[k]; !ok {
					t.Errorf("row %d: missing key %q (got %v)", i, k, row)
				}
			}
			// Spot-check a couple of types so a regression in generateValue
			// surfaces here rather than at insert time.
			if _, ok := row["email"].(string); !ok {
				t.Errorf("row %d: email not string: %T", i, row["email"])
			}
			if _, ok := row["active"].(bool); !ok {
				t.Errorf("row %d: active not bool: %T", i, row["active"])
			}
		}
	})

	t.Run("MockData_RespectsOverrides", func(t *testing.T) {
		spec := schemabuilder.NewCollection("mockdata_overrides").
			Field("title", schemabuilder.NewText().Required()).
			Field("status", schemabuilder.NewSelect("draft", "published", "archived"))

		md := NewMockData(spec).Seed(2).Set("status", "draft")
		rows := md.Generate(10)
		if len(rows) != 10 {
			t.Fatalf("Generate(10): got %d", len(rows))
		}
		for i, row := range rows {
			if got := row["status"]; got != "draft" {
				t.Errorf("row %d: override not applied: status=%v", i, got)
			}
		}
	})

	t.Run("MockData_DeterministicWithSeed", func(t *testing.T) {
		spec := schemabuilder.NewCollection("mockdata_seed").
			Field("title", schemabuilder.NewText()).
			Field("email", schemabuilder.NewEmail()).
			Field("score", schemabuilder.NewNumber().Int())

		a := NewMockData(spec).Seed(12345).Generate(7)
		b := NewMockData(spec).Seed(12345).Generate(7)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("Generate with same seed produced different output:\n a=%v\n b=%v", a, b)
		}
		// And a different seed yields different rows (sanity check —
		// astronomically unlikely to collide on 7 rows of 3 fields).
		c := NewMockData(spec).Seed(67890).Generate(7)
		if reflect.DeepEqual(a, c) {
			t.Errorf("different seeds produced identical output — pseudo-RNG not seeded?")
		}
	})

	t.Run("MockData_GenerateAndInsert_PersistsRows", func(t *testing.T) {
		// End-to-end: register a collection on the live app, then
		// generate + POST 10 rows via the actor. Assert the list endpoint
		// reports 10 items.
		a := app.WithTB(t)
		// Default rules in v0.4+ are LOCKED — opt into public access for
		// the anonymous-actor smoke check.
		spec := schemabuilder.NewCollection("mockdata_persist").
			Field("title", schemabuilder.NewText().Required()).
			Field("score", schemabuilder.NewNumber().Int()).
			ListRule("true").
			CreateRule("true")
		a.Register(spec)

		md := NewMockData(spec).Seed(99)
		ids := md.GenerateAndInsert(a.AsAnonymous(), 10)
		if len(ids) != 10 {
			t.Fatalf("GenerateAndInsert: got %d ids, want 10", len(ids))
		}

		// Verify via REST list — perPage default may be lower than 10
		// (typical 30); use perPage=50 to be safe.
		body := a.AsAnonymous().
			Get("/api/collections/mockdata_persist/records?perPage=50").
			Status(200).
			JSON()
		items, _ := body["items"].([]any)
		if len(items) != 10 {
			t.Errorf("list returned %d items, want 10", len(items))
		}
	})
}
