// Package export ships document-generation primitives for v1.6.x.
//
// v1.6.0 covers XLSX export only (streaming, sync over HTTP). PDF /
// markdown→PDF / async / .Export() schema modifier follow in v1.6.x
// sub-slices.
//
// Design constraints:
//
//   - **Streaming**: never materialise the whole dataset in memory.
//     excelize ships a `StreamWriter` that buffers <1 MB of cells
//     before flushing to a temp file on disk; we expose that directly
//     so a 1M-row export uses constant memory.
//   - **Pure Go**: excelize is pure Go (~3 MB to the binary), keeping
//     the single-binary contract intact.
//   - **No schema knowledge**: this package operates on column
//     metadata + opaque row iteration. The REST handler (which DOES
//     know about builder.CollectionSpec) decides what to feed in.
package export

import (
	"fmt"
	"io"
	"time"

	"github.com/xuri/excelize/v2"
)

// Column is one output column of an XLSX export.
type Column struct {
	// Key is the row map key the writer uses to look up the cell.
	Key string
	// Header is the human-readable label written to row 1. Empty →
	// fall back to Key.
	Header string
	// Format, if non-empty, is an Excel number-format code applied to
	// every data cell in this column ("yyyy-mm-dd", "#,##0.00",
	// "$#,##0.00", "0.00%", ...). The writer creates one excelize
	// style per distinct Format string and tags each cell with the
	// style ID via excelize.Cell{StyleID:...} so the StreamWriter
	// emits the format reference into the workbook's styles.xml.
	// Empty → cells render verbatim (the v1.6.3 baseline). DSL-3.
	Format string
}

// XLSXWriter streams rows into a single-sheet XLSX file. Construct
// with NewXLSXWriter; call AppendRow() per row; call Close() to
// finalise and flush the workbook to the destination io.Writer.
//
// Lifecycle:
//
//	w, err := NewXLSXWriter("Posts", columns)
//	defer w.Discard() // safe if Close() succeeded; tears down temp file otherwise
//	for ... {
//	  if err := w.AppendRow(row); err != nil { ... }
//	}
//	if err := w.Finish(dst); err != nil { ... }
type XLSXWriter struct {
	file   *excelize.File
	stream *excelize.StreamWriter
	cols   []Column
	sheet  string
	row    int // 1-based; row 1 is the header, data starts at 2
	closed bool
	// styleIDs[i] is the excelize style ID for column i, or 0 if the
	// column has no Format set. Resolved once at NewXLSXWriter so
	// AppendRow doesn't pay NewStyle cost per cell. DSL-3.
	styleIDs []int
}

// NewXLSXWriter creates a new workbook with one sheet named `sheet`,
// writes the header row from `cols`, and returns the writer ready to
// accept data rows.
func NewXLSXWriter(sheet string, cols []Column) (*XLSXWriter, error) {
	if sheet == "" {
		sheet = "Sheet1"
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("export: at least one column required")
	}
	f := excelize.NewFile()
	// NewFile starts with a default sheet called "Sheet1". Rename it
	// to the caller's chosen name so the output looks intentional.
	if sheet != "Sheet1" {
		if err := f.SetSheetName("Sheet1", sheet); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("export: rename sheet: %w", err)
		}
	}
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("export: open stream writer: %w", err)
	}

	// Header row at cell A1.
	header := make([]any, len(cols))
	for i, c := range cols {
		label := c.Header
		if label == "" {
			label = c.Key
		}
		header[i] = label
	}
	if err := sw.SetRow("A1", header); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("export: write header: %w", err)
	}

	// DSL-3 — resolve one style ID per column with a non-empty
	// Format string. Cached strings (yyyy-mm-dd, #,##0.00 ...) reuse
	// the same style; we keep a small per-call map so identical
	// format codes share a single styles.xml entry.
	styleIDs := make([]int, len(cols))
	styleCache := make(map[string]int)
	for i, c := range cols {
		if c.Format == "" {
			continue
		}
		if id, ok := styleCache[c.Format]; ok {
			styleIDs[i] = id
			continue
		}
		fmtCode := c.Format
		id, err := f.NewStyle(&excelize.Style{CustomNumFmt: &fmtCode})
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("export: register format %q: %w", c.Format, err)
		}
		styleIDs[i] = id
		styleCache[c.Format] = id
	}

	return &XLSXWriter{
		file:     f,
		stream:   sw,
		cols:     cols,
		sheet:    sheet,
		row:      2,
		styleIDs: styleIDs,
	}, nil
}

