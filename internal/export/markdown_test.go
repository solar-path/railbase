package export

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderMarkdownToPDF_BasicDocument(t *testing.T) {
	md := []byte("# Hello\n\nThis is a paragraph.\n")
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Errorf("not a PDF: first bytes %q", out[:20])
	}
	if !bytes.Contains(out, []byte("%%EOF")) {
		t.Error("missing PDF EOF marker")
	}
	if len(out) < 500 {
		t.Errorf("PDF too small: %d bytes", len(out))
	}
}

func TestRenderMarkdownToPDF_AllHeadingLevels(t *testing.T) {
	md := []byte(strings.Join([]string{
		"# H1",
		"## H2",
		"### H3",
		"#### H4",
		"##### H5",
		"###### H6",
		"",
		"body",
	}, "\n"))
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
	// Sanity: multi-heading doc is a bit bigger than the trivial case.
	if len(out) < 1000 {
		t.Errorf("multi-heading PDF suspiciously small: %d bytes", len(out))
	}
}

func TestRenderMarkdownToPDF_Lists(t *testing.T) {
	md := []byte(strings.Join([]string{
		"- alpha",
		"- bravo",
		"- charlie",
		"",
		"1. one",
		"2. two",
		"3. three",
	}, "\n"))
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestRenderMarkdownToPDF_Tables(t *testing.T) {
	md := []byte(strings.Join([]string{
		"| Item  | Qty | Total |",
		"|-------|-----|-------|",
		"| Widget | 3  | 30    |",
		"| Gadget | 1  | 100   |",
	}, "\n"))
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestRenderMarkdownToPDF_FencedCodeBlock(t *testing.T) {
	md := []byte("```go\nfunc main() {\n  fmt.Println(\"hi\")\n}\n```")
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestRenderMarkdownToPDF_Blockquote(t *testing.T) {
	md := []byte("> quoted line one\n>\n> quoted line two")
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestRenderMarkdownToPDF_InlineMarkup(t *testing.T) {
	md := []byte("Some **bold** and _italic_ and `code` and [link](https://example.com) text.")
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestRenderMarkdownToPDF_Frontmatter(t *testing.T) {
	md := []byte(strings.Join([]string{
		"---",
		"title: Quarterly Report",
		"header: Acme Corp",
		"footer: Confidential",
		"---",
		"",
		"# Body heading",
		"",
		"Real content here.",
	}, "\n"))
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestRenderMarkdownToPDF_EmptyInput(t *testing.T) {
	out, err := RenderMarkdownToPDF(nil, nil)
	if err != nil {
		t.Fatalf("render nil: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("empty MD: not a PDF")
	}
}

func TestSplitMarkdownFrontmatter_NoFrontmatter(t *testing.T) {
	front, body := splitMarkdownFrontmatter([]byte("# Just a heading\n\ntext"))
	if front != "" {
		t.Errorf("front = %q, want empty", front)
	}
	if string(body) != "# Just a heading\n\ntext" {
		t.Errorf("body = %q", body)
	}
}

func TestSplitMarkdownFrontmatter_StandardYAMLDelimiters(t *testing.T) {
	in := []byte("---\ntitle: hi\nheader: top\n---\nbody here")
	front, body := splitMarkdownFrontmatter(in)
	if !strings.Contains(front, "title: hi") {
		t.Errorf("front = %q", front)
	}
	if string(body) != "body here" {
		t.Errorf("body = %q", body)
	}
}

func TestParseMarkdownFrontmatter_KeyValueLines(t *testing.T) {
	front := "title: Hello\nheader: \"Quoted\"\nfooter: 'singled'\n# comment\nempty:\n"
	m := parseMarkdownFrontmatter(front)
	if m["title"] != "Hello" {
		t.Errorf("title = %q", m["title"])
	}
	if m["header"] != "Quoted" {
		t.Errorf("header = %q (quote strip failed)", m["header"])
	}
	if m["footer"] != "singled" {
		t.Errorf("footer = %q (single-quote strip failed)", m["footer"])
	}
	if _, ok := m["#"]; ok {
		t.Error("comment line leaked into map")
	}
}

func TestCollectInline_Mixed(t *testing.T) {
	// Cheap end-to-end: ensure inline collector survives the gomarkdown
	// AST for a mix of emphasis, code, and links without panicking.
	md := []byte("**bold** _em_ `code` [text](https://example.com)")
	out, err := RenderMarkdownToPDF(md, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestRenderMarkdownToPDF_LargeDocumentPaginates(t *testing.T) {
	// Build a doc longer than a single page so the AppendSizedText
	// auto-page-break path runs. 100 paragraphs is comfortably past
	// the ~50-line capacity of A4 portrait at 12pt.
	var b strings.Builder
	b.WriteString("---\ntitle: Big Report\nheader: ACME\nfooter: Page\n---\n\n# Big Report\n\n")
	for i := 0; i < 100; i++ {
		b.WriteString("This is paragraph number ")
		b.WriteString(strings.Repeat("X", 5))
		b.WriteString(".\n\n")
	}
	out, err := RenderMarkdownToPDF([]byte(b.String()), nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
	// Multi-page documents have multiple /Type /Page occurrences.
	if got := bytes.Count(out, []byte("/Type /Page")); got < 3 {
		t.Errorf("expected pagination (/Type /Page x ≥3), got %d", got)
	}
}
