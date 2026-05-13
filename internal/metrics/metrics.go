// Package metrics is the lightweight in-process metric registry that
// backs the /api/_admin/metrics endpoint.
//
// Design rules baked in:
//
//   - **Pure stdlib.** No Prometheus dependency. Counter is a plain
//     atomic.Uint64; Histogram is a fixed-bucket atomic.Uint64 array.
//     Snapshot returns a JSON-marshalable point-in-time read. The
//     Prometheus-style /metrics target in docs/14 is aspirational — we
//     cover the surface the admin UI needs today (HTTP rps, error rate,
//     p95 latency, hook invocations) without pulling in a 100 kLoC dep.
//
//   - **Lock-free hot path.** Counter.Inc / Counter.Add and
//     Histogram.Observe each compile down to a single
//     atomic.AddUint64. The map lookup (Counter / Histogram) is
//     amortised behind a sync.Map; once warm, the steady-state
//     instruments are looked up via the map's read-mostly cache.
//
//   - **Opt-in publishing.** Every caller (HTTP middleware, hooks
//     dispatcher) nil-guards the registry. Pass nil → nothing fires;
//     production wires a single *Registry through pkg/railbase/app.go.
//
//   - **Fixed histogram buckets.** 1ms, 10ms, 100ms, 500ms, 1s, 5s,
//     10s. Covers the HTTP-latency band the admin dashboard cares
//     about; tail beyond 10s lands in the +Inf bucket. Percentile()
//     does a linear scan over 8 atomic loads — cheap.
//
//   - **Time source.** Snapshot timestamps go through clock.Clock so
//     tests can pin the wall-clock; the metric values themselves
//     don't depend on time (counters monotonic, histogram observes
//     a duration the caller measured).
//
// What's INTENTIONALLY out of scope for v1:
//
//   - DB query rate / slow query count. Wrapping every pgx call to
//     observe a histogram is too invasive for the metric surface the
//     admin dashboard needs today. Documented in docs/14 as a gap.
//
//   - Labels / dimensions. The registry uses flat string keys; if
//     callers want a labelled counter they encode the label in the
//     name ("http.errors_4xx_total"). Cardinality stays bounded.
//
//   - Persistence. Process-restart resets every counter; the admin
//     UI's rolling buffer hook reconciles via two-sample deltas so a
//     restart just shows a flatline until the next sample.
package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/railbase/railbase/internal/clock"
)

// Registry is the process-wide metric container. Construct exactly one
// via New() in pkg/railbase/app.go and pass into every subsystem that
// publishes (HTTP middleware, hooks dispatcher, …). Safe for concurrent
// use; counter and histogram lookups go through a sync.Map.
type Registry struct {
	clock      clock.Clock
	counters   sync.Map // map[string]*Counter
	histograms sync.Map // map[string]*Histogram
}

// New constructs a fresh Registry with the given clock source. Pass
// clock.Real() in production; tests inject clock.Fixed(...) so
// Snapshot timestamps are deterministic.
func New(c clock.Clock) *Registry {
	if c == nil {
		c = realClock{}
	}
	return &Registry{clock: c}
}

// realClock is the fallback when New is called with a nil clock —
// keeps the constructor allocation-free for the common "pass the app's
// clock" path while still surviving a nil-arg test.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Counter returns the named counter, creating it on first reference.
// Safe for concurrent use; the first caller that wins the
// LoadOrStore race installs the instance, every subsequent caller
// gets the same pointer.
//
// Lookup cost in steady state: one sync.Map.Load — ~10ns on modern
// hardware. Hot-path callers can cache the pointer at construction
// time (the HTTP middleware does so).
func (r *Registry) Counter(name string) *Counter {
	if c, ok := r.counters.Load(name); ok {
		return c.(*Counter)
	}
	fresh := &Counter{name: name}
	actual, _ := r.counters.LoadOrStore(name, fresh)
	return actual.(*Counter)
}

