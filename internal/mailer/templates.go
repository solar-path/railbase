// Package mailer template engine. Templates are Markdown with YAML-ish
// frontmatter at the top, looking like:
//
//	---
//	subject: Welcome to {{ site.name }}
//	from:    "Railbase <noreply@example.com>"
//	reply_to: support@example.com
//	---
//
//	# Hi {{ user.email }}
//
//	Please verify your address by clicking the link below.
//
//	[Verify]({{ verify_url }})
//
// Variable interpolation is a single-level {{ a.b.c }} lookup against
// the data map. No conditionals, no loops — operator-authored content,
// no need for a full template language. (Hooks scripting can build
// HTML directly via SendDirect when more logic is needed.)
//
// Markdown subset:
//   - `# h1`, `## h2`, `### h3` headings
//   - blank-line-separated paragraphs
//   - `**bold**`, `*italic*`, `` `code` `` inline
//   - `[label](url)` links
//   - bullet lists: lines starting with `- ` or `* `
//
// Anything else passes through HTML-escaped. CommonMark fidelity is
// not a goal — operator can hand-author HTML in the body if they
// need more.

package mailer

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// Templates resolves and renders templates. Constructed once at boot;
// safe for concurrent use.
type Templates struct {
	mu sync.RWMutex

	// dirs is the resolution chain (highest priority first). Typical:
	//   1. pb_data/email_templates  (operator overrides)
	//   2. builtin (embedded)       (defaults)
	dirs []TemplateSource
}

// TemplateSource is one layer of the resolution chain. Either a host
// filesystem directory (FS=nil) or an embedded FS rooted at Root.
type TemplateSource struct {
	Name string // human label for logs ("filesystem", "builtin")
	FS   fs.FS  // nil → read from disk via DiskDir
	Root string // subpath inside FS, or absolute path on disk
}

// TemplatesOptions tunes the constructor. Both fields are optional.
type TemplatesOptions struct {
	// DiskDir is the operator-controlled override directory (e.g.
	// `pb_data/email_templates`). Files here take precedence over
	// embedded defaults. Empty → embedded only.
	DiskDir string
}

// NewTemplates builds the resolver chain. Embedded `builtin/*.md` is
// always present as the lowest-priority fallback.
func NewTemplates(opts TemplatesOptions) *Templates {
	t := &Templates{}
	if opts.DiskDir != "" {
		t.dirs = append(t.dirs, TemplateSource{Name: "disk", Root: opts.DiskDir})
	}
	t.dirs = append(t.dirs, TemplateSource{Name: "builtin", FS: builtinFS, Root: "builtin"})
	return t
}

// Rendered is the output of Render: subject + bodies + headers
// derived from the template frontmatter.
type Rendered struct {
	Subject string
	From    Address
	ReplyTo Address
	HTML    string
	Text    string
}

// Render finds the named template, substitutes variables, and
// converts the body to HTML + plain text. The name must end in
// `.md`; missing extension is added automatically.
func (t *Templates) Render(name string, data map[string]any) (*Rendered, error) {
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	raw, err := t.read(name)
	if err != nil {
		return nil, err
	}

	front, body := splitFrontmatter(raw)
	meta := parseFrontmatter(front)
	body = interpolate(body, data)

	html := markdownToHTML(body)
	text := htmlToText(html)

	r := &Rendered{
		Subject: interpolate(meta["subject"], data),
		HTML:    html,
		Text:    text,
	}
	if v := interpolate(meta["from"], data); v != "" {
		r.From = parseAddress(v)
	}
	if v := interpolate(meta["reply_to"], data); v != "" {
		r.ReplyTo = parseAddress(v)
	}
	return r, nil
}

// BuiltinKinds returns the sorted list of kind names (without the
// `.md` extension) for every template embedded in the binary. This
// is the canonical "what could an operator override?" list — the
// admin UI's read-only Mailer templates browser reads it to render
// the left-hand kind list, so the embed.FS stays the single source
// of truth instead of a parallel hand-maintained slice.
func BuiltinKinds() []string {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		// The embed is compiled in — failing here means the build
		// itself is broken, but returning an empty list is a graceful
		// degradation that doesn't 500 the admin UI.
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}

