package cache

// Registry — package-global directory of named cache instances for
// admin-UI observability.
//
// Why a package-global instead of a per-app handle:
//   - Most caches are constructed deep in some subsystem (roles
//     resolver, settings manager, jobs scheduler) and never bubble up
//     to app.go cleanly. Forcing every constructor to thread a
//     `*Registry` through its signature would touch every wire-up site
//     in the codebase.
//   - The admin UI just needs "list everyone who said `cache.Register`"
//     — there's no scoping concern (no multi-tenant cache fences,
//     no test-isolation requirement beyond the per-test Unregister
//     callers already do).
//   - sync.Map is the right primitive: read-heavy (every admin poll
//     enumerates), write-rare (subsystems register once at boot).
//
// What gets registered: anything implementing StatsProvider — the
// existing *Cache[K, V] type qualifies because its Stats() / Clear()
// methods don't mention the K/V type parameters, so the interface is
// satisfied uniformly across heterogeneous caches. Wrappers that wrap
// a foreign cache (e.g. a hashicorp/golang-lru-backed one in a plugin)
// can also implement StatsProvider directly and be registered the
// same way.
//
// Lifecycle: instances register themselves on construction, Unregister
// on tear-down. Tests dial in their own registrations explicitly via
// Register / Unregister. Production wiring of `cache.Register(name, c)`
// happens at the call site of each cache.New() in app.go (slice that
// follows this admin-UI commit).
//
// Idempotency contract: Register(name, c) with a name already present
// REPLACES the prior registration (last-writer-wins). Operators
// reloading a subsystem don't end up with two entries for the same
// name. Unregister(name) is a no-op when the name isn't present.

import "sync"

// StatsProvider is the registry-facing interface. Anything providing
// Stats() + Clear() can be registered — the concrete K/V of a
// *Cache[K, V] is hidden behind this surface so the admin handler can
// loop over heterogeneous caches without type assertions.
//
// Stats() returns a point-in-time snapshot; Clear() is the operator-
// facing reset (drop entries + zero counters). Both should be cheap
// and goroutine-safe — the admin poll cadence is 5s and the registry
// snapshot is taken without holding any registry lock past the
// initial read.
type StatsProvider interface {
	Stats() Stats
	Clear()
}

// registry is the package-global map of name → StatsProvider. sync.Map
// because the access pattern is "list/lookup-many, write-rare", which
// is exactly what sync.Map is tuned for (vs. a plain map+RWMutex which
// pays Lock/Unlock cost on every concurrent reader).
//
// Not exported as a type — there's exactly one registry per process;
// hiding the type forces callers through Register / Unregister / All
// so the contract stays stable if we ever swap the implementation.
var registry sync.Map // map[string]StatsProvider

// Register adds an instance to the registry under the given name. If a
// registration already exists for that name, it is REPLACED. Safe to
// call from package init / constructor paths (no allocations beyond
// the sync.Map's own).
//
// Empty name is rejected silently — the registry doesn't accept the
// zero string because the admin UI keys on name for routing
// (POST /api/_admin/cache/{name}/clear) and an empty path would
// collide with the list route.
//
// Nil instances are also rejected silently — the registry only stores
// usable providers; a nil StatsProvider would panic on the next
// Stats() call from the admin handler.
func Register(name string, c StatsProvider) {
	if name == "" || c == nil {
		return
	}
	registry.Store(name, c)
}

// Unregister removes the instance with the given name. Idempotent:
// calling it for an unknown name is a no-op (no error, no panic).
// Intended for test cleanup and subsystem tear-down on graceful
// shutdown.
func Unregister(name string) {
	if name == "" {
		return
	}
	registry.Delete(name)
}

// All returns a snapshot of every registered instance. The returned
// map is a fresh allocation owned by the caller — mutating it does NOT
// affect the registry, and concurrent Register/Unregister calls after
// the snapshot is taken are NOT reflected. This matches what the
// admin handler wants: a stable view of the registry for the duration
// of one HTTP response.
//
// Order is non-deterministic (Go map iteration). Callers that need
// sorted output (the admin handler does, for stable UI rendering)
// sort by name themselves.
func All() map[string]StatsProvider {
	out := make(map[string]StatsProvider)
	registry.Range(func(k, v any) bool {
		name, ok := k.(string)
		if !ok {
			return true
		}
		c, ok := v.(StatsProvider)
		if !ok {
			return true
		}
		out[name] = c
		return true
	})
	return out
}

// Get looks up a single registered instance by name. Returns
// (provider, true) when present, (nil, false) otherwise. Used by the
// admin Clear handler to route the action without enumerating the
// full registry.
func Get(name string) (StatsProvider, bool) {
	if name == "" {
		return nil, false
	}
	v, ok := registry.Load(name)
	if !ok {
		return nil, false
	}
	c, ok := v.(StatsProvider)
	if !ok {
		return nil, false
	}
	return c, true
}
