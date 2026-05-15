// Regression for Sentinel FEEDBACK.md G4 — anonymous principal +
// rule comparing @request.auth.id against a UUID-typed column must
// NOT emit `<col> = ''`, because Postgres rejects '' as a uuid:
//
//   ERROR: invalid input syntax for type uuid: "" (SQLSTATE 22P02)
//
// The REST layer surfaced that as a 500 "count failed". Operators
// expect "anonymous can't match an owner UUID" → rule denies → empty
// list / 200, not 500.
//
// Fix (sql.go:emitCompare): when AuthID == "" and the magic auth-id
// var is being compared to a UUID-typed column, collapse the whole
// compare to a constant SQL `false`. The deny-by-default posture for
// rules makes this safe for both `=` and `!=` operators.
//
// Tests below cover:
//   - Relation field (Sentinel's `owner` column on projects)
//   - System `id` column
//   - System `tenant_id` (only when Tenant() enabled)
//   - System `parent` (only when AdjacencyList() enabled)
//   - The `!= ''` idiom for "authenticated only" still works
//   - Authenticated request still emits a real $N binding
//   - Text-typed column gets no short-circuit (empty string is valid)
package filter_test

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/schema/builder"
)

func projectsSpec() builder.CollectionSpec {
	// Matches Sentinel's `projects` shape: owner is a Relation to users.
	return builder.NewCollection("projects").
		Field("title", builder.NewText()).
		Field("owner", builder.NewRelation("users").Required()).
		Spec()
}

func compileWithSpec(t *testing.T, src string, spec builder.CollectionSpec, ctx filter.Context) (string, []any) {
	t.Helper()
	ast, err := filter.Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	sql, args, _, err := filter.Compile(ast, spec, ctx, 1)
	if err != nil {
		t.Fatalf("Compile(%q): %v", src, err)
	}
	return sql, args
}

// TestAnonRelationCompare_ShortCircuitsToFalse — the exact Sentinel
// pattern: `@request.auth.id = owner` on projects, no token attached.
// Pre-fix, this emitted `($1 = owner)` with $1 = "" and Postgres
// crashed at count time. Post-fix, it emits `(false)` so the rule
// safely denies anonymous principals without DB round-trip.
func TestAnonRelationCompare_ShortCircuitsToFalse(t *testing.T) {
	spec := projectsSpec()
	anon := filter.Context{} // AuthID == ""

	for _, rule := range []string{
		`@request.auth.id = owner`,
		`owner = @request.auth.id`, // operand order doesn't matter
		`@me = owner`,
		`owner = @me`,
		`@request.auth.id != owner`, // != also short-circuits (deny-by-default)
	} {
		sql, args := compileWithSpec(t, rule, spec, anon)
		if !strings.Contains(sql, "(false)") {
			t.Errorf("rule %q: expected (false) short-circuit for anonymous, got %q (args=%v)", rule, sql, args)
		}
		if len(args) != 0 {
			t.Errorf("rule %q: short-circuited compare should bind ZERO params, got %d: %v", rule, len(args), args)
		}
		// Critical: the buggy empty-string binding must not be present.
		if strings.Contains(sql, "$1") {
			t.Errorf("rule %q: SQL still contains a $N placeholder — short-circuit failed: %q", rule, sql)
		}
	}
}

// TestAnonIDCompare_ShortCircuitsToFalse — same short-circuit for the
// `id` system column (always UUID).
func TestAnonIDCompare_ShortCircuitsToFalse(t *testing.T) {
	sql, _ := compileWithSpec(t, `id = @request.auth.id`, projectsSpec(), filter.Context{})
	if !strings.Contains(sql, "(false)") {
		t.Errorf("expected (false), got %q", sql)
	}
}

