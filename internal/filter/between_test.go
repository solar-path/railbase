package filter_test

// v1.7.21 — BETWEEN parser + SQL emitter coverage (docs/17 #17 — closes
// the "filter BETWEEN feature gap" v1 SHIP item from the test-debt list).
//
// IN coverage was already healthy (TestCompile_In + "empty IN" error
// case); this file adds the missing BETWEEN happy-path / error-path
// surface plus extra IN tests that cover multi-type lists and the
// interaction with surrounding `&&` / `||` precedence.
//
// All assertions look at the rendered SQL string + the args slice —
// that's the contract the rest of the system relies on (the SQL splices
// into a larger SELECT, the args feed pgx's parameter binding).

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/filter"
)

// ---------- BETWEEN happy paths ----------

func TestCompile_Between_IntLiterals(t *testing.T) {
	sql, args := compileToSQL(t, "hits BETWEEN 10 AND 20", filter.Context{})
	if sql != "(hits BETWEEN $1 AND $2)" {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 2 || args[0] != int64(10) || args[1] != int64(20) {
		t.Errorf("args = %v", args)
	}
}

func TestCompile_Between_FloatLiterals(t *testing.T) {
	sql, args := compileToSQL(t, "hits BETWEEN 1.5 AND 9.75", filter.Context{})
	if sql != "(hits BETWEEN $1 AND $2)" {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 2 || args[0] != 1.5 || args[1] != 9.75 {
		t.Errorf("args = %v", args)
	}
}

func TestCompile_Between_StringLiterals(t *testing.T) {
	// BETWEEN on text columns is valid SQL — lexicographic ordering.
	sql, args := compileToSQL(t, "title BETWEEN 'a' AND 'z'", filter.Context{})
	if sql != "(title BETWEEN $1 AND $2)" {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 2 || args[0] != "a" || args[1] != "z" {
		t.Errorf("args = %v", args)
	}
}

func TestCompile_Between_CaseInsensitiveKeyword(t *testing.T) {
	// `BETWEEN` and `AND` are recognised case-insensitively, matching
	// PB's filter parser.
	for _, src := range []string{
		"hits between 10 and 20",
		"hits BETWEEN 10 and 20",
		"hits BeTwEeN 10 aNd 20",
	} {
		sql, _ := compileToSQL(t, src, filter.Context{})
		if !strings.Contains(sql, "BETWEEN $1 AND $2") {
			t.Errorf("source %q produced %q", src, sql)
		}
	}
}

func TestCompile_Between_MagicVarBound(t *testing.T) {
	// `@me` resolves to a $N parameter binding. With AuthID set, the
	// param value is the AuthID string.
	ctx := filter.Context{AuthID: "abc-123", AuthCollection: "users"}
	sql, args := compileToSQL(t, "title BETWEEN @me AND 'zzzzz'", ctx)
	if sql != "(title BETWEEN $1 AND $2)" {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 2 || args[0] != "abc-123" || args[1] != "zzzzz" {
		t.Errorf("args = %v", args)
	}
}

func TestCompile_Between_NestedInAndOr(t *testing.T) {
	// A BETWEEN clause should compose with && and || at the natural
	// precedence (BETWEEN is a comparison, sits below && which sits
	// below ||).
	sql, _ := compileToSQL(t,
		"status = 'published' && hits BETWEEN 5 AND 100",
		filter.Context{})
	// Expected shape: ((status = $1) AND (hits BETWEEN $2 AND $3))
	if !strings.Contains(sql, "(status = $1) AND (hits BETWEEN $2 AND $3)") {
		t.Errorf("AND+BETWEEN composition wrong: %s", sql)
	}
}

func TestCompile_Between_Negated_ViaOuterNotEqIsAbsent(t *testing.T) {
	// We don't ship NOT BETWEEN in v1.7.21. The operator can express it
	// via De Morgan: `!(x BETWEEN a AND b)` would need unary `!` which
	// is deferred. Today they OR two comparisons: `x < a || x > b`.
	// This test pins the absence so anyone adding NOT BETWEEN updates
	// the negation tests at the same time.
	_, err := filter.Parse("hits NOT BETWEEN 10 AND 20")
	if err == nil {
		t.Error("NOT BETWEEN should not parse in v1.7.21 (use De Morgan instead)")
	}
}

// ---------- BETWEEN error paths ----------

func TestParse_Between_MissingAndKeyword(t *testing.T) {
	_, err := filter.Parse("hits BETWEEN 10 20")
	if err == nil {
		t.Fatal("expected error: missing AND keyword")
	}
	if !strings.Contains(err.Error(), "AND") {
		t.Errorf("error should mention AND: %v", err)
	}
}

func TestParse_Between_MissingLowerBound(t *testing.T) {
	_, err := filter.Parse("hits BETWEEN AND 20")
	if err == nil {
		t.Fatal("expected error: missing lower bound")
	}
	// The lower bound is parsed via parsePrimary; AND is a bare ident,
	// so parsePrimary actually accepts it as an Ident — which then fails
	// the columnAllowed check at compile time. Either way: parse OR
	// compile error is acceptable; the user sees a 400.
}

func TestParse_Between_MissingUpperBound(t *testing.T) {
	_, err := filter.Parse("hits BETWEEN 10 AND")
	if err == nil {
		t.Fatal("expected error: missing upper bound")
	}
}

func TestParse_Between_DoubleBetween(t *testing.T) {
	// `a BETWEEN b AND c BETWEEN d AND e` should fail — BETWEEN is a
	// comparison, comparisons don't chain.
	_, err := filter.Parse("hits BETWEEN 1 AND 5 BETWEEN 6 AND 10")
	if err == nil {
		t.Error("expected error on chained BETWEEN")
	}
}

func TestParse_Between_WrongAndForm(t *testing.T) {
	// `&&` between bounds should fail — that's the LOGICAL conjunction
	// operator and would cause ambiguous parsing.
	_, err := filter.Parse("hits BETWEEN 10 && 20")
	if err == nil {
		t.Error("expected error: `&&` is not allowed inside BETWEEN bounds")
	}
}

// ---------- IN coverage extension ----------

func TestCompile_In_MixedNumericTypes(t *testing.T) {
	// IN with a mix of int / float literals — pgx coerces at bind time.
	sql, args := compileToSQL(t, "hits IN (1, 2, 3.5)", filter.Context{})
	if !strings.Contains(sql, "IN ($1, $2, $3)") {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 3 {
		t.Fatalf("args len: %d", len(args))
	}
	if args[0] != int64(1) || args[1] != int64(2) || args[2] != 3.5 {
		t.Errorf("args = %v", args)
	}
}

func TestCompile_In_SingleItem(t *testing.T) {
	// A one-element IN should still emit `IN ($N)` (Postgres accepts).
	sql, _ := compileToSQL(t, "status IN ('draft')", filter.Context{})
	if !strings.Contains(sql, "IN ($1)") {
		t.Errorf("sql = %q", sql)
	}
}

func TestCompile_In_LargeList(t *testing.T) {
	// 50-item IN — exercises the param-counter logic without hitting
	// the pgx 65k-param limit.
	parts := make([]string, 50)
	for i := range parts {
		parts[i] = "'v" // each unique string literal
	}
	src := "status IN ("
	for i, p := range parts {
		if i > 0 {
			src += ", "
		}
		src += p + string(rune('0'+i%10)) + "'"
	}
	src += ")"
	sql, args := compileToSQL(t, src, filter.Context{})
	if len(args) != 50 {
		t.Fatalf("args len = %d, want 50", len(args))
	}
	if !strings.Contains(sql, "IN ($1, $2, $3,") {
		t.Errorf("placeholders not sequential: %s", sql[:80])
	}
	if !strings.Contains(sql, "$50)") {
		t.Errorf("last placeholder missing: ...%s", sql[len(sql)-50:])
	}
}

func TestCompile_In_CaseInsensitiveKeyword(t *testing.T) {
	for _, src := range []string{
		"status in ('draft')",
		"status In ('draft')",
		"status IN ('draft')",
	} {
		sql, _ := compileToSQL(t, src, filter.Context{})
		if !strings.Contains(sql, "IN ($1)") {
			t.Errorf("source %q produced %q", src, sql)
		}
	}
}

func TestCompile_In_ComposesWithAnd(t *testing.T) {
	sql, _ := compileToSQL(t,
		"status IN ('draft', 'published') && public = true",
		filter.Context{})
	if !strings.Contains(sql, "(status IN ($1, $2))") {
		t.Errorf("missing IN clause: %s", sql)
	}
	if !strings.Contains(sql, "AND (public = $3)") {
		t.Errorf("AND chain wrong: %s", sql)
	}
}

// ---------- BETWEEN compile error paths (after parse succeeds) ----------

func TestBetween_UnknownColumnInBound_RejectedAtCompile(t *testing.T) {
	// `hits BETWEEN mystery AND 20` — `mystery` is an Ident that's not
	// on the spec; parser accepts (Ident is a valid primary), compiler
	// rejects via columnAllowed.
	ast, err := filter.Parse("hits BETWEEN mystery AND 20")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, _, _, err = filter.Compile(ast, samplePostsSpec(), filter.Context{}, 1)
	if err == nil {
		t.Fatal("expected compile error for unknown ident bound")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error should mention unknown field: %v", err)
	}
}

func TestBetween_ForbiddenColumnInTarget_RejectedAtCompile(t *testing.T) {
	// `meta BETWEEN ...` — meta is JSON, not allowed in filters.
	ast, err := filter.Parse("meta BETWEEN 1 AND 5")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, _, _, err = filter.Compile(ast, samplePostsSpec(), filter.Context{}, 1)
	if err == nil {
		t.Error("BETWEEN on JSON column should be rejected at compile time")
	}
}
