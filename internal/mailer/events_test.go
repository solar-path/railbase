package mailer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/eventbus"
)

// countingDriver wraps an inner driver and records whether Send was
// invoked. Lets us assert that a Reject veto truly short-circuits
// before the transport layer.
type countingDriver struct {
	inner  Driver
	calls  atomic.Int32
	err    error
	withMu sync.Mutex
	last   Message
}

func (d *countingDriver) Name() string { return "counting" }

func (d *countingDriver) Send(ctx context.Context, msg Message) error {
	d.calls.Add(1)
	d.withMu.Lock()
	d.last = msg
	d.withMu.Unlock()
	if d.err != nil {
		return d.err
	}
	if d.inner != nil {
		return d.inner.Send(ctx, msg)
	}
	return nil
}

// newTestMailer constructs a Mailer wired to a fresh bus + counting
// driver, suitable for the sub-tests below.
func newTestMailer(t *testing.T, driverErr error) (*Mailer, *eventbus.Bus, *countingDriver) {
	t.Helper()
	bus := eventbus.New(nil)
	t.Cleanup(bus.Close)
	drv := &countingDriver{inner: NewConsoleDriver(&bytes.Buffer{}), err: driverErr}
	m := New(Options{
		Driver:      drv,
		Bus:         bus,
		DefaultFrom: Address{Email: "from@example.com"},
	})
	return m, bus, drv
}

// validMessage returns a minimally-acceptable Message for SendDirect.
func validMessage() Message {
	return Message{
		To:      []Address{{Email: "to@example.com"}},
		Subject: "Hello",
		HTML:    "<p>Hi</p>",
	}
}

// TestMailerEvents_BeforeAfterFires asserts both hooks publish in
// order on a successful send, with the after-event observable via the
// async bus path.
func TestMailerEvents_BeforeAfterFires(t *testing.T) {
	m, bus, drv := newTestMailer(t, nil)

	var beforeAt, afterAt time.Time
	var beforeSubject string
	var afterErr error
	afterCh := make(chan struct{}, 1)

	bus.SubscribeSync(TopicBeforeSend, func(_ context.Context, e eventbus.Event) {
		ev := e.Payload.(*MailerBeforeSendEvent)
		beforeAt = time.Now()
		beforeSubject = ev.Message.Subject
	})
	bus.Subscribe(TopicAfterSend, 0, func(_ context.Context, e eventbus.Event) {
		ev := e.Payload.(MailerAfterSendEvent)
		afterAt = time.Now()
		afterErr = ev.Err
		afterCh <- struct{}{}
	})

	if err := m.SendDirect(context.Background(), validMessage()); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case <-afterCh:
	case <-time.After(2 * time.Second):
		t.Fatal("after_send did not fire within 2s")
	}

	if beforeSubject != "Hello" {
		t.Errorf("before subject = %q, want Hello", beforeSubject)
	}
	if afterErr != nil {
		t.Errorf("after err = %v, want nil", afterErr)
	}
	if !beforeAt.Before(afterAt) && !beforeAt.Equal(afterAt) {
		t.Errorf("before fired AFTER after: before=%v after=%v", beforeAt, afterAt)
	}
	if drv.calls.Load() != 1 {
		t.Errorf("driver calls = %d, want 1", drv.calls.Load())
	}
}

// TestMailerEvents_BeforeMutates_From asserts the driver receives the
// mutated message — proving the sync-hook contract (pointer mutation
// flows through to the transport).
func TestMailerEvents_BeforeMutates_From(t *testing.T) {
	m, bus, drv := newTestMailer(t, nil)

	bus.SubscribeSync(TopicBeforeSend, func(_ context.Context, e eventbus.Event) {
		ev := e.Payload.(*MailerBeforeSendEvent)
		ev.Message.From = Address{Email: "rewritten@example.com", Name: "Bot"}
		if ev.Message.Headers == nil {
			ev.Message.Headers = map[string]string{}
		}
		ev.Message.Headers["X-Tenant"] = "acme"
	})

	if err := m.SendDirect(context.Background(), validMessage()); err != nil {
		t.Fatalf("send: %v", err)
	}

	drv.withMu.Lock()
	last := drv.last
	drv.withMu.Unlock()

	if last.From.Email != "rewritten@example.com" {
		t.Errorf("driver From = %q, want rewritten@example.com (hook mutation lost)", last.From.Email)
	}
	if last.Headers["X-Tenant"] != "acme" {
		t.Errorf("driver X-Tenant = %q, want acme", last.Headers["X-Tenant"])
	}
}

