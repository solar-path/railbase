// Package realtime is the v1.3.0 server-sent-events fan-out.
//
// Architecture:
//
//	REST CRUD handler
//	  ↓ Publish("record.<collection>.<verb>", {id, record})
//	  ↓
//	eventbus (in-process + LISTEN/NOTIFY across replicas)
//	  ↓
//	realtime.Broker (subscribed to "record.*")
//	  ↓ for each Subscription whose topics match: enqueue
//	  ↓
//	SSE writer goroutine → drains queue → writes "event:/data:" frames
//
// Scope (v1.3.0 MVP):
//
//	- SSE transport ONLY. WebSocket lands in v1.3.x — SSE is enough
//	  for ~95% of "live update the list view" workloads, works through
//	  browser fetch + curl + EventSource, and avoids the WS upgrade /
//	  ping-pong / binary-frame surface for now.
//	- Subscription model: client connects with `?topics=posts/*,users/me`
//	  query param. Topics are dotted/slashed strings matched as
//	  `record.<collection>` (slash → dot rewrite for ergonomics).
//	  `*` matches any single segment.
//	- Auth: middleware-resolved Principal is required (no anonymous
//	  realtime). Tenant binding is honoured (cross-tenant events drop).
//	- Backpressure: per-client bounded queue (64 events default). On
//	  overflow we DROP the oldest queued event and log; the slow
//	  client doesn't stall the broker.
//	- Heartbeats: 25s keepalive (proxies often kill idle SSE at 30s).
//
// Deliberately deferred to v1.3.x:
//
//	- WebSocket transport with binary frames
//	- Resume tokens / 1000-event replay window
//	- Per-record RBAC filter (current check is "authed + collection
//	  ListRule passes"; full row-level rule integration is bigger)
//	- PB SDK drop-in compat in strict mode
//	- Cross-replica delivery is automatic via the existing PGBridge —
//	  no new code, but worth documenting in v1.3.x release notes
package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/eventbus"
)

// EventTopic is the single eventbus topic every record event flows
// through. We use one fixed topic + a structured payload (rather
// than per-(collection,verb) topics) because the existing eventbus
// only supports single-segment suffix wildcards (`auth.*` matches
// `auth.signin` but NOT `auth.signin.succeeded`) — that's too narrow
// for record events whose natural topic is `record.<coll>.<verb>`.
// Collection/verb discrimination happens at the broker → SSE step
// where we have richer matching.
const EventTopic = "record.changed"

// Event verbs published per record mutation. Stable wire identifiers.
type Verb string

const (
	VerbCreate Verb = "create"
	VerbUpdate Verb = "update"
	VerbDelete Verb = "delete"
)

// RecordEvent is the payload published to the eventbus on every
// mutation. Subscribers receive it inside an eventbus.Event whose
// Topic is `record.<collection>.<verb>`.
type RecordEvent struct {
	Collection string         `json:"collection"`
	Verb       Verb           `json:"verb"`
	ID         string         `json:"id"`
	Record     map[string]any `json:"record,omitempty"`
	TenantID   string         `json:"tenant_id,omitempty"`
	At         time.Time      `json:"at"`
}

// Publish is the helper REST handlers / hooks call to push a record
// mutation into the bus. Single fixed topic; broker discriminates
// by (collection, verb) on the subscriber side.
func Publish(bus *eventbus.Bus, e RecordEvent) {
	if bus == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	bus.Publish(eventbus.Event{
		Topic:   EventTopic,
		Payload: e,
	})
}

// BrokerConfig tunes broker behaviour. Zero value yields defaults
// suitable for production (1000-event resume buffer).
type BrokerConfig struct {
	// ReplayBufferSize is the maximum number of past events retained
	// for SSE resume. <=0 means "use default" (1000); set to a
	// negative value to disable resume entirely (no buffer kept).
	//
	// We default to 1000 because at ~200 bytes/event in JSON that's
	// only ~200KB of retained memory per broker — cheap enough to
	// keep "always on" and large enough to cover the 30-60s gap a
	// typical mobile network reconnect leaves behind.
	ReplayBufferSize int
}

// DefaultReplayBufferSize is the number of past events held for SSE
// resume when BrokerConfig.ReplayBufferSize is zero.
const DefaultReplayBufferSize = 1000

