package export

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestTemplates(t *testing.T) (*PDFTemplates, string) {
	t.Helper()
	dir := t.TempDir()
	tpl := NewPDFTemplates(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return tpl, dir
}

func writeTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestPDFTemplates_RenderHappyPath(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "hello.md", "# Hello {{ .Name }}\n\nWelcome.\n")
	if err := tpl.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := tpl.Render("hello", map[string]any{"Name": "World"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Errorf("not a PDF: %q", out[:20])
	}
	if !bytes.Contains(out, []byte("%%EOF")) {
		t.Error("missing PDF trailer")
	}
}

func TestPDFTemplates_RenderAddsMdSuffix(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "x.md", "# Hi")
	_ = tpl.Load()
	if _, err := tpl.Render("x", nil); err != nil {
		t.Errorf("render without suffix: %v", err)
	}
	if _, err := tpl.Render("x.md", nil); err != nil {
		t.Errorf("render with suffix: %v", err)
	}
}

func TestPDFTemplates_TemplateNotFound(t *testing.T) {
	tpl, _ := newTestTemplates(t)
	_ = tpl.Load()
	_, err := tpl.Render("nope", nil)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("got %v, want ErrTemplateNotFound", err)
	}
}

func TestPDFTemplates_MissingDirIsNotError(t *testing.T) {
	tpl := NewPDFTemplates(filepath.Join(t.TempDir(), "does", "not", "exist"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := tpl.Load(); err != nil {
		t.Errorf("missing dir should not error: %v", err)
	}
	if len(tpl.List()) != 0 {
		t.Errorf("list should be empty: %v", tpl.List())
	}
}

func TestPDFTemplates_BadTemplateSkippedNotFatal(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "ok.md", "# Hi {{ .X }}")
	writeTemplate(t, dir, "broken.md", "# Hi {{ .X") // unterminated action
	if err := tpl.Load(); err != nil {
		t.Errorf("load should not fail on bad template: %v", err)
	}
	if names := tpl.List(); len(names) != 1 || names[0] != "ok.md" {
		t.Errorf("expected only ok.md cached, got %v", names)
	}
}

func TestPDFTemplates_HelperDate(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "d.md", `{{ date "2006-01-02" .When }}`)
	_ = tpl.Load()
	when := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	out, err := tpl.Render("d", map[string]any{"When": when})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestPDFTemplates_HelperDefault(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "d.md", `{{ .Missing | default "fallback" }}`)
	_ = tpl.Load()
	out, err := tpl.Render("d", map[string]any{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestPDFTemplates_HelperRange(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "items.md", "# Items\n\n{{ range .Records }}- {{ .name }}\n{{ end }}")
	_ = tpl.Load()
	out, err := tpl.Render("items", map[string]any{
		"Records": []map[string]any{
			{"name": "alpha"},
			{"name": "bravo"},
			{"name": "charlie"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestPDFTemplates_ExecError(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	// Call a non-existent function in the template — text/template
	// surfaces this as an exec error, not a parse error.
	writeTemplate(t, dir, "exec.md", `{{ nope }}`)
	_ = tpl.Load()
	// Parse error → not loaded → not found.
	if _, err := tpl.Render("exec", nil); err == nil {
		t.Error("expected error for bad helper call")
	}
}

func TestPDFTemplates_List(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "b.md", "b")
	writeTemplate(t, dir, "a.md", "a")
	writeTemplate(t, dir, "c.md", "c")
	writeTemplate(t, dir, "skip.txt", "non-md") // should be ignored
	_ = tpl.Load()
	names := tpl.List()
	if got := strings.Join(names, ","); got != "a.md,b.md,c.md" {
		t.Errorf("List = %q want a.md,b.md,c.md", got)
	}
}

func TestPDFTemplates_HotReload(t *testing.T) {
	tpl, dir := newTestTemplates(t)
	writeTemplate(t, dir, "initial.md", "# Initial")
	if err := tpl.Load(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tpl.StartWatcher(ctx); err != nil {
		t.Fatalf("watcher: %v", err)
	}
	defer tpl.Stop()

	// New template appears on disk; debounced reload picks it up.
	writeTemplate(t, dir, "new.md", "# New")
	deadline := time.After(2 * time.Second)
	for {
		names := tpl.List()
		hasInitial, hasNew := false, false
		for _, n := range names {
			if n == "initial.md" {
				hasInitial = true
			}
			if n == "new.md" {
				hasNew = true
			}
		}
		if hasInitial && hasNew {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("hot reload didn't pick up new.md within 2s; have %v", names)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestPDFTemplates_HelperFuncsMatrix(t *testing.T) {
	// Smoke each helper through a single small template so the
	// funcmap registration is exercised on every reload.
	tpl, dir := newTestTemplates(t)
	body := strings.Join([]string{
		`{{ date "2006" .Now }}`,
		`{{ .Title | default "Untitled" }}`,
		`{{ truncate 5 "hello world" }}`,
		`{{ money 42.5 }}`,
		`{{ range each .Items }}{{ . }}{{ end }}`,
	}, "\n")
	writeTemplate(t, dir, "all.md", body)
	if err := tpl.Load(); err != nil {
		t.Fatal(err)
	}
	out, err := tpl.Render("all", map[string]any{
		"Now":   time.Now(),
		"Items": []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
}

func TestFnDate(t *testing.T) {
	when := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	if got := fnDate("2006-01-02", when); got != "2026-05-11" {
		t.Errorf("time.Time: %q", got)
	}
	if got := fnDate("2006-01-02", &when); got != "2026-05-11" {
		t.Errorf("*time.Time: %q", got)
	}
	if got := fnDate("2006", "2026-05-11T00:00:00Z"); got != "2026" {
		t.Errorf("RFC3339 string: %q", got)
	}
	if got := fnDate("2006-01-02", "not a date"); got != "not a date" {
		t.Errorf("unparseable string: should pass through, got %q", got)
	}
	if got := fnDate("2006", nil); got != "" {
		t.Errorf("nil: %q", got)
	}
}

func TestFnDefault(t *testing.T) {
	if got := fnDefault("fallback", ""); got != "fallback" {
		t.Errorf("empty string: %v", got)
	}
	if got := fnDefault("fallback", "value"); got != "value" {
		t.Errorf("non-empty: %v", got)
	}
	if got := fnDefault(0, nil); got != 0 {
		t.Errorf("nil: %v", got)
	}
	if got := fnDefault("x", 42); got != 42 {
		t.Errorf("non-zero int: %v", got)
	}
}

func TestFnMoneyStub(t *testing.T) {
	if got := fnMoneyStub(42.5); got != "$42.50" {
		t.Errorf("float: %q", got)
	}
	if got := fnMoneyStub(7); got != "$7" {
		t.Errorf("int: %q", got)
	}
	if got := fnMoneyStub("USD 10"); got != "$USD 10" {
		t.Errorf("string: %q", got)
	}
	if got := fnMoneyStub(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
}

func TestIsZero(t *testing.T) {
	tests := []struct {
		v    any
		want bool
	}{
		{nil, true},
		{"", true},
		{0, true},
		{int64(0), true},
		{0.0, true},
		{false, true},
		{"x", false},
		{1, false},
		{true, false},
		{[]int{1, 2}, false}, // slice → not "zero" per our simple shape
	}
	for i, tc := range tests {
		if got := isZero(tc.v); got != tc.want {
			t.Errorf("[%d] isZero(%v) = %v want %v", i, tc.v, got, tc.want)
		}
	}
}
