package realtime

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/eventbus"
)

// makeBrokerCfg returns a fresh bus + broker with the given config.
// Used by resume tests that need a tighter ring than the 1000 default.
func makeBrokerCfg(t *testing.T, cfg BrokerConfig) (*eventbus.Bus, *Broker) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	b := NewBrokerWithConfig(bus, log, cfg)
	b.Start()
	t.Cleanup(func() {
		b.Stop()
		bus.Close()
	})
	return bus, b
}

// publishAndWait pushes an event and blocks until the broker's
// fanOut has assigned an id to it. We bounce off Snapshot's
// implicit mu acquire — there's no public LastEventID, so we poll
// the ring through a benign Subscribe/Unsubscribe cycle.
func publishAndWait(t *testing.T, bus *eventbus.Bus, b *Broker, rec RecordEvent) {
	t.Helper()
	want := b.NextEventIDTesting() + 1
	Publish(bus, rec)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b.NextEventIDTesting() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for event id %d (broker still at %d)", want, b.NextEventIDTesting())
}

// --- Buffer eviction ---

func TestRing_HoldsLastNAndEvicts(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 5})

	for i := 0; i < 10; i++ {
		publishAndWait(t, bus, broker, RecordEvent{
			Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i),
		})
	}

	got := broker.BufferSnapshotTesting()
	if len(got) != 5 {
		t.Fatalf("ring length = %d, want 5", len(got))
	}
	// Oldest retained id should be (10 - 5 + 1) = 6; newest should be 10.
	if got[0].id != 6 || got[len(got)-1].id != 10 {
		t.Errorf("retained ids = %v, want [6..10]", idsOf(got))
	}
}

// --- Resume happy path ---

func TestResume_ValidSinceReplaysNewer(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 100})

	for i := 0; i < 5; i++ {
		publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i)})
	}

	// Resume from id 2 → should receive events 3, 4, 5.
	sub, res, _ := broker.SubscribeWithResume([]string{"posts/*"}, "u/1", "", 2, true)
	defer broker.Unsubscribe(sub.ID)

	if res.Truncated {
		t.Errorf("Truncated = true, want false (since=2 is within buffer)")
	}
	if got := idsOfEvents(res.Replay); !equalU64s(got, []uint64{3, 4, 5}) {
		t.Errorf("replay ids = %v, want [3 4 5]", got)
	}
}

// --- Truncation marker ---

func TestResume_TooOldEmitsReplayTruncated(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 3})

	for i := 0; i < 10; i++ {
		publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i)})
	}
	// Ring now retains ids 8, 9, 10. Asking for since=2 means we
	// missed 3..7 → must signal truncation.
	sub, res, _ := broker.SubscribeWithResume([]string{"posts/*"}, "u/1", "", 2, true)
	defer broker.Unsubscribe(sub.ID)

	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	// We still deliver what's available.
	if got := idsOfEvents(res.Replay); !equalU64s(got, []uint64{8, 9, 10}) {
		t.Errorf("replay ids = %v, want [8 9 10]", got)
	}
}

// --- Topic filtering during replay ---

func TestResume_TopicPatternFiltersReplay(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 50})

	// Interleave posts/* and users/* events.
	publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "p1"})
	publishAndWait(t, bus, broker, RecordEvent{Collection: "users", Verb: VerbCreate, ID: "u1"})
	publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbUpdate, ID: "p1"})
	publishAndWait(t, bus, broker, RecordEvent{Collection: "users", Verb: VerbUpdate, ID: "u1"})

	sub, res, _ := broker.SubscribeWithResume([]string{"posts/*"}, "u/1", "", 0, true)
	defer broker.Unsubscribe(sub.ID)

	for _, ev := range res.Replay {
		if !strings.HasPrefix(ev.Topic, "posts/") {
			t.Errorf("replay leaked non-posts event: %q", ev.Topic)
		}
	}
	if len(res.Replay) != 2 {
		t.Errorf("replay len = %d, want 2 (posts/create, posts/update)", len(res.Replay))
	}
}

// --- Tenant filtering during replay ---

func TestResume_TenantFilterApplied(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 50})
	tA := uuid.Must(uuid.NewV7()).String()
	tB := uuid.Must(uuid.NewV7()).String()

	publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "a1", TenantID: tA})
	publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "b1", TenantID: tB})
	publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "site1"}) // site-wide

	sub, res, _ := broker.SubscribeWithResume([]string{"posts/*"}, "u/1", tA, 0, true)
	defer broker.Unsubscribe(sub.ID)

	// tenantA subscriber should get the tA event + site-wide event,
	// but NOT the tB event.
	if len(res.Replay) != 2 {
		t.Fatalf("replay len = %d, want 2 (tA + site-wide)", len(res.Replay))
	}
	for _, ev := range res.Replay {
		if strings.Contains(string(ev.Data), `"id":"b1"`) {
			t.Errorf("replay leaked tenantB event: %s", ev.Data)
		}
	}
}

// --- Race safety: concurrent publishers + resuming subscribers ---

