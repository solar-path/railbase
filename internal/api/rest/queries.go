package rest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/schema/builder"
)

// Hard limits on pagination — defended at the handler layer too,
// re-checked here so a buggy caller never sends `LIMIT 100000`.
const (
	defaultPerPage = 30
	maxPerPage     = 500
)

// buildSelectColumns returns the SELECT clause column list for a
// readable view of the collection. UUID/TIMESTAMPTZ are cast to text
// where the JSON marshaller wants strings; everything else relies on
// pgx's default scan.
//
// Order is stable: id, created, updated, [tenant_id,] then user
// fields in declaration order — matches recordOutFields.
func buildSelectColumns(spec builder.CollectionSpec) []string {
	cols := []string{
		"id::text AS id",
		"created",
		"updated",
	}
	if spec.Tenant {
		cols = append(cols, "tenant_id::text AS tenant_id")
	}
	if spec.SoftDelete {
		cols = append(cols, "deleted")
	}
	if spec.AdjacencyList {
		// Cast to text so NULL/UUID round-trips through JSON as
		// string-or-null cleanly (matches the Relation field pattern).
		cols = append(cols, "parent::text AS parent")
	}
	if spec.Ordered {
		cols = append(cols, "sort_index")
	}
	for _, f := range recordOutFields(spec) {
		cols = append(cols, sqlReadColumn(f))
	}
	return cols
}

// sqlReadColumn renders the SELECT expression for one field. Mostly
// `name` verbatim, with a cast for UUID-typed columns so the driver
// hands us a string instead of pgtype.UUID.
func sqlReadColumn(f builder.FieldSpec) string {
	switch f.Type {
	case builder.TypeRelation:
		return fmt.Sprintf("%s::text AS %s", f.Name, f.Name)
	case builder.TypeFinance, builder.TypePercentage:
		// Cast NUMERIC to text so the marshaller gets a string and not
		// a pgtype.Numeric. Strings are the only safe wire shape for
		// monetary values: a JSON consumer in JS / Python parses
		// "1234.5678" as a string (no precision loss); parsing a JSON
		// number would silently lossily coerce in many runtimes.
		return fmt.Sprintf("%s::text AS %s", f.Name, f.Name)
	case builder.TypeTreePath:
		// pgx doesn't have a native LTREE scanner; cast to text so the
		// driver hands us a string and JSON marshalling Just Works.
		return fmt.Sprintf("%s::text AS %s", f.Name, f.Name)
	case builder.TypeDateRange:
		// daterange has a pgx scanner but the easy path is text → string,
		// matching our wire form ("[2024-01-01,2024-12-31)").
		return fmt.Sprintf("%s::text AS %s", f.Name, f.Name)
	}
	return f.Name
}

// listQuery captures everything the list endpoint needs to render.
// page+perPage already clamped to [1, max].
type listQuery struct {
	page    int
	perPage int
	where   string // SQL fragment without leading "WHERE"; "" if no constraints
	whereArgs []any
	sort    []filter.SortKey
	// includeDeleted: when false on a soft-delete-enabled collection,
	// the builder prepends `deleted IS NULL AND` to the WHERE clause.
	// Set true by handler when client passes `?includeDeleted=true`.
	includeDeleted bool
}

// buildList returns (sql, args) for the list SELECT and the COUNT
// SELECT, sharing the same WHERE clause so totalItems matches the
// page contents. Sort defaults to `-created, -id` when q.sort is nil
// (most-recent-first, deterministic tie-break).
func buildList(spec builder.CollectionSpec, q listQuery) (selectSQL string, selectArgs []any, countSQL string, countArgs []any) {
	if q.page < 1 {
		q.page = 1
	}
	if q.perPage <= 0 {
		q.perPage = defaultPerPage
	}
	if q.perPage > maxPerPage {
		q.perPage = maxPerPage
	}
	offset := (q.page - 1) * q.perPage

	whereSQL := ""
	if spec.SoftDelete && !q.includeDeleted {
		// Prepend the soft-delete predicate. Combined with any user
		// WHERE, the partial index `…_alive_idx ON (created) WHERE
		// deleted IS NULL` makes the IS-NULL test free.
		if q.where != "" {
			whereSQL = " WHERE deleted IS NULL AND " + q.where
		} else {
			whereSQL = " WHERE deleted IS NULL"
		}
	} else if q.where != "" {
		whereSQL = " WHERE " + q.where
	}

	orderSQL := filter.JoinSQL(q.sort)
	if orderSQL == "" {
		orderSQL = "created DESC, id DESC"
	}

	// Pagination args come AFTER the WHERE args, so $N counts continue.
	limitN := len(q.whereArgs) + 1
	offsetN := len(q.whereArgs) + 2
	selectArgs = append(append([]any{}, q.whereArgs...), q.perPage, offset)

	selectSQL = fmt.Sprintf(
		"SELECT %s FROM %s%s ORDER BY %s LIMIT $%d OFFSET $%d",
		strings.Join(buildSelectColumns(spec), ", "),
		spec.Name,
		whereSQL,
		orderSQL,
		limitN, offsetN,
	)
	countSQL = fmt.Sprintf("SELECT COUNT(*) FROM %s%s", spec.Name, whereSQL)
	countArgs = append([]any{}, q.whereArgs...)
	return
}

