// Package registry holds the in-memory map of declared collections.
//
// User code calls schema.Register(...) (a thin wrapper that delegates
// here) inside an init() function in their schema package. CLI
// commands then iterate All() to know what the user wants Railbase
// to do.
//
// Why a global registry vs explicit slice argument:
//   - User's `schema/posts.go`, `schema/users.go` etc. each declare
//     their collection at package scope and call Register in init().
//     A single import of the schema package by the user's `cmd/main.go`
//     populates the registry without further glue.
//   - Migration tooling, admin UI, MCP server — all want the same
//     view. Threading a slice through every entry point is friction.
//
// Cost we accept: tests that touch the registry must call Reset() to
// avoid cross-test pollution. Tests are tagged accordingly.
package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/railbase/railbase/internal/schema/builder"
)

// global is intentionally package-private. All access goes through
// the exported functions, which take the lock.
var (
	mu       sync.Mutex
	bySource = map[string]*builder.CollectionBuilder{}
)

// Register adds c to the registry. Calling twice with the same
// collection name panics — the registry is meant to be populated
// once per process during init().
//
// Validation runs here too: a collection that fails Validate() will
// surface the error early (panic, since this is init-time).
func Register(c *builder.CollectionBuilder) {
	if c == nil {
		panic("registry: nil collection")
	}
	if err := c.Validate(); err != nil {
		panic("registry: " + err.Error())
	}

	mu.Lock()
	defer mu.Unlock()

	name := c.Spec().Name
	if _, dup := bySource[name]; dup {
		panic(fmt.Sprintf("registry: collection %q registered twice", name))
	}
	bySource[name] = c
}

// Add registers c at runtime — the admin-UI counterpart of Register.
// Unlike Register it returns an error instead of panicking: a bad
// request must not take down the process. Validation runs here, the
// same as Register.
//
// Replace=false: a name already in the registry is an error (the
// caller wanted a fresh create). Replace=true: an existing entry is
// overwritten in place — used by the runtime "edit collection" path
// after the DDL has been applied.
func Add(c *builder.CollectionBuilder, replace bool) error {
	if c == nil {
		return fmt.Errorf("registry: nil collection")
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("registry: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()

	name := c.Spec().Name
	if _, dup := bySource[name]; dup && !replace {
		return fmt.Errorf("registry: collection %q already registered", name)
	}
	bySource[name] = c
	return nil
}

// Remove drops a collection from the registry. Returns true if the
// name was present. Runtime-only — the compile-time registry is never
// shrunk.
func Remove(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := bySource[name]
	delete(bySource, name)
	return ok
}

// All returns the registered collections in deterministic order
// (alphabetical by name). Returned slice is a snapshot — caller
// can sort/filter/iterate freely.
func All() []*builder.CollectionBuilder {
	mu.Lock()
	defer mu.Unlock()

	names := make([]string, 0, len(bySource))
	for n := range bySource {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]*builder.CollectionBuilder, 0, len(names))
	for _, n := range names {
		out = append(out, bySource[n])
	}
	return out
}

// Specs is a convenience that returns the materialised CollectionSpec
// for every registered collection, in the same order as All().
func Specs() []builder.CollectionSpec {
	cols := All()
	out := make([]builder.CollectionSpec, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.Spec())
	}
	return out
}

// Get looks up one collection by name. Returns nil if not registered.
func Get(name string) *builder.CollectionBuilder {
	mu.Lock()
	defer mu.Unlock()
	return bySource[name]
}

// Count returns how many collections are registered.
func Count() int {
	mu.Lock()
	defer mu.Unlock()
	return len(bySource)
}

// Reset clears the registry. Use ONLY in tests; production code never
// resets. The function is exported so tests in other packages can
// scrub state between table-driven cases.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	bySource = map[string]*builder.CollectionBuilder{}
}
