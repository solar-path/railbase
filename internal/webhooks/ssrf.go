package webhooks

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// AllowPrivateMode controls whether the URL validator permits private
// / loopback / link-local IP ranges. Tests + dev mode pass true; prod
// passes false (the default).
//
// The validator rejects:
//   - non-http(s) schemes (file://, gopher://, etc — classic SSRF vector)
//   - hosts that resolve to private CIDRs (10/8, 172.16/12, 192.168/16,
//     169.254/16, 127/8, ::1, fc00::/7, fe80::/10) when private mode
//     is off
//   - hosts that are bare IPs in those ranges (e.g. http://10.0.0.5/)
//
// The validator does NOT do DNS rebinding protection beyond a single
// lookup at validation time — racing the resolver is theoretically
// possible. Operators who need defense-in-depth should run Railbase
// behind a webhook proxy with egress firewall rules.
type ValidatorOptions struct {
	AllowPrivate bool
}

// ValidateURL checks `raw` against the SSRF policy. Returns the parsed
// URL on success so callers can pass it to http.NewRequest directly.
func ValidateURL(raw string, opts ValidatorOptions) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("webhooks: invalid url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("webhooks: scheme %q rejected (use http or https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("webhooks: url has no host")
	}
	if opts.AllowPrivate {
		return u, nil
	}
	// Resolve and reject anything in disallowed ranges. We resolve
	// rather than string-match because operators can put DNS names
	// that point at internal addresses.
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("webhooks: cannot resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if isDisallowed(ip) {
			return nil, fmt.Errorf("webhooks: %s resolves to disallowed range %s", host, ip)
		}
	}
	return u, nil
}

// isDisallowed reports whether an IP is in a private / loopback /
// link-local / unspecified range. Mirrors RFC 1918 + RFC 4193 + the
// usual SSRF block list.
func isDisallowed(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// IsPrivate covers RFC 1918 + RFC 4193 for us. Catch the rest:
	// 100.64.0.0/10 (CGNAT) and 192.0.0.0/24 (IETF protocol assignments)
	// are not strictly private but typically NOT desired as webhook
	// destinations. We err on the side of permissive here (they're
	// reachable on the public net in some configurations) — operators
	// who want to block them set up an egress firewall.
	return false
}