// Histogram returns the named histogram, creating it on first
// reference. Buckets are fixed (see histBoundsNs); the caller does not
// pick the bucket layout.
func (r *Registry) Histogram(name string) *Histogram {
	if h, ok := r.histograms.Load(name); ok {
		return h.(*Histogram)
	}
	fresh := &Histogram{name: name}
	actual, _ := r.histograms.LoadOrStore(name, fresh)
	return actual.(*Histogram)
}

// Counter is a monotonic uint64. Inc / Add are lock-free; Value
// returns the current count via atomic load.
type Counter struct {
	name string
	v    atomic.Uint64
}

// Inc bumps the counter by 1.
func (c *Counter) Inc() {
	if c == nil {
		return
	}
	c.v.Add(1)
}

// Add bumps the counter by n. Pass 0 → no-op (still emits an atomic
// op; callers typically guard at the call site).
func (c *Counter) Add(n uint64) {
	if c == nil {
		return
	}
	c.v.Add(n)
}

// Value returns the current count.
func (c *Counter) Value() uint64 {
	if c == nil {
		return 0
	}
	return c.v.Load()
}

// histBoundsNs is the fixed bucket layout in nanoseconds. Each entry
// is the UPPER bound (inclusive) of bucket i; bucket len(histBoundsNs)
// is the +Inf bucket for anything slower than 10s. Must stay sorted
// ascending — Percentile assumes monotonicity.
var histBoundsNs = [...]int64{
	int64(1 * time.Millisecond),
	int64(10 * time.Millisecond),
	int64(100 * time.Millisecond),
	int64(500 * time.Millisecond),
	int64(1 * time.Second),
	int64(5 * time.Second),
	int64(10 * time.Second),
}

// Histogram tracks observation counts in fixed buckets. Observe is
// lock-free (one atomic.Add per call); Percentile does len(buckets)+1
// atomic loads and a linear scan — cheap enough to call on every
// /metrics request.
type Histogram struct {
	name string
	// buckets[i] = count of observations with d <= histBoundsNs[i]
	// AND d > histBoundsNs[i-1]. buckets[len(histBoundsNs)] = +Inf
	// bucket (count of observations > histBoundsNs[last]).
	buckets [len(histBoundsNs) + 1]atomic.Uint64
	total   atomic.Uint64
	// sumNs is a running sum of all observations in nanoseconds.
	// Not exposed today but kept so a future revision can return an
	// average / mean without re-counting. atomic.Uint64 because Go
	// doesn't have an atomic.Int64 (the ns values are always positive
	// — we'd assert at Observe time but a negative duration is a
	// caller bug, not something to handle).
	sumNs atomic.Uint64
}

// Observe records one sample. Durations < 0 are clamped to 0 (the
// caller computed elapsed = time.Since(start) and the clock went
// backwards; we don't want to corrupt the histogram for what is
// almost certainly a monotonic-clock skew).
func (h *Histogram) Observe(d time.Duration) {
	if h == nil {
		return
	}
	ns := int64(d)
	if ns < 0 {
		ns = 0
	}
	// Linear scan — 7 comparisons on the hot path. A binary search
	// would shave 2-3 comparisons but the constant overhead of the
	// loop bookkeeping likely eats the win at this size.
	idx := len(histBoundsNs)
	for i, b := range histBoundsNs {
		if ns <= b {
			idx = i
			break
		}
	}
	h.buckets[idx].Add(1)
	h.total.Add(1)
	h.sumNs.Add(uint64(ns))
}

// Count returns the total number of observations across all buckets.
func (h *Histogram) Count() uint64 {
	if h == nil {
		return 0
	}
	return h.total.Load()
}

