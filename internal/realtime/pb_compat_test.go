package realtime

// PocketBase JS SDK drop-in compatibility test for the SSE transport.
//
// This file exercises the full PB SDK handshake against the SSE
// handler running in compat.ModeStrict (the v1 SHIP default):
//
//  1. Open GET /api/realtime.
//  2. Read the FIRST frame; assert PB_CONNECT event with a clientId.
//  3. POST /api/realtime body {clientId, subscriptions:["posts/create"]}.
//  4. Publish a record event server-side via the broker.
//  5. Assert the SSE stream surfaces a `posts/create` event whose
//     payload matches the PB SDK's `{action, record}` shape.
//
// The test is the verification artifact §3.5.9 closes — it locks in
// the PB-compat wire contract so future refactors can't silently
// break the SDK.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/compat"
)

// pbCompatServer wires the SSE GET + POST subscribe handlers behind
// a httptest server with the compat middleware stamping ModeStrict
// onto every request. Mirrors the production wiring in app.go but
// skips auth / tenant / RBAC layers so the test stays focused on the
// PB-protocol surface.
func pbCompatServer(t *testing.T) (string, *Broker) {
	t.Helper()
	bus, broker := makeBroker(t)
	userID := uuid.Must(uuid.NewV7())
	principal := func(*http.Request) (string, uuid.UUID, bool) {
		return "users", userID, true
	}
	tenant := func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false }
	registry := NewClientRegistry()

	// Stamp ModeStrict onto every request context, exactly like the
	// production compatResolver.Middleware().
	resolver := compat.NewResolver(compat.ModeStrict)
	mw := resolver.Middleware()

	getHandler := mw(Handler(broker, registry, principal, tenant))
	postHandler := mw(SubscribeHandler(broker, registry, principal))
	mux := http.NewServeMux()
	mux.Handle("/api/realtime", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// httptest's mux doesn't method-discriminate; route here.
		switch r.Method {
		case http.MethodGet:
			getHandler.ServeHTTP(w, r)
		case http.MethodPost:
			postHandler.ServeHTTP(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_ = bus
	return srv.URL, broker
}

// sseFrame is one parsed SSE frame from the wire. SSE frames are
// blank-line-separated key/value blocks; we extract the three fields
// the PB protocol uses (`id`, `event`, `data`).
type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// readNextSSEFrame parses one SSE frame off the stream. Returns the
// frame when a blank-line terminator is seen, or the read error.
// Heartbeat comment lines (starting with `:`) are skipped — they're
// not real frames and don't affect protocol behaviour.
func readNextSSEFrame(br *bufio.Reader) (sseFrame, error) {
	var f sseFrame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Blank line terminates a frame. If we haven't accumulated
			// any fields, keep reading — could be the initial retry/
			// comment block separating from the first real frame.
			if f.Event == "" && f.Data == "" && f.ID == "" {
				continue
			}
			return f, nil
		}
		if strings.HasPrefix(line, ":") {
			// SSE comment frame (heartbeat / connected hint). Skip.
			continue
		}
		switch {
		case strings.HasPrefix(line, "id:"):
			f.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			f.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			f.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case strings.HasPrefix(line, "retry:"):
			// retry hint — not relevant to protocol assertions
		}
	}
}

