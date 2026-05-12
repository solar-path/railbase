// v1.7.2 — three-axis token-bucket rate limiter.
//
// Protects against per-actor traffic spikes: bot scrapes (per-IP),
// runaway clients with a stolen credential (per-user), runaway
// tenants (per-tenant). Each axis is independent — a request that
// passes all three goes through; failing ANY axis returns 429 with
// the smallest Retry-After. This is intentional: a tenant on its
// own quota shouldn't be penalised because a single user inside the
// tenant burst, AND vice versa.
//
// Algorithm: classical token bucket. O(1) per-check, no event list,
// natural burst semantics (a bucket starts full → first N requests
// are free, then they cost a token each at the refill rate).
//
// State: in-process `sync.Map`-style sharded buckets. No Redis, no
// out-of-process coordination — Railbase ships as a single binary
// and clusters via a separate plugin (railbase-cluster). Operators
// running multiple replicas should layer a CDN / load-balancer
// rate limiter; the per-process limiter prevents one replica from
// drowning under abuse, not from coordinating across replicas.
//
// Bounded memory: a background sweeper drops buckets that haven't
// been touched for `IdleEvictionAfter` (default 10 min). Worst case
// the limiter retains one bucket per active key for that window,
// which scales to millions of unique IPs without blowing memory.

package security

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/tenant"
)

// Rule is a single axis's quota. Zero-valued Requests OR zero
// Window disables that axis entirely — operators who don't want
// per-tenant limiting (e.g. single-tenant deployments) leave the
// PerTenant struct zero-valued.
type Rule struct {
	// Requests is the steady-state allowance per Window. Refill rate
	// is Requests / Window per nanosecond, accumulated into the
	// bucket up to Burst.
	Requests int
	// Window is the period over which Requests are spread.
	Window time.Duration
	// Burst is the bucket capacity — max tokens accumulated when the
	// bucket is full (i.e. when a key has been idle long enough).
	// Zero means "same as Requests" (no extra burst over the steady
	// rate). Operators commonly set Burst = Requests * 2 to absorb
	// brief spikes without 429ing the legitimate user.
	Burst int
}

// Enabled reports whether the rule actually limits anything. A rule
// with Requests <= 0 OR Window <= 0 is a no-op — we treat it as
// disabled so operators can flip axes independently in settings
// without having to delete keys.
func (r Rule) Enabled() bool {
	return r.Requests > 0 && r.Window > 0
}

// effectiveBurst returns the bucket capacity. Defaults to Requests
// when Burst is unset — a "no burst over steady rate" baseline.
func (r Rule) effectiveBurst() float64 {
	if r.Burst > 0 {
		return float64(r.Burst)
	}
	return float64(r.Requests)
}

// refillPerNanosecond is the rate at which tokens are added to a
// bucket. Computed once per Rule rather than per-check to avoid
// repeated float math.
func (r Rule) refillPerNanosecond() float64 {
	if !r.Enabled() {
		return 0
	}
	return float64(r.Requests) / float64(r.Window.Nanoseconds())
}

// Config bundles the three axes plus operator knobs.
type Config struct {
	PerIP     Rule
	PerUser   Rule
	PerTenant Rule

	// IdleEvictionAfter bounds how long a stale bucket lives in
	// memory. Default 10 min — a key the sweeper sees idle for that
	// long is dropped. Reads during the idle window keep the bucket
	// fresh, so this only fires for truly cold keys.
	IdleEvictionAfter time.Duration

	// SweepInterval is how often the sweeper scans for cold buckets.
	// Default 1 min — short enough to keep memory bounded under
	// adversarial workloads, long enough to amortise the scan cost.
	SweepInterval time.Duration

	// Shards is the number of sharded maps. Power of 2 strongly
	// preferred (we use AND-mask routing). Default 16 — good balance
	// for most workloads; busy installs bump to 64 or 256.
	Shards int
}

// applyDefaults returns a Config with zero fields filled in.
func (c Config) applyDefaults() Config {
	if c.IdleEvictionAfter <= 0 {
		c.IdleEvictionAfter = 10 * time.Minute
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = time.Minute
	}
	if c.Shards <= 0 {
		c.Shards = 16
	}
	// Round shards up to next power of 2 for AND-mask routing.
	s := 1
	for s < c.Shards {
		s <<= 1
	}
	c.Shards = s
	return c
}

// bucket is the per-key token store. mu is per-bucket so contention
// is limited to concurrent hits on the same key (an IP under DoS
// from many goroutines on the same machine).
type bucket struct {
	mu        sync.Mutex
	tokens    float64
	lastFill  int64 // unix nanos — set on every check via TimeNow
	lastTouch int64 // unix nanos — sweeper reads this; bumped on every check
}

