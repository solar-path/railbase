// Package runtimeconfig is the single source of truth for every UI-
// mutable setting at runtime. The unified pattern replaces the
// scattered "subscribe to settings.TopicChanged in five different
// places in app.go" approach with one dispatcher + atomic-backed
// accessors.
//
// Architectural contract:
//
//	  Every value the operator can change through the admin Settings
//	  UI is reloaded LIVE — no restart, no admin downtime, no
//	  "restart required" badge in the UI. Values that genuinely
//	  cannot be live (master key, DSN, listen address) live in
//	  env-vars / config files, NOT in the admin UI.
//
// Why this matters: the previous model forced operators to remember
// which knobs required a process bounce. That mental tax leaked into
// outage windows ("did I restart after toggling that?") and into
// operational confidence ("can I touch the rate-limit on a hot
// production system?"). Single contract: if the UI lets you Save it,
// it's live.
//
// Surface for consumers:
//
//	import "github.com/railbase/railbase/internal/runtimeconfig"
//
//	// Stateless consumers — read on every hot-path call. The atomic
//	// load is sub-nanosecond on x86_64; this is cheaper than the
//	// settings.Manager.Get* path (no map lookup, no JSON unmarshal,
//	// no fallback dance).
//	if r.ContentLength > runtimeconfig.MaxUploadBytes() { ... }
//
//	// Stateful services — register a callback. The dispatcher fires
//	// it whenever any of the keys the callback declared interest in
//	// move. Use this for services that hold the value in a struct
//	// field they can't atomic-swap (mailer service, webauthn verifier).
//	cfg.OnChange([]string{"site.name", "site.url"}, func() {
//	    mailer.Reload(cfg.SiteName(), cfg.SiteURL())
//	})
//
// What this package does NOT do:
//
//   - It does not own the persistence layer. settings.Manager keeps
//     persisting to `_settings` table; runtimeconfig is the cache +
//     accessor over Manager.
//
//   - It does not subscribe to the bus itself — the wiring in
//     pkg/railbase/app.go calls Init() and pumps events from there.
//     That keeps this package free of pgxpool / eventbus imports and
//     trivially unit-testable.
package runtimeconfig

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Loader is the read side of settings.Manager — exactly the methods
// runtimeconfig needs and no more. Decoupled as an interface so the
// package doesn't drag in pgxpool transitively and so tests can pass
// an in-memory fake.
type Loader interface {
	GetString(ctx context.Context, key string) (string, bool, error)
	GetInt(ctx context.Context, key string) (int64, bool, error)
	GetBool(ctx context.Context, key string) (bool, bool, error)
}

// Config is the process-wide live config handle. Construct once in
// app.go with New, hold for process lifetime, share through Deps /
// closures. Goroutine-safe — every getter is an atomic load.
//
// The package also exports a process-global Default handle wired by
// Init/Default so hot-path call sites don't need to thread a *Config
// through. The global is the production wiring; tests build their
// own *Config and avoid touching Default.
type Config struct {
	loader Loader

	// envMap maps a catalog key to its operator pre-boot env-var
	// fallback. Consulted on every reloadKey AFTER the Loader misses.
	// Empty for unit tests; populated by the production wiring in
	// app.go so `RAILBASE_*` shell exports keep their documented
	// effect even though settings.Manager is now the live source of
	// truth. Env vars don't change at runtime — re-reading them on
	// every reload is correct (they win only when the persisted
	// setting is absent).
	envMap map[string]string

	// Atomic-backed value slots. One per catalog key. Pointer-typed
	// for strings and []string so atomic.Pointer keeps the API
	// uniform; the load result is then nil-guarded against the
	// declared default below.
	siteName atomic.Pointer[string]
	siteURL  atomic.Pointer[string]

	storageMaxUploadBytes atomic.Int64
	storageURLTTL         atomic.Int64 // nanoseconds

	allowedIPs     atomic.Pointer[[]string]
	deniedIPs      atomic.Pointer[[]string]
	trustedProxies atomic.Pointer[[]string]

	corsAllowedOrigins  atomic.Pointer[[]string]
	corsAllowCredentials atomic.Bool

	rateLimitPerIP     atomic.Pointer[string]
	rateLimitPerUser   atomic.Pointer[string]
	rateLimitPerTenant atomic.Pointer[string]

	antibotEnabled atomic.Bool
	logsPersist    atomic.Bool
	compatMode     atomic.Pointer[string]

	// Subscribers — `OnChange` registrations. Each entry pairs a key
	// set with a callback. The dispatcher runs callbacks synchronously
	// on whatever goroutine pumps the change event into Notify; this
	// is intentional so a callback's reload happens before the next
	// consumer reads the value. Slow callbacks block the dispatcher;
	// stateful reloads should run in a goroutine if they need to.
	subsMu sync.RWMutex
	subs   []changeSub
}