// TestPBCompat_HandshakeAndEventShape is the end-to-end PB-SDK
// compatibility check. The flow mirrors what a PB JS client does in
// production:
//
//   - Open SSE; read PB_CONNECT pre-frame; extract clientId.
//   - POST /api/realtime with clientId + topics.
//   - Trigger a publish; assert the event lands with PB-shape payload.
//
// Asserts on the wire bytes (not internal Go state) so changes to the
// frame layout / payload shape get caught immediately.
func TestPBCompat_HandshakeAndEventShape(t *testing.T) {
	url, broker := pbCompatServer(t)

	// Step 1: Open SSE without ?topics= — PB clients populate
	// subscriptions via POST, so the GET must accept empty topics
	// when compat mode is strict.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/realtime", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("SSE open status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	br := bufio.NewReader(resp.Body)

	// Step 2: Read the FIRST event frame; assert PB_CONNECT.
	pbConnect, err := readNextSSEFrame(br)
	if err != nil {
		t.Fatalf("read PB_CONNECT frame: %v", err)
	}
	if pbConnect.Event != "PB_CONNECT" {
		t.Fatalf("first frame event = %q, want PB_CONNECT (frame = %+v)", pbConnect.Event, pbConnect)
	}
	if pbConnect.ID == "" {
		t.Errorf("PB_CONNECT frame missing id (SSE event-id MUST carry clientId)")
	}
	var connectData struct {
		ClientID string `json:"clientId"`
	}
	if err := json.Unmarshal([]byte(pbConnect.Data), &connectData); err != nil {
		t.Fatalf("PB_CONNECT data not JSON: %q (err: %v)", pbConnect.Data, err)
	}
	if connectData.ClientID == "" {
		t.Fatalf("PB_CONNECT data.clientId is empty: %q", pbConnect.Data)
	}
	if connectData.ClientID != pbConnect.ID {
		t.Errorf("PB_CONNECT clientId mismatch: id=%q data.clientId=%q", pbConnect.ID, connectData.ClientID)
	}

	// Drain the Railbase-namespaced `railbase.subscribed` frame the
	// SSE handler emits next. PB SDKs ignore unknown event names; we
	// just need to advance past it so the next assertion lands on a
	// fresh frame.
	if _, err := readNextSSEFrame(br); err != nil {
		t.Fatalf("read railbase.subscribed frame: %v", err)
	}

	// Step 3: POST /api/realtime with the clientId + topic list. PB
	// SDK serialises this exact body shape.
	body, _ := json.Marshal(map[string]any{
		"clientId":      connectData.ClientID,
		"subscriptions": []string{"posts/create"},
	})
	postReq, _ := http.NewRequestWithContext(ctx, "POST", url+"/api/realtime", bytes.NewReader(body))
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("POST subscribe: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST subscribe status = %d, want 204", postResp.StatusCode)
	}

	// Step 4: Publish an event the new subscription should match.
	// Give the broker's SetSubscriptionTopics a moment to land —
	// it's a synchronous mu-guarded swap, but the SSE handler's
	// SubscribeWithResume happened with empty topics, so we need
	// the POST handler's SetSubscriptionTopics call to have run
	// before fanOut evaluates `sub.matches`.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := broker.Snapshot()
		matched := false
		for _, sub := range s.Subscriptions {
			for _, tp := range sub.Topics {
				if tp == "posts/create" {
					matched = true
					break
				}
			}
		}
		if matched {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	Publish(broker.bus, RecordEvent{
		Collection: "posts",
		Verb:       VerbCreate,
		ID:         "abc",
		Record:     map[string]any{"id": "abc", "title": "hello pb"},
	})

	// Step 5: Read frames until we see a posts/create event; assert
	// its payload matches the PB SDK's `{action, record}` shape.
	got, err := readUntilEventOnWire(br, "posts/create", 3*time.Second)
	if err != nil {
		t.Fatalf("waiting for posts/create: %v", err)
	}
	var pbPayload struct {
		Action string                 `json:"action"`
		Record map[string]any         `json:"record"`
		// PB shape MUST NOT carry these native-shape fields. If they
		// leak the PB SDK still ignores them (extra fields are fine
		// per JSON contract) but we want to catch shape drift early.
		Collection string `json:"collection,omitempty"`
		Verb       string `json:"verb,omitempty"`
	}
	if err := json.Unmarshal([]byte(got.Data), &pbPayload); err != nil {
		t.Fatalf("posts/create data not JSON: %q (err: %v)", got.Data, err)
	}
	if pbPayload.Action != "create" {
		t.Errorf("payload.action = %q, want create (full payload: %s)", pbPayload.Action, got.Data)
	}
	if pbPayload.Record["title"] != "hello pb" {
		t.Errorf("payload.record.title = %v, want hello pb (full payload: %s)", pbPayload.Record["title"], got.Data)
	}
	if pbPayload.Collection != "" || pbPayload.Verb != "" {
		t.Errorf("payload leaked native-shape fields: collection=%q verb=%q (full payload: %s)",
			pbPayload.Collection, pbPayload.Verb, got.Data)
	}
}

