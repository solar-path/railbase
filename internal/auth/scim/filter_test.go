package scim

import (
	"strings"
	"testing"
)

func TestParse_Empty(t *testing.T) {
	n, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("expected nil, got %#v", n)
	}
	n, err = Parse("   \t  ")
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("expected nil for whitespace, got %#v", n)
	}
}

func TestParse_SimpleEq(t *testing.T) {
	n, err := Parse(`userName eq "alice"`)
	if err != nil {
		t.Fatal(err)
	}
	cmp, ok := n.(CompareNode)
	if !ok {
		t.Fatalf("expected CompareNode, got %T", n)
	}
	if cmp.Op != OpEq {
		t.Errorf("op = %q want eq", cmp.Op)
	}
	if cmp.Value != "alice" {
		t.Errorf("value = %v want alice", cmp.Value)
	}
	if len(cmp.Path) != 1 || cmp.Path[0] != "userName" {
		t.Errorf("path = %v", cmp.Path)
	}
}

func TestParse_DottedPath(t *testing.T) {
	n, err := Parse(`meta.created gt "2024-01-01T00:00:00Z"`)
	if err != nil {
		t.Fatal(err)
	}
	cmp := n.(CompareNode)
	if len(cmp.Path) != 2 || cmp.Path[0] != "meta" || cmp.Path[1] != "created" {
		t.Errorf("path = %v", cmp.Path)
	}
}

func TestParse_Boolean(t *testing.T) {
	n, err := Parse(`active eq true`)
	if err != nil {
		t.Fatal(err)
	}
	cmp := n.(CompareNode)
	if cmp.Value != true {
		t.Errorf("value = %v want true", cmp.Value)
	}
}

func TestParse_Present(t *testing.T) {
	n, err := Parse(`externalId pr`)
	if err != nil {
		t.Fatal(err)
	}
	cmp := n.(CompareNode)
	if cmp.Op != OpPresent {
		t.Errorf("op = %q want pr", cmp.Op)
	}
	if cmp.Value != nil {
		t.Errorf("pr should have no value, got %v", cmp.Value)
	}
}