// buildView returns the single-row SELECT used by the view endpoint
// and as the `RETURNING` shape for create/update. extraWhere is an
// optional rule expression AND'd onto `id = $1`; pass "" for no rule.
func buildView(spec builder.CollectionSpec, id string, extraWhere string, extraArgs []any) (string, []any) {
	return buildViewOpts(spec, id, extraWhere, extraArgs, false)
}

// buildViewOpts is buildView with explicit soft-delete control. When
// includeDeleted is true AND spec.SoftDelete, tombstones are visible.
// The 3-arg buildView wrapper preserves the read-path default of
// "honour soft-delete".
func buildViewOpts(spec builder.CollectionSpec, id string, extraWhere string, extraArgs []any, includeDeleted bool) (string, []any) {
	args := append([]any{id}, extraArgs...)
	where := "id = $1"
	if spec.SoftDelete && !includeDeleted {
		where = where + " AND deleted IS NULL"
	}
	if extraWhere != "" {
		where = where + " AND " + extraWhere
	}
	sql := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s",
		strings.Join(buildSelectColumns(spec), ", "),
		spec.Name,
		where,
	)
	return sql, args
}

// buildInsert returns (sql, args) for an INSERT, including a
// RETURNING clause shaped like buildView so the caller can reuse the
// row decoder. Field order is sorted alphabetically — sql output is
// then deterministic for snapshot tests.
//
// Returns ErrNoFields if the body has no insertable values; the
// handler converts that into an "id-only INSERT" using DEFAULT VALUES.
func buildInsert(spec builder.CollectionSpec, fields map[string]any) (string, []any, error) {
	// v1.4.4 domain-types preprocessing (slug auto-derive, drop server-
	// owned sequential_code). Mutates `fields` in place.
	if err := preprocessInsertFields(spec, fields); err != nil {
		return "", nil, err
	}

	if len(fields) == 0 {
		// PG INSERT INTO t DEFAULT VALUES is the explicit zero-arg form.
		sql := fmt.Sprintf(
			"INSERT INTO %s DEFAULT VALUES RETURNING %s",
			spec.Name,
			strings.Join(buildSelectColumns(spec), ", "),
		)
		return sql, nil, nil
	}

	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	// Stable order so generated SQL is deterministic — easier to grep
	// in slow-query logs and snapshot-test friendly.
	sortStrings(names)

	cols := make([]string, len(names))
	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, n := range names {
		cols[i] = n
		placeholders[i] = "$" + strconv.Itoa(i+1)
		v, err := coerceForPG(spec, n, fields[n])
		if err != nil {
			return "", nil, err
		}
		args[i] = v
	}

	sql := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		spec.Name,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		strings.Join(buildSelectColumns(spec), ", "),
	)
	return sql, args, nil
}

// stripServerOwnedUpdateFields removes columns a PATCH is never allowed
// to touch — currently sequential_code values, which are server-
// generated, so an UPDATE attempt is silently dropped. Mutates `fields`
// in place; safe to call more than once (delete-of-absent is a no-op).
//
// updateHandler MUST call this BEFORE it sizes the access rule's
// placeholder offset off len(fields): buildUpdate strips these columns
// too, so measuring len(fields) without stripping first would number
// the rule's $N placeholders for columns that never reach the SET
// clause — desyncing the final argument list.
func stripServerOwnedUpdateFields(spec builder.CollectionSpec, fields map[string]any) {
	for _, f := range spec.Fields {
		if f.Type == builder.TypeSequentialCode {
			delete(fields, f.Name)
		}
	}
}

