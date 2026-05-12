package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"path/filepath"
	"strings"
	"time"
)

// SMTPConfig is the operator-supplied SMTP target. Loaded from
// `settings._settings` keys `mailer.smtp.*` or, in dev, from env vars.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string

	// TLS modes:
	//   "starttls" (default) — connect plain, upgrade with STARTTLS
	//   "implicit"           — connect over TLS from the start
	//   "off"                — plain SMTP (dev only — never prod)
	TLS string

	// Optional ServerName for TLS handshake. Defaults to Host when empty.
	ServerName string

	// DialTimeout caps the TCP dial. 0 → 10s.
	DialTimeout time.Duration
}

// NewSMTPDriver returns a Driver that talks to cfg. The driver
// reconnects on every Send — SMTP is a session-per-message protocol
// and connection pooling adds complexity (auth state, timeouts) that
// isn't worth it under v1.0's expected volumes (<100 emails/min).
//
// v1.1 will add a pooled variant when bulk sends (newsletter mode)
// arrive.
func NewSMTPDriver(cfg SMTPConfig) Driver {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	return &smtpDriver{cfg: cfg}
}

type smtpDriver struct{ cfg SMTPConfig }

func (d *smtpDriver) Name() string { return "smtp" }

func (d *smtpDriver) Send(ctx context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", d.cfg.Host, d.cfg.Port)
	dialer := &net.Dialer{Timeout: d.cfg.DialTimeout}

	var conn net.Conn
	var err error

	tlsCfg := &tls.Config{
		ServerName: d.cfg.ServerName,
	}
	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = d.cfg.Host
	}

	mode := strings.ToLower(d.cfg.TLS)
	if mode == "" {
		mode = "starttls"
	}

	switch mode {
	case "implicit":
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	case "off", "starttls":
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	default:
		return fmt.Errorf("mailer/smtp: unknown TLS mode %q", d.cfg.TLS)
	}
	if err != nil {
		return fmt.Errorf("mailer/smtp: dial %s: %w", addr, err)
	}

	cli, err := smtp.NewClient(conn, d.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer/smtp: client: %w", err)
	}
	defer func() { _ = cli.Quit() }()

	// STARTTLS upgrade if requested AND server advertises it.
	if mode == "starttls" {
		if ok, _ := cli.Extension("STARTTLS"); ok {
			if err := cli.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("mailer/smtp: starttls: %w", err)
			}
		}
	}

	// Auth — PLAIN inside TLS only (don't leak credentials in clear).
	if d.cfg.Username != "" {
		secure := mode == "implicit" || mode == "starttls"
		var auth smtp.Auth
		if secure {
			auth = smtp.PlainAuth("", d.cfg.Username, d.cfg.Password, d.cfg.Host)
		} else {
			// Plain over non-TLS is rejected by net/smtp by default
			// (good!). We don't override that here — operator must
			// configure TLS or use CRAM-MD5 / OAUTH2 (out of v1.0
			// scope).
			return fmt.Errorf("mailer/smtp: refuse to send credentials over plain connection (set tls=starttls or implicit)")
		}
		if ok, mechs := cli.Extension("AUTH"); ok && strings.Contains(mechs, "PLAIN") {
			if err := cli.Auth(auth); err != nil {
				return fmt.Errorf("mailer/smtp: auth: %w", err)
			}
		}
	}

	from := msg.From.Email
	if from == "" {
		return fmt.Errorf("mailer/smtp: From is required")
	}
	if err := cli.Mail(from); err != nil {
		return fmt.Errorf("mailer/smtp: MAIL FROM: %w", err)
	}
	for _, rcpt := range flattenRecipients(msg) {
		if err := cli.Rcpt(rcpt); err != nil {
			return fmt.Errorf("mailer/smtp: RCPT %s: %w", rcpt, err)
		}
	}

	wc, err := cli.Data()
	if err != nil {
		return fmt.Errorf("mailer/smtp: DATA: %w", err)
	}
	if err := writeMIME(wc, msg); err != nil {
		_ = wc.Close()
		return fmt.Errorf("mailer/smtp: write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("mailer/smtp: close DATA: %w", err)
	}
	return nil
}

