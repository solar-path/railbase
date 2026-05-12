package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- basic semantics ---

func TestGetSetDelete(t *testing.T) {
	c := New[string, int](Options{Capacity: 8})
	if _, ok := c.Get("a"); ok {
		t.Error("empty cache should miss")
	}
	c.Set("a", 1)
	v, ok := c.Get("a")
	if !ok || v != 1 {
		t.Errorf("got (%d, %v); want (1, true)", v, ok)
	}
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Error("after delete should miss")
	}
}

func TestSet_Overwrites(t *testing.T) {
	c := New[string, int](Options{Capacity: 2})
	c.Set("k", 1)
	c.Set("k", 2)
	v, _ := c.Get("k")
	if v != 2 {
		t.Errorf("overwrite: got %d, want 2", v)
	}
	if c.Len() != 1 {
		t.Errorf("len: got %d, want 1", c.Len())
	}
}

// --- LRU eviction ---

func TestLRU_EvictsOldest(t *testing.T) {
	// 1 shard with cap=4 — fill, touch "a" to promote, then insert
	// a 5th key. "b" (LRU after the promotion) should be evicted.
	c := New[string, int](Options{Capacity: 4, Shards: 1})
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	c.Set("d", 4)
	_, _ = c.Get("a")
	c.Set("e", 5)
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a should still be present (was promoted)")
	}
	st := c.Stats()
	if st.Evictions == 0 {
		t.Error("expected at least one eviction")
	}
}

// --- TTL ---

func TestTTL_ExpiresEntries(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clk := func() time.Time { return now }
	c := New[string, int](Options{Capacity: 4, TTL: 30 * time.Second, Clock: clk})
	c.Set("k", 42)
	// Read while fresh.
	if v, ok := c.Get("k"); !ok || v != 42 {
		t.Errorf("fresh: got (%d, %v)", v, ok)
	}
	// Advance past TTL.
	now = now.Add(31 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Error("expired entry should miss")
	}
	if c.Len() != 0 {
		t.Errorf("len after expiry: got %d, want 0", c.Len())
	}
}

func TestTTL_RefreshedOnSet(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clk := func() time.Time { return now }
	c := New[string, int](Options{Capacity: 4, TTL: 10 * time.Second, Clock: clk})
	c.Set("k", 1)
	now = now.Add(8 * time.Second)
	c.Set("k", 2) // refresh
	now = now.Add(5 * time.Second)
	if v, ok := c.Get("k"); !ok || v != 2 {
		t.Errorf("refreshed should still be live; got (%d, %v)", v, ok)
	}
}

// --- singleflight ---

func TestGetOrLoad_SingleFlight(t *testing.T) {
	c := New[string, string](Options{Capacity: 8})
	var calls atomic.Int32
	loader := func(ctx context.Context) (string, error) {
		calls.Add(1)
		// Block long enough that concurrent callers stack up.
		time.Sleep(50 * time.Millisecond)
		return "value", nil
	}
	const N = 16
	var wg sync.WaitGroup
	results := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = c.GetOrLoad(context.Background(), "key", loader)
		}(i)
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("loader invocations: got %d, want 1 (singleflight)", got)
	}
	for i, r := range results {
		if r != "value" || errs[i] != nil {
			t.Errorf("caller %d: got (%q, %v)", i, r, errs[i])
		}
	}
}

func TestGetOrLoad_ErrorNotCached(t *testing.T) {
	c := New[string, string](Options{Capacity: 8})
	var calls atomic.Int32
	errBoom := errors.New("boom")
	loader := func(ctx context.Context) (string, error) {
		calls.Add(1)
		return "", errBoom
	}
	for i := 0; i < 3; i++ {
		_, err := c.GetOrLoad(context.Background(), "k", loader)
		if !errors.Is(err, errBoom) {
			t.Errorf("attempt %d: got %v", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("loader should be called once per failing attempt; got %d", got)
	}
}

func TestGetOrLoad_HitSkipsLoader(t *testing.T) {
	c := New[string, int](Options{Capacity: 4})
	c.Set("k", 99)
	var calls atomic.Int32
	v, err := c.GetOrLoad(context.Background(), "k", func(_ context.Context) (int, error) {
		calls.Add(1)
		return 0, nil
	})
	if err != nil || v != 99 {
		t.Errorf("hit: got (%d, %v)", v, err)
	}
	if calls.Load() != 0 {
		t.Error("loader must not run on hit")
	}
}

// --- stats ---

func TestStats(t *testing.T) {
	// Cap 4 per shard (the package minimum). Fill 4 entries, then a
	// 5th to force exactly one eviction.
	c := New[string, int](Options{Capacity: 4, Shards: 1})
	for i, k := range []string{"a", "b", "c", "d"} {
		c.Set(k, i)
	}
	_, _ = c.Get("a")
	_, _ = c.Get("a")
	_, _ = c.Get("x") // miss
	c.Set("e", 99)    // evicts b (a was promoted by Get)
	st := c.Stats()
	if st.Hits != 2 || st.Misses != 1 || st.Evictions != 1 {
		t.Errorf("stats: hits=%d misses=%d evictions=%d", st.Hits, st.Misses, st.Evictions)
	}
}

// --- purge ---

func TestPurge(t *testing.T) {
	c := New[string, int](Options{Capacity: 16})
	for i := 0; i < 10; i++ {
		c.Set(string(rune('a'+i)), i)
	}
	c.Purge()
	if c.Len() != 0 {
		t.Errorf("len after purge: %d", c.Len())
	}
	if _, ok := c.Get("a"); ok {
		t.Error("key survived purge")
	}
}

// --- sharding behaviour ---

func TestSharding_RoutesKeys(t *testing.T) {
	c := New[string, int](Options{Capacity: 256, Shards: 16})
	// Insert distinct keys; verify they end up in multiple shards.
	for i := 0; i < 100; i++ {
		c.Set(string(rune('a'+i%26))+string(rune('A'+i/26)), i)
	}
	occupied := 0
	for _, sh := range c.shards {
		sh.mu.Lock()
		if len(sh.entries) > 0 {
			occupied++
		}
		sh.mu.Unlock()
	}
	if occupied < 4 {
		t.Errorf("expected keys to spread across shards; only %d occupied", occupied)
	}
}

func TestShardCount_RoundsUpToPow2(t *testing.T) {
	c := New[string, int](Options{Capacity: 100, Shards: 5})
	if len(c.shards) != 8 {
		t.Errorf("shards: got %d, want 8 (rounded up)", len(c.shards))
	}
}

// --- race-safety smoke ---

func TestRaceSafety(t *testing.T) {
	c := New[int, int](Options{Capacity: 1024, Shards: 16})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				k := (seed*7 + j) % 200
				if j%3 == 0 {
					c.Set(k, j)
				} else {
					_, _ = c.Get(k)
				}
			}
		}(i)
	}
	wg.Wait()
}
