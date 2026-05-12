package realtime

// PocketBase JS SDK drop-in compatibility for the SSE transport.
//
// The PB JS SDK's `RealtimeService` expects this wire dance:
//
//	1. Client opens GET /api/realtime (SSE).
//	2. Server's FIRST event is:
//	     id: <clientId>
//	     event: PB_CONNECT
//	     data: {"clientId":"<clientId>"}
//	   Client extracts clientId, stores it.
//	3. Client POSTs /api/realtime with body
//	     {"clientId":"<clientId>","subscriptions":["topic1","topic2"]}
//	   Server matches clientId → the live SSE connection and updates its
//	   subscription set.
//	4. Per matching change the server emits:
//	     event: <topic>
//	     data: {"action":"create|update|delete","record":{...}}
//
// Railbase's native SSE protocol (v1.3.0) uses `?topics=` on the GET
// and emits `{collection, verb, id, record, tenant_id, at}` as the
// payload. That's a superset of the PB shape but the field names
// differ — the PB SDK wouldn't parse it.
//
// Strict mode (compat.ModeStrict, the v1 SHIP default) opts the SSE
// handler into the PB-compatible wire format:
//
//   - PB_CONNECT pre-frame with a generated clientId.
//   - `?topics=` is optional (PB clients subscribe via POST).
//   - Per-event payload re-shaped to `{action, record}`.
//
// Native + both modes preserve the original Railbase shape so existing
// consumers don't break.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	rerr "github.com/railbase/railbase/internal/errors"
)

// ClientRegistry maps PB-SDK clientId strings to the live broker
// Subscription that owns the matching SSE connection. The POST
// /api/realtime handler looks up the clientId here and mutates the
// subscription's topic set via Broker.SetSubscriptionTopics.
//
// One registry per Broker. Goroutine-safe.
type ClientRegistry struct {
	mu      sync.Mutex
	byID    map[string]uuid.UUID // clientId → broker subscription id
}

// NewClientRegistry constructs an empty registry. The SSE handler
// adds entries on connect; the POST subscribe handler reads them.
func NewClientRegistry() *ClientRegistry {
	return &ClientRegistry{byID: map[string]uuid.UUID{}}
}

// Register binds a PB-SDK clientId to a broker subscription id. The
// SSE handler calls this immediately after generating its PB_CONNECT
// pre-frame so a racing POST subscribe (PB SDK fires it within ~10ms
// of receiving the clientId) finds the live subscription.
func (r *ClientRegistry) Register(clientID string, subID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[clientID] = subID
}

// Unregister removes a clientId binding. The SSE handler calls this
// on connection teardown so a stale POST from an already-disconnected
// client doesn't mutate a recycled subscription id.
func (r *ClientRegistry) Unregister(clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, clientID)
}

// Lookup returns the broker subscription id for a clientId, or
// (zero, false) if unknown. The POST subscribe handler uses this to
// route the topic update to the right SSE connection.
func (r *ClientRegistry) Lookup(clientID string) (uuid.UUID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byID[clientID]
	return id, ok
}

// pbEventPayload is the PB-SDK wire shape for a single record-change
// frame's `data:` field. The SDK's RealtimeService unmarshals this
// envelope and surfaces `record` to user code; `action` discriminates
// the create/update/delete callback.
//
// Marshalled with `record` as raw bytes so we can copy the existing
// RecordEvent.Record JSON through without an extra unmarshal/marshal
// round trip when the broker already serialised it.
type pbEventPayload struct {
	Action string          `json:"action"`
	Record json.RawMessage `json:"record"`
}

