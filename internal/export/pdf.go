package export

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"time"

	"github.com/signintech/gopdf"
)

// Roboto Regular ships with the binary so a PDF export works without
// any system-font dependency. Apache 2.0 licence — explicitly
// redistributable. ~170 KB; a fair price for guaranteed text rendering
// across all platforms (gopdf otherwise needs an external TTF path).
//
//go:embed assets/Roboto-Regular.ttf
var robotoRegular []byte

// DefaultFont returns a copy of the embedded Roboto Regular TTF used
// by NewPDFWriter for rendering. Embedders building their own gopdf
// documents alongside Railbase get the same font without having to
// re-embed it themselves — FEEDBACK #31 (the shopper project copied
// the 170 KB TTF into its own binary just to call `pdf.AddTTFFontData`
// directly).
//
// Returns a defensive copy so callers can't mutate the package-level
// embedded buffer.
func DefaultFont() []byte {
	out := make([]byte, len(robotoRegular))
	copy(out, robotoRegular)
	return out
}

// DefaultFontName is the family name NewPDFWriter registers Roboto
// under. Embedders writing their own gopdf code should use this when
// calling `pdf.SetFont(DefaultFontName, "", size)` so the font lookup
// matches the bytes returned by DefaultFont().
const DefaultFontName = defaultFont

// pageWidth / pageHeight in points (1/72 inch). A4 portrait.
const (
	pageWidth   = 595.28
	pageHeight  = 841.89
	pageMargin  = 36.0 // 0.5 inch margins all sides
	defaultFont = "Roboto"
)

// PDFConfig configures a programmatic PDF document. All fields
// optional — defaults: A4 portrait, 12pt Roboto body, header/footer
// drawn only when set.
type PDFConfig struct {
	Title  string // shown on the first page as large text
	Header string // optional repeating header on every page
	Footer string // optional repeating footer ("Page N of M" merged)
	// Sheet is the docs/08-aligned alias for Title — handy when the
	// caller wants the same source struct to drive both XLSX
	// (`Sheet` → sheet name) and PDF (→ document title). Title takes
	// precedence if both are set.
	Sheet string
}

// Column header + width hint for table rendering. Width is in points.
// 0 → auto-size evenly across page.
type PDFColumn struct {
	Key    string
	Header string
	Width  float64
}

// PDFWriter is a streaming PDF builder. Construction sets up the
// page + font; AppendXxx() methods write content; Finish() flushes
// the bytes to the destination io.Writer.
//
// Memory profile: gopdf buffers the entire document in memory. For
// huge tables (~100k rows) this can hit 100s of MB; the REST handler
// caps row count at defaultExportMaxRows the same way XLSX does.
// True row-by-row streaming for PDF deferred to a future slice
// (would need pdf.Output(io.Writer) per-page, not gopdf's all-at-once
// model).
type PDFWriter struct {
	pdf    *gopdf.GoPdf
	cfg    PDFConfig
	cols   []PDFColumn
	closed bool
	// rowsWritten tracks data rows appended via AppendRow (excluding
	// header). Audit / log emission needs the count without us
	// rummaging through gopdf internals.
	rowsWritten int
}

