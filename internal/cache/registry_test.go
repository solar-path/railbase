package cache

import (
	"sync"
	"testing"
)

// TestRegistry_RegisterAndAll covers the happy path: register a few
// instances under distinct names, snapshot via All(), confirm each
// name maps to the expected provider. Verifies the returned map is a
// fresh allocation (mutating it doesn't affect subsequent All() calls).
func TestRegistry_RegisterAndAll(t *testing.T) {
	// Use unique names per test so concurrent test runs don't collide
	// on the package-global registry. The simple approach is a
	// per-test name prefix + manual Unregister on cleanup.
	c1 := New[string, int](Options{Capacity: 8})
	c2 := New[string, string](Options{Capacity: 8})

	Register("t-regAndAll-a", c1)
	Register("t-regAndAll-b", c2)
	t.Cleanup(func() {
		Unregister("t-regAndAll-a")
		Unregister("t-regAndAll-b")
	})

	all := All()
	if _, ok := all["t-regAndAll-a"]; !ok {
		t.Errorf("expected t-regAndAll-a in registry")
	}
	if _, ok := all["t-regAndAll-b"]; !ok {
		t.Errorf("expected t-regAndAll-b in registry")
	}

	// Mutating the returned map must not affect the registry.
	delete(all, "t-regAndAll-a")
	all2 := All()
	if _, ok := all2["t-regAndAll-a"]; !ok {
		t.Errorf("registry should still hold t-regAndAll-a after caller mutation")
	}
}

// TestRegistry_RegisterReplaces verifies the idempotency contract:
// Register(name, c2) after Register(name, c1) replaces — last writer
// wins. The admin UI surfaces exactly one row per name.
func TestRegistry_RegisterReplaces(t *testing.T) {
	c1 := New[string, int](Options{Capacity: 8})
	c1.Set("k", 1)
	// Drive c1 to a non-zero stats footprint so the post-replace
	// snapshot looks different from c2's.
	_, _ = c1.Get("k")

	c2 := New[string, int](Options{Capacity: 8})
	// c2 is fresh — zero hits.

	Register("t-replace", c1)
	Register("t-replace", c2)
	t.Cleanup(func() { Unregister("t-replace") })

	got, ok := Get("t-replace")
	if !ok {
		t.Fatalf("registry lost the entry")
	}
	stats := got.Stats()
	if stats.Hits != 0 {
		t.Errorf("expected c2 (fresh) to be live after replace; got hits=%d", stats.Hits)
	}
}

// TestRegistry_Unregister covers removal + the no-op-on-unknown
// behaviour. Two assertions: removing a present name drops it; removing
// an absent name does not panic.
func TestRegistry_Unregister(t *testing.T) {
	c := New[string, int](Options{Capacity: 8})
	Register("t-unreg", c)
	if _, ok := Get("t-unreg"); !ok {
		t.Fatalf("setup failed")
	}
	Unregister("t-unreg")
	if _, ok := Get("t-unreg"); ok {
		t.Errorf("Unregister did not remove the entry")
	}
	// Idempotent — second Unregister on the same name must not panic.
	Unregister("t-unreg")
	// And unknown names must not panic either.
	Unregister("t-never-existed")
}

// TestRegistry_AllSnapshotThreadSafe drives concurrent Register /
// Unregister / All() to confirm the sync.Map-backed registry is safe
// under -race. We don't assert on a particular intermediate state;
// the test passes if `go test -race` doesn't fire a data-race report.
func TestRegistry_AllSnapshotThreadSafe(t *testing.T) {
	const goroutines = 8
	const iterations = 200

	// Pre-allocate a pool of caches so we're not measuring allocator
	// behaviour. Same instance reused across iterations is fine — the
	// registry stores pointers.
	caches := make([]*Cache[string, int], goroutines)
	for i := range caches {
		caches[i] = New[string, int](Options{Capacity: 8})
	}

	t.Cleanup(func() {
		for i := 0; i < goroutines; i++ {
			Unregister("t-conc-" + string(rune('a'+i)))
		}
	})

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writer goroutines: register + unregister hot.
	for g := 0; g < goroutines; g++ {
		name := "t-conc-" + string(rune('a'+g))
		c := caches[g]
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				Register(name, c)
				Unregister(name)
			}
		}()
	}

	// Reader goroutines: pull All() snapshots in a tight loop.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = All()
			}
		}()
	}

	wg.Wait()
}

// TestRegistry_ClearViaRegistry exercises the full path the admin
// handler uses: look up by name, call Clear() through the StatsProvider
// interface (so heterogeneous K/V caches are interchangeable), confirm
// both the entry count AND the counters reset. This is the contract
// the admin UI's Clear button depends on.
func TestRegistry_ClearViaRegistry(t *testing.T) {
	c := New[string, int](Options{Capacity: 8})
	c.Set("a", 1)
	c.Set("b", 2)
	_, _ = c.Get("a") // hit
	_, _ = c.Get("z") // miss

	Register("t-clear", c)
	t.Cleanup(func() { Unregister("t-clear") })

	before := c.Stats()
	if before.Hits == 0 || before.Misses == 0 || before.Size == 0 {
		t.Fatalf("setup failed; stats=%+v", before)
	}

	got, ok := Get("t-clear")
	if !ok {
		t.Fatalf("registry lookup failed")
	}
	got.Clear()

	after := c.Stats()
	if after.Hits != 0 || after.Misses != 0 || after.Size != 0 || after.Evictions != 0 {
		t.Errorf("Clear() did not reset; stats=%+v", after)
	}
}

// TestRegistry_RejectsEmptyAndNil pins the defensive guards in
// Register: empty name + nil provider are silently rejected so the
// admin route's {name} segment can't collide with the list endpoint
// and so the handler never dereferences a nil provider.
func TestRegistry_RejectsEmptyAndNil(t *testing.T) {
	Register("", New[string, int](Options{Capacity: 4}))
	if _, ok := Get(""); ok {
		t.Errorf("empty-name registration should be rejected")
	}

	Register("t-nil", nil)
	if _, ok := Get("t-nil"); ok {
		t.Errorf("nil provider registration should be rejected")
	}
}
