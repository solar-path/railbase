package realtime

// Tests for the PB-SDK per-record dual-fan behaviour (v1.7.36c).
//
// PB clients call `subscribe("posts/<recordId>", cb)` to filter events
// for ONE specific record — a wire pattern Railbase's native broker
// didn't support (it only emitted `<collection>/<verb>` topics). The
// broker now fans every record event out to subscribers on BOTH the
// canonical verb topic AND the per-record topic. Subscribers whose
// pattern matches the verb topic are deduped out of the per-record
// pass so a wildcard sub (`posts/*`) sees exactly one frame per
// publish — the verb topic — instead of two.
//
// Subscriber pattern              expected delivery for one
//                                 posts/create id=abc publish
//	`posts/create` literal          1 frame, topic=posts/create
//	`posts/abc` literal             1 frame, topic=posts/abc
//	`posts/*` wildcard              1 frame, topic=posts/create
//	`*/*` wildcard                  1 frame, topic=posts/create
//	`posts/create` + `posts/abc`    1 frame, topic=posts/create

import (
	"testing"
	"time"
)

// drainAll pulls every queued event off `sub.Queue()` until a short
// quiescence window elapses with no new events. Returns the slice in
// arrival order — both count AND topics matter for these assertions
// because a deduplication bug would manifest as duplicate frames.
func drainAll(sub *Subscription, quiescence time.Duration) []event {
	var out []event
	for {
		select {
		case ev := <-sub.Queue():
			out = append(out, ev)
		case <-sub.Done():
			// v1.7.38 — queue is never closed; subscription teardown
			// surfaces here. Test drains return early if the sub
			// gets Unsubscribed mid-test.
			return out
		case <-time.After(quiescence):
			return out
		}
	}
}

// topicsOf is a tiny helper for error messages — extracts just the
// topic strings from a frame slice so failures print readably.
func topicsOf(evs []event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Topic
	}
	return out
}

// TestBroker_PerRecordTopic_FansBoth verifies the headline behaviour:
// a single Publish on `posts/create` with record id "abc" reaches a
// subscriber on `posts/create` (verb topic) AND a separate subscriber
// on `posts/abc` (per-record topic). This is what enables PB SDK
// callers like `subscribe("posts/<recordId>")` to see updates on the
// same wire path as native `posts/*` subscribers.
func TestBroker_PerRecordTopic_FansBoth(t *testing.T) {
	bus, broker := makeBroker(t)

	verbSub := broker.Subscribe([]string{"posts/create"}, "u/1", "")
	defer broker.Unsubscribe(verbSub.ID)
	recordSub := broker.Subscribe([]string{"posts/abc"}, "u/2", "")
	defer broker.Unsubscribe(recordSub.ID)

	Publish(bus, RecordEvent{
		Collection: "posts",
		Verb:       VerbCreate,
		ID:         "abc",
		Record:     map[string]any{"id": "abc", "title": "dual-fan"},
	})

	verbFrames := drainAll(verbSub, 200*time.Millisecond)
	recordFrames := drainAll(recordSub, 200*time.Millisecond)

	if len(verbFrames) != 1 || verbFrames[0].Topic != "posts/create" {
		t.Fatalf("verb subscriber: got %d frames %+v, want 1 frame on posts/create",
			len(verbFrames), topicsOf(verbFrames))
	}
	if len(recordFrames) != 1 || recordFrames[0].Topic != "posts/abc" {
		t.Fatalf("per-record subscriber: got %d frames %+v, want 1 frame on posts/abc",
			len(recordFrames), topicsOf(recordFrames))
	}
	// Both legs share the same broker event id — there's one logical
	// event with two delivery topics, not two events. Resume cursors
	// stay coherent.
	if verbFrames[0].ID != recordFrames[0].ID {
		t.Errorf("legs disagree on event id: verb=%d record=%d (should match)",
			verbFrames[0].ID, recordFrames[0].ID)
	}
}

// TestBroker_PerRecordTopic_NoFanOutWithoutID verifies the guard
// against the per-record fan-out leg when the event carries no id.
// Without the guard a "deleted N rows" style aggregate would emit on
// `posts/` (empty trailing segment) which is nonsense; with the
// guard a per-record subscriber sees nothing while the verb
// subscriber still gets its frame.
func TestBroker_PerRecordTopic_NoFanOutWithoutID(t *testing.T) {
	bus, broker := makeBroker(t)

	verbSub := broker.Subscribe([]string{"posts/create"}, "u/1", "")
	defer broker.Unsubscribe(verbSub.ID)
	// A literal per-record subscriber that would catch a stray
	// empty-id fan-out — must stay silent.
	emptyIDSub := broker.Subscribe([]string{"posts/"}, "u/2", "")
	defer broker.Unsubscribe(emptyIDSub.ID)

	Publish(bus, RecordEvent{
		Collection: "posts",
		Verb:       VerbCreate,
		// ID intentionally empty.
		Record: map[string]any{"title": "no-id"},
	})

	verbFrames := drainAll(verbSub, 200*time.Millisecond)
	emptyIDFrames := drainAll(emptyIDSub, 200*time.Millisecond)

	if len(verbFrames) != 1 || verbFrames[0].Topic != "posts/create" {
		t.Fatalf("verb subscriber: got %d frames %+v, want 1 frame on posts/create",
			len(verbFrames), topicsOf(verbFrames))
	}
	if len(emptyIDFrames) != 0 {
		t.Fatalf("posts/ subscriber should receive NOTHING when id is empty, got %d %+v",
			len(emptyIDFrames), topicsOf(emptyIDFrames))
	}
}