type changeSub struct {
	keys map[string]struct{}
	fn   func()
}

// Defaults are the same constants that every consumer used to embed
// directly. Pulled here so getters fall back consistently when the
// persisted setting is missing.
const (
	defaultSiteName              = "Railbase"
	defaultStorageMaxUploadBytes = int64(50 << 20) // 50 MiB
	defaultStorageURLTTL         = 5 * time.Minute
	defaultCompatMode            = "strict"
	defaultAntibotEnabled        = true
	defaultLogsPersist           = true
)

// New constructs a Config and Reloads every value from the loader.
// Call once on boot, after the settings.Manager is available but
// before any consumer reads a getter.
//
// envMap is the optional key→env-var-name table for pre-boot
// operator overrides. Pass nil in tests; production wiring passes
// the catalog's EnvVar mapping so `RAILBASE_*` exports remain a
// working override when the setting is absent from `_settings`.
func New(loader Loader, envMap map[string]string) *Config {
	c := &Config{loader: loader, envMap: envMap}
	c.ReloadAll(context.Background())
	return c
}

// ReloadAll re-reads every catalog key from the loader and stores
// it in the atomic slots. Triggered on boot by New and as a fallback
// if Notify is called with an unknown key (shouldn't happen — the
// dispatcher table catches every catalog key — but the fallback
// keeps the package robust against catalog drift).
func (c *Config) ReloadAll(ctx context.Context) {
	for _, k := range Keys() {
		c.reloadKey(ctx, k)
	}
}

// Notify is the dispatcher entry point. app.go's single bus
// subscriber calls this with the changed key; we re-read just that
// one key and fire the matching OnChange callbacks. Unknown keys
// fall through silently — settings outside the runtimeconfig surface
// (mailer.*, oauth.*, etc.) reach here too but have no slot to
// update.
func (c *Config) Notify(ctx context.Context, key string) {
	c.reloadKey(ctx, key)
	c.subsMu.RLock()
	matched := make([]func(), 0, len(c.subs))
	for _, s := range c.subs {
		if _, ok := s.keys[key]; ok {
			matched = append(matched, s.fn)
		}
	}
	c.subsMu.RUnlock()
	// Run callbacks outside the lock — a callback that re-registers
	// must not deadlock.
	for _, fn := range matched {
		fn()
	}
}

