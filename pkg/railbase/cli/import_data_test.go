package cli

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
)

func TestPeekCSVHeader_HappyPath(t *testing.T) {
	body := []byte("id,name,qty\n1,foo,10\n2,bar,20\n")
	cols, err := peekCSVHeader(body, ',')
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	want := []string{"id", "name", "qty"}
	if len(cols) != len(want) {
		t.Fatalf("len(cols) = %d, want %d", len(cols), len(want))
	}
	for i, c := range cols {
		if c != want[i] {
			t.Errorf("cols[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestPeekCSVHeader_AlternateDelimiter(t *testing.T) {
	body := []byte("id;name;qty\n1;foo;10\n")
	cols, err := peekCSVHeader(body, ';')
	if err != nil {
		t.Fatalf("peek with ';' delimiter: %v", err)
	}
	if len(cols) != 3 || cols[2] != "qty" {
		t.Errorf("delimiter-aware peek failed: %v", cols)
	}
}

func TestPeekCSVHeader_TrimsWhitespace(t *testing.T) {
	body := []byte("  id  , name ,qty\n")
	cols, _ := peekCSVHeader(body, ',')
	if cols[0] != "id" || cols[1] != "name" {
		t.Errorf("expected trimmed names, got %v", cols)
	}
}

func TestPeekCSVHeader_EmptyFileRejected(t *testing.T) {
	_, err := peekCSVHeader([]byte(""), ',')
	if err == nil {
		t.Fatal("expected error on empty file")
	}
	if !strings.Contains(err.Error(), "empty CSV") {
		t.Errorf("error should mention empty CSV: %v", err)
	}
}

func TestValidateColumns_AllowsSystemColumns(t *testing.T) {
	spec := builder.CollectionSpec{
		Name:   "posts",
		Fields: []builder.FieldSpec{{Name: "title", Type: "text"}},
	}
	for _, ok := range []string{"id", "created", "updated", "title"} {
		if err := validateColumnsAgainstSpec(spec, []string{ok}); err != nil {
			t.Errorf("system/spec column %q should be allowed: %v", ok, err)
		}
	}
}

func TestValidateColumns_AllowsTenantAndSoftdeleteColumns(t *testing.T) {
	spec := builder.CollectionSpec{
		Name:   "items",
		Fields: []builder.FieldSpec{{Name: "name", Type: "text"}},
	}
	for _, ok := range []string{"tenant_id", "deleted", "parent", "sort_index"} {
		if err := validateColumnsAgainstSpec(spec, []string{ok}); err != nil {
			t.Errorf("system column %q should be allowed: %v", ok, err)
		}
	}
}

func TestValidateColumns_AuthCollectionExtras(t *testing.T) {
	spec := builder.CollectionSpec{
		Name:   "users",
		Auth:   true,
		Fields: []builder.FieldSpec{{Name: "username", Type: "text"}},
	}
	for _, ok := range []string{"email", "verified", "token_key", "password_hash"} {
		if err := validateColumnsAgainstSpec(spec, []string{ok}); err != nil {
			t.Errorf("auth-extra column %q should be allowed on auth collection: %v", ok, err)
		}
	}
	// On a non-auth collection these should NOT be allowed.
	plainSpec := builder.CollectionSpec{
		Name:   "items",
		Auth:   false,
		Fields: []builder.FieldSpec{{Name: "name", Type: "text"}},
	}
	if err := validateColumnsAgainstSpec(plainSpec, []string{"email"}); err == nil {
		t.Errorf("non-auth collection should reject `email` column")
	}
}

func TestValidateColumns_UnknownColumnRejected(t *testing.T) {
	spec := builder.CollectionSpec{
		Name:   "posts",
		Fields: []builder.FieldSpec{{Name: "title", Type: "text"}},
	}
	err := validateColumnsAgainstSpec(spec, []string{"title", "nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown column")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the bad column: %v", err)
	}
	// The error should also list valid columns to help the operator.
	if !strings.Contains(err.Error(), "valid:") {
		t.Errorf("error should hint at valid columns: %v", err)
	}
}

func TestValidateColumns_DuplicateColumnRejected(t *testing.T) {
	spec := builder.CollectionSpec{
		Name:   "posts",
		Fields: []builder.FieldSpec{{Name: "title", Type: "text"}},
	}
	err := validateColumnsAgainstSpec(spec, []string{"title", "title"})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-column error, got %v", err)
	}
}

func TestValidateColumns_EmptyNameRejected(t *testing.T) {
	spec := builder.CollectionSpec{
		Name:   "posts",
		Fields: []builder.FieldSpec{{Name: "title", Type: "text"}},
	}
	err := validateColumnsAgainstSpec(spec, []string{"title", ""})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty-column error, got %v", err)
	}
}

func TestBuildCopySQL_ShapeAndQuoting(t *testing.T) {
	opts := importDataOptions{delimiter: ',', nullStr: "", quote: `"`, header: true}
	got := buildCopySQL("posts", []string{"id", "title"}, opts)
	// Pin the major shape.
	for _, want := range []string{
		`COPY "posts"`,
		`("id", "title")`,
		`FROM STDIN`,
		`FORMAT csv`,
		`HEADER true`,
		`DELIMITER ','`,
		`NULL ''`,
		`QUOTE '"'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("COPY SQL missing %q: %s", want, got)
		}
	}
}

func TestBuildCopySQL_EscapesEmbeddedQuotes(t *testing.T) {
	// Operator passes a delimiter that's a literal single-quote
	// (pathological but allowed). The literal must be escaped via
	// SQL doubling: '\'' → '''.
	opts := importDataOptions{delimiter: '\'', nullStr: "", quote: `"`, header: true}
	got := buildCopySQL("t", []string{"x"}, opts)
	if !strings.Contains(got, `DELIMITER ''''`) {
		t.Errorf("single-quote delimiter not doubled: %s", got)
	}
}

func TestBuildCopySQL_CustomDelimiterAndNull(t *testing.T) {
	opts := importDataOptions{delimiter: ';', nullStr: "\\N", quote: `"`, header: true}
	got := buildCopySQL("orders", []string{"id"}, opts)
	if !strings.Contains(got, `DELIMITER ';'`) {
		t.Errorf("custom delimiter lost: %s", got)
	}
	if !strings.Contains(got, `NULL '\N'`) {
		t.Errorf("custom null sentinel lost: %s", got)
	}
}

func TestPgQuoteLiteral(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"a", "'a'"},
		{"o'reilly", "'o''reilly'"},
		{"''", "''''''"},
	}
	for _, c := range cases {
		if got := pgQuoteLiteral(c.in); got != c.want {
			t.Errorf("pgQuoteLiteral(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