// Broker is the subscription registry. One per process. Goroutine-safe.
type Broker struct {
	log      *slog.Logger
	bus      *eventbus.Bus
	busSubID uint64

	// mu protects subs, ring (head/tail/buf), and nextID. We use a
	// single Mutex rather than separate locks because the critical
	// path — atomically (snapshot replay + register subscription) on
	// resume, and (append-to-ring + fan-out to subscribers) on
	// publish — needs both pieces consistent w.r.t. each other.
	mu     sync.Mutex
	subs   map[uuid.UUID]*Subscription
	nextID uint64 // next event id to assign (monotonic)

	// ring is a fixed-capacity circular buffer of the most recent
	// events. Allocated lazily on the first publish; nil/zero-cap
	// means "resume disabled".
	ring    []bufferedEvent
	ringPos int  // index of the next slot to write into
	ringLen int  // number of valid entries currently in the ring
	ringCap int  // cap(ring); cached so we don't realloc
}

// bufferedEvent is one entry in the resume ring. Mirrors `event` but
// carries the assigned monotonic id so consumers can ask "give me
// everything strictly after id N".
type bufferedEvent struct {
	id    uint64
	topic string
	data  []byte
}

// NewBroker constructs a Broker wired to a bus with default config.
// Caller MUST invoke Start() to attach to the bus and Stop() on
// shutdown.
func NewBroker(bus *eventbus.Bus, log *slog.Logger) *Broker {
	return NewBrokerWithConfig(bus, log, BrokerConfig{})
}

// NewBrokerWithConfig constructs a Broker with explicit configuration.
// See BrokerConfig for tunables.
func NewBrokerWithConfig(bus *eventbus.Bus, log *slog.Logger, cfg BrokerConfig) *Broker {
	if log == nil {
		log = slog.Default()
	}
	size := cfg.ReplayBufferSize
	switch {
	case size == 0:
		size = DefaultReplayBufferSize
	case size < 0:
		size = 0 // explicitly disabled
	}
	b := &Broker{
		log:     log,
		bus:     bus,
		subs:    map[uuid.UUID]*Subscription{},
		ringCap: size,
	}
	if size > 0 {
		b.ring = make([]bufferedEvent, size)
	}
	return b
}

// Start attaches the broker to the eventbus. From here on every
// record-mutation event flows through the broker's fan-out loop.
func (b *Broker) Start() {
	b.busSubID = b.bus.Subscribe(EventTopic, 256, func(_ context.Context, e eventbus.Event) {
		rec, ok := e.Payload.(RecordEvent)
		if !ok {
			return
		}
		// Derive the user-visible topic from the payload:
		// "<collection>/<verb>". Subscribers match against this.
		b.fanOut(rec.Collection+"/"+string(rec.Verb), rec)
	})
}

// Stop unsubscribes from the bus. Subscriptions remain — caller
// closes those when the HTTP connections terminate.
func (b *Broker) Stop() {
	if b == nil || b.bus == nil {
		return
	}
	b.bus.Unsubscribe(b.busSubID)
}

// Subscription is a single SSE client. Created by Subscribe;
// goroutine that drains .Queue() writes frames.
//
// Concurrency model (v1.7.38 — Unsubscribe race fix):
//   - fanOut goroutines send to `queue` via non-blocking selects in
//     enqueueOrDrop.
//   - The reader goroutine (SSE / WS handler) drains `queue` AND
//     watches `done` so it can exit cleanly when Unsubscribe fires.
//   - `done` is closed (once) by Unsubscribe.
//   - `queue` is NEVER closed. Closing a channel while another
//     goroutine is in the middle of `case q <- ev:` panics with
//     "send on closed channel"; the only way to avoid the TOCTOU
//     between `s.Closed()` and the send is to never close the
//     channel at all. The queue gets GC'd when the broker's map
//     entry is removed and the reader goroutine releases its
//     reference. Buffered events still in the queue at teardown
//     are dropped silently — fine for a torn-down subscription.
//   - `closed` (atomic) is the fast path "is this sub still alive"
//     check; senders use it to skip enqueue entirely when set.
//     `done` is the slow-path channel-select gate; senders use it
//     inside the enqueue select to drop sends that lose the race.
type Subscription struct {
	ID         uuid.UUID
	Topics     []string      // user-supplied patterns
	UserID     string        // authenticated user (collection/uuid pair)
	TenantID   string        // bound tenant (empty = no tenant scope)
	queue      chan event    // bounded; oldest dropped on overflow; NEVER closed
	done       chan struct{} // closed by Unsubscribe to signal teardown
	closed     atomic.Bool
	createdAt  time.Time
	droppedCnt atomic.Uint64
}

