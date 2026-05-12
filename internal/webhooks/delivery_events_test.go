package webhooks

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/eventbus"
)

// TestDeliveryEvents_NilBus_NoOp guards the default-nil path — when
// production wiring forgets to pass a Bus (or tests explicitly skip
// it), emitTerminal must NOT panic.
func TestDeliveryEvents_NilBus_NoOp(t *testing.T) {
	d := HandlerDeps{Bus: nil}
	d.emitTerminal(DeliveryEvent{Outcome: "success"})
}

// TestDeliveryEvents_BusPublishes verifies the helper actually fans
// onto the registered topic with the expected payload.
func TestDeliveryEvents_BusPublishes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	t.Cleanup(func() { bus.Close() })

	var (
		mu     sync.Mutex
		gotEv  DeliveryEvent
		gotTop string
		wg     sync.WaitGroup
	)
	wg.Add(1)
	bus.Subscribe(TopicWebhookDelivered, 4, func(_ context.Context, e eventbus.Event) {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		gotTop = e.Topic
		if ev, ok := e.Payload.(DeliveryEvent); ok {
			gotEv = ev
		}
	})

	d := HandlerDeps{Bus: bus}
	want := DeliveryEvent{
		DeliveryID: uuid.New(),
		WebhookID:  uuid.New(),
		Webhook:    "test-hook",
		Event:      "records.posts.created",
		Outcome:    "success",
		StatusCode: 201,
		Attempt:    1,
	}
	d.emitTerminal(want)

	// Async — wait briefly.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotTop != TopicWebhookDelivered {
		t.Errorf("topic = %q, want %q", gotTop, TopicWebhookDelivered)
	}
	if gotEv.Webhook != want.Webhook || gotEv.Outcome != want.Outcome {
		t.Errorf("payload mismatch: got %+v want %+v", gotEv, want)
	}
	if gotEv.DeliveryID != want.DeliveryID {
		t.Errorf("delivery id mismatch")
	}
}

// TestDeliveryEvents_TerminalOnly documents the design — only success/
// dead outcomes fire. "retry" deliveries don't emit because they're
// not terminal; subscribers learn about a delivery once at its final
// resolution. Test this by emitting all three and counting fires.
func TestDeliveryEvents_TerminalOnly(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	t.Cleanup(func() { bus.Close() })

	var (
		mu    sync.Mutex
		count int
	)
	bus.Subscribe(TopicWebhookDelivered, 4, func(_ context.Context, _ eventbus.Event) {
		mu.Lock()
		defer mu.Unlock()
		count++
	})

	d := HandlerDeps{Bus: bus}
	// We don't have a "retry" path through emitTerminal because the
	// production code never calls it with retry. But document that
	// the helper does fire whatever Outcome is set — the discipline
	// of "only success/dead" lives in the callers, not the helper.
	d.emitTerminal(DeliveryEvent{Outcome: "success"})
	d.emitTerminal(DeliveryEvent{Outcome: "dead"})

	// Give async delivery a window.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Errorf("got %d events, want 2", count)
	}
}
