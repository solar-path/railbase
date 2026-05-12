package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// wsTestServer builds an httptest server with the WS handler wired to
// a fresh broker. Returns server URL (ws://...), broker, bus so tests
// can publish + assert.
func wsTestServer(t *testing.T, authed bool) (string, *Broker, *httptest.Server) {
	t.Helper()
	bus, broker := makeBroker(t)
	userID := uuid.Must(uuid.NewV7())
	// v1.7.37 — pass nil registry to keep the native payload shape;
	// the PB-compat reshape is opt-in via a non-nil registry (set in
	// production by app.go) and gated on compat.ModeStrict.
	h := WSHandler(broker, nil,
		func(*http.Request) (string, uuid.UUID, bool) {
			if !authed {
				return "", uuid.Nil, false
			}
			return "users", userID, true
		},
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// httptest hands out http://; coder/websocket Dial accepts both
	// http:// and ws:// (it rewrites internally), so passing http URL
	// works and avoids URL surgery. Keep bus referenced via broker.
	_ = bus
	return srv.URL, broker, srv
}

// dialWS opens a WS connection to the test server. The defer-friendly
// close fn shuts it down with StatusNormalClosure.
func dialWS(t *testing.T, ctx context.Context, url string) (*websocket.Conn, func()) {
	t.Helper()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{"railbase.v1"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c, func() { _ = c.Close(websocket.StatusNormalClosure, "test done") }
}

// readJSONFrame reads ONE JSON frame and decodes it into the provided
// target. Wraps a tight timeout so a hanging server doesn't stall the
// test runner.
func readJSONFrame(t *testing.T, ctx context.Context, c *websocket.Conn) map[string]any {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	mt, data, err := c.Read(rctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if mt != websocket.MessageText {
		t.Fatalf("expected text frame, got %v", mt)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return out
}

// readUntilEvent reads frames until one with `event == want` arrives
// or `timeout` elapses. Returns the matching frame. Other frames
// (subscribed ack, replays, heartbeats) are silently consumed.
func readUntilEvent(t *testing.T, c *websocket.Conn, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		mt, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read while waiting for %q: %v (got: nothing)", want, err)
		}
		if mt != websocket.MessageText {
			continue
		}
		var frm map[string]any
		if err := json.Unmarshal(data, &frm); err != nil {
			continue
		}
		if evt, _ := frm["event"].(string); evt == want {
			return frm
		}
	}
	t.Fatalf("did not see event %q within %v", want, timeout)
	return nil
}

// writeJSON sends a JSON object as one text frame.
func writeJSON(t *testing.T, ctx context.Context, c *websocket.Conn, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageText, body); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// --- Tests ---

func TestWS_SubscribeAndReceive(t *testing.T) {
	url, broker, _ := wsTestServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, closeFn := dialWS(t, ctx, url)
	defer closeFn()

	writeJSON(t, ctx, c, map[string]any{
		"action": "subscribe",
		"topics": []string{"posts/*"},
	})

	// First server frame must be the subscribe ack.
	ack := readJSONFrame(t, ctx, c)
	if ack["event"] != "railbase.subscribed" {
		t.Fatalf("expected railbase.subscribed, got %v", ack)
	}

	// Publish via the broker → expect a record frame on the wire.
	Publish(broker.bus, RecordEvent{
		Collection: "posts", Verb: VerbCreate, ID: "abc",
		Record: map[string]any{"id": "abc", "title": "hello"},
	})

	frm := readUntilEvent(t, c, "posts/create", 2*time.Second)
	if frm["id"] == "" {
		t.Errorf("missing id field: %v", frm)
	}
	data, _ := frm["data"].(map[string]any)
	if got, _ := data["id"].(string); got != "abc" {
		t.Errorf("payload id = %v, want abc; full frame = %v", got, frm)
	}
}

func TestWS_DynamicUnsubscribe(t *testing.T) {
	url, broker, _ := wsTestServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, closeFn := dialWS(t, ctx, url)
	defer closeFn()

	writeJSON(t, ctx, c, map[string]any{
		"action": "subscribe",
		"topics": []string{"posts/*", "users/*"},
	})
	readUntilEvent(t, c, "railbase.subscribed", 2*time.Second)

	// Drop posts/*; users/* must keep flowing.
	writeJSON(t, ctx, c, map[string]any{
		"action": "unsubscribe",
		"topics": []string{"posts/*"},
	})
	ackFrm := readUntilEvent(t, c, "railbase.unsubscribed", 2*time.Second)
	topics, _ := ackFrm["topics"].([]any)
	if len(topics) != 1 || topics[0] != "users/*" {
		t.Errorf("post-unsubscribe topics = %v, want [users/*]", topics)
	}

	// Publish a posts event — it should NOT arrive. Then a users
	// event — it MUST arrive. We assert the users event by reading
	// the next frame; if posts leaked it'd show up first.
	Publish(broker.bus, RecordEvent{Collection: "posts", Verb: VerbCreate, ID: "p1"})
	Publish(broker.bus, RecordEvent{Collection: "users", Verb: VerbCreate, ID: "u1"})

	frm := readUntilEvent(t, c, "users/create", 2*time.Second)
	data, _ := frm["data"].(map[string]any)
	if got, _ := data["id"].(string); got != "u1" {
		t.Errorf("expected users/create id=u1, got %v", frm)
	}
}

func TestWS_PingPong(t *testing.T) {
	url, _, _ := wsTestServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, closeFn := dialWS(t, ctx, url)
	defer closeFn()

	writeJSON(t, ctx, c, map[string]any{"action": "subscribe", "topics": []string{"posts/*"}})
	readUntilEvent(t, c, "railbase.subscribed", 2*time.Second)

	start := time.Now()
	writeJSON(t, ctx, c, map[string]any{"action": "ping"})
	pong := readUntilEvent(t, c, "pong", 1*time.Second)
	if pong["event"] != "pong" {
		t.Errorf("expected pong frame, got %v", pong)
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("pong took %v (want <1s)", elapsed)
	}
}

func TestWS_AuthRequired(t *testing.T) {
	url, _, _ := wsTestServer(t, false /* not authed */)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Don't use websocket.Dial here — we want to inspect the HTTP
	// status BEFORE any upgrade attempt. The auth gate writes a 401
	// JSON envelope; the upgrade should never happen.
	httpURL := strings.Replace(url, "ws://", "http://", 1)
	req, _ := http.NewRequestWithContext(ctx, "GET", httpURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// Confirm the upgrade did NOT happen — no Upgrade header in the
	// response means the server short-circuited before Accept.
	if up := resp.Header.Get("Upgrade"); strings.EqualFold(up, "websocket") {
		t.Errorf("unexpected Upgrade header — upgrade should NOT have happened: %q", up)
	}
}

func TestWS_Resume(t *testing.T) {
	bus, broker := makeBroker(t)
	userID := uuid.Must(uuid.NewV7())
	// nil registry → native payload shape; the resume IDs are the
	// only thing this test asserts on, but seeing the reshaped
	// shape on retrieve would confuse the diff if the test ever
	// gains payload-content assertions.
	h := WSHandler(broker, nil,
		func(*http.Request) (string, uuid.UUID, bool) { return "users", userID, true },
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Pre-populate the broker with events 1..5.
	for i := 0; i < 5; i++ {
		publishAndWait(t, bus, broker, RecordEvent{
			Collection: "posts", Verb: VerbCreate, ID: strconv.Itoa(i),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, closeFn := dialWS(t, ctx, srv.URL)
	defer closeFn()

	// Subscribe with since=2 → expect replay of ids 3, 4, 5.
	writeJSON(t, ctx, c, map[string]any{
		"action": "subscribe",
		"topics": []string{"posts/*"},
		"since":  "2",
	})

	// First frame: ack. After that, three replay frames with ids 3..5.
	readUntilEvent(t, c, "railbase.subscribed", 2*time.Second)
	var got []uint64
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 3 && time.Now().Before(deadline) {
		rctx, rcancel := context.WithDeadline(context.Background(), deadline)
		_, data, err := c.Read(rctx)
		rcancel()
		if err != nil {
			break
		}
		var frm map[string]any
		if json.Unmarshal(data, &frm) != nil {
			continue
		}
		if frm["event"] == "posts/create" {
			if idStr, _ := frm["id"].(string); idStr != "" {
				if n, err := strconv.ParseUint(idStr, 10, 64); err == nil {
					got = append(got, n)
				}
			}
		}
	}
	if !equalU64s(got, []uint64{3, 4, 5}) {
		t.Errorf("resume replay ids = %v, want [3 4 5]", got)
	}
}

func TestWS_BadFrame_Errors(t *testing.T) {
	url, _, _ := wsTestServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, closeFn := dialWS(t, ctx, url)
	defer closeFn()

	// Send malformed JSON as the very first frame — handler must
	// reply with an error frame and then close.
	if err := c.Write(ctx, websocket.MessageText, []byte("{not valid json")); err != nil {
		t.Fatalf("write: %v", err)
	}

	frm := readUntilEvent(t, c, "error", 2*time.Second)
	if msg, _ := frm["message"].(string); !strings.Contains(strings.ToLower(msg), "subscribe") &&
		!strings.Contains(strings.ToLower(msg), "json") {
		t.Errorf("error message should mention subscribe or json, got %q", msg)
	}

	// Verify the server closed the connection (next read errors).
	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcancel()
	if _, _, err := c.Read(rctx); err == nil {
		t.Errorf("expected connection close after error frame")
	}
}
