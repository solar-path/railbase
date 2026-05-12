package rbac

// Tests for the resolved-actor cache (cache.go) — covers the
// store-fronting helper without standing up a Postgres pool.
//
// Strategy: cachedResolve takes a *Store, but the only thing it does
// with the store in the cached path is invoke store.Resolve via the
// loader closure. We use a fakeStore-style stand-in by going through
// resolverCache directly with a hand-rolled loader that mimics what
// Resolve would return. This keeps the test pure-Go and lets us
// assert hit/miss/TTL behaviour deterministically.
//
// Tests run against the package-global resolverCache; each test
// purges the cache at the top so order doesn't matter. The cache
// state is shared, but the package-level mutex inside cache.Cache
// keeps concurrent test runs (-parallel) safe.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/cache"
	"github.com/railbase/railbase/internal/rbac/actionkeys"
)

// makeResolved is a test helper that builds a populated *Resolved so
// callers can verify the cache returns the same pointer across hits.
func makeResolved(uid uuid.UUID, tid *uuid.UUID, actions ...actionkeys.ActionKey) *Resolved {
	m := make(map[actionkeys.ActionKey]struct{}, len(actions))
	for _, a := range actions {
		m[a] = struct{}{}
	}
	return &Resolved{
		UserCollection: "users",
		UserID:         uid,
		TenantID:       tid,
		Actions:        m,
	}
}

// TestResolverCache_HitsAfterMiss exercises the basic miss→load→hit
// sequence. We bypass Store.Resolve entirely (we don't have a
// Postgres) by driving resolverCache.GetOrLoad with a synthetic
// loader; this is the exact same code path cachedResolve takes.
func TestResolverCache_HitsAfterMiss(t *testing.T) {
	resolverCache.Clear()

	uid := uuid.New()
	tid := uuid.New()
	key := resolverKey{collectionName: "users", recordID: uid, tenantID: tid}

	var loads atomic.Int32
	loader := func(_ context.Context) (*Resolved, error) {
		loads.Add(1)
		return makeResolved(uid, &tid, "tenant.read"), nil
	}

	// First call: miss + load.
	r1, err := resolverCache.GetOrLoad(context.Background(), key, loader)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if r1 == nil {
		t.Fatal("first load returned nil")
	}
	if got := loads.Load(); got != 1 {
		t.Errorf("loads after first call: got %d, want 1", got)
	}

	// Second call: hit, no load.
	r2, err := resolverCache.GetOrLoad(context.Background(), key, loader)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if r2 != r1 {
		t.Errorf("expected the same *Resolved pointer back from cache")
	}
	if got := loads.Load(); got != 1 {
		t.Errorf("loader should not run on hit; got %d invocations", got)
	}
}

// TestResolverCache_TTLExpiry confirms the 5-minute TTL spelled out in
// docs/14 takes effect: an entry past its TTL re-loads on next access.
// We can't easily inject a fake clock into the package-global cache
// without exposing internals, so we spin up a local cache.Cache with
// the same options + a Clock hook to drive deterministic expiry.
func TestResolverCache_TTLExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clk := func() time.Time { return now }

	c := cache.New[resolverKey, *Resolved](cache.Options{
		Capacity: 1024,
		TTL:      5 * time.Minute,
		Clock:    clk,
	})

	uid := uuid.New()
	key := resolverKey{collectionName: "users", recordID: uid}

	var loads atomic.Int32
	loader := func(_ context.Context) (*Resolved, error) {
		loads.Add(1)
		return makeResolved(uid, nil, "audit.read"), nil
	}

	// Initial load.
	if _, err := c.GetOrLoad(context.Background(), key, loader); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	// Within TTL: hit.
	now = now.Add(4 * time.Minute)
	if _, err := c.GetOrLoad(context.Background(), key, loader); err != nil {
		t.Fatalf("within-ttl load: %v", err)
	}
	if got := loads.Load(); got != 1 {
		t.Errorf("loader should not run inside TTL; got %d", got)
	}

	// Past TTL: re-loads.
	now = now.Add(2 * time.Minute) // 6 min total → past 5 min TTL
	if _, err := c.GetOrLoad(context.Background(), key, loader); err != nil {
		t.Fatalf("after-ttl load: %v", err)
	}
	if got := loads.Load(); got != 2 {
		t.Errorf("loader should re-run after TTL expiry; got %d, want 2", got)
	}
}

