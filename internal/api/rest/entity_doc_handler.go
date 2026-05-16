package rest

// FEEDBACK #29 — per-entity PDF document handler.
//
// Schema-declarative: `.EntityDoc(builder.EntityDocConfig{...})` on a
// collection registers GET /api/collections/{name}/{id}/{doc}.pdf.
//
// The handler:
//   1. Resolves the parent collection (404 if not registered).
//   2. Loads the row by id through the SAME read pipeline as the
//      regular GET-one endpoint — ViewRule, tenant scoping, and
//      soft-delete filtering all apply. An owner-only invoice rule
//      (`@request.auth.id = customer`) returns 404 to non-owners,
//      matching the existence-hiding behaviour of /api/collections/
//      {name}/records/{id}.
//   3. Runs each declared Related query (one per child collection),
//      producing []map[string]any.
//   4. Builds a struct context (.Record, .Related, .Now, .Tenant).
//   5. Renders cfg.Template via the same PDFTemplates engine used by
//      .Export()'s template path.
//
// All Related queries use parameterised SQL — child rows are fetched
// with WHERE <child_col> = $1, $1 bound to the parent row's value.
// No template-string interpolation into SQL; the DSL is the only
// surface that can name a column. SQL injection isn't reachable from
// HTTP input.
//
// Pre-v1.7.50 history: the parent-row fetch was a bare
// `SELECT * FROM <table> WHERE id = $1` — no rule compilation, no
// tenant filter, no soft-delete check. That was a real RBAC bypass
// (anyone with a UUID could pull the rendered PDF). The doc-comment
// also lied about applying ViewRule. Both fixed together: the handler
// now delegates to composeRowExtras + buildViewOpts + queryFor +
// scanRow, sharing one code path with viewHandler.

import (
	stdctx "context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// MountEntityDocs walks the registry and registers one route per
// (collection, EntityDoc) pair. Call from Mount after the standard
// routes are mounted so the more-specific path wins chi's mux ordering.
func MountEntityDocs(r chi.Router, d *handlerDeps) {
	for _, c := range registry.All() {
		if c == nil {
			continue
		}
		spec := c.Spec()
		for _, doc := range spec.EntityDocs {
			// Capture loop variables.
			collName := spec.Name
			docCfg := doc
			route := fmt.Sprintf("/api/collections/%s/{id}/%s.pdf", collName, docCfg.Name)
			r.Get(route, func(w http.ResponseWriter, req *http.Request) {
				d.entityDocPDFHandler(w, req, collName, docCfg)
			})
		}
	}
}

// entityDocPDFHandler renders one per-entity document.
func (d *handlerDeps) entityDocPDFHandler(w http.ResponseWriter, r *http.Request, collName string, doc builder.EntityDocConfig) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
		return
	}

	c := registry.Get(collName)
	if c == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "collection %q not found", collName))
		return
	}
	spec := c.Spec()
	// Load the parent row through the same read pipeline as viewHandler
	// so ViewRule + tenant scoping + soft-delete filtering apply
	// uniformly. We pass the *Request rather than just the context so
	// filterCtx can read the authenticated principal.
	parentRow, lookupErr := d.loadEntityDocParent(r, spec, id)
	if lookupErr != nil {
		rerr.WriteJSON(w, lookupErr)
		return
	}

	// Run each Related query.
	related := make(map[string][]map[string]any, len(doc.Related))
	for key, spec := range doc.Related {
		rows, err := d.loadEntityDocRelated(ctx, spec, parentRow)
		if err != nil {
			rerr.WriteJSON(w, err)
			return
		}
		related[key] = rows
	}

	// Build template context.
	tenantID := ""
	if v, ok := parentRow["tenant_id"]; ok && v != nil {
		tenantID = fmt.Sprint(v)
	}
	tplCtx := struct {
		Record  map[string]any
		Related map[string][]map[string]any
		Tenant  string
		Now     time.Time
	}{
		Record:  parentRow,
		Related: related,
		Tenant:  tenantID,
		Now:     time.Now().UTC(),
	}

	if d.pdfTemplates == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
			"pdf templates not initialised (no pb_data/pdf_templates directory configured)"))
		return
	}
	out, err := d.pdfTemplates.Render(doc.Template, tplCtx)
	if err != nil {
		if errors.Is(err, export.ErrTemplateNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound,
				"pdf template %q not found in pb_data/pdf_templates", doc.Template))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "render entity-doc template"))
		return
	}

	// Filename: <collection>-<id-short>-<docname>-<timestamp>.pdf.
	short := id.String()
	if len(short) > 8 {
		short = short[:8]
	}
	filename := fmt.Sprintf("%s-%s-%s-%s.pdf",
		collName, short, doc.Name, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// loadEntityDocParent fetches the parent row by id, going through the
// same compose/build/queryFor/scan chain as viewHandler so the entity-
// doc surface inherits ViewRule + tenant scope + soft-delete filtering
// without divergence.
//
// A row that exists but is filtered out by the ViewRule returns
// CodeNotFound — same existence-hiding contract as the regular
// /api/collections/{name}/records/{id} read path. We deliberately do
// not 403 here.
func (d *handlerDeps) loadEntityDocParent(r *http.Request, spec builder.CollectionSpec, id uuid.UUID) (map[string]any, *rerr.Error) {
	ctx := r.Context()
	fctx := filterCtx(authmw.PrincipalFrom(ctx))

	// $1 is the row id (see buildViewOpts); rule placeholders start at $2.
	extras, err := composeRowExtras(ctx, spec, fctx, spec.Rules.View, 2)
	if err != nil {
		d.log.Error("rest: entity-doc view rule compile failed",
			"collection", spec.Name, "err", err)
		return nil, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed: %v", err)
	}
	q, qErr := d.queryFor(ctx, spec)
	if qErr != nil {
		return nil, qErr
	}
	sql, args := buildViewOpts(spec, id.String(), extras.Where, extras.Args, false)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		if isInvalidUUID(err) {
			return nil, rerr.New(rerr.CodeNotFound, "record not found")
		}
		d.log.Error("rest: entity-doc parent query failed",
			"collection", spec.Name, "err", err)
		return nil, rerr.Wrap(err, rerr.CodeInternal, "load parent row")
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rerr.New(rerr.CodeNotFound, "record not found")
	}
	row, err := scanRow(rows, spec)
	if err != nil {
		return nil, rerr.Wrap(err, rerr.CodeInternal, "scan parent row")
	}
	return row, nil
}

