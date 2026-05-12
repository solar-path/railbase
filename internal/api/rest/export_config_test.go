package rest

import (
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
)

// v1.6.3 schema-declarative export config resolution.

func textSpec() builder.CollectionSpec {
	return builder.NewCollection("posts").
		Field("title", builder.NewText().Required()).
		Field("status", builder.NewText()).
		Spec()
}

func TestResolveExportColumns_NoConfig_NoQuery_ReturnsAllReadable(t *testing.T) {
	cols, err := resolveExportColumns(textSpec(), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// id, created, updated + title, status → 5 default columns.
	if len(cols) != 5 {
		t.Errorf("len=%d want 5: %v", len(cols), cols)
	}
}

func TestResolveExportColumns_ConfigColumnsApplyWhenQueryEmpty(t *testing.T) {
	cols, err := resolveExportColumns(textSpec(), "", []string{"title", "status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 {
		t.Fatalf("len=%d want 2: %v", len(cols), cols)
	}
	if cols[0].Key != "title" || cols[1].Key != "status" {
		t.Errorf("got %v", cols)
	}
}

func TestResolveExportColumns_QueryWinsOverConfig(t *testing.T) {
	cols, err := resolveExportColumns(textSpec(), "id", []string{"title", "status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0].Key != "id" {
		t.Errorf("expected query to win, got %v", cols)
	}
}

func TestResolveExportColumns_HeadersApply(t *testing.T) {
	cols, err := resolveExportColumns(textSpec(), "", []string{"title"},
		map[string]string{"title": "Headline"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0].Header != "Headline" {
		t.Errorf("header not applied: %v", cols)
	}
}

func TestResolveExportColumns_HeadersApplyEvenWithoutConfigColumns(t *testing.T) {
	// Default (all readable) + headers map → headers should still apply.
	cols, err := resolveExportColumns(textSpec(), "", nil,
		map[string]string{"title": "Headline"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range cols {
		if c.Key == "title" {
			if c.Header != "Headline" {
				t.Errorf("title header = %q", c.Header)
			}
			found = true
		}
	}
	if !found {
		t.Error("title column missing from default set")
	}
}

func TestResolveExportColumns_UnknownConfigColumnRejected(t *testing.T) {
	// Schema author typo'd a column name in .Export(...). Catch at
	// first request rather than render an empty column silently.
	_, err := resolveExportColumns(textSpec(), "", []string{"bogus"}, nil)
	if err == nil {
		t.Error("unknown config column: expected error")
	}
}

func TestResolveExportColumns_UnknownQueryColumnRejected(t *testing.T) {
	_, err := resolveExportColumns(textSpec(), "bogus", nil, nil)
	if err == nil {
		t.Error("unknown query column: expected error")
	}
}

func TestResolveExportColumns_EmptyQueryStringRejected(t *testing.T) {
	// Trailing comma → all parts trimmed to empty → 400.
	_, err := resolveExportColumns(textSpec(), " , , ", nil, nil)
	if err == nil {
		t.Error("all-whitespace columns: expected error")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "c") != "c" {
		t.Error("third value")
	}
	if firstNonEmpty("a", "b", "c") != "a" {
		t.Error("first non-empty wins")
	}
	if firstNonEmpty("", "b", "c") != "b" {
		t.Error("second")
	}
	if firstNonEmpty("", "", "") != "" {
		t.Error("all empty")
	}
}