// TestBroker_PerRecordTopic_NoInfiniteFanOut verifies the per-record
// fan-out leg cannot trigger ANOTHER per-record fan-out — the
// fan-out must terminate at depth 1. A wildcard `posts/*` subscriber
// would catch any extraneous frame; we assert it sees exactly ONE
// (the verb topic) and the broker's event id counter advanced by
// exactly one (the per-record leg shares the primary id; no new ids
// minted).
//
// The "publish to posts/abc directly" framing from the task spec is
// expressed here as the per-record leg's view of the subscriber set:
// the leg matches subscribers on `posts/abc`, and that matching pass
// MUST NOT re-enter fan-out for `posts/abc` itself.
func TestBroker_PerRecordTopic_NoInfiniteFanOut(t *testing.T) {
	bus, broker := makeBroker(t)

	// Wildcard sub matches BOTH `posts/create` AND `posts/abc`. The
	// dedup in fanOut means it should still see exactly one frame —
	// the primary verb topic. If the per-record leg fired again
	// recursively, we'd see a second frame on `posts/abc`.
	wildSub := broker.Subscribe([]string{"posts/*"}, "u/1", "")
	defer broker.Unsubscribe(wildSub.ID)
	// Literal per-record sub — verifies the per-record leg fires
	// exactly ONCE for the id "abc" (depth-1 leg), not twice (which
	// a recursive bug would produce).
	recordSub := broker.Subscribe([]string{"posts/abc"}, "u/2", "")
	defer broker.Unsubscribe(recordSub.ID)

	startID := broker.NextEventIDTesting()

	Publish(bus, RecordEvent{
		Collection: "posts",
		Verb:       VerbCreate,
		ID:         "abc",
		Record:     map[string]any{"id": "abc"},
	})

	wildFrames := drainAll(wildSub, 250*time.Millisecond)
	recordFrames := drainAll(recordSub, 250*time.Millisecond)

	if len(wildFrames) != 1 || wildFrames[0].Topic != "posts/create" {
		t.Fatalf("wildcard subscriber: got %d frames %+v, want 1 frame on posts/create (dedup must suppress per-record leg)",
			len(wildFrames), topicsOf(wildFrames))
	}
	if len(recordFrames) != 1 || recordFrames[0].Topic != "posts/abc" {
		t.Fatalf("per-record subscriber: got %d frames %+v, want 1 frame on posts/abc",
			len(recordFrames), topicsOf(recordFrames))
	}

	endID := broker.NextEventIDTesting()
	if got := endID - startID; got != 1 {
		t.Fatalf("event ids consumed: got %d, want 1 (per-record leg shares the primary id; no recursion)",
			got)
	}
}

// TestBroker_PerRecordTopic_RecordIDEqualsVerb covers the pathological
// case where a record's id string is identical to its verb (e.g.
// id="create"). The would-be per-record topic is identical to the
// verb topic; the broker must suppress the per-record leg entirely
// so the subscriber on `posts/create` doesn't receive duplicate
// frames.
func TestBroker_PerRecordTopic_RecordIDEqualsVerb(t *testing.T) {
	bus, broker := makeBroker(t)

	verbSub := broker.Subscribe([]string{"posts/create"}, "u/1", "")
	defer broker.Unsubscribe(verbSub.ID)

	Publish(bus, RecordEvent{
		Collection: "posts",
		Verb:       VerbCreate,
		ID:         "create", // collides with the verb string
		Record:     map[string]any{"id": "create"},
	})

	frames := drainAll(verbSub, 200*time.Millisecond)
	if len(frames) != 1 {
		t.Fatalf("subscriber should receive exactly one frame even when recordId==verb; got %d %+v",
			len(frames), topicsOf(frames))
	}
}

// TestBroker_PerRecordTopic_TenantFilterEnforced verifies the per-
// record fan-out leg inherits the same cross-tenant filter as the
// primary leg. RBAC/tenant filtering is per-subscriber, so a
// tenant-scoped subscriber on `posts/abc` MUST NOT receive an event
// emitted by a different tenant — even if the per-record topic
// matches their pattern.
func TestBroker_PerRecordTopic_TenantFilterEnforced(t *testing.T) {
	bus, broker := makeBroker(t)
	tenantA := "tenant-a"
	tenantB := "tenant-b"

	scopedB := broker.Subscribe([]string{"posts/abc"}, "u/1", tenantB)
	defer broker.Unsubscribe(scopedB.ID)

	Publish(bus, RecordEvent{
		Collection: "posts",
		Verb:       VerbCreate,
		ID:         "abc",
		TenantID:   tenantA, // published under tenant A
		Record:     map[string]any{"id": "abc"},
	})

	frames := drainAll(scopedB, 200*time.Millisecond)
	if len(frames) != 0 {
		t.Fatalf("tenant-B subscriber on per-record topic must NOT receive a tenant-A event; got %d %+v",
			len(frames), topicsOf(frames))
	}
}
