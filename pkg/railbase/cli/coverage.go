package cli

// v1.7.28d — `railbase coverage` CLI (docs/23 §3.12.7).
//
// Merges a Go coverprofile (the file `go test -coverprofile=…` emits)
// with a Vitest c8 JSON report into a single HTML file. The output is
// deliberately boring: one table per side, a totals row each, inline
// CSS, no JavaScript. An operator running inside a Docker container
// can `python -m http.server` the file and view it from any browser.
//
// v1 scope is the basic merger — counts files + statements + percent.
// A file-by-file source view (the "fancy" mode in §3.12.7) is deferred.
//
// We intentionally hand-roll the Go coverprofile parser instead of
// pulling in golang.org/x/tools/cover: the format is five fields per
// line and a 20-line state machine. Avoiding the dep keeps the binary
// ~10KB leaner and side-steps a sizable transitive `go/ast` pull-in.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// fileCoverage is a one-row record for the rendered table.
//
// Statements is the total count of measurable units (Go statements
// per coverprofile; c8 entries in `statementMap`). Covered is how
// many of those have a non-zero hit count. Percent is derived so the
// HTML template stays branch-free.
type fileCoverage struct {
	File       string
	Statements int
	Covered    int
	Percent    float64
}

// coverageReport is the merged view both sides feed into. Go and JS
// are kept as separate slices so the HTML template can omit a section
// entirely when its source is absent.
type coverageReport struct {
	Go      []fileCoverage
	JS      []fileCoverage
	GoTotal fileCoverage
	JSTotal fileCoverage
}

