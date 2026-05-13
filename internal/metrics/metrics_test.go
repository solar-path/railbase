package metrics

// Unit tests for the in-process metric registry.
//
// Runs under `-race -count=1` per the package's contract. We exercise
// counter concurrency (Inc + Add from many goroutines must converge to
// the right total), histogram percentile math (known-quantile inputs
// land in the expected bucket), Snapshot atomicity / shape, and the
// HTTPMiddleware status bucketing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/clock"
)

func TestCounter_IncAdd_Concurrent(t *testing.T) {
	r := New(nil)
	c := r.Counter("x")

	const goroutines = 64
	const incsPer = 1_000
	const addPer = 500
	const addAmt = 7

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < incsPer; j++ {
				c.Inc()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < addPer; j++ {
				c.Add(addAmt)
			}
		}()
	}
	wg.Wait()

	want := uint64(goroutines*incsPer + goroutines*addPer*addAmt)
	if got := c.Value(); got != want {
		t.Fatalf("counter value: want %d, got %d", want, got)
	}
	// Same instance on second Counter() call — sync.Map LoadOrStore
	// must hand back the existing pointer.
	if c2 := r.Counter("x"); c2 != c {
		t.Fatalf("Counter(name) returned a fresh instance on second call")
	}
}

func TestCounter_NilSafe(t *testing.T) {
	var c *Counter // explicit nil
	c.Inc()
	c.Add(5)
	if v := c.Value(); v != 0 {
		t.Fatalf("nil counter Value: want 0, got %d", v)
	}
}

func TestHistogram_PercentileBuckets(t *testing.T) {
	r := New(nil)
	h := r.Histogram("lat")

	// Feed 100 observations: 90 at 5ms, 9 at 200ms, 1 at 2s.
	// Expected bucketing (boundaries: 1, 10, 100, 500, 1000, 5000, 10000 ms):
	//   - 5ms       → bucket index 1 (≤ 10ms)
	//   - 200ms     → bucket index 3 (≤ 500ms)
	//   - 2s        → bucket index 5 (≤ 5s)
	//
	// Percentile uses cumulative-count → first bucket whose cum ≥ p*total.
	//   - p50 → target 50; cum at bucket 1 = 90 → returns upper of bucket 1 (10ms)
	//   - p95 → target 95; cum at bucket 1 = 90, bucket 3 = 99 → returns upper of bucket 3 (500ms)
	//   - p99 → target 99; cum at bucket 3 = 99 → returns upper of bucket 3 (500ms)
	for i := 0; i < 90; i++ {
		h.Observe(5 * time.Millisecond)
	}
	for i := 0; i < 9; i++ {
		h.Observe(200 * time.Millisecond)
	}
	h.Observe(2 * time.Second)

	if got := h.Count(); got != 100 {
		t.Fatalf("histogram count: want 100, got %d", got)
	}
	if got, want := h.Percentile(0.50), 10*time.Millisecond; got != want {
		t.Errorf("p50: want %v, got %v", want, got)
	}
	if got, want := h.Percentile(0.95), 500*time.Millisecond; got != want {
		t.Errorf("p95: want %v, got %v", want, got)
	}
	if got, want := h.Percentile(0.99), 500*time.Millisecond; got != want {
		t.Errorf("p99: want %v, got %v", want, got)
	}
}

func TestHistogram_OverflowBucket(t *testing.T) {
	r := New(nil)
	h := r.Histogram("slow")
	h.Observe(30 * time.Second) // larger than max bound (10s)

	// p99 should land in the overflow band — reports just above 10s.
	got := h.Percentile(0.99)
	if got <= 10*time.Second {
		t.Fatalf("overflow p99: want > 10s, got %v", got)
	}
}

func TestHistogram_EmptyZero(t *testing.T) {
	r := New(nil)
	h := r.Histogram("empty")
	if got := h.Percentile(0.95); got != 0 {
		t.Fatalf("empty histogram p95: want 0, got %v", got)
	}
}

func TestHistogram_NegativeDurationClamped(t *testing.T) {
	r := New(nil)
	h := r.Histogram("neg")
	// Monotonic-clock skew can produce a negative duration in some
	// pathological cases; we clamp to 0 so the histogram stays valid.
	h.Observe(-5 * time.Millisecond)
	if got := h.Count(); got != 1 {
		t.Fatalf("negative observation: want count 1, got %d", got)
	}
	// 0 lands in the first bucket (≤ 1ms), so p50 returns 1ms.
	if got, want := h.Percentile(0.50), 1*time.Millisecond; got != want {
		t.Errorf("p50 after negative: want %v, got %v", want, got)
	}
}

