// Package mailer is Railbase's outbound email surface.
//
// Layering:
//
//	caller (auth flow / hook / CLI)
//	   │
//	   ▼
//	mailer.Mailer.Send(ctx, Message)
//	   │           ├─► template render (markdown → html + text)
//	   │           ├─► rate limit check
//	   │           └─► driver.Send (smtp | console | …)
//
// Drivers conform to the small `Driver` interface so adding a v1.1
// SES adapter is one new file.
//
// What v1.0 ships:
//
//   - Mailer interface + Message + Address shape
//   - SMTP driver (net/smtp with STARTTLS + PLAIN/LOGIN auth)
//   - Console driver (prints message to a writer — dev mode)
//   - Markdown→HTML template engine with `--- frontmatter ---`
//   - Template resolver: filesystem (`pb_data/email_templates/`)
//     → embedded defaults
//   - Built-in templates: signup_verification, password_reset,
//     email_change, otp, magic_link, invite, 2fa_recovery, new_device
//   - Global + per-recipient rate limiter (in-process)
//   - `railbase mailer test` CLI
//
// What's deferred:
//
//   - SES native adapter — v1.0.1 (one file, no breaking changes)
//   - Postmark/Sendgrid/Mailgun — plugins (docs/15)
//   - fsnotify hot-reload — v1.0.1 (templates already re-read each
//     send so disk edits take effect on next send anyway)
//   - i18n (.ru.md, .en.md) — v1.1 alongside i18n resolver
//   - Per-tenant template overrides — v1.1 with orgs plugin
//   - Mailer hooks dispatcher — v1.1 with goja
//   - Bounce/delivery webhooks — plugin territory
package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/eventbus"
)

// Address is an email address with an optional display name.
//
// JSON marshals as { "email": "...", "name": "..." } so the same
// shape works for hooks scripting and admin-UI testing.
type Address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// String formats as `"Name" <email@example.com>` or just the email
// when Name is empty. Suitable for an RFC 5322 header value.
func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	// Avoid quote-escaping rabbit hole — operator-controlled value,
	// rejected at config time if it contains `"` or `\r\n`.
	return fmt.Sprintf("%q <%s>", a.Name, a.Email)
}

// Attachment is a single attached file. Content is the raw bytes;
// we don't stream from disk in v1.0 because attachments are rare and
// small (<5 MB typical) — streaming lands when the file storage
// driver does (v1.3).
type Attachment struct {
	Filename string
	MIMEType string // optional; sniffed from filename when empty
	Content  []byte
}

// Message is the resolved (post-template) email ready to send. Either
// `Subject`+`HTML`+`Text` come from a rendered template, or the
// caller supplies them directly via Mailer.SendDirect.
type Message struct {
	From    Address
	To      []Address
	CC      []Address
	BCC     []Address
	ReplyTo Address

	Subject string
	HTML    string
	Text    string // plaintext fallback (auto-generated from HTML if empty)

	Attachments []Attachment

	// Headers carries extra RFC 5322 headers (e.g. List-Unsubscribe).
	// X- prefix is fine; the driver passes them through verbatim.
	Headers map[string]string
}

// Driver is the transport layer. Implementations live in this
// package as siblings (smtp.go, console.go) but external plugins
// can also implement this interface and inject themselves via
// `mailer.Use(driver)` at boot.
type Driver interface {
	// Send delivers msg. The returned error is treated as transient
	// (caller may retry) unless wrapped in ErrPermanent.
	Send(ctx context.Context, msg Message) error
	// Name returns a stable identifier for logging / metrics.
	Name() string
}

// ErrPermanent wraps a driver error to signal "do not retry"
// (e.g. invalid recipient, authentication failure on a fixed
// credential). The job queue / retry logic checks via errors.Is.
var ErrPermanent = errors.New("mailer: permanent failure")

// ErrRateLimited is returned by Send when the rate limiter blocks
// the call. Caller decides whether to queue + retry later.
var ErrRateLimited = errors.New("mailer: rate limited")

// ErrTemplateNotFound is returned when SendTemplate can't locate
// the named template in any layer (filesystem + embedded).
var ErrTemplateNotFound = errors.New("mailer: template not found")

// Mailer is the public service. Constructed once in app.go and
// shared for the process lifetime. Goroutine-safe — the underlying
// Driver and Templates handle concurrency internally.
type Mailer struct {
	driver    Driver
	templates *Templates
	limiter   *Limiter
	log       *slog.Logger
	// bus, when non-nil, receives mailer.before_send (sync) and
	// mailer.after_send (async) events. nil = no events published,
	// preserving zero-cost behaviour for callers that don't wire
	// hooks. See events.go for payload shapes.
	bus *eventbus.Bus

	// events, when non-nil, receives one persisted row per (recipient,
	// outcome) after every Send. nil keeps the v1.0 zero-cost path —
	// tests + embedded callers that never wired a DB get the same
	// behaviour they always had. See events_store.go.
	events *EventStore

	// defaultFrom is the fallback From address when a template /
	// caller doesn't set one. Operator-configured.
	defaultFrom Address
}

// Options are the runtime knobs the constructor accepts. All fields
// are optional — zero values yield a working "console driver +
// embedded templates + no rate limiter" instance, suitable for tests.
type Options struct {
	Driver      Driver
	Templates   *Templates
	Limiter     *Limiter
	Log         *slog.Logger
	DefaultFrom Address
	// Bus, when non-nil, enables the mailer hooks dispatcher.
	// Subscribers to TopicBeforeSend (sync) can mutate/reject; subscribers
	// to TopicAfterSend (async) can observe for telemetry. nil = no
	// events published, matching pre-hooks behaviour.
	Bus *eventbus.Bus
	// EventStore, when non-nil, persists one `_email_events` row per
	// (recipient, outcome) after each driver.Send returns. nil = no
	// persistence (tests, embedded callers without a DB pool). See
	// events_store.go for the wiring rationale.
	EventStore *EventStore
}