// TestMailerEvents_BeforeRejects_DriverNotCalled asserts Reject=true
// returns an error AND skips the driver entirely.
func TestMailerEvents_BeforeRejects_DriverNotCalled(t *testing.T) {
	m, bus, drv := newTestMailer(t, nil)

	bus.SubscribeSync(TopicBeforeSend, func(_ context.Context, e eventbus.Event) {
		ev := e.Payload.(*MailerBeforeSendEvent)
		ev.Reject = true
		ev.Reason = "tenant over-quota"
	})

	err := m.SendDirect(context.Background(), validMessage())
	if err == nil {
		t.Fatal("expected rejection error, got nil")
	}
	if !strings.Contains(err.Error(), "tenant over-quota") {
		t.Errorf("error %q does not surface reason", err)
	}
	if !strings.Contains(err.Error(), "before-send hook") {
		t.Errorf("error %q missing hook marker", err)
	}
	if got := drv.calls.Load(); got != 0 {
		t.Errorf("driver called %d times after reject; want 0", got)
	}
}

// TestMailerEvents_AfterFires_OnDriverError asserts the after-event
// still fires (with Err populated) when the transport fails. Observers
// rely on this for delivery-failure telemetry.
func TestMailerEvents_AfterFires_OnDriverError(t *testing.T) {
	driverErr := errors.New("simulated SMTP 451")
	m, bus, _ := newTestMailer(t, driverErr)

	afterCh := make(chan MailerAfterSendEvent, 1)
	bus.Subscribe(TopicAfterSend, 0, func(_ context.Context, e eventbus.Event) {
		afterCh <- e.Payload.(MailerAfterSendEvent)
	})

	err := m.SendDirect(context.Background(), validMessage())
	if !errors.Is(err, driverErr) {
		t.Errorf("SendDirect err = %v, want wrap of %v", err, driverErr)
	}

	select {
	case ev := <-afterCh:
		if !errors.Is(ev.Err, driverErr) {
			t.Errorf("after-event err = %v, want %v", ev.Err, driverErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("after_send did not fire within 2s on driver error")
	}
}

// TestMailerEvents_NoBus_NoOps is the regression guard: Mailer
// constructed WITHOUT a bus must keep working identically to v1.0
// (no publish path, no nil-deref).
func TestMailerEvents_NoBus_NoOps(t *testing.T) {
	drv := &countingDriver{inner: NewConsoleDriver(&bytes.Buffer{})}
	m := New(Options{
		Driver:      drv,
		DefaultFrom: Address{Email: "from@example.com"},
		// Bus deliberately nil.
	})

	if err := m.SendDirect(context.Background(), validMessage()); err != nil {
		t.Fatalf("send with nil bus: %v", err)
	}
	if drv.calls.Load() != 1 {
		t.Errorf("driver calls = %d, want 1", drv.calls.Load())
	}
}

// TestMailerEvents_TemplateRoute_AlsoFires asserts SendTemplate fires
// the same hooks (it delegates to SendDirect internally — this guard
// catches future refactors that bypass that path).
func TestMailerEvents_TemplateRoute_AlsoFires(t *testing.T) {
	m, bus, drv := newTestMailer(t, nil)

	var beforeFired, afterFired atomic.Bool
	afterCh := make(chan struct{}, 1)

	bus.SubscribeSync(TopicBeforeSend, func(_ context.Context, e eventbus.Event) {
		ev := e.Payload.(*MailerBeforeSendEvent)
		if ev.Message.Subject == "" {
			t.Errorf("template route: empty subject seen by before-hook")
		}
		beforeFired.Store(true)
	})
	bus.Subscribe(TopicAfterSend, 0, func(_ context.Context, e eventbus.Event) {
		afterFired.Store(true)
		afterCh <- struct{}{}
	})

	err := m.SendTemplate(
		context.Background(),
		"signup_verification",
		[]Address{{Email: "user@example.com"}},
		map[string]any{
			"site":       map[string]any{"name": "Test", "from": "from@example.com"},
			"user":       map[string]any{"email": "user@example.com"},
			"verify_url": "https://example.com/verify",
		},
	)
	if err != nil {
		t.Fatalf("SendTemplate: %v", err)
	}

	select {
	case <-afterCh:
	case <-time.After(2 * time.Second):
		t.Fatal("after_send did not fire within 2s from template route")
	}
	if !beforeFired.Load() {
		t.Error("before_send did not fire from SendTemplate path")
	}
	if !afterFired.Load() {
		t.Error("after_send did not fire from SendTemplate path")
	}
	if drv.calls.Load() != 1 {
		t.Errorf("driver calls = %d, want 1", drv.calls.Load())
	}
}
