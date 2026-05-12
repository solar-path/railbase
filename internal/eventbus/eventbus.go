// Package eventbus is the in-process publish/subscribe bus.
//
// Per docs/02-architecture.md it's the spine that lets modules
// announce state changes without taking direct dependencies on each
// other:
//
//	settings → cache invalidator
//	auth → audit writer
//	migrate → schema-snapshot store
//	hooks runtime → realtime broker
//
// v0.5 ships only the in-process variant. v0.6 layers Postgres
// LISTEN/NOTIFY on top so events cross processes (cluster, sidecars).
//
// Topic shape: dotted namespace, lower-case, no whitespace
// (`settings.changed`, `auth.signin.succeeded`,
// `migrations.applied`). Subscribers can match exact names or use
// the `*` suffix wildcard once — `auth.*` matches `auth.signin` but
// not `auth.signin.succeeded`. Two-level wildcards (`auth.**`)
// are not supported in v0.5; if you need them, subscribe twice.
//
// Concurrency: Publish is fire-and-forget on each subscriber's
// goroutine. Slow subscribers do NOT block the publisher — there's
// a per-subscriber buffered channel (default 64 events). When the
// buffer fills the publisher logs a drop at WARN and continues.
package eventbus

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// Event is what flows through the bus. Topic identifies the kind
// (`settings.changed`); Payload is opaque — typically a struct the
// publisher's package defines and subscribers type-assert to.
type Event struct {
	Topic   string
	Payload any
}

// Handler is the subscriber callback. ctx is the bus's lifecycle
// context — it cancels on Bus.Close so handlers can drop work.
type Handler func(ctx context.Context, e Event)

// Bus is the publish/subscribe engine. Construct via New.
type Bus struct {
	log *slog.Logger

	mu   sync.RWMutex
	subs []*subscription
	// syncSubs are inline subscribers: PublishSync calls their Handler
	// directly on the publisher's goroutine. Stored separately from
	// async subs so the hot async fan-out path stays untouched. Used
	// by subsystems that need to MUTATE the event payload or VETO the
	// operation (e.g. mailer.before_send), where buffered async
	// delivery would arrive too late to affect the caller's outcome.
	syncSubs []*syncSubscription

	// nextID feeds Subscribe so Unsubscribe can find the right
	// subscription by stable handle. atomic so we don't widen the
	// mutex's contention surface.
	nextID atomic.Uint64

	closed atomic.Bool
}

// New returns a Bus. log may be nil; uses slog.Default() in that case.
func New(log *slog.Logger) *Bus {
	if log == nil {
		log = slog.Default()
	}
	return &Bus{log: log}
}

// subscription captures one subscriber. queue feeds the per-sub
// goroutine; closing queue terminates it.
type subscription struct {
	id      uint64
	pattern string
	queue   chan Event
	done    chan struct{}
}

// syncSubscription is the inline counterpart used by SubscribeSync /
// PublishSync. No queue, no goroutine — PublishSync calls fn directly.
type syncSubscription struct {
	id      uint64
	pattern string
	fn      Handler
}

// Subscribe registers fn for events matching topic pattern. Pattern
// is either a literal (`settings.changed`) or ends with `.*` for
// single-segment wildcard. Returns a handle Unsubscribe consumes.
//
// bufSize is the per-subscriber buffer. Pass 0 for the default (64).
// Subscribers that cannot keep up will see events dropped — the bus
// logs a WARN per drop so the operator can tune buf or split the
// subscriber.
func (b *Bus) Subscribe(pattern string, bufSize int, fn Handler) uint64 {
	if bufSize <= 0 {
		bufSize = 64
	}
	sub := &subscription{
		id:      b.nextID.Add(1),
		pattern: pattern,
		queue:   make(chan Event, bufSize),
		done:    make(chan struct{}),
	}
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()

	go func() {
		// Each subscriber runs on its own goroutine. context.Background
		// is fine — the queue close is the lifecycle signal.
		ctx := context.Background()
		for e := range sub.queue {
			fn(ctx, e)
		}
		close(sub.done)
	}()
	return sub.id
}

