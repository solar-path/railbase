package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/tenant"
)

// defaultExportMaxRows caps the synchronous /export.xlsx response.
// Anything above this should run through the async jobs path (deferred
// to a follow-up slice). The cap keeps a single request's memory + time
// budget bounded — 100k rows × ~10 columns × ~64-byte avg cell is
// ~64 MB before excelize compresses, comfortably under the 256 MB
// soft ceiling docs/08 specifies.
const defaultExportMaxRows = 100_000

// exportXLSXHandler implements `GET /api/collections/{name}/export.xlsx`.
//
// Reuses the listHandler's filter / rule / tenant composition so the
// same `?filter=` and `?sort=` work — and so RBAC via ListRule applies
// without a second access decision. Returns a streamed XLSX body
// when ok, or a JSON error envelope when not.
//
// Query params:
//
//   - filter — same grammar as list
//   - sort — same as list (default created DESC, id DESC)
//   - columns — comma-separated allow-list of columns to include
//     (must be a subset of the collection's readable columns).
//     Default: all readable system + user columns.
//   - sheet — sheet name in the workbook. Default: collection name.
//   - includeDeleted — only meaningful on soft-delete collections.
//
// File name: `<collection>-<UTC yyyymmdd-hhmmss>.xlsx` via
// Content-Disposition.
func (d *handlerDeps) exportXLSXHandler(w http.ResponseWriter, r *http.Request) {
	collectionName := chi.URLParam(r, "name")
	spec, rerrSpec := resolveCollection(collectionName)
	if rerrSpec != nil {
		code, msg := classifyResolveErr(rerrSpec)
		d.emitExportAudit(r.Context(), r, "export.xlsx", outcomeForCode(code),
			code,
			map[string]any{"collection": collectionName, "error": msg})
		rerr.WriteJSON(w, rerrSpec)
		return
	}
	// Auth-collection records aren't exportable — same rule as list:
	// the generic CRUD path blocks /records to keep credential leakage
	// surface small. /export is the same surface with a different
	// renderer.
	if spec.Auth {
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeDenied,
			string(rerr.CodeForbidden),
			map[string]any{"collection": collectionName, "error": "auth collection"})
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"auth collection records are not exportable via /export.xlsx"))
		return
	}

	q := r.URL.Query()

	// v1.6.3: resolve column allow-list + headers honouring the
	// schema-declared .Export(...) config. Query params still win.
	var cfgCols []string
	var cfgHeaders map[string]string
	var cfgFormats map[string]string
	if spec.Exports.XLSX != nil {
		cfgCols = spec.Exports.XLSX.Columns
		cfgHeaders = spec.Exports.XLSX.Headers
		cfgFormats = spec.Exports.XLSX.Format
	}
	cols, errEnv := resolveExportColumns(spec, q.Get("columns"), cfgCols, cfgHeaders)
	if errEnv != nil {
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeFailed,
			string(errEnv.Code),
			map[string]any{"collection": spec.Name, "error": errEnv.Message})
		rerr.WriteJSON(w, errEnv)
		return
	}
	applyFormats(cols, cfgFormats)

	principal := authmw.PrincipalFrom(r.Context())
	fctx := filterCtx(principal)

	// Compose WHERE: tenant scope → list rule → user filter. Exact
	// same chain as listHandler so the RBAC story is identical.
	startParam := 1
	combined := compiledFragment{}
	if spec.Tenant && tenant.HasID(r.Context()) {
		var tf compiledFragment
		tf, startParam = tenantFragment(tenant.ID(r.Context()).String(), startParam)
		combined = combineFragments(combined, tf)
	}
	ruleFrag, nextParam, err := compileRule(spec.Rules.List, spec, fctx, startParam)
	if err != nil {
		d.log.Error("rest: export rule compile failed", "collection", spec.Name, "err", err)
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "rule compile failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}
	combined = combineFragments(combined, ruleFrag)
	userFrag, _, err := compileFilter(q.Get("filter"), spec, fctx, nextParam)
	if err != nil {
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeFailed,
			string(rerr.CodeValidation),
			map[string]any{"collection": spec.Name, "error": err.Error()})
		rerr.WriteJSON(w, asValidationErr(err, "filter"))
		return
	}
	combined = combineFragments(combined, userFrag)

	sortKeys, err := filter.ParseSort(q.Get("sort"), spec)
	if err != nil {
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeFailed,
			string(rerr.CodeValidation),
			map[string]any{"collection": spec.Name, "error": err.Error()})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	q2, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeFailed,
			string(qErr.Code),
			map[string]any{"collection": spec.Name, "error": qErr.Message})
		rerr.WriteJSON(w, qErr)
		return
	}

	includeDeleted := parseBoolParam(q.Get("includeDeleted"))
	maxRows := defaultExportMaxRows

	selectSQL, selectArgs := buildExportSelect(spec, combined.Where, combined.Args, sortKeys, includeDeleted, maxRows+1)

	rows, err := q2.Query(r.Context(), selectSQL, selectArgs...)
	if err != nil {
		d.log.Error("rest: export query failed", "collection", spec.Name, "err", err)
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "query failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "export query failed"))
		return
	}
	defer rows.Close()

	// Sheet precedence: ?sheet= > config.Sheet > collection name.
	var cfgSheet string
	if spec.Exports.XLSX != nil {
		cfgSheet = spec.Exports.XLSX.Sheet
	}
	sheet := firstNonEmpty(q.Get("sheet"), cfgSheet, spec.Name)
	xw, err := export.NewXLSXWriter(sheet, cols)
	if err != nil {
		d.log.Error("rest: export writer init failed", "collection", spec.Name, "err", err)
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "writer init failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "export writer init failed"))
		return
	}
	defer xw.Discard()

	count := 0
	for rows.Next() {
		row, err := scanRow(rows, spec)
		if err != nil {
			d.log.Error("rest: export scan failed", "collection", spec.Name, "err", err)
			d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeError,
				string(rerr.CodeInternal),
				map[string]any{"collection": spec.Name, "error": "scan failed"})
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan failed"))
			return
		}
		count++
		if count > maxRows {
			// Selected $maxRows+1 to detect overflow. Sync handler
			// caps at maxRows so a single request's memory/time stays
			// bounded; bigger datasets need the async export path
			// (deferred to a follow-up slice).
			d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeFailed,
				string(rerr.CodeValidation),
				map[string]any{"collection": spec.Name, "error": "exceeds maxRows", "max_rows": maxRows})
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"export exceeds maxRows=%d; narrow your filter or use async export (POST /api/exports — coming soon)",
				maxRows))
			return
		}
		if err := xw.AppendRow(row); err != nil {
			d.log.Error("rest: export row write failed", "collection", spec.Name, "err", err)
			d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeError,
				string(rerr.CodeInternal),
				map[string]any{"collection": spec.Name, "error": "row write failed"})
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "row write failed"))
			return
		}
	}
	if err := rows.Err(); err != nil {
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "list iter failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list iter failed"))
		return
	}

	filename := fmt.Sprintf("%s-%s.xlsx", spec.Name, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	// X-Accel-Buffering off so reverse proxies don't buffer the multi-MB
	// body — same approach as v1.5.2 stream helpers.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := xw.Finish(w); err != nil {
		// Headers are already sent; best we can do is log.
		d.log.Error("rest: export flush failed", "collection", spec.Name, "err", err, "rows", count)
		d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "flush failed", "rows": count})
		return
	}
	d.emitExportAudit(r.Context(), r, "export.xlsx", audit.OutcomeSuccess, "",
		map[string]any{
			"collection": spec.Name,
			"rows":       xw.RowsWritten(),
			"columns":    len(cols),
			"filter":     q.Get("filter"),
		})
	d.log.Info("rest: export ok", "collection", spec.Name, "rows", xw.RowsWritten(), "columns", len(cols))
}