// Percentile computes the p-th percentile (0 <= p <= 1) from the
// fixed-bucket counts. Returns 0 when no observations have been
// recorded. The result is the UPPER bound of the bucket that contains
// the percentile — i.e. a worst-case estimate within bucket resolution.
//
// This is the standard bucket-bound approach used by Prometheus
// histograms when no linear interpolation is requested; precision is
// bound by the bucket layout. For Railbase's admin dashboard ("is p95
// degrading over the last 2 min?") that's more than enough.
func (h *Histogram) Percentile(p float64) time.Duration {
	if h == nil {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	total := h.total.Load()
	if total == 0 {
		return 0
	}
	target := uint64(float64(total) * p)
	if target == 0 {
		target = 1
	}
	var cum uint64
	for i := range h.buckets {
		cum += h.buckets[i].Load()
		if cum >= target {
			if i < len(histBoundsNs) {
				return time.Duration(histBoundsNs[i])
			}
			// Overflow bucket — cap reporting at the largest
			// finite bound + 1ns so consumers can distinguish
			// "in the 10s bucket" from "above 10s".
			return time.Duration(histBoundsNs[len(histBoundsNs)-1]) + time.Nanosecond
		}
	}
	// Unreachable given total > 0 and cum monotonic, but stay defensive.
	return 0
}

// Snapshot is the JSON-marshalable point-in-time read of every
// registered metric. The endpoint handler serialises this directly;
// the React side reshapes via the typed wrapper in admin/src/api/types.ts.
type Snapshot struct {
	SnapshotAt time.Time                `json:"snapshot_at"`
	Counters   map[string]uint64        `json:"counters"`
	Histograms map[string]HistSnapshot  `json:"histograms"`
}

// HistSnapshot trims a Histogram to the fields the dashboard cares
// about — count + p50/p95/p99. Durations are serialised as integer
// nanoseconds so the React side does the unit conversion (avoids the
// "should we round to ms here?" debate at the JSON boundary).
type HistSnapshot struct {
	Count uint64        `json:"count"`
	P50   time.Duration `json:"p50_ns"`
	P95   time.Duration `json:"p95_ns"`
	P99   time.Duration `json:"p99_ns"`
}

// Snapshot returns a consistent-across-instruments point-in-time read.
//
// "Atomicity" here is per-instrument: each Counter / Histogram is read
// via atomic loads, but the registry as a whole isn't locked. A
// concurrent Inc on counter A while we're reading counter B is fine —
// we just see the post-Inc value of A and the pre-Inc value of B if
// they happened in that order. This matches Prometheus's exposition
// model and is what the dashboard expects (rate computation tolerates
// per-counter monotonic skew).
func (r *Registry) Snapshot() Snapshot {
	out := Snapshot{
		SnapshotAt: r.clock.Now().UTC(),
		Counters:   make(map[string]uint64),
		Histograms: make(map[string]HistSnapshot),
	}
	// Collect names first so the output is sorted-deterministic; tests
	// and operators alike benefit from a stable key order on the wire
	// (JSON object key order is technically unordered but every
	// encoder we care about preserves insertion order, and we sort to
	// keep diffs readable).
	var counterNames []string
	r.counters.Range(func(k, _ any) bool {
		counterNames = append(counterNames, k.(string))
		return true
	})
	sort.Strings(counterNames)
	for _, name := range counterNames {
		c, ok := r.counters.Load(name)
		if !ok {
			continue
		}
		out.Counters[name] = c.(*Counter).Value()
	}

	var histNames []string
	r.histograms.Range(func(k, _ any) bool {
		histNames = append(histNames, k.(string))
		return true
	})
	sort.Strings(histNames)
	for _, name := range histNames {
		h, ok := r.histograms.Load(name)
		if !ok {
			continue
		}
		hh := h.(*Histogram)
		out.Histograms[name] = HistSnapshot{
			Count: hh.Count(),
			P50:   hh.Percentile(0.50),
			P95:   hh.Percentile(0.95),
			P99:   hh.Percentile(0.99),
		}
	}
	return out
}
