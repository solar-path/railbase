// Package contact mounts the public POST /api/contact endpoint that
// backs the scaffold's "Contact sales" page.
//
// Design:
//
//   - PUBLIC: no auth required (the form is on the marketing site).
//   - Rate-limited per IP: cheap in-memory token bucket so an
//     anonymous attacker can't burn the operator's email quota. The
//     scaffold's UI sends one submission per ~30s; the bucket allows
//     5 per minute per IP.
//   - Mailer-driven: when the mailer is wired and a `contact.recipient`
//     setting is configured, the handler sends the submission to that
//     address. When EITHER is missing, the handler returns 503 with
//     a clear "contact form not configured" message — operators see
//     it in dev and wire what's missing.
//   - Honeypot: a hidden `website` field on the form; non-empty values
//     return 202 silently (no email sent). Cheap bot deterrent that
//     doesn't degrade the legitimate path.
//
// Routes:
//
//	POST /api/contact   — submit (rate-limited; mailer required)
package contact

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/mailer"
)

// Deps wires the mailer + recipient address. RecipientEmail is the
// To: of every successful submission; operators set it via settings
// or env. When either Mailer or RecipientEmail is empty, the handler
// 503s — keeps the failure loud.
type Deps struct {
	Mailer         *mailer.Mailer
	RecipientEmail string
	// SiteName is interpolated into the subject so the operator's
	// inbox shows "[<site>] Sales inquiry — <name>". Empty defaults
	// to "Contact form".
	SiteName string
	Log      *slog.Logger
}

// Mount installs POST /api/contact. Caller should NOT install the
// auth middleware on this route — submissions are anonymous.
func Mount(r chi.Router, d *Deps) {
	limiter := newLimiter(5, time.Minute) // 5 submissions / IP / minute
	r.Post("/api/contact", d.submit(limiter))
}

// body is the wire shape. message + email + name are required. The
// `website` field is the honeypot; legitimate users can't see it.
type body struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company"`
	Phone   string `json:"phone"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	// Website is the honeypot. The HTML form has this field hidden
	// from screen-readers + sighted users; only naive bots fill it.
	Website string `json:"website"`
}

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (d *Deps) submit(lim *limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !lim.allow(ip) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeRateLimit,
				"too many contact submissions; try again later"))
			return
		}
		var in body
		if err := decodeJSON(r, &in); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
			return
		}
		// Honeypot — bot path. Return 202 (looks like success to the
		// caller) without sending the email or logging an error. We
		// DO record at info-level so an operator chasing missing
		// submissions can spot the false negatives.
		if strings.TrimSpace(in.Website) != "" {
			if d.Log != nil {
				d.Log.Info("contact: honeypot triggered", "ip", ip, "email", in.Email)
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		name := strings.TrimSpace(in.Name)
		email := strings.TrimSpace(in.Email)
		message := strings.TrimSpace(in.Message)
		if name == "" || len(name) > 120 {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name is required (max 120 chars)"))
			return
		}
		if !emailRE.MatchString(email) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "valid email is required"))
			return
		}
		if message == "" || len(message) > 5000 {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "message is required (max 5000 chars)"))
			return
		}
		// Mailer / recipient must be configured. Loud 503 rather than
		// silent drop so dev environments don't accumulate ghost
		// submissions.
		if d.Mailer == nil || d.RecipientEmail == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
				"contact form not configured (set mailer + contact.recipient)"))
			return
		}

		subj := "[" + nonEmpty(d.SiteName, "Contact form") + "] " +
			nonEmpty(strings.TrimSpace(in.Subject), "Sales inquiry") +
			" — " + name
		bodyText := buildText(in, name, email, ip)
		bodyHTML := buildHTML(in, name, email, ip)

		msg := mailer.Message{
			To:      []mailer.Address{{Email: d.RecipientEmail}},
			ReplyTo: mailer.Address{Email: email, Name: name},
			Subject: subj,
			Text:    bodyText,
			HTML:    bodyHTML,
		}
		if err := d.Mailer.SendDirect(r.Context(), msg); err != nil {
			if d.Log != nil {
				d.Log.Error("contact: send failed", "err", err, "email", email)
			}
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "send failed"))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func buildText(in body, name, email, ip string) string {
	var b strings.Builder
	b.WriteString("New contact form submission\n\n")
	b.WriteString("Name:    " + name + "\n")
	b.WriteString("Email:   " + email + "\n")
	if c := strings.TrimSpace(in.Company); c != "" {
		b.WriteString("Company: " + c + "\n")
	}
	if p := strings.TrimSpace(in.Phone); p != "" {
		b.WriteString("Phone:   " + p + "\n")
	}
	b.WriteString("IP:      " + ip + "\n\n")
	b.WriteString("Message:\n")
	b.WriteString(strings.TrimSpace(in.Message))
	b.WriteString("\n")
	return b.String()
}

func buildHTML(in body, name, email, ip string) string {
	// Plain, semantic HTML — operator inboxes don't need branding,
	// and a trimmed template means less to escape. We DO escape every
	// user-supplied field; the scaffold UI can pre-validate but the
	// handler must not trust it.
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:system-ui,sans-serif;line-height:1.5">`)
	b.WriteString(`<h2>New contact form submission</h2>`)
	b.WriteString(`<table style="border-collapse:collapse">`)
	b.WriteString(`<tr><td><b>Name</b></td><td>` + escapeHTML(name) + `</td></tr>`)
	b.WriteString(`<tr><td><b>Email</b></td><td><a href="mailto:` + escapeHTML(email) + `">` + escapeHTML(email) + `</a></td></tr>`)
	if c := strings.TrimSpace(in.Company); c != "" {
		b.WriteString(`<tr><td><b>Company</b></td><td>` + escapeHTML(c) + `</td></tr>`)
	}
	if p := strings.TrimSpace(in.Phone); p != "" {
		b.WriteString(`<tr><td><b>Phone</b></td><td>` + escapeHTML(p) + `</td></tr>`)
	}
	b.WriteString(`<tr><td><b>IP</b></td><td>` + escapeHTML(ip) + `</td></tr>`)
	b.WriteString(`</table>`)
	b.WriteString(`<h3>Message</h3>`)
	b.WriteString(`<pre style="white-space:pre-wrap">` + escapeHTML(strings.TrimSpace(in.Message)) + `</pre>`)
	b.WriteString(`</body></html>`)
	return b.String()
}

