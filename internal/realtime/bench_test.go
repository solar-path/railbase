package realtime

// v1.7.11 — docs/17 #52 latency benchmark. The docs/17 acceptance
// criterion is "events delivered in < 100ms (single-node)" — these
// benchmarks measure publish→deliver latency in the local broker.
//
// Run with `go test -bench=. -benchmem -run=^$ ./internal/realtime/`.
// Per-event latency is reported via b.ReportMetric so `go test` output
// includes the actual p50/p99 you can compare against the 100ms gate.

import (
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/eventbus"
)

// newBenchBroker spins up a broker + bus identical to the production
// wiring. Cleanup via b.Cleanup.
func newBenchBroker(b *testing.B) (*eventbus.Bus, *Broker) {
	b.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	br := NewBroker(bus, log)
	br.Start()
	b.Cleanup(func() {
		br.Stop()
		bus.Close()
	})
	return bus, br
}

// BenchmarkPublishToDeliver measures end-to-end latency from Publish
// to event-received on a single subscriber's queue. This is the v1
// SHIP gate for docs/17 #52 ("< 100ms single-node").
func BenchmarkPublishToDeliver(b *testing.B) {
	bus, broker := newBenchBroker(b)
	sub := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	defer broker.Unsubscribe(sub.ID)

	// Drain any initial frames the broker emits (subscribed-frame
	// pattern from v1.3.0).
	drain := func() {
		for {
			select {
			case <-sub.Queue():
			default:
				return
			}
		}
	}
	drain()

	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		Publish(bus, RecordEvent{
			Collection: "posts",
			Verb:       VerbCreate,
			ID:         "id-1",
		})
		select {
		case <-sub.Queue():
			latencies = append(latencies, time.Since(t0))
		case <-time.After(500 * time.Millisecond):
			b.Fatalf("publish→deliver exceeded 500ms (iter %d)", i)
		}
	}
	b.StopTimer()

	// Report p50/p99 so the benchmark output makes the SLA visible.
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[len(latencies)*50/100]
		p99 := latencies[len(latencies)*99/100]
		b.ReportMetric(float64(p50.Microseconds()), "p50_µs")
		b.ReportMetric(float64(p99.Microseconds()), "p99_µs")
	}
}

// BenchmarkPublishToDeliver_FanOut_100 / _1k / _10k cover docs/17 #169:
// "10k concurrent subscribers, 100 events/sec — no degradation".
// We measure per-publish latency at three fan-out levels so the scaling
// curve is visible in benchmark output. The expectation is roughly
// linear in subscriber count (each subscriber gets one channel-send per
// matching event), so 10k → ~100× the 100-subscriber number.
//
// Note: we use `subs[0]` as the latency proxy (the broker dispatches
// in subscription-order; the first sub is the worst-case-latency
// position because it's woken up first but the goroutine has to enqueue
// to all others before the test loop fires the next publish).

// BenchmarkPublishToDeliver_FanOut measures latency when 100
// subscribers all match the same pattern. The fan-out happens in a
// single broker goroutine; we want to confirm the dispatch cost
// scales linearly (not super-linearly) with subscriber count.
func BenchmarkPublishToDeliver_FanOut(b *testing.B) { benchFanOut(b, 100) }

// BenchmarkPublishToDeliver_FanOut_1k — docs/17 #169 intermediate
// data point. Caught by `go test -bench=FanOut_1k`.
func BenchmarkPublishToDeliver_FanOut_1k(b *testing.B) { benchFanOut(b, 1000) }

// BenchmarkPublishToDeliver_FanOut_10k — docs/17 #169 v1 SHIP gate.
// Caught by `go test -bench=FanOut_10k -benchtime=3s`. Costs ~3-5s
// per iteration (single-event fan-out across 10k channels), so cap the
// outer iteration count via `-benchtime=Nx` when running locally.
func BenchmarkPublishToDeliver_FanOut_10k(b *testing.B) { benchFanOut(b, 10000) }

