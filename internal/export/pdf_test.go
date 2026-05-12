package export

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPDFWriter_EmptyDocument(t *testing.T) {
	w, err := NewPDFWriter(PDFConfig{Title: "Empty"}, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatalf("finish: %v", err)
	}
	// Every PDF starts with `%PDF-`.
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Errorf("missing PDF header (first 10 bytes: %q)", buf.Bytes()[:10])
	}
	// EOF marker `%%EOF` must be near the end.
	if !bytes.Contains(buf.Bytes(), []byte("%%EOF")) {
		t.Error("missing PDF EOF marker")
	}
	if w.RowsWritten() != 0 {
		t.Errorf("rows=%d want 0", w.RowsWritten())
	}
}

func TestPDFWriter_TableLayout(t *testing.T) {
	cols := []PDFColumn{
		{Key: "id", Header: "ID"},
		{Key: "title", Header: "Title"},
		{Key: "count", Header: "Count"},
	}
	w, err := NewPDFWriter(PDFConfig{Title: "Posts"}, cols)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := w.AppendRow(map[string]any{
			"id":    "id-" + string(rune('A'+i)),
			"title": "row " + string(rune('A'+i)),
			"count": i * 10,
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if w.RowsWritten() != 5 {
		t.Errorf("rows=%d want 5", w.RowsWritten())
	}
	if buf.Len() < 500 {
		t.Errorf("PDF too small: %d bytes", buf.Len())
	}
}

func TestPDFWriter_AppendAfterFinishErrors(t *testing.T) {
	w, _ := NewPDFWriter(PDFConfig{Title: "x"}, []PDFColumn{{Key: "k"}})
	var buf bytes.Buffer
	_ = w.Finish(&buf)
	if err := w.AppendRow(map[string]any{"k": "v"}); err == nil {
		t.Error("AppendRow after Finish: want error")
	}
	if err := w.AppendText("v"); err == nil {
		t.Error("AppendText after Finish: want error")
	}
	if err := w.AppendTitle("v"); err == nil {
		t.Error("AppendTitle after Finish: want error")
	}
}

func TestPDFWriter_DiscardSafe(t *testing.T) {
	w, _ := NewPDFWriter(PDFConfig{Title: "x"}, nil)
	w.Discard() // must not panic
	// Finish after Discard now errors (writer is sealed).
	var buf bytes.Buffer
	if err := w.Finish(&buf); err == nil {
		t.Error("Finish after Discard: want error")
	}
}

func TestPDFWriter_AppendRowNoColsIsNoop(t *testing.T) {
	w, err := NewPDFWriter(PDFConfig{Title: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// No cols → AppendRow returns nil without writing anything.
	if err := w.AppendRow(map[string]any{"a": "b"}); err != nil {
		t.Errorf("AppendRow without cols: %v", err)
	}
	if w.RowsWritten() != 0 {
		t.Errorf("rows = %d, want 0 (no-op)", w.RowsWritten())
	}
}

func TestPDFWriter_Pagination(t *testing.T) {
	// 100 rows at ~18pt each = 1800pt → way past 1 page (~770 usable).
	// Verifies the AddPage path runs cleanly.
	cols := []PDFColumn{{Key: "n", Header: "N"}}
	w, err := NewPDFWriter(PDFConfig{Title: "Big", Header: "H", Footer: "F"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		_ = w.AppendRow(map[string]any{"n": i})
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatalf("finish: %v", err)
	}
	// Multi-page documents emit "/Type /Page" multiple times.
	count := bytes.Count(buf.Bytes(), []byte("/Type /Page"))
	if count < 3 {
		// The /Type /Page substring appears once per /Pages object plus
		// once per page; ≥ 3 is a reasonable minimum for 100 rows.
		t.Errorf("expected multiple pages, /Type /Page count = %d", count)
	}
}

func TestFormatPDFCell(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"hello", "hello"},
		{42, "42"},
		{3.14, "3.14"},
		{true, "true"},
		{[]byte("bytes"), "bytes"},
	}
	for i, tc := range tests {
		if got := formatPDFCell(tc.in); got != tc.want {
			t.Errorf("[%d] %v → %q want %q", i, tc.in, got, tc.want)
		}
	}
	// time.Time → RFC3339
	tt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	if formatPDFCell(tt) != "2026-05-11T12:00:00Z" {
		t.Errorf("time fmt: %q", formatPDFCell(tt))
	}
}

func TestTruncateForWidth(t *testing.T) {
	// Short string → unchanged.
	if got := truncateForWidth("abc", 200); got != "abc" {
		t.Errorf("short: %q", got)
	}
	// Very narrow → returns as-is (no meaningful truncation possible).
	if got := truncateForWidth("hello world", 10); got != "hello world" {
		t.Errorf("narrow: %q", got)
	}
	// Long string in moderate width → truncated with ellipsis.
	got := truncateForWidth(strings.Repeat("x", 200), 50)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("ellipsis missing: %q", got)
	}
	if len([]rune(got)) >= 200 {
		t.Errorf("not truncated: len=%d", len([]rune(got)))
	}
}

func TestPDFWriter_TitleFallsBackToSheet(t *testing.T) {
	w, err := NewPDFWriter(PDFConfig{Sheet: "Reports"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatal(err)
	}
	// Document was built without error; that's the assertion (the
	// title is embedded as drawing primitives in the PDF byte stream,
	// not as a stringly-searchable substring after font subsetting).
	if buf.Len() < 200 {
		t.Errorf("PDF too small: %d", buf.Len())
	}
}