// parseGoCoverProfile parses a Go coverprofile from r and aggregates
// per-file statement totals.
//
// Format (excluding the mode: header line):
//
//	file.go:start_line.start_col,end_line.end_col stmt_count hit_count
//
// We only care about stmt_count and hit_count — a block contributes
// stmt_count to its file's total, and stmt_count to "covered" if
// hit_count > 0.
//
// Empty input returns an empty (zero-length) slice and no error. An
// unreadable line returns an error with the offending line included.
func parseGoCoverProfile(r io.Reader) ([]fileCoverage, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read coverprofile: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	// Per-file accumulators, then sorted at the end for deterministic
	// HTML output (operators eyeballing a diff appreciate stability).
	type acc struct{ stmts, covered int }
	byFile := map[string]*acc{}

	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// First non-empty line of a real coverprofile is the mode
		// header (e.g. "mode: set"). Skip if present.
		if strings.HasPrefix(line, "mode:") {
			continue
		}

		// Split on the first colon — filenames can't contain ':' on
		// the platforms we ship to. Right side is "range stmts hits".
		colon := strings.Index(line, ":")
		if colon < 0 {
			return nil, fmt.Errorf("coverprofile line %d: missing colon: %q", i+1, line)
		}
		file := line[:colon]
		rest := strings.Fields(line[colon+1:])
		if len(rest) != 3 {
			return nil, fmt.Errorf("coverprofile line %d: want 3 fields after colon, got %d: %q", i+1, len(rest), line)
		}
		stmts, err := strconv.Atoi(rest[1])
		if err != nil {
			return nil, fmt.Errorf("coverprofile line %d: stmt count: %w", i+1, err)
		}
		hits, err := strconv.Atoi(rest[2])
		if err != nil {
			return nil, fmt.Errorf("coverprofile line %d: hit count: %w", i+1, err)
		}

		a, ok := byFile[file]
		if !ok {
			a = &acc{}
			byFile[file] = a
		}
		a.stmts += stmts
		if hits > 0 {
			a.covered += stmts
		}
	}

	out := make([]fileCoverage, 0, len(byFile))
	for f, a := range byFile {
		out = append(out, fileCoverage{
			File:       f,
			Statements: a.stmts,
			Covered:    a.covered,
			Percent:    pct(a.covered, a.stmts),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, nil
}

// c8FileEntry is the slice of c8's coverage-final.json we read. The
// full schema has branch/function maps too; for the basic merge we
// only need statement counters.
//
// `s` is a JSON object whose keys are statement IDs (strings) and
// values are hit counts. `statementMap` is the same shape with range
// info — we only need its size.
type c8FileEntry struct {
	StatementMap map[string]json.RawMessage `json:"statementMap"`
	S            map[string]int             `json:"s"`
	Path         string                     `json:"path"`
}

// parseC8JSON parses a Vitest/c8 coverage-final.json and aggregates
// per-file totals.
//
// The c8 file is a single JSON object keyed by absolute file path
// (or by `entry.path` if present — we prefer the key for stability).
// Each entry has `s` (a map of statement-id → hit count) and
// `statementMap` (the parallel range metadata). The number of
// statements is len(statementMap); covered is the count of `s` values
// that are > 0.
func parseC8JSON(r io.Reader) ([]fileCoverage, error) {
	var raw map[string]c8FileEntry
	dec := json.NewDecoder(r)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse c8 JSON: %w", err)
	}

	out := make([]fileCoverage, 0, len(raw))
	for key, entry := range raw {
		file := key
		if entry.Path != "" {
			file = entry.Path
		}
		stmts := len(entry.StatementMap)
		covered := 0
		for _, hits := range entry.S {
			if hits > 0 {
				covered++
			}
		}
		out = append(out, fileCoverage{
			File:       file,
			Statements: stmts,
			Covered:    covered,
			Percent:    pct(covered, stmts),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, nil
}

// pct is the one place we guard against divide-by-zero. A file with
// zero measurable statements renders as 0%.
func pct(covered, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(covered) / float64(total) * 100
}

// totalsOf collapses a per-file slice into a single fileCoverage with
// File="TOTAL".
func totalsOf(rows []fileCoverage) fileCoverage {
	t := fileCoverage{File: "TOTAL"}
	for _, r := range rows {
		t.Statements += r.Statements
		t.Covered += r.Covered
	}
	t.Percent = pct(t.Covered, t.Statements)
	return t
}

// coverageHTMLTmpl is the rendered template. CSS is inlined (~30
// lines) so a single .html file is self-contained — operators can
// scp it out of a server without dragging assets along.
const coverageHTMLTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Railbase coverage report</title>
<style>
  body { font: 14px/1.4 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 2em; color: #1a1a1a; background: #fafafa; }
  h1 { font-size: 1.6em; margin: 0 0 0.2em; }
  h2 { font-size: 1.2em; margin: 1.6em 0 0.4em; padding-bottom: 0.3em; border-bottom: 1px solid #ddd; }
  .meta { color: #666; margin-bottom: 1.5em; }
  table { width: 100%; border-collapse: collapse; background: #fff; box-shadow: 0 1px 2px rgba(0,0,0,0.04); }
  th, td { padding: 0.5em 0.8em; text-align: left; border-bottom: 1px solid #eee; }
  th { background: #f3f3f3; font-weight: 600; font-size: 0.85em; text-transform: uppercase; letter-spacing: 0.04em; }
  td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; }
  tr.total { font-weight: 600; background: #f7f7f7; }
  tr.total td { border-top: 2px solid #ccc; }
  .pct-high { color: #1a7f37; }
  .pct-mid  { color: #9a6700; }
  .pct-low  { color: #b42318; }
  .empty { color: #999; font-style: italic; padding: 0.6em 0; }
</style>
</head>
<body>
<h1>Railbase coverage report</h1>
<p class="meta">Merged Go coverprofile + Vitest c8 JSON. Generated by <code>railbase coverage</code>.</p>

{{if .Go}}
<h2>Go</h2>
<table>
  <thead><tr><th>File</th><th class="num">Statements</th><th class="num">Covered</th><th class="num">%</th></tr></thead>
  <tbody>
  {{range .Go}}
    <tr><td>{{.File}}</td><td class="num">{{.Statements}}</td><td class="num">{{.Covered}}</td><td class="num {{pctClass .Percent}}">{{printf "%.1f" .Percent}}</td></tr>
  {{end}}
    <tr class="total"><td>TOTAL</td><td class="num">{{.GoTotal.Statements}}</td><td class="num">{{.GoTotal.Covered}}</td><td class="num {{pctClass .GoTotal.Percent}}">{{printf "%.1f" .GoTotal.Percent}}</td></tr>
  </tbody>
</table>
{{else}}
<h2>Go</h2>
<p class="empty">No Go coverprofile provided.</p>
{{end}}

{{if .JS}}
<h2>JavaScript (Vitest / c8)</h2>
<table>
  <thead><tr><th>File</th><th class="num">Statements</th><th class="num">Covered</th><th class="num">%</th></tr></thead>
  <tbody>
  {{range .JS}}
    <tr><td>{{.File}}</td><td class="num">{{.Statements}}</td><td class="num">{{.Covered}}</td><td class="num {{pctClass .Percent}}">{{printf "%.1f" .Percent}}</td></tr>
  {{end}}
    <tr class="total"><td>TOTAL</td><td class="num">{{.JSTotal.Statements}}</td><td class="num">{{.JSTotal.Covered}}</td><td class="num {{pctClass .JSTotal.Percent}}">{{printf "%.1f" .JSTotal.Percent}}</td></tr>
  </tbody>
</table>
{{else}}
<h2>JavaScript (Vitest / c8)</h2>
<p class="empty">No Vitest c8 JSON provided.</p>
{{end}}
</body>
</html>
`

// pctClass maps a coverage percentage to a CSS class for the cell.
// Thresholds match the loose convention most CI dashboards use.
func pctClass(p float64) string {
	switch {
	case p >= 80:
		return "pct-high"
	case p >= 50:
		return "pct-mid"
	default:
		return "pct-low"
	}
}

// renderHTML emits the merged report to w. Returns an error if both
// inputs are empty — a coverage report with neither side is almost
// certainly a misinvocation, not a deliberate "I have nothing".
func renderHTML(w io.Writer, rep coverageReport) error {
	if len(rep.Go) == 0 && len(rep.JS) == 0 {
		return errors.New("no coverage data: provide --go and/or --js")
	}
	tmpl, err := template.New("coverage").Funcs(template.FuncMap{
		"pctClass": pctClass,
	}).Parse(coverageHTMLTmpl)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	return tmpl.Execute(w, rep)
}

// buildReport opens the two input files (each optional), parses
// them, and assembles a coverageReport. Missing files are silently
// treated as "this side wasn't provided" — but only if the operator
// also didn't explicitly point at them. The caller decides.
func buildReport(goPath, jsPath string) (coverageReport, error) {
	var rep coverageReport

	if goPath != "" {
		f, err := os.Open(goPath)
		if err != nil {
			return rep, fmt.Errorf("open Go coverprofile %q: %w", goPath, err)
		}
		defer f.Close()
		rows, err := parseGoCoverProfile(f)
		if err != nil {
			return rep, fmt.Errorf("parse Go coverprofile %q: %w", goPath, err)
		}
		rep.Go = rows
		rep.GoTotal = totalsOf(rows)
	}

	if jsPath != "" {
		f, err := os.Open(jsPath)
		if err != nil {
			return rep, fmt.Errorf("open c8 JSON %q: %w", jsPath, err)
		}
		defer f.Close()
		rows, err := parseC8JSON(f)
		if err != nil {
			return rep, fmt.Errorf("parse c8 JSON %q: %w", jsPath, err)
		}
		rep.JS = rows
		rep.JSTotal = totalsOf(rows)
	}

	return rep, nil
}

// resolveInputs takes the operator's flag values plus a "did they set
// it" hint and resolves each side to either an existing file path or
// the empty string ("skip this side"). If a flag was set explicitly
// and points at a missing file, we error — silently dropping a
// requested input would mask CI misconfiguration.
func resolveInputs(goPath string, goExplicit bool, jsPath string, jsExplicit bool) (string, string, error) {
	resolve := func(path string, explicit bool, label string) (string, error) {
		if path == "" {
			return "", nil
		}
		_, err := os.Stat(path)
		if err == nil {
			return path, nil
		}
		if os.IsNotExist(err) {
			if explicit {
				return "", fmt.Errorf("%s file %q not found", label, path)
			}
			// Default path didn't exist; that's fine, just skip.
			return "", nil
		}
		return "", fmt.Errorf("stat %s file %q: %w", label, path, err)
	}
	g, err := resolve(goPath, goExplicit, "--go")
	if err != nil {
		return "", "", err
	}
	j, err := resolve(jsPath, jsExplicit, "--js")
	if err != nil {
		return "", "", err
	}
	if g == "" && j == "" {
		return "", "", fmt.Errorf("no coverage inputs found: looked for %q and %q (pass --go / --js to override)", goPath, jsPath)
	}
	return g, j, nil
}

func newCoverageCmd() *cobra.Command {
	var (
		goPath  string
		jsPath  string
		outPath string
	)
	cmd := &cobra.Command{
		Use:   "coverage",
		Short: "Merge Go coverprofile + Vitest c8 JSON into a unified HTML report",
		Long: `Merge a Go coverprofile and a Vitest c8 JSON report into a single
self-contained HTML file.

Either source is optional — if --go is missing/empty, only the JS
side renders, and vice versa. At least one of the two must exist or
the command errors with a friendly hint.

Defaults:
  --go  ./coverage.go.out
  --js  ./admin/coverage/coverage-final.json
  --out ./coverage.html

Examples:
  railbase coverage
  railbase coverage --go ./coverage.out
  railbase coverage --js ./web/coverage/coverage-final.json
  railbase coverage --out ./reports/cov.html`,
		RunE: func(cmd *cobra.Command, args []string) error {
			goExplicit := cmd.Flags().Changed("go")
			jsExplicit := cmd.Flags().Changed("js")

			g, j, err := resolveInputs(goPath, goExplicit, jsPath, jsExplicit)
			if err != nil {
				return err
			}

			rep, err := buildReport(g, j)
			if err != nil {
				return err
			}

			// Create parent dir if it doesn't exist — operators
			// pointing --out at coverage/index.html shouldn't have
			// to mkdir first.
			if dir := filepath.Dir(outPath); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("create output dir %q: %w", dir, err)
				}
			}

			f, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("create output %q: %w", outPath, err)
			}
			defer f.Close()

			if err := renderHTML(f, rep); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"wrote %s (Go: %d files / %.1f%%, JS: %d files / %.1f%%)\n",
				outPath, len(rep.Go), rep.GoTotal.Percent, len(rep.JS), rep.JSTotal.Percent)
			return nil
		},
	}
	cmd.Flags().StringVar(&goPath, "go", "./coverage.go.out", "path to Go coverprofile (empty string disables)")
	cmd.Flags().StringVar(&jsPath, "js", "./admin/coverage/coverage-final.json", "path to Vitest c8 JSON (empty string disables)")
	cmd.Flags().StringVar(&outPath, "out", "./coverage.html", "output HTML file path")
	return cmd
}