// take attempts to consume one token. Returns (allowed, retryAfter)
// where retryAfter is the time until the next token if blocked.
// Caller must hold no other locks.
func (b *bucket) take(now int64, rule Rule) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Refill from elapsed time.
	if b.lastFill > 0 {
		elapsed := float64(now - b.lastFill)
		if elapsed > 0 {
			b.tokens += elapsed * rule.refillPerNanosecond()
		}
		cap := rule.effectiveBurst()
		if b.tokens > cap {
			b.tokens = cap
		}
	} else {
		// First hit — bucket starts full.
		b.tokens = rule.effectiveBurst()
	}
	b.lastFill = now
	b.lastTouch = now

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}

	// Not enough tokens. Compute time until next whole token.
	deficit := 1.0 - b.tokens
	nsToWait := int64(deficit / rule.refillPerNanosecond())
	if nsToWait < 0 {
		nsToWait = 0
	}
	return false, time.Duration(nsToWait)
}

// shard is one slice of the sharded bucket map.
type shard struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// Limiter is the live rate-limit machinery. Holds three shard maps
// (one per axis) + a sweeper goroutine. Concurrency-safe; install
// once at boot, share across the whole HTTP stack.
type Limiter struct {
	cfg atomic.Pointer[Config]

	ipShards     []*shard
	userShards   []*shard
	tenantShards []*shard

	// shardMask = Shards - 1 (cached pow-2 AND mask).
	shardMask uint32

	// TimeNow is overridable for tests. Returns wall-clock UTC.
	TimeNow func() time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewLimiter constructs a fresh limiter and launches its sweeper.
// Always call Stop() on shutdown to release the sweeper goroutine.
func NewLimiter(cfg Config) *Limiter {
	cfg = cfg.applyDefaults()
	l := &Limiter{
		ipShards:     newShards(cfg.Shards),
		userShards:   newShards(cfg.Shards),
		tenantShards: newShards(cfg.Shards),
		shardMask:    uint32(cfg.Shards - 1),
		TimeNow:      time.Now,
		stopCh:       make(chan struct{}),
	}
	l.cfg.Store(&cfg)
	go l.sweep()
	return l
}

func newShards(n int) []*shard {
	out := make([]*shard, n)
	for i := range out {
		out[i] = &shard{buckets: map[string]*bucket{}}
	}
	return out
}

// Update swaps the active config atomically. Existing buckets keep
// their tokens — operators tightening a limit don't accidentally
// reset every IP's allowance. Operators broadening a limit see the
// new burst on the next refill.
func (l *Limiter) Update(cfg Config) {
	cfg = cfg.applyDefaults()
	l.cfg.Store(&cfg)
}

// Stop terminates the sweeper. Idempotent.
func (l *Limiter) Stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
}

// Allow is the test-friendly check API. Production code calls
// Middleware() and never touches this directly. Returns the most
// restrictive Retry-After across the three axes (so a client only
// has to honour one wait).
//
// Empty key for any axis (e.g. anonymous → no user key) skips that
// axis silently. This is the right default — anonymous clients are
// covered by the IP axis, and unscoped requests are covered by IP
// too.
func (l *Limiter) Allow(ip, userID, tenantID string) (bool, time.Duration) {
	cfg := l.cfg.Load()
	now := l.TimeNow().UnixNano()
	var worstWait time.Duration
	allowed := true

	check := func(shards []*shard, key string, rule Rule) {
		if key == "" || !rule.Enabled() {
			return
		}
		b := l.bucketFor(shards, key)
		ok, wait := b.take(now, rule)
		if !ok {
			allowed = false
			if wait > worstWait {
				worstWait = wait
			}
		}
	}

	check(l.ipShards, ip, cfg.PerIP)
	check(l.userShards, userID, cfg.PerUser)
	check(l.tenantShards, tenantID, cfg.PerTenant)

	return allowed, worstWait
}

// bucketFor looks up (or creates) the bucket for a key in the given
// shard set. Double-checked locking pattern: take the read path
// under the shard mutex, return existing if present, else create.
func (l *Limiter) bucketFor(shards []*shard, key string) *bucket {
	idx := hashKey(key) & l.shardMask
	sh := shards[idx]
	sh.mu.Lock()
	b, ok := sh.buckets[key]
	if !ok {
		b = &bucket{}
		sh.buckets[key] = b
	}
	sh.mu.Unlock()
	return b
}