func TestResume_RaceSafe_ConcurrentPublishersAndSubscribers(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 200})

	const publishers = 4
	const perPublisher = 50
	const subscribers = 8

	var pubWG sync.WaitGroup
	pubWG.Add(publishers)
	for p := 0; p < publishers; p++ {
		go func(p int) {
			defer pubWG.Done()
			for i := 0; i < perPublisher; i++ {
				Publish(bus, RecordEvent{
					Collection: "posts",
					Verb:       VerbCreate,
					ID:         fmt.Sprintf("p%d-i%d", p, i),
				})
			}
		}(p)
	}

	var subWG sync.WaitGroup
	subWG.Add(subscribers)
	for s := 0; s < subscribers; s++ {
		go func() {
			defer subWG.Done()
			sub, _, _ := broker.SubscribeWithResume([]string{"posts/*"}, "u/1", "", 0, true)
			// drain a few live events to exercise the queue path too
			deadline := time.After(200 * time.Millisecond)
			for {
				select {
				case <-sub.Queue():
				case <-deadline:
					broker.Unsubscribe(sub.ID)
					return
				}
			}
		}()
	}

	pubWG.Wait()
	subWG.Wait()

	// Sanity: no panics, broker still functional.
	if broker.NextEventIDTesting() < uint64(publishers*perPublisher) {
		t.Errorf("nextID = %d, want >= %d", broker.NextEventIDTesting(), publishers*perPublisher)
	}
}

// --- Last-Event-ID header vs ?since= precedence ---

func TestSSEHandler_HeaderTakesPrecedenceOverQuery(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 100})
	for i := 0; i < 5; i++ {
		publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i)})
	}

	userID := uuid.Must(uuid.NewV7())
	h := Handler(broker, nil,
		func(*http.Request) (string, uuid.UUID, bool) { return "users", userID, true },
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Last-Event-ID = 3 → should replay 4, 5
	// ?since = 1 → ignored because header wins
	req, _ := http.NewRequest("GET", srv.URL+"?topics=posts/*&since=1", nil)
	req.Header.Set("Last-Event-ID", "3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body := readSSEUntilQuiet(t, resp.Body, 300*time.Millisecond)
	ids := extractIDs(body)
	if !equalU64s(ids, []uint64{4, 5}) {
		t.Errorf("got replay ids = %v, want [4 5] (header should win)", ids)
	}
}

func TestSSEHandler_QueryFallback(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 100})
	for i := 0; i < 5; i++ {
		publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i)})
	}

	userID := uuid.Must(uuid.NewV7())
	h := Handler(broker, nil,
		func(*http.Request) (string, uuid.UUID, bool) { return "users", userID, true },
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?topics=posts/*&since=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body := readSSEUntilQuiet(t, resp.Body, 300*time.Millisecond)
	ids := extractIDs(body)
	if !equalU64s(ids, []uint64{3, 4, 5}) {
		t.Errorf("got replay ids = %v, want [3 4 5]", ids)
	}
}

// --- No since-id == fresh subscription, no replay ---

func TestSSEHandler_NoSinceMeansNoReplay(t *testing.T) {
	bus, broker := makeBrokerCfg(t, BrokerConfig{ReplayBufferSize: 100})
	for i := 0; i < 5; i++ {
		publishAndWait(t, bus, broker, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i)})
	}

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

	// Single reader goroutine accumulates the entire SSE response.
	// We assert on it at two checkpoints: (1) after the initial
	// subscribe phase to verify NO replay occurred, then (2) after
	// publishing a live event to verify it flows through with an id.
	var mu sync.Mutex
	var acc []byte
	readDone := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				mu.Lock()
				acc = append(acc, buf[:n]...)
				mu.Unlock()
			}
			if err != nil {
				close(readDone)
				return
			}
		}
	}()

	// Phase 1: wait until the "railbase.subscribed" frame arrives,
	// then verify no replay frames were sent.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ready := strings.Contains(string(acc), "railbase.subscribed")
		mu.Unlock()
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Give the handler a beat to also flush any (erroneous) replays.
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	snap1 := string(acc)
	mu.Unlock()
	if ids := extractIDs(snap1); len(ids) != 0 {
		t.Errorf("got replay ids = %v, want none (no since-id supplied)", ids)
	}
	if strings.Contains(snap1, "replay-truncated") {
		t.Errorf("unexpected replay-truncated event: %s", snap1)
	}

	// Phase 2: publish a live event, then verify it shows up with
	// id 6 (5 pre-existing + this one).
	Publish(bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "post6"})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ready := strings.Contains(string(acc), "event: posts/create") && strings.Contains(string(acc), "id: 6")
		mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	t.Fatalf("did not receive live event with id 6: %q", string(acc))
	mu.Unlock()
}

// --- Helpers ---

func idsOf(buf []bufferedEvent) []uint64 {
	out := make([]uint64, len(buf))
	for i, b := range buf {
		out[i] = b.id
	}
	return out
}

func idsOfEvents(evs []event) []uint64 {
	out := make([]uint64, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func equalU64s(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// readSSEUntilQuiet reads from r until no data has arrived for `quiet`
// duration, then returns everything accumulated. Useful for asserting
// "the server stopped sending more frames" without arbitrary sleeps.
func readSSEUntilQuiet(t *testing.T, r io.Reader, quiet time.Duration) string {
	t.Helper()
	var acc []byte
	br := bufio.NewReader(r)
	type chunk struct {
		data []byte
		err  error
	}
	ch := make(chan chunk, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				c := make([]byte, n)
				copy(c, buf[:n])
				ch <- chunk{data: c}
			}
			if err != nil {
				ch <- chunk{err: err}
				return
			}
		}
	}()
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	overall := time.NewTimer(3 * time.Second)
	defer overall.Stop()
	for {
		select {
		case c := <-ch:
			if c.err != nil {
				return string(acc)
			}
			acc = append(acc, c.data...)
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(quiet)
		case <-timer.C:
			return string(acc)
		case <-overall.C:
			return string(acc)
		}
	}
}

// extractIDs parses out the "id: N" prefix from each SSE frame in
// `body`, in order. Lines starting with "id:" are how the broker
// stamps assigned event ids onto the wire.
func extractIDs(body string) []uint64 {
	var out []uint64
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "id:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		n, err := strconv.ParseUint(rest, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}