// event is the broker-internal envelope flowing from fanOut to the
// SSE writer goroutine. Carries enough to compose the SSE frame.
type event struct {
	ID    uint64 // monotonic broker-assigned id; encoded into SSE `id:` field
	Topic string
	Data  []byte
}

// Subscribe registers a new SSE subscription. The returned channel
// emits frames; caller's writer goroutine reads them. Close to
// signal departure.
func (b *Broker) Subscribe(topics []string, userID, tenantID string) *Subscription {
	sub, _, _ := b.SubscribeWithResume(topics, userID, tenantID, 0, false)
	return sub
}

// ResumeResult describes the outcome of a resume request. Fields:
//
//   - Replay: buffered events the caller MUST write to the wire
//     BEFORE draining sub.Queue() — they precede the live stream and
//     are ordered oldest→newest by event id.
//   - Truncated: true iff the requested since-id was older than the
//     broker's oldest retained event. The caller should emit a
//     `replay-truncated` SSE marker so the client knows it may have
//     missed events.
type ResumeResult struct {
	Replay    []event
	Truncated bool
}

// SubscribeWithResume registers a subscription AND, atomically with
// the registration, snapshots any buffered events with id > sinceID
// that match the subscription's topics + tenant filter. The replay
// slice is returned to the caller; once the caller writes those, it
// can drain sub.Queue() for live events without missing or
// duplicating any event in between.
//
// If hasSince is false, no replay snapshot is taken and Truncated is
// false. Truncated is also reported when the resume buffer is
// disabled (size 0) but a since-id was supplied — semantically the
// client must assume gap.
func (b *Broker) SubscribeWithResume(topics []string, userID, tenantID string, sinceID uint64, hasSince bool) (*Subscription, ResumeResult, error) {
	sub := &Subscription{
		ID:        uuid.Must(uuid.NewV7()),
		Topics:    cleanTopics(topics),
		UserID:    userID,
		TenantID:  tenantID,
		queue:     make(chan event, 64),
		done:      make(chan struct{}),
		createdAt: time.Now().UTC(),
	}
	var res ResumeResult
	b.mu.Lock()
	// Snapshot replay BEFORE registering so we don't double-count
	// events: any future fanOut will land in sub.queue exclusively.
	if hasSince {
		res = b.replaySinceLocked(sub, sinceID)
	}
	b.subs[sub.ID] = sub
	b.mu.Unlock()
	return sub, res, nil
}

// replaySinceLocked walks the ring buffer (oldest→newest) and
// collects events with id > sinceID that match the subscription's
// topic patterns and tenant filter. b.mu MUST be held.
func (b *Broker) replaySinceLocked(sub *Subscription, sinceID uint64) ResumeResult {
	var res ResumeResult
	if b.ringCap == 0 || b.ringLen == 0 {
		// No buffer (disabled) OR nothing buffered yet. If the
		// client expected resume but we have no data covering it,
		// flag truncation so the client knows there might be a gap.
		// (When ringLen == 0 we genuinely don't know if events were
		// missed; safer to be honest.)
		if b.ringCap == 0 {
			res.Truncated = true
		}
		return res
	}
	// Oldest entry sits at (ringPos - ringLen) mod ringCap.
	start := (b.ringPos - b.ringLen + b.ringCap) % b.ringCap
	oldestID := b.ring[start].id
	if sinceID < oldestID-1 {
		// sinceID is strictly older than oldest-1 means the client
		// missed events that have already been evicted.
		res.Truncated = true
	}
	for i := 0; i < b.ringLen; i++ {
		entry := b.ring[(start+i)%b.ringCap]
		if entry.id <= sinceID {
			continue
		}
		if !sub.matches(entry.topic) {
			continue
		}
		// We don't store TenantID per buffered event to keep the
		// ring compact — RecordEvent's tenant_id is inside the JSON
		// payload. For resume, we rely on the broker's per-event
		// fanOut having already enforced tenant scoping at
		// publish-time would be incorrect: the buffer is shared
		// across all subscribers. So we re-decode just the tenant
		// field if the subscriber is tenant-scoped. Cheap: small
		// JSON, only on resume paths.
		if sub.TenantID != "" {
			if t := peekTenantID(entry.data); t != "" && t != sub.TenantID {
				continue
			}
		}
		res.Replay = append(res.Replay, event{
			ID:    entry.id,
			Topic: entry.topic,
			Data:  entry.data,
		})
	}
	return res
}