// readUntilEventOnWire walks the SSE stream until it finds a frame
// whose `event:` line matches `want`, or the deadline elapses.
// Errors propagate so the test gets an actionable signal (timeout,
// EOF, etc.) rather than a hanging select.
func readUntilEventOnWire(br *bufio.Reader, want string, timeout time.Duration) (sseFrame, error) {
	deadline := time.Now().Add(timeout)
	type result struct {
		frame sseFrame
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			f, err := readNextSSEFrame(br)
			if err != nil {
				ch <- result{err: err}
				return
			}
			if f.Event == want {
				ch <- result{frame: f}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.frame, r.err
	case <-time.After(time.Until(deadline)):
		return sseFrame{}, &timeoutErr{want: want}
	}
}

type timeoutErr struct{ want string }

func (e *timeoutErr) Error() string { return "timed out waiting for SSE event: " + e.want }

// TestPBCompat_SubscribeRejectsUnknownClientID ensures the POST
// subscribe path returns 404 (not 500 / not silent success) when the
// clientId doesn't match any open SSE connection. PB SDKs detect
// this and force a full reconnect; if the server silently no-op'd
// the SDK would believe its subscription took effect.
func TestPBCompat_SubscribeRejectsUnknownClientID(t *testing.T) {
	url, _ := pbCompatServer(t)

	body, _ := json.Marshal(map[string]any{
		"clientId":      "00000000-0000-0000-0000-000000000000",
		"subscriptions": []string{"posts/create"},
	})
	req, _ := http.NewRequest("POST", url+"/api/realtime", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 404; body = %s", resp.StatusCode, raw)
	}
}

// TestPBCompat_NativeModeKeepsOriginalShape protects the v1.3.0
// native wire shape (`{collection,verb,id,record,tenant_id,at}`) for
// clients that didn't opt into the PB-compat handshake. Even when
// the registry is provided, a non-strict ctx mode must yield the
// native payload — otherwise we'd silently re-shape events for every
// client and break the v1.3.0 contract.
func TestPBCompat_NativeModeKeepsOriginalShape(t *testing.T) {
	bus, broker := makeBroker(t)
	userID := uuid.Must(uuid.NewV7())
	registry := NewClientRegistry()
	resolver := compat.NewResolver(compat.ModeNative)
	h := resolver.Middleware()(Handler(broker, registry,
		func(*http.Request) (string, uuid.UUID, bool) { return "users", userID, true },
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?topics=posts/*")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)

	// Native mode MUST NOT emit a PB_CONNECT frame. The first real
	// frame is `railbase.subscribed`.
	first, err := readNextSSEFrame(br)
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if first.Event == "PB_CONNECT" {
		t.Fatalf("native mode leaked PB_CONNECT frame: %+v", first)
	}

	// Publish an event; assert the data carries the native fields.
	Publish(bus, RecordEvent{
		Collection: "posts", Verb: VerbCreate, ID: "n1",
		Record: map[string]any{"id": "n1", "title": "native"},
	})
	frm, err := readUntilEventOnWire(br, "posts/create", 2*time.Second)
	if err != nil {
		t.Fatalf("read posts/create: %v", err)
	}
	if !strings.Contains(frm.Data, `"collection":"posts"`) {
		t.Errorf("native payload missing collection field: %s", frm.Data)
	}
	if !strings.Contains(frm.Data, `"verb":"create"`) {
		t.Errorf("native payload missing verb field: %s", frm.Data)
	}
}
