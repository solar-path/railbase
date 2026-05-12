package cli

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/schema/builder"
)

// v1.6.6 export CLI — unit tests for the column-resolution + SQL
// helper logic. E2e against a live Postgres lives in export_e2e_test.go.

func TestFirstNonEmpty_CLI(t *testing.T) {
	if got := firstNonEmpty("", "", "c"); got != "c" {
		t.Errorf("got %q", got)
	}
	if got := firstNonEmpty("a", "b", "c"); got != "a" {
		t.Errorf("got %q", got)
	}
	if got := firstNonEmpty(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func textSpec() builder.CollectionSpec {
	return builder.NewCollection("posts").
		Field("title", builder.NewText().Required()).
		Field("status", builder.NewText()).
		Spec()
}

func TestAllReadableColumnsForCLI(t *testing.T) {
	cols := allReadableColumnsForCLI(textSpec())
	// id, created, updated, title, status → 5 defaults.
	if len(cols) != 5 {
		t.Errorf("got %d cols: %v", len(cols), cols)
	}
	// First three are always system fields, in the documented order.
	if cols[0].Key != "id" || cols[1].Key != "created" || cols[2].Key != "updated" {
		t.Errorf("system field order: %v", cols)
	}
}

func TestAllReadableColumnsForCLI_SkipsFilePassword(t *testing.T) {
	spec := builder.NewCollection("c").
		Field("title", builder.NewText().Required()).
		Field("avatar", builder.NewFile()).
		Field("docs", builder.NewFiles()).
		Field("authors", builder.NewRelations("users")).
		Spec()
	cols := allReadableColumnsForCLI(spec)
	for _, c := range cols {
		if c.Key == "avatar" || c.Key == "docs" || c.Key == "authors" {
			t.Errorf("unexpected column %q in default set", c.Key)
		}
	}
}

func TestAllReadableColumnsForCLI_HierarchyAndSystemFlags(t *testing.T) {
	spec := builder.NewCollection("comments").
		Field("body", builder.NewText().Required()).
		Tenant().
		SoftDelete().
		AdjacencyList().
		Ordered().
		Spec()
	cols := allReadableColumnsForCLI(spec)
	keys := keysOf(cols)
	for _, want := range []string{"id", "created", "updated", "tenant_id", "deleted", "parent", "sort_index", "body"} {
		if !contains(keys, want) {
			t.Errorf("missing %q in %v", want, keys)
		}
	}
}

func TestNarrowColumns_QueryWinsOverConfig(t *testing.T) {
	all := []export.Column{
		{Key: "id"}, {Key: "title"}, {Key: "status"},
	}
	out, err := narrowColumns(all, "id", []string{"title", "status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "id" {
		t.Errorf("query should win: %v", out)
	}
}

func TestNarrowColumns_ConfigUsedWhenQueryEmpty(t *testing.T) {
	all := []export.Column{{Key: "id"}, {Key: "title"}, {Key: "status"}}
	out, err := narrowColumns(all, "", []string{"title"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "title" {
		t.Errorf("config should apply: %v", out)
	}
}

func TestNarrowColumns_DefaultsWhenNeitherSet(t *testing.T) {
	all := []export.Column{{Key: "id"}, {Key: "title"}}
	out, err := narrowColumns(all, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("default should return all: %v", out)
	}
}

func TestNarrowColumns_HeadersAppliedAlways(t *testing.T) {
	all := []export.Column{{Key: "title", Header: "title"}}
	out, _ := narrowColumns(all, "", nil, map[string]string{"title": "Headline"})
	if out[0].Header != "Headline" {
		t.Errorf("header not applied: %v", out)
	}
}

func TestNarrowColumns_UnknownColumnErrors(t *testing.T) {
	all := []export.Column{{Key: "id"}}
	if _, err := narrowColumns(all, "bogus", nil, nil); err == nil {
		t.Error("unknown column should error")
	}
}

func TestSelectColumns_BasicTextSpec(t *testing.T) {
	got := selectColumns(textSpec())
	joined := strings.Join(got, " ")
	for _, want := range []string{"id::text", "created", "updated", "title", "status"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

func TestSelectColumns_SoftDeleteAndHierarchy(t *testing.T) {
	spec := builder.NewCollection("c").
		Field("body", builder.NewText().Required()).
		SoftDelete().
		AdjacencyList().
		Ordered().
		Spec()
	got := selectColumns(spec)
	joined := strings.Join(got, " ")
	for _, want := range []string{"deleted", "parent::text", "sort_index"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

func TestWhereClauseSQL(t *testing.T) {
	if got := whereClauseSQL(""); got != "" {
		t.Errorf("empty: %q", got)
	}
	if got := whereClauseSQL("a = $1"); got != " WHERE a = $1" {
		t.Errorf("non-empty: %q", got)
	}
}

func TestOrderBySQL_DefaultWhenNoKeys(t *testing.T) {
	if got := orderBySQL(nil); got != "created DESC, id DESC" {
		t.Errorf("default: %q", got)
	}
}

// --- tiny helpers ---

func keysOf(cols []export.Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Key
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
