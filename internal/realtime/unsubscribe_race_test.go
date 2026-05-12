// v1.7.38 — regression test for the "Unsubscribe while fanOut
// in-flight" race that both the v1.7.36b agent and the v1.7.37b
// agent flagged independently.
//
// Pre-fix: Unsubscribe called `close(sub.queue)` while another
// goroutine could be inside `case sub.queue <- ev:` in
// enqueueOrDrop — a classic TOCTOU race that the race detector
// flags + can manifest as a "send on closed channel" panic in
// production.
//
// Post-fix: Unsubscribe closes a separate `done` channel; the
// queue is never closed. enqueueOrDrop selects on done so an
// in-flight send observes the teardown and drops the event
// instead of either blocking forever (queue full + no reader)
// or panicking (closed-send).
//
// The test pattern: many publishes interleaved with many
// subscribe/unsubscribe cycles. The race detector amplifies any
// stale window; a single occurrence anywhere in the run fails
// the test.

package realtime

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestBroker_UnsubscribeRace_NoCloseOnSendPanic exercises the
// happy path of the fix: concurrent publish-fanOut + Unsubscribe
// pairs must never panic. Without the done-gate, this fails
// readily under `-race -count=20` with "send on closed channel".
func TestBroker_UnsubscribeRace_NoCloseOnSendPanic(t *testing.T) {
	bus, broker := makeBroker(t)

	const publishers = 8
	const cycles = 50

	var wg sync.WaitGroup

	// Publisher goroutines: keep firing record events. Each event
	// fans out to whatever subscribers happen to be registered at
	// that moment.
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < cycles; i++ {
				Publish(bus, RecordEvent{
					Collection: "posts",
					Verb:       VerbCreate,
					ID:         strconv.Itoa(p*cycles + i),
				})
			}
		}(p)
	}

	// Subscribe/unsubscribe churn: register a sub, hold for a
	// tiny window so a fanOut iteration can pick it up, then
	// tear it down. The teardown is the racey path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < cycles; i++ {
			sub := broker.Subscribe([]string{"posts/*"}, "users/u"+strconv.Itoa(i), "")
			// Don't drain — leave the queue full so the fanOut's
			// non-blocking send hits the drop-oldest path on the
			// hot side of the race window.
			time.Sleep(100 * time.Microsecond)
			broker.Unsubscribe(sub.ID)
		}
	}()

	wg.Wait()
}

// TestBroker_UnsubscribeRace_QueueFullWhileTeardown stresses the
// specific case the agents flagged: queue full → enqueueOrDrop
// enters its drop-oldest retry loop → teardown happens mid-loop.
// We deliberately don't drain at all so the queue stays full.
func TestBroker_UnsubscribeRace_QueueFullWhileTeardown(t *testing.T) {
	bus, broker := makeBroker(t)

	for cycle := 0; cycle < 30; cycle++ {
		sub := broker.Subscribe([]string{"posts/*"}, "u", "")
		// Saturate the queue (cap 64). No drain → enqueueOrDrop
		// enters its drop-oldest path on every subsequent send.
		for i := 0; i < 80; i++ {
			Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i)})
		}
		// More publishes happening concurrently with teardown.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "concurrent-" + strconv.Itoa(i)})
			}
		}()
		go func() {
			defer wg.Done()
			// Slight delay so the publish goroutine has work in
			// flight when the teardown fires.
			time.Sleep(50 * time.Microsecond)
			broker.Unsubscribe(sub.ID)
		}()
		wg.Wait()
	}
}

// TestBroker_UnsubscribeRace_DoubleUnsubscribeIsSafe locks in the
// idempotency contract — Unsubscribe is called by both `defer
// broker.Unsubscribe(...)` AND occasionally by an explicit close
// path in some handlers, so it must not panic on the second call.
func TestBroker_UnsubscribeRace_DoubleUnsubscribeIsSafe(t *testing.T) {
	_, broker := makeBroker(t)

	sub := broker.Subscribe([]string{"posts/*"}, "u", "")
	broker.Unsubscribe(sub.ID)
	broker.Unsubscribe(sub.ID) // must not panic on closed done channel
	broker.Unsubscribe(sub.ID) // belt-and-braces
}

// TestBroker_Subscription_DoneCloses confirms the new Done()
// channel actually closes on Unsubscribe — the SSE/WS readers
// rely on this to exit their select loops.
func TestBroker_Subscription_DoneCloses(t *testing.T) {
	_, broker := makeBroker(t)

	sub := broker.Subscribe([]string{"posts/*"}, "u", "")
	select {
	case <-sub.Done():
		t.Fatal("Done() should not be closed before Unsubscribe")
	default:
	}
	broker.Unsubscribe(sub.ID)
	select {
	case <-sub.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("Done() did not close within 1s after Unsubscribe")
	}
}
