package rbac

// Resolved-actor cache — wires the v1.5.1 internal/cache primitive to
// Store.Resolve (§3.9.4 — "per-subsystem wiring deferred").
//
// Why cache resolved actors:
//
//   - Every admin-route hit triggers a Resolve walk: one query for the
//     user's roles (joined to _roles), then a second query for every
//     action across those roles. That's two round-trips on the hot
//     path of EVERY request that goes through the RBAC middleware.
//   - Roles don't churn during a typical session — a logged-in admin
//     keeps the same role set across hundreds of requests. The
//     middleware already collapses repeated resolves WITHIN one
//     request (the resolveHandle sync.Once); this cache extends that
//     to ACROSS requests.
//
// Cache config (per docs/14 cache spec):
//
//   - Capacity 1024: roughly one entry per concurrent session in a
//     typical small/medium deployment. Headroom for traffic spikes;
//     the LRU absorbs the overflow.
//   - TTL 5 minutes: short enough that role grants/revocations
//     propagate within the window without requiring event-driven
//     invalidation infrastructure to be in place; long enough to
//     absorb the tight request bursts that are the main motivation.
//
// Invalidation:
//
//	Bus-driven (the live path): Store.Grant / Revoke / Assign /
//	Unassign / DeleteRole publish on the rbac.role_* topics (see
//	events.go). SubscribeInvalidation wires this package's cache to
//	those topics — every mutation triggers a Purge within
//	bus-delivery latency (single-digit milliseconds). The 5-minute
//	TTL remains as a backstop for events lost under bus load.
//
//	PurgeResolverCache is retained as a manual escape hatch for the
//	admin Cache inspector's "Clear" button and for callers that need
//	to force a flush synchronously (a mutation followed immediately
//	by a recheck in the same goroutine, where waiting for the async
//	bus delivery would race the read). New mutation sites should
//	rely on the bus subscription instead — the manual purge is now
//	a niche tool, not the primary mechanism.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/cache"
	"github.com/railbase/railbase/internal/eventbus"
)

// resolverKey is the composite cache key. uuid.UUID is comparable so a
// struct works directly with cache.New[K, V] (which only requires
// K comparable). Storing the components as typed fields — rather than
// flattening to a `fmt.Sprintf("%s|%s|%s", …)` string — avoids the
// allocation per lookup and keeps the key self-documenting.
//
// tenantID is a value (not a pointer) so the struct is comparable;
// the zero UUID encodes "no tenant" (site-scoped resolve). This
// matches the convention used everywhere else in the package where
// uuid.Nil distinguishes a missing tenant from a real one.
type resolverKey struct {
	collectionName string
	recordID       uuid.UUID
	tenantID       uuid.UUID
}

// resolverCache is the package-private cache instance. Wrapped by the
// helper functions below — callers don't reach in directly.
//
// 5-minute TTL aligns with the docs/14 spec recommendation: short
// enough to bound staleness on role changes (the upper bound on "user
// keeps revoked privileges" in the no-event-publisher case), long
// enough that the LRU + TTL combo absorbs the burst patterns the
// cache is sized to handle.
var resolverCache = cache.New[resolverKey, *Resolved](cache.Options{
	Capacity: 1024,
	TTL:      5 * time.Minute,
})

func init() {
	// Register with the global cache directory (v1.7.24b admin Cache
	// inspector reads from cache.All). Name follows the docs/14
	// convention of `<subsystem>.<purpose>`.
	cache.Register("rbac.resolver", resolverCache)
}

