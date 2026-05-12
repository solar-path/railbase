package settings

import (
	"testing"

	"github.com/railbase/railbase/internal/cache"
)

// TestManager_RegistersWithCacheRegistry verifies that constructing
// a settings.Manager auto-registers it as a StatsProvider, so the
// admin Cache inspector picks it up alongside filter.ast / rbac.resolver
// / i18n.bundles.
func TestManager_RegistersWithCacheRegistry(t *testing.T) {
	// Construct a Manager with nil pool — we never invoke Get/Set in
	// this test, so the pool isn't dereferenced. New() must still
	// register the cache.
	m := New(Options{}) // pool=nil, bus=nil — registration must not depend on either
	if m == nil {
		t.Fatal("New returned nil")
	}
	if _, ok := cache.Get("settings"); !ok {
		t.Fatal("settings cache not registered with cache.Registry")
	}

	// Stats on a fresh Manager should be all-zero.
	st := m.Stats()
	if st.Hits != 0 || st.Misses != 0 || st.Size != 0 {
		t.Errorf("fresh Manager Stats() = %+v, want zero", st)
	}
}

// TestManager_StatsReflectInternalState exercises Hits/Misses/Size
// counters via direct cache-map mutation (we don't have a real pool
// here so we bypass Get's Postgres-read fallback).
func TestManager_StatsReflectInternalState(t *testing.T) {
	m := New(Options{})

	// Seed two entries directly + bump counters as Get would.
	m.mu.Lock()
	m.cache["foo"] = []byte(`"bar"`)
	m.cache["x"] = []byte(`42`)
	m.mu.Unlock()
	m.hits.Add(7)
	m.misses.Add(3)

	st := m.Stats()
	if st.Hits != 7 {
		t.Errorf("Hits = %d, want 7", st.Hits)
	}
	if st.Misses != 3 {
		t.Errorf("Misses = %d, want 3", st.Misses)
	}
	if st.Size != 2 {
		t.Errorf("Size = %d, want 2", st.Size)
	}

	// Clear drops entries + zeros counters.
	m.Clear()
	st = m.Stats()
	if st.Hits != 0 || st.Misses != 0 || st.Size != 0 {
		t.Errorf("Stats post-Clear = %+v, want zero", st)
	}
}
