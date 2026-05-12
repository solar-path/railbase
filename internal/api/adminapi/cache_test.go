package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/cache"
)

// Test helpers for the cache admin surface. The registry is a package-
// global in `internal/cache`, so each test must Register under a
// uniquely-namespaced name and Unregister on Cleanup to keep tests
// independent under `-count=N`.

// TestCacheList_Empty exercises the "no caches registered" shape — the
// admin UI relies on this returning an empty `instances` array (not a
// 503) so the empty-state copy renders cleanly during the gradual
// per-subsystem wire-up.
func TestCacheList_Empty(t *testing.T) {
	// Pre-flight: snapshot the registry size so we can refuse to run
	// if another test leaked. (Cheap defence; the per-test Unregister
	// pattern means this rarely fires.)
	for n := range cache.All() {
		t.Logf("note: pre-existing registry entry %q (other test leak?)", n)
	}

	d := &Deps{}
	r := chi.NewRouter()
	d.mountCache(r)

	req := httptest.NewRequest(http.MethodGet, "/cache", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	var got cacheListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	// The handler always emits an array — never nil — so the JS side
	// can iterate without a null check. If another test leaked an
	// entry, the slice is non-empty; we tolerate that here so this
	// test isn't fragile, but we do require the array form.
	if got.Instances == nil {
		t.Errorf("instances should be a non-nil array (got null)")
	}
}

// TestCacheList_TwoRegistered confirms two caches register, their
// stats roll through the JSON envelope, and the server-computed
// hit_rate_pct matches the documented formula.
func TestCacheList_TwoRegistered(t *testing.T) {
	c1 := cache.New[string, int](cache.Options{Capacity: 8})
	c1.Set("a", 1)
	c1.Set("b", 2)
	// Drive c1 to 3 hits / 1 miss → 75.0%.
	_, _ = c1.Get("a")
	_, _ = c1.Get("a")
	_, _ = c1.Get("b")
	_, _ = c1.Get("missing")

	c2 := cache.New[string, string](cache.Options{Capacity: 8})
	// c2 stays fresh — zero requests → 0.0%.

	cache.Register("t-list-aaa", c1)
	cache.Register("t-list-bbb", c2)
	t.Cleanup(func() {
		cache.Unregister("t-list-aaa")
		cache.Unregister("t-list-bbb")
	})

	d := &Deps{}
	r := chi.NewRouter()
	d.mountCache(r)

	req := httptest.NewRequest(http.MethodGet, "/cache", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got cacheListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}

	// Find our two entries by name (other tests may have leaked).
	var aaa, bbb *cacheInstanceJSON
	for i := range got.Instances {
		switch got.Instances[i].Name {
		case "t-list-aaa":
			aaa = &got.Instances[i]
		case "t-list-bbb":
			bbb = &got.Instances[i]
		}
	}
	if aaa == nil {
		t.Fatalf("missing t-list-aaa in response: %+v", got)
	}
	if bbb == nil {
		t.Fatalf("missing t-list-bbb in response: %+v", got)
	}

	if aaa.Stats.Hits != 3 {
		t.Errorf("aaa hits: want 3, got %d", aaa.Stats.Hits)
	}
	if aaa.Stats.Misses != 1 {
		t.Errorf("aaa misses: want 1, got %d", aaa.Stats.Misses)
	}
	if aaa.Stats.HitRatePct != 75.0 {
		t.Errorf("aaa hit_rate_pct: want 75.0, got %v", aaa.Stats.HitRatePct)
	}
	if aaa.Stats.Size != 2 {
		t.Errorf("aaa size: want 2, got %d", aaa.Stats.Size)
	}

	if bbb.Stats.Hits != 0 || bbb.Stats.Misses != 0 {
		t.Errorf("bbb should be quiescent; got hits=%d misses=%d", bbb.Stats.Hits, bbb.Stats.Misses)
	}
	if bbb.Stats.HitRatePct != 0.0 {
		t.Errorf("bbb hit_rate_pct: want 0.0 for zero-request cache, got %v", bbb.Stats.HitRatePct)
	}
}

// TestCacheClear_ResetsCache wires Clear through the handler and
// verifies the underlying cache's stats zero out — this is the
// contract the admin UI's Clear button leans on.
func TestCacheClear_ResetsCache(t *testing.T) {
	c := cache.New[string, int](cache.Options{Capacity: 8})
	c.Set("a", 1)
	_, _ = c.Get("a")        // hit
	_, _ = c.Get("missing")  // miss

	cache.Register("t-clear-h", c)
	t.Cleanup(func() { cache.Unregister("t-clear-h") })

	before := c.Stats()
	if before.Hits == 0 || before.Size == 0 {
		t.Fatalf("setup failed; stats=%+v", before)
	}

	d := &Deps{}
	r := chi.NewRouter()
	d.mountCache(r)

	req := httptest.NewRequest(http.MethodPost, "/cache/t-clear-h/clear", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d body=%s", rec.Code, rec.Body.String())
	}

	after := c.Stats()
	if after.Hits != 0 || after.Misses != 0 || after.Size != 0 {
		t.Errorf("Clear() did not zero the cache; stats=%+v", after)
	}
}

// TestCacheClear_UnknownName covers the 404 path.
func TestCacheClear_UnknownName(t *testing.T) {
	d := &Deps{}
	r := chi.NewRouter()
	d.mountCache(r)

	req := httptest.NewRequest(http.MethodPost, "/cache/t-does-not-exist/clear", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "not_found" {
		t.Errorf("error.code: want not_found, got %q", env.Error.Code)
	}
}

// TestCacheRoutes_GatedByAdminAuth_Smoke pins that mounted under the
// full /api/_admin pipeline, the cache routes sit behind RequireAdmin.
// A request with no Authorization header must NOT reach the handler —
// the middleware rejects it first (401).
//
// We mount via the same chi Group structure as adminapi.Mount but
// without the auth-related setup (no auth handlers, no Pool) — only
// the RequireAdmin middleware + mountCache, to keep this test focused
// on "auth gates the cache surface".
func TestCacheRoutes_GatedByAdminAuth_Smoke(t *testing.T) {
	d := &Deps{}
	r := chi.NewRouter()
	r.Route("/api/_admin", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(RequireAdmin)
			d.mountCache(r)
		})
	})

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/_admin/cache"},
		{http.MethodPost, "/api/_admin/cache/anything/clear"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without admin auth: want 401, got %d body=%s",
				tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// TestShapeCacheStats_RoundingAndZeroDiv exercises the helper directly:
//   - zero-request cache → hit_rate_pct == 0.0 (no NaN leak)
//   - mixed → rounded to one decimal place
//   - all-hits → 100.0
//   - all-misses → 0.0
func TestShapeCacheStats_RoundingAndZeroDiv(t *testing.T) {
	cases := []struct {
		name string
		in   cache.Stats
		want float64
	}{
		{"zero requests", cache.Stats{}, 0.0},
		{"all hits", cache.Stats{Hits: 10, Misses: 0}, 100.0},
		{"all misses", cache.Stats{Hits: 0, Misses: 7}, 0.0},
		{"3/4", cache.Stats{Hits: 3, Misses: 1}, 75.0},
		{"two-thirds rounds to 66.7", cache.Stats{Hits: 2, Misses: 1}, 66.7},
		{"one-third rounds to 33.3", cache.Stats{Hits: 1, Misses: 2}, 33.3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shapeCacheStats(tc.in)
			if got.HitRatePct != tc.want {
				t.Errorf("hit_rate_pct: want %v, got %v", tc.want, got.HitRatePct)
			}
		})
	}
}
