package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/hooks"
	"github.com/railbase/railbase/internal/i18n"
	"github.com/railbase/railbase/internal/realtime"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/tenant"
)

// queryFor returns the pgQuerier the handler should use for spec.
// For tenant-scoped collections we MUST use the per-request connection
// the tenant middleware acquired (it has `railbase.tenant` set so RLS
// passes); otherwise the pool itself is fine.
//
// Returns an error envelope when the request is missing the tenant
// context for a tenant collection — caller writes it and returns.
func (d *handlerDeps) queryFor(ctx context.Context, spec builder.CollectionSpec) (pgQuerier, *rerr.Error) {
	if !spec.Tenant {
		return d.pool, nil
	}
	if !tenant.HasID(ctx) {
		return nil, rerr.New(rerr.CodeValidation,
			"collection %q is tenant-scoped; X-Tenant header is required",
			spec.Name)
	}
	conn := tenant.Conn(ctx)
	if conn == nil {
		// Should never happen — middleware always pairs ID with conn.
		return nil, rerr.New(rerr.CodeInternal,
			"tenant context attached without connection (middleware bug)")
	}
	return conn, nil
}

// tenantInsertExtras returns the column → value pair forced into
// every INSERT on a tenant collection so the row gets the right
// tenant_id without trusting client input. Empty when spec is not
// tenant-scoped.
func tenantInsertExtras(ctx context.Context, spec builder.CollectionSpec) map[string]any {
	if !spec.Tenant {
		return nil
	}
	id := tenant.ID(ctx)
	if id == [16]byte{} {
		return nil
	}
	return map[string]any{"tenant_id": id.String()}
}

// asValidationErr maps a parse / sort error into the canonical
// envelope. PositionedError carries the byte offset; surface it in
// `details` so editor-style clients can underline the bad span.
func asValidationErr(err error, paramName string) *rerr.Error {
	e := rerr.New(rerr.CodeValidation, "invalid %s: %s", paramName, err.Error())
	var pe *filter.PositionedError
	if errors.As(err, &pe) {
		e = e.WithDetail("position", pe.Position)
	}
	return e
}

// pgQuerier is the slice of pgxpool we depend on. Keeps the package
// testable — fakes implement the interface without dragging in a
// running Postgres.
//
// Begin returns a pgx.Tx that itself implements pgQuerier — handlers
// that need transactional grouping (v1.4.13 batch ops) begin a tx
// off whatever queryFor returned and pass the tx through to
// buildInsert/buildUpdate/buildDelete unchanged.
type pgQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// handlerDeps is what every CRUD handler needs. Built once per Mount
// and threaded through closures rather than re-resolved per request.
type handlerDeps struct {
	pool         pgQuerier
	log          *slog.Logger
	hooks        *hooks.Runtime       // nil when no hooks runtime is wired
	bus          *eventbus.Bus        // nil → no realtime publish
	filesDeps    *FilesDeps           // nil → file fields render as raw filename
	pdfTemplates *export.PDFTemplates // nil → PDFConfig.Template is ignored, handler falls back to data-table layout
	audit        *audit.Writer        // nil → export handlers skip audit emission (silent no-op)
}

// recordURLFn builds the per-record fileURLFunc passed into
// marshalRecord. When filesDeps is unwired returns nil — marshalRecord
// handles that gracefully (emits filename only, no signed URL).
func (d *handlerDeps) recordURLFn(spec builder.CollectionSpec, row map[string]any) fileURLFunc {
	if d.filesDeps == nil || d.filesDeps.Signer == nil {
		return nil
	}
	id, _ := row["id"].(string)
	if id == "" {
		return nil
	}
	// v1.x — pull TTL live per signer-builder invocation. Each call to
	// this builder happens during a single response render, so reading
	// it once at the top is fine (avoids divergence between siblings
	// in the same JSON payload). New requests get the fresh value.
	var ttl time.Duration
	if d.filesDeps != nil && d.filesDeps.URLTTL != nil {
		ttl = d.filesDeps.URLTTL()
	}
	if ttl == 0 {
		ttl = 5 * 60 * 1_000_000_000 // 5 min as time.Duration value
	}
	signer := d.filesDeps.Signer
	collection := spec.Name
	return func(field, filename string) string {
		return signedFileURL(signer, collection, id, field, filename, ttl)
	}
}

