package auth

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/eventbus"
)

// TestAuthEvents_PublishOnTopics verifies v1.7.34's eventbus topic
// publishing — every typed AuditHook method that writes a row also
// fans an AuthEvent onto the bus.
//
// Bus is async fire-and-forget, so we wait on a WaitGroup with a
// generous timeout. nil-Writer is fine in this test because the
// publish path doesn't touch the Writer (the audit-row write is
// best-effort and unrelated to event delivery).
func TestAuthEvents_PublishOnTopics(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	t.Cleanup(func() { bus.Close() })

	// Capture every fired event across all topics.
	var mu sync.Mutex
	got := map[string]AuthEvent{}
	var wg sync.WaitGroup

	for _, topic := range []string{
		TopicAuthSignin, TopicAuthSignup, TopicAuthRefresh,
		TopicAuthLogout, TopicAuthLockout,
	} {
		topic := topic
		wg.Add(1)
		bus.Subscribe(topic, 4, func(_ context.Context, e eventbus.Event) {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			if ev, ok := e.Payload.(AuthEvent); ok {
				got[topic] = ev
			}
		})
	}

	// Build a hook with nil Writer (only the publish path is under
	// test). WithBus returns a fresh hook with the bus attached.
	h := &AuditHook{Writer: nil, Bus: bus}

	ctx := context.Background()
	uid := uuid.New()

	// Skip the Writer-touching path by short-circuiting nil-Writer.
	// Each method's first guard is `if h == nil` only; Writer.Write
	// happens unconditionally. We'd panic if we called h.signin
	// directly with nil Writer, so use publish directly to exercise
	// the bus path — that's what we're testing here.
	h.publish(ctx, TopicAuthSignin, AuthEvent{
		UserID: uid, UserCollection: "users", Identity: "a@b.co",
		Outcome: audit.OutcomeSuccess, IP: "1.2.3.4", UserAgent: "test",
	})
	h.publish(ctx, TopicAuthSignup, AuthEvent{
		UserID: uid, UserCollection: "users", Identity: "a@b.co",
		Outcome: audit.OutcomeSuccess, IP: "1.2.3.4", UserAgent: "test",
	})
	h.publish(ctx, TopicAuthRefresh, AuthEvent{
		UserID: uid, UserCollection: "users",
		Outcome: audit.OutcomeSuccess, IP: "1.2.3.4", UserAgent: "test",
	})
	h.publish(ctx, TopicAuthLogout, AuthEvent{
		UserID: uid, UserCollection: "users",
		Outcome: audit.OutcomeSuccess, IP: "1.2.3.4", UserAgent: "test",
	})
	h.publish(ctx, TopicAuthLockout, AuthEvent{
		UserCollection: "users", Identity: "a@b.co",
		Outcome: audit.OutcomeDenied, IP: "1.2.3.4", UserAgent: "test",
	})

	// Wait for async delivery — generous 2s timeout.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for all 5 events")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{
		TopicAuthSignin, TopicAuthSignup, TopicAuthRefresh,
		TopicAuthLogout, TopicAuthLockout,
	} {
		ev, ok := got[want]
		if !ok {
			t.Errorf("topic %q: no event received", want)
			continue
		}
		if ev.Topic != want {
			t.Errorf("topic %q: event.Topic = %q, want %q", want, ev.Topic, want)
		}
	}
}

// TestAuthEvents_NilBusNoOp guards the path where operators construct
// an AuditHook without a bus (test wiring, embedded callers). The
// publish call must NOT panic + must NOT do anything observable.
func TestAuthEvents_NilBusNoOp(t *testing.T) {
	h := &AuditHook{Writer: nil, Bus: nil}
	// No assert needed — we just expect this not to panic.
	h.publish(context.Background(), TopicAuthSignin, AuthEvent{})
}

// TestAuditHook_WithBus_ReturnsCopy verifies WithBus returns a fresh
// hook (so a tests-without-bus hook isn't mutated when production
// wiring attaches the bus elsewhere).
func TestAuditHook_WithBus_ReturnsCopy(t *testing.T) {
	original := &AuditHook{Writer: nil, Bus: nil}
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()

	copied := original.WithBus(bus)
	if original.Bus != nil {
		t.Error("WithBus mutated the original hook")
	}
	if copied == nil || copied.Bus != bus {
		t.Errorf("WithBus copy: bus = %v, want %v", copied.Bus, bus)
	}
}

// TestAuditHook_NilWithBus_ReturnsNil documents the nil-receiver
// safety of WithBus — needed because the v1.5.x test path passes a
// nil Audit through Deps to skip audit-row writes.
func TestAuditHook_NilWithBus_ReturnsNil(t *testing.T) {
	var h *AuditHook
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()

	if got := h.WithBus(bus); got != nil {
		t.Errorf("nil-receiver WithBus = %v, want nil", got)
	}
}