// hashKey routes a key to a shard via FNV-1a. Deterministic across
// the process's lifetime so the same key always hits the same shard.
func hashKey(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// sweep runs in its own goroutine, scanning every SweepInterval for
// buckets that haven't been touched for IdleEvictionAfter. Holds
// each shard's lock for the duration of its scan — cheap because
// shards are O(N/S) keys.
func (l *Limiter) sweep() {
	for {
		cfg := l.cfg.Load()
		select {
		case <-l.stopCh:
			return
		case <-time.After(cfg.SweepInterval):
		}
		cutoff := l.TimeNow().UnixNano() - cfg.IdleEvictionAfter.Nanoseconds()
		for _, set := range [][]*shard{l.ipShards, l.userShards, l.tenantShards} {
			for _, sh := range set {
				sh.mu.Lock()
				for k, b := range sh.buckets {
					b.mu.Lock()
					stale := b.lastTouch < cutoff
					b.mu.Unlock()
					if stale {
						delete(sh.buckets, k)
					}
				}
				sh.mu.Unlock()
			}
		}
	}
}

// Middleware returns an http.Handler middleware that enforces the
// current Config against every incoming request.
//
// Order of operations:
//
//  1. Extract IP from security.ClientIP(ctx) (set by IPFilter
//     middleware further out in the chain). Falls back to
//     RemoteAddr without proxy hop walking — operators wanting
//     XFF should chain IPFilter first.
//  2. Extract authenticated UserID from authmw.PrincipalFrom(ctx).
//     Empty for anonymous requests.
//  3. Extract tenant ID from tenant.ID(ctx). Empty for unscoped.
//  4. Allow() across the three axes. On 429, emit standard
//     X-RateLimit-* headers + Retry-After + JSON envelope.
//
// On allow, headers are still emitted so well-behaved clients can
// pace themselves without hitting the limit.
func (l *Limiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIPString(r)
			user := userIDString(r.Context())
			tenantID := tenantIDString(r.Context())

			ok, wait := l.Allow(ip, user, tenantID)
			cfg := l.cfg.Load()
			writeRateLimitHeaders(w, cfg, ok)
			if !ok {
				retry := int(wait.Seconds())
				if retry < 1 {
					retry = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				err := rerr.New(rerr.CodeRateLimit,
					"too many requests; retry after %d seconds", retry).
					WithDetail("retry_after", retry)
				rerr.WriteJSON(w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeRateLimitHeaders emits the standard RFC-style hints. Even on
// success — well-behaved clients can self-throttle before hitting
// the limit, sparing the server CPU.
func writeRateLimitHeaders(w http.ResponseWriter, cfg *Config, ok bool) {
	// The "primary" rule is the IP rule by convention (it's the
	// always-applicable axis). Headers reflect that; per-axis
	// breakdown would need three sets of headers which clutters
	// the response without a real-world consumer demanding it.
	if !cfg.PerIP.Enabled() {
		return
	}
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.PerIP.Requests))
	// Remaining is approximate (we don't expose the live token count
	// to avoid per-request locking just for the header). Set to the
	// limit on success and 0 on rejection — a useful signal even
	// without exact precision.
	if ok {
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(cfg.PerIP.Requests))
	} else {
		w.Header().Set("X-RateLimit-Remaining", "0")
	}
	w.Header().Set("X-RateLimit-Reset", strconv.Itoa(int(cfg.PerIP.Window.Seconds())))
}

func clientIPString(r *http.Request) string {
	if ip := ClientIP(r.Context()); ip != nil {
		return ip.String()
	}
	// Fall back to RemoteAddr without port stripping — close enough
	// for limiting; operators wanting full XFF correctness chain the
	// IPFilter middleware first.
	return r.RemoteAddr
}

func userIDString(ctx context.Context) string {
	p := authmw.PrincipalFrom(ctx)
	if !p.Authenticated() {
		return ""
	}
	return p.UserID.String()
}

func tenantIDString(ctx context.Context) string {
	id := tenant.ID(ctx)
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// --- settings integration helpers ---

// ParseRule turns the YAML-ish string form "100/min" into a Rule.
// Accepts suffixes: "s" (second), "min" / "m" (minute), "hour" /
// "h", "day" / "d". Defaults Burst to Requests.
//
// Empty input returns a zero-valued (disabled) Rule. Use this for
// settings-backed rule construction:
//
//	r, err := ParseRule(settings.Get("security.rate_limit.per_ip"))
//
// Returns (zero, nil) on empty input so the caller can lift "unset"
// to "disabled" without branching on errors.
func ParseRule(s string) (Rule, error) {
	if s == "" {
		return Rule{}, nil
	}
	// Find the slash separator.
	var i int
	for i = 0; i < len(s); i++ {
		if s[i] == '/' {
			break
		}
	}
	if i == len(s) {
		return Rule{}, fmt.Errorf("rate-limit rule %q: missing slash separator (use e.g. \"100/min\")", s)
	}
	reqs, err := strconv.Atoi(s[:i])
	if err != nil {
		return Rule{}, fmt.Errorf("rate-limit rule %q: bad request count: %w", s, err)
	}
	if reqs < 1 {
		return Rule{}, fmt.Errorf("rate-limit rule %q: requests must be >= 1", s)
	}
	unit := s[i+1:]
	var win time.Duration
	switch unit {
	case "s", "sec", "second":
		win = time.Second
	case "m", "min", "minute":
		win = time.Minute
	case "h", "hour":
		win = time.Hour
	case "d", "day":
		win = 24 * time.Hour
	default:
		return Rule{}, fmt.Errorf("rate-limit rule %q: unknown time unit %q (use s/min/hour/day)", s, unit)
	}
	return Rule{Requests: reqs, Window: win}, nil
}