// benchFanOut is the shared driver for the three fan-out benchmarks
// (100 / 1k / 10k subscribers). It measures **last-subscriber latency**
// — the time from Publish() until every subscriber has received the
// event. That's the metric that matches the docs/17 #169 acceptance
// gate ("no degradation"); it's also necessary for correctness because
// returning from the benchmark with in-flight dispatches racing the
// per-Subscription Unsubscribe close-channel path panics on send.
//
// Each iteration:
//  1. Publish one event.
//  2. Block on every subscriber's queue (with a per-event deadline) so
//     dispatch is fully drained before the next publish.
//  3. Record total wall time as the iteration's latency.
//
// Setup pre-creates `fanOut` subscriptions matching `posts/*` and drains
// any initial subscribed-frame. Teardown happens via b.Cleanup (Stop +
// Close) which fires AFTER the b.N loop returns; the drain-per-iteration
// invariant ensures no fanOut goroutine is still iterating subs when
// Unsubscribe begins closing channels.
func benchFanOut(b *testing.B, fanOut int) {
	bus, broker := newBenchBroker(b)
	subs := make([]*Subscription, fanOut)
	for i := range subs {
		subs[i] = broker.Subscribe([]string{"posts/*"}, "u/1", "")
	}
	// Drain initial frames.
	for _, s := range subs {
		drainSubscriber(s)
	}

	// Per-event deadline. 10k subs over channels takes a few ms even
	// on a fast box; cap at 1s so a stuck dispatcher fails fast.
	const perEventDeadline = 1 * time.Second

	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		Publish(bus, RecordEvent{
			Collection: "posts",
			Verb:       VerbCreate,
			ID:         "id-1",
		})
		// Drain every subscriber. This serializes the iteration
		// against the broker's fanOut goroutine — when the loop exits,
		// dispatch is guaranteed complete.
		deadline := time.After(perEventDeadline)
		for j := 0; j < fanOut; j++ {
			select {
			case <-subs[j].Queue():
			case <-deadline:
				b.Fatalf("fan-out %d publish→deliver exceeded %v (iter %d, sub %d/%d)",
					fanOut, perEventDeadline, i, j, fanOut)
			}
		}
		latencies = append(latencies, time.Since(t0))
	}
	b.StopTimer()

	// Explicitly unsubscribe BEFORE b.Cleanup (which fires Stop + Close)
	// so the broker doesn't race on still-registered channels. With
	// the drain-per-iteration invariant above, no fanOut goroutine
	// should be holding a reference at this point.
	for _, s := range subs {
		broker.Unsubscribe(s.ID)
	}

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[len(latencies)*50/100]
		p99 := latencies[len(latencies)*99/100]
		b.ReportMetric(float64(p50.Microseconds()), "p50_µs")
		b.ReportMetric(float64(p99.Microseconds()), "p99_µs")
		// Per-subscriber-µs gives a "scales linearly" check across
		// the three fan-out levels. If 10k is significantly worse per
		// sub than 100, the dispatcher needs work.
		b.ReportMetric(float64(p50.Microseconds())/float64(fanOut), "p50_µs_per_sub")
	}
}

// drainSubscriber non-blockingly pops every queued event off s. Used
// to clear the initial subscribed-frame before a benchmark loop.
func drainSubscriber(s *Subscription) {
	for {
		select {
		case <-s.Queue():
		default:
			return
		}
	}
}

// TestPublishToDeliver_Under100ms is the docs/17 #52 acceptance test:
// running 1000 publishes, p99 latency must be < 100ms. This guards
// against silent regressions; the benchmark gives nuanced numbers.
//
// NOT a Benchmark so it runs as part of the normal test sweep.
func TestPublishToDeliver_Under100ms(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency test in -short mode")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	broker := NewBroker(bus, log)
	broker.Start()
	defer func() { broker.Stop(); bus.Close() }()

	sub := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	defer broker.Unsubscribe(sub.ID)
	// Drain initial frames.
	for {
		select {
		case <-sub.Queue():
		default:
			goto seeded
		}
	}
seeded:

	const N = 1000
	latencies := make([]time.Duration, 0, N)
	for i := 0; i < N; i++ {
		t0 := time.Now()
		Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "x"})
		select {
		case <-sub.Queue():
			latencies = append(latencies, time.Since(t0))
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("publish→deliver exceeded 500ms (iter %d)", i)
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99 := latencies[N*99/100]
	if p99 > 100*time.Millisecond {
		t.Errorf("p99 latency = %v, want < 100ms (docs/17 #52)", p99)
	}
	t.Logf("publish→deliver: p50=%v p99=%v over N=%d",
		latencies[N*50/100], p99, N)
}
