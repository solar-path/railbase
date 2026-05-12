package filter

// AST cache — wires the `?filter=` string parser to the v1.5.1
// internal/cache primitive (§3.9.4 — "per-subsystem wiring deferred").
//
// Why cache parsed ASTs:
//
//   - Every HTTP list request that arrives with a `?filter=…` re-runs
//     the recursive-descent parser. Many real clients hit the same
//     endpoint in a tight loop (admin polling, paginating, "refresh
//     every 5s" dashboards), and the same filter string parses to the
//     same AST every time — the grammar is pure-functional (no
//     request-context state, no time-dependent identifiers).
//   - The parser is cheap individually (~5µs/parse on short filters)
//     but adds up to a meaningful fraction of list-handler CPU when
//     bursty workloads pile up; the singleflight inside GetOrLoad
//     additionally collapses concurrent identical parses to one shared
//     parse, which matters under thundering-herd "many tabs polling
//     the same filter" patterns.
//
// Why no TTL:
//
//	The AST is a pure function of the filter source. Once parsed
//	correctly, the same string will parse the same way forever — there
//	is no staleness concern. LRU eviction alone bounds memory.
//
// Capacity 4096:
//
//	Sized for the typical "long tail of distinct filter strings per
//	process" seen in admin/dashboard workloads. Filter strings are
//	small (typically <200 bytes), so 4096 entries is well under 1 MB
//	resident; the Cache inspector exposes the live size for tuning.
//
// Negative caching: we deliberately do NOT cache parse errors. Loader
// errors are not cached by GetOrLoad anyway — each malformed filter
// re-parses on every hit, which is fine because malformed filters are
// rare in steady state (clients validate before sending) and the parse
// error message must surface the position consistently.

import (
	"context"

	"github.com/railbase/railbase/internal/cache"
)

// astCache is the package-private AST cache. Exposed via Parse — which
// transparently routes through GetOrLoad — and registered globally so
// the admin Cache inspector can surface its hit/miss/evict counters.
//
// Capacity 4096, no TTL (the AST is a pure function of the source —
// see file header). Default shard count (16) — the access pattern is
// bursty short-string keys, fnv-1a spreads them evenly.
var astCache = cache.New[string, Node](cache.Options{
	Capacity: 4096,
})

// init wires the cache into the global registry so the admin Cache
// inspector (v1.7.24b) renders it. Package init is the right place —
// runs once per process at startup, before any HTTP server starts
// accepting requests, no app.go threading required (the registry is
// a sync.Map keyed by name).
//
// Registration name "filter.ast" follows the docs/14 spec convention
// of `<subsystem>.<purpose>`.
func init() {
	cache.Register("filter.ast", astCache)
}

// parseCached returns the cached AST for src, or invokes parseUncached
// under singleflight. Callers go through Parse — this is the inner
// helper that splits the cache lookup from the actual parse so the
// uncached path remains testable directly.
//
// ctx is threaded for parity with the cache.GetOrLoad signature, even
// though the parser itself doesn't honour cancellation (parse times
// are bounded by source length, which the HTTP layer caps upstream).
func parseCached(ctx context.Context, src string) (Node, error) {
	return astCache.GetOrLoad(ctx, src, func(_ context.Context) (Node, error) {
		return parseUncached(src)
	})
}