// buildUpdate returns the UPDATE statement. The WHERE clause is by
// id, optionally AND'd with the rule expression compiled by the
// caller. extraWhere args MUST already use placeholders starting at
// $(setCount+2) — buildUpdate appends them straight after the SET
// args + id arg. setCount is measured AFTER stripServerOwnedUpdateFields,
// so callers must strip first (see updateHandler).
//
// If fields is empty we still touch the row so the `updated` trigger
// bumps — the API promise is "PATCH always returns the current row".
func buildUpdate(spec builder.CollectionSpec, id string, fields map[string]any, extraWhere string, extraArgs []any) (string, []any, error) {
	// v1.4.4: strip server-owned sequential_code fields (UPDATE attempts
	// are silently ignored). Slug is NOT auto-re-derived on UPDATE —
	// stable URLs trump auto-update.
	stripServerOwnedUpdateFields(spec, fields)

	// Soft-delete: UPDATE on a tombstoned row is refused. Caller sees
	// 404 (same as if the row never existed). Restore via the dedicated
	// `/restore` endpoint is the only way to bring back a tombstone.
	if spec.SoftDelete {
		if extraWhere != "" {
			extraWhere = "deleted IS NULL AND " + extraWhere
		} else {
			extraWhere = "deleted IS NULL"
		}
	}

	if len(fields) == 0 {
		// Even on a no-op PATCH the rule must be re-checked so a user
		// without UpdateRule access can't ping a row.
		args := append([]any{id}, extraArgs...)
		where := "id = $1"
		if extraWhere != "" {
			where = where + " AND " + extraWhere
		}
		sql := fmt.Sprintf(
			"UPDATE %s SET updated = now() WHERE %s RETURNING %s",
			spec.Name, where,
			strings.Join(buildSelectColumns(spec), ", "),
		)
		return sql, args, nil
	}

	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sortStrings(names)

	sets := make([]string, len(names))
	args := make([]any, 0, len(names)+1+len(extraArgs))
	for i, n := range names {
		sets[i] = n + " = $" + strconv.Itoa(i+1)
		v, err := coerceForPG(spec, n, fields[n])
		if err != nil {
			return "", nil, err
		}
		args = append(args, v)
	}
	idParam := len(names) + 1
	args = append(args, id)
	args = append(args, extraArgs...)

	where := fmt.Sprintf("id = $%d", idParam)
	if extraWhere != "" {
		where = where + " AND " + extraWhere
	}
	sql := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s RETURNING %s",
		spec.Name,
		strings.Join(sets, ", "),
		where,
		strings.Join(buildSelectColumns(spec), ", "),
	)
	return sql, args, nil
}

// buildDelete returns the row-removal SQL. For a regular (hard-delete)
// collection: `DELETE FROM t WHERE …`. For a soft-delete collection:
// `UPDATE t SET deleted = now() WHERE … AND deleted IS NULL` — already-
// tombstoned rows are not double-deleted (the IS-NULL guard prevents
// resetting `deleted` to a fresh timestamp on every retry).
//
// Both branches use `RETURNING id` so the handler can detect "not found"
// via affected-row count vs "delete succeeded".
func buildDelete(spec builder.CollectionSpec, id string, extraWhere string, extraArgs []any) (string, []any) {
	args := append([]any{id}, extraArgs...)
	where := "id = $1"
	if extraWhere != "" {
		where = where + " AND " + extraWhere
	}
	if spec.SoftDelete {
		// Idempotent soft-delete: re-running on a tombstoned row
		// is a no-op (returns no rows → 404).
		sql := fmt.Sprintf(
			"UPDATE %s SET deleted = now() WHERE %s AND deleted IS NULL RETURNING id::text",
			spec.Name, where)
		return sql, args
	}
	sql := fmt.Sprintf("DELETE FROM %s WHERE %s RETURNING id::text", spec.Name, where)
	return sql, args
}

// buildRestore returns the SQL that clears `deleted` on a soft-deleted
// row. Only valid when spec.SoftDelete=true. The `deleted IS NOT NULL`
// guard makes the call idempotent at the row level — restoring a
// live row is a no-op (returns 0 affected → 404 from the handler).
func buildRestore(spec builder.CollectionSpec, id string, extraWhere string, extraArgs []any) (string, []any) {
	args := append([]any{id}, extraArgs...)
	where := "id = $1 AND deleted IS NOT NULL"
	if extraWhere != "" {
		where = where + " AND " + extraWhere
	}
	sql := fmt.Sprintf(
		"UPDATE %s SET deleted = NULL WHERE %s RETURNING %s",
		spec.Name, where, strings.Join(buildSelectColumns(spec), ", "))
	return sql, args
}