// Unsubscribe removes the subscription by id. Idempotent for unknown ids.
// Blocks until the subscriber goroutine exits.
//
// Handles both async (Subscribe) and sync (SubscribeSync) handles —
// ids share one numeric space so the caller doesn't need to know which
// flavour it owns.
func (b *Bus) Unsubscribe(id uint64) {
	b.mu.Lock()
	var found *subscription
	for i, s := range b.subs {
		if s.id == id {
			found = s
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			break
		}
	}
	if found == nil {
		for i, s := range b.syncSubs {
			if s.id == id {
				b.syncSubs = append(b.syncSubs[:i], b.syncSubs[i+1:]...)
				break
			}
		}
	}
	b.mu.Unlock()
	if found != nil {
		close(found.queue)
		<-found.done
	}
}

// SubscribeSync registers fn for INLINE delivery: PublishSync invokes
// it on the publisher's goroutine, so fn can mutate event payloads
// (pointer fields) and the caller sees the changes before returning.
//
// Pattern syntax matches Subscribe (literal or trailing `.*`). Sync
// subscribers do NOT receive events from Publish — async and sync are
// two separate fan-outs. Pick one per topic.
//
// Sync subscribers run serially in the order they registered. A slow
// or panicking subscriber blocks the publisher — keep them fast.
func (b *Bus) SubscribeSync(pattern string, fn Handler) uint64 {
	sub := &syncSubscription{
		id:      b.nextID.Add(1),
		pattern: pattern,
		fn:      fn,
	}
	b.mu.Lock()
	b.syncSubs = append(b.syncSubs, sub)
	b.mu.Unlock()
	return sub.id
}

// Publish broadcasts e to every matching subscriber. Returns
// immediately — the actual delivery happens on subscriber goroutines.
// Drops events on full buffers and logs a WARN.
func (b *Bus) Publish(e Event) {
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	subs := append([]*subscription(nil), b.subs...)
	b.mu.RUnlock()

	for _, s := range subs {
		if !match(s.pattern, e.Topic) {
			continue
		}
		select {
		case s.queue <- e:
		default:
			b.log.Warn("eventbus: dropped event (subscriber buffer full)",
				"topic", e.Topic, "pattern", s.pattern)
		}
	}
}

// PublishSync invokes every matching sync subscriber INLINE on the
// caller's goroutine, in subscription order. Use this when subscribers
// must observe or mutate a payload before the publisher proceeds (e.g.
// before-send hooks that may veto an outbound email).
//
// ctx is forwarded to each Handler so subscribers can honour cancellation.
//
// Async subscribers registered via Subscribe do NOT see PublishSync
// events. If you need both, publish twice or have the sync subscriber
// re-emit asynchronously.
func (b *Bus) PublishSync(ctx context.Context, e Event) {
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	subs := append([]*syncSubscription(nil), b.syncSubs...)
	b.mu.RUnlock()

	for _, s := range subs {
		if !match(s.pattern, e.Topic) {
			continue
		}
		s.fn(ctx, e)
	}
}

// Close shuts every subscription down and refuses subsequent Publishes.
// Safe to call multiple times.
func (b *Bus) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	subs := b.subs
	b.subs = nil
	b.syncSubs = nil
	b.mu.Unlock()

	for _, s := range subs {
		close(s.queue)
		<-s.done
	}
}

// match implements the simple wildcard rule: literal equality, OR
// pattern ending in ".*" matching any single dotted segment.
func match(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := pattern[:len(pattern)-1] // keeps trailing dot
		if !strings.HasPrefix(topic, prefix) {
			return false
		}
		// Reject deeper segments — `auth.*` matches `auth.signin`
		// but NOT `auth.signin.succeeded`. v0.5 keeps wildcards single-
		// level; ** lands later if usage demands it.
		return !strings.ContainsRune(topic[len(prefix):], '.')
	}
	return false
}