// requestLocaleFor returns the locale to use when marshalling records
// of `spec`. Empty when the spec has no Translatable fields (skip
// locale-picking entirely so the wire shape stays untouched for
// non-translatable collections) OR when the request didn't stamp a
// locale via the i18n middleware. The Translatable read path uses
// the empty-locale case to emit the FULL locale map — useful for
// admin UI / export consumers that want every translation.
func requestLocaleFor(ctx context.Context, spec builder.CollectionSpec) i18n.Locale {
	hasTranslatable := false
	for _, f := range spec.Fields {
		if f.Translatable {
			hasTranslatable = true
			break
		}
	}
	if !hasTranslatable {
		return ""
	}
	return i18n.FromContext(ctx)
}

// resolveCollection looks up the collection from the URL path. We
// re-read from the registry on every request rather than caching at
// Mount time — keeps the path open for hot-reload of schema in v1.
func resolveCollection(name string) (builder.CollectionSpec, error) {
	c := registry.Get(name)
	if c == nil {
		return builder.CollectionSpec{}, rerr.New(rerr.CodeNotFound,
			"collection %q not found", name)
	}
	spec := c.Spec()
	if spec.Auth {
		// Auth collections own NOT NULL system columns (password_hash,
		// token_key) that the generic CRUD writer can't fill safely.
		// Use the dedicated /auth-signup / /auth-with-password / /me
		// endpoints to mutate or read auth records.
		return spec, rerr.New(rerr.CodeForbidden,
			"collection %q is an auth collection; use /api/collections/%s/auth-* endpoints",
			name, name)
	}
	return spec, nil
}

// listHandler implements GET /api/collections/{name}/records.
//
// Response envelope (PB-compat):
//
//	{
//	  "page": 1, "perPage": 30,
//	  "totalItems": 42, "totalPages": 2,
//	  "items": [ {record}, {record}, ... ]
//	}
//
// Query params handled in v0.3.3:
//   - page, perPage (offset pagination, default 30, max 500)
//   - filter (parameterized via internal/filter)
//   - sort   (signed comma-separated field list, default `-created,-id`)
// Deferred to v0.3.4 / v1: expand, fields.
func (d *handlerDeps) listHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(q.Get("perPage"))
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}

	for _, unsupported := range []string{"expand", "fields"} {
		if q.Get(unsupported) != "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"query parameter %q not yet supported (v0.3.4 / v1)", unsupported))
			return
		}
	}

	principal := authmw.PrincipalFrom(r.Context())
	fctx := filterCtx(principal)

	// Compose order: tenant scope (if applicable) → rule → user filter.
	// Each compile call starts at the param index left by the previous
	// step so $N counts stay sequential through the final WHERE.
	startParam := 1
	combined := compiledFragment{}
	if spec.Tenant && tenant.HasID(r.Context()) {
		var tf compiledFragment
		tf, startParam = tenantFragment(tenant.ID(r.Context()).String(), startParam)
		combined = combineFragments(combined, tf)
	}
	ruleFrag, nextParam, err := compileRule(spec.Rules.List, spec, fctx, startParam)
	if err != nil {
		d.log.Error("rest: list rule compile failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}
	combined = combineFragments(combined, ruleFrag)
	userFrag, _, err := compileFilter(q.Get("filter"), spec, fctx, nextParam)
	if err != nil {
		rerr.WriteJSON(w, asValidationErr(err, "filter"))
		return
	}
	combined = combineFragments(combined, userFrag)

	sortKeys, err := filter.ParseSort(q.Get("sort"), spec)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	q2, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		rerr.WriteJSON(w, qErr)
		return
	}

	selectSQL, selectArgs, countSQL, countArgs := buildList(spec, listQuery{
		page:      page,
		perPage:   perPage,
		where:     combined.Where,
		whereArgs: combined.Args,
		sort:      sortKeys,
		// Soft-delete: client opts in to seeing tombstones via
		// ?includeDeleted=true. Anything else (omitted, "false", "0")
		// excludes them. Lets trash UI and admin tools see deleted rows;
		// regular API consumers see a clean dataset by default.
		includeDeleted: parseBoolParam(q.Get("includeDeleted")),
	})

	var total int64
	if err := q2.QueryRow(r.Context(), countSQL, countArgs...).Scan(&total); err != nil {
		d.log.Error("rest: count failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count failed"))
		return
	}

	rows, err := q2.Query(r.Context(), selectSQL, selectArgs...)
	if err != nil {
		d.log.Error("rest: list query failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list query failed"))
		return
	}
	defer rows.Close()

	items := make([]json.RawMessage, 0)
	for rows.Next() {
		row, err := scanRow(rows, spec)
		if err != nil {
			d.log.Error("rest: scan failed", "collection", spec.Name, "err", err)
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan failed"))
			return
		}
		buf, err := marshalRecordLoc(spec, row, d.recordURLFn(spec, row), requestLocaleFor(r.Context(), spec))
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "marshal failed"))
			return
		}
		items = append(items, buf)
	}
	if err := rows.Err(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list iter failed"))
		return
	}

	totalPages := int64(0)
	if perPage > 0 {
		totalPages = (total + int64(perPage) - 1) / int64(perPage)
	}

	envelope := struct {
		Page       int               `json:"page"`
		PerPage    int               `json:"perPage"`
		TotalItems int64             `json:"totalItems"`
		TotalPages int64             `json:"totalPages"`
		Items      []json.RawMessage `json:"items"`
	}{
		Page:       page,
		PerPage:    perPage,
		TotalItems: total,
		TotalPages: totalPages,
		Items:      items,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(envelope)
}

