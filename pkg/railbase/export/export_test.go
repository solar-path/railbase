// Regression tests for the public export re-export. Closes
// FEEDBACK #30 (`internal/export` not reachable from userland) and
// FEEDBACK #31 (DefaultFont() lets embedders drop their 170 KB
// Roboto copy). Each test exercises the public surface; if the
// re-export drifts away from internal/export (e.g. someone renames
// a function), the build fails here first.
package export_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/railbase/railbase/pkg/railbase/export"
)

// TestNewPDFWriter_RoundTrip — the headline #30 path. Embedder
// constructs a PDFWriter purely through pkg/railbase/export, writes
// content, gets valid PDF bytes back.
func TestNewPDFWriter_RoundTrip(t *testing.T) {
	w, err := export.NewPDFWriter(export.PDFConfig{Title: "Invoice #42"}, nil)
	if err != nil {
		t.Fatalf("NewPDFWriter: %v", err)
	}
	if err := w.AppendText("Total: $99.00"); err != nil {
		t.Fatalf("AppendText: %v", err)
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("Finish produced 0 bytes")
	}
	// %PDF-1.x magic must be at the start of every valid PDF.
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Errorf("output missing %%PDF- magic; first 20 bytes: %q", buf.Bytes()[:20])
	}
}

// TestNewPDFWriter_TableRoundTrip — column layout + AppendRow rendering.
// Mirrors the shopper-class "I need an invoice with line items" use case.
func TestNewPDFWriter_TableRoundTrip(t *testing.T) {
	cols := []export.PDFColumn{
		{Key: "sku", Header: "SKU", Width: 100},
		{Key: "qty", Header: "Qty", Width: 50},
		{Key: "amount", Header: "Amount", Width: 100},
	}
	w, err := export.NewPDFWriter(export.PDFConfig{Title: "Order"}, cols)
	if err != nil {
		t.Fatalf("NewPDFWriter: %v", err)
	}
	for _, row := range []map[string]any{
		{"sku": "A1", "qty": 2, "amount": "$10.00"},
		{"sku": "B2", "qty": 5, "amount": "$25.50"},
	} {
		if err := w.AppendRow(row); err != nil {
			t.Fatalf("AppendRow: %v", err)
		}
	}
	if got := w.RowsWritten(); got != 2 {
		t.Errorf("RowsWritten: got %d, want 2", got)
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if buf.Len() < 1000 {
		t.Errorf("PDF suspiciously small: %d bytes", buf.Len())
	}
}

// TestDefaultFont_ReturnsCopy — the helper must return a defensive
// copy, not a slice aliased to the package's embedded buffer.
// Otherwise a caller that mutates the slice would corrupt every
// subsequent NewPDFWriter call.
func TestDefaultFont_ReturnsCopy(t *testing.T) {
	a := export.DefaultFont()
	if len(a) < 100_000 {
		t.Fatalf("Roboto font suspiciously small: %d bytes", len(a))
	}
	// TTF magic — file must start with 0x00 0x01 0x00 0x00 (TTF) or
	// 0x4F 0x54 0x54 0x4F ('OTTO'). Roboto Regular is TTF.
	want := []byte{0x00, 0x01, 0x00, 0x00}
	if !bytes.Equal(a[:4], want) {
		t.Errorf("font header mismatch: got %x, want %x", a[:4], want)
	}

	// Mutate the returned slice. The next call must return an
	// unaffected buffer — i.e. the helper made a copy.
	orig := a[0]
	a[0] = 0xff
	b := export.DefaultFont()
	if b[0] != orig {
		t.Errorf("DefaultFont returned a view, not a copy: first byte mutated through")
	}
}

// TestDefaultFontName_Stable — embedders pin to this constant; a
// silent rename would invalidate downstream font lookups.
func TestDefaultFontName_Stable(t *testing.T) {
	if export.DefaultFontName == "" {
		t.Errorf("DefaultFontName must not be empty")
	}
	// "Roboto" is the family the embedded TTF actually registers as.
	if !strings.EqualFold(export.DefaultFontName, "Roboto") {
		t.Errorf("DefaultFontName changed: got %q, expected \"Roboto\". "+
			"If this is intentional, document the migration step for embedders.", export.DefaultFontName)
	}
}

// TestNewXLSXWriter_RoundTrip — the XLSX side of the re-export.
func TestNewXLSXWriter_RoundTrip(t *testing.T) {
	cols := []export.Column{
		{Key: "id", Header: "ID"},
		{Key: "name", Header: "Name"},
	}
	w, err := export.NewXLSXWriter("Sheet1", cols)
	if err != nil {
		t.Fatalf("NewXLSXWriter: %v", err)
	}
	if err := w.AppendRow(map[string]any{"id": 1, "name": "Alice"}); err != nil {
		t.Fatalf("AppendRow: %v", err)
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("XLSX Finish produced 0 bytes")
	}
	// XLSX is a ZIP archive — must start with PK\x03\x04.
	if !bytes.HasPrefix(buf.Bytes(), []byte{0x50, 0x4B, 0x03, 0x04}) {
		t.Errorf("XLSX missing ZIP magic; first 4 bytes: %x", buf.Bytes()[:4])
	}
}

// TestRenderMarkdownToPDF_RoundTrip — the Markdown convenience path.
// Embedders use this for one-shot custom PDFs without an on-disk
// template file.
func TestRenderMarkdownToPDF_RoundTrip(t *testing.T) {
	md := []byte("# Hello {{.name}}\n\nThis is a paragraph.")
	out, err := export.RenderMarkdownToPDF(md, map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("RenderMarkdownToPDF: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Errorf("output missing PDF magic; first 20 bytes: %q", out[:20])
	}
}
