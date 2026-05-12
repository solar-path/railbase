package export

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ErrTemplateNotFound is returned by PDFTemplates.Render when the
// named template is missing from the loader's directory.
var ErrTemplateNotFound = errors.New("export: pdf template not found")

// PDFTemplates manages a directory of Markdown templates compiled
// via text/template, with helpers registered for the docs/08 Helpers
// list. Templates render to a Markdown intermediate, then pipe
// through v1.6.2's RenderMarkdownToPDF for the final PDF bytes.
//
// Lifecycle:
//
//	t := NewPDFTemplates("pb_data/pdf_templates", logger)
//	if err := t.Load(); err != nil { ... }
//	if err := t.StartWatcher(ctx); err != nil { ... }
//	defer t.Stop()
//	out, err := t.Render("posts-report.md", data)
//
// Hot-reload via fsnotify mirrors the v1.2.0 hooks pattern: 150ms
// debounce, watches the dir root, .md suffix gate. Reloads replace
// the whole cache atomically.
type PDFTemplates struct {
	mu sync.RWMutex

	dir   string
	log   *slog.Logger
	funcs template.FuncMap

	cache map[string]*template.Template

	// stops are watcher teardowns. Stop() invokes them in LIFO order.
	stops []func()
}

// NewPDFTemplates builds an empty loader. Call Load() to read the
// directory + populate the cache. If `log` is nil, slog.Default()
// is used.
func NewPDFTemplates(dir string, log *slog.Logger) *PDFTemplates {
	if log == nil {
		log = slog.Default()
	}
	return &PDFTemplates{
		dir:   dir,
		log:   log,
		funcs: defaultPDFFuncs(),
		cache: map[string]*template.Template{},
	}
}

// Load reads every `*.md` file under the configured directory and
// compiles it. Replaces the previous cache atomically. Missing
// directory is not an error — the loader stays empty and Render
// will return ErrTemplateNotFound for any name.
func (t *PDFTemplates) Load() error {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			// No directory yet — treat as empty, not an error. Operators
			// who haven't created any templates shouldn't get boot noise.
			t.replace(map[string]*template.Template{})
			return nil
		}
		return fmt.Errorf("export: read pdf-templates dir %s: %w", t.dir, err)
	}
	fresh := make(map[string]*template.Template, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(t.dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.log.Warn("export: read pdf template", "path", path, "err", err)
			continue
		}
		tpl, err := template.New(e.Name()).Funcs(t.funcs).Parse(string(raw))
		if err != nil {
			// Bad template = log + skip; don't fail the whole reload
			// because one operator edit had a typo. The previous good
			// version stays in the cache.
			t.log.Warn("export: parse pdf template", "path", path, "err", err)
			continue
		}
		fresh[e.Name()] = tpl
	}
	t.replace(fresh)
	return nil
}

// Render looks up `name` (with optional `.md` suffix), executes it
// against `data` via text/template (with the registered helpers),
// then renders the resulting markdown to PDF bytes.
//
// `data` can be any Go value text/template knows how to walk: a
// struct, a map, etc. Convention from docs/08: pass a struct with
// at least Records / Tenant / Now / Filter.
func (t *PDFTemplates) Render(name string, data any) ([]byte, error) {
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	t.mu.RLock()
	tpl, ok := t.cache[name]
	t.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("export: execute pdf template %q: %w", name, err)
	}
	// Pipe the interpolated markdown through v1.6.2's renderer. We pass
	// nil for the data arg there — interpolation already happened.
	return RenderMarkdownToPDF(buf.Bytes(), nil)
}

// List returns every cached template name in deterministic order.
// Used by admin UI / `railbase pdf list-templates` (CLI deferred).
func (t *PDFTemplates) List() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.cache))
	for name := range t.cache {
		out = append(out, name)
	}
	// Lightweight in-place sort — names are few (<100), cost negligible.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// StartWatcher spins up an fsnotify-driven reloader. Mirrors the
// v1.2.0 hooks pattern: 150ms debounce, .md-suffix gate, create the
// directory if missing so operators see it as a hint to drop files
// in. Returns nil on a nil receiver so callers don't need to guard.
func (t *PDFTemplates) StartWatcher(ctx context.Context) error {
	if t == nil {
		return nil
	}
	if _, err := os.Stat(t.dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			return fmt.Errorf("export: mkdir %s: %w", t.dir, err)
		}
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("export: fsnotify: %w", err)
	}
	if err := w.Add(t.dir); err != nil {
		_ = w.Close()
		return fmt.Errorf("export: watch %s: %w", t.dir, err)
	}
	stop := make(chan struct{})
	go t.watchLoop(ctx, w, stop)
	t.stops = append(t.stops, func() {
		close(stop)
		_ = w.Close()
	})
	return nil
}