// cachedResolve wraps Store.Resolve with the resolverCache. Concurrent
// requests for the same actor share a single resolve via the cache's
// singleflight; the result is then served from the cache until the
// 5-minute TTL elapses or PurgeResolverCache is invoked.
//
// The Resolved struct is shared across callers — its fields are read
// only (Has / HasAny). The Actions map is never mutated after Resolve
// returns. Callers that need a private copy should clone first; in
// the current codebase no such caller exists.
//
// Unauthenticated requests (recordID == uuid.Nil) bypass the cache —
// the middleware already short-circuits to an empty Resolved for that
// path, and caching a per-request empty struct would waste an entry
// without saving any work.
func cachedResolve(ctx context.Context, store *Store, collectionName string, recordID uuid.UUID, tenantID *uuid.UUID) (*Resolved, error) {
	if store == nil || recordID == uuid.Nil {
		// No-op path — matches the existing middleware behaviour for
		// guest requests (return the empty Resolved without touching
		// the DB or the cache).
		return &Resolved{
			UserCollection: collectionName,
			UserID:         recordID,
			TenantID:       tenantID,
		}, nil
	}
	key := resolverKey{
		collectionName: collectionName,
		recordID:       recordID,
	}
	if tenantID != nil {
		key.tenantID = *tenantID
	}
	return resolverCache.GetOrLoad(ctx, key, func(ctx context.Context) (*Resolved, error) {
		return store.Resolve(ctx, collectionName, recordID, tenantID)
	})
}

// PurgeResolverCache drops every cached entry. Intended for admin
// handlers that perform role mutations (Grant / Revoke / Assign /
// Unassign / DeleteRole) and want to force an immediate revalidation
// rather than waiting up to 5 minutes for TTL expiry. Also wired by
// the admin Cache inspector's Clear button (via cache.Clear).
//
// As of v1.7.31d the resolver cache subscribes to rbac.role_* bus
// events (see SubscribeInvalidation), so most callers no longer need
// to call this directly — the publish happens inside the Store
// mutation methods. PurgeResolverCache survives as a synchronous
// escape hatch: callers that mutate AND read inside the same goroutine
// can't wait for the async bus delivery and must purge inline.
//
// Cheap (~O(shards) lock+unlock). Idempotent.
func PurgeResolverCache() {
	resolverCache.Purge()
}

// SubscribeInvalidation wires the resolver cache to the rbac.role_*
// eventbus topics so role mutations propagate within bus-delivery
// latency instead of waiting for the 5-minute TTL.
//
// Called from app.go once, immediately after the Store is constructed
// with the same Bus instance. The Store publishes on every successful
// Grant / Revoke / Assign / Unassign / DeleteRole; this subscription
// Purges the entire cache on receipt.
//
// Coarse-grained Purge is the right call here: fine-grained
// invalidation would require knowing which resolverKey entries each
// event affects, which is a non-trivial reverse mapping (an actor
// holds multiple roles; a single Grant changes resolution for every
// user holding that role across every tenant scope). A full Purge is
// one map allocation per shard — cheap enough that the complexity of
// tracking those reverse links isn't worth it. Stats counters are
// preserved (Purge, not Clear) so operators continue to see hit/miss
// trends through the invalidation events.
//
// Idempotent in the "calls don't panic" sense: calling SubscribeInvalidation
// twice on the same bus registers two subscriptions, so each event
// triggers two Purges. The cache still converges to the right state;
// operators just see duplicate invalidation work in metrics. Don't do
// it on purpose, but don't worry if a defensive caller does.
//
// A nil bus is a no-op — the CLI tool and tests construct Stores
// without a bus and would otherwise crash here.
func SubscribeInvalidation(bus *eventbus.Bus) {
	if bus == nil {
		return
	}
	handler := func(_ context.Context, _ eventbus.Event) {
		resolverCache.Purge()
	}
	// One subscription per topic. Buffer 16 absorbs admin-UI batch
	// operations (granting 10 actions in a single form submit) without
	// drops. The handler is fast (single Purge call), so it drains the
	// buffer well ahead of any realistic publish rate.
	bus.Subscribe(TopicRoleGranted, 16, handler)
	bus.Subscribe(TopicRoleRevoked, 16, handler)
	bus.Subscribe(TopicRoleAssigned, 16, handler)
	bus.Subscribe(TopicRoleUnassigned, 16, handler)
	bus.Subscribe(TopicRoleDeleted, 16, handler)
}
