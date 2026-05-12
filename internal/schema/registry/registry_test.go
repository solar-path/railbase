package registry_test

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// resetting helps every test start from a clean slate. Tests in this
// package mutate package-global state, so we can't run them with
// t.Parallel.
func setup(t *testing.T) {
	t.Helper()
	registry.Reset()
	t.Cleanup(registry.Reset)
}

func TestRegister_AddsCollection(t *testing.T) {
	setup(t)

	c := builder.NewCollection("posts").Field("title", builder.NewText())
	registry.Register(c)

	if registry.Count() != 1 {
		t.Fatalf("count: %d", registry.Count())
	}
	if got := registry.Get("posts"); got == nil {
		t.Fatal("Get returned nil")
	}
}

func TestRegister_NilPanics(t *testing.T) {
	setup(t)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	registry.Register(nil)
}

func TestRegister_DuplicatePanics(t *testing.T) {
	setup(t)
	registry.Register(builder.NewCollection("posts").Field("x", builder.NewText()))
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate")
		}
		if !strings.Contains(r.(string), "registered twice") {
			t.Errorf("unexpected panic message: %v", r)
		}
	}()
	registry.Register(builder.NewCollection("posts").Field("y", builder.NewText()))
}

func TestRegister_InvalidPanics(t *testing.T) {
	setup(t)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on invalid collection")
		}
	}()
	// Name starts with `_` — reserved.
	registry.Register(builder.NewCollection("_internal").Field("x", builder.NewText()))
}

func TestAll_SortedByName(t *testing.T) {
	setup(t)

	registry.Register(builder.NewCollection("zebra").Field("x", builder.NewText()))
	registry.Register(builder.NewCollection("alpha").Field("x", builder.NewText()))
	registry.Register(builder.NewCollection("middle").Field("x", builder.NewText()))

	got := registry.All()
	if len(got) != 3 {
		t.Fatalf("count: %d", len(got))
	}
	want := []string{"alpha", "middle", "zebra"}
	for i, c := range got {
		if c.Spec().Name != want[i] {
			t.Errorf("at %d: got %q want %q", i, c.Spec().Name, want[i])
		}
	}
}

func TestSpecs_Materialised(t *testing.T) {
	setup(t)

	registry.Register(builder.NewCollection("posts").
		Field("title", builder.NewText().Required()))

	specs := registry.Specs()
	if len(specs) != 1 {
		t.Fatalf("count: %d", len(specs))
	}
	if specs[0].Name != "posts" || len(specs[0].Fields) != 1 {
		t.Errorf("unexpected spec: %+v", specs[0])
	}
}

func TestReset_ClearsState(t *testing.T) {
	setup(t)
	registry.Register(builder.NewCollection("a").Field("x", builder.NewText()))
	if registry.Count() != 1 {
		t.Fatal("setup failed")
	}
	registry.Reset()
	if registry.Count() != 0 {
		t.Fatal("Reset did not clear")
	}
}
