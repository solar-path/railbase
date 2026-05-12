package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/jobs"
)

// v1.7.34 — eventbus topic published on every TERMINAL webhook
// delivery outcome (success or dead). Retries are silent — subscribers
// learn about a delivery exactly once. Payload is DeliveryEvent below.
//
// Subscribers (admin UI realtime tile, custom metrics emitters, hook
// authors observing webhook success rate) use this instead of polling
// `_webhook_deliveries`.
const TopicWebhookDelivered = "webhook.delivered"

// DeliveryEvent is the payload on TopicWebhookDelivered. Outcome is
// "success" (2xx) or "dead" (4xx, validation failure, webhook
// deleted/paused). Mirrors `_webhook_deliveries.status` exactly.
type DeliveryEvent struct {
	DeliveryID uuid.UUID
	WebhookID  uuid.UUID
	Webhook    string // name, for log-friendly observers
	Event      string // the event topic that triggered this delivery (e.g. "records.posts.created")
	Outcome    string // "success" or "dead" — "retry" never fires this event
	StatusCode int    // HTTP status if reached the receiver; 0 for pre-send failures
	Attempt    int    // 1-indexed attempt count at terminal
	Error      string // empty for success
}

// HandlerDeps wires the delivery handler to its dependencies. The
// http.Client is configurable so tests can plug in a transport that
// hits httptest.Server without going through DNS / TCP.
type HandlerDeps struct {
	Store        *Store
	Client       *http.Client
	Log          *slog.Logger
	AllowPrivate bool // dev mode — permit private-IP destinations
	// Bus is OPTIONAL — when nil, no event topics fire. When non-nil
	// (production wiring in app.go), every terminal delivery
	// (success / dead) publishes DeliveryEvent on TopicWebhookDelivered.
	Bus *eventbus.Bus
}

// emitTerminal fans a DeliveryEvent onto the bus. nil-Bus → no-op.
// Called from the 2xx-success branch + every "dead" branch.
func (d HandlerDeps) emitTerminal(ev DeliveryEvent) {
	if d.Bus == nil {
		return
	}
	d.Bus.Publish(eventbus.Event{Topic: TopicWebhookDelivered, Payload: ev})
}