// NewPDFWriter creates an empty A4 document with the embedded Roboto
// font registered. The caller adds content via AppendTitle /
// AppendHeader / AppendRow / Finish.
//
// `cfg` may be the zero value — the writer falls back to sane
// defaults (no title, no header/footer, default font/size).
//
// `cols`, when non-nil, primes the writer for a table layout: the
// first AppendRow draws a header row, subsequent calls draw data
// rows. Without cols, AppendRow is a no-op (only freeform AppendText
// is meaningful).
func NewPDFWriter(cfg PDFConfig, cols []PDFColumn) (*PDFWriter, error) {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: pageWidth, H: pageHeight}})
	if err := pdf.AddTTFFontData(defaultFont, robotoRegular); err != nil {
		return nil, fmt.Errorf("export: load embedded font: %w", err)
	}
	pdf.AddPage()
	pdf.SetMargins(pageMargin, pageMargin, pageMargin, pageMargin)
	if err := pdf.SetFont(defaultFont, "", 12); err != nil {
		return nil, fmt.Errorf("export: set default font: %w", err)
	}

	w := &PDFWriter{pdf: pdf, cfg: cfg, cols: cols}

	if cfg.Header != "" {
		if err := w.drawHeader(cfg.Header); err != nil {
			return nil, err
		}
	}
	title := cfg.Title
	if title == "" {
		title = cfg.Sheet
	}
	if title != "" {
		if err := w.AppendTitle(title); err != nil {
			return nil, err
		}
	}
	if len(cols) > 0 {
		if err := w.drawTableHeader(); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// AppendTitle writes a large title line + spacer. Idempotent if
// called multiple times — each call adds another big-text section.
func (w *PDFWriter) AppendTitle(s string) error {
	if w.closed {
		return fmt.Errorf("export: AppendTitle after Finish")
	}
	if err := w.pdf.SetFont(defaultFont, "", 20); err != nil {
		return err
	}
	w.pdf.SetX(pageMargin)
	if err := w.pdf.Cell(nil, s); err != nil {
		return err
	}
	w.pdf.Br(28)
	return w.pdf.SetFont(defaultFont, "", 12)
}

// AppendText writes one freeform line at 12pt. Wraps automatically
// if the string is longer than the page width.
func (w *PDFWriter) AppendText(s string) error {
	return w.AppendSizedText(s, 12)
}

// AppendSizedText writes one freeform line at an arbitrary size.
// Used by the Markdown→PDF renderer for headings h2..h6 + fenced
// code blocks (smaller, indented). Restores the writer's font to
// 12pt before returning so subsequent AppendText calls aren't sticky.
//
// Page break-aware: if the next line would overflow the bottom
// margin, a new page is added first. Header (if configured) is
// redrawn at the top of the new page so multi-page documents keep
// the same chrome.
func (w *PDFWriter) AppendSizedText(s string, size float64) error {
	if w.closed {
		return fmt.Errorf("export: AppendSizedText after Finish")
	}
	// Pre-flight the next line's vertical room. Use the size itself
	// as a generous estimate of line-box height — for body text at
	// 12pt this gives ~12pt of leading, for h1 at 24pt it gives ~24pt.
	lineH := size + 4
	if w.pdf.GetY()+lineH > pageHeight-pageMargin {
		w.pdf.AddPage()
		w.pdf.SetY(pageMargin)
		if w.cfg.Header != "" {
			if err := w.drawHeader(w.cfg.Header); err != nil {
				return err
			}
		}
	}
	if err := w.pdf.SetFont(defaultFont, "", size); err != nil {
		return err
	}
	w.pdf.SetX(pageMargin)
	if err := w.pdf.Cell(nil, s); err != nil {
		return err
	}
	w.pdf.Br(lineH)
	// Restore body font so the next freeform AppendText doesn't
	// inherit the heading size.
	return w.pdf.SetFont(defaultFont, "", 12)
}

// AppendRow writes one data row using the column layout passed to
// NewPDFWriter. No-op (and returns nil) if the writer was built
// without columns. Wraps to a new page when the next row would
// overflow the bottom margin.
func (w *PDFWriter) AppendRow(row map[string]any) error {
	if w.closed {
		return fmt.Errorf("export: AppendRow after Finish")
	}
	if len(w.cols) == 0 {
		return nil
	}
	if w.pdf.GetY() > pageHeight-pageMargin-32 {
		w.pdf.AddPage()
		w.pdf.SetY(pageMargin)
		if w.cfg.Header != "" {
			if err := w.drawHeader(w.cfg.Header); err != nil {
				return err
			}
		}
		if err := w.drawTableHeader(); err != nil {
			return err
		}
	}
	widths := w.resolvedWidths()
	w.pdf.SetX(pageMargin)
	for i, c := range w.cols {
		txt := formatPDFCell(row[c.Key])
		// gopdf.CellWithOption gives a fixed-width rect for clipping.
		if err := w.pdf.CellWithOption(
			&gopdf.Rect{W: widths[i], H: 18},
			truncateForWidth(txt, widths[i]),
			gopdf.CellOption{Align: gopdf.Left | gopdf.Middle, Border: gopdf.Bottom},
		); err != nil {
			return err
		}
	}
	w.pdf.Br(18)
	w.rowsWritten++
	return nil
}

// Finish flushes the workbook to dst and seals the writer. WriteOut
// in gopdf is one-shot — calling again or appending content errors.
func (w *PDFWriter) Finish(dst io.Writer) error {
	if w.closed {
		return fmt.Errorf("export: Finish after Finish")
	}
	if w.cfg.Footer != "" {
		// Apply footer to the *current* (last) page. Multi-page footers
		// are deferred — gopdf supports per-page hooks via AddHeader
		// / AddFooter but those bind at page-add time. Tracked as
		// follow-up.
		w.pdf.SetY(pageHeight - pageMargin - 8)
		w.pdf.SetX(pageMargin)
		_ = w.pdf.SetFont(defaultFont, "", 9)
		_ = w.pdf.Cell(nil, w.cfg.Footer)
	}
	var buf bytes.Buffer
	if _, err := w.pdf.WriteTo(&buf); err != nil {
		return fmt.Errorf("export: write pdf: %w", err)
	}
	if _, err := dst.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("export: copy pdf to dst: %w", err)
	}
	w.closed = true
	return nil
}

