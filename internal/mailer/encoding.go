package mailer

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"strings"
)

// makeBoundary returns a MIME-safe boundary string with a tagged
// prefix so wireshark-style traces are easy to follow. The tag is
// purely informational; receiver code reads boundary from headers.
func makeBoundary(tag string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failure on a healthy system is essentially
		// never — fall back to a fixed string rather than panicking
		// inside an email send.
		return tag + "-railbase-static"
	}
	return tag + "-railbase-" + base64.RawURLEncoding.EncodeToString(raw[:])
}

// quotedPrintable encodes s for use inside a `Content-Transfer-Encoding:
// quoted-printable` MIME part. Encodes non-ASCII safely + soft-wraps
// long lines per RFC 2045.
func quotedPrintable(s string) string {
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.String()
}

// base64Encode formats bytes for Content-Transfer-Encoding: base64,
// wrapping at 76 chars per RFC 2045.
func base64Encode(data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	const lineLen = 76
	var b strings.Builder
	for i := 0; i < len(enc); i += lineLen {
		end := i + lineLen
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		b.WriteString("\r\n")
	}
	return b.String()
}

// encodeHeader applies MIME encoded-word (RFC 2047) when s contains
// non-ASCII characters. Pure-ASCII strings pass through unchanged so
// `Subject: Hello` stays readable in raw transcripts.
func encodeHeader(s string) string {
	if isASCII(s) {
		return s
	}
	return mime.QEncoding.Encode("utf-8", s)
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// htmlToText degrades HTML to a plaintext fallback. Used when the
// caller's Message has no Text body. The conversion is *not* a
// full HTML→text fidelity job — it strips tags, decodes basic
// entities, collapses whitespace, and re-inserts line breaks for
// block-level elements so URLs in links still appear.
//
// Email clients render the HTML part anyway; this fallback exists
// for accessibility (screen readers, security-conscious receivers
// that block HTML) and spam-score (Gmail flags messages without a
// text part as suspicious).
func htmlToText(html string) string {
	if html == "" {
		return ""
	}
	// Insert newlines before/after common block elements so the
	// stripped output reads as paragraphs.
	replacer := strings.NewReplacer(
		"</p>", "\n\n",
		"<br>", "\n",
		"<br/>", "\n",
		"<br />", "\n",
		"</h1>", "\n\n",
		"</h2>", "\n\n",
		"</h3>", "\n\n",
		"</h4>", "\n\n",
		"</li>", "\n",
		"<li>", "  * ",
	)
	out := replacer.Replace(html)

	// Surface link URLs: `<a href="X">label</a>` → `label (X)`.
	// We do the simplest regex-free replacement that works for the
	// hand-written templates we ship.
	for {
		i := strings.Index(out, `<a `)
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], `>`)
		if j < 0 {
			break
		}
		hrefStart := strings.Index(out[i:i+j], `href="`)
		if hrefStart < 0 {
			break
		}
		hrefStart += i + len(`href="`)
		hrefEnd := strings.Index(out[hrefStart:], `"`)
		if hrefEnd < 0 {
			break
		}
		url := out[hrefStart : hrefStart+hrefEnd]
		closeIdx := strings.Index(out[i+j:], "</a>")
		if closeIdx < 0 {
			break
		}
		label := out[i+j+1 : i+j+closeIdx]
		replacement := label + " (" + url + ")"
		out = out[:i] + replacement + out[i+j+closeIdx+len("</a>"):]
	}

	// Strip remaining tags.
	out = stripTags(out)

	// Decode the small set of named/numeric entities we care about.
	out = entityDecoder.Replace(out)

	// Collapse runs of whitespace, but keep paragraph breaks.
	out = collapseWhitespace(out)
	return strings.TrimSpace(out)
}

var entityDecoder = strings.NewReplacer(
	"&nbsp;", " ",
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&#39;", "'",
	"&apos;", "'",
)

func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	var lastWasNL int // 0 normal, 1 \n, 2 \n\n+
	var lastWasSpace bool
	for _, r := range s {
		switch r {
		case '\n':
			if lastWasNL < 2 {
				b.WriteRune('\n')
				lastWasNL++
			}
			lastWasSpace = false
		case ' ', '\t', '\r':
			if !lastWasSpace && lastWasNL == 0 {
				b.WriteRune(' ')
			}
			lastWasSpace = true
		default:
			b.WriteRune(r)
			lastWasNL = 0
			lastWasSpace = false
		}
	}
	return b.String()
}

// silence unused-import warnings until reused elsewhere.
var _ = fmt.Sprintf
