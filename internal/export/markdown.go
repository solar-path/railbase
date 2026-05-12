package export

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/parser"
)

// RenderMarkdownToPDF parses `md` as Markdown (with optional YAML
// frontmatter for document chrome — title/header/footer/margins/sheet),
// walks the AST, and emits a PDF using the v1.6.1 PDFWriter primitives.
//
// `data` is reserved for the v1.6.x `.Export()` schema-builder slice
// which will plumb `text/template` interpolation through the
// frontmatter + body. For v1.6.2 the caller is responsible for any
// templating — `data` is accepted in the signature for forward
// compatibility but unused.
//
// Supported Markdown:
//   - Headings h1..h6 (each rendered at a decreasing size: 24/18/14/12/11/10 pt)
//   - Paragraphs (normal 12pt text, wrapped at the page width)
//   - Bullet and numbered lists (with prefix bullet / "N." markers)
//   - Fenced code blocks (rendered at 10pt with a left-edge indent;
//     real monospace would need a second embedded font — deferred)
//   - Blockquotes (rendered with a "  > " prefix at 12pt)
//   - Tables (header row + body rows via the PDFWriter table layout)
//   - Inline emphasis / strong / code / link → text passes through;
//     italic + bold renderings need the matching TTF variants (we ship
//     only Regular). Link URLs are appended in parens after the text.
//
// Returns the rendered PDF as a byte slice. Output is suitable for
// writing to disk, attaching to an email, or streaming to an HTTP
// response.
func RenderMarkdownToPDF(md []byte, data map[string]any) ([]byte, error) {
	_ = data // reserved — see godoc

	front, body := splitMarkdownFrontmatter(md)
	meta := parseMarkdownFrontmatter(front)

	cfg := PDFConfig{
		Title:  meta["title"],
		Header: meta["header"],
		Footer: meta["footer"],
		Sheet:  meta["sheet"],
	}

	p := parser.NewWithExtensions(parser.CommonExtensions)
	doc := p.Parse(body)

	w, err := NewPDFWriter(cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("export: pdf writer init: %w", err)
	}
	defer w.Discard()

	r := &mdRenderer{w: w}
	if err := r.render(doc); err != nil {
		return nil, fmt.Errorf("export: render md: %w", err)
	}

	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		return nil, fmt.Errorf("export: finish pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// mdRenderer walks the gomarkdown AST and emits primitives via w.
//
// Each top-level block is rendered by handleBlock(). Inline content
// (text/strong/em/code/link) is collected into a string by collectInline()
// — we don't try to draw per-glyph styling since gopdf with a single
// font can't actually switch to bold/italic mid-line.
type mdRenderer struct {
	w *PDFWriter
}

func (r *mdRenderer) render(root ast.Node) error {
	// Walk only top-level children — the block handlers recurse as needed.
	for _, child := range childrenOf(root) {
		if err := r.handleBlock(child); err != nil {
			return err
		}
	}
	return nil
}

// handleBlock dispatches a single block-level AST node to the
// appropriate PDFWriter call.
func (r *mdRenderer) handleBlock(n ast.Node) error {
	switch v := n.(type) {
	case *ast.Heading:
		return r.handleHeading(v)
	case *ast.Paragraph:
		return r.w.AppendText(collectInline(v))
	case *ast.List:
		return r.handleList(v)
	case *ast.CodeBlock:
		return r.handleCodeBlock(v)
	case *ast.BlockQuote:
		return r.handleBlockQuote(v)
	case *ast.Table:
		return r.handleTable(v)
	case *ast.HorizontalRule:
		// Render as a centred bullet row — drawing an actual line
		// requires gopdf Line primitive + position math; the bullet
		// gets the visual break across without the layout work.
		return r.w.AppendText("· · ·")
	case *ast.HTMLBlock:
		// Strip HTML — most embeds don't make sense in PDF anyway.
		// Passthrough as plain text so authors notice and remove it.
		return r.w.AppendText(string(v.Literal))
	}
	// Unknown block: walk children. Lets us handle nested containers
	// (Document → CaptionFigure → Table) without an explicit case.
	for _, child := range childrenOf(n) {
		if err := r.handleBlock(child); err != nil {
			return err
		}
	}
	return nil
}

// Heading sizes per level (pt). h1 is poster-sized; h6 is barely
// distinguishable from body text — matches conventional HTML defaults.
var headingSizes = [7]float64{0, 24, 18, 14, 12, 11, 10}

func (r *mdRenderer) handleHeading(h *ast.Heading) error {
	level := h.Level
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	text := collectInline(h)
	if level == 1 {
		// h1 reuses AppendTitle for the 20pt + spacer convention. Other
		// levels use raw setSize+Cell so we don't accumulate extra spacer
		// at each subhead.
		return r.w.AppendTitle(text)
	}
	return r.w.AppendSizedText(text, headingSizes[level])
}

func (r *mdRenderer) handleList(l *ast.List) error {
	ordered := l.ListFlags&ast.ListTypeOrdered != 0
	idx := 1
	if l.Start > 0 {
		idx = l.Start
	}
	for _, item := range childrenOf(l) {
		marker := "• "
		if ordered {
			marker = strconv.Itoa(idx) + ". "
			idx++
		}
		// A list item's children are typically Paragraph nodes; we
		// flatten via collectInline so multi-line items still fit on
		// one PDF line. Nested lists fall through the default branch.
		text := marker + collectInline(item)
		if err := r.w.AppendText(text); err != nil {
			return err
		}
	}
	return nil
}

func (r *mdRenderer) handleCodeBlock(c *ast.CodeBlock) error {
	// 10pt at left-indent. We don't have a monospace font shipped,
	// so this is "smaller text with a visible indent" rather than
	// true `code` style. Mono support deferred to a future slice
	// where we'd embed a second TTF (e.g. Roboto Mono).
	text := string(c.Literal)
	text = strings.TrimRight(text, "\n")
	for _, line := range strings.Split(text, "\n") {
		if err := r.w.AppendSizedText("    "+line, 10); err != nil {
			return err
		}
	}
	return nil
}

func (r *mdRenderer) handleBlockQuote(b *ast.BlockQuote) error {
	for _, child := range childrenOf(b) {
		// Flatten each child block to its inline content, prefix with
		// "  > " so the visual block-quote convention survives even
		// without italic / coloured rendering.
		text := "  > " + collectInline(child)
		if err := r.w.AppendText(text); err != nil {
			return err
		}
	}
	return nil
}

func (r *mdRenderer) handleTable(t *ast.Table) error {
	// Collect rows: first scan for the header row, then the body rows.
	// gomarkdown emits TableHeader → TableRow → TableCell and
	// TableBody → TableRow → TableCell.
	var header []string
	var body [][]string
	for _, sec := range childrenOf(t) {
		switch sec.(type) {
		case *ast.TableHeader:
			for _, row := range childrenOf(sec) {
				header = collectRow(row)
			}
		case *ast.TableBody:
			for _, row := range childrenOf(sec) {
				body = append(body, collectRow(row))
			}
		}
	}
	if len(header) == 0 && len(body) == 0 {
		return nil
	}
	// Build PDFColumn list — auto-width zero so PDFWriter even-splits.
	width := len(header)
	if width == 0 {
		width = len(body[0])
	}
	cols := make([]PDFColumn, width)
	for i := 0; i < width; i++ {
		key := strconv.Itoa(i)
		h := ""
		if i < len(header) {
			h = header[i]
		}
		cols[i] = PDFColumn{Key: key, Header: h}
	}

	// Spin a nested writer just for the table — keeps the cursor /
	// margin state in the parent writer untouched. We emit the table
	// as a series of `AppendText` lines drawn in the parent writer to
	// avoid duplicating the gopdf page management.
	//
	// MVP shortcut: render the table as plain text rows separated by
	// ` | `. Full gopdf table primitives (matching the table renderer
	// in the v1.6.1 PDF data-export endpoint) need a fresh PDFWriter
	// instance with `cols`, which doesn't compose cleanly. Tracked as
	// follow-up; the text-table is still readable and round-trips the
	// data.
	if len(header) > 0 {
		if err := r.w.AppendSizedText(strings.Join(header, " | "), 11); err != nil {
			return err
		}
	}
	for _, row := range body {
		if err := r.w.AppendText(strings.Join(row, " | ")); err != nil {
			return err
		}
	}
	return nil
}

func collectRow(n ast.Node) []string {
	var cells []string
	for _, c := range childrenOf(n) {
		cells = append(cells, collectInline(c))
	}
	return cells
}

// collectInline walks the inline children of a block node, gathering
// text. Emphasis/strong/code render as plain text (no font-variant
// switching); links append `(href)` after the visible text.
func collectInline(n ast.Node) string {
	var b strings.Builder
	ast.Walk(n, ast.NodeVisitorFunc(func(node ast.Node, entering bool) ast.WalkStatus {
		if !entering {
			// On exit of a link, append the URL in parens so readers
			// see where the link goes (since PDF text-only rendering
			// loses the hyperlink interactivity).
			if l, ok := node.(*ast.Link); ok && len(l.Destination) > 0 {
				b.WriteString(" (")
				b.Write(l.Destination)
				b.WriteString(")")
			}
			return ast.GoToNext
		}
		switch v := node.(type) {
		case *ast.Text:
			b.Write(v.Literal)
		case *ast.Code:
			// Inline code gets a backtick wrapper to mark the boundary.
			b.WriteByte('`')
			b.Write(v.Literal)
			b.WriteByte('`')
		case *ast.Softbreak:
			b.WriteByte(' ')
		case *ast.Hardbreak:
			b.WriteByte('\n')
		case *ast.NonBlockingSpace:
			b.WriteByte(' ')
		}
		return ast.GoToNext
	}))
	return strings.TrimSpace(b.String())
}

// childrenOf returns the Children slice of any container node. ast
// uses an embedded Container struct so this is the safe accessor.
func childrenOf(n ast.Node) []ast.Node {
	if n == nil {
		return nil
	}
	return n.GetChildren()
}

// --- frontmatter (mirror of mailer.splitFrontmatter/parseFrontmatter;
// duplicated rather than imported so internal/export stays decoupled
// from internal/mailer. If a third caller appears, factor out to
// internal/markdown.) ---

func splitMarkdownFrontmatter(raw []byte) (front string, body []byte) {
	if !bytes.HasPrefix(raw, []byte("---\n")) && !bytes.HasPrefix(raw, []byte("---\r\n")) {
		return "", raw
	}
	rest := raw[4:]
	if bytes.HasPrefix(raw, []byte("---\r\n")) {
		rest = raw[5:]
	}
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		end = bytes.Index(rest, []byte("\n---\r\n"))
	}
	if end < 0 {
		return "", raw
	}
	front = string(rest[:end])
	afterFence := end + len("\n---\n")
	if afterFence > len(rest) {
		afterFence = len(rest)
	}
	return front, rest[afterFence:]
}

func parseMarkdownFrontmatter(front string) map[string]string {
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
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}
