// Regression tests for the SDK-codegen fixes in Sentinel FEEDBACK.md
// #5–#7. Each was a "the DDL exists but the SDK doesn't surface it"
// papercut that forced consumers into `as unknown as X` casts at every
// read site.
package ts

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
)

// TestEmitTypes_SystemFields_AdjacencyList proves that a collection
// declared with .AdjacencyList() gets `parent?: string | null` on the
// generated TS interface. Without this, Sentinel's tasks tree could
// be loaded from the API (the column exists) but the SDK Tasks type
// hid it — making `task.parent` a compile error.
//
// FEEDBACK.md #5.
func TestEmitTypes_SystemFields_AdjacencyList(t *testing.T) {
	spec := builder.NewCollection("tasks").
		AdjacencyList().
		Field("title", builder.NewText().Required()).
		Spec()
	out := EmitTypes([]builder.CollectionSpec{spec})

	if !strings.Contains(out, "parent?: string | null;") {
		t.Errorf("AdjacencyList collection missing `parent?: string | null;` in TS gen.\noutput:\n%s", out)
	}
}

// TestEmitTypes_SystemFields_Ordered proves `.Ordered()` surfaces
// `sort_index: number;`. Sentinel orders WBS tasks by sort_index and
// without this the SDK forced manual casts.
func TestEmitTypes_SystemFields_Ordered(t *testing.T) {
	spec := builder.NewCollection("tasks").
		Ordered().
		Field("title", builder.NewText().Required()).
		Spec()
	out := EmitTypes([]builder.CollectionSpec{spec})

	if !strings.Contains(out, "sort_index: number;") {
		t.Errorf("Ordered collection missing `sort_index: number;` in TS gen.\noutput:\n%s", out)
	}
}

// TestEmitTypes_SystemFields_SoftDelete proves `.SoftDelete()`
// surfaces `deleted?: string | null;`. Consumers that want to show
// tombstone state (via ?includeDeleted=true) need the field on the
// read interface.
func TestEmitTypes_SystemFields_SoftDelete(t *testing.T) {
	spec := builder.NewCollection("posts").
		SoftDelete().
		Field("title", builder.NewText().Required()).
		Spec()
	out := EmitTypes([]builder.CollectionSpec{spec})

	if !strings.Contains(out, "deleted?: string | null;") {
		t.Errorf("SoftDelete collection missing `deleted?: string | null;` in TS gen.\noutput:\n%s", out)
	}
}

// TestEmitTypes_AllSystemFields_Combined proves stacking the three
// modifiers surfaces ALL three columns — the actual Sentinel `tasks`
// shape.
func TestEmitTypes_AllSystemFields_Combined(t *testing.T) {
	spec := builder.NewCollection("tasks").
		AdjacencyList().
		Ordered().
		SoftDelete().
		Field("title", builder.NewText().Required()).
		Spec()
	out := EmitTypes([]builder.CollectionSpec{spec})

	for _, want := range []string{
		"parent?: string | null;",
		"sort_index: number;",
		"deleted?: string | null;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("combined system fields missing %q.\noutput:\n%s", want, out)
		}
	}
}

// TestEmitTypes_NoSystemFields_OnFlatCollection proves we DON'T leak
// the structural columns when their modifier wasn't set. A regression
// here would emit `parent`/`sort_index`/`deleted` on every interface
// and confuse consumers about what columns actually exist.
func TestEmitTypes_NoSystemFields_OnFlatCollection(t *testing.T) {
	spec := builder.NewCollection("posts").
		Field("title", builder.NewText().Required()).
		Spec()
	out := EmitTypes([]builder.CollectionSpec{spec})

	for _, unwanted := range []string{"parent?:", "sort_index:", "deleted?:"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("flat collection leaked structural column %q.\noutput:\n%s", unwanted, out)
		}
	}
}

// TestEmitTypes_JSON_IsUnknown proves a JSON field generates `unknown`
// not `Record<string, unknown>`. The Record shape rejected legitimate
// array/scalar JSON values — Sentinel's `holidays: string[]` stored
// in JSONB failed to assign. `unknown` forces consumers to narrow,
// which is the correct posture for an unconstrained value.
//
// FEEDBACK.md #6.
func TestEmitTypes_JSON_IsUnknown(t *testing.T) {
	spec := builder.NewCollection("calendars").
		Field("holidays", builder.NewJSON()).
		Spec()
	out := EmitTypes([]builder.CollectionSpec{spec})

	// Must contain `holidays?: unknown;`.
	if !strings.Contains(out, "holidays?: unknown;") {
		t.Errorf("JSON field not typed as `unknown`.\noutput:\n%s", out)
	}
	// Must NOT regress to Record<string, unknown>.
	if strings.Contains(out, "Record<string, unknown>") {
		t.Errorf("JSON field regressed to Record<string, unknown>.\noutput:\n%s", out)
	}
}

// TestEmitRealtime_YieldsGenericTypedEvent proves the realtime
// codegen now casts the parsed event to RealtimeEvent<T> at the
// yield site. Without the cast, `for await (const ev of
// rb.realtime.subscribe<Tasks>(...))` had `ev.data` typed as
// `unknown` rather than `Tasks`, defeating the generic.
//
// FEEDBACK.md #7.
func TestEmitRealtime_YieldsGenericTypedEvent(t *testing.T) {
	out := EmitRealtime()

	// The cast — without it, parseFrame's return type
	// (RealtimeEvent<unknown>) leaks back to callers.
	if !strings.Contains(out, "yield ev as RealtimeEvent<T>") {
		t.Errorf("realtime.ts missing `yield ev as RealtimeEvent<T>` cast.\noutput:\n%s", out)
	}
	// The generic on subscribe is present.
	if !strings.Contains(out, "async *subscribe<T = unknown>(") {
		t.Errorf("realtime.ts subscribe is not generic.\noutput:\n%s", out)
	}
	// The RealtimeEvent type itself is generic.
	if !strings.Contains(out, "RealtimeEvent<T = unknown>") {
		t.Errorf("realtime.ts RealtimeEvent is not generic.\noutput:\n%s", out)
	}
}