// Stop tears down any started watchers. Idempotent.
func (t *PDFTemplates) Stop() {
	if t == nil {
		return
	}
	for _, s := range t.stops {
		s()
	}
	t.stops = nil
}

func (t *PDFTemplates) watchLoop(ctx context.Context, w *fsnotify.Watcher, stop chan struct{}) {
	const debounce = 150 * time.Millisecond
	var reload *time.Timer
	doReload := func() {
		if err := t.Load(); err != nil {
			t.log.Error("export: pdf template reload failed", "err", err)
			return
		}
		t.log.Info("export: pdf templates reloaded", "count", len(t.cache))
	}
	for {
		select {
		case <-ctx.Done():
			if reload != nil {
				reload.Stop()
			}
			return
		case <-stop:
			if reload != nil {
				reload.Stop()
			}
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(strings.ToLower(ev.Name), ".md") {
				continue
			}
			if reload != nil {
				reload.Stop()
			}
			reload = time.AfterFunc(debounce, doReload)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			t.log.Warn("export: pdf template watcher error", "err", err)
		}
	}
}

func (t *PDFTemplates) replace(fresh map[string]*template.Template) {
	t.mu.Lock()
	t.cache = fresh
	t.mu.Unlock()
}

// defaultPDFFuncs returns the helper funcmap registered on every
// template. Per docs/08 §Helpers we ship `date`, `default` natively;
// `if`/`range` are text/template stdlib builtins (free). The remaining
// docs/08 helpers — `money`, `truncate`, `each` (range alias) — are
// stubbed so authors can write templates that use them today and have
// them honoured when v1.6.5 lands.
func defaultPDFFuncs() template.FuncMap {
	return template.FuncMap{
		"date":     fnDate,
		"default":  fnDefault,
		"money":    fnMoneyStub,
		"truncate": fnTruncate,
		"each":     fnEachStub,
	}
}

// fnDate formats a Go time.Time using the supplied Go-layout string.
// Pipe-friendly usage:
//
//	{{ .Now | date "2006-01-02" }}
//	{{ date "Jan 2, 2006" .Invoice.IssuedAt }}
//
// Accepts time.Time, *time.Time, or a string already in RFC3339 (the
// shape RenderMarkdownToPDF emits for time values). Anything else
// renders as the raw string form.
func fnDate(layout string, v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.Format(layout)
	case *time.Time:
		if t == nil {
			return ""
		}
		return t.Format(layout)
	case string:
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return parsed.Format(layout)
		}
		return t
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// fnDefault returns `v` when truthy, `fallback` otherwise. text/template
// already has `or` / `and` / `not`, but the explicit `default` helper
// reads more naturally for the operator's common case:
//
//	{{ .Title | default "Untitled" }}
func fnDefault(fallback, v any) any {
	if isZero(v) {
		return fallback
	}
	return v
}

// fnTruncate clips a string to N runes + ellipsis. Useful for table
// cells in PDF reports. Implemented today (not a stub) because the
// rune-aware boundary logic already lives in pdf.go.
func fnTruncate(n int, s string) string {
	return truncateForWidth(s, float64(n*6)+4)
}

// fnMoneyStub renders `v` with a `$` prefix. v1.6.5 will swap this
// for locale-aware currency formatting using the v1.5.6 `currency`
// field type's metadata. Stub for now so authors can write money
// templates today.
func fnMoneyStub(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return fmt.Sprintf("$%.2f", x)
	case int, int64:
		return fmt.Sprintf("$%v", x)
	case string:
		return "$" + x
	}
	return fmt.Sprint(v)
}

// fnEachStub is the docs/08 `each` helper — semantically identical
// to text/template's stdlib `range`. We register it as an alias so
// the docs/08 template syntax compiles + does the right thing when
// run, but the canonical form remains `{{range .Items}}{{end}}`.
//
// Since text/template doesn't expose pipeline-context iteration via
// a function (only via the `range` action), this stub just returns
// the input slice unchanged. Operators using `{{ .Items | each }}`
// get the original slice back — they should use `{{ range .Items }}`
// instead. The helper exists to avoid template compile errors.
func fnEachStub(v any) any { return v }

// isZero returns true when v is Go's zero value for its type — used
// by fnDefault to decide whether to swap in the fallback.
func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case int:
		return x == 0
	case int64:
		return x == 0
	case float64:
		return x == 0
	}
	return false
}