// NewDeliveryHandler returns a jobs.Handler that does one POST attempt
// per call. Returns a non-nil error to trigger the jobs framework's
// exp-backoff retry; returns nil after committing success / dead.
func NewDeliveryHandler(deps HandlerDeps) jobs.Handler {
	if deps.Client == nil {
		deps.Client = &http.Client{}
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return func(ctx context.Context, j *jobs.Job) error {
		var p deliveryPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return fmt.Errorf("webhooks: bad payload: %w", err)
		}
		delID, err := uuid.Parse(p.DeliveryID)
		if err != nil {
			return fmt.Errorf("webhooks: delivery id: %w", err)
		}
		webhookID, err := uuid.Parse(p.WebhookID)
		if err != nil {
			return fmt.Errorf("webhooks: webhook id: %w", err)
		}

		w, err := deps.Store.GetByID(ctx, webhookID)
		if err != nil {
			// Webhook was deleted while jobs were in flight. Mark the
			// delivery `dead`; no point retrying.
			_ = deps.Store.CompleteDelivery(ctx, delID, "dead", 0, "", "webhook deleted")
			deps.emitTerminal(DeliveryEvent{DeliveryID: delID, WebhookID: webhookID,
				Outcome: "dead", Error: "webhook deleted"})
			return nil
		}
		if !w.Active {
			// Operator paused while in flight. Mark `dead` so the queue
			// drains instead of looping; the next event will trigger a
			// fresh delivery once they re-enable.
			_ = deps.Store.CompleteDelivery(ctx, delID, "dead", 0, "", "webhook inactive")
			deps.emitTerminal(DeliveryEvent{DeliveryID: delID, WebhookID: webhookID,
				Webhook: w.Name, Outcome: "dead", Error: "webhook inactive"})
			return nil
		}

		dels, err := deps.Store.ListDeliveries(ctx, webhookID, 1) // get the row we care about
		_ = dels
		_ = err

		// Re-validate URL on each attempt — operators may have fixed
		// a typo since the delivery was queued.
		u, err := ValidateURL(w.URL, ValidatorOptions{AllowPrivate: deps.AllowPrivate})
		if err != nil {
			_ = deps.Store.CompleteDelivery(ctx, delID, "dead", 0, "", err.Error())
			deps.emitTerminal(DeliveryEvent{DeliveryID: delID, WebhookID: webhookID,
				Webhook: w.Name, Outcome: "dead", Error: err.Error()})
			return nil
		}

		// Load the original payload from the delivery row so retries
		// re-POST the IDENTICAL body (consumers can dedupe).
		body, event, attempt, err := loadDeliveryRequest(ctx, deps.Store, delID)
		if err != nil {
			return fmt.Errorf("webhooks: load delivery: %w", err)
		}

		secret, err := DecodeSecret(w.SecretB64)
		if err != nil {
			_ = deps.Store.CompleteDelivery(ctx, delID, "dead", 0, "", "bad secret")
			deps.emitTerminal(DeliveryEvent{DeliveryID: delID, WebhookID: webhookID,
				Webhook: w.Name, Event: event, Attempt: attempt,
				Outcome: "dead", Error: "bad secret"})
			return nil
		}
		sig := Sign(secret, body, time.Now().UTC())

		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(w.TimeoutMS)*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			_ = deps.Store.CompleteDelivery(ctx, delID, "dead", 0, "", err.Error())
			deps.emitTerminal(DeliveryEvent{DeliveryID: delID, WebhookID: webhookID,
				Webhook: w.Name, Event: event, Attempt: attempt,
				Outcome: "dead", Error: err.Error()})
			return nil
		}
		// Custom operator headers first; then canonical ones (these
		// always overwrite custom — no spoofing the canonical chain).
		for k, v := range w.Headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Railbase-Webhook/1.0")
		req.Header.Set("X-Railbase-Event", event)
		req.Header.Set("X-Railbase-Webhook", w.Name)
		req.Header.Set("X-Railbase-Delivery", delID.String())
		req.Header.Set("X-Railbase-Attempt", fmt.Sprintf("%d", attempt))
		req.Header.Set(SignatureHeader, sig)

		resp, err := deps.Client.Do(req)
		if err != nil {
			deps.Log.Warn("webhooks: deliver error", "webhook", w.Name, "url", w.URL, "err", err)
			_ = deps.Store.CompleteDelivery(ctx, delID, "retry", 0, "", err.Error())
			// Return the err so jobs framework retries with backoff.
			return err
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			_ = deps.Store.CompleteDelivery(ctx, delID, "success", resp.StatusCode, string(respBody), "")
			deps.emitTerminal(DeliveryEvent{DeliveryID: delID, WebhookID: webhookID,
				Webhook: w.Name, Event: event, Attempt: attempt,
				Outcome: "success", StatusCode: resp.StatusCode})
			return nil
		case resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500:
			// Retryable — no terminal event yet.
			_ = deps.Store.CompleteDelivery(ctx, delID, "retry", resp.StatusCode, string(respBody), "")
			return fmt.Errorf("webhooks: http %d", resp.StatusCode)
		default:
			// 4xx non-retryable — receiver explicitly rejected.
			_ = deps.Store.CompleteDelivery(ctx, delID, "dead", resp.StatusCode, string(respBody), "")
			deps.emitTerminal(DeliveryEvent{DeliveryID: delID, WebhookID: webhookID,
				Webhook: w.Name, Event: event, Attempt: attempt,
				Outcome: "dead", StatusCode: resp.StatusCode,
				Error: fmt.Sprintf("http %d", resp.StatusCode)})
			return nil
		}
	}
}

// loadDeliveryRequest fetches the body + event + attempt from a
// delivery row. We need this so retry attempts re-POST identical
// bytes (consumer-side dedup relies on it).
func loadDeliveryRequest(ctx context.Context, s *Store, id uuid.UUID) ([]byte, string, int, error) {
	row := s.q.QueryRow(ctx, `SELECT payload, event, attempt FROM _webhook_deliveries WHERE id = $1`, id)
	var body []byte
	var event string
	var attempt int
	if err := row.Scan(&body, &event, &attempt); err != nil {
		return nil, "", 0, err
	}
	return body, event, attempt, nil
}
