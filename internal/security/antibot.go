// v1.x — anti-bot defense-in-depth middleware.
//
// Closes the §3.9.5 "anti-bot deferred" note in plan.md by shipping
// two layered checks that catch the trivial-but-noisy half of
// drive-by automation BEFORE it touches auth / rate-limit budget:
//
//   - Honeypot:    invisible form fields that a CSS-hiding template
//                  keeps off-screen for humans. Bots scrape the form
//                  HTML, fill in everything they see, and submit. If
//                  a honeypot comes back non-empty on a POST we know
//                  it's a bot and short-circuit with a 200 OK + a
//                  benign-looking `{}` body. The bot's success-path
//                  follows the wrong branch; humans never see this
//                  path because the field is never rendered.
//
//   - UA sanity:   reject obviously-scripted User-Agents on the
//                  enumeration-vulnerable endpoints (signup, password
//                  reset, OAuth callback). Configurable substring
//                  set; case-insensitive. NOT a substitute for rate
//                  limiting — this is the "don't even let curl probe
//                  /api/auth/sign-up" baseline that turns the most
//                  common credential-stuffing tooling away with 403
//                  before it spends our CPU.
//
// Per docs/14 this is production-gated by default; dev mode keeps
// every check off so `curl localhost:8095/api/...` Just Works for
// the operator at their terminal.
//
// Tier-3 IP list (Tor exits / scrape ranges) is a v1.x.x follow-up —
// needs a curated upstream CIDR-feed decision. AntiBotConfig is
// pre-shaped so wiring it in later is additive, not breaking.

package security

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// AntiBot is the live, settings-driven middleware. Holds an
// atomic.Pointer[AntiBotConfig] so settings subscribers can swap
// config without locking the request path.
type AntiBot struct {
	cfg atomic.Pointer[AntiBotConfig]
	log *slog.Logger
	mu  sync.Mutex // serialises UpdateConfig; not held on request path
}

// AntiBotConfig is the operator-facing knob set. Lower-case copies
// of substring lists are cached on Store via UpdateConfig — the
// request path doesn't repeat the ToLower work.
type AntiBotConfig struct {
	// Enabled is the master switch. Default false (dev); production
	// boot flips it to true unless the operator explicitly turns it
	// off via `security.antibot.enabled`. When false the middleware
	// is a pure pass-through (one atomic.Load + branch).
	Enabled bool

	// HoneypotFields lists form-field names that MUST be empty on
	// every POST/PUT/PATCH carrying a form body. Any non-empty value
	// → caught. Default ["website", "url", "email_confirm"] — names
	// chosen to look plausible to a naive scraper (it'll see "url"
	// and dutifully paste a URL into it).
	HoneypotFields []string

	// RejectUAs lists case-insensitive substrings that disqualify a
	// request on UA-enforced paths. Default catches the four most
	// common automation User-Agents (curl, requests, Go stdlib,
	// generic bot markers).
	RejectUAs []string

	// UAEnforcePaths is the path-prefix allow-list for the UA check.
	// We DON'T enforce on the full API surface (legitimate SDKs use
	// Go-http-client too); only the credential-and-enumeration
	// endpoints where automated probing is the dominant failure mode.
	UAEnforcePaths []string

	// derived: lower-cased copies for the request path. Filled by
	// UpdateConfig — request handlers don't reach into the raw
	// slices.
	rejectUAsLower []string
}

// DefaultAntiBotConfig returns the production-ready baseline.
// Caller flips Enabled per environment (production: true; dev:
// false). Other fields are sensible substring sets — operators
// extend via settings rather than overriding code.
func DefaultAntiBotConfig() AntiBotConfig {
	return AntiBotConfig{
		Enabled: false, // caller decides per environment
		HoneypotFields: []string{
			"website",
			"url",
			"email_confirm",
		},
		RejectUAs: []string{
			"bot",
			"crawler",
			"spider",
			"curl/",
			"python-requests",
			"Go-http-client/",
		},
		UAEnforcePaths: []string{
			"/api/auth/",
			"/api/oauth/",
		},
	}
}