// exportPDFHandler implements `GET /api/collections/{name}/export.pdf`.
//
// Mirror of exportXLSXHandler — same RBAC (ListRule), same filter
// chain, same tenant story, same row cap. Output is an A4 table
// of the same columns the XLSX export would produce. Programmatic
// PDFs with custom layout / Markdown templates land in v1.6.2.
func (d *handlerDeps) exportPDFHandler(w http.ResponseWriter, r *http.Request) {
	collectionName := chi.URLParam(r, "name")
	spec, rerrSpec := resolveCollection(collectionName)
	if rerrSpec != nil {
		code, msg := classifyResolveErr(rerrSpec)
		d.emitExportAudit(r.Context(), r, "export.pdf", outcomeForCode(code),
			code,
			map[string]any{"collection": collectionName, "error": msg})
		rerr.WriteJSON(w, rerrSpec)
		return
	}
	if spec.Auth {
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeDenied,
			string(rerr.CodeForbidden),
			map[string]any{"collection": collectionName, "error": "auth collection"})
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"auth collection records are not exportable via /export.pdf"))
		return
	}

	q := r.URL.Query()

	// v1.6.3: pull cols + headers from .Export(ExportPDF(...)) when set.
	var cfgCols []string
	var cfgHeaders map[string]string
	if spec.Exports.PDF != nil {
		cfgCols = spec.Exports.PDF.Columns
		cfgHeaders = spec.Exports.PDF.Headers
	}
	cols, errEnv := resolveExportColumns(spec, q.Get("columns"), cfgCols, cfgHeaders)
	if errEnv != nil {
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeFailed,
			string(errEnv.Code),
			map[string]any{"collection": spec.Name, "error": errEnv.Message})
		rerr.WriteJSON(w, errEnv)
		return
	}

	principal := authmw.PrincipalFrom(r.Context())
	fctx := filterCtx(principal)

	startParam := 1
	combined := compiledFragment{}
	if spec.Tenant && tenant.HasID(r.Context()) {
		var tf compiledFragment
		tf, startParam = tenantFragment(tenant.ID(r.Context()).String(), startParam)
		combined = combineFragments(combined, tf)
	}
	ruleFrag, nextParam, err := compileRule(spec.Rules.List, spec, fctx, startParam)
	if err != nil {
		d.log.Error("rest: pdf export rule compile failed", "collection", spec.Name, "err", err)
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "rule compile failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}
	combined = combineFragments(combined, ruleFrag)
	userFrag, _, err := compileFilter(q.Get("filter"), spec, fctx, nextParam)
	if err != nil {
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeFailed,
			string(rerr.CodeValidation),
			map[string]any{"collection": spec.Name, "error": err.Error()})
		rerr.WriteJSON(w, asValidationErr(err, "filter"))
		return
	}
	combined = combineFragments(combined, userFrag)

	sortKeys, err := filter.ParseSort(q.Get("sort"), spec)
	if err != nil {
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeFailed,
			string(rerr.CodeValidation),
			map[string]any{"collection": spec.Name, "error": err.Error()})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	q2, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeFailed,
			string(qErr.Code),
			map[string]any{"collection": spec.Name, "error": qErr.Message})
		rerr.WriteJSON(w, qErr)
		return
	}

	includeDeleted := parseBoolParam(q.Get("includeDeleted"))
	// PDF is heavier per-row than XLSX (no streaming through to disk;
	// gopdf holds the whole document in memory until WriteTo). Drop
	// the cap to 10k rows for sync mode — async/job-backed PDF for
	// big datasets lands in a follow-up slice.
	maxRows := defaultExportMaxRows / 10

	// v1.6.4 branch: if the schema declared a Markdown template AND a
	// loader is wired, render the template instead of the data table.
	// The template gets the SAME filter-matched rows + same row cap,
	// so RBAC + tenant + soft-delete semantics are identical to the
	// data-table path. Falls back to the v1.6.1 data-table layout
	// when either is unset.
	templateName := ""
	if spec.Exports.PDF != nil {
		templateName = spec.Exports.PDF.Template
	}
	if templateName != "" && d.pdfTemplates != nil {
		d.renderPDFTemplate(w, r, spec, q2, templateName, combined, sortKeys, includeDeleted, maxRows)
		return
	}

	selectSQL, selectArgs := buildExportSelect(spec, combined.Where, combined.Args, sortKeys, includeDeleted, maxRows+1)

	rows, err := q2.Query(r.Context(), selectSQL, selectArgs...)
	if err != nil {
		d.log.Error("rest: pdf export query failed", "collection", spec.Name, "err", err)
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "query failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "export query failed"))
		return
	}
	defer rows.Close()

	// Doc-chrome precedence: ?param= > config.Param > default.
	var cfgTitle, cfgHeader, cfgFooter string
	if spec.Exports.PDF != nil {
		cfgTitle = spec.Exports.PDF.Title
		cfgHeader = spec.Exports.PDF.Header
		cfgFooter = spec.Exports.PDF.Footer
	}
	title := firstNonEmpty(q.Get("title"), cfgTitle, spec.Name)
	header := firstNonEmpty(q.Get("header"), cfgHeader)
	footer := firstNonEmpty(q.Get("footer"), cfgFooter)

	pdfCols := make([]export.PDFColumn, len(cols))
	for i, c := range cols {
		// Width 0 → auto-distribute across the page.
		pdfCols[i] = export.PDFColumn{Key: c.Key, Header: c.Header}
	}
	pw, err := export.NewPDFWriter(export.PDFConfig{
		Title:  title,
		Header: header,
		Footer: footer,
	}, pdfCols)
	if err != nil {
		d.log.Error("rest: pdf writer init failed", "collection", spec.Name, "err", err)
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "writer init failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "pdf writer init failed"))
		return
	}
	defer pw.Discard()

	count := 0
	for rows.Next() {
		row, err := scanRow(rows, spec)
		if err != nil {
			d.log.Error("rest: pdf export scan failed", "collection", spec.Name, "err", err)
			d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
				string(rerr.CodeInternal),
				map[string]any{"collection": spec.Name, "error": "scan failed"})
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan failed"))
			return
		}
		count++
		if count > maxRows {
			d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeFailed,
				string(rerr.CodeValidation),
				map[string]any{"collection": spec.Name, "error": "exceeds maxRows", "max_rows": maxRows})
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"PDF export exceeds maxRows=%d; narrow your filter or use async export (POST /api/exports — coming soon)",
				maxRows))
			return
		}
		if err := pw.AppendRow(row); err != nil {
			d.log.Error("rest: pdf row write failed", "collection", spec.Name, "err", err)
			d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
				string(rerr.CodeInternal),
				map[string]any{"collection": spec.Name, "error": "row write failed"})
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "row write failed"))
			return
		}
	}
	if err := rows.Err(); err != nil {
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "list iter failed"})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list iter failed"))
		return
	}

	filename := fmt.Sprintf("%s-%s.pdf", spec.Name, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := pw.Finish(w); err != nil {
		d.log.Error("rest: pdf flush failed", "collection", spec.Name, "err", err, "rows", count)
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "flush failed", "rows": count})
		return
	}
	d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeSuccess, "",
		map[string]any{
			"collection": spec.Name,
			"rows":       pw.RowsWritten(),
			"columns":    len(cols),
			"filter":     q.Get("filter"),
		})
	d.log.Info("rest: pdf export ok", "collection", spec.Name, "rows", pw.RowsWritten(), "columns", len(cols))
}

