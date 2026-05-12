package realtime

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/eventbus"
)

// makeBroker returns a fresh bus + started broker for tests.
func makeBroker(t *testing.T) (*eventbus.Bus, *Broker) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	b := NewBroker(bus, log)
	b.Start()
	t.Cleanup(func() {
		b.Stop()
		bus.Close()
	})
	return bus, b
}

func TestTopicMatch(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		{"posts/create", "posts/create", true},
		{"posts/create", "posts/update", false},
		{"posts/*", "posts/create", true},
		{"posts/*", "posts/delete", true},
		{"posts/*", "users/create", false},
		{"*/create", "posts/create", true},
		{"*/create", "users/create", true},
		{"*/create", "users/update", false},
		{"posts/create", "posts/create/extra", false}, // segment count differs
	}
	for _, c := range cases {
		if got := topicMatch(c.pattern, c.topic); got != c.want {
			t.Errorf("topicMatch(%q, %q) = %v, want %v", c.pattern, c.topic, got, c.want)
		}
	}
}

func TestPublishAndFanOut(t *testing.T) {
	bus, broker := makeBroker(t)
	sub := broker.Subscribe([]string{"posts/*"}, "users/123", "")
	defer broker.Unsubscribe(sub.ID)

	Publish(bus, RecordEvent{
		Collection: "posts",
		Verb:       VerbCreate,
		ID:         "abc",
		Record:     map[string]any{"id": "abc", "title": "hello"},
	})

	select {
	case ev := <-sub.Queue():
		if ev.Topic != "posts/create" {
			t.Errorf("topic = %q, want posts/create", ev.Topic)
		}
		if !strings.Contains(string(ev.Data), `"title":"hello"`) {
			t.Errorf("data missing title: %s", ev.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestSubscription_FilteredByTopic(t *testing.T) {
	bus, broker := makeBroker(t)
	postsOnly := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	defer broker.Unsubscribe(postsOnly.ID)
	usersOnly := broker.Subscribe([]string{"users/*"}, "u/1", "")
	defer broker.Unsubscribe(usersOnly.ID)

	Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "a"})

	select {
	case <-postsOnly.Queue():
	case <-time.After(500 * time.Millisecond):
		t.Error("postsOnly should have received the event")
	}
	select {
	case ev := <-usersOnly.Queue():
		t.Errorf("usersOnly should NOT have received posts event: %v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSubscription_TenantFilter(t *testing.T) {
	bus, broker := makeBroker(t)
	tenantA := uuid.Must(uuid.NewV7()).String()
	tenantB := uuid.Must(uuid.NewV7()).String()

	scopedA := broker.Subscribe([]string{"posts/*"}, "u/1", tenantA)
	defer broker.Unsubscribe(scopedA.ID)
	scopedB := broker.Subscribe([]string{"posts/*"}, "u/1", tenantB)
	defer broker.Unsubscribe(scopedB.ID)
	siteWide := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	defer broker.Unsubscribe(siteWide.ID)

	Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "x", TenantID: tenantA})

	select {
	case <-scopedA.Queue():
	case <-time.After(500 * time.Millisecond):
		t.Error("tenantA subscriber should receive its event")
	}
	select {
	case <-scopedB.Queue():
		t.Error("tenantB subscriber should NOT receive tenantA event")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-siteWide.Queue():
	case <-time.After(500 * time.Millisecond):
		t.Error("site-wide subscriber should receive any event")
	}
}

func TestSubscription_Backpressure_DropsOldest(t *testing.T) {
	bus, broker := makeBroker(t)
	sub := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	defer broker.Unsubscribe(sub.ID)

	// Fill the bounded queue (cap 64) + a lot extra.
	for i := 0; i < 200; i++ {
		Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "x"})
	}
	// Wait for the bus's per-subscriber goroutine to drain its
	// input queue into the broker's fanOut.
	time.Sleep(150 * time.Millisecond)

	if sub.Dropped() == 0 {
		t.Errorf("expected drops > 0 (slow consumer overflowed queue)")
	}
	// Drain so we don't leak goroutines.
	for {
		select {
		case <-sub.Queue():
		default:
			return
		}
	}
}

func TestUnsubscribe_StopsDelivery(t *testing.T) {
	bus, broker := makeBroker(t)
	sub := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	broker.Unsubscribe(sub.ID)

	if !sub.Closed() {
		t.Errorf("subscription should be closed")
	}
	Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "x"})
	time.Sleep(50 * time.Millisecond)
	// Queue is closed — any read returns ok=false immediately.
	select {
	case _, ok := <-sub.Queue():
		if ok {
			t.Errorf("queue should be closed")
		}
	case <-time.After(100 * time.Millisecond):
		// also fine — sub is gone
	}
}

func TestSnapshot(t *testing.T) {
	_, broker := makeBroker(t)
	s1 := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	defer broker.Unsubscribe(s1.ID)
	s2 := broker.Subscribe([]string{"users/me"}, "u/2", "")
	defer broker.Unsubscribe(s2.ID)

	stats := broker.Snapshot()
	if stats.SubscriptionCount != 2 {
		t.Errorf("count = %d, want 2", stats.SubscriptionCount)
	}
	if len(stats.Subscriptions) != 2 {
		t.Errorf("snapshot details: %d, want 2", len(stats.Subscriptions))
	}
}

// --- SSE handler tests ---

func TestSSEHandler_Unauthorized(t *testing.T) {
	_, broker := makeBroker(t)
	h := Handler(broker, nil,
		func(*http.Request) (string, uuid.UUID, bool) { return "", uuid.Nil, false },
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?topics=posts/*")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestSSEHandler_MissingTopics(t *testing.T) {
	_, broker := makeBroker(t)
	userID := uuid.Must(uuid.NewV7())
	h := Handler(broker, nil,
		func(*http.Request) (string, uuid.UUID, bool) { return "users", userID, true },
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL) // no ?topics=
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for missing topics, got %d", resp.StatusCode)
	}
}

func TestSSEHandler_HappyPath(t *testing.T) {
	bus, broker := makeBroker(t)
	userID := uuid.Must(uuid.NewV7())
	h := Handler(broker, nil,
		func(*http.Request) (string, uuid.UUID, bool) { return "users", userID, true },
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?topics=posts/*")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Spawn a goroutine to read frames into a growing buffer until
	// we see what we're looking for or timeout.
	var captured atomic.Value
	captured.Store("")
	done := make(chan struct{})
	go func() {
		var acc []byte
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				captured.Store(string(acc))
				if strings.Contains(string(acc), "event: posts/create") {
					close(done)
					return
				}
			}
			if err != nil {
				close(done)
				return
			}
		}
	}()

	// Publish after the connection establishes.
	time.Sleep(100 * time.Millisecond)
	Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "abc",
		Record: map[string]any{"id": "abc", "title": "live"}})

	select {
	case <-done:
		got := captured.Load().(string)
		if !strings.Contains(got, "event: posts/create") {
			t.Errorf("SSE frame missing event line: %q", got)
		}
		if !strings.Contains(got, `"title":"live"`) {
			t.Errorf("SSE frame missing payload: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE frame: got %q" + captured.Load().(string))
	}
}
