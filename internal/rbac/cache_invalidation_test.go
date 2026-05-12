package rbac

// Bus-driven invalidation tests — confirm SubscribeInvalidation wires
// the resolver cache to every rbac.role_* topic such that a publish
// purges every cached Resolved set.
//
// Strategy: each test stands up a fresh eventbus, calls
// SubscribeInvalidation, seeds the resolver cache via the same
// GetOrLoad path cachedResolve takes, publishes the topic under test,
// then waits (briefly) for the async handler to drain and asserts the
// cache is empty.
//
// We do NOT exercise the Store.Grant/etc. publish call sites here —
// those need a live Postgres and are covered by the package's
// end-to-end test. This file isolates the cache-invalidation contract
// so it can run pure-Go.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/eventbus"
)

// seedCache puts one entry into resolverCache via the same path
// cachedResolve takes (so the test is a true smoke test of the
// invalidation contract, not a contrived map mutation).
func seedCache(t *testing.T) resolverKey {
	t.Helper()
	uid := uuid.New()
	key := resolverKey{collectionName: "users", recordID: uid}
	loaded := makeResolved(uid, nil, "settings.read")
	if _, err := resolverCache.GetOrLoad(context.Background(), key,
		func(_ context.Context) (*Resolved, error) { return loaded, nil },
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := resolverCache.Stats().Size; got != 1 {
		t.Fatalf("seed: cache size after load = %d, want 1", got)
	}
	return key
}

// waitForEmpty polls resolverCache.Stats().Size until it hits 0 or the
// deadline elapses. The bus is async; we can't synchronously observe
// the handler completing without exporting an internal hook, so the
// poll is the simplest correct approach.
func waitForEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if resolverCache.Stats().Size == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cache did not empty within 500ms; size=%d", resolverCache.Stats().Size)
}

// publishAndAssertPurge is the body shared by every per-topic test.
// It seeds the cache, fires `topic` once on a fresh bus, then asserts
// the cache empties asynchronously.
func publishAndAssertPurge(t *testing.T, topic string, payload RoleEvent) {
	t.Helper()
	resolverCache.Clear()

	bus := eventbus.New(nil)
	defer bus.Close()
	SubscribeInvalidation(bus)

	seedCache(t)
	bus.Publish(eventbus.Event{Topic: topic, Payload: payload})
	waitForEmpty(t)
}

func TestCacheInvalidation_OnRoleGranted(t *testing.T) {
	publishAndAssertPurge(t, TopicRoleGranted, RoleEvent{
		Role:   uuid.New().String(),
		Action: "settings.read",
	})
}

func TestCacheInvalidation_OnRoleRevoked(t *testing.T) {
	publishAndAssertPurge(t, TopicRoleRevoked, RoleEvent{
		Role:   uuid.New().String(),
		Action: "settings.write",
	})
}

func TestCacheInvalidation_OnRoleAssigned(t *testing.T) {
	publishAndAssertPurge(t, TopicRoleAssigned, RoleEvent{
		Role:   uuid.New().String(),
		Actor:  "users",
		UserID: uuid.New().String(),
		Tenant: uuid.New().String(),
	})
}

func TestCacheInvalidation_OnRoleUnassigned(t *testing.T) {
	publishAndAssertPurge(t, TopicRoleUnassigned, RoleEvent{
		Role:   uuid.New().String(),
		Actor:  "users",
		UserID: uuid.New().String(),
	})
}

func TestCacheInvalidation_OnRoleDeleted(t *testing.T) {
	publishAndAssertPurge(t, TopicRoleDeleted, RoleEvent{
		Role: uuid.New().String(),
	})
}

// TestCacheInvalidation_NilBus_NoOp confirms the nil-bus guard:
// SubscribeInvalidation(nil) must not panic, and the resolver cache
// continues to work normally (the package-global is unaffected).
func TestCacheInvalidation_NilBus_NoOp(t *testing.T) {
	resolverCache.Clear()

	// Must not panic.
	SubscribeInvalidation(nil)

	// Cache continues to function normally: a load populates, a hit
	// returns the same pointer.
	uid := uuid.New()
	key := resolverKey{collectionName: "users", recordID: uid}
	var loads atomic.Int32
	loader := func(_ context.Context) (*Resolved, error) {
		loads.Add(1)
		return makeResolved(uid, nil), nil
	}
	r1, err := resolverCache.GetOrLoad(context.Background(), key, loader)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r2, err := resolverCache.GetOrLoad(context.Background(), key, loader)
	if err != nil {
		t.Fatalf("hit: %v", err)
	}
	if r1 != r2 {
		t.Errorf("nil-bus path broke caching: %p vs %p", r1, r2)
	}
	if got := loads.Load(); got != 1 {
		t.Errorf("loader ran %d times; want 1 (hit should be served from cache)", got)
	}
}

// TestCacheInvalidation_UnrelatedTopicIgnored is a guard: only the
// rbac.role_* topics should trigger a purge. A random unrelated topic
// firing on the same bus must not flush the cache.
func TestCacheInvalidation_UnrelatedTopicIgnored(t *testing.T) {
	resolverCache.Clear()

	bus := eventbus.New(nil)
	defer bus.Close()
	SubscribeInvalidation(bus)

	seedCache(t)
	bus.Publish(eventbus.Event{Topic: "settings.changed", Payload: nil})

	// Wait a short window then confirm the cache is STILL populated.
	// We can't easily wait for "nothing happened" so we give the bus
	// a generous slice to deliver and then check.
	time.Sleep(50 * time.Millisecond)
	if got := resolverCache.Stats().Size; got != 1 {
		t.Errorf("unrelated topic purged the cache; size=%d, want 1", got)
	}
}