// resolveExportColumns expands the `?columns=` parameter into a
// slice of export.Column.
//
// Precedence: query param `raw` > config `cfgCols` > all readable.
// `cfgHeaders` provides per-column display label overrides; missing
// keys fall back to the column key (matching default behaviour).
// Unknown columns (anywhere) reject with a 400 envelope so a
// schema-declared `.Export(...)` with a typo is caught at the first
// request rather than silently rendering an empty column.
func resolveExportColumns(spec builder.CollectionSpec, raw string, cfgCols []string, cfgHeaders map[string]string) ([]export.Column, *rerr.Error) {
	all := allReadableColumns(spec)
	allowed := make(map[string]export.Column, len(all))
	for _, c := range all {
		allowed[c.Key] = c
	}

	// Decide which column keys we're going to render. Query wins; then
	// config; then everything readable.
	var keys []string
	switch {
	case raw != "":
		for _, p := range strings.Split(raw, ",") {
			k := strings.TrimSpace(p)
			if k == "" {
				continue
			}
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			return nil, rerr.New(rerr.CodeValidation, "columns: empty after parse")
		}
	case len(cfgCols) > 0:
		keys = cfgCols
	default:
		out := make([]export.Column, len(all))
		copy(out, all)
		applyHeaders(out, cfgHeaders)
		return out, nil
	}

	out := make([]export.Column, 0, len(keys))
	for _, k := range keys {
		c, ok := allowed[k]
		if !ok {
			return nil, rerr.New(rerr.CodeValidation,
				"unknown export column %q (allowed: %s)",
				k, strings.Join(readableColumnNames(spec), ", "))
		}
		out = append(out, c)
	}
	applyHeaders(out, cfgHeaders)
	return out, nil
}