// escapeHTML is the small subset we need — operator inboxes don't
// render exotic tags. Keeps the dep surface tiny.
func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func clientIP(r *http.Request) string {
	// Prefer the leftmost X-Forwarded-For entry — that's the caller
	// before any proxies the operator runs in front of railbase.
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if i := strings.Index(h, ","); i >= 0 {
			return strings.TrimSpace(h[:i])
		}
		return strings.TrimSpace(h)
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		return strings.TrimSpace(h)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- limiter ------------------------------------------------------------

// limiter is a coarse per-IP token bucket. Sized for the contact
// form path: 5 submissions per minute is roomy for legitimate use
// (autocomplete-failure retries) but defeats trivial flooding. The
// scaffold's UI sets a 30s lockout client-side; the server-side
// limit is the real defence.
//
// Memory cost is bounded by the GC pass that drops entries idle for
// >5 minutes.
type limiter struct {
	max    int
	window time.Duration
	mu     sync.Mutex
	hits   map[string][]time.Time
	last   time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(ip string) bool {
	if ip == "" {
		return true // can't rate-limit without a key; let it through
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	// Cheap GC: every minute or so, drop stale entries.
	if now.Sub(l.last) > time.Minute {
		for k, ts := range l.hits {
			fresh := ts[:0]
			for _, t := range ts {
				if now.Sub(t) <= l.window {
					fresh = append(fresh, t)
				}
			}
			if len(fresh) == 0 {
				delete(l.hits, k)
			} else {
				l.hits[k] = fresh
			}
		}
		l.last = now
	}
	cutoff := now.Add(-l.window)
	ts := l.hits[ip]
	fresh := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= l.max {
		l.hits[ip] = fresh
		return false
	}
	l.hits[ip] = append(fresh, now)
	return true
}

// --- helpers ------------------------------------------------------------

func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if len(body) == 0 {
		return errEmpty
	}
	return json.Unmarshal(body, dst)
}

var errEmpty = &emptyErr{}

type emptyErr struct{}

func (e *emptyErr) Error() string { return "empty request body" }