// writeMIME emits an RFC 5322 + MIME message to w.
//
// Layout:
//
//	Subject / From / To / etc.
//	Content-Type: multipart/mixed; boundary=…  (only if attachments)
//	  Content-Type: multipart/alternative; boundary=…
//	    Content-Type: text/plain; charset=utf-8     (text fallback)
//	    Content-Type: text/html; charset=utf-8      (HTML body)
//	  Content-Type: application/octet-stream …      (attachment 1)
//	  …
//
// We always emit multipart/alternative for the body (even when only
// HTML is present) so receiving clients with no HTML support pick
// up the text. The text fallback is auto-generated when the caller
// didn't supply one (see Mailer.SendDirect).
func writeMIME(w interface{ Write([]byte) (int, error) }, msg Message) error {
	var b strings.Builder

	// --- top-level headers ---
	hdr := textproto.MIMEHeader{}
	hdr.Set("MIME-Version", "1.0")
	if msg.From.Email != "" {
		hdr.Set("From", msg.From.String())
	}
	if len(msg.To) > 0 {
		hdr.Set("To", formatRecipientList(msg.To))
	}
	if len(msg.CC) > 0 {
		hdr.Set("Cc", formatRecipientList(msg.CC))
	}
	if (msg.ReplyTo != Address{}) {
		hdr.Set("Reply-To", msg.ReplyTo.String())
	}
	hdr.Set("Subject", encodeHeader(msg.Subject))
	hdr.Set("Date", time.Now().UTC().Format(time.RFC1123Z))
	for k, v := range msg.Headers {
		hdr.Set(k, v)
	}

	hasAttach := len(msg.Attachments) > 0

	if hasAttach {
		// multipart/mixed (alternative + attachments)
		outerBoundary := makeBoundary("mixed")
		hdr.Set("Content-Type", `multipart/mixed; boundary="`+outerBoundary+`"`)
		if err := writeHeaders(&b, hdr); err != nil {
			return err
		}

		mw := multipart.NewWriter(&b)
		if err := mw.SetBoundary(outerBoundary); err != nil {
			return err
		}

		// 1) alternative part
		altBoundary := makeBoundary("alt")
		altPart, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type": []string{`multipart/alternative; boundary="` + altBoundary + `"`},
		})
		if err != nil {
			return err
		}
		alt := multipart.NewWriter(altPart)
		if err := alt.SetBoundary(altBoundary); err != nil {
			return err
		}
		if err := writeAlternativeParts(alt, msg); err != nil {
			return err
		}
		if err := alt.Close(); err != nil {
			return err
		}

		// 2) attachments
		for _, a := range msg.Attachments {
			if err := writeAttachment(mw, a); err != nil {
				return err
			}
		}
		if err := mw.Close(); err != nil {
			return err
		}
	} else {
		altBoundary := makeBoundary("alt")
		hdr.Set("Content-Type", `multipart/alternative; boundary="`+altBoundary+`"`)
		if err := writeHeaders(&b, hdr); err != nil {
			return err
		}
		mw := multipart.NewWriter(&b)
		if err := mw.SetBoundary(altBoundary); err != nil {
			return err
		}
		if err := writeAlternativeParts(mw, msg); err != nil {
			return err
		}
		if err := mw.Close(); err != nil {
			return err
		}
	}

	_, err := w.Write([]byte(b.String()))
	return err
}

func writeAlternativeParts(mw *multipart.Writer, msg Message) error {
	// text/plain FIRST (older clients pick the first part they
	// understand; HTML clients prefer the last part).
	if msg.Text != "" {
		part, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              []string{"text/plain; charset=utf-8"},
			"Content-Transfer-Encoding": []string{"quoted-printable"},
		})
		if err != nil {
			return err
		}
		if _, err := part.Write([]byte(quotedPrintable(msg.Text))); err != nil {
			return err
		}
	}
	if msg.HTML != "" {
		part, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              []string{"text/html; charset=utf-8"},
			"Content-Transfer-Encoding": []string{"quoted-printable"},
		})
		if err != nil {
			return err
		}
		if _, err := part.Write([]byte(quotedPrintable(msg.HTML))); err != nil {
			return err
		}
	}
	return nil
}

func writeAttachment(mw *multipart.Writer, a Attachment) error {
	ct := a.MIMEType
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(a.Filename))
		if ct == "" {
			ct = "application/octet-stream"
		}
	}
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              []string{ct + `; name="` + a.Filename + `"`},
		"Content-Disposition":       []string{`attachment; filename="` + a.Filename + `"`},
		"Content-Transfer-Encoding": []string{"base64"},
	})
	if err != nil {
		return err
	}
	_, err = part.Write([]byte(base64Encode(a.Content)))
	return err
}

func writeHeaders(b *strings.Builder, h textproto.MIMEHeader) error {
	for k, vs := range h {
		for _, v := range vs {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\r\n")
	return nil
}
