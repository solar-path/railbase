// Regression test for Sentinel FEEDBACK.md #4 — v0.4.1 wiring of the
// filter.Context.Schema resolver inside filterCtx().
//
// Background. Phase 2/A2 added dotted-path filters (`project.owner =
// @request.auth.id`): lexer, parser, AST node, and SQL emitter all
// support one FK hop. But the REST handler that compiles user-supplied
// filters and admin-supplied rules built `filter.Context` without
// populating Schema, so any dotted-path expression hit
//
//	"filter Context.Schema resolver not wired"
//
// at compile time. The fix is in rules.go: filterCtx() now sets
// Schema: schemaResolver, where schemaResolver adapts registry.Get
// to the filter package's signature.
//
// These tests prove the wire is live by exercising compileRule
// through filterCtx() against a two-collection registry (projects +
// tasks) — the exact shape Sentinel needs to write
// `project.owner = @request.auth.id` on the tasks list rule without
// having to denormalise `project_owner` onto every task row.
package rest

import (
	"strings"
	"testing"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/filter"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// registerProjectsAndTasks installs a `projects(owner)` + `tasks(project
// → projects)` pair in the global registry for the duration of the test.
// Returns the tasks spec for compileRule input.
func registerProjectsAndTasks(t *testing.T) schemabuilder.CollectionSpec {
	t.Helper()
	registry.Reset()
	t.Cleanup(registry.Reset)

	projects := schemabuilder.NewCollection("projects").
		Field("owner", schemabuilder.NewRelation("users").Required())
	tasks := schemabuilder.NewCollection("tasks").
		Field("title", schemabuilder.NewText().Required()).
		Field("project", schemabuilder.NewRelation("projects").Required())
	registry.Register(projects)
	registry.Register(tasks)
	return tasks.Spec()
}

// TestFilterDottedPath_SchemaResolverWired proves filterCtx() now
// supplies the Schema resolver, so a rule using a one-hop dotted path
// compiles to the documented scalar-subquery SQL shape.
func TestFilterDottedPath_SchemaResolverWired(t *testing.T) {
	tasksSpec := registerProjectsAndTasks(t)

	fctx := filterCtx(authmw.Principal{}) // anonymous is fine — we're testing wiring
	if fctx.Schema == nil {
		t.Fatal("filterCtx().Schema is nil — resolver not wired (FEEDBACK #4 regression)")
	}

	// The same rule shape Sentinel wants to use on tasks:
	//   list rule: tasks visible iff caller owns the parent project.
	rule := `project.owner = @request.auth.id`
	frag, _, err := compileRule(rule, tasksSpec, fctx, 1)
	if err != nil {
		t.Fatalf("compileRule(%q): %v", rule, err)
	}

	want := "(SELECT projects.owner FROM projects WHERE projects.id = tasks.project)"
	if !strings.Contains(frag.Where, want) {
		t.Errorf("compiled SQL missing scalar subquery for dotted path.\n got: %s\nwant substring: %s", frag.Where, want)
	}
	// And the magic var must have produced a parameter binding rather
	// than be inlined as a string literal.
	if !strings.Contains(frag.Where, "$1") {
		t.Errorf("compiled SQL missing $1 placeholder for @request.auth.id: %s", frag.Where)
	}
	if len(frag.Args) != 1 {
		t.Errorf("expected exactly 1 arg (auth.id), got %d: %+v", len(frag.Args), frag.Args)
	}
}

// TestFilterDottedPath_UnknownRelatedCollection proves the resolver
// returns (zero, false) on a miss so the compiler emits a clear error
// instead of panicking or silently emitting wrong SQL. This is the
// safety net for typos in the related-collection name.
func TestFilterDottedPath_UnknownRelatedCollection(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	// Register tasks pointing at a collection that doesn't exist.
	tasks := schemabuilder.NewCollection("tasks").
		Field("title", schemabuilder.NewText().Required()).
		Field("project", schemabuilder.NewRelation("ghost").Required())
	registry.Register(tasks)

	fctx := filterCtx(authmw.Principal{})
	_, _, err := compileRule(`project.owner = @request.auth.id`, tasks.Spec(), fctx, 1)
	if err == nil {
		t.Fatal("expected compile error for unknown related collection, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention the unknown collection name; got: %v", err)
	}
}

// TestFilterDottedPath_MissingResolverErrors proves the negative path:
// if some future refactor drops the Schema wire, dotted paths fail
// with the exact "resolver not wired" message that surfaced in
// Sentinel. We construct a filter.Context manually (without Schema)
// rather than going through filterCtx() so the test outlives any
// rewiring of filterCtx itself.
func TestFilterDottedPath_MissingResolverErrors(t *testing.T) {
	tasksSpec := registerProjectsAndTasks(t)

	bareCtx := filter.Context{} // deliberately no Schema
	_, _, err := compileRule(`project.owner = @request.auth.id`, tasksSpec, bareCtx, 1)
	if err == nil {
		t.Fatal("expected error when Schema resolver is nil, got nil")
	}
	if !strings.Contains(err.Error(), "Schema resolver not wired") {
		t.Errorf("error message should explain the resolver miss; got: %v", err)
	}
}

// TestSchemaResolver_Adapter proves the adapter returns (zero, false)
// for unknown names and (spec, true) for hits. Direct unit test on
// the helper so refactors to filterCtx don't accidentally regress
// the resolver semantics.
func TestSchemaResolver_Adapter(t *testing.T) {
	registerProjectsAndTasks(t)

	spec, ok := schemaResolver("projects")
	if !ok {
		t.Fatal("schemaResolver returned !ok for registered collection")
	}
	if spec.Name != "projects" {
		t.Errorf("schemaResolver returned wrong spec: %q", spec.Name)
	}
	if _, ok := schemaResolver("nonexistent"); ok {
		t.Error("schemaResolver returned ok for unregistered collection")
	}
}
