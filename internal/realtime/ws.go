package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/compat"
	rerr "github.com/railbase/railbase/internal/errors"
)

// WSHandler returns the WebSocket sibling of the SSE Handler. Mounted
// at `/api/realtime/ws` by app.go.
//
// Why both transports? SSE (v1.3.0) works fine for ~95% of "live
// update the list view" workloads but has known gaps:
//
//   - Browsers + intermediate proxies sometimes drop long-lived SSE
//     connections at the 30-60s mark even with keepalives.
//   - One-way only (server → client). To subscribe / unsubscribe at
//     runtime the SSE client must reconnect with a new `?topics=` or
//     hit a separate REST endpoint.
//   - PocketBase JS SDK uses EventSource for v0.22- and WebSocket for
//     v0.23+. Shipping WS keeps the PB-compat surface forward-friendly.
//
// Both transports run side-by-side. Clients pick whichever they prefer;
// the broker is identical so events fan out to all subscribers.
//
// Wire protocol (newline-separated JSON frames over TEXT messages —
// one JSON object per WebSocket text frame, exactly like the PB v0.23+
// shape):
//
//	Client → Server:
//	  {"action":"subscribe","topics":["posts/*","users/me"]}
//	  {"action":"unsubscribe","topics":["posts/*"]}
//	  {"action":"ping"}                                  // keepalive
//
//	Server → Client:
//	  {"event":"<collection>/<verb>","id":"<event_id>","data":{...}}
//	  {"event":"railbase.subscribed","topics":[...]}      // sub ack
//	  {"event":"railbase.unsubscribed","topics":[...]}    // unsub ack
//	  {"event":"railbase.replay-truncated","since":N}     // resume gap
//	  {"event":"pong"}                                    // heartbeat
//	  {"event":"error","message":"..."}                   // terminal
//
// The FIRST inbound frame from the client MUST be a subscribe frame,
// optionally carrying a "since" field for resume:
//
//	{"action":"subscribe","topics":[...],"since":"<event_id>"}
//
// Subsequent subscribe / unsubscribe frames mutate the live topic set
// without reconnecting — that's the headline win over the v1.3.0 SSE
// transport, which made clients reconnect to change topics.
//
// Auth + tenant scoping are reused from the SSE handler's middleware
// chain. The HTTP request is fully authenticated BEFORE the WebSocket
// upgrade happens; an unauthenticated request never reaches `Accept()`
// and instead receives a 401 JSON envelope.
// WSHandler signature gained `registry *ClientRegistry` in v1.7.37 —
// passing a non-nil value activates PB-compat behaviours (currently
// just the `{action, record}` payload reshape; the WS protocol
// doesn't need the clientId/PB_CONNECT handshake that SSE uses).
// Production callers in app.go thread the SAME registry as SSE so
// the two transports stay symmetric. nil = native-shape forever
// (the contract the v1.3.0 WS callers depend on).
func WSHandler(broker *Broker, registry *ClientRegistry, principal principalExtractor, tenant tenantExtractor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Auth gate runs BEFORE upgrade so 401 surfaces as a normal
		// JSON envelope (which the upgrade flow can't emit later).
		coll, uid, ok := principal(r)
		if !ok || coll == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "realtime requires authentication"))
			return
		}
		userRef := coll + "/" + uid.String()

		tenantStr := ""
		if tid, ok := tenant(r); ok {
			tenantStr = tid.String()
		}

		// v1.7.37 — PB SDK v0.23+ uses WebSocket and expects its
		// `{action, record}` inner payload shape. Mirror the SSE path
		// from v1.7.36b: when the caller passed a non-nil registry
		// AND the request runs in strict mode, re-marshal the broker's
		// native RecordEvent into PB shape on every fan-out. Either
		// gate failing keeps the payload bit-for-bit unchanged.
		//
		// Why the registry-nil gate matters: `compat.From` defaults to
		// ModeStrict for unstamped contexts (a safe-default policy
		// inherited from the resolver). Tests that don't run the compat
		// middleware would otherwise reshape unexpectedly. Production
		// passes the registry; tests pass nil → native shape preserved.
		pbCompat := registry != nil && compat.From(r.Context()) == compat.ModeStrict

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Subprotocol negotiation lets future iterations roll the
			// wire format forward without breaking existing clients.
			Subprotocols: []string{"railbase.v1"},
			// Postgres rows already encode small. Compression buys us
			// little for sub-1KB JSON frames and burns CPU + memory
			// per-connection — skip it.
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			// Accept already wrote whatever error status the underlying
			// hijack/upgrade flow produced. Nothing more to do here.
			return
		}
		// CloseNow is the "best effort, don't wait for handshake"
		// teardown. The structured close happens in the normal exit
		// paths via c.Close().
		defer c.CloseNow()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Heartbeat: send WS-level pings at the same 25s cadence as
		// SSE heartbeats so proxies don't kill idle connections. We
		// ignore the error — a dead connection surfaces as a Read
		// error on the inbound loop and tears the handler down.
		go func() {
			t := time.NewTicker(25 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					pingCtx, pcancel := context.WithTimeout(ctx, 10*time.Second)
					_ = c.Ping(pingCtx)
					pcancel()
				}
			}
		}()

		// The first frame from the client is the subscribe frame. We
		// give it a generous timeout: clients reading PB-compat docs
		// expect "open WS, then send subscribe" to be racey-tolerant.
		firstCtx, firstCancel := context.WithTimeout(ctx, 30*time.Second)
		first, err := readFrame(firstCtx, c)
		firstCancel()
		if err != nil {
			writeError(ctx, c, "expected subscribe frame: "+err.Error())
			_ = c.Close(websocket.StatusPolicyViolation, "missing subscribe frame")
			return
		}
		if first.Action != "subscribe" || len(first.Topics) == 0 {
			writeError(ctx, c, "first frame must be {\"action\":\"subscribe\",\"topics\":[...]}")
			_ = c.Close(websocket.StatusPolicyViolation, "bad subscribe frame")
			return
		}

		// Resume cursor — same semantics as the SSE handler. Empty /
		// garbage parses as "no resume" rather than error, matching the
		// SSE policy of being lenient with reconnection hints.
		var sinceID uint64
		hasSince := false
		if s := strings.TrimSpace(first.Since); s != "" {
			if n, perr := strconv.ParseUint(s, 10, 64); perr == nil {
				sinceID = n
				hasSince = true
			}
		}

		sub, resume, _ := broker.SubscribeWithResume(first.Topics, userRef, tenantStr, sinceID, hasSince)
		defer broker.Unsubscribe(sub.ID)

		// State the WS handler owns beyond the broker: the live topic
		// set we've ACKed so dynamic subscribe / unsubscribe can mutate
		// it without re-registering the broker subscription. We could
		// alternatively unsubscribe + resubscribe on every change, but
		// that would drop in-flight events queued for the sub and
		// reset the resume cursor — both worse for live UIs.
		var topicMu sync.Mutex
		liveTopics := append([]string(nil), sub.Topics...)

		updateTopics := func(action string, delta []string) ([]string, error) {
			topicMu.Lock()
			defer topicMu.Unlock()
			switch action {
			case "subscribe":
				for _, t := range cleanTopics(delta) {
					if !containsString(liveTopics, t) {
						liveTopics = append(liveTopics, t)
					}
				}
			case "unsubscribe":
				keep := liveTopics[:0]
				drop := map[string]bool{}
				for _, t := range cleanTopics(delta) {
					drop[t] = true
				}
				for _, t := range liveTopics {
					if !drop[t] {
						keep = append(keep, t)
					}
				}
				liveTopics = append([]string(nil), keep...)
			default:
				return nil, fmt.Errorf("unknown action %q", action)
			}
			// Push the updated topic set onto the broker subscription.
			// SetTopics is a cheap mu-guarded swap — safe to call while
			// the broker fans out concurrently.
			broker.SetSubscriptionTopics(sub.ID, liveTopics)
			return append([]string(nil), liveTopics...), nil
		}

		// Ack the initial subscribe BEFORE writing replay so the client
		// has a clean "you're subscribed to X" landmark before the
		// historical frames arrive.
		if err := writeFrame(ctx, c, outFrame{
			Event:  "railbase.subscribed",
			ID:     sub.ID.String(),
			Topics: append([]string(nil), liveTopics...),
		}); err != nil {
			return
		}

		// Replay buffered events that match. These carry their
		// original broker event-ids so a later reconnect can resume
		// from the highest id delivered.
		for _, ev := range resume.Replay {
			if err := writeRecordFrame(ctx, c, ev, pbCompat); err != nil {
				return
			}
		}
		if hasSince && resume.Truncated {
			if err := writeFrame(ctx, c, outFrame{
				Event: "railbase.replay-truncated",
				Since: strconv.FormatUint(sinceID, 10),
			}); err != nil {
				return
			}
		}

		// Reader goroutine: parses inbound frames + applies subscribe /
		// unsubscribe / ping actions. Exits on any read error (close
		// from the peer, network drop, malformed framing). Surfaces
		// the exit by cancelling ctx so the writer loop below stops.
		readerErr := make(chan error, 1)
		go func() {
			for {
				frm, err := readFrame(ctx, c)
				if err != nil {
					readerErr <- err
					return
				}
				switch frm.Action {
				case "ping":
					_ = writeFrame(ctx, c, outFrame{Event: "pong"})
				case "subscribe", "unsubscribe":
					topics, err := updateTopics(frm.Action, frm.Topics)
					if err != nil {
						writeError(ctx, c, err.Error())
						continue
					}
					ackEvent := "railbase.subscribed"
					if frm.Action == "unsubscribe" {
						ackEvent = "railbase.unsubscribed"
					}
					_ = writeFrame(ctx, c, outFrame{Event: ackEvent, Topics: topics})
				default:
					writeError(ctx, c, "unknown action: "+frm.Action)
				}
			}
		}()

		// Writer loop: forwards live broker frames to the WebSocket.
		// We give up on the first write error; the client reconnects.
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Done():
				// v1.7.38 — broker-side teardown signal. queue is
				// never closed (would race with concurrent fanOut
				// sends), so we read done explicitly. ctx.Done()
				// covers the HTTP-request cancel path; sub.Done()
				// covers broker shutdown / external Unsubscribe.
				return
			case err := <-readerErr:
				// Reader failed (client closed, malformed frame, etc).
				// Treat normal close codes as a graceful exit; anything
				// else gets surfaced as an error frame on a best-effort
				// basis (the conn may already be gone).
				if !isCloseErr(err) {
					writeError(ctx, c, err.Error())
				}
				return
			case ev := <-sub.Queue():
				if err := writeRecordFrame(ctx, c, ev, pbCompat); err != nil {
					return
				}
			}
		}
	}
}