// applyHeaders mutates `cols` so each entry's Header reflects the
// schema-declared display label when one is set; otherwise the
// header stays as the column key.
func applyHeaders(cols []export.Column, headers map[string]string) {
	if len(headers) == 0 {
		return
	}
	for i := range cols {
		if label, ok := headers[cols[i].Key]; ok && label != "" {
			cols[i].Header = label
		}
	}
}

// applyFormats mutates `cols` so each entry's Format reflects the
// schema-declared Excel number-format code from XLSXExportConfig.Format.
// Missing keys leave the column unformatted (cells render verbatim,
// the pre-DSL-3 behaviour). DSL-3 — closes the loop on the
// previously-stored-but-ignored Format map.
func applyFormats(cols []export.Column, formats map[string]string) {
	if len(formats) == 0 {
		return
	}
	for i := range cols {
		if code, ok := formats[cols[i].Key]; ok && code != "" {
			cols[i].Format = code
		}
	}
}

// classifyResolveErr decomposes an error returned from
// resolveCollection (which is a *rerr.Error wearing the error
// interface) into its code + message so callers can stash both into
// audit metadata. Falls back to a generic internal classification
// when the underlying type isn't *rerr.Error.
func classifyResolveErr(err error) (string, string) {
	var re *rerr.Error
	if errors.As(err, &re) {
		return string(re.Code), re.Message
	}
	return string(rerr.CodeInternal), err.Error()
}

