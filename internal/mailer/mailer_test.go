package mailer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// We cover the parts of the package that work offline: template
// rendering, Markdown conversion, plain-text fallback, rate limiter,
// SendDirect dispatch with the console driver. SMTP itself is tested
// in the e2e smoke against a live server — net/smtp would need a
// test SMTP server to verify here, which is out of v1.0 scope.

func TestConsoleDriver_CapturesMessage(t *testing.T) {
	var buf bytes.Buffer
	drv := NewConsoleDriver(&buf)
	m := New(Options{Driver: drv, DefaultFrom: Address{Email: "from@example.com"}})

	err := m.SendDirect(context.Background(), Message{
		To:      []Address{{Email: "to@example.com"}},
		Subject: "Hello",
		HTML:    "<p>Hi <strong>there</strong></p>",
	})
	if err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{"From:", "from@example.com", "To:", "to@example.com", "Subject: Hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}

	captured := drv.Captured()
	if len(captured) != 1 {
		t.Fatalf("captured=%d, want 1", len(captured))
	}
	if captured[0].Text == "" {
		t.Errorf("Text fallback not generated for HTML-only send")
	}
}

func TestSendDirect_RequiresFields(t *testing.T) {
	drv := NewConsoleDriver(&bytes.Buffer{})
	m := New(Options{Driver: drv})
	ctx := context.Background()

	cases := []struct {
		name string
		msg  Message
	}{
		{"no subject", Message{To: []Address{{Email: "x@x"}}, HTML: "<p>x</p>"}},
		{"no recipient", Message{Subject: "x", HTML: "<p>x</p>"}},
		{"no body", Message{To: []Address{{Email: "x@x"}}, Subject: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := m.SendDirect(ctx, c.msg); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestRateLimit_PerRecipient(t *testing.T) {
	lim := NewLimiter(LimiterConfig{
		GlobalPerMinute:  1000,
		PerRecipientHour: 2,
	})
	drv := NewConsoleDriver(&bytes.Buffer{})
	m := New(Options{
		Driver:      drv,
		Limiter:     lim,
		DefaultFrom: Address{Email: "from@example.com"},
	})
	ctx := context.Background()

	send := func() error {
		return m.SendDirect(ctx, Message{
			To:      []Address{{Email: "alice@example.com"}},
			Subject: "x",
			Text:    "x",
		})
	}

	for i := 0; i < 2; i++ {
		if err := send(); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := send(); !errors.Is(err, ErrRateLimited) {
		t.Errorf("third send err=%v, want ErrRateLimited", err)
	}
}

func TestRateLimit_Global(t *testing.T) {
	lim := NewLimiter(LimiterConfig{
		GlobalPerMinute:  2,
		PerRecipientHour: 1000,
	})
	drv := NewConsoleDriver(&bytes.Buffer{})
	m := New(Options{
		Driver:      drv,
		Limiter:     lim,
		DefaultFrom: Address{Email: "from@example.com"},
	})

	ctx := context.Background()
	for i, addr := range []string{"a@x", "b@x"} {
		if err := m.SendDirect(ctx, Message{
			To:      []Address{{Email: addr}},
			Subject: "x",
			Text:    "x",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := m.SendDirect(ctx, Message{
		To:      []Address{{Email: "c@x"}},
		Subject: "x",
		Text:    "x",
	}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("third send err=%v, want ErrRateLimited", err)
	}
}

func TestRateLimit_WindowSlide(t *testing.T) {
	now := time.Now()
	lim := NewLimiter(LimiterConfig{
		GlobalPerMinute:  2,
		PerRecipientHour: 1000,
		Now:              func() time.Time { return now },
	})
	ctx := context.Background()

	if err := lim.Allow(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if err := lim.Allow(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if err := lim.Allow(ctx, "x"); !errors.Is(err, ErrRateLimited) {
		t.Error("expected rate limit")
	}
	// Slide the clock past the window.
	now = now.Add(2 * time.Minute)
	if err := lim.Allow(ctx, "x"); err != nil {
		t.Errorf("after window: %v", err)
	}
}

func TestTemplates_RendersBuiltin(t *testing.T) {
	t.Parallel()
	tpl := NewTemplates(TemplatesOptions{})
	data := map[string]any{
		"site":       map[string]any{"name": "Acme", "from": "noreply@acme.test"},
		"user":       map[string]any{"email": "alice@example.com"},
		"verify_url": "https://acme.test/verify/abc",
	}
	r, err := tpl.Render("signup_verification", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Subject, "Acme") {
		t.Errorf("subject not interpolated: %q", r.Subject)
	}
	if !strings.Contains(r.HTML, "alice@example.com") {
		t.Errorf("HTML missing user email: %s", r.HTML)
	}
	if !strings.Contains(r.HTML, `href="https://acme.test/verify/abc"`) {
		t.Errorf("link not rendered: %s", r.HTML)
	}
	if !strings.Contains(r.Text, "Verify email") {
		t.Errorf("text fallback empty / unrendered: %s", r.Text)
	}
	if r.From.Email != "noreply@acme.test" {
		t.Errorf("from=%v", r.From)
	}
}

func TestTemplates_NotFound(t *testing.T) {
	tpl := NewTemplates(TemplatesOptions{})
	_, err := tpl.Render("does_not_exist", nil)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("err=%v want ErrTemplateNotFound", err)
	}
}

func TestTemplates_DiskOverrideWins(t *testing.T) {
	dir := t.TempDir()
	const customBody = `---
subject: Custom override
from: custom@example.com
---

# Hello, **{{ user.email }}**!
`
	if err := writeFile(dir, "signup_verification.md", customBody); err != nil {
		t.Fatal(err)
	}
	tpl := NewTemplates(TemplatesOptions{DiskDir: dir})
	r, err := tpl.Render("signup_verification", map[string]any{
		"user": map[string]any{"email": "alice@x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "Custom override" {
		t.Errorf("disk override didn't win: subject=%q", r.Subject)
	}
	if !strings.Contains(r.HTML, "<strong>alice@x</strong>") {
		t.Errorf("override body not rendered: %s", r.HTML)
	}
}

func TestMarkdown_BasicFeatures(t *testing.T) {
	in := `# Title

Some **bold** and *italic* and ` + "`code`" + ` and a [link](https://example.com).

## Subtitle

- one
- two
- three
`
	out := markdownToHTML(in)
	for _, want := range []string{
		"<h1>Title</h1>",
		"<strong>bold</strong>",
		"<em>italic</em>",
		"<code>code</code>",
		`<a href="https://example.com">link</a>`,
		"<h2>Subtitle</h2>",
		"<ul>",
		"<li>one</li>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestMarkdown_EscapesHTML(t *testing.T) {
	out := markdownToHTML("Hello <script>alert(1)</script>")
	if strings.Contains(out, "<script>") {
		t.Errorf("script tag survived: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("HTML not escaped: %s", out)
	}
}

func TestHTMLToText_StripsTags(t *testing.T) {
	in := `<p>Hello <strong>world</strong>. <a href="https://x.test">Click</a> here.</p>`
	out := htmlToText(in)
	if !strings.Contains(out, "Hello world") {
		t.Errorf("missing label: %s", out)
	}
	if !strings.Contains(out, "(https://x.test)") {
		t.Errorf("URL not surfaced: %s", out)
	}
	if strings.Contains(out, "<") {
		t.Errorf("tag survived: %s", out)
	}
}

func TestFrontmatter_Parses(t *testing.T) {
	front, body := splitFrontmatter([]byte("---\nsubject: hi\nfrom: x@y\n---\nbody here\n"))
	if !strings.Contains(front, "subject:") {
		t.Errorf("frontmatter not extracted: %q", front)
	}
	if !strings.Contains(body, "body here") {
		t.Errorf("body wrong: %q", body)
	}
	m := parseFrontmatter(front)
	if m["subject"] != "hi" {
		t.Errorf("subject=%q", m["subject"])
	}
	if m["from"] != "x@y" {
		t.Errorf("from=%q", m["from"])
	}
}

func TestParseAddress_Forms(t *testing.T) {
	cases := []struct {
		in   string
		want Address
	}{
		{"bare@example.com", Address{Email: "bare@example.com"}},
		{`"Name Here" <name@example.com>`, Address{Name: "Name Here", Email: "name@example.com"}},
		{"Name Here <name@example.com>", Address{Name: "Name Here", Email: "name@example.com"}},
	}
	for _, c := range cases {
		got := parseAddress(c.in)
		if got != c.want {
			t.Errorf("parseAddress(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestInterpolate_NestedPath(t *testing.T) {
	out := interpolate("hi {{ user.name }} ({{ user.email }})", map[string]any{
		"user": map[string]any{
			"name":  "Alice",
			"email": "a@x",
		},
	})
	if out != "hi Alice (a@x)" {
		t.Errorf("got %q", out)
	}
}

func TestInterpolate_MissingKey_Empty(t *testing.T) {
	out := interpolate("{{ a.b.c }}/{{ d }}", map[string]any{"d": "ok"})
	if out != "/ok" {
		t.Errorf("got %q", out)
	}
}

// --- helpers ---

func writeFile(dir, name, body string) error {
	return os.WriteFile(dir+"/"+name, []byte(body), 0o644)
}