// viewHandler implements GET /api/collections/{name}/records/{id}.
func (d *handlerDeps) viewHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	fctx := filterCtx(authmw.PrincipalFrom(r.Context()))

	// $1 is the row id. Then optional tenant fragment at $2, then
	// rule placeholders. composeRowExtras handles the chaining.
	extras, err := composeRowExtras(r.Context(), spec, fctx, spec.Rules.View, 2)
	if err != nil {
		d.log.Error("rest: view rule compile failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}
	q, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		rerr.WriteJSON(w, qErr)
		return
	}
	includeDeleted := parseBoolParam(r.URL.Query().Get("includeDeleted"))
	sql, args := buildViewOpts(spec, id, extras.Where, extras.Args, includeDeleted)
	rows, err := q.Query(r.Context(), sql, args...)
	if err != nil {
		// pgx surfaces invalid UUID input as a CodeInvalidTextRepresentation
		// SQLSTATE — translate to 404 since the URL path was bad.
		if isInvalidUUID(err) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
			return
		}
		d.log.Error("rest: view query failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "view query failed"))
		return
	}
	defer rows.Close()

	if !rows.Next() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
		return
	}
	row, err := scanRow(rows, spec)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan failed"))
		return
	}
	buf, err := marshalRecordLoc(spec, row, d.recordURLFn(spec, row), requestLocaleFor(r.Context(), spec))
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "marshal failed"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// createHandler implements POST /api/collections/{name}/records.
// Body: bare JSON object of {field: value}. Returns the inserted row.
func (d *handlerDeps) createHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB ceiling
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "read body failed"))
		return
	}
	defer r.Body.Close()

	fields, perr := parseInput(spec, body, true)
	if perr != nil {
		e := rerr.New(rerr.CodeValidation, "%s", perr.Message)
		for k, v := range perr.Details {
			e = e.WithDetail(k, v)
		}
		rerr.WriteJSON(w, e)
		return
	}

	q, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		rerr.WriteJSON(w, qErr)
		return
	}

	// Force tenant_id from context — never trust client input. The
	// schema generator already declared tenant_id NOT NULL on tenant
	// collections, so leaving it out triggers a 400 the user can
	// understand.
	for k, v := range tenantInsertExtras(r.Context(), spec) {
		fields[k] = v
	}

	// v1.2.0 hook dispatch: BeforeCreate handlers can mutate `fields`
	// before we build the INSERT. A throw aborts the request with 400.
	if d.hooks != nil && d.hooks.HasHandlers(spec.Name, hooks.EventRecordBeforeCreate) {
		evt, err := d.hooks.Dispatch(r.Context(), spec.Name, hooks.EventRecordBeforeCreate, fields)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
			return
		}
		fields = evt.Record()
	}

	// CreateRule enforcement is transactional: INSERT the row, evaluate
	// the compiled Create rule against it, and COMMIT only if it passes.
	// A failing rule ROLLBACKs — the row never becomes visible. This is
	// race-free (single tx) and has no bypass path: an empty Create rule
	// compiles to constant-false (see compileRule), so a collection with
	// no Create rule rejects every public insert.
	tx, err := q.Begin(r.Context())
	if err != nil {
		d.log.Error("rest: begin tx failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "insert failed"))
		return
	}
	// Rollback is a no-op once the tx is committed, so it's safe to
	// always defer — it covers every early return below.
	defer func() { _ = tx.Rollback(r.Context()) }()

	// v1.5.12 AdjacencyList/Ordered preprocessing: cycle/depth check on
	// `parent` (if set), auto-assign `sort_index` (if omitted). Runs on
	// the tx so it reads a consistent snapshot.
	if spec.AdjacencyList || spec.Ordered {
		if err := hierarchyPreInsert(r.Context(), tx, spec, fields); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
			return
		}
	}

	sql, args, err := buildInsert(spec, fields)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	rows, err := tx.Query(r.Context(), sql, args...)
	if err != nil {
		if pgErr := pgErrorFor(err); pgErr != nil {
			rerr.WriteJSON(w, pgErr)
			return
		}
		d.log.Error("rest: insert failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "insert failed"))
		return
	}

	if !rows.Next() {
		// Server-side INSERT failures (CHECK violations, enum mismatches)
		// surface as rows.Err() after Next() returns false — pgx defers
		// the protocol error until iteration. Drain Err and classify.
		iterErr := rows.Err()
		rows.Close()
		if iterErr != nil {
			if pgErr := pgErrorFor(iterErr); pgErr != nil {
				rerr.WriteJSON(w, pgErr)
				return
			}
			d.log.Error("rest: insert iter failed", "collection", spec.Name, "err", iterErr)
			rerr.WriteJSON(w, rerr.Wrap(iterErr, rerr.CodeInternal, "insert failed"))
			return
		}
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "insert returned no row"))
		return
	}
	row, err := scanRow(rows, spec)
	// pgx requires the result set be closed before the next query on the
	// same tx — close explicitly rather than deferring.
	rows.Close()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan failed"))
		return
	}

	// Evaluate the Create rule against the row we just inserted, inside
	// the same tx. $1 is the new row id; rule placeholders start at $2.
	// An empty rule compiled to "false" → EXISTS is false → 403, and the
	// deferred Rollback reverts the insert.
	fctx := filterCtx(authmw.PrincipalFrom(r.Context()))
	ruleFrag, _, err := compileRule(spec.Rules.Create, spec, fctx, 2)
	if err != nil {
		d.log.Error("rest: create rule compile failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}
	checkSQL := fmt.Sprintf(
		"SELECT EXISTS(SELECT 1 FROM %s WHERE id = $1 AND (%s))",
		spec.Name, ruleFrag.Where,
	)
	checkArgs := append([]any{row["id"]}, ruleFrag.Args...)
	var allowed bool
	if err := tx.QueryRow(r.Context(), checkSQL, checkArgs...).Scan(&allowed); err != nil {
		d.log.Error("rest: create rule check failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "insert failed"))
		return
	}
	if !allowed {
		// Deferred Rollback reverts the insert — the row never commits.
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"create not permitted by the collection's create rule"))
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		d.log.Error("rest: commit failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "insert failed"))
		return
	}

	// Post-commit only: hooks + realtime must observe a durable row.
	// v1.2.0 hook dispatch: AfterCreate. Throws are logged but DO NOT
	// undo the DB write — Dispatch() handles that.
	if d.hooks != nil {
		_, _ = d.hooks.Dispatch(r.Context(), spec.Name, hooks.EventRecordAfterCreate, row)
	}
	// v1.3.0 realtime publish: fire AFTER the hook (so hooks can still
	// mutate before subscribers see). nil-safe.
	d.publishRecord(r, spec, realtime.VerbCreate, row)
	// v3.x — auto-audit when CollectionSpec.Audit is on. No-op when
	// off. Emitted post-commit so a failed insert never leaves a
	// phantom audit row.
	emitRecordAudit(r, d.audit, spec, recordVerbCreated, recordIDFromRow(row), nil, row)
	buf, err := marshalRecordLoc(spec, row, d.recordURLFn(spec, row), requestLocaleFor(r.Context(), spec))
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "marshal failed"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// updateHandler implements PATCH /api/collections/{name}/records/{id}.
func (d *handlerDeps) updateHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	id := chi.URLParam(r, "id")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "read body failed"))
		return
	}
	defer r.Body.Close()

	// Update is partial — `create=false` skips the required-field check.
	fields, perr := parseInput(spec, body, false)
	if perr != nil {
		e := rerr.New(rerr.CodeValidation, "%s", perr.Message)
		for k, v := range perr.Details {
			e = e.WithDetail(k, v)
		}
		rerr.WriteJSON(w, e)
		return
	}

	fctx := filterCtx(authmw.PrincipalFrom(r.Context()))

	// v1.2.0 hook dispatch: BeforeUpdate.
	if d.hooks != nil && d.hooks.HasHandlers(spec.Name, hooks.EventRecordBeforeUpdate) {
		evt, err := d.hooks.Dispatch(r.Context(), spec.Name, hooks.EventRecordBeforeUpdate, fields)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
			return
		}
		fields = evt.Record()
	}

	// Drop server-owned columns (sequential_code) up front: buildUpdate
	// strips them anyway, so sizing the rule's placeholder offset off
	// len(fields) without stripping first would desync the $N numbering
	// (the rule would be numbered for columns that never hit the SET
	// clause). Idempotent — buildUpdate re-strips defensively.
	stripServerOwnedUpdateFields(spec, fields)

	// Update placeholders run: $1..$N for SET, $(N+1) for id, then
	// optional tenant fragment + rule. composeRowExtras chains them.
	extras, err := composeRowExtras(r.Context(), spec, fctx, spec.Rules.Update, len(fields)+2)
	if err != nil {
		d.log.Error("rest: update rule compile failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}

	q, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		rerr.WriteJSON(w, qErr)
		return
	}
	// v3.x Phase 1.5 — pre-image fetch for the v3 audit row. No-op
	// when spec.Audit is off (short-circuit inside the helper) so
	// non-audited collections pay zero cost. Best-effort: failure
	// just means the audit row gets before=nil — UPDATE still
	// proceeds. Reading BEFORE the UPDATE (not in the same tx)
	// means a concurrent writer could shift the actual pre-image
	// out from under us, but for audit purposes "what was visible
	// to this request at the start" is the honest record.
	auditBefore := fetchPreImage(r.Context(), q, spec, id)
	// v1.5.12 AdjacencyList cycle/depth check on UPDATE. No-op if
	// `parent` is not in the patch (no chain change).
	if spec.AdjacencyList {
		if err := hierarchyPreUpdate(r.Context(), q, spec, id, fields); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
			return
		}
	}
	sql, args, err := buildUpdate(spec, id, fields, extras.Where, extras.Args)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	rows, err := q.Query(r.Context(), sql, args...)
	if err != nil {
		if isInvalidUUID(err) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
			return
		}
		if pgErr := pgErrorFor(err); pgErr != nil {
			rerr.WriteJSON(w, pgErr)
			return
		}
		d.log.Error("rest: update failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update failed"))
		return
	}
	defer rows.Close()

	if !rows.Next() {
		// UPDATE…RETURNING: server-side failures (CHECK, FK, enum)
		// are deferred to rows.Err() the same way INSERT does. Branch
		// on err vs no-row first so 0-row simply maps to 404.
		if iterErr := rows.Err(); iterErr != nil {
			if pgErr := pgErrorFor(iterErr); pgErr != nil {
				rerr.WriteJSON(w, pgErr)
				return
			}
			d.log.Error("rest: update iter failed", "collection", spec.Name, "err", iterErr)
			rerr.WriteJSON(w, rerr.Wrap(iterErr, rerr.CodeInternal, "update failed"))
			return
		}
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
		return
	}
	row, err := scanRow(rows, spec)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan failed"))
		return
	}
	// v1.2.0 hook dispatch: AfterUpdate.
	if d.hooks != nil {
		_, _ = d.hooks.Dispatch(r.Context(), spec.Name, hooks.EventRecordAfterUpdate, row)
	}
	d.publishRecord(r, spec, realtime.VerbUpdate, row)
	// v3.x — auto-audit with real before/after diff. auditBefore
	// was captured before the UPDATE; row is the post-image.
	emitRecordAudit(r, d.audit, spec, recordVerbUpdated, recordIDFromRow(row), auditBefore, row)
	buf, err := marshalRecordLoc(spec, row, d.recordURLFn(spec, row), requestLocaleFor(r.Context(), spec))
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "marshal failed"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// deleteHandler implements DELETE /api/collections/{name}/records/{id}.
// Empty 204 on success — PB returns 204 No Content here.
func (d *handlerDeps) deleteHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	fctx := filterCtx(authmw.PrincipalFrom(r.Context()))

	extras, err := composeRowExtras(r.Context(), spec, fctx, spec.Rules.Delete, 2)
	if err != nil {
		d.log.Error("rest: delete rule compile failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}

	// v1.2.0 hook dispatch: BeforeDelete. The event carries just the
	// id — handlers that want pre-delete state should query for it.
	// (A v1.2.x enhancement could fetch+pass the row, at the cost of
	// one extra SELECT on every delete.)
	if d.hooks != nil && d.hooks.HasHandlers(spec.Name, hooks.EventRecordBeforeDelete) {
		evt, err := d.hooks.Dispatch(r.Context(), spec.Name, hooks.EventRecordBeforeDelete,
			map[string]any{"id": id})
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
			return
		}
		_ = evt
	}

	q, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		rerr.WriteJSON(w, qErr)
		return
	}
	// v3.x Phase 1.5 — pre-image fetch for the v3 audit row. Same
	// best-effort policy as updateHandler. Captured BEFORE the
	// DELETE so the audit row records what was removed.
	auditBefore := fetchPreImage(r.Context(), q, spec, id)
	sql, args := buildDelete(spec, id, extras.Where, extras.Args)
	var returned string
	err = q.QueryRow(r.Context(), sql, args...).Scan(&returned)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
			return
		}
		if isInvalidUUID(err) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
			return
		}
		if pgErr := pgErrorFor(err); pgErr != nil {
			rerr.WriteJSON(w, pgErr)
			return
		}
		d.log.Error("rest: delete failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete failed"))
		return
	}
	// v1.2.0 hook dispatch: AfterDelete. Carries the same id payload.
	if d.hooks != nil {
		_, _ = d.hooks.Dispatch(r.Context(), spec.Name, hooks.EventRecordAfterDelete,
			map[string]any{"id": id})
	}
	// v1.3.0 realtime publish: delete event carries id only (no body
	// after deletion). Subscribers correlate by id.
	d.publishRecord(r, spec, realtime.VerbDelete, map[string]any{"id": id})
	// v3.x — auto-audit with the captured pre-image. after is nil
	// per delete semantics (the row is gone). entity_id carries the
	// deleted id so the Timeline entity filter still hits.
	emitRecordAudit(r, d.audit, spec, recordVerbDeleted, id, auditBefore, nil)
	w.WriteHeader(http.StatusNoContent)
}