// NewAntiBot constructs a middleware around the given config. The
// log argument is used for structured `antibot.*` events when a
// rule fires — pass the app's root logger so events go through the
// same dispatcher (and into the v1.7.6 logs.Sink if enabled).
func NewAntiBot(initial AntiBotConfig, log *slog.Logger) *AntiBot {
	a := &AntiBot{log: log}
	a.storeConfig(initial)
	return a
}

// UpdateConfig atomically swaps the live config. Subscribers to
// settings.changed call this on key updates. Cheap — single
// atomic.Pointer.Store after a one-time slice copy.
func (a *AntiBot) UpdateConfig(c AntiBotConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.storeConfig(c)
}

// storeConfig precomputes derived fields and stores the result.
// Must be called under a.mu.
func (a *AntiBot) storeConfig(c AntiBotConfig) {
	c.rejectUAsLower = make([]string, len(c.RejectUAs))
	for i, s := range c.RejectUAs {
		c.rejectUAsLower[i] = strings.ToLower(s)
	}
	a.cfg.Store(&c)
}

// honeypotBodyCap bounds the form parse so a 5MB POST can't OOM
// the process before we even know whether the request is human.
// 1MB is generous for honest signup / reset forms (those rarely
// exceed a few hundred bytes) and tight enough that adversaries
// can't pay much CPU per request.
const honeypotBodyCap = 1 << 20 // 1 MiB

// Middleware is the chi-compatible handler. Wired into the global
// chain in pkg/railbase/app.go alongside HSTS / IPFilter / CSRF.
//
// Order of checks (cheapest first; bail early on miss):
//
//  1. cfg.Enabled? No → pass through.
//  2. UA path-prefix match → UA substring check → 403 on hit.
//  3. Mutating method + form Content-Type → ParseForm → honeypot
//     non-empty? → 200 OK with `{}` body (bot success-bait).
//  4. Otherwise → pass to next handler.
func (a *AntiBot) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.cfg.Load()
		if cfg == nil || !cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		// UA sanity check on enumeration-vulnerable paths.
		if a.uaCheck(cfg, r) {
			a.logEvent("antibot.ua_rejected", r,
				"user_agent", r.Header.Get("User-Agent"))
			// NO detail in the response — leaking "why" lets an
			// adversary tune their UA until it slips through. Just
			// "forbidden" matches every other refusal in the stack.
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}

		// Honeypot check on form-encoded mutations.
		if a.honeypotCheck(cfg, w, r) {
			return // honeypotCheck wrote the bait response
		}

		next.ServeHTTP(w, r)
	})
}