// AppendRow writes one data row. Missing keys render as empty cells.
// Values pass through with no coercion: time.Time becomes an Excel
// date, ints/floats become numbers, everything else becomes a string
// via fmt.Sprint (excelize's default behaviour for non-numeric values).
//
// Returns an error if the underlying stream writer rejects the row —
// usually only on disk-full at the temp file.
func (w *XLSXWriter) AppendRow(row map[string]any) error {
	if w.closed {
		return fmt.Errorf("export: AppendRow after Close")
	}
	cells := make([]any, len(w.cols))
	for i, c := range w.cols {
		v := formatCell(row[c.Key])
		// DSL-3 — when the column declared a Format, wrap the cell
		// value in excelize.Cell{StyleID:...} so the StreamWriter
		// emits a styled cell. Cells without a format pass through
		// as raw values (the pre-DSL-3 path) — minimises overhead
		// for the no-format case.
		if w.styleIDs[i] != 0 {
			cells[i] = excelize.Cell{StyleID: w.styleIDs[i], Value: v}
		} else {
			cells[i] = v
		}
	}
	cell, err := excelize.CoordinatesToCellName(1, w.row)
	if err != nil {
		return fmt.Errorf("export: cell name for row %d: %w", w.row, err)
	}
	if err := w.stream.SetRow(cell, cells); err != nil {
		return fmt.Errorf("export: write row %d: %w", w.row, err)
	}
	w.row++
	return nil
}

// Finish finalises the workbook and copies it to dst. Implicitly
// closes the writer — calling AppendRow after Finish errors.
//
// `dst` is usually an http.ResponseWriter for sync export, or an
// *os.File for offline generation.
func (w *XLSXWriter) Finish(dst io.Writer) error {
	if w.closed {
		return fmt.Errorf("export: Finish after Close")
	}
	if err := w.stream.Flush(); err != nil {
		return fmt.Errorf("export: flush stream: %w", err)
	}
	if err := w.file.Write(dst); err != nil {
		return fmt.Errorf("export: write workbook: %w", err)
	}
	w.closed = true
	return w.file.Close()
}

// Discard releases the writer's temp file without producing output.
// Safe to call after Finish (no-op). Use in a defer to clean up on
// the error path without leaking the temp file excelize wrote to.
func (w *XLSXWriter) Discard() {
	if w.closed {
		return
	}
	_ = w.file.Close()
	w.closed = true
}

// RowsWritten reports the number of data rows (excluding the header).
// Handlers use it for audit logging.
func (w *XLSXWriter) RowsWritten() int {
	return w.row - 2 // row starts at 2; -2 because data begins after header
}

// formatCell normalises a value into something excelize handles
// predictably. time.Time → Excel date; primitives pass through; the
// kitchen sink (maps, slices, structs) collapses to its fmt.Sprint
// form so the cell renders something useful instead of a Go memory
// address.
func formatCell(v any) any {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case time.Time:
		// Excelize's StreamWriter writes time.Time as ISO-formatted
		// strings; that's exactly what we want for portability across
		// locales. Operators wanting Excel-native dates can format
		// post-export.
		return x.UTC().Format(time.RFC3339)
	case []byte:
		return string(x)
	case string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return x
	}
	return fmt.Sprint(v)
}