// peekTenantID extracts the "tenant_id" field from a JSON RecordEvent
// payload without unmarshalling the whole thing. Returns "" if not
// present or empty. Used by replaySinceLocked for cross-tenant
// filtering. Tiny + allocation-free for the common no-match case.
func peekTenantID(data []byte) string {
	var probe struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ""
	}
	return probe.TenantID
}

// Unsubscribe removes a subscription. Idempotent.
//
// v1.7.38 — the previous version closed `sub.queue` here, which
// raced with any in-flight fanOut goroutine doing a non-blocking
// `case sub.queue <- ev` send. "send on closed channel" panics
// surfaced under load + race-detector ran them readily. The fix:
// close a separate `done` channel instead, signalling teardown to
// the reader goroutine + to enqueueOrDrop's select. The queue
// itself is never closed and gets GC'd when the reader releases
// its reference.
//
// CompareAndSwap on `closed` makes this idempotent — repeat calls
// to Unsubscribe (defer + manual close, etc.) are safe; we close
// `done` exactly once.
func (b *Broker) Unsubscribe(id uuid.UUID) {
	b.mu.Lock()
	sub, ok := b.subs[id]
	if ok {
		delete(b.subs, id)
	}
	b.mu.Unlock()
	if !ok {
		return
	}
	if sub.closed.CompareAndSwap(false, true) {
		close(sub.done)
	}
}

// Queue exposes the bounded channel the SSE writer drains.
// Callers MUST also watch `Done()` — `queue` is never closed
// (closing a channel mid-send would race with fanOut), so
// reader goroutines need the done signal to know when to exit.
func (s *Subscription) Queue() <-chan event { return s.queue }

// Done returns a channel that's closed when the subscription has
// been Unsubscribed. Used by reader goroutines (SSE/WS handlers)
// to exit promptly when the broker tears the sub down.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Closed reports whether the subscription has been Unsubscribed.
func (s *Subscription) Closed() bool { return s.closed.Load() }

// Dropped returns the count of events backpressure-dropped for
// this subscription (admin UI surfaces this).
func (s *Subscription) Dropped() uint64 { return s.droppedCnt.Load() }