// restoreHandler implements POST /api/collections/{name}/records/{id}/restore.
// Only valid on collections declared `.SoftDelete()`; returns 404 on
// regular collections (the route still exists, but the row will never
// match "deleted IS NOT NULL"). The UpdateRule applies — restoring is
// treated as a mutation, so callers need write access.
func (d *handlerDeps) restoreHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	if !spec.SoftDelete {
		// 404 keeps the response shape identical to "row not found",
		// so naive clients calling /restore on a hard-delete collection
		// don't get a special error class to surface.
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
		return
	}
	id := chi.URLParam(r, "id")
	fctx := filterCtx(authmw.PrincipalFrom(r.Context()))

	// Restore is a mutation — reuse the UpdateRule.
	extras, err := composeRowExtras(r.Context(), spec, fctx, spec.Rules.Update, 2)
	if err != nil {
		d.log.Error("rest: restore rule compile failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rule compile failed"))
		return
	}
	q, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		rerr.WriteJSON(w, qErr)
		return
	}
	sql, args := buildRestore(spec, id, extras.Where, extras.Args)
	rows, err := q.Query(r.Context(), sql, args...)
	if err != nil {
		if isInvalidUUID(err) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
			return
		}
		if pgErr := pgErrorFor(err); pgErr != nil {
			rerr.WriteJSON(w, pgErr)
			return
		}
		d.log.Error("rest: restore failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "restore failed"))
		return
	}
	defer rows.Close()
	if !rows.Next() {
		// No row matched id = $1 AND deleted IS NOT NULL — either the
		// row never existed or it wasn't tombstoned. Both = 404.
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "record not found"))
		return
	}
	row, err := scanRow(rows, spec)
	if err != nil {
		d.log.Error("rest: restore scan failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "restore failed"))
		return
	}
	// Treat restore as an update for downstream consumers — realtime +
	// hooks see the same after-update shape they'd see from a PATCH.
	if d.hooks != nil {
		_, _ = d.hooks.Dispatch(r.Context(), spec.Name, hooks.EventRecordAfterUpdate, row)
	}
	d.publishRecord(r, spec, realtime.VerbUpdate, row)
	buf, err := marshalRecordLoc(spec, row, d.recordURLFn(spec, row), requestLocaleFor(r.Context(), spec))
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "marshal failed"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// publishRecord pushes a record mutation onto the eventbus so the
// realtime broker fans it out to subscribers. nil-safe — when no
// bus is wired we silently skip.
func (d *handlerDeps) publishRecord(r *http.Request, spec builder.CollectionSpec, verb realtime.Verb, row map[string]any) {
	if d.bus == nil {
		return
	}
	id, _ := row["id"].(string)
	tenantID := ""
	if tenant.HasID(r.Context()) {
		tenantID = tenant.ID(r.Context()).String()
	}
	realtime.Publish(d.bus, realtime.RecordEvent{
		Collection: spec.Name,
		Verb:       verb,
		ID:         id,
		Record:     row,
		TenantID:   tenantID,
	})
}

// scanRow turns one pgx.Rows row into a map[colname]any, using
// FieldDescriptions for the column names. Caller has already
// positioned via rows.Next().
func scanRow(rows pgx.Rows, spec builder.CollectionSpec) (map[string]any, error) {
	descs := rows.FieldDescriptions()
	values, err := rows.Values()
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(descs))
	for i, d := range descs {
		out[string(d.Name)] = values[i]
	}
	_ = spec
	return out, nil
}

