package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// IPFilterRules holds parsed allow/deny CIDRs. Use NewIPFilterRules
// to construct from string lists (settings.Manager values).
//
// Semantics:
//
//   - Allow list set, deny empty   → only allowed IPs pass.
//   - Allow empty, deny list set   → all IPs pass except denied.
//   - Both empty                   → no-op (everything passes).
//   - Both set                     → deny first (deny wins on overlap).
type IPFilterRules struct {
	allow []*net.IPNet
	deny  []*net.IPNet
}

// NewIPFilterRules parses CIDR strings. A bare IP (no slash) is
// promoted to a /32 or /128 — operators don't need to type "/32"
// for single-host rules. Returns an error listing every malformed
// entry so the operator can fix them in one pass.
func NewIPFilterRules(allow, deny []string) (*IPFilterRules, error) {
	r := &IPFilterRules{}
	var bad []string
	for _, s := range allow {
		nets, err := parseCIDR(s)
		if err != nil {
			bad = append(bad, fmt.Sprintf("allow %q: %s", s, err.Error()))
			continue
		}
		r.allow = append(r.allow, nets)
	}
	for _, s := range deny {
		nets, err := parseCIDR(s)
		if err != nil {
			bad = append(bad, fmt.Sprintf("deny %q: %s", s, err.Error()))
			continue
		}
		r.deny = append(r.deny, nets)
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("invalid CIDRs: %s", strings.Join(bad, "; "))
	}
	return r, nil
}

// Allowed reports whether ip passes the filter. Returns false when
// any deny CIDR matches OR when an allow list is set and no allow
// CIDR matches.
func (r *IPFilterRules) Allowed(ip net.IP) bool {
	if r == nil {
		return true
	}
	// Deny wins on overlap.
	for _, n := range r.deny {
		if n.Contains(ip) {
			return false
		}
	}
	if len(r.allow) == 0 {
		return true
	}
	for _, n := range r.allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsEmpty reports whether the filter has no rules — useful so the
// middleware can skip remoteIP resolution entirely when there's
// nothing to check.
func (r *IPFilterRules) IsEmpty() bool {
	return r == nil || (len(r.allow) == 0 && len(r.deny) == 0)
}

// IPFilter is the live, settings-driven filter middleware. Holds
// atomic.Pointers to the parsed rule set AND the trusted-proxy list
// so settings subscribers can swap either without locking the request
// path. The mutex serialises writers; readers (Middleware /
// clientIP) only do atomic loads.
type IPFilter struct {
	rules atomic.Pointer[IPFilterRules]
	// trustedProxies bounds how far back we walk X-Forwarded-For.
	// Without it, attackers can spoof origin by prepending a header
	// chain. Live-updatable via UpdateTrustedProxies so the
	// runtimeconfig dispatcher can flip this on Settings save with
	// no restart (Phase 2c — `security.trusted_proxies` → live).
	trustedProxies atomic.Pointer[[]*net.IPNet]
	mu             sync.Mutex // serialises Update*; not held on request path
}

// NewIPFilter constructs a filter with no rules. Use Update() (typically
// from a settings.Manager subscriber) to populate.
//
// trustedProxies are the load balancers / reverse-proxies whose
// X-Forwarded-For we trust. Pass an empty slice when the server sits
// directly on the internet — XFF will be ignored, RemoteAddr wins.
func NewIPFilter(trustedProxies []string) (*IPFilter, error) {
	f := &IPFilter{}
	empty := &IPFilterRules{}
	f.rules.Store(empty)
	emptyProxies := []*net.IPNet{}
	f.trustedProxies.Store(&emptyProxies)
	if len(trustedProxies) > 0 {
		if err := f.UpdateTrustedProxies(trustedProxies); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// Update atomically swaps the live rule set. Subscribers to settings
// changes call this from their handler.
func (f *IPFilter) Update(allow, deny []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := NewIPFilterRules(allow, deny)
	if err != nil {
		return err
	}
	f.rules.Store(r)
	return nil
}

// UpdateTrustedProxies atomically swaps the live trusted-proxy list.
// Phase 2c — `security.trusted_proxies` is now live: the
// runtimeconfig dispatcher calls this from its OnChange callback so
// flipping the setting in the admin UI immediately changes which
// hops are trusted for X-Forwarded-For walking. Failure (invalid
// CIDR) leaves the previous list in place — the caller logs.
func (f *IPFilter) UpdateTrustedProxies(proxies []string) error {
	parsed := make([]*net.IPNet, 0, len(proxies))
	for _, s := range proxies {
		nets, err := parseCIDR(s)
		if err != nil {
			return fmt.Errorf("trusted proxy %q: %w", s, err)
		}
		parsed = append(parsed, nets)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trustedProxies.Store(&parsed)
	return nil
}

// Middleware returns the http.Handler middleware. Refuses denied IPs
// with 403; allowed/no-rules pass through unchanged.
func (f *IPFilter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rules := f.rules.Load()
			if rules.IsEmpty() {
				next.ServeHTTP(w, r)
				return
			}
			ip := f.clientIP(r)
			if !rules.Allowed(ip) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP returns the effective client IP. When trustedProxies is
// non-empty AND RemoteAddr is in the trusted set, we walk the
// X-Forwarded-For chain backwards (right-most = last hop) and return
// the first untrusted IP. Without trusted proxies, RemoteAddr wins
// unconditionally — XFF is never honoured when the server faces the
// internet directly.
func (f *IPFilter) clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	if remote == nil {
		return nil
	}
	// Trust check: is the IMMEDIATE hop a trusted proxy?
	if !f.isTrustedProxy(remote) {
		return remote
	}
	// Walk XFF chain right-to-left: each entry was added by the
	// previous hop. Return the first IP we don't trust.
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remote
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		s := strings.TrimSpace(parts[i])
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if !f.isTrustedProxy(ip) {
			return ip
		}
	}
	return remote
}

func (f *IPFilter) isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	proxies := f.trustedProxies.Load()
	if proxies == nil {
		return false
	}
	for _, n := range *proxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseCIDR accepts "10.0.0.0/8", "192.168.1.5" (bare IP → /32 or /128),
// or "fc00::/7" (IPv6). Returns *net.IPNet for both branches.
func parseCIDR(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		return n, err
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("not a valid IP or CIDR")
	}
	if ip.To4() != nil {
		_, n, _ := net.ParseCIDR(ip.String() + "/32")
		return n, nil
	}
	_, n, _ := net.ParseCIDR(ip.String() + "/128")
	return n, nil
}

// ctxKey type guards against context-key collisions.
type ctxKey struct{ name string }

var clientIPKey = ctxKey{"client_ip"}

// WithClientIP stashes the resolved client IP into context. Handlers
// retrieve via ClientIP(ctx). Useful for audit log + rate limiter.
func WithClientIP(ctx context.Context, ip net.IP) context.Context {
	return context.WithValue(ctx, clientIPKey, ip)
}

// ClientIP retrieves the IP stashed by WithClientIP, or nil.
func ClientIP(ctx context.Context) net.IP {
	v, _ := ctx.Value(clientIPKey).(net.IP)
	return v
}