// outcomeForCode maps an error code (returned by classifyResolveErr or
// extracted from a rerr.Error) to the audit Outcome that best
// describes the request. RBAC/auth refusals go to OutcomeDenied so
// operators can grep for access violations; everything else lands as
// OutcomeFailed.
func outcomeForCode(code string) audit.Outcome {
	switch rerr.Code(code) {
	case rerr.CodeForbidden, rerr.CodeUnauthorized:
		return audit.OutcomeDenied
	}
	return audit.OutcomeFailed
}

// emitExportAudit writes one row to the audit log for an export
// operation. nil-safe: if the handler was Mounted without an audit
// Writer, this is a no-op. Failures are swallowed (fire-and-forget,
// same convention as the auth audit hook) so a transient audit
// outage never corrupts an export response.
//
// `event` is the dotted name ("export.xlsx" / "export.pdf" /
// "export.enqueue" / "export.complete" / "export.fail"). `after`
// carries the structured metadata (collection, row count, format,
// filter, file size, error string) the operator will grep for.
func (d *handlerDeps) emitExportAudit(ctx context.Context, r *http.Request, event string, outcome audit.Outcome, errCode string, after map[string]any) {
	if d == nil || d.audit == nil {
		return
	}
	principal := authmw.PrincipalFrom(r.Context())
	tenantID := [16]byte{}
	if tenant.HasID(r.Context()) {
		tenantID = tenant.ID(r.Context())
	}
	_, _ = d.audit.Write(ctx, audit.Event{
		UserID:         principal.UserID,
		UserCollection: principal.CollectionName,
		TenantID:       tenantID,
		Event:          event,
		Outcome:        outcome,
		After:          after,
		ErrorCode:      errCode,
		IP:             r.RemoteAddr,
		UserAgent:      r.UserAgent(),
	})
}

// firstNonEmpty returns the first non-empty string from a list — for
// applying query > config > default precedence on simple string
// fields like Sheet / Title / Header / Footer.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// allReadableColumns returns the default column set: system fields,
// then user fields in declaration order. File / password fields
// are filtered out via the same readable-column rule as record JSON.
func allReadableColumns(spec builder.CollectionSpec) []export.Column {
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
	for _, f := range recordOutFields(spec) {
		// Skip file fields — the cell would carry only the filename
		// (without the signed URL) and operators expect either the
		// URL or a download. Mailbox attachments live in async export.
		switch f.Type {
		case builder.TypeFile, builder.TypeFiles:
			continue
		}
		cols = append(cols, export.Column{Key: f.Name, Header: f.Name})
	}
	return cols
}

// readableColumnNames is the same key list as allReadableColumns but
// just the string keys — used in error messages.
func readableColumnNames(spec builder.CollectionSpec) []string {
	cols := allReadableColumns(spec)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Key
	}
	return names
}

// buildExportSelect emits the SELECT for the export query. Unlike
// buildList it has no LIMIT/OFFSET (we want every matching row), but
// we DO pass a `cap` arg used as a `LIMIT cap` to detect when the
// caller is asking for more than the sync handler will serve.
func buildExportSelect(spec builder.CollectionSpec, where string, whereArgs []any, sort []filter.SortKey, includeDeleted bool, cap int) (string, []any) {
	whereSQL := ""
	if spec.SoftDelete && !includeDeleted {
		if where != "" {
			whereSQL = " WHERE deleted IS NULL AND " + where
		} else {
			whereSQL = " WHERE deleted IS NULL"
		}
	} else if where != "" {
		whereSQL = " WHERE " + where
	}
	orderSQL := filter.JoinSQL(sort)
	if orderSQL == "" {
		orderSQL = "created DESC, id DESC"
	}
	limitN := len(whereArgs) + 1
	args := append(append([]any{}, whereArgs...), cap)
	return fmt.Sprintf(
		"SELECT %s FROM %s%s ORDER BY %s LIMIT $%d",
		strings.Join(buildSelectColumns(spec), ", "),
		spec.Name,
		whereSQL,
		orderSQL,
		limitN,
	), args
}