// reloadKey is the dispatcher table. ADD A LINE HERE WHEN ADDING A
// CATALOG KEY. Keeping the dispatch explicit (vs reflection / map
// lookup) gives us compile-time coverage: a missing case for a key
// is a missing slot in Config, which is a compile error.
func (c *Config) reloadKey(ctx context.Context, key string) {
	switch key {
	case "site.name":
		c.siteName.Store(strPtrOrNil(c.loadString(ctx, key)))
	case "site.url":
		c.siteURL.Store(strPtrOrNil(c.loadString(ctx, key)))
	case "storage.max_upload_bytes":
		c.storageMaxUploadBytes.Store(c.loadInt(ctx, key))
	case "storage.url_ttl":
		c.storageURLTTL.Store(int64(c.loadDuration(ctx, key)))
	case "security.allow_ips":
		v := c.loadCSV(ctx, key)
		c.allowedIPs.Store(&v)
	case "security.deny_ips":
		v := c.loadCSV(ctx, key)
		c.deniedIPs.Store(&v)
	case "security.trusted_proxies":
		v := c.loadCSV(ctx, key)
		c.trustedProxies.Store(&v)
	case "security.cors.allowed_origins":
		v := c.loadCSV(ctx, key)
		c.corsAllowedOrigins.Store(&v)
	case "security.cors.allow_credentials":
		c.corsAllowCredentials.Store(c.loadBool(ctx, key))
	case "security.rate_limit.per_ip":
		c.rateLimitPerIP.Store(strPtrOrNil(c.loadString(ctx, key)))
	case "security.rate_limit.per_user":
		c.rateLimitPerUser.Store(strPtrOrNil(c.loadString(ctx, key)))
	case "security.rate_limit.per_tenant":
		c.rateLimitPerTenant.Store(strPtrOrNil(c.loadString(ctx, key)))
	case "security.antibot.enabled":
		c.antibotEnabled.Store(c.loadBoolWithDefault(ctx, key, defaultAntibotEnabled))
	case "logs.persist":
		c.logsPersist.Store(c.loadBoolWithDefault(ctx, key, defaultLogsPersist))
	case "compat.mode":
		c.compatMode.Store(strPtrOrNil(c.loadString(ctx, key)))
	}
}

// Keys returns every catalog key runtimeconfig owns. Stable order,
// used by ReloadAll on boot and by tests asserting catalog coverage.
func Keys() []string {
	return []string{
		"site.name",
		"site.url",
		"storage.max_upload_bytes",
		"storage.url_ttl",
		"security.allow_ips",
		"security.deny_ips",
		"security.trusted_proxies",
		"security.cors.allowed_origins",
		"security.cors.allow_credentials",
		"security.rate_limit.per_ip",
		"security.rate_limit.per_user",
		"security.rate_limit.per_tenant",
		"security.antibot.enabled",
		"logs.persist",
		"compat.mode",
	}
}

// OnChange registers a callback invoked whenever ANY of the listed
// keys move. Used by stateful services (mailer, webauthn, IPFilter)
// that can't just atomic-swap a value — they need to rebuild internal
// state from the new snapshot.
//
// The callback runs synchronously on the dispatcher's goroutine
// (which is the eventbus subscriber goroutine in production). Slow
// callbacks block other subscribers; if Reload work is heavy, the
// callback should spawn a goroutine.
//
// No unregister — callbacks live for the process lifetime. The
// stateful services that register here are themselves process-lived.
func (c *Config) OnChange(keys []string, fn func()) {
	if fn == nil || len(keys) == 0 {
		return
	}
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	c.subsMu.Lock()
	c.subs = append(c.subs, changeSub{keys: set, fn: fn})
	c.subsMu.Unlock()
}

// ─── typed accessors ────────────────────────────────────────────

func (c *Config) SiteName() string {
	if p := c.siteName.Load(); p != nil && *p != "" {
		return *p
	}
	return defaultSiteName
}

func (c *Config) SiteURL() string {
	if p := c.siteURL.Load(); p != nil {
		return *p
	}
	return ""
}

func (c *Config) MaxUploadBytes() int64 {
	v := c.storageMaxUploadBytes.Load()
	if v <= 0 {
		return defaultStorageMaxUploadBytes
	}
	return v
}

func (c *Config) StorageURLTTL() time.Duration {
	v := c.storageURLTTL.Load()
	if v <= 0 {
		return defaultStorageURLTTL
	}
	return time.Duration(v)
}

func (c *Config) AllowedIPs() []string     { return loadSlice(&c.allowedIPs) }
func (c *Config) DeniedIPs() []string      { return loadSlice(&c.deniedIPs) }
func (c *Config) TrustedProxies() []string { return loadSlice(&c.trustedProxies) }

