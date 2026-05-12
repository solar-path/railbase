package mailer

import (
	"html"
	"strings"
)

// RenderMarkdownForCLI is the public entry point for code outside
// the package that needs the same Markdown→HTML conversion the
// template engine uses (the `mailer test` CLI uses this for the
// `--body` flag).
func RenderMarkdownForCLI(src string) string { return markdownToHTML(src) }

// markdownToHTML converts a small Markdown subset to HTML suitable
// for email bodies. The supported features are intentionally narrow:
//
//   - headings:   # h1, ## h2, ### h3
//   - paragraphs: blank-line-separated text
//   - bullet list: lines starting with "- " or "* "
//   - inline:     **bold**, *italic*, `code`, [label](url)
//   - escape:     everything else gets HTML-escaped
//
// We don't aim for CommonMark fidelity — operator-authored email
// templates are tightly controlled, and full CommonMark would pull
// in a heavy dependency (goldmark = ~250KB to binary size). The
// hand-rolled converter weighs <2KB.
//
// Tested round-trip via templates_test.go. Future v1.x can swap in
// goldmark by changing this function alone — `Rendered.HTML` is
// the only output that consumes it.
func markdownToHTML(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	var b strings.Builder
	var para []string
	var bullets []string

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, " ")
		b.WriteString("<p>")
		b.WriteString(renderInline(text))
		b.WriteString("</p>\n")
		para = nil
	}
	flushBullets := func() {
		if len(bullets) == 0 {
			return
		}
		b.WriteString("<ul>\n")
		for _, item := range bullets {
			b.WriteString("  <li>")
			b.WriteString(renderInline(item))
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
		bullets = nil
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimLeft(line, " \t")

		switch {
		case trimmed == "":
			flushPara()
			flushBullets()
		case strings.HasPrefix(trimmed, "### "):
			flushPara()
			flushBullets()
			b.WriteString("<h3>")
			b.WriteString(renderInline(trimmed[4:]))
			b.WriteString("</h3>\n")
		case strings.HasPrefix(trimmed, "## "):
			flushPara()
			flushBullets()
			b.WriteString("<h2>")
			b.WriteString(renderInline(trimmed[3:]))
			b.WriteString("</h2>\n")
		case strings.HasPrefix(trimmed, "# "):
			flushPara()
			flushBullets()
			b.WriteString("<h1>")
			b.WriteString(renderInline(trimmed[2:]))
			b.WriteString("</h1>\n")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			flushPara()
			bullets = append(bullets, trimmed[2:])
		default:
			flushBullets()
			para = append(para, trimmed)
		}
	}
	flushPara()
	flushBullets()
	return strings.TrimRight(b.String(), "\n")
}

// renderInline handles the inline pass: bold/italic/code/link, then
// escapes everything else. Order matters — we capture inline tokens
// into placeholders, escape the rest, then expand placeholders back.
//
// The placeholder strategy avoids the double-escape pitfall (escaping
// `<a href=...>` produced by the link expansion).
func renderInline(s string) string {
	type token struct {
		marker string // unique placeholder
		html   string // pre-baked HTML to swap back
	}
	var tokens []token
	emit := func(htmlSnippet string) string {
		m := "\x00RB" + idxString(len(tokens)) + "\x00"
		tokens = append(tokens, token{marker: m, html: htmlSnippet})
		return m
	}

	// 1. links: [label](url)
	s = replaceAllFn(s, "[", "]", "(", ")", func(label, url string) string {
		safeURL := html.EscapeString(url)
		safeLabel := html.EscapeString(label)
		return emit(`<a href="` + safeURL + `">` + safeLabel + `</a>`)
	})

	// 2. inline code: `code`
	s = replacePairs(s, "`", "`", func(inner string) string {
		return emit("<code>" + html.EscapeString(inner) + "</code>")
	})

	// 3. bold: **text** (must run before italic so ** isn't eaten as *)
	s = replacePairs(s, "**", "**", func(inner string) string {
		return emit("<strong>" + html.EscapeString(inner) + "</strong>")
	})

	// 4. italic: *text*
	s = replacePairs(s, "*", "*", func(inner string) string {
		return emit("<em>" + html.EscapeString(inner) + "</em>")
	})

	// 5. escape the leftover plaintext
	s = html.EscapeString(s)

	// 6. restore placeholders (escape twice swapped \x00 etc.; but
	// EscapeString leaves \x00 alone, so the markers survive intact).
	for _, t := range tokens {
		s = strings.ReplaceAll(s, t.marker, t.html)
	}
	return s
}

// replacePairs swaps every occurrence of <open>X<close> by calling
// transform on X. open and close may be identical (e.g. "*").
// Single-line only — markers don't span line breaks.
func replacePairs(s, open, close string, transform func(inner string) string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], open)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+j])
		i += j + len(open)
		k := strings.Index(s[i:], close)
		// Disallow newline inside the captured run so an unmatched
		// asterisk doesn't eat the rest of the line.
		if k < 0 || strings.IndexByte(s[i:i+k], '\n') >= 0 {
			b.WriteString(open)
			continue
		}
		b.WriteString(transform(s[i : i+k]))
		i += k + len(close)
	}
	return b.String()
}

// replaceAllFn handles the `[label](url)` style: two paired-bracket
// runs in a row. Inline only.
func replaceAllFn(s string, lo, lc, ro, rc string, fn func(label, url string) string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], lo)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+j])
		// Look for the closing bracket on the same line.
		labelEnd := strings.Index(s[i+j+len(lo):], lc)
		if labelEnd < 0 {
			b.WriteString(s[i+j:])
			break
		}
		// Must be immediately followed by `(`.
		afterLabel := i + j + len(lo) + labelEnd + len(lc)
		if afterLabel >= len(s) || !strings.HasPrefix(s[afterLabel:], ro) {
			// Not a link — emit the [ literally and resume scan.
			b.WriteByte(s[i+j])
			i = i + j + 1
			continue
		}
		urlStart := afterLabel + len(ro)
		urlEnd := strings.Index(s[urlStart:], rc)
		if urlEnd < 0 {
			b.WriteString(s[i+j:])
			break
		}
		label := s[i+j+len(lo) : i+j+len(lo)+labelEnd]
		url := s[urlStart : urlStart+urlEnd]
		b.WriteString(fn(label, url))
		i = urlStart + urlEnd + len(rc)
	}
	return b.String()
}

// idxString stringifies a small non-negative int without importing
// strconv (keeps the conversion inlined to keep markers stable).
func idxString(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