// renderPDFTemplate is the v1.6.4 template-driven PDF render path.
// Called only when the spec declares `.Export(ExportPDF{Template: "..."})`
// AND the app wired a non-nil PDFTemplates loader at Mount time.
//
// Layout of the template context (the `.` dot inside text/template):
//
//	.Records — []map[string]any of filter-matched rows
//	.Tenant  — tenant ID string ("" when not tenant-scoped)
//	.Now     — time.Time at request time (UTC)
//	.Filter  — raw filter expression ("" when none)
//
// All the RBAC / tenant / soft-delete / row-cap semantics of the
// data-table path apply here too — same SQL, just a different sink.
func (d *handlerDeps) renderPDFTemplate(
	w http.ResponseWriter,
	r *http.Request,
	spec builder.CollectionSpec,
	q pgQuerier,
	templateName string,
	combined compiledFragment,
	sortKeys []filter.SortKey,
	includeDeleted bool,
	maxRows int,
) {
	selectSQL, selectArgs := buildExportSelect(spec, combined.Where, combined.Args, sortKeys, includeDeleted, maxRows+1)

	rows, err := q.Query(r.Context(), selectSQL, selectArgs...)
	if err != nil {
		d.log.Error("rest: pdf template query failed", "collection", spec.Name, "err", err)
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "query failed", "template": templateName})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "export query failed"))
		return
	}
	defer rows.Close()

	records := make([]map[string]any, 0, 128)
	for rows.Next() {
		row, err := scanRow(rows, spec)
		if err != nil {
			d.log.Error("rest: pdf template scan failed", "collection", spec.Name, "err", err)
			d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
				string(rerr.CodeInternal),
				map[string]any{"collection": spec.Name, "error": "scan failed", "template": templateName})
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan failed"))
			return
		}
		if len(records) >= maxRows {
			d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeFailed,
				string(rerr.CodeValidation),
				map[string]any{"collection": spec.Name, "error": "exceeds maxRows", "max_rows": maxRows, "template": templateName})
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"PDF export exceeds maxRows=%d; narrow your filter or use async export (POST /api/exports — coming soon)",
				maxRows))
			return
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "list iter failed", "template": templateName})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list iter failed"))
		return
	}

	tenantID := ""
	if spec.Tenant && tenant.HasID(r.Context()) {
		tenantID = tenant.ID(r.Context()).String()
	}

	// Convention: the dot is a struct (not a map) so authors can
	// write `{{ .Records }}` / `{{ .Now }}` without quotes. Maps
	// would force `{{ index . "Records" }}`.
	ctx := struct {
		Records []map[string]any
		Tenant  string
		Now     time.Time
		Filter  string
	}{
		Records: records,
		Tenant:  tenantID,
		Now:     time.Now().UTC(),
		Filter:  r.URL.Query().Get("filter"),
	}

	out, err := d.pdfTemplates.Render(templateName, ctx)
	if err != nil {
		// Distinguish "template not found" from "template execution
		// failed" so operators get actionable errors. ErrTemplateNotFound
		// surfaces as 404; everything else (parse/exec error) as 500.
		if errors.Is(err, export.ErrTemplateNotFound) {
			d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeFailed,
				string(rerr.CodeNotFound),
				map[string]any{"collection": spec.Name, "error": "template not found", "template": templateName})
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound,
				"pdf template %q not found", templateName))
			return
		}
		d.log.Error("rest: pdf template render failed", "collection", spec.Name, "template", templateName, "err", err)
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "template render failed", "template": templateName})
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "pdf template render failed"))
		return
	}

	filename := fmt.Sprintf("%s-%s.pdf", spec.Name, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(out); err != nil {
		d.log.Error("rest: pdf template flush failed", "collection", spec.Name, "err", err, "rows", len(records))
		d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeError,
			string(rerr.CodeInternal),
			map[string]any{"collection": spec.Name, "error": "flush failed", "template": templateName, "rows": len(records)})
		return
	}
	d.emitExportAudit(r.Context(), r, "export.pdf", audit.OutcomeSuccess, "",
		map[string]any{
			"collection": spec.Name,
			"rows":       len(records),
			"template":   templateName,
			"filter":     r.URL.Query().Get("filter"),
		})
	d.log.Info("rest: pdf template export ok",
		"collection", spec.Name,
		"template", templateName,
		"records", len(records))
}