// TestAnonTenantCompare_ShortCircuitsToFalse — `tenant_id` is UUID
// when Tenant() is enabled. On a non-tenant spec, tenant_id isn't a
// real column and won't even reach emitCompare (columnAllowed rejects
// it upstream), so we only test the positive case here.
func TestAnonTenantCompare_ShortCircuitsToFalse(t *testing.T) {
	spec := builder.NewCollection("docs").
		Tenant().
		Field("title", builder.NewText()).
		Spec()
	sql, _ := compileWithSpec(t, `tenant_id = @request.auth.id`, spec, filter.Context{})
	if !strings.Contains(sql, "(false)") {
		t.Errorf("expected (false), got %q", sql)
	}
}

// TestAnonParentCompare_ShortCircuitsToFalse — `parent` is UUID when
// AdjacencyList() is enabled.
func TestAnonParentCompare_ShortCircuitsToFalse(t *testing.T) {
	spec := builder.NewCollection("tasks").
		AdjacencyList().
		Field("title", builder.NewText()).
		Spec()
	sql, _ := compileWithSpec(t, `parent = @request.auth.id`, spec, filter.Context{})
	if !strings.Contains(sql, "(false)") {
		t.Errorf("expected (false), got %q", sql)
	}
}

// TestAnonAuthIdNotEmpty_StillWorks — the canonical
// `@request.auth.id != ''` rule (== "any authenticated user") must
// keep its existing semantics: anonymous → empty string → '' != ''
// → false → rule denies. We bind the empty string parameter exactly
// like before; the short-circuit doesn't fire because the other
// operand is a string literal, not a UUID column.
func TestAnonAuthIdNotEmpty_StillWorks(t *testing.T) {
	sql, args := compileWithSpec(t, `@request.auth.id != ''`, projectsSpec(), filter.Context{})
	if strings.Contains(sql, "(false)") {
		t.Errorf("text-typed comparison incorrectly short-circuited: %q", sql)
	}
	if len(args) != 2 {
		t.Errorf("expected 2 args (empty-string magic var + empty string literal), got %d: %v", len(args), args)
	}
	// Both args bind the empty string — Postgres correctly evaluates
	// '' != '' → false, ListRule denies anonymous.
	for i, a := range args {
		if s, ok := a.(string); !ok || s != "" {
			t.Errorf("arg[%d]: expected empty string, got %v (%T)", i, a, a)
		}
	}
}

// TestAuthenticatedCompare_EmitsParameter — when the principal IS
// authenticated, we DO want the real $N binding. This is the happy
// path: rule `@request.auth.id = owner` for user u → SQL emits
// `($1 = owner)` with $1 bound to u.
func TestAuthenticatedCompare_EmitsParameter(t *testing.T) {
	authed := filter.Context{
		AuthID:         "550e8400-e29b-41d4-a716-446655440000",
		AuthCollection: "users",
	}
	sql, args := compileWithSpec(t, `@request.auth.id = owner`, projectsSpec(), authed)
	if strings.Contains(sql, "(false)") {
		t.Errorf("authenticated comparison incorrectly short-circuited: %q", sql)
	}
	if !strings.Contains(sql, "$1") {
		t.Errorf("expected $1 placeholder, got %q", sql)
	}
	if len(args) != 1 || args[0] != authed.AuthID {
		t.Errorf("expected single arg = AuthID, got %v", args)
	}
}

// TestAnonTextField_NoShortCircuit — only UUID-typed columns trigger
// the short-circuit. A text-typed user field (which legitimately
// accepts the empty string) must keep its existing behaviour.
func TestAnonTextField_NoShortCircuit(t *testing.T) {
	// `title` is TypeText — '' is a valid title (degenerate but legal).
	sql, args := compileWithSpec(t, `title = @request.auth.id`, projectsSpec(), filter.Context{})
	if strings.Contains(sql, "(false)") {
		t.Errorf("text-typed comparison incorrectly short-circuited: %q", sql)
	}
	if len(args) != 1 || args[0] != "" {
		t.Errorf("expected single empty-string arg, got %v", args)
	}
}
