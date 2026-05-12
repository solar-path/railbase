package filter_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/schema/builder"
)

func samplePostsSpec() builder.CollectionSpec {
	c := builder.NewCollection("posts").
		Field("title", builder.NewText()).
		Field("body", builder.NewText()).
		Field("status", builder.NewSelect("draft", "published")).
		Field("hits", builder.NewNumber().Int()).
		Field("public", builder.NewBool()).
		Field("meta", builder.NewJSON()).        // not allowed in filter
		Field("password", builder.NewPassword()) // not allowed in filter
	return c.Spec()
}

func compileToSQL(t *testing.T, src string, ctx filter.Context) (string, []any) {
	t.Helper()
	ast, err := filter.Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	sql, args, _, err := filter.Compile(ast, samplePostsSpec(), ctx, 1)
	if err != nil {
		t.Fatalf("Compile(%q): %v", src, err)
	}
	return sql, args
}

func TestParse_Empty(t *testing.T) {
	n, err := filter.Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Errorf("empty source should parse to nil, got %T", n)
	}
}

func TestCompile_StringEquality(t *testing.T) {
	sql, args := compileToSQL(t, "status = 'published'", filter.Context{})
	if sql != "(status = $1)" {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 1 || args[0] != "published" {
		t.Errorf("args = %v", args)
	}
}

func TestCompile_AndOr_Precedence(t *testing.T) {
	// `a || b && c` should parse as `a || (b && c)`.
	sql, _ := compileToSQL(t, "status = 'a' || status = 'b' && hits > 5", filter.Context{})
	// expected: (status=$1 OR (status=$2 AND hits>$3))
	if !strings.Contains(sql, "OR") {
		t.Fatalf("missing OR: %s", sql)
	}
	if !strings.Contains(sql, "AND") {
		t.Fatalf("missing AND: %s", sql)
	}
	// Verify AND nests INSIDE the OR — looking for `OR ((status`
	if !strings.Contains(sql, "OR ((status = $2)") {
		t.Errorf("precedence wrong: %s", sql)
	}
}

func TestCompile_Parens(t *testing.T) {
	sql, _ := compileToSQL(t, "(status = 'a' || status = 'b') && hits > 5", filter.Context{})
	if !strings.Contains(sql, "((status = $1) OR (status = $2))") {
		t.Errorf("parens not respected: %s", sql)
	}
}

func TestCompile_LikeAndNotLike(t *testing.T) {
	sql, args := compileToSQL(t, "title ~ 'world'", filter.Context{})
	if !strings.Contains(sql, "ILIKE") || !strings.Contains(sql, "'%' || $1 || '%'") {
		t.Errorf("LIKE pattern wrong: %s", sql)
	}
	if args[0] != "world" {
		t.Errorf("args: %v", args)
	}
	sql, _ = compileToSQL(t, "title !~ 'world'", filter.Context{})
	if !strings.Contains(sql, "NOT ILIKE") {
		t.Errorf("NOT ILIKE missing: %s", sql)
	}
}

func TestCompile_NullRewrite(t *testing.T) {
	sql, _ := compileToSQL(t, "title = null", filter.Context{})
	if !strings.Contains(sql, "IS NULL") {
		t.Errorf("`= null` should rewrite to IS NULL: %s", sql)
	}
	sql, _ = compileToSQL(t, "title != null", filter.Context{})
	if !strings.Contains(sql, "IS NOT NULL") {
		t.Errorf("`!= null` should rewrite to IS NOT NULL: %s", sql)
	}
}

func TestCompile_In(t *testing.T) {
	sql, args := compileToSQL(t, "status IN ('draft', 'published')", filter.Context{})
	if !strings.Contains(sql, "IN ($1, $2)") {
		t.Errorf("IN clause wrong: %s", sql)
	}
	if len(args) != 2 || args[0] != "draft" || args[1] != "published" {
		t.Errorf("args: %v", args)
	}
}

func TestCompile_MagicVars(t *testing.T) {
	ctx := filter.Context{AuthID: "abc-123", AuthCollection: "users"}
	sql, args := compileToSQL(t, "@me != ''", ctx)
	if !strings.Contains(sql, "$1 != $2") {
		t.Errorf("expected two params: %s", sql)
	}
	if args[0] != "abc-123" {
		t.Errorf("@me arg: %v", args)
	}
	if args[1] != "" {
		t.Errorf("string arg: %v", args[1])
	}
}

func TestCompile_RejectsUnknownColumn(t *testing.T) {
	_, _, _, err := filter.Compile(mustParse(t,"mystery = 'x'"), samplePostsSpec(), filter.Context{}, 1)
	if err == nil {
		t.Fatal("expected error for unknown column")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error: %v", err)
	}
}

func TestCompile_RejectsForbiddenColumns(t *testing.T) {
	for _, col := range []string{"meta", "password"} {
		_, _, _, err := filter.Compile(mustParse(t,col+" = 'x'"), samplePostsSpec(), filter.Context{}, 1)
		if err == nil {
			t.Errorf("filter on %q should be rejected", col)
		}
	}
}

func TestParse_Errors(t *testing.T) {
	cases := map[string]string{
		"unterminated string":  "title = 'oops",
		"stray !":              "! hits = 1",
		"unknown magic":        "@request.body.x = 1",
		"empty IN":             "status IN ()",
		"missing close paren":  "(status = 'a'",
		"comparison no rhs":    "status =",
		"trailing garbage":     "status = 'a' garbage",
	}
	for name, src := range cases {
		_, err := filter.Parse(src)
		if err == nil {
			t.Errorf("%s: expected error, got nil for %q", name, src)
		}
	}
}

func TestParse_PositionedError(t *testing.T) {
	_, err := filter.Parse("title ?? 'x'")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *filter.PositionedError
	if !errors.As(err, &pe) {
		t.Errorf("expected PositionedError, got %T", err)
	}
}

func TestParseSort_HappyAndDefaults(t *testing.T) {
	spec := samplePostsSpec()
	keys, err := filter.ParseSort("-status, +created", spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("len = %d", len(keys))
	}
	if keys[0].Field != "status" || !keys[0].Desc {
		t.Errorf("key0: %+v", keys[0])
	}
	if keys[1].Field != "created" || keys[1].Desc {
		t.Errorf("key1: %+v", keys[1])
	}
	if got := filter.JoinSQL(keys); got != "status DESC, created ASC" {
		t.Errorf("JoinSQL: %s", got)
	}
}

func TestParseSort_RejectsInvalidField(t *testing.T) {
	if _, err := filter.ParseSort("mystery", samplePostsSpec()); err == nil {
		t.Errorf("expected error for unknown field")
	}
	if _, err := filter.ParseSort("meta", samplePostsSpec()); err == nil {
		t.Errorf("JSON field should not be sortable")
	}
}

func TestParseSort_Empty(t *testing.T) {
	keys, err := filter.ParseSort("", samplePostsSpec())
	if err != nil || keys != nil {
		t.Errorf("empty sort: got %v err=%v", keys, err)
	}
}

func mustParse(t *testing.T, src string) filter.Node {
	t.Helper()
	n, err := filter.Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return n
}