func TestParse_AndOr(t *testing.T) {
	n, err := Parse(`userName eq "alice" and active eq true`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(AndNode); !ok {
		t.Fatalf("expected AndNode, got %T", n)
	}
	n2, err := Parse(`userName eq "alice" or userName eq "bob"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n2.(OrNode); !ok {
		t.Fatalf("expected OrNode, got %T", n2)
	}
}

func TestParse_AndBeatsOr(t *testing.T) {
	// `a or b and c` should parse as `a or (b and c)` per SCIM precedence.
	n, err := Parse(`userName eq "alice" or active eq true and userName eq "bob"`)
	if err != nil {
		t.Fatal(err)
	}
	or, ok := n.(OrNode)
	if !ok {
		t.Fatalf("top should be OrNode, got %T", n)
	}
	if _, ok := or.Right.(AndNode); !ok {
		t.Errorf("right of OR should be AndNode, got %T", or.Right)
	}
}

func TestParse_Parens(t *testing.T) {
	n, err := Parse(`(userName eq "alice" or userName eq "bob") and active eq true`)
	if err != nil {
		t.Fatal(err)
	}
	and, ok := n.(AndNode)
	if !ok {
		t.Fatalf("top should be AndNode, got %T", n)
	}
	if _, ok := and.Left.(OrNode); !ok {
		t.Errorf("left of AND should be OrNode, got %T", and.Left)
	}
}

func TestParse_Not(t *testing.T) {
	n, err := Parse(`not (active eq false)`)
	if err != nil {
		t.Fatal(err)
	}
	not, ok := n.(NotNode)
	if !ok {
		t.Fatalf("top should be NotNode, got %T", n)
	}
	if _, ok := not.Inner.(CompareNode); !ok {
		t.Errorf("inner should be CompareNode, got %T", not.Inner)
	}
}

func TestParse_CaseInsensitiveKeywords(t *testing.T) {
	// SCIM operators are case-insensitive.
	cases := []string{
		`userName EQ "alice"`,
		`userName Eq "alice"`,
		`userName eq "alice" AND active eq TRUE`,
		`active eq TRUE`,
	}
	for _, c := range cases {
		if _, err := Parse(c); err != nil {
			t.Errorf("case-insensitive parse failed for %q: %v", c, err)
		}
	}
}

func TestParse_AllCompareOps(t *testing.T) {
	ops := []string{"eq", "ne", "co", "sw", "ew", "gt", "ge", "lt", "le"}
	for _, op := range ops {
		f := `userName ` + op + ` "x"`
		n, err := Parse(f)
		if err != nil {
			t.Errorf("op %s failed: %v", op, err)
			continue
		}
		cmp, ok := n.(CompareNode)
		if !ok {
			t.Errorf("op %s: expected CompareNode", op)
			continue
		}
		if string(cmp.Op) != op {
			t.Errorf("op %s: got %s", op, cmp.Op)
		}
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct {
		input    string
		wantSubs string
	}{
		{`userName eq`, "expected value"},
		{`userName`, "expected operator"},
		{`eq "alice"`, "expected attribute"},
		{`userName eq "alice" and`, "unexpected end"},
		{`(userName eq "alice"`, "missing ')'"},
		{`not active eq true`, "must be followed by '('"},
	}
	for _, c := range cases {
		_, err := Parse(c.input)
		if err == nil {
			t.Errorf("%q: expected error", c.input)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSubs) {
			t.Errorf("%q: error %q did not contain %q", c.input, err, c.wantSubs)
		}
	}
}

func TestToSQL_Simple(t *testing.T) {
	n, _ := Parse(`userName eq "alice"`)
	frag, args, err := ToSQL(n, ColumnMap{"username": "lower(email)"})
	if err != nil {
		t.Fatal(err)
	}
	if frag != `lower(email) = $1` {
		t.Errorf("frag = %q", frag)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args = %v", args)
	}
}

func TestToSQL_AndOrNot(t *testing.T) {
	cols := ColumnMap{"username": "lower(email)", "active": "verified"}
	n, _ := Parse(`(userName eq "alice" or userName eq "bob") and active eq true`)
	frag, args, err := ToSQL(n, cols)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag, "OR") || !strings.Contains(frag, "AND") {
		t.Errorf("frag = %q (missing AND/OR)", frag)
	}
	if len(args) != 3 {
		t.Errorf("args = %v want 3", args)
	}
}

func TestToSQL_Pr(t *testing.T) {
	n, _ := Parse(`externalId pr`)
	frag, args, err := ToSQL(n, ColumnMap{"externalid": "external_id"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag, "IS NOT NULL") {
		t.Errorf("frag = %q", frag)
	}
	if len(args) != 0 {
		t.Errorf("pr should produce no args, got %v", args)
	}
}

func TestToSQL_Contains(t *testing.T) {
	n, _ := Parse(`displayName co "engineering"`)
	frag, args, err := ToSQL(n, ColumnMap{"displayname": "display_name"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag, "ILIKE") {
		t.Errorf("frag = %q", frag)
	}
	if args[0] != "%engineering%" {
		t.Errorf("arg = %q", args[0])
	}
}

func TestToSQL_UnknownAttribute(t *testing.T) {
	n, _ := Parse(`unknownAttr eq "x"`)
	_, _, err := ToSQL(n, ColumnMap{"username": "email"})
	if err == nil {
		t.Fatal("expected error for unknown attribute")
	}
	if !strings.Contains(err.Error(), "not filterable") {
		t.Errorf("err = %v", err)
	}
}

func TestToSQL_EqNull(t *testing.T) {
	n, _ := Parse(`externalId eq null`)
	frag, args, err := ToSQL(n, ColumnMap{"externalid": "external_id"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag, "IS NULL") {
		t.Errorf("frag = %q", frag)
	}
	if len(args) != 0 {
		t.Errorf("args = %v want empty", args)
	}
}

func TestToSQL_NilNode(t *testing.T) {
	frag, args, err := ToSQL(nil, ColumnMap{})
	if err != nil {
		t.Fatal(err)
	}
	if frag != "" || args != nil {
		t.Errorf("nil node should yield empty frag, got %q %v", frag, args)
	}
}