// New builds a Mailer. Pass nil Driver → console driver writing to
// os.Stdout; nil Templates → embedded defaults only.
func New(opts Options) *Mailer {
	driver := opts.Driver
	if driver == nil {
		driver = NewConsoleDriver(nil)
	}
	tpl := opts.Templates
	if tpl == nil {
		tpl = NewTemplates(TemplatesOptions{})
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Mailer{
		driver:      driver,
		templates:   tpl,
		limiter:     opts.Limiter,
		log:         log,
		bus:         opts.Bus,
		events:      opts.EventStore,
		defaultFrom: opts.DefaultFrom,
	}
}

// SendDirect bypasses the template engine and sends msg as-is. Used
// by hooks that build the body themselves and by the test CLI.
//
// Subject is required. At least one of To/CC/BCC must be non-empty.
// If both HTML and Text are empty the call returns an error.
func (m *Mailer) SendDirect(ctx context.Context, msg Message) error {
	return m.sendInternal(ctx, msg, "")
}

// sendInternal is the shared core for SendDirect + SendTemplate. The
// extra `template` argument carries the template name down to the
// persistence layer so EventStore rows record "which template fired"
// for SendTemplate callers (empty string for direct sends). Public
// callers should use SendDirect / SendTemplate — this stays unexported.
func (m *Mailer) sendInternal(ctx context.Context, msg Message, template string) error {
	if msg.Subject == "" {
		return fmt.Errorf("mailer: subject is required")
	}
	if len(msg.To)+len(msg.CC)+len(msg.BCC) == 0 {
		return fmt.Errorf("mailer: at least one recipient is required")
	}
	if msg.HTML == "" && msg.Text == "" {
		return fmt.Errorf("mailer: HTML or Text body is required")
	}
	if (msg.From == Address{}) {
		msg.From = m.defaultFrom
	}
	if msg.Text == "" {
		msg.Text = htmlToText(msg.HTML)
	}
	if err := m.checkRate(ctx, msg); err != nil {
		return err
	}
	// Hooks dispatcher (§3.1.6). Synchronous before_send so subscribers
	// can mutate the message (e.g. rewrite From, inject X-Tenant) or
	// veto the send by setting Reject. After_send is async — observers
	// don't block the response. Skipped entirely when no bus wired.
	if m.bus != nil {
		ev := &MailerBeforeSendEvent{Message: &msg}
		m.bus.PublishSync(ctx, eventbus.Event{Topic: TopicBeforeSend, Payload: ev})
		if ev.Reject {
			reason := ev.Reason
			if reason == "" {
				reason = "rejected"
			}
			return fmt.Errorf("mailer: rejected by before-send hook: %s", reason)
		}
	}
	start := time.Now()
	err := m.driver.Send(ctx, msg)
	m.log.Info("mailer: send",
		"driver", m.driver.Name(),
		"to", recipientCount(msg),
		"subject", msg.Subject,
		"duration_ms", time.Since(start).Milliseconds(),
		"err", err)
	// Persistent shadow of the outcome (§3.1.4). One row per recipient
	// — see events_store.go for the per-recipient rationale. No-op when
	// m.events is nil (preserves zero-config UX + existing test setups).
	m.recordSendOutcome(ctx, msg, err, template)
	if m.bus != nil {
		m.bus.Publish(eventbus.Event{
			Topic:   TopicAfterSend,
			Payload: MailerAfterSendEvent{Message: msg, Err: err},
		})
	}
	return err
}

// SendTemplate renders the named template with data, then sends. The
// template's frontmatter supplies Subject / From / Reply-To; the
// caller's overrides on msg take precedence (so a test send to a
// different address can override the template's `to:` field).
//
// data is interpolated into the template body as `{{ key.path }}`
// — see ./templates.go for the variable shape.
func (m *Mailer) SendTemplate(ctx context.Context, templateName string, recipients []Address, data map[string]any) error {
	rendered, err := m.templates.Render(templateName, data)
	if err != nil {
		return err
	}
	from := rendered.From
	if (from == Address{}) {
		from = m.defaultFrom
	}
	msg := Message{
		From:    from,
		To:      recipients,
		ReplyTo: rendered.ReplyTo,
		Subject: rendered.Subject,
		HTML:    rendered.HTML,
		Text:    rendered.Text,
	}
	// Funnel through sendInternal so the EventStore row records the
	// template name. The before/after hooks still fire from
	// sendInternal — the contract that "SendTemplate hooks the same
	// way SendDirect does" (tested in events_test.go) is preserved.
	return m.sendInternal(ctx, msg, templateName)
}

func (m *Mailer) checkRate(ctx context.Context, msg Message) error {
	if m.limiter == nil {
		return nil
	}
	for _, r := range msg.To {
		if err := m.limiter.Allow(ctx, r.Email); err != nil {
			return err
		}
	}
	return nil
}

func recipientCount(msg Message) int {
	return len(msg.To) + len(msg.CC) + len(msg.BCC)
}

// flattenRecipients returns every recipient address (To + CC + BCC)
// in send order. Used by drivers that need RCPT TO commands.
func flattenRecipients(msg Message) []string {
	out := make([]string, 0, recipientCount(msg))
	for _, a := range msg.To {
		out = append(out, a.Email)
	}
	for _, a := range msg.CC {
		out = append(out, a.Email)
	}
	for _, a := range msg.BCC {
		out = append(out, a.Email)
	}
	return out
}

// formatRecipientList renders a To: / CC: header value from a slice
// of addresses, comma-separated per RFC 5322.
func formatRecipientList(addrs []Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}