// toPBShape converts a Railbase-native event payload into the PB-SDK
// wire shape. The input is the broker's serialised RecordEvent (the
// same bytes the native handler writes verbatim into the SSE frame).
//
// Returns (pbBytes, true) on a clean conversion. On any parse error
// we return (nil, false) so the caller can fall back to the native
// payload — better to deliver the native shape than to drop the frame
// entirely, even if a PB SDK client wouldn't parse it.
func toPBShape(data []byte) ([]byte, bool) {
	// We need to extract `verb` and `record` from RecordEvent and
	// re-emit as `{action, record}`. Decode just the fields we need
	// — `record` stays as raw JSON to avoid a re-encode cycle.
	var probe struct {
		Verb   Verb            `json:"verb"`
		Record json.RawMessage `json:"record"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, false
	}
	// PB action vocab: "create" | "update" | "delete". Our Verb has
	// the same string values; passthrough is safe. Empty / unknown
	// verbs would slip through too — we don't reject because the
	// broker never emits those today and we'd rather forward an
	// odd-looking action than swallow the event.
	if probe.Record == nil {
		// PB SDK expects `record` to always be present (it's how the
		// SDK distinguishes a real event from a heartbeat). Use an
		// empty object rather than null so JS-side property access
		// doesn't NPE.
		probe.Record = json.RawMessage("{}")
	}
	out, err := json.Marshal(pbEventPayload{
		Action: string(probe.Verb),
		Record: probe.Record,
	})
	if err != nil {
		return nil, false
	}
	return out, true
}

// subscribeRequest is the PB-SDK POST body for /api/realtime. Field
// names match PB's wire contract exactly so the SDK serialises to it
// without a custom encoder.
type subscribeRequest struct {
	ClientID      string   `json:"clientId"`
	Subscriptions []string `json:"subscriptions"`
}

// SubscribeHandler returns the chi-compatible handler for
// POST /api/realtime — the PB-SDK subscription-update endpoint.
// Mounted alongside the SSE GET handler in strict / both modes.
//
// The handler looks up the SSE connection by clientId in the registry
// and replaces its topic set with `subscriptions`. Empty subscriptions
// list is allowed (PB SDK uses it to "unsubscribe from everything"
// without closing the SSE connection).
//
// Auth: the same principal-required gate as the SSE GET. We don't
// re-verify clientId ownership against the principal — PB doesn't
// either, and the clientId is a UUIDv7 (effectively unguessable) so
// the SSE connection's authentication implicitly scopes who can
// mutate it. A determined attacker who guesses a clientId can only
// re-target someone else's SSE connection; the events that flow are
// still gated by that connection's principal/tenant.
func SubscribeHandler(broker *Broker, registry *ClientRegistry, principal principalExtractor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, _, ok := principal(r)
		if !ok || coll == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "realtime subscribe requires authentication"))
			return
		}

		var req subscribeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid JSON body: %s", err.Error()))
			return
		}
		if strings.TrimSpace(req.ClientID) == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "clientId is required"))
			return
		}

		subID, ok := registry.Lookup(req.ClientID)
		if !ok {
			// Stale or unknown clientId. PB returns 404; we mirror.
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "unknown clientId — open /api/realtime first"))
			return
		}
		broker.SetSubscriptionTopics(subID, req.Subscriptions)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNoContent)
	}
}

// writePBConnectFrame emits the PB_CONNECT pre-frame the PB JS SDK
// waits for before issuing its POST subscribe. The frame uses the
// clientId as the SSE event-id so the JS-side `lastEventId` stays
// coherent across reconnects (PB clients re-send it as
// Last-Event-ID on reconnect; we accept any string there, falling
// through to no-resume when it isn't numeric — same lenient policy
// as parseSinceCursor).
func writePBConnectFrame(w http.ResponseWriter, clientID string) error {
	_, err := fmt.Fprintf(w, "id: %s\nevent: PB_CONNECT\ndata: {\"clientId\":%q}\n\n", clientID, clientID)
	return err
}

// newClientID mints a clientId for a fresh PB SDK connection. UUIDv7
// is monotonic + globally unique — convenient for log correlation —
// and the JS SDK doesn't care about format (it treats clientId as an
// opaque string).
func newClientID() string {
	return uuid.Must(uuid.NewV7()).String()
}