// loadEntityDocRelated runs one Related query against the child
// collection. Returns []map[colname]value.
func (d *handlerDeps) loadEntityDocRelated(ctx stdctx.Context, spec builder.RelatedSpec, parent map[string]any) ([]map[string]any, *rerr.Error) {
	if spec.Collection == "" || spec.ChildColumn == "" {
		return nil, rerr.New(rerr.CodeValidation, "entity-doc related: Collection and ChildColumn are required")
	}
	parentCol := spec.ParentColumn
	if parentCol == "" {
		parentCol = "id"
	}
	parentVal, ok := parent[parentCol]
	if !ok {
		return nil, rerr.New(rerr.CodeInternal, "parent row missing column %q for related-spec", parentCol)
	}
	// Identifier-quote the child collection + child column. Both came
	// from a builder spec, registry-validated, so a malicious payload
	// can't reach this point.
	childTable := `"` + strings.ReplaceAll(spec.Collection, `"`, `""`) + `"`
	childCol := `"` + strings.ReplaceAll(spec.ChildColumn, `"`, `""`) + `"`
	q := fmt.Sprintf("SELECT * FROM %s WHERE %s = $1", childTable, childCol)
	if spec.OrderBy != "" {
		// OrderBy is operator-controlled (schema DSL), not user-controlled.
		// Still defend with an identifier-character allow list.
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == ',' || r == '_' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, spec.OrderBy)
		if clean != "" {
			q += " ORDER BY " + clean
		}
	}
	limit := spec.Limit
	if limit <= 0 {
		limit = 1000
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := d.pool.Query(ctx, q, parentVal)
	if err != nil {
		return nil, rerr.Wrap(err, rerr.CodeInternal, "load related rows")
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 32)
	for rows.Next() {
		descs := rows.FieldDescriptions()
		values, err := rows.Values()
		if err != nil {
			return nil, rerr.Wrap(err, rerr.CodeInternal, "scan related row")
		}
		row := make(map[string]any, len(descs))
		for i, d := range descs {
			row[string(d.Name)] = values[i]
		}
		out = append(out, row)
	}
	return out, nil
}
