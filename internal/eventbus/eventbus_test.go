package eventbus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishSubscribe_ExactMatch(t *testing.T) {
	b := New(nil)
	defer b.Close()

	var got atomic.Int64
	id := b.Subscribe("hello.world", 0, func(ctx context.Context, e Event) {
		if e.Topic == "hello.world" {
			got.Add(1)
		}
	})
	defer b.Unsubscribe(id)

	b.Publish(Event{Topic: "hello.world"})
	b.Publish(Event{Topic: "hello.world"})
	b.Publish(Event{Topic: "hello.other"})

	waitFor(t, func() bool { return got.Load() == 2 })
}

func TestPublishSubscribe_WildcardSingleSegment(t *testing.T) {
	b := New(nil)
	defer b.Close()

	var got atomic.Int64
	b.Subscribe("auth.*", 0, func(ctx context.Context, e Event) {
		got.Add(1)
	})

	b.Publish(Event{Topic: "auth.signin"})  // match
	b.Publish(Event{Topic: "auth.signout"}) // match
	b.Publish(Event{Topic: "auth.signin.succeeded"}) // NO match: deeper segment
	b.Publish(Event{Topic: "audit.write"})  // NO match

	waitFor(t, func() bool { return got.Load() == 2 })
}

func TestUnsubscribe(t *testing.T) {
	b := New(nil)
	defer b.Close()

	var got atomic.Int64
	id := b.Subscribe("topic", 0, func(_ context.Context, _ Event) { got.Add(1) })

	b.Publish(Event{Topic: "topic"})
	waitFor(t, func() bool { return got.Load() == 1 })

	b.Unsubscribe(id)
	b.Publish(Event{Topic: "topic"})
	time.Sleep(20 * time.Millisecond)
	if got.Load() != 1 {
		t.Errorf("expected handler to stop receiving, got=%d", got.Load())
	}
}

func TestPayload_RoundTrip(t *testing.T) {
	b := New(nil)
	defer b.Close()

	type msg struct{ N int }
	var wg sync.WaitGroup
	wg.Add(1)
	b.Subscribe("payload", 0, func(_ context.Context, e Event) {
		defer wg.Done()
		m, ok := e.Payload.(msg)
		if !ok || m.N != 42 {
			t.Errorf("payload mismatch: %#v", e.Payload)
		}
	})
	b.Publish(Event{Topic: "payload", Payload: msg{N: 42}})
	wg.Wait()
}

func TestClose_RefusesPublish(t *testing.T) {
	b := New(nil)
	var got atomic.Int64
	b.Subscribe("topic", 0, func(_ context.Context, _ Event) { got.Add(1) })
	b.Close()
	b.Publish(Event{Topic: "topic"})
	time.Sleep(20 * time.Millisecond)
	if got.Load() != 0 {
		t.Errorf("publish after Close delivered %d events", got.Load())
	}
}

func TestSlowSubscriber_DropsRatherThanBlocks(t *testing.T) {
	b := New(nil)
	defer b.Close()

	block := make(chan struct{})
	defer close(block)
	var processed atomic.Int64
	b.Subscribe("slow", 1, func(_ context.Context, _ Event) {
		<-block
		processed.Add(1)
	})

	// First publish parks in the queue (buf=1). Subsequent publishes
	// would block a non-async bus; we expect them to drop instead.
	for i := 0; i < 100; i++ {
		b.Publish(Event{Topic: "slow"})
	}
	// Without dropping, this test would hang forever.
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for condition")
}