func TestSnapshot_DeterministicClock(t *testing.T) {
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	r := New(clock.Fixed(fixed))
	r.Counter("a").Inc()
	r.Counter("b").Add(42)
	r.Histogram("lat").Observe(20 * time.Millisecond)

	snap := r.Snapshot()

	if !snap.SnapshotAt.Equal(fixed) {
		t.Errorf("snapshot_at: want %v, got %v", fixed, snap.SnapshotAt)
	}
	if snap.Counters["a"] != 1 {
		t.Errorf("counter a: want 1, got %d", snap.Counters["a"])
	}
	if snap.Counters["b"] != 42 {
		t.Errorf("counter b: want 42, got %d", snap.Counters["b"])
	}
	if h, ok := snap.Histograms["lat"]; !ok {
		t.Fatalf("histogram lat missing from snapshot; got %+v", snap.Histograms)
	} else if h.Count != 1 {
		t.Errorf("histogram lat count: want 1, got %d", h.Count)
	}

	// Snapshot must JSON-marshal cleanly — the endpoint serialises this
	// directly. p50/p95/p99 emit as integer nanoseconds.
	blob, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatalf("unmarshal snapshot: %v body=%s", err, string(blob))
	}
	if _, ok := round["snapshot_at"]; !ok {
		t.Errorf("snapshot_at key missing; body=%s", string(blob))
	}
	if _, ok := round["counters"]; !ok {
		t.Errorf("counters key missing; body=%s", string(blob))
	}
	if _, ok := round["histograms"]; !ok {
		t.Errorf("histograms key missing; body=%s", string(blob))
	}
}

func TestSnapshot_ConcurrentSafe(t *testing.T) {
	// Smoke test: writers + reader race on the registry. -race catches
	// any unsynchronized access; the test asserts no panic + reasonable
	// final counts.
	r := New(nil)
	c := r.Counter("c")
	h := r.Histogram("h")

	const writers = 16
	const iters = 1_000

	stop := make(chan struct{})
	var writersWG sync.WaitGroup
	writersWG.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer writersWG.Done()
			for j := 0; j < iters; j++ {
				c.Inc()
				h.Observe(time.Duration(j) * time.Microsecond)
			}
		}()
	}
	// Reader goroutine — runs continuously until the writers finish.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.Snapshot()
			}
		}
	}()
	writersWG.Wait()
	close(stop)
	<-readerDone

	if got, want := c.Value(), uint64(writers*iters); got != want {
		t.Errorf("final counter: want %d, got %d", want, got)
	}
}

func TestHTTPMiddleware_StatusBuckets(t *testing.T) {
	r := New(nil)
	mw := HTTPMiddleware(r)

	cases := []struct {
		path   string
		status int
	}{
		{"/ok", 200},
		{"/created", 201},
		{"/redirect", 302},
		{"/bad", 400},
		{"/notfound", 404},
		{"/boom", 500},
		{"/oops", 503},
	}
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Pull the desired status from the path so we don't need a
		// router; deterministic + side-effect-free.
		for _, c := range cases {
			if req.URL.Path == c.path {
				w.WriteHeader(c.status)
				return
			}
		}
		w.WriteHeader(200)
	}))

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := r.Snapshot()
	if snap.Counters["http.requests_total"] != uint64(len(cases)) {
		t.Errorf("requests_total: want %d, got %d", len(cases), snap.Counters["http.requests_total"])
	}
	if snap.Counters["http.errors_4xx_total"] != 2 {
		t.Errorf("errors_4xx_total: want 2, got %d", snap.Counters["http.errors_4xx_total"])
	}
	if snap.Counters["http.errors_5xx_total"] != 2 {
		t.Errorf("errors_5xx_total: want 2, got %d", snap.Counters["http.errors_5xx_total"])
	}
	if h, ok := snap.Histograms["http.latency"]; !ok || h.Count != uint64(len(cases)) {
		t.Errorf("http.latency: want count %d, got %+v", len(cases), h)
	}
}

func TestHTTPMiddleware_NilRegistry_PassThrough(t *testing.T) {
	mw := HTTPMiddleware(nil)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatalf("nil-registry middleware did not call next handler")
	}
	if rec.Code != 200 {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
}