// fanOut dispatches one event to every subscription whose topic
// pattern matches. Per-subscription enqueue is non-blocking — on
// overflow we drop the OLDEST queued event and stamp dropped count.
//
// The bus → broker hop happens on a single goroutine (eventbus
// dispatches in serialised per-subscription order). The broker
// fanOut → per-client enqueue path is non-blocking, so a slow
// SSE consumer doesn't stall the bus.
//
// PB-SDK compat: alongside the primary `<collection>/<verb>` topic,
// every record event with a non-empty `rec.ID` ALSO fans out to
// subscribers whose pattern matches the per-record topic
// `<collection>/<recordId>` (e.g. `posts/abc123`). This lets PB SDK
// callers do `subscribe("posts/<recordId>")` to filter events for
// ONE specific record — the wire pattern PB's RealtimeService uses.
//
// To avoid double-delivery to wildcard subscribers (e.g. `posts/*`
// matches BOTH `posts/create` and `posts/abc123`), the per-record
// pass filters OUT any subscriber that already matched the primary
// topic. Net result:
//
//	pattern              primary fan      per-record fan
//	`posts/create`       ✓                — (no match)
//	`posts/abc`          — (no match)     ✓
//	`posts/*`            ✓                — (already got primary)
//	`*/*`                ✓                — (already got primary)
//
// We deliberately do NOT store a second ring entry for the
// per-record topic — that would double resume-buffer memory + event
// IDs for no gain (per-record subscribers are inherently transient,
// scoped to a single visible record). Per-record resume is a
// recognised gap, tracked for a future slice. Live-stream delivery
// is the 95% case we ship now.
func (b *Broker) fanOut(userTopic string, rec RecordEvent) {
	body, err := json.Marshal(rec)
	if err != nil {
		b.log.Warn("realtime: marshal event", "err", err)
		return
	}

	b.mu.Lock()
	// Assign monotonic id. We start at 1 — id 0 is reserved as the
	// sentinel "no events seen yet" value for resume callers.
	b.nextID++
	id := b.nextID
	primaryEv := event{ID: id, Topic: userTopic, Data: body}

	// Append to the resume ring (drop-oldest semantics: we just
	// overwrite the next slot and advance). The ring is intentionally
	// shared across topics; per-topic resume buffers would multiply
	// memory by topic-count for marginal benefit. The per-record
	// topic shares this entry — see function doc.
	if b.ringCap > 0 {
		b.ring[b.ringPos] = bufferedEvent{id: id, topic: userTopic, data: body}
		b.ringPos = (b.ringPos + 1) % b.ringCap
		if b.ringLen < b.ringCap {
			b.ringLen++
		}
	}

	subs := make([]*Subscription, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	// Per-record topic is computed once outside the loop; "" means
	// "no per-record fan-out for this event" (record without an id,
	// or id collides with the verb so the two topics are identical).
	perRecordTopic := ""
	if rec.ID != "" && isVerbTopic(userTopic, rec.Collection, rec.Verb) {
		candidate := rec.Collection + "/" + rec.ID
		if candidate != userTopic {
			perRecordTopic = candidate
		}
	}

	for _, s := range subs {
		if s.Closed() {
			continue
		}
		matchedPrimary := s.matches(userTopic)
		// Tenant filter: when the subscriber is bound to a tenant
		// AND the event carries a different tenant, drop. (Empty
		// tenant_id on the event means "site-scoped" — visible to
		// everyone.)
		tenantOK := s.TenantID == "" || rec.TenantID == "" || s.TenantID == rec.TenantID
		if matchedPrimary && tenantOK {
			enqueueOrDrop(s, primaryEv)
			continue
		}
		// Primary didn't match (or tenant-filtered) — try the
		// per-record topic. Subscribers with a literal per-record
		// pattern like `posts/abc` land here.
		if perRecordTopic == "" || !tenantOK {
			continue
		}
		if !s.matches(perRecordTopic) {
			continue
		}
		// Same event id as the primary — there's only one logical
		// event flowing, two delivery topics. SSE/WS clients see
		// `id: N event: posts/abc` on this leg vs `id: N event:
		// posts/create` on the primary leg, never both for one
		// subscription.
		enqueueOrDrop(s, event{ID: id, Topic: perRecordTopic, Data: body})
	}
}

// enqueueOrDrop pushes one event onto sub.queue with non-blocking,
// drop-oldest semantics. Factored out so the per-record fan-out leg
// can share the exact backpressure behaviour as the primary leg.
//
// v1.7.38 — the send is now gated by `sub.done` so a teardown
// initiated by Unsubscribe (while this fanOut was in flight past
// the s.Closed() check) drops the event instead of blocking OR
// panicking. Without this gate, a slow-consumer subscription that
// got Unsubscribed mid-publish would have hit a "send on closed
// channel" panic in production — both the v1.7.36b and v1.7.37b
// agents flagged the race independently.
//
// Note: queue is NEVER closed (see Subscription doc). Done IS the
// teardown signal. The select-on-done lines drop events that would
// otherwise either block forever (queue full + reader gone) or
// race with a future closer.
func enqueueOrDrop(s *Subscription, ev event) {
	// Fast-path bail when the sub is already torn down. Saves the
	// select setup cost on a common case (publish-after-disconnect).
	if s.Closed() {
		return
	}
	select {
	case <-s.done:
		// Lost the race — sub was Unsubscribed between Closed()
		// check and here. Drop the event.
		return
	case s.queue <- ev:
		// Happy path: enqueued.
		return
	default:
		// Queue full. Drain one + retry; the dropped count is the
		// signal to operators that the client can't keep up.
		select {
		case <-s.queue:
			s.droppedCnt.Add(1)
		default:
		}
		select {
		case <-s.done:
			return
		case s.queue <- ev:
		default:
			// Still full (race with another fanOut). Give up; the
			// next event flows through.
			s.droppedCnt.Add(1)
		}
	}
}

// matches reports whether the subscription's topic patterns admit
// `topic`. Pattern syntax: literal segments + `*` single-segment
// wildcard. Examples:
//
//	"posts/*"        matches  posts/create, posts/update, posts/delete
//	"posts/create"   matches  posts/create only
//	"*/create"       matches  any-collection create
//	"*"              matches  any 1-segment topic (rarely useful)
//
// A subscription with zero topics matches NOTHING (defensive — we
// don't want a forgetful client to subscribe to everything by
// accident; the SSE handler rejects zero-topic queries at request
// time too).
func (s *Subscription) matches(topic string) bool {
	for _, p := range s.Topics {
		if topicMatch(p, topic) {
			return true
		}
	}
	return false
}

// isVerbTopic reports whether `topic` is the canonical
// `<collection>/<verb>` shape for THIS RecordEvent — i.e. matches
// `<rec.Collection>/<rec.Verb>` exactly. We compare against the
// event's own collection + verb rather than parsing the topic
// structurally so the check is unambiguous even when a recordId
// happens to be a single-segment string.
//
// Used by fanOut to decide whether the per-record fan-out leg
// applies. Returning false suppresses the per-record fan-out
// entirely — useful as a guard against future callers passing a
// pre-rewritten topic that isn't in the canonical verb shape.
func isVerbTopic(topic, collection string, verb Verb) bool {
	if collection == "" || verb == "" {
		return false
	}
	return topic == collection+"/"+string(verb)
}

func topicMatch(pattern, topic string) bool {
	pp := strings.Split(pattern, "/")
	tp := strings.Split(topic, "/")
	if len(pp) != len(tp) {
		return false
	}
	for i := range pp {
		if pp[i] == "*" {
			continue
		}
		if pp[i] != tp[i] {
			return false
		}
	}
	return true
}

// Phase 3.x roadmap — server-side topic filtering (Sentinel B4).
//
// Sentinel's `project-screen.tsx:93-104` subscribes to `tasks/*`
// (every task event in the system) and refetches the full list on
// any event because there's no way to scope by project. The proper
// fix needs three pieces:
//
//   1. Topic-filter syntax extension. Reserve `?` in pattern strings
//      to introduce filter predicates:
//          tasks/*?project=<uuid>
//          tasks/update?owner=<uuid>&priority=high
//      Multiple predicates AND together.
//
//   2. Pre-publish predicate evaluation. The broker now needs to
//      look at the RecordEvent's `Payload` map and confirm every
//      predicate matches BEFORE adding the subscriber to the fan-
//      out set. Touch site: matchesWithPayload(sub, topic, payload).
//
//   3. SSE / WS handler validation. Reject patterns with predicates
//      pointing at non-existent columns (early error, not silent
//      no-match), and reject predicates referencing private columns
//      (password_hash, token_key).
//
// Punt rationale: shipping this requires a broker-wide invariant
// change (`matches(topic)` becomes `matches(topic, payload)`) plus
// new SSE/POST API surface for filter syntax. Not done in this
// patch; tracked here so the next person picking it up doesn't have
// to re-derive the design.

// cleanTopics strips whitespace + drops empties so a stray comma in
// the query string doesn't widen the subscription unexpectedly.
func cleanTopics(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Stats returns a snapshot of the broker's subscriptions. Admin UI
// uses it for the realtime monitor panel (v1.3.x).
type Stats struct {
	SubscriptionCount int          `json:"subscription_count"`
	Subscriptions     []SubStats   `json:"subscriptions,omitempty"`
}

// SubStats is the per-subscription admin-visible state.
type SubStats struct {
	ID        uuid.UUID `json:"id"`
	UserID    string    `json:"user_id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	Topics    []string  `json:"topics"`
	CreatedAt time.Time `json:"created_at"`
	Dropped   uint64    `json:"dropped"`
}

// Snapshot returns the current Stats. Cheap — single lock.
func (b *Broker) Snapshot() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := Stats{
		SubscriptionCount: len(b.subs),
	}
	for _, s := range b.subs {
		out.Subscriptions = append(out.Subscriptions, SubStats{
			ID:        s.ID,
			UserID:    s.UserID,
			TenantID:  s.TenantID,
			Topics:    append([]string(nil), s.Topics...),
			CreatedAt: s.createdAt,
			Dropped:   s.Dropped(),
		})
	}
	return out
}

// silence imports kept for future expansion
var _ = fmt.Sprintf
