package i18n

// Bundle-file cache tests — verify the cache wiring in cache.go behaves
// the way the docs promise: hit on the second call, eviction on the
// flush hook, registry presence.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/railbase/railbase/internal/cache"
)

// TestBundleCache_HitOnSecondLoad asserts that re-reading the SAME file
// path serves the warm entry instead of re-parsing. The signal is the
// underlying disk content changing AFTER the first load: with the cache
// in place, the second LoadDir surfaces the OLD content (cache hit);
// without it, the second LoadDir would surface the NEW content.
//
// This is the "TTL bounds staleness" property the brief calls out. The
// follow-up TestBundleCache_PurgeInvalidates test confirms the manual
// flush hook breaks out of that staleness when needed.
func TestBundleCache_HitOnSecondLoad(t *testing.T) {
	// Tests share the package-level bundleCache; flush before and
	// after so we don't bleed entries between t.Run subtests or other
	// tests in this package.
	bundleCache.Purge()
	t.Cleanup(bundleCache.Purge)

	dir := t.TempDir()
	path := filepath.Join(dir, "en.json")
	if err := os.WriteFile(path, []byte(`{"hi":"Hello"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c1 := NewCatalog("en", []Locale{"en"})
	if _, err := c1.LoadDir(dir); err != nil {
		t.Fatalf("first LoadDir: %v", err)
	}
	if got := c1.T("en", "hi", nil); got != "Hello" {
		t.Fatalf("first load value: got %q want %q", got, "Hello")
	}

	// Mutate the file on disk. A non-cached loader would pick this
	// up immediately; the cached loader serves the prior parse for
	// the duration of the TTL window.
	if err := os.WriteFile(path, []byte(`{"hi":"Bonjour"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	statsBefore := bundleCache.Stats()
	c2 := NewCatalog("en", []Locale{"en"})
	if _, err := c2.LoadDir(dir); err != nil {
		t.Fatalf("second LoadDir: %v", err)
	}
	statsAfter := bundleCache.Stats()

	if got := c2.T("en", "hi", nil); got != "Hello" {
		t.Errorf("expected cached value %q, got %q (cache miss on second load?)", "Hello", got)
	}
	if statsAfter.Hits <= statsBefore.Hits {
		t.Errorf("expected cache hits to increase: before=%+v after=%+v", statsBefore, statsAfter)
	}
}

// TestBundleCache_PurgeInvalidates asserts that PurgeBundleCache drops
// the cached entry so the next read re-parses from disk. This is the
// invariant the future fsnotify watcher and the admin Cache inspector's
// Clear button rely on.
func TestBundleCache_PurgeInvalidates(t *testing.T) {
	bundleCache.Purge()
	t.Cleanup(bundleCache.Purge)

	dir := t.TempDir()
	path := filepath.Join(dir, "en.json")
	if err := os.WriteFile(path, []byte(`{"hi":"Hello"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c1 := NewCatalog("en", []Locale{"en"})
	if _, err := c1.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := c1.T("en", "hi", nil); got != "Hello" {
		t.Fatalf("initial value: got %q", got)
	}

	// Mutate the file, then purge the cache. The next LoadDir MUST
	// surface the new content.
	if err := os.WriteFile(path, []byte(`{"hi":"Bonjour"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	PurgeBundleCache()

	c2 := NewCatalog("en", []Locale{"en"})
	if _, err := c2.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := c2.T("en", "hi", nil); got != "Bonjour" {
		t.Errorf("post-purge value: got %q want %q", got, "Bonjour")
	}
}

// TestBundleCache_RegisteredInRegistry asserts the package init hook
// registered the cache under the documented name. The admin Cache
// inspector relies on this for surface-level observability.
func TestBundleCache_RegisteredInRegistry(t *testing.T) {
	provider, ok := cache.Get("i18n.bundles")
	if !ok {
		t.Fatal(`cache.Get("i18n.bundles") returned (_, false); init() did not register`)
	}
	if provider == nil {
		t.Fatal("registered provider is nil")
	}
	// Sanity-check that Stats() is callable through the interface
	// (the admin handler reaches StatsProvider, not the concrete type).
	_ = provider.Stats()
}

// TestBundleCache_DistinctPathsDoNotCollide ensures the cache key
// includes the full path. Two different files with the same locale
// name but different parent dirs must NOT share a cache entry.
func TestBundleCache_DistinctPathsDoNotCollide(t *testing.T) {
	bundleCache.Purge()
	t.Cleanup(bundleCache.Purge)

	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "en.json"), []byte(`{"k":"A"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "en.json"), []byte(`{"k":"B"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cA := NewCatalog("en", []Locale{"en"})
	if _, err := cA.LoadDir(dirA); err != nil {
		t.Fatal(err)
	}
	cB := NewCatalog("en", []Locale{"en"})
	if _, err := cB.LoadDir(dirB); err != nil {
		t.Fatal(err)
	}

	if got := cA.T("en", "k", nil); got != "A" {
		t.Errorf("dirA: got %q want A", got)
	}
	if got := cB.T("en", "k", nil); got != "B" {
		t.Errorf("dirB: got %q want B (collision with dirA?)", got)
	}
}