// inFrame is the client → server wire shape. Fields are a union;
// `Action` discriminates.
type inFrame struct {
	Action string   `json:"action"`
	Topics []string `json:"topics,omitempty"`
	Since  string   `json:"since,omitempty"`
}

// outFrame is the server → client wire shape. `Event` discriminates;
// payload fields are populated only for the relevant event types.
//
// `Data` is `json.RawMessage` rather than `map[string]any` so we can
// forward broker-serialised payloads byte-for-byte without re-marshal
// (matches how the SSE handler writes raw ev.Data into the frame body).
type outFrame struct {
	Event   string          `json:"event"`
	ID      string          `json:"id,omitempty"`
	Topics  []string        `json:"topics,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Since   string          `json:"since,omitempty"`
	Message string          `json:"message,omitempty"`
}

// readFrame reads ONE JSON text frame off the WebSocket. We use a
// MessageType-aware reader so binary frames (which we don't support)
// surface as an explicit error rather than silently corrupting the
// decoder.
func readFrame(ctx context.Context, c *websocket.Conn) (inFrame, error) {
	mt, data, err := c.Read(ctx)
	if err != nil {
		return inFrame{}, err
	}
	if mt != websocket.MessageText {
		return inFrame{}, fmt.Errorf("expected text frame, got %v", mt)
	}
	var f inFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return inFrame{}, fmt.Errorf("malformed JSON: %w", err)
	}
	return f, nil
}

// writeFrame encodes + sends one JSON text frame.
func writeFrame(ctx context.Context, c *websocket.Conn, f outFrame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, body)
}

// writeRecordFrame forwards a broker `event` envelope as the
// `{"event": "<topic>", "id": "<broker-id>", "data": <payload>}` shape
// the JS client expects. ev.Data is already a marshalled RecordEvent;
// embedding it as RawMessage avoids the extra unmarshal/marshal round
// trip on every fan-out.
//
// v1.7.37 — when pbCompat is set (caller computed via
// `compat.From(ctx) == ModeStrict`) the inner data is re-marshalled
// from the broker's native RecordEvent shape to the PB SDK v0.23+
// `{action, record}` shape via toPBShape. If the reshape fails (data
// isn't recognisable as a RecordEvent) we pass it through verbatim —
// same fail-soft policy as the SSE handler in v1.7.36b.
func writeRecordFrame(ctx context.Context, c *websocket.Conn, ev event, pbCompat bool) error {
	data := ev.Data
	if pbCompat {
		if reshaped, ok := toPBShape(data); ok {
			data = reshaped
		}
	}
	return writeFrame(ctx, c, outFrame{
		Event: ev.Topic,
		ID:    strconv.FormatUint(ev.ID, 10),
		Data:  json.RawMessage(data),
	})
}

// writeError emits a terminal `{"event":"error", ...}` frame.
// Best-effort; we don't bubble the write error because the conn is
// almost certainly already dying when we land here.
func writeError(ctx context.Context, c *websocket.Conn, msg string) {
	_ = writeFrame(ctx, c, outFrame{Event: "error", Message: msg})
}

// isCloseErr is the cheap classifier for "client went away normally".
// We don't want to spam error frames on legitimate disconnects.
func isCloseErr(err error) bool {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return true
	}
	// Context cancellation surfaces as ctx.Err() from the reader; it's
	// also a normal teardown.
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// containsString is a tiny set-membership helper. We use a slice
// rather than a map because the topic set is typically 1-3 entries —
// allocation cost dominates compare cost at that size.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// SetSubscriptionTopics swaps the active topic patterns for a live
// subscription without re-registering it on the broker. The new set
// takes effect on the NEXT fanOut — events currently mid-flight
// through the bounded queue are unaffected (they were enqueued under
// the previous matcher).
//
// Used by the WebSocket transport to support dynamic subscribe /
// unsubscribe frames; the SSE transport never calls this because the
// SSE protocol doesn't expose a client → server channel.
//
// Returns silently if `id` is unknown — the WS handler defends against
// this by unregistering the sub on connection teardown, so a stale
// SetSubscriptionTopics from a racing reader goroutine is a no-op
// rather than a panic.
func (b *Broker) SetSubscriptionTopics(id uuid.UUID, topics []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub, ok := b.subs[id]
	if !ok {
		return
	}
	sub.Topics = cleanTopics(topics)
}
