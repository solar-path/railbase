package mailer

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// ConsoleDriver prints the message to a writer instead of dialing
// SMTP. The default writer is os.Stdout — useful for `railbase serve`
// in dev mode so signup flows are observable without standing up a
// real SMTP server.
//
// Tests pass a *bytes.Buffer and assert on its contents.
type ConsoleDriver struct {
	mu sync.Mutex
	w  io.Writer

	// Sent records every message in send order. Tests read from
	// `.Captured()`. Production code shouldn't read this — the
	// slice grows unbounded.
	captured []Message
}

// NewConsoleDriver returns a Driver writing to w. nil → os.Stdout.
func NewConsoleDriver(w io.Writer) *ConsoleDriver {
	if w == nil {
		w = os.Stdout
	}
	return &ConsoleDriver{w: w}
}

func (d *ConsoleDriver) Name() string { return "console" }

func (d *ConsoleDriver) Send(_ context.Context, msg Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.captured = append(d.captured, msg)

	var b strings.Builder
	b.WriteString("\n--- mailer (console driver) ---\n")
	if msg.From.Email != "" {
		fmt.Fprintf(&b, "From:    %s\n", msg.From)
	}
	if len(msg.To) > 0 {
		fmt.Fprintf(&b, "To:      %s\n", formatRecipientList(msg.To))
	}
	if len(msg.CC) > 0 {
		fmt.Fprintf(&b, "Cc:      %s\n", formatRecipientList(msg.CC))
	}
	if len(msg.BCC) > 0 {
		fmt.Fprintf(&b, "Bcc:     %s\n", formatRecipientList(msg.BCC))
	}
	if (msg.ReplyTo != Address{}) {
		fmt.Fprintf(&b, "Reply-To: %s\n", msg.ReplyTo)
	}
	fmt.Fprintf(&b, "Subject: %s\n", msg.Subject)
	if len(msg.Attachments) > 0 {
		fmt.Fprintf(&b, "Attachments: %d\n", len(msg.Attachments))
		for _, a := range msg.Attachments {
			fmt.Fprintf(&b, "  - %s (%d bytes)\n", a.Filename, len(a.Content))
		}
	}
	b.WriteString("\n")
	if msg.Text != "" {
		b.WriteString(msg.Text)
		b.WriteString("\n")
	} else if msg.HTML != "" {
		b.WriteString("[HTML body — text fallback empty]\n")
		b.WriteString(htmlToText(msg.HTML))
		b.WriteString("\n")
	}
	b.WriteString("--- end mailer ---\n")

	_, err := d.w.Write([]byte(b.String()))
	return err
}

// Captured returns a snapshot of all messages this driver has been
// asked to send. Test helper — not for production code paths.
func (d *ConsoleDriver) Captured() []Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Message, len(d.captured))
	copy(out, d.captured)
	return out
}

// Reset clears the captured slice. Tests call this between cases.
func (d *ConsoleDriver) Reset() {
	d.mu.Lock()
	d.captured = nil
	d.mu.Unlock()
}