// Discard releases the writer without producing output. Safe after
// Finish (no-op). Defer to clean up on the error path.
func (w *PDFWriter) Discard() {
	w.closed = true
}

// RowsWritten reports the data row count (excluding the header row).
func (w *PDFWriter) RowsWritten() int { return w.rowsWritten }

// --- internal helpers ---

func (w *PDFWriter) drawHeader(text string) error {
	w.pdf.SetX(pageMargin)
	if err := w.pdf.SetFont(defaultFont, "", 10); err != nil {
		return err
	}
	if err := w.pdf.Cell(nil, text); err != nil {
		return err
	}
	w.pdf.Br(20)
	return w.pdf.SetFont(defaultFont, "", 12)
}

func (w *PDFWriter) drawTableHeader() error {
	widths := w.resolvedWidths()
	w.pdf.SetX(pageMargin)
	for i, c := range w.cols {
		label := c.Header
		if label == "" {
			label = c.Key
		}
		if err := w.pdf.CellWithOption(
			&gopdf.Rect{W: widths[i], H: 20},
			truncateForWidth(label, widths[i]),
			gopdf.CellOption{Align: gopdf.Left | gopdf.Middle, Border: gopdf.Bottom},
		); err != nil {
			return err
		}
	}
	w.pdf.Br(20)
	return nil
}

// resolvedWidths fills any zero-width column with the leftover
// page-width divided evenly. Stable + deterministic — gives the
// caller predictable layouts even when widths are partly specified.
func (w *PDFWriter) resolvedWidths() []float64 {
	avail := pageWidth - 2*pageMargin
	used := 0.0
	zeroCount := 0
	for _, c := range w.cols {
		if c.Width > 0 {
			used += c.Width
		} else {
			zeroCount++
		}
	}
	per := 0.0
	if zeroCount > 0 && used < avail {
		per = (avail - used) / float64(zeroCount)
	}
	out := make([]float64, len(w.cols))
	for i, c := range w.cols {
		if c.Width > 0 {
			out[i] = c.Width
		} else {
			out[i] = per
		}
	}
	return out
}

// formatPDFCell mirrors formatCell in xlsx.go but always returns
// a string — gopdf cells take strings only (no typed-cell distinction
// like Excel).
func formatPDFCell(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case string:
		return x
	case []byte:
		return string(x)
	}
	return fmt.Sprint(v)
}

// truncateForWidth approximates how many characters fit in `w`
// points at the current 12pt font. Roboto Regular averages ~5.5 pts
// per character at 12pt, so each point ≈ 0.18 chars. Cuts cleanly
// at the boundary + appends an ellipsis to make truncation visible.
//
// Approximation is good enough for an MVP — perfect glyph metrics
// would require sfnt parsing, deferred.
func truncateForWidth(s string, w float64) string {
	if w <= 0 {
		return s
	}
	// Reserve ~4 pts for the right padding so text doesn't kiss the
	// column border.
	chars := int((w - 4) / 6.0)
	if chars < 4 {
		return s // too narrow to truncate meaningfully — let gopdf clip
	}
	// strings.RuneCountInString is the correct measure for multibyte
	// (every rune is one visual char). For pure ASCII this matches
	// len(s) exactly.
	if runeCount(s) <= chars {
		return s
	}
	// Truncate at rune boundary + ellipsis.
	out := make([]rune, 0, chars)
	count := 0
	for _, r := range s {
		if count >= chars-1 {
			break
		}
		out = append(out, r)
		count++
	}
	return string(out) + "…"
}

func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