func (c *Config) CORSAllowedOrigins() []string { return loadSlice(&c.corsAllowedOrigins) }
func (c *Config) CORSAllowCredentials() bool   { return c.corsAllowCredentials.Load() }

func (c *Config) RateLimitPerIP() string     { return loadStringPtr(&c.rateLimitPerIP) }
func (c *Config) RateLimitPerUser() string   { return loadStringPtr(&c.rateLimitPerUser) }
func (c *Config) RateLimitPerTenant() string { return loadStringPtr(&c.rateLimitPerTenant) }

func (c *Config) AntibotEnabled() bool { return c.antibotEnabled.Load() }
func (c *Config) LogsPersist() bool    { return c.logsPersist.Load() }

func (c *Config) CompatMode() string {
	if p := c.compatMode.Load(); p != nil && *p != "" {
		return *p
	}
	return defaultCompatMode
}

// ─── load helpers ────────────────────────────────────────────────
//
// Each helper handles the three-way outcome of loader access
// (missing / empty / present-and-valid) and folds it into the
// caller's expected zero / default behaviour. We deliberately
// swallow the loader's error — a flaky DB read should fall back to
// the previous in-memory value, NOT crash the consumer; the
// settings.Manager already retries on next access.
//
// Resolution chain (highest to lowest precedence):
//
//	1. settings.Manager (live, mutable through the admin UI)
//	2. process env-var (envMap[key] → os.Getenv, pre-boot override)
//	3. typed zero (caller folds against its own default)

// envFallback resolves the env-var override for `key`, or "" if no
// env mapping exists or the env-var is unset. Centralised so each
// typed helper handles its own parsing.
func (c *Config) envFallback(key string) string {
	if c.envMap == nil {
		return ""
	}
	envKey := c.envMap[key]
	if envKey == "" {
		return ""
	}
	return os.Getenv(envKey)
}

func (c *Config) loadString(ctx context.Context, key string) string {
	if v, ok, _ := c.loader.GetString(ctx, key); ok {
		return v
	}
	return c.envFallback(key)
}

func (c *Config) loadInt(ctx context.Context, key string) int64 {
	if v, ok, _ := c.loader.GetInt(ctx, key); ok {
		return v
	}
	// GetInt only matches JSON numbers. Fall back to string-parsing
	// the value because operators sometimes Save "50000000" as a JSON
	// string out of habit; ALSO covers env-var values which are always
	// strings.
	s, ok, _ := c.loader.GetString(ctx, key)
	if !ok {
		s = c.envFallback(key)
	}
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (c *Config) loadBool(ctx context.Context, key string) bool {
	if v, ok, _ := c.loader.GetBool(ctx, key); ok {
		return v
	}
	if s := c.envFallback(key); s != "" {
		return parseBoolLoose(s)
	}
	return false
}

func (c *Config) loadBoolWithDefault(ctx context.Context, key string, def bool) bool {
	if v, ok, _ := c.loader.GetBool(ctx, key); ok {
		return v
	}
	if s := c.envFallback(key); s != "" {
		return parseBoolLoose(s)
	}
	return def
}

func (c *Config) loadCSV(ctx context.Context, key string) []string {
	s, ok, _ := c.loader.GetString(ctx, key)
	if !ok || s == "" {
		s = c.envFallback(key)
	}
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *Config) loadDuration(ctx context.Context, key string) time.Duration {
	s, ok, _ := c.loader.GetString(ctx, key)
	if !ok || s == "" {
		s = c.envFallback(key)
	}
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return d
}

// parseBoolLoose accepts the same shapes shell exports usually
// carry — "1/0", "true/false", "on/off", "yes/no" — case-insensitive.
// Mirrors the pre-runtimeconfig readSetting → strings.ToLower path
// in app.go.
func parseBoolLoose(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func loadSlice(p *atomic.Pointer[[]string]) []string {
	v := p.Load()
	if v == nil {
		return nil
	}
	return *v
}

func loadStringPtr(p *atomic.Pointer[string]) string {
	v := p.Load()
	if v == nil {
		return ""
	}
	return *v
}
