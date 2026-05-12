package i18n

// Bundle-file cache — wires the v1.5.1 internal/cache primitive to the
// disk-side loaders (§3.9.4 — "per-subsystem wiring deferred"). Slice 2
// of the v1.7.31 cache-wiring rollout (after filter.ast + rbac.resolver
// in slice 1).
//
// Why cache parsed bundles:
//
//   - LoadDir / LoadFS run os.ReadFile + json.Unmarshal for every locale
//     on every invocation. The Catalog already holds the resulting
//     Bundle in its in-memory map after the first call, but operators
//     who hot-reload (manual Reload() today; fsnotify in a future slice)
//     re-walk the directory and re-parse every file. For an app with
//     N locales × M reloads, that's N×M JSON parses on a path that does
//     not need to be hot.
//   - The Catalog's own bundles map is the AUTHORITATIVE store — this
//     cache sits IN FRONT of the disk read, not behind the Catalog.
//     Result: Catalog locking discipline (RWMutex around the bundles
//     map) is unchanged; we only intercept the io+parse step.
//
// Why TTL 30s:
//
//	The brief calls out fsnotify-based hot-reload (deferred to a later
//	slice). When that lands, the watcher will invalidate this cache on
//	file events; until then, the 30-second TTL bounds staleness if an
//	operator edits a bundle file and triggers an out-of-band Reload.
//	30s is short enough that interactive iteration on translations is
//	not painful, long enough that the typical "reload once per deploy"
//	pattern still hits the cache for the duration of the reload itself
//	(multiple locales loaded in a tight loop all hit warm entries).
//
// Why capacity 64:
//
//	Most apps announce <20 locales. 64 entries is generous headroom
//	with negligible memory cost (each Bundle is a small map[string]
//	string — typical bundles are <10 KB). The LRU evicts cold locales
//	first; the admin Cache inspector surfaces the live size for tuning.
//
// Key choice — file path, not locale name:
//
//	The brief sketches `key = locale name` ("en-US"). We use the
//	absolute file path instead because:
//	  (a) LoadDir and LoadFS can be invoked against different roots
//	      in the same process (an operator overrides the embedded
//	      defaults with a disk dir; the embedded set still lives in
//	      the fs.FS). Locale-only keying would collide.
//	  (b) Tests run in parallel against t.TempDir() — sharing a
//	      registered cache across tests with locale-only keys would
//	      cross-contaminate.
//	  (c) The path is what the loader already has in hand; no extra
//	      derivation step.
//	The registered name remains `i18n.bundles` per the brief.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/railbase/railbase/internal/cache"
)

// bundleCache is the package-private parsed-bundle cache. Wrapped by
// readBundleFileCached / readBundleFSCached — neither LoadDir nor LoadFS
// reaches in directly. Registered globally so the admin Cache inspector
// surfaces its hit/miss/evict counters.
//
// Capacity 64, TTL 30s. Default shard count (16) — fnv-1a spreads short
// path strings evenly enough that contention is not a concern at this
// access rate (a handful of reads per Reload).
var bundleCache = cache.New[string, Bundle](cache.Options{
	Capacity: 64,
	TTL:      30 * time.Second,
})

// init wires the cache into the global registry so the admin Cache
// inspector (v1.7.24b) renders it. Package init is the right place —
// runs once per process at startup, no app.go threading required (the
// registry is a sync.Map keyed by name).
//
// Registration name "i18n.bundles" follows the docs/14 spec convention
// of `<subsystem>.<purpose>`.
func init() {
	cache.Register("i18n.bundles", bundleCache)
}

// readBundleFileCached returns the parsed Bundle for the disk file at
// path, hitting bundleCache on subsequent calls within the TTL window.
// Parse errors are NOT cached (cache.GetOrLoad's contract) — each
// malformed file re-parses, which surfaces the position consistently.
//
// The loader closure mirrors the pre-cache loadBundleFile body. We
// route through the bundleCache singleflight so concurrent LoadDir
// invocations against the same path collapse to one read+parse.
//
// context.Background() is used because the loaders are not request-
// scoped; they run at boot or on operator-driven Reload. If a future
// caller wants cancellation, expose a ctx-taking variant — the cache
// API already supports it.
func readBundleFileCached(path string) (Bundle, error) {
	// Clean defensively so logically-identical paths share a key.
	// (`./a/b` and `a/b` would otherwise be distinct entries.)
	key := filepath.Clean(path)
	return bundleCache.GetOrLoad(context.Background(), key, func(_ context.Context) (Bundle, error) {
		return loadBundleFile(path)
	})
}

// readBundleFSCached is the fs.FS counterpart. The cache key combines
// the FS identity (its Go type fingerprint) with the path; without
// that, two distinct embedded FS roots reading the same logical path
// would collide. In practice the only fs.FS caller in the codebase
// today is the embedded one (internal/i18n/embed), but the safety net
// costs nothing.
//
// We key on `fmt.Sprintf("%T:%p", fsys) + "|" + path`. The %T+%p combo
// captures pointer identity for pointer-typed FS values (the embedded
// one wraps embed.FS); for value-typed FS implementations (fstest.MapFS,
// some test fakes) %p degrades to the literal "0x0" string but the %T
// half still keeps distinct concrete types apart. Cache hits across
// runs of the SAME FS variable, which is what callers expect.
func readBundleFSCached(fsys fs.FS, path string) (Bundle, error) {
	key := fmt.Sprintf("%T:%p|%s", fsys, fsys, filepath.Clean(path))
	return bundleCache.GetOrLoad(context.Background(), key, func(_ context.Context) (Bundle, error) {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, err
		}
		var b Bundle
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		return b, nil
	})
}

// PurgeBundleCache drops every cached entry. Intended for the future
// fsnotify-driven invalidator (one slice ahead) and for the admin Cache
// inspector's Clear button (routed via cache.Clear on the registered
// instance). Idempotent.
func PurgeBundleCache() {
	bundleCache.Purge()
}
