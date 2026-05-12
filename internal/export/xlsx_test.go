package export

import (
	"bytes"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func TestXLSXWriter_HeaderAndRows(t *testing.T) {
	cols := []Column{
		{Key: "id", Header: "ID"},
		{Key: "title", Header: "Title"},
		{Key: "count", Header: "Count"},
	}
	w, err := NewXLSXWriter("Posts", cols)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := w.AppendRow(map[string]any{"id": "abc", "title": "Hello", "count": 42}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := w.AppendRow(map[string]any{"id": "def", "title": "World", "count": 7}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	if buf.Len() < 100 {
		t.Fatalf("xlsx too small (%d bytes)", buf.Len())
	}
	if w.RowsWritten() != 2 {
		t.Errorf("rows = %d want 2", w.RowsWritten())
	}

	f, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatalf("open back: %v", err)
	}
	defer f.Close()
	rows, err := f.GetRows("Posts")
	if err != nil {
		t.Fatalf("get rows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d want 3 (1 header + 2 data)", len(rows))
	}
	if rows[0][0] != "ID" || rows[0][1] != "Title" || rows[0][2] != "Count" {
		t.Errorf("header = %v", rows[0])
	}
	if rows[1][0] != "abc" || rows[1][1] != "Hello" || rows[1][2] != "42" {
		t.Errorf("row 1 = %v", rows[1])
	}
}

func TestXLSXWriter_DefaultsHeaderToKey(t *testing.T) {
	cols := []Column{{Key: "name"}} // no Header
	w, err := NewXLSXWriter("S", cols)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.AppendRow(map[string]any{"name": "x"})
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatal(err)
	}
	f, _ := excelize.OpenReader(&buf)
	defer f.Close()
	rows, _ := f.GetRows("S")
	if rows[0][0] != "name" {
		t.Errorf("expected header fallback to key, got %q", rows[0][0])
	}
}

func TestXLSXWriter_MissingKeyRendersEmpty(t *testing.T) {
	cols := []Column{{Key: "a"}, {Key: "b"}}
	w, _ := NewXLSXWriter("S", cols)
	_ = w.AppendRow(map[string]any{"a": "yes"}) // b missing
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatal(err)
	}
	f, _ := excelize.OpenReader(&buf)
	defer f.Close()
	rows, _ := f.GetRows("S")
	// Excelize trims trailing empty cells; the row may have len 1.
	if rows[1][0] != "yes" {
		t.Errorf("a = %q", rows[1][0])
	}
}

func TestXLSXWriter_TimeFormat(t *testing.T) {
	cols := []Column{{Key: "at"}}
	w, _ := NewXLSXWriter("S", cols)
	when := time.Date(2026, 5, 11, 12, 34, 56, 0, time.UTC)
	_ = w.AppendRow(map[string]any{"at": when})
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatal(err)
	}
	f, _ := excelize.OpenReader(&buf)
	defer f.Close()
	rows, _ := f.GetRows("S")
	if rows[1][0] != "2026-05-11T12:34:56Z" {
		t.Errorf("time cell = %q", rows[1][0])
	}
}

func TestXLSXWriter_RejectsEmptyColumns(t *testing.T) {
	if _, err := NewXLSXWriter("S", nil); err == nil {
		t.Error("nil columns: want error")
	}
}

func TestXLSXWriter_AppendAfterCloseErrors(t *testing.T) {
	w, _ := NewXLSXWriter("S", []Column{{Key: "x"}})
	var buf bytes.Buffer
	if err := w.Finish(&buf); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendRow(map[string]any{"x": "y"}); err == nil {
		t.Error("append after WriteTo: want error")
	}
}

func TestXLSXWriter_DiscardAfterCloseNoOp(t *testing.T) {
	w, _ := NewXLSXWriter("S", []Column{{Key: "x"}})
	var buf bytes.Buffer
	_ = w.Finish(&buf)
	w.Discard() // must not panic
}

func TestFormatCell(t *testing.T) {
	tests := []struct {
		in       any
		wantKind string // "string" | "int" | "float" | "time" | "bool"
	}{
		{nil, "string"},
		{"hello", "string"},
		{42, "int"},
		{int64(7), "int"},
		{3.14, "float"},
		{true, "bool"},
		{time.Now(), "string"}, // → RFC3339 string
		{[]byte("bytes"), "string"},
	}
	for i, tc := range tests {
		got := formatCell(tc.in)
		switch tc.wantKind {
		case "string":
			if _, ok := got.(string); !ok {
				t.Errorf("[%d] %T → %T (want string)", i, tc.in, got)
			}
		case "int":
			switch got.(type) {
			case int, int64:
			default:
				t.Errorf("[%d] %T → %T (want int)", i, tc.in, got)
			}
		case "float":
			if _, ok := got.(float64); !ok {
				t.Errorf("[%d] %T → %T (want float)", i, tc.in, got)
			}
		case "bool":
			if _, ok := got.(bool); !ok {
				t.Errorf("[%d] %T → %T (want bool)", i, tc.in, got)
			}
		}
	}
}