// coerceForPG converts a JSON-decoded value into the form pgx wants
// for the column. Most types pass through; the interesting cases:
//
//   - JSON column with a non-string value → re-marshal so we can pass
//     bytes to JSONB.
//   - Number column declared .Int() → demand integer-shaped JSON
//     (json.Number with no fractional part), reject "1.5" early.
//   - MultiSelect → []any → []string for pgx's text-array encoder.
//
// Returns an error when the user's value can't fit the declared
// shape — surfaced as 400 validation by the handler.
//
// System columns (tenant_id) are passed through verbatim — they're
// only ever supplied by the framework, never by the client.
func coerceForPG(spec builder.CollectionSpec, name string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if name == "tenant_id" && spec.Tenant {
		return v, nil
	}
	// Adjacency-list `parent` and Ordered `sort_index` are system
	// columns that the client populates directly — `parent` is the
	// FK to the same collection; `sort_index` is the explicit position
	// hint. Pass through as-is and let pgx coerce. Cycle / depth checks
	// run in a separate pre-INSERT/UPDATE pass.
	if name == "parent" && spec.AdjacencyList {
		if v == nil {
			return nil, nil
		}
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("field %q: expected UUID string", name)
		}
		return s, nil
	}
	if name == "sort_index" && spec.Ordered {
		// Accept ints, JSON-decoded float64, AND json.Number (parseInput
		// uses dec.UseNumber() so integer JSON numbers arrive as
		// json.Number strings, not float64). Coerce to int64 so pgx
		// writes it to INTEGER cleanly.
		switch x := v.(type) {
		case float64:
			return int64(x), nil
		case int:
			return int64(x), nil
		case int64:
			return x, nil
		case json.Number:
			n, err := x.Int64()
			if err != nil {
				return nil, fmt.Errorf("field %q: expected integer, got %q", name, x.String())
			}
			return n, nil
		}
		return nil, fmt.Errorf("field %q: expected integer", name)
	}
	var f *builder.FieldSpec
	for i := range spec.Fields {
		if spec.Fields[i].Name == name {
			f = &spec.Fields[i]
			break
		}
	}
	if f == nil {
		return nil, fmt.Errorf("unknown field %q", name)
	}

	// Translatable fields short-circuit the per-type switch — the column
	// is JSONB regardless of the declared base type. Accept a JSON object
	// `{locale: value, ...}` and validate each key against the BCP-47
	// shape regex shared with the schema builder.
	if f.Translatable {
		return coerceTranslatable(name, v)
	}

	switch f.Type {
	case builder.TypeJSON:
		// JSONB accepts a []byte; encode whatever we got back to JSON.
		buf, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: encode json: %w", name, err)
		}
		return buf, nil

	case builder.TypeMultiSelect:
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("field %q: expected array, got %T", name, v)
		}
		out := make([]string, 0, len(arr))
		for i, item := range arr {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("field %q: array element %d is not a string", name, i)
			}
			out = append(out, s)
		}
		return out, nil

	case builder.TypeNumber:
		switch n := v.(type) {
		case json.Number:
			if f.IsInt {
				i, err := n.Int64()
				if err != nil {
					return nil, fmt.Errorf("field %q: expected integer, got %s", name, n.String())
				}
				return i, nil
			}
			fl, err := n.Float64()
			if err != nil {
				return nil, fmt.Errorf("field %q: invalid number %s", name, n.String())
			}
			return fl, nil
		case float64:
			if f.IsInt {
				if n != float64(int64(n)) {
					return nil, fmt.Errorf("field %q: expected integer, got %v", name, n)
				}
				return int64(n), nil
			}
			return n, nil
		case int:
			return int64(n), nil
		case int64:
			return n, nil
		}
		return nil, fmt.Errorf("field %q: expected number, got %T", name, v)

	case builder.TypeBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("field %q: expected bool, got %T", name, v)

	case builder.TypeDate:
		// We accept either RFC3339 or PB-style. Pass through as a
		// string and let Postgres parse — keeps the queries layer
		// dialect-aware.
		if s, ok := v.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("field %q: expected timestamp string, got %T", name, v)

	case builder.TypeText, builder.TypeEmail, builder.TypeURL,
		builder.TypeRichText, builder.TypeSelect, builder.TypeRelation:
		if s, ok := v.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("field %q: expected string, got %T", name, v)

	case builder.TypeTel:
		// Accept any string; normalise to E.164. Empty → reject (let
		// Required handle null intent).
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseTel(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypePersonName:
		// Two accepted shapes: bare string ("John Q. Public") → wrapped
		// into {full: ...}; or object with allowed keys only. Validate
		// each value is a string ≤ 200 chars.
		obj, err := normalisePersonName(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		buf, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("field %q: encode: %w", name, err)
		}
		return buf, nil

	case builder.TypeAddress:
		// Object-only (no string sugar — address is too structured for
		// a single-field shorthand; admins / SDK pass the full object).
		raw, err := normaliseAddress(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return raw, nil

	case builder.TypeTaxID:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseTaxID(s, f.TaxCountry)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeBarcode:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseBarcode(s, f.BarcodeFormat)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeCurrency:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseCurrency(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeMoneyRange:
		raw, err := normaliseMoneyRange(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return raw, nil

	case builder.TypeDateRange:
		s, err := normaliseDateRange(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return s, nil

	case builder.TypeTimeRange:
		raw, err := normaliseTimeRange(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return raw, nil

	case builder.TypeBankAccount:
		raw, err := normaliseBankAccount(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return raw, nil

	case builder.TypeQRCode:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseQRCode(s, f.QRFormat)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeSlug:
		// Accept any string; normalise to canonical lowercase-hyphen
		// shape. Empty → reject (preprocessInsertFields should have
		// auto-derived earlier; reaching coerceForPG with empty slug
		// means the source field was also empty).
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseSlug(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeSequentialCode:
		// Server-owned. The preprocess + buildUpdate strips it from
		// the field map; reaching here means something forgot to strip.
		return nil, fmt.Errorf("field %q is server-owned (sequential_code) — do not supply value", name)

	case builder.TypeColor:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseColor(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeCron:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		// Validate by attempting to compile; we don't store the parsed
		// form, only the source string. Whitespace is normalised
		// (collapse internal runs, trim ends) so two equivalent
		// expressions compare equal at the byte level.
		trimmed := strings.Join(strings.Fields(s), " ")
		if _, err := jobs.ParseCron(trimmed); err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return trimmed, nil

	case builder.TypeMarkdown:
		// No write-side transformation; Markdown's grammar is
		// intentionally permissive. Min/max-len CHECK runs DB-side.
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		return s, nil

	case builder.TypeCountry:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseCountry(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeLanguage:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseLanguage(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeLocale:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseLocale(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeCoordinates:
		raw, err := normaliseCoordinates(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return raw, nil

	case builder.TypeIBAN:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseIBAN(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeBIC:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseBIC(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeQuantity:
		// Per-collection spec carries the allow-list of units. Look up
		// the field spec to pass that into normaliseQuantity.
		var fieldSpec builder.FieldSpec
		for _, fs := range spec.Fields {
			if fs.Name == name {
				fieldSpec = fs
				break
			}
		}
		obj, err := normaliseQuantity(v, fieldSpec.QuantityUnits)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		buf, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("field %q: encode: %w", name, err)
		}
		return buf, nil

	case builder.TypeDuration:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeTags:
		var fieldSpec builder.FieldSpec
		for _, fs := range spec.Fields {
			if fs.Name == name {
				fieldSpec = fs
				break
			}
		}
		tags, err := normaliseTags(v, fieldSpec.TagMaxLen, fieldSpec.TagsMaxCount)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return tags, nil

	case builder.TypeTreePath:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		canonical, err := normaliseTreePath(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil

	case builder.TypeStatus:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		// Membership check (transition check runs at the handler layer,
		// where we have access to the current row).
		var fieldSpec builder.FieldSpec
		for _, fs := range spec.Fields {
			if fs.Name == name {
				fieldSpec = fs
				break
			}
		}
		ok = false
		for _, v := range fieldSpec.StatusValues {
			if v == s {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("field %q: %q not in allowed states %v", name, s, fieldSpec.StatusValues)
		}
		return s, nil

	case builder.TypePriority, builder.TypeRating:
		// Accept integer, json.Number with no fraction, or digit string.
		// We convert to int16 for SMALLINT storage.
		var n int64
		switch t := v.(type) {
		case float64:
			if t != float64(int64(t)) {
				return nil, fmt.Errorf("field %q: must be an integer, got %v", name, t)
			}
			n = int64(t)
		case json.Number:
			parsed, err := t.Int64()
			if err != nil {
				return nil, fmt.Errorf("field %q: must be an integer, got %v", name, t)
			}
			n = parsed
		case string:
			parsed, err := strconv.ParseInt(t, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", name, err)
			}
			n = parsed
		case int:
			n = int64(t)
		case int64:
			n = t
		default:
			return nil, fmt.Errorf("field %q: expected integer, got %T", name, v)
		}
		// Range bounds re-checked here so we surface a clean error
		// before the DB CHECK fires.
		var fieldSpec builder.FieldSpec
		for _, fs := range spec.Fields {
			if fs.Name == name {
				fieldSpec = fs
				break
			}
		}
		if fieldSpec.IntMin != nil && n < int64(*fieldSpec.IntMin) {
			return nil, fmt.Errorf("field %q: %d below min %d", name, n, *fieldSpec.IntMin)
		}
		if fieldSpec.IntMax != nil && n > int64(*fieldSpec.IntMax) {
			return nil, fmt.Errorf("field %q: %d above max %d", name, n, *fieldSpec.IntMax)
		}
		return n, nil

	case builder.TypeTimezone:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", name, v)
		}
		// Empty timezone falls back to UTC in stdlib — reject so callers
		// pass UTC explicitly when they mean it.
		if strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("field %q: timezone must not be empty (use \"UTC\" explicitly)", name)
		}
		// Validate via stdlib — same IANA tz database Postgres uses.
		if _, err := time.LoadLocation(s); err != nil {
			return nil, fmt.Errorf("field %q: unknown timezone %q (must be IANA identifier like \"Europe/Moscow\")", name, s)
		}
		return s, nil

	case builder.TypeFinance, builder.TypePercentage:
		// Accept string ("1234.56") OR JSON number (json.Number because
		// parseInput uses UseNumber to preserve large-int precision).
		// We pass the literal lexeme straight through — no float
		// conversion — so a value like "0.10000000000000003" survives
		// untouched. validateDecimalString is the shape check.
		var s string
		switch t := v.(type) {
		case string:
			s = t
		case json.Number:
			s = string(t)
		case float64:
			// Fallback for code paths that bypass UseNumber.
			s = strconv.FormatFloat(t, 'f', -1, 64)
		case int, int64:
			s = fmt.Sprintf("%d", t)
		default:
			return nil, fmt.Errorf("field %q: expected string or number, got %T", name, v)
		}
		canonical, err := validateDecimalString(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		return canonical, nil
	}
	return v, nil
}

// preprocessInsertFields runs the v1.4.4 domain-type insert-time logic:
//
//   - Slug auto-derive: if the client omitted the slug AND the field
//     has `From(<source>)`, derive the slug from the source field's
//     value. If the source field is also empty/missing, leave the slug
//     out (Required validation surfaces a clean error later).
//   - SequentialCode strip: drop any client-supplied value so the
//     column DEFAULT (nextval-backed) is used.
//
// Mutates `fields` in place.
func preprocessInsertFields(spec builder.CollectionSpec, fields map[string]any) error {
	for _, f := range spec.Fields {
		switch f.Type {
		case builder.TypeSlug:
			present := false
			if v, ok := fields[f.Name]; ok {
				if s, isStr := v.(string); isStr && s != "" {
					present = true
				} else if v != nil && !isStr {
					// Non-string slug value is a client error; let
					// coerceForPG surface it with a clear message.
					present = true
				}
			}
			if present {
				continue
			}
			// Try auto-derive from source field.
			if f.SlugFrom == "" {
				continue // no auto-derive configured; let Required check it
			}
			src, ok := fields[f.SlugFrom]
			if !ok {
				continue
			}
			srcStr, ok := src.(string)
			if !ok || srcStr == "" {
				continue
			}
			derived, err := normaliseSlug(srcStr)
			if err != nil {
				return fmt.Errorf("field %q: cannot derive slug from %q (%q): %w",
					f.Name, f.SlugFrom, srcStr, err)
			}
			fields[f.Name] = derived

		case builder.TypeSequentialCode:
			// Server-owned. Strip whatever the client sent so the
			// column DEFAULT (nextval) runs.
			delete(fields, f.Name)
		}
	}
	return nil
}

// normaliseTel canonicalises a phone-number string to E.164:
//   - strips spaces, parens, dashes, dots
//   - keeps leading '+'; rejects multiple '+' anywhere
//   - must result in '+' followed by 1-15 digits, first digit non-zero
//
// PocketBase / Twilio / Stripe all accept E.164 — easiest interop.
func normaliseTel(in string) (string, error) {
	var b []byte
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case c == '+':
			if len(b) != 0 {
				return "", fmt.Errorf("invalid phone number (extra '+')")
			}
			b = append(b, '+')
		case c >= '0' && c <= '9':
			b = append(b, c)
		case c == ' ', c == '-', c == '(', c == ')', c == '.':
			// skip separators
		default:
			return "", fmt.Errorf("invalid phone number character %q", c)
		}
	}
	if len(b) == 0 || b[0] != '+' {
		return "", fmt.Errorf("phone number must start with '+<country code>'")
	}
	digits := b[1:]
	if len(digits) < 2 || len(digits) > 15 {
		return "", fmt.Errorf("phone number must have 2-15 digits (got %d)", len(digits))
	}
	if digits[0] == '0' {
		return "", fmt.Errorf("phone number country code may not start with 0")
	}
	return string(b), nil
}

// normalisePersonName accepts either a string (treated as `full`) or
// an object with allowed keys (first/middle/last/suffix/full). Returns
// the normalised map.
func normalisePersonName(v any) (map[string]string, error) {
	allowed := map[string]bool{"first": true, "middle": true, "last": true, "suffix": true, "full": true}
	out := map[string]string{}
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil, fmt.Errorf("empty name")
		}
		if len(t) > 200 {
			return nil, fmt.Errorf("name too long (max 200 chars)")
		}
		out["full"] = t
		return out, nil
	case map[string]any:
		if len(t) == 0 {
			return nil, fmt.Errorf("at least one component required")
		}
		for k, vv := range t {
			if !allowed[k] {
				return nil, fmt.Errorf("unknown name component %q (allowed: first/middle/last/suffix/full)", k)
			}
			s, ok := vv.(string)
			if !ok {
				return nil, fmt.Errorf("component %q: expected string, got %T", k, vv)
			}
			if len(s) > 200 {
				return nil, fmt.Errorf("component %q too long (max 200 chars)", k)
			}
			if s == "" {
				continue
			}
			out[k] = s
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("at least one non-empty component required")
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected string or object, got %T", v)
}

// normaliseSlug canonicalises an arbitrary string into the slug shape
// enforced by the CHECK constraint: lowercase ASCII letters/digits,
// single hyphens between runs. Strategy:
//
//   - Walk one byte at a time. Non-ASCII (multibyte UTF-8 leading bytes,
//     accented characters, Cyrillic, CJK) is treated as a separator —
//     callers who want transliteration should do it client-side.
//   - Lowercase A-Z by adding 32.
//   - Anything not [a-z0-9] becomes a hyphen sentinel.
//   - Collapse consecutive hyphens to one, strip leading/trailing.
//   - Empty result → error.
func normaliseSlug(in string) (string, error) {
	hyphens := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			hyphens = append(hyphens, c)
		case c >= 'A' && c <= 'Z':
			hyphens = append(hyphens, c+32)
		default:
			// Anything else — space, punctuation, multibyte byte — becomes
			// a hyphen marker. We dedupe in the next pass.
			hyphens = append(hyphens, '-')
		}
	}
	// Dedupe consecutive hyphens; strip leading/trailing.
	out := make([]byte, 0, len(hyphens))
	prevHyphen := true // start state: "previous was hyphen" → suppress leading
	for _, c := range hyphens {
		if c == '-' {
			if prevHyphen {
				continue
			}
			out = append(out, '-')
			prevHyphen = true
		} else {
			out = append(out, c)
			prevHyphen = false
		}
	}
	// Strip trailing hyphen.
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return "", fmt.Errorf("slug is empty after normalisation (input had no ASCII alphanumerics)")
	}
	return string(out), nil
}

// normaliseColor canonicalises a hex color string into the form the
// CHECK constraint expects: '#' + 6 lowercase hex digits. Accepts:
//
//   - "#abc" / "abc" → "#aabbcc" (3-digit shorthand expanded)
//   - "#FF5733" / "FF5733" → "#ff5733" (uppercase lowered, '#' added)
//
// Rejects anything else — empty, wrong length, non-hex chars.
func normaliseColor(in string) (string, error) {
	s := strings.TrimSpace(in)
	if strings.HasPrefix(s, "#") {
		s = s[1:]
	}
	switch len(s) {
	case 3:
		// Expand each digit: "abc" → "aabbcc".
		expanded := make([]byte, 6)
		for i := 0; i < 3; i++ {
			c := s[i]
			if !isHex(c) {
				return "", fmt.Errorf("invalid hex digit %q", c)
			}
			expanded[2*i] = c
			expanded[2*i+1] = c
		}
		s = string(expanded)
	case 6:
		for i := 0; i < 6; i++ {
			if !isHex(s[i]) {
				return "", fmt.Errorf("invalid hex digit %q", s[i])
			}
		}
	default:
		return "", fmt.Errorf("color must be #RGB or #RRGGBB (got %d chars)", len(s))
	}
	// Lowercase.
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return "#" + string(b), nil
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// validateDecimalString parses a decimal-number string with optional
// leading sign and optional fractional part. Returns the canonical
// form (no leading '+', no leading zeros except for "0." or "0",
// stripped trailing zeros after the decimal point, "-0" → "0").
//
// We deliberately don't use math/big.Rat or strconv.ParseFloat — float
// parsing loses precision, and we want byte-exact round-trip from
// user → DB → user. The DB NUMERIC column does the heavy lifting on
// the precision/scale enforcement; this validator just confirms the
// shape so we don't punt syntax errors into the SQL layer.
func validateDecimalString(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", fmt.Errorf("empty decimal")
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	if s == "" {
		return "", fmt.Errorf("decimal has no digits")
	}
	dotIdx := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if dotIdx >= 0 {
				return "", fmt.Errorf("decimal has multiple dots")
			}
			dotIdx = i
			continue
		}
		if c < '0' || c > '9' {
			return "", fmt.Errorf("decimal has invalid character %q", c)
		}
	}
	intPart, fracPart := s, ""
	if dotIdx >= 0 {
		intPart = s[:dotIdx]
		fracPart = s[dotIdx+1:]
		if intPart == "" {
			intPart = "0"
		}
	}
	if intPart == "" {
		return "", fmt.Errorf("decimal missing integer part")
	}
	// Reject inputs that have a dot but no digits on either side
	// ("." after sign-stripping). We already filled intPart="0" above,
	// so check the original construction: if both intPart was forced
	// AND fracPart is empty, we never saw a real digit.
	if dotIdx >= 0 && dotIdx == 0 && fracPart == "" {
		return "", fmt.Errorf("decimal has no digits")
	}
	// Trim leading zeros from integer part (keep at least one digit).
	for len(intPart) > 1 && intPart[0] == '0' {
		intPart = intPart[1:]
	}
	// Trim trailing zeros from fractional part.
	for len(fracPart) > 0 && fracPart[len(fracPart)-1] == '0' {
		fracPart = fracPart[:len(fracPart)-1]
	}
	// "-0" or "-0.0" → "0".
	out := intPart
	if fracPart != "" {
		out = intPart + "." + fracPart
	}
	if neg && (out != "0") {
		out = "-" + out
	}
	return out, nil
}

// sortStrings is a tiny in-place sort to avoid pulling in sort just
// for these hot paths. Insertion sort: column lists are O(<50) at
// the worst, perf doesn't matter.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// coerceTranslatable validates and encodes a Translatable field value.
// Accepted input shapes:
//
//   - map[string]any with string values + BCP-47 keys
//   - already-encoded []byte (passthrough, no validation — paranoia
//     guard: a hook may have pre-marshalled the column)
//
// Rejected:
//
//   - any other type (string / number / array / nested object)
//   - any key not matching the localeKey regex
//   - any value that isn't a string (number / bool / nested object)
//
// Returns the JSONB-ready []byte to pass into pgx.
func coerceTranslatable(name string, v any) (any, error) {
	switch obj := v.(type) {
	case map[string]any:
		if len(obj) == 0 {
			return nil, fmt.Errorf("field %q: translatable map must have at least one locale entry", name)
		}
		out := make(map[string]string, len(obj))
		for k, val := range obj {
			if !builder.IsValidLocaleKey(k) {
				return nil, fmt.Errorf("field %q: invalid locale key %q (expected BCP-47 `xx` or `xx-XX`)", name, k)
			}
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("field %q: value for locale %q must be a string, got %T", name, k, val)
			}
			out[k] = s
		}
		buf, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("field %q: encode translatable: %w", name, err)
		}
		return buf, nil
	case []byte:
		// Defensive — assume already validated. Verify the shape is at
		// least an object so we don't write garbage to a JSONB column.
		var probe map[string]any
		if err := json.Unmarshal(obj, &probe); err != nil {
			return nil, fmt.Errorf("field %q: pre-encoded translatable is not a JSON object", name)
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("field %q: translatable expects a JSON object {locale: value}, got %T", name, v)
	}
}
