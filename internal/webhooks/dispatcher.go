package webhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/realtime"
)

// JobKind is the kind string registered with the jobs registry. The
// delivery handler claims rows of this kind off the queue.
const JobKind = "webhook_deliver"

// DispatcherDeps is what Start needs from the host application. We
// take pointers explicitly so this package never imports app/server.
type DispatcherDeps struct {
	Store     *Store
	Bus       *eventbus.Bus
	JobsStore *jobs.Store
	Log       *slog.Logger
}

// Start subscribes the dispatcher to the eventbus and returns a
// cancel func to unsubscribe. Idempotent — call once during boot.
func Start(ctx context.Context, d DispatcherDeps) (cancel func(), err error) {
	if d.Store == nil || d.Bus == nil || d.JobsStore == nil {
		return nil, fmt.Errorf("webhooks: store, bus, and jobsstore required")
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	// Subscribe to the realtime record-changed topic. Buffer is
	// modest — record events are bursty, but the dispatcher writes
	// only a DB row + an Enqueue per match, both <10ms.
	subID := d.Bus.Subscribe(realtime.EventTopic, 256, func(ctx context.Context, e eventbus.Event) {
		rec, ok := e.Payload.(realtime.RecordEvent)
		if !ok {
			return
		}
		dispatch(ctx, d, rec)
	})
	return func() { d.Bus.Unsubscribe(subID) }, nil
}

// dispatch fans one RecordEvent out to every matching webhook.
//
// We INSERT a delivery row first (status=pending) so the admin UI
// shows the attempt immediately, then enqueue the job. If the job
// worker dies between INSERT and Enqueue, the row is orphaned —
// recovery: a periodic sweep (deferred) can mark stale `pending` rows
// as `dead` after some grace.
func dispatch(ctx context.Context, d DispatcherDeps, rec realtime.RecordEvent) {
	topic := fmt.Sprintf("record.%s.%s", rec.Verb, rec.Collection)
	hooks, err := d.Store.ListActiveMatching(ctx, topic)
	if err != nil {
		d.Log.Error("webhooks: list matching", "err", err)
		return
	}
	if len(hooks) == 0 {
		return
	}
	body, err := json.Marshal(eventPayload{
		Event:      topic,
		Collection: rec.Collection,
		Verb:       string(rec.Verb),
		ID:         rec.ID,
		Record:     rec.Record,
		TenantID:   rec.TenantID,
		At:         rec.At,
	})
	if err != nil {
		d.Log.Error("webhooks: marshal payload", "err", err)
		return
	}
	for _, w := range hooks {
		del, err := d.Store.InsertDelivery(ctx, w.ID, topic, body, 1)
		if err != nil {
			d.Log.Error("webhooks: insert delivery", "webhook", w.Name, "err", err)
			continue
		}
		if _, err := d.JobsStore.Enqueue(ctx, JobKind, deliveryPayload{
			DeliveryID: del.ID.String(),
			WebhookID:  w.ID.String(),
		}, jobs.EnqueueOptions{MaxAttempts: w.MaxAttempts}); err != nil {
			d.Log.Error("webhooks: enqueue", "webhook", w.Name, "err", err)
		}
	}
}

// eventPayload is the JSON shape POSTed to webhooks. Mirrors docs/21.
type eventPayload struct {
	Event      string         `json:"event"`
	Collection string         `json:"collection"`
	Verb       string         `json:"verb"`
	ID         string         `json:"id"`
	Record     map[string]any `json:"data,omitempty"`
	TenantID   string         `json:"tenant_id,omitempty"`
	At         time.Time      `json:"ts"`
}

// deliveryPayload is the jobs.payload for a webhook_deliver job —
// just two ids; the handler loads the rest.
type deliveryPayload struct {
	DeliveryID string `json:"delivery_id"`
	WebhookID  string `json:"webhook_id"`
}