// uaCheck returns true if the request should be 403'd. False means
// "no rule applies here" — caller falls through to next checks.
func (a *AntiBot) uaCheck(cfg *AntiBotConfig, r *http.Request) bool {
	if len(cfg.UAEnforcePaths) == 0 || len(cfg.rejectUAsLower) == 0 {
		return false
	}
	if !hasAnyPrefix(r.URL.Path, cfg.UAEnforcePaths) {
		return false
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	if ua == "" {
		// Empty UA on an auth endpoint is itself suspicious — the
		// curl/requests/SDK defaults all SEND a UA. We treat empty
		// as a match for the "obvious automation" bucket.
		return true
	}
	for _, sub := range cfg.rejectUAsLower {
		if sub == "" {
			continue
		}
		if strings.Contains(ua, sub) {
			return true
		}
	}
	return false
}

// honeypotCheck inspects form bodies for non-empty honeypot fields.
// Returns true if it handled the response (i.e. wrote the bait
// success body and caller MUST NOT continue); false if the request
// should flow through unchanged.
//
// We MaxBytesReader-cap the body BEFORE ParseForm so an attacker
// can't OOM us with a 5GB form. On cap-exceeded, ParseForm returns
// an error; we treat that as "bot probably" and short-circuit with
// 413 — adversarial-large form bodies aren't legitimate signup
// traffic.
func (a *AntiBot) honeypotCheck(cfg *AntiBotConfig, w http.ResponseWriter, r *http.Request) bool {
	if len(cfg.HoneypotFields) == 0 {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return false
	}
	ct := r.Header.Get("Content-Type")
	if !isFormContentType(ct) {
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, honeypotBodyCap)
	if err := r.ParseForm(); err != nil {
		// Cap exceeded OR malformed body. Refuse with 413 (Payload
		// Too Large) — legitimate clients with honest forms never
		// trip this; bot-with-giant-payload does. We DON'T fall
		// through here because ParseForm may have consumed bytes
		// the downstream handler would re-read.
		a.logEvent("antibot.form_too_large", r, "err", err.Error())
		http.Error(w, `{"error":"payload too large"}`, http.StatusRequestEntityTooLarge)
		return true
	}

	triggered := make([]string, 0, len(cfg.HoneypotFields))
	for _, name := range cfg.HoneypotFields {
		if v := r.PostForm.Get(name); v != "" {
			triggered = append(triggered, name)
		}
	}
	if len(triggered) == 0 {
		return false
	}

	a.logEvent("antibot.honeypot_triggered", r,
		"fields", strings.Join(triggered, ","),
		"user_agent", r.Header.Get("User-Agent"))

	// Bait response: looks like success to the bot, so it moves on
	// to its next target instead of mutating its payload until it
	// gets through. Empty JSON object is the lowest-information
	// "success" we can return — no error code to fingerprint.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
	return true
}

// logEvent emits a structured slog record at INFO. Anti-bot hits
// are operationally interesting (volume = scraping intensity) but
// not error-level — humans never trip these in real-world flows.
func (a *AntiBot) logEvent(event string, r *http.Request, kv ...any) {
	if a.log == nil {
		return
	}
	args := make([]any, 0, 6+len(kv))
	args = append(args,
		"event", event,
		"path", r.URL.Path,
		"remote", clientRemote(r),
	)
	args = append(args, kv...)
	a.log.Info("antibot", args...)
}

// --- helpers ---

func hasAnyPrefix(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if p == "" {
			continue
		}
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// isFormContentType reports whether ct is one of the form-encoded
// media types ParseForm handles. We don't enforce honeypot on JSON
// bodies because the JSON-API surface is SDK-driven; honeypot
// fields only make sense for HTML forms a human (or scraper) might
// fill in.
func isFormContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	// strip any "; charset=..." trailer
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "application/x-www-form-urlencoded" ||
		ct == "multipart/form-data"
}

// clientRemote returns a best-effort IP string for log lines. We
// prefer the ctx-stashed value (set by IPFilter's clientIP() when
// it ran upstream); fall back to RemoteAddr's host portion. This
// is for human-readable logs, NOT for security decisions — the
// IPFilter middleware already authoritatively resolved the IP.
func clientRemote(r *http.Request) string {
	if ip := ClientIP(r.Context()); ip != nil {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ParseStringList accepts either a JSON array (`["a","b"]`) or a
// comma-separated string ("a,b"). Returns nil + nil for empty
// input — settings subscribers pass that straight through to
// AntiBotConfig and the middleware treats empty lists as "skip
// that axis".
//
// Exported so the wiring layer can re-use the same parse logic
// for all four list-shaped settings without re-implementing
// the JSON-or-CSV dance.
func ParseStringList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, err
		}
		// Trim each entry; drop blanks.
		clean := make([]string, 0, len(out))
		for _, s := range out {
			if t := strings.TrimSpace(s); t != "" {
				clean = append(clean, t)
			}
		}
		return clean, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out, nil
}
