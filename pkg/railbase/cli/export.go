package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// newExportCmd assembles `railbase export ...`.
//
// Subcommands (per docs/08 §5):
//
//	export collection <name> --format xlsx|pdf [--filter expr] [--sort cols]
//	                         [--columns c1,c2,...] [--out path]
//	                         [--sheet name] [--title title] [--header h] [--footer f]
//	                         [--include-deleted] [--template template.md]
//
// CLI bypasses RBAC: the operator running the binary has full access
// to the local DB. List/View Rules are NOT applied — the CLI is for
// operations work (backups, ad-hoc reports), not user-facing fetches.
// Tenant filters are off too — the export emits every tenant unless
// the caller passes `--filter "tenant_id='<uuid>'"` explicitly.
func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export collection data to XLSX or PDF",
	}
	cmd.AddCommand(newExportCollectionCmd())
	return cmd
}

func newExportCollectionCmd() *cobra.Command {
	var (
		format         string
		filterExpr     string
		sortExpr       string
		columns        string
		out            string
		sheet          string
		title          string
		header         string
		footer         string
		includeDeleted bool
		template       string
		templateDir    string
		maxRows        int
	)
	cmd := &cobra.Command{
		Use:   "collection <name>",
		Short: "Export a collection to XLSX or PDF",
		Long: `Export every record (or a filtered subset) of a collection to a
spreadsheet or PDF file on disk. Reads the collection schema from
the binary's registered specs — the operator must have called
schema.Register() somewhere in the project for the name to resolve.

Examples:

  railbase export collection posts --format xlsx --out posts.xlsx
  railbase export collection posts --format xlsx --filter "status='published'" --sort "-created" --out latest.xlsx
  railbase export collection posts --format pdf --template invoice.md --out invoice.pdf

CLI exports bypass List/View Rules — operators running the binary
have full access. For RBAC-aware exports, use the REST endpoints
or the async POST /api/exports surface (v1.6.5).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			switch format {
			case "xlsx", "pdf":
				// ok
			default:
				return fmt.Errorf("--format must be 'xlsx' or 'pdf', got %q", format)
			}
			if out == "" {
				// Default to <collection>-<UTC>.<format> in the cwd —
				// matches the REST Content-Disposition convention.
				out = fmt.Sprintf("%s-%s.%s",
					name, time.Now().UTC().Format("20060102-150405"), format)
			}
			if maxRows <= 0 {
				maxRows = 1_000_000
			}

			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			spec, err := resolveCollectionSpec(name)
			if err != nil {
				return err
			}
			if spec.Auth {
				return fmt.Errorf("collection %q is an auth collection; auth records aren't exportable", name)
			}

			req := exportRequest{
				Format:         format,
				Spec:           spec,
				Filter:         filterExpr,
				Sort:           sortExpr,
				Columns:        columns,
				Sheet:          sheet,
				Title:          title,
				Header:         header,
				Footer:         footer,
				IncludeDeleted: includeDeleted,
				Template:       template,
				TemplateDir:    templateDir,
				MaxRows:        maxRows,
				Out:            out,
			}
			rendered, rowCount, err := runExport(cmd.Context(), rt, req)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"exported %d rows → %s (%d bytes)\n", rowCount, rendered, fileSize(rendered))
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "xlsx", "output format: xlsx or pdf")
	cmd.Flags().StringVar(&filterExpr, "filter", "", "filter expression (same grammar as /api/collections/.../records?filter=...)")
	cmd.Flags().StringVar(&sortExpr, "sort", "", "sort spec, e.g. `-created,id` (default: -created,-id)")
	cmd.Flags().StringVar(&columns, "columns", "", "comma-separated column allow-list (default: all readable)")
	cmd.Flags().StringVar(&out, "out", "", "output path (default: <collection>-<UTC>.<format>)")
	cmd.Flags().StringVar(&sheet, "sheet", "", "XLSX sheet name (default: collection name)")
	cmd.Flags().StringVar(&title, "title", "", "PDF document title")
	cmd.Flags().StringVar(&header, "header", "", "PDF repeating page header")
	cmd.Flags().StringVar(&footer, "footer", "", "PDF document footer")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted rows (default: live rows only)")
	cmd.Flags().StringVar(&template, "template", "", "PDF Markdown template name (relative to --template-dir)")
	cmd.Flags().StringVar(&templateDir, "template-dir", "", "directory holding PDF templates (default: <data-dir>/pdf_templates)")
	cmd.Flags().IntVar(&maxRows, "max-rows", 1_000_000, "row cap; export aborts if exceeded")
	return cmd
}

// exportRequest is the resolved input for runExport — same shape the
// REST async worker handles, kept package-private so the CLI surface
// doesn't expose internals.
type exportRequest struct {
	Format         string
	Spec           builder.CollectionSpec
	Filter         string
	Sort           string
	Columns        string
	Sheet          string
	Title          string
	Header         string
	Footer         string
	IncludeDeleted bool
	Template       string
	TemplateDir    string
	MaxRows        int
	Out            string
}

// runExport composes the WHERE (filter only — no rules, no tenant
// fragment), runs the SELECT, streams rows into the right writer,
// and lands the result on disk. Returns (outPath, rowCount, error).
func runExport(ctx context.Context, rt *runtimeContext, req exportRequest) (string, int, error) {
	// Empty filter.Context — magic vars (@request.auth.id, @me) would
	// resolve to "" anyway; CLI doesn't have a principal. If a filter
	// uses them, the bound value is just empty string.
	fctx := filter.Context{}

	whereSQL := ""
	var whereArgs []any
	if req.Filter != "" {
		ast, err := filter.Parse(req.Filter)
		if err != nil {
			return "", 0, fmt.Errorf("parse filter: %w", err)
		}
		sql, args, _, err := filter.Compile(ast, req.Spec, fctx, 1)
		if err != nil {
			return "", 0, fmt.Errorf("compile filter: %w", err)
		}
		whereSQL = sql
		whereArgs = args
	}

	sortKeys, err := filter.ParseSort(req.Sort, req.Spec)
	if err != nil {
		return "", 0, fmt.Errorf("parse sort: %w", err)
	}

	// Soft-delete: same default as REST — exclude tombstones unless
	// the operator explicitly asks for them.
	finalWhere := whereSQL
	if req.Spec.SoftDelete && !req.IncludeDeleted {
		if finalWhere == "" {
			finalWhere = "deleted IS NULL"
		} else {
			finalWhere = "deleted IS NULL AND " + finalWhere
		}
	}

	cols := allReadableColumnsForCLI(req.Spec)
	// Column allow-list narrowing — same precedence as REST: query
	// (--columns) > schema config > all-readable.
	cfgCols, cfgHeaders := configCols(req.Spec, req.Format)
	cols, err = narrowColumns(cols, req.Columns, cfgCols, cfgHeaders)
	if err != nil {
		return "", 0, err
	}

	limitArg := len(whereArgs) + 1
	args := append(append([]any{}, whereArgs...), req.MaxRows+1)
	selectSQL := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT $%d",
		strings.Join(selectColumns(req.Spec), ", "),
		req.Spec.Name,
		whereClauseSQL(finalWhere),
		orderBySQL(sortKeys),
		limitArg)

	pgRows, err := rt.pool.Pool.Query(ctx, selectSQL, args...)
	if err != nil {
		return "", 0, fmt.Errorf("query: %w", err)
	}
	defer pgRows.Close()

	// Render path: XLSX streams; PDF (data-table OR template) buffers.
	outFile, err := os.Create(req.Out)
	if err != nil {
		return "", 0, fmt.Errorf("create %s: %w", req.Out, err)
	}
	defer outFile.Close()

	switch req.Format {
	case "xlsx":
		count, err := renderXLSXFromRows(pgRows, req, cols, outFile)
		if err != nil {
			return "", 0, err
		}
		return req.Out, count, nil
	case "pdf":
		count, err := renderPDFFromRows(ctx, pgRows, req, cols, outFile, rt)
		if err != nil {
			return "", 0, err
		}
		return req.Out, count, nil
	}
	return "", 0, fmt.Errorf("unknown format %q", req.Format)
}

// renderXLSXFromRows iterates pgx rows + writes to the XLSXWriter.
// Mirrors the REST handler's per-row loop including the maxRows
// overflow guard.
func renderXLSXFromRows(pgRows pgx.Rows, req exportRequest, cols []export.Column, out *os.File) (int, error) {
	sheet := firstNonEmpty(req.Sheet, configSheet(req.Spec), req.Spec.Name)
	xw, err := export.NewXLSXWriter(sheet, cols)
	if err != nil {
		return 0, fmt.Errorf("xlsx writer: %w", err)
	}
	defer xw.Discard()

	count := 0
	for pgRows.Next() {
		row, err := rowToMap(pgRows)
		if err != nil {
			return 0, fmt.Errorf("scan: %w", err)
		}
		count++
		if count > req.MaxRows {
			return 0, fmt.Errorf("exceeds --max-rows=%d", req.MaxRows)
		}
		if err := xw.AppendRow(row); err != nil {
			return 0, fmt.Errorf("append: %w", err)
		}
	}
	if err := pgRows.Err(); err != nil {
		return 0, fmt.Errorf("iter: %w", err)
	}
	if err := xw.Finish(out); err != nil {
		return 0, fmt.Errorf("finish: %w", err)
	}
	return count, nil
}

func renderPDFFromRows(ctx context.Context, pgRows pgx.Rows, req exportRequest, cols []export.Column, out *os.File, rt *runtimeContext) (int, error) {
	// Template branch.
	if req.Template != "" {
		tplDir := req.TemplateDir
		if tplDir == "" {
			tplDir = filepath.Join(rt.cfg.DataDir, "pdf_templates")
		}
		loader := export.NewPDFTemplates(tplDir, rt.log)
		if err := loader.Load(); err != nil {
			return 0, fmt.Errorf("load templates: %w", err)
		}
		records := make([]map[string]any, 0, 128)
		count := 0
		for pgRows.Next() {
			row, err := rowToMap(pgRows)
			if err != nil {
				return 0, fmt.Errorf("scan: %w", err)
			}
			count++
			if count > req.MaxRows {
				return 0, fmt.Errorf("exceeds --max-rows=%d", req.MaxRows)
			}
			records = append(records, row)
		}
		if err := pgRows.Err(); err != nil {
			return 0, fmt.Errorf("iter: %w", err)
		}
		data := struct {
			Records []map[string]any
			Tenant  string
			Now     time.Time
			Filter  string
		}{
			Records: records,
			Tenant:  "",
			Now:     time.Now().UTC(),
			Filter:  req.Filter,
		}
		pdfBytes, err := loader.Render(req.Template, data)
		if err != nil {
			return 0, fmt.Errorf("render template: %w", err)
		}
		if _, err := out.Write(pdfBytes); err != nil {
			return 0, fmt.Errorf("write: %w", err)
		}
		return count, nil
	}

	// Data-table branch — same config-precedence as the REST handler.
	title := firstNonEmpty(req.Title, configPDFTitle(req.Spec), req.Spec.Name)
	header := firstNonEmpty(req.Header, configPDFHeader(req.Spec))
	footer := firstNonEmpty(req.Footer, configPDFFooter(req.Spec))

	pdfCols := make([]export.PDFColumn, len(cols))
	for i, c := range cols {
		pdfCols[i] = export.PDFColumn{Key: c.Key, Header: c.Header}
	}
	pw, err := export.NewPDFWriter(export.PDFConfig{
		Title: title, Header: header, Footer: footer,
	}, pdfCols)
	if err != nil {
		return 0, fmt.Errorf("pdf writer: %w", err)
	}
	defer pw.Discard()

	count := 0
	for pgRows.Next() {
		row, err := rowToMap(pgRows)
		if err != nil {
			return 0, fmt.Errorf("scan: %w", err)
		}
		count++
		if count > req.MaxRows {
			return 0, fmt.Errorf("exceeds --max-rows=%d", req.MaxRows)
		}
		if err := pw.AppendRow(row); err != nil {
			return 0, fmt.Errorf("append: %w", err)
		}
	}
	if err := pgRows.Err(); err != nil {
		return 0, fmt.Errorf("iter: %w", err)
	}
	if err := pw.Finish(out); err != nil {
		return 0, fmt.Errorf("finish: %w", err)
	}
	return count, nil
}

// rowToMap adapts pgx.Rows into the map[string]any shape the export
// writers consume. Kept local so the CLI doesn't depend on
// internal/api/rest.scanRow (which has special-cases for the JSON
// wire format that aren't relevant here — spreadsheets render
// Go-native scalars verbatim).
func rowToMap(rows pgx.Rows) (map[string]any, error) {
	fields := rows.FieldDescriptions()
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(fields))
	for i, f := range fields {
		out[f.Name] = vals[i]
	}
	return out, nil
}

// --- spec / column helpers ---

func resolveCollectionSpec(name string) (builder.CollectionSpec, error) {
	c := registry.Get(name)
	if c == nil {
		return builder.CollectionSpec{},
			fmt.Errorf("collection %q not registered (check your project's schema.Register calls)", name)
	}
	return c.Spec(), nil
}

// allReadableColumnsForCLI returns the default column set — same shape
// the REST handler uses, but without the dependency on
// `internal/api/rest.allReadableColumns` (which is package-private).
// Includes system fields + user fields (filtering out file / password
// / relations / files / multiselect, same as the REST writer).
func allReadableColumnsForCLI(spec builder.CollectionSpec) []export.Column {
	cols := []export.Column{
		{Key: "id", Header: "id"},
		{Key: "created", Header: "created"},
		{Key: "updated", Header: "updated"},
	}
	if spec.Tenant {
		cols = append(cols, export.Column{Key: "tenant_id", Header: "tenant_id"})
	}
	if spec.SoftDelete {
		cols = append(cols, export.Column{Key: "deleted", Header: "deleted"})
	}
	if spec.AdjacencyList {
		cols = append(cols, export.Column{Key: "parent", Header: "parent"})
	}
	if spec.Ordered {
		cols = append(cols, export.Column{Key: "sort_index", Header: "sort_index"})
	}
	for _, f := range spec.Fields {
		switch f.Type {
		case builder.TypeFile, builder.TypeFiles, builder.TypePassword, builder.TypeRelations:
			continue
		}
		cols = append(cols, export.Column{Key: f.Name, Header: f.Name})
	}
	return cols
}

// narrowColumns applies the same precedence the REST handler uses:
// query (--columns) > config (spec.Exports.*.Columns) > all readable.
// Headers map applies in all cases.
func narrowColumns(all []export.Column, queryCols string, cfgCols []string, cfgHeaders map[string]string) ([]export.Column, error) {
	allowed := make(map[string]export.Column, len(all))
	for _, c := range all {
		allowed[c.Key] = c
	}
	var keys []string
	switch {
	case queryCols != "":
		for _, p := range strings.Split(queryCols, ",") {
			k := strings.TrimSpace(p)
			if k == "" {
				continue
			}
			keys = append(keys, k)
		}
	case len(cfgCols) > 0:
		keys = cfgCols
	default:
		applyHeadersCLI(all, cfgHeaders)
		return all, nil
	}
	out := make([]export.Column, 0, len(keys))
	for _, k := range keys {
		c, ok := allowed[k]
		if !ok {
			names := make([]string, 0, len(allowed))
			for k := range allowed {
				names = append(names, k)
			}
			return nil, fmt.Errorf("unknown column %q (allowed: %s)", k, strings.Join(names, ", "))
		}
		out = append(out, c)
	}
	applyHeadersCLI(out, cfgHeaders)
	return out, nil
}

func applyHeadersCLI(cols []export.Column, headers map[string]string) {
	if len(headers) == 0 {
		return
	}
	for i := range cols {
		if v, ok := headers[cols[i].Key]; ok && v != "" {
			cols[i].Header = v
		}
	}
}

func configCols(spec builder.CollectionSpec, format string) (cols []string, headers map[string]string) {
	if format == "xlsx" && spec.Exports.XLSX != nil {
		return spec.Exports.XLSX.Columns, spec.Exports.XLSX.Headers
	}
	if format == "pdf" && spec.Exports.PDF != nil {
		return spec.Exports.PDF.Columns, spec.Exports.PDF.Headers
	}
	return nil, nil
}

func configSheet(spec builder.CollectionSpec) string {
	if spec.Exports.XLSX != nil {
		return spec.Exports.XLSX.Sheet
	}
	return ""
}

func configPDFTitle(spec builder.CollectionSpec) string {
	if spec.Exports.PDF != nil {
		return spec.Exports.PDF.Title
	}
	return ""
}

func configPDFHeader(spec builder.CollectionSpec) string {
	if spec.Exports.PDF != nil {
		return spec.Exports.PDF.Header
	}
	return ""
}

func configPDFFooter(spec builder.CollectionSpec) string {
	if spec.Exports.PDF != nil {
		return spec.Exports.PDF.Footer
	}
	return ""
}

// --- SQL helpers ---

// selectColumns is the SELECT clause column list for the CLI export
// SELECT. Mirrors `internal/api/rest.buildSelectColumns` but uses
// minimal casts — the export writers consume Go scalars directly,
// no need for the UUID-as-text / Numeric-as-text dances the JSON
// renderer requires.
func selectColumns(spec builder.CollectionSpec) []string {
	cols := []string{"id::text AS id", "created", "updated"}
	if spec.Tenant {
		cols = append(cols, "tenant_id::text AS tenant_id")
	}
	if spec.SoftDelete {
		cols = append(cols, "deleted")
	}
	if spec.AdjacencyList {
		cols = append(cols, "parent::text AS parent")
	}
	if spec.Ordered {
		cols = append(cols, "sort_index")
	}
	for _, f := range spec.Fields {
		switch f.Type {
		case builder.TypeFile, builder.TypeFiles, builder.TypePassword, builder.TypeRelations:
			continue
		case builder.TypeRelation, builder.TypeFinance, builder.TypePercentage,
			builder.TypeTreePath, builder.TypeDateRange:
			// Match the REST handler's text-cast shape so the rendered
			// output matches what the REST endpoints emit.
			cols = append(cols, fmt.Sprintf("%s::text AS %s", f.Name, f.Name))
		default:
			cols = append(cols, f.Name)
		}
	}
	return cols
}

func whereClauseSQL(where string) string {
	if where == "" {
		return ""
	}
	return " WHERE " + where
}

func orderBySQL(keys []filter.SortKey) string {
	s := filter.JoinSQL(keys)
	if s == "" {
		return "created DESC, id DESC"
	}
	return s
}

// firstNonEmpty mirrors the REST helper of the same name — picked
// out here to avoid importing internal/api/rest from the CLI.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
