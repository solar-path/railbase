// Package export re-exports Railbase's internal PDF + XLSX writers so
// embedders can build their own typed-document handlers without
// re-implementing (or vendoring) the same gopdf / excelize boilerplate
// the shopper project hit. Cross-references:
//
//   - FEEDBACK #30 — `internal/export` was reachable from CLI/REST
//     but not from userland. Each embedder ended up writing their
//     own gopdf wrapper for per-entity documents (invoice PDFs, etc.).
//   - FEEDBACK #31 — DefaultFont() exposes the embedded Roboto Regular
//     so embedders don't have to copy the 170 KB TTF into their own
//     binary.
//   - FEEDBACK #32 — re-exporting the writer surface helps avoid the
//     gopdf.WritePdf-vs-Write footgun (Finish writes to an
//     io.Writer; there's no path-named variant).
//
// All re-exports are Go type aliases — handler code written against
// these names is identical to using the internal package directly,
// and `*export.PDFWriter` passes through unchanged to anything else
// in railbase that expects an `internal/export.PDFWriter`.
package export

import (
	"github.com/railbase/railbase/internal/export"
)

// ── PDF ──────────────────────────────────────────────────────────

// PDFConfig configures a programmatic PDF document. Mirrors
// internal/export.PDFConfig.
type PDFConfig = export.PDFConfig

// PDFColumn declares one column for a tabular PDF layout. Width=0
// auto-sizes across the page.
type PDFColumn = export.PDFColumn

// PDFWriter is the streaming PDF builder. Construction sets up the
// page + font; AppendTitle / AppendText / AppendSizedText / AppendRow
// write content; Finish(w) flushes to the destination io.Writer.
//
// Memory note: the document is buffered in memory until Finish — gopdf
// doesn't expose per-page streaming yet. For O(100k) row tables, cap
// at the same limit Railbase's REST handlers use (defaultExportMaxRows).
type PDFWriter = export.PDFWriter

// NewPDFWriter creates an empty A4 PDF document with the embedded
// Roboto font registered. The caller adds content via AppendTitle /
// AppendText / AppendRow / Finish.
//
// `cfg` may be the zero value — defaults: no title, no header/footer,
// 12pt body. `cols`, when non-nil, primes the writer for a table
// layout: the first AppendRow draws a header row.
func NewPDFWriter(cfg PDFConfig, cols []PDFColumn) (*PDFWriter, error) {
	return export.NewPDFWriter(cfg, cols)
}

// DefaultFont returns a copy of the embedded Roboto Regular TTF used
// by NewPDFWriter. Embedders building their own gopdf documents
// alongside Railbase don't need to re-embed the file — FEEDBACK #31.
//
// Usage:
//
//	pdf := &gopdf.GoPdf{}
//	pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: 595, H: 842}})
//	_ = pdf.AddTTFFontData(export.DefaultFontName, export.DefaultFont())
//	_ = pdf.SetFont(export.DefaultFontName, "", 12)
func DefaultFont() []byte { return export.DefaultFont() }

// DefaultFontName is the family name NewPDFWriter registers Roboto
// under (currently "Roboto"). Use this with `pdf.SetFont(...)` so
// the lookup matches DefaultFont().
const DefaultFontName = export.DefaultFontName

// RenderMarkdownToPDF takes a Markdown document plus a data dict and
// returns the rendered PDF bytes. `data` is passed to text/template
// before Markdown parsing, so the source can reference fields like
// `{{.Records}}` or `{{.Tenant}}`. Useful for ad-hoc one-shot PDFs
// from custom HTTP handlers (no on-disk template file needed).
func RenderMarkdownToPDF(md []byte, data map[string]any) ([]byte, error) {
	return export.RenderMarkdownToPDF(md, data)
}

// ── XLSX ─────────────────────────────────────────────────────────

// Column declares one column for an XLSX sheet.
type Column = export.Column

// XLSXWriter is the streaming XLSX builder. Same lifecycle as
// PDFWriter: New → AppendRow* → Finish(w).
type XLSXWriter = export.XLSXWriter

// NewXLSXWriter creates an empty workbook with one sheet (`sheet`)
// and the given column layout. AppendRow writes one data row.
func NewXLSXWriter(sheet string, cols []Column) (*XLSXWriter, error) {
	return export.NewXLSXWriter(sheet, cols)
}