// TestResolverCache_TenantKeyIsolation confirms the cache key includes
// tenantID — the same user in two different tenants must NOT share a
// resolve entry (different tenant scopes pick up different grants).
func TestResolverCache_TenantKeyIsolation(t *testing.T) {
	resolverCache.Clear()

	uid := uuid.New()
	tenantA := uuid.New()
	tenantB := uuid.New()

	keyA := resolverKey{collectionName: "users", recordID: uid, tenantID: tenantA}
	keyB := resolverKey{collectionName: "users", recordID: uid, tenantID: tenantB}

	var loads atomic.Int32
	loader := func(_ context.Context) (*Resolved, error) {
		loads.Add(1)
		return makeResolved(uid, nil), nil
	}

	if _, err := resolverCache.GetOrLoad(context.Background(), keyA, loader); err != nil {
		t.Fatalf("load A: %v", err)
	}
	if _, err := resolverCache.GetOrLoad(context.Background(), keyB, loader); err != nil {
		t.Fatalf("load B: %v", err)
	}
	if got := loads.Load(); got != 2 {
		t.Errorf("different tenants must miss independently; got %d loads, want 2", got)
	}
}

// TestPurgeResolverCache_ClearsEntries exercises the public hook that
// admin handlers (and future eventbus subscribers) will call after a
// role mutation. After a Purge the next access must re-load.
func TestPurgeResolverCache_ClearsEntries(t *testing.T) {
	resolverCache.Clear()

	uid := uuid.New()
	key := resolverKey{collectionName: "users", recordID: uid}

	var loads atomic.Int32
	loader := func(_ context.Context) (*Resolved, error) {
		loads.Add(1)
		return makeResolved(uid, nil, "settings.read"), nil
	}

	if _, err := resolverCache.GetOrLoad(context.Background(), key, loader); err != nil {
		t.Fatalf("seed: %v", err)
	}
	PurgeResolverCache()
	if _, err := resolverCache.GetOrLoad(context.Background(), key, loader); err != nil {
		t.Fatalf("post-purge: %v", err)
	}
	if got := loads.Load(); got != 2 {
		t.Errorf("purge should force re-load; got %d invocations", got)
	}
}

// TestCachedResolve_NilStoreShortCircuits verifies the guest-path
// guard: an unauthenticated request (or a misconfigured wiring with
// nil store / zero recordID) returns the empty Resolved without
// touching the cache or the DB.
func TestCachedResolve_NilStoreShortCircuits(t *testing.T) {
	resolverCache.Clear()
	r, err := cachedResolve(context.Background(), nil, "users", uuid.Nil, nil)
	if err != nil {
		t.Fatalf("nil-store path: %v", err)
	}
	if r == nil {
		t.Fatal("nil-store path returned nil Resolved")
	}
	if r.UserID != uuid.Nil {
		t.Errorf("UserID = %v; want zero", r.UserID)
	}
	if got := resolverCache.Stats().Size; got != 0 {
		t.Errorf("nil-store path must not populate cache; size=%d", got)
	}
}

// TestResolverCache_RegisteredInRegistry confirms the package init
// registered the cache so the admin Cache inspector sees it.
func TestResolverCache_RegisteredInRegistry(t *testing.T) {
	got, ok := cache.Get("rbac.resolver")
	if !ok {
		t.Fatal("rbac.resolver not registered in cache.registry")
	}
	if got == nil {
		t.Fatal("registered StatsProvider is nil")
	}
}

// TestResolverCache_ConcurrentSameKey is the race-safety smoke test
// for the singleflight path: many goroutines hit the same fresh key,
// the loader should run exactly once.
func TestResolverCache_ConcurrentSameKey(t *testing.T) {
	resolverCache.Clear()

	uid := uuid.New()
	key := resolverKey{collectionName: "users", recordID: uid}

	var loads atomic.Int32
	loader := func(_ context.Context) (*Resolved, error) {
		loads.Add(1)
		// Tiny sleep so concurrent callers can pile up on the
		// inflight singleflight entry.
		time.Sleep(20 * time.Millisecond)
		return makeResolved(uid, nil), nil
	}

	const N = 24
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = resolverCache.GetOrLoad(context.Background(), key, loader)
		}()
	}
	wg.Wait()

	if got := loads.Load(); got != 1 {
		t.Errorf("singleflight should collapse to one load; got %d", got)
	}
}