// BuiltinSource returns the embedded Markdown source for kind (no
// `.md` extension), or ("", false) if no builtin matches. Used by
// the admin UI's viewer to show the default content that an operator
// would override by writing to `<DataDir>/email_templates/<kind>.md`.
//
// The returned string is the raw template text — frontmatter + body,
// before variable interpolation or HTML rendering. Callers wanting
// HTML can pipe it through RenderMarkdownForCLI.
func BuiltinSource(kind string) (string, bool) {
	if kind == "" || strings.ContainsAny(kind, "/\\") {
		return "", false
	}
	body, err := fs.ReadFile(builtinFS, "builtin/"+kind+".md")
	if err != nil {
		return "", false
	}
	return string(body), true
}

// List enumerates every template name discoverable through any
// source. Used by the admin UI's template manager (v1.1) and
// `railbase mailer list-templates` (v1.0 stretch goal — not wired
// into CLI yet).
func (t *Templates) List() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	seen := map[string]struct{}{}
	for _, src := range t.dirs {
		var entries []fs.DirEntry
		var err error
		if src.FS != nil {
			entries, err = fs.ReadDir(src.FS, src.Root)
		} else {
			entries, err = os.ReadDir(src.Root)
		}
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			seen[e.Name()] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (t *Templates) read(name string) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, src := range t.dirs {
		var body []byte
		var err error
		if src.FS != nil {
			body, err = fs.ReadFile(src.FS, filepath.Join(src.Root, name))
		} else {
			body, err = os.ReadFile(filepath.Join(src.Root, name))
		}
		if err == nil {
			return body, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("mailer: read %s in %s: %w", name, src.Name, err)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
}

// --- frontmatter ---

// splitFrontmatter divides raw into (frontmatter, body). When the
// file doesn't start with "---\n", frontmatter is empty and body is
// the full input.
func splitFrontmatter(raw []byte) (front string, body string) {
	if !bytes.HasPrefix(raw, []byte("---\n")) && !bytes.HasPrefix(raw, []byte("---\r\n")) {
		return "", string(raw)
	}
	// Locate the closing `---` on its own line.
	rest := raw[4:]
	if bytes.HasPrefix(raw, []byte("---\r\n")) {
		rest = raw[5:]
	}
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		end = bytes.Index(rest, []byte("\n---\r\n"))
	}
	if end < 0 {
		return "", string(raw)
	}
	front = string(rest[:end])
	// Skip past the closing fence + trailing newline.
	afterFence := end + len("\n---\n")
	if afterFence > len(rest) {
		afterFence = len(rest)
	}
	body = string(rest[afterFence:])
	return front, body
}

// parseFrontmatter handles a minimal YAML subset: `key: value` lines.
// Quoted values strip outer quotes. Unknown lines are ignored.
func parseFrontmatter(front string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		// Strip quotes.
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}

// parseAddress accepts either a bare `email@example.com` or
// `"Display Name" <email@example.com>` and returns the structured
// form. Names with no surrounding quotes are also accepted —
// `Display Name <email@example.com>`.
func parseAddress(s string) Address {
	s = strings.TrimSpace(s)
	lt := strings.IndexByte(s, '<')
	gt := strings.LastIndexByte(s, '>')
	if lt < 0 || gt < lt {
		return Address{Email: s}
	}
	name := strings.TrimSpace(s[:lt])
	name = strings.Trim(name, `"' `)
	return Address{
		Name:  name,
		Email: strings.TrimSpace(s[lt+1 : gt]),
	}
}

// --- variable interpolation ---

// interpolate replaces every `{{ key }}` / `{{ a.b.c }}` token with
// the value looked up in data. Missing keys → empty string (no error
// — better to ship a partial email than refuse to send).
func interpolate(s string, data map[string]any) string {
	if s == "" || !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], "{{")
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+j])
		k := strings.Index(s[i+j+2:], "}}")
		if k < 0 {
			b.WriteString(s[i+j:])
			break
		}
		path := strings.TrimSpace(s[i+j+2 : i+j+2+k])
		b.WriteString(stringify(lookupPath(data, path)))
		i = i + j + 2 + k + 2
	}
	return b.String()
}

func lookupPath(data map[string]any, path string) any {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, ".")
	var cur any = data
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int, int64, float64:
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
