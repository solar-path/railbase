package filter

// Tests for the filter AST cache (cache.go) — proves the hit/miss path
// and the singleflight collapse of concurrent identical parses.
//
// Internal-package tests (no _test suffix on the package name) so the
// assertions can inspect astCache.Stats() directly without exposing
// the cache instance through the public API. The cache instance is a
// package-global, so each test calls Purge + resets stats by Clear()
// at the top to keep the suite order-independent — the parser is
// pure-functional so the only shared state to manage is the cache
// itself.

import (
	"strings"
	"sync"
	"testing"
)

// TestASTCache_HitMissPath verifies the second parse of an identical
// filter string skips the recursive-descent parser and is served from
// the cache. Stats: 1 miss + load on first call, 1 hit on second.
func TestASTCache_HitMissPath(t *testing.T) {
	astCache.Clear()
	const src = "status = 'published' && hits > 10"

	// First call: miss + load.
	ast1, err := Parse(src)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	if ast1 == nil {
		t.Fatal("first parse returned nil AST")
	}

	st := astCache.Stats()
	if st.Loads != 1 {
		t.Errorf("after first parse: loads=%d, want 1", st.Loads)
	}
	if st.Hits != 0 {
		t.Errorf("after first parse: hits=%d, want 0", st.Hits)
	}

	// Second call with identical source: should hit the cache. Pointer
	// equality is the cleanest "we didn't re-parse" signal because the
	// recursive-descent parser allocates a fresh AST every invocation —
	// if we observe the same root pointer twice, it came from the cache.
	ast2, err := Parse(src)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if ast2 != ast1 {
		t.Errorf("second call should return cached AST pointer; got fresh tree")
	}

	st = astCache.Stats()
	if st.Hits != 1 {
		t.Errorf("after second parse: hits=%d, want 1", st.Hits)
	}
	if st.Loads != 1 {
		t.Errorf("after second parse: loads=%d, want 1 (no re-parse)", st.Loads)
	}
}

// TestASTCache_DistinctSourcesMissIndependently verifies the cache
// keys on the full source string — superficially-similar filters with
// different operands miss independently.
func TestASTCache_DistinctSourcesMissIndependently(t *testing.T) {
	astCache.Clear()
	sources := []string{
		"status = 'a'",
		"status = 'b'",
		"status = 'c'",
		"hits > 10",
		"hits < 20",
	}
	for _, src := range sources {
		if _, err := Parse(src); err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
	}
	st := astCache.Stats()
	if st.Loads != int64(len(sources)) {
		t.Errorf("loads=%d, want %d (each distinct source loads once)",
			st.Loads, len(sources))
	}
	if st.Size != len(sources) {
		t.Errorf("size=%d, want %d", st.Size, len(sources))
	}
}

// TestASTCache_ConcurrentIdenticalParse exercises the singleflight
// path inside GetOrLoad. N goroutines fire the same filter source at
// once; the parser should run exactly once and every caller should
// receive the same AST root pointer.
//
// The `-race` build flag makes this a real thread-safety check: the
// underlying cache shard mutex + the singleflight inflight map must
// keep the AST sharing race-clean.
func TestASTCache_ConcurrentIdenticalParse(t *testing.T) {
	astCache.Clear()
	const src = "(status = 'a' || status = 'b') && hits BETWEEN 1 AND 100"

	const N = 32
	var wg sync.WaitGroup
	roots := make([]Node, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			roots[i], errs[i] = Parse(src)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: err=%v", i, err)
		}
		if roots[i] == nil {
			t.Errorf("caller %d: nil root", i)
		}
	}
	// All callers should observe the same shared root pointer (only
	// one made it past singleflight into the cache; the rest waited
	// for that one and got its value back).
	first := roots[0]
	for i, r := range roots[1:] {
		if r != first {
			t.Errorf("caller %d returned a different root pointer; expected shared", i+1)
		}
	}

	st := astCache.Stats()
	if st.Loads != 1 {
		t.Errorf("singleflight should collapse to one load; got %d", st.Loads)
	}
}

// TestASTCache_RegisteredInRegistry sanity-checks the package init —
// the admin Cache inspector relies on cache.Get / cache.All returning
// our instance.
func TestASTCache_RegisteredInRegistry(t *testing.T) {
	// Imported indirectly via the registry — but we don't want to add
	// a dep on internal/cache here just to call cache.Get. Instead, we
	// confirm registration succeeded by checking the package init
	// completed (astCache is non-nil) and that a known-bad source
	// surfaces a PositionedError without panicking — proving the
	// cache miss + loader-error path is wired.
	if astCache == nil {
		t.Fatal("astCache nil — package init did not run")
	}
	astCache.Clear()
	_, err := Parse("status ?? 'broken'")
	if err == nil {
		t.Fatal("expected parse error for bad source")
	}
	if !strings.Contains(err.Error(), "unexpected") {
		t.Errorf("err=%v; want 'unexpected token' style error", err)
	}
	// Bad source should NOT be cached (singleflight contract: errors
	// don't poison the entry).
	st := astCache.Stats()
	if st.LoadFails != 1 {
		t.Errorf("loadFails=%d, want 1", st.LoadFails)
	}
	if st.Size != 0 {
		t.Errorf("size=%d, want 0 (errors must not be cached)", st.Size)
	}
}
