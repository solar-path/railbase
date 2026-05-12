package realtime

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/compat"
	rerr "github.com/railbase/railbase/internal/errors"
)

// principalExtractor / tenantExtractor are closure-style accessors
// injected by app.go so this package doesn't import internal/auth/
// middleware or internal/tenant directly (mirrors the RBAC wiring).
type principalExtractor func(*http.Request) (collectionName string, recordID uuid.UUID, ok bool)
type tenantExtractor func(*http.Request) (tenantID uuid.UUID, ok bool)

// Handler returns the chi-compatible SSE handler. Mounted at
// `/api/realtime` by app.go.
//
// Query params:
//
//	topics=posts/*,users/me    (required; comma-separated list)
//	since=<event-id>           (optional; resume from broker event id)
//
// Headers honoured:
//
//	Last-Event-ID: <event-id>  (standard SSE resume; takes precedence
//	                            over ?since= when both are present)
//
// Resume semantics: when a since-id is supplied, the broker replays
// every buffered event whose id > since AND whose topic + tenant
// match this subscription, then transitions to the live stream. If
// the buffer no longer covers `since` (oldest evicted), a
// `railbase.replay-truncated` event is emitted before live frames so
// the client knows it may have missed events.
//
// Flow:
//
//	1. extract principal + tenant
//	2. parse topics + resume cursor
//	3. set SSE headers + flush
//	4. create subscription + snapshot replay (atomically)
//	5. write replay frames, then `replay-truncated` if applicable
//	6. ticker for heartbeat, drain live queue, write frames
//	7. on r.Context().Done() unsubscribe
//
// PB-SDK compat: when compat.From(ctx) == ModeStrict the handler
// follows PocketBase's wire dance — emits a PB_CONNECT pre-frame
// carrying a clientId, accepts an empty initial topic set (PB
// clients populate it via POST /api/realtime), and re-shapes each
// outgoing event payload to `{action, record}`. See pb_compat.go.
// The registry argument is non-nil iff PB-compat is wired in;
// passing nil disables the PB-compat path entirely (useful for
// unit tests of the native path).
func Handler(broker *Broker, registry *ClientRegistry, principal principalExtractor, tenant tenantExtractor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Auth is required. SSE wraps the response so 401 via our
		// JSON envelope only works BEFORE we write the SSE headers.
		coll, uid, ok := principal(r)
		if !ok || coll == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "realtime requires authentication"))
			return
		}
		userRef := coll + "/" + uid.String()

		// PB-compat strict mode allows opening the SSE stream with an
		// empty topic set; PB SDKs populate it via the POST subscribe
		// endpoint. Native mode still requires ?topics= up-front.
		pbCompat := registry != nil && compat.From(r.Context()) == compat.ModeStrict

		topicsRaw := r.URL.Query().Get("topics")
		if topicsRaw == "" && !pbCompat {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "?topics= is required (comma-separated)"))
			return
		}
		var topics []string
		if topicsRaw != "" {
			topics = strings.Split(topicsRaw, ",")
		}
		if len(topics) == 0 && !pbCompat {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "no topics given"))
			return
		}

		// Resume cursor: Last-Event-ID header wins, falling back to
		// ?since=. We accept empty/garbage as "no resume" rather than
		// error because EventSource auto-sets Last-Event-ID after a
		// reconnect and we don't want to break the standard contract.
		sinceID, hasSince := parseSinceCursor(r)

		tenantStr := ""
		if tid, ok := tenant(r); ok {
			tenantStr = tid.String()
		}

		// Verify the response writer supports flushing — it MUST for
		// SSE. Behind some proxies (or Go's auto-buffering) we'd
		// otherwise queue frames in a buffer until the response
		// closes, defeating the realtime contract.
		flusher, ok := w.(http.Flusher)
		if !ok {
			rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "streaming not supported"))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // nginx: disable response buffering
		w.WriteHeader(http.StatusOK)
		// Send an immediate retry hint + a comment frame so the
		// client knows the stream is alive even before the first
		// event. EventSource will reconnect after `retry` ms on
		// network errors.
		_, _ = fmt.Fprintf(w, "retry: 2000\n: connected\n\n")
		flusher.Flush()

		sub, resume, _ := broker.SubscribeWithResume(topics, userRef, tenantStr, sinceID, hasSince)
		defer broker.Unsubscribe(sub.ID)

		// PB-SDK handshake. The PB JS client treats the FIRST event
		// frame as PB_CONNECT and stores its clientId. We register
		// the clientId → broker subscription mapping before the SSE
		// write so a POST subscribe arriving within milliseconds
		// finds the live connection. Native mode skips this entirely.
		var clientID string
		if pbCompat {
			clientID = newClientID()
			registry.Register(clientID, sub.ID)
			defer registry.Unregister(clientID)
			if err := writePBConnectFrame(w, clientID); err != nil {
				return
			}
		}

		// Tell the client its assigned subscription id. Useful for
		// admin UI debugging + resume-token wire-up. In PB-compat
		// mode we still emit this — it's a Railbase-namespaced event
		// (`railbase.subscribed`) so PB SDKs that don't listen for
		// it simply ignore it.
		_, _ = fmt.Fprintf(w, "event: railbase.subscribed\ndata: {\"id\":%q}\n\n", sub.ID.String())
		flusher.Flush()

		// Replay any buffered events that match. These carry their
		// original broker event-ids so a second reconnect can resume
		// from the highest one delivered.
		for _, ev := range resume.Replay {
			data := ev.Data
			if pbCompat {
				if reshaped, ok := toPBShape(data); ok {
					data = reshaped
				}
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Topic, data); err != nil {
				return
			}
		}
		// Truncation marker: emitted AFTER any partial replay so the
		// client receives whatever the buffer could still produce
		// before it learns of the gap.
		if hasSince && resume.Truncated {
			_, _ = fmt.Fprintf(w, "event: railbase.replay-truncated\ndata: {\"since\":%d}\n\n", sinceID)
		}
		if hasSince || len(resume.Replay) > 0 {
			flusher.Flush()
		}

		heartbeat := time.NewTicker(25 * time.Second)
		defer heartbeat.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Done():
				// v1.7.38 — broker-side teardown (Unsubscribe).
				// queue is never closed (would race with fanOut
				// sends), so we exit on done instead. ctx.Done()
				// covers the request-cancel path; sub.Done()
				// covers broker shutdown / external teardown.
				return
			case <-heartbeat.C:
				// SSE comment frame — no event, keeps proxies + clients
				// from closing on idle.
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case ev := <-sub.Queue():
				// Use the topic name as the SSE `event:` field so
				// EventSource's `.addEventListener("posts/create",
				// ...)` filters work. Topic strings can contain `/`
				// and other safe characters but MUST NOT contain
				// newlines — our publisher controls them, so this is
				// fine.
				data := ev.Data
				if pbCompat {
					if reshaped, ok := toPBShape(data); ok {
						data = reshaped
					}
				}
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Topic, data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// parseSinceCursor extracts the resume cursor from the request,
// preferring the Last-Event-ID header (per the SSE spec) over the
// ?since= query param. Returns hasSince=false on missing/invalid
// cursors — the SSE spec is explicit that invalid Last-Event-ID
// values must NOT abort the request.
func parseSinceCursor(r *http.Request) (uint64, bool) {
	if h := strings.TrimSpace(r.Header.Get("Last-Event-ID")); h != "" {
		if n, err := strconv.ParseUint(h, 10, 64); err == nil {
			return n, true
		}
		// Bad header — fall through to the query param. We don't
		// short-circuit because Last-Event-ID may be auto-set by
		// EventSource to a value we wrote earlier in non-numeric
		// form; the query param represents an explicit client intent.
	}
	if q := strings.TrimSpace(r.URL.Query().Get("since")); q != "" {
		if n, err := strconv.ParseUint(q, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}