// pgErrorFor classifies a pgx error into a Railbase *Error. Returns
// nil if the error doesn't match a known-mapped class — caller falls
// through to the generic CodeInternal path.
//
// We map only what the v0.3.1 generic CRUD can produce:
//   - 23505 unique_violation   → 409 conflict
//   - 23502 not_null_violation → 400 validation
//   - 23503 foreign_key_violation → 400 validation (unless cascade)
//   - 23514 check_violation    → 400 validation
//   - 22P02 invalid_text_representation (UUID, enum) → 400 validation
func pgErrorFor(err error) *rerr.Error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case "23505":
		return rerr.Wrap(err, rerr.CodeConflict, "unique constraint violated").
			WithDetail("constraint", pgErr.ConstraintName)
	case "23502":
		return rerr.Wrap(err, rerr.CodeValidation, "field cannot be null").
			WithDetail("column", pgErr.ColumnName)
	case "23503":
		return rerr.Wrap(err, rerr.CodeValidation, "foreign key violation").
			WithDetail("constraint", pgErr.ConstraintName)
	case "23514":
		return rerr.Wrap(err, rerr.CodeValidation, "check constraint failed").
			WithDetail("constraint", pgErr.ConstraintName)
	case "22P02":
		return rerr.Wrap(err, rerr.CodeValidation, "invalid input value").
			WithDetail("detail", pgErr.Message)
	}
	return nil
}

// isInvalidUUID reports whether err is "invalid UUID syntax" — the
// path /records/{id} can hand pgx a non-UUID and we want a 404 rather
// than a 500.
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "22P02"
	}
	return false
}

// parseBoolParam returns true for "true", "1", "yes", "on" (any case).
// Anything else (empty, "false", "0", arbitrary text) returns false.
// We don't surface a validation error for unparseable values — keeps
// the query string forgiving (a typo just disables the flag).
func parseBoolParam(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
