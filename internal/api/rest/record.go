// Package rest mounts the generic CRUD HTTP routes for every
// collection in the schema registry. Spec: docs/02-architecture.md
// "REST endpoints" + docs/11-frontend-sdk.md "Wire format".
//
// v0.3.1 scope:
//   - PB-compat URLs for list/view/create/update/delete:
//     /api/collections/{name}/records[/{id}]
//   - JSON record envelope: id (string), collectionName, created,
//     updated, then user fields flat. Timestamps in PB format
//     (2006-01-02 15:04:05.000Z). UUID emitted as plain string.
//   - Offset pagination with {page, perPage, totalItems, totalPages,
//     items}. Default perPage=30, max 500.
//   - Field types covered: text, email, url, select, richtext,
//     number, bool, date, json, multiselect, relation.
//   - Field types deferred: password, file, files, relations
//     (writing returns 501; reading skips them silently).
//   - Tenant collections refuse all CRUD with 501 — tenant resolution
//     middleware lands in v0.4.
//   - Rules (List/View/...) are stored on the collection but NOT
//     enforced; auth + filter parser ship in v0.3.2 / v0.3.3.
package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/railbase/railbase/internal/i18n"
	"github.com/railbase/railbase/internal/schema/builder"
)

// PB-style timestamp layout. Differs from RFC3339 in two ways:
// space separator between date and time, fractional seconds zero-
// padded to milliseconds. Existing PocketBase clients rely on this.
const pbTimeLayout = "2006-01-02 15:04:05.000Z"

// formatTime renders t as a PB-compat string. Always emits UTC so
// clients in any timezone see a single canonical form.
func formatTime(t time.Time) string {
	return t.UTC().Format(pbTimeLayout)
}

// recordOutFields returns the field specs we round-trip through the
// API in v0.3.1. Password / file / files / relations are skipped —
// they need dedicated endpoints (auth, uploads, m2m) not yet built.
func recordOutFields(spec builder.CollectionSpec) []builder.FieldSpec {
	out := make([]builder.FieldSpec, 0, len(spec.Fields))
	for _, f := range spec.Fields {
		if !isReadableField(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// isReadableField reports whether the generic CRUD layer surfaces
// the column on read. Deferred types are filtered so callers don't
// receive a half-implemented value.
//
// v1.3.1 widens this to include TypeFile / TypeFiles — the column is
// rendered as `{name, url}` (single) or `[{name, url}, ...]` (multi)
// when a URL builder is plumbed through marshalRecord; otherwise the
// raw filename string passes through.
func isReadableField(f builder.FieldSpec) bool {
	switch f.Type {
	case builder.TypePassword, builder.TypeRelations:
		return false
	}
	return true
}

// isWritableField says which field types accept JSON-body input on
// create/update. File / files columns are populated through the
// dedicated upload endpoint (v1.3.1) — accepting them in the JSON
// body would let callers point a record at any filename, bypassing
// MIME / size validation.
func isWritableField(f builder.FieldSpec) bool {
	switch f.Type {
	case builder.TypePassword, builder.TypeRelations,
		builder.TypeFile, builder.TypeFiles:
		return false
	}
	return true
}

// marshalRecord assembles the JSON object Railbase emits for one row.
// row keys must match the SQL column names used in the SELECT.
//
// Shape (PB-compat):
//
//	{
//	  "id": "<uuid string>",
//	  "collectionName": "<spec.Name>",
//	  "created": "2026-05-10 12:34:56.000Z",
//	  "updated": "2026-05-10 12:34:56.000Z",
//	  "<field>": <value>,
//	  ...
//	}
//
// Unknown / deferred fields in row are ignored. Missing-but-expected
// fields render as null in JSON.
// fileURLFunc, when non-nil, is called by marshalRecord to construct
// signed download URLs for file/files fields. Pass nil when storage
// isn't wired (e.g. unit tests) — the marshaller falls back to the
// raw filename string.
type fileURLFunc func(field, filename string) string

// fileObject renders a single-file column value as {name, url}.
// When the column is empty we caller-skip, but in case it reaches us
// we treat empty as null.
func fileObject(name, field string, urlFn fileURLFunc) any {
	if name == "" {
		return nil
	}
	out := map[string]any{"name": name}
	if urlFn != nil {
		out["url"] = urlFn(field, name)
	}
	return out
}

// filesArray renders a multi-file JSONB column value as an array of
// {name, url} objects. Accepts either []byte (raw JSONB from pgx)
// or already-parsed []any.
func filesArray(val any, field string, urlFn fileURLFunc) any {
	var names []string
	switch v := val.(type) {
	case []byte:
		if len(v) == 0 {
			return []any{}
		}
		var arr []any
		if err := json.Unmarshal(v, &arr); err != nil {
			return []any{}
		}
		for _, item := range arr {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
	case nil:
		return []any{}
	default:
		return []any{}
	}
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, fileObject(n, field, urlFn))
	}
	return out
}

// pickTranslatableLoc returns the best value from a translatable
// field's locale-keyed map for the given request locale. Fallback
// order matches the documented contract:
//
//  1. exact match on the requested locale            ("ru" → ru)
//  2. base language of the requested locale           ("pt-BR" → pt)
//  3. first key in alphabetical order                 (deterministic)
//  4. empty string when the map is empty / nil
//
// The catalog's DefaultLocale fallback is NOT applied here — the REST
// handler does not carry a Catalog reference, and i18n middleware
// already stamps a usable locale on the context (negotiating against
// the catalog's supported set). For an entirely empty / unsupported
// request, callers pass loc="" which short-circuits this helper.
func pickTranslatableLoc(loc i18n.Locale, values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	loc = i18n.Canonical(string(loc))
	if v, ok := values[string(loc)]; ok {
		return v
	}
	if base := loc.Base(); base != loc {
		if v, ok := values[string(base)]; ok {
			return v
		}
	}
	// Deterministic last resort.
	var pick string
	first := true
	for k := range values {
		if first || k < pick {
			pick = k
			first = false
		}
	}
	return values[pick]
}

// marshalRecord is the locale-free legacy entrypoint — keeps the wire
// shape stable for callers that don't have a request context. Records
// containing Translatable fields emit the FULL locale map as a JSON
// object (so the SDK can still see every translation).
func marshalRecord(spec builder.CollectionSpec, row map[string]any, urlFn fileURLFunc) ([]byte, error) {
	return marshalRecordLoc(spec, row, urlFn, "")
}

// marshalRecordLoc is the locale-aware marshaller. When `loc` is empty
// (no request context, or no Translatable field on the spec) the
// behaviour matches marshalRecord exactly. Otherwise Translatable
// fields collapse from a JSONB locale map to a single string picked
// via the same fallback chain as Catalog.PickLocaleValue (with
// `loc` standing in for the catalog's DefaultLocale fallback).
//
// Pass loc = "" to opt out of per-locale picking (admin UI / exports
// still want to see every translation).
func marshalRecordLoc(spec builder.CollectionSpec, row map[string]any, urlFn fileURLFunc, loc i18n.Locale) ([]byte, error) {
	// Use a buffer with manual ordering rather than map iteration so
	// the wire format is deterministic — system fields first, user
	// fields in declaration order. Helps log diffing and snapshot tests.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	buf.WriteByte('{')
	first := true
	write := func(key string, val any) error {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(key)
		buf.Write(k)
		buf.WriteByte(':')
		// json.Encoder appends a newline; trim it so the surrounding
		// object keeps a single line.
		var sub bytes.Buffer
		se := json.NewEncoder(&sub)
		se.SetEscapeHTML(false)
		if err := se.Encode(val); err != nil {
			return err
		}
		raw := bytes.TrimRight(sub.Bytes(), "\n")
		buf.Write(raw)
		return nil
	}

	if err := write("id", row["id"]); err != nil {
		return nil, err
	}
	if err := write("collectionName", spec.Name); err != nil {
		return nil, err
	}
	if v, ok := row["created"].(time.Time); ok {
		if err := write("created", formatTime(v)); err != nil {
			return nil, err
		}
	} else if v, ok := row["created"].(string); ok {
		if err := write("created", v); err != nil {
			return nil, err
		}
	}
	if v, ok := row["updated"].(time.Time); ok {
		if err := write("updated", formatTime(v)); err != nil {
			return nil, err
		}
	} else if v, ok := row["updated"].(string); ok {
		if err := write("updated", v); err != nil {
			return nil, err
		}
	}
	// Soft-delete tombstone timestamp. Emitted only when the collection
	// has SoftDelete enabled and the row carries the `deleted` column.
	// null on live rows; ISO-8601 timestamp on tombstones.
	if spec.SoftDelete {
		val, _ := row["deleted"]
		if t, ok := val.(time.Time); ok {
			if err := write("deleted", formatTime(t)); err != nil {
				return nil, err
			}
		} else {
			if err := write("deleted", nil); err != nil {
				return nil, err
			}
		}
	}
	// Hierarchy system columns: parent (AdjacencyList) and sort_index
	// (Ordered) are emitted right after the tombstone slot so the user
	// fields come last in declaration order.
	if spec.AdjacencyList {
		// parent is cast to text in the SELECT → string-or-nil here.
		if err := write("parent", row["parent"]); err != nil {
			return nil, err
		}
	}
	if spec.Ordered {
		// sort_index is plain INTEGER → int64 from pgx.
		if err := write("sort_index", row["sort_index"]); err != nil {
			return nil, err
		}
	}

	for _, f := range recordOutFields(spec) {
		val, ok := row[f.Name]
		if !ok {
			val = nil
		}
		// Translatable fields are stored as JSONB locale-keyed maps.
		// When a request locale is known, collapse to the best match;
		// otherwise emit the full map as real JSON (json.RawMessage).
		if f.Translatable {
			if b, isBytes := val.([]byte); isBytes && len(b) > 0 {
				if loc != "" {
					var m map[string]string
					if err := json.Unmarshal(b, &m); err == nil {
						val = pickTranslatableLoc(loc, m)
					} else {
						val = json.RawMessage(b)
					}
				} else {
					val = json.RawMessage(b)
				}
			} else if val == nil {
				// Leave nil — column was NULL.
			}
			if err := write(f.Name, val); err != nil {
				return nil, err
			}
			continue
		}
		switch f.Type {
		case builder.TypeDate:
			if t, ok := val.(time.Time); ok {
				val = formatTime(t)
			}
		case builder.TypeJSON:
			// Stored as JSONB — driver returns []byte. Re-emit as
			// json.RawMessage so it lands in the parent object as
			// real JSON, not a base64 string.
			if b, ok := val.([]byte); ok {
				val = json.RawMessage(b)
			}
		case builder.TypeFile:
			// Single-file column stores a filename string. Emit as a
			// nested object so the wire shape matches multi-file.
			name, _ := val.(string)
			if name == "" {
				val = nil
				break
			}
			val = fileObject(name, f.Name, urlFn)
		case builder.TypeFiles:
			// JSONB array of filename strings. Render each as a
			// nested object.
			val = filesArray(val, f.Name, urlFn)
		case builder.TypePersonName, builder.TypeQuantity, builder.TypeCoordinates,
			builder.TypeAddress, builder.TypeMoneyRange, builder.TypeTimeRange,
			builder.TypeBankAccount:
			// JSONB row → real JSON object on the wire (not base64).
			if b, ok := val.([]byte); ok {
				val = json.RawMessage(b)
			}
		}
		if err := write(f.Name, val); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// parseInput decodes a request body into a map of column → value
// suitable for queries.buildInsert / buildUpdate. Validation runs:
//
//   - Unknown keys → CodeValidation, listed in details.
//   - System fields (id/created/updated/tenant_id/collectionName) →
//     silently dropped. PB clients sometimes echo the whole record
//     back, and rejecting on those would be hostile.
//   - Required fields on create → checked here when create=true.
//   - Deferred field types → CodeValidation with hint about v0.3
//     scope.
//
// JSON values are kept as the json package decoded them (string,
// float64, bool, []any, map[string]any, nil); the queries layer
// coerces them to PG types as it builds the placeholder list.
func parseInput(spec builder.CollectionSpec, body []byte, create bool) (map[string]any, *parseErr) {
	var raw map[string]any
	if len(bytes.TrimSpace(body)) == 0 {
		raw = map[string]any{}
	} else {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return nil, &parseErr{Message: "invalid JSON body: " + err.Error()}
		}
	}

	systemKeys := map[string]bool{
		"id":             true,
		"created":        true,
		"updated":        true,
		"tenant_id":      true,
		"collectionName": true,
		"collectionId":   true,
	}

	known := make(map[string]builder.FieldSpec, len(spec.Fields))
	for _, f := range spec.Fields {
		known[f.Name] = f
	}

	out := make(map[string]any, len(raw))
	var unknown []string
	var deferred []string
	for k, v := range raw {
		if systemKeys[k] {
			continue
		}
		// Hierarchy modifiers: AdjacencyList adds `parent`; Ordered adds
		// `sort_index`. Both are client-writable system columns.
		if k == "parent" && spec.AdjacencyList {
			out[k] = v
			continue
		}
		if k == "sort_index" && spec.Ordered {
			out[k] = v
			continue
		}
		f, ok := known[k]
		if !ok {
			unknown = append(unknown, k)
			continue
		}
		if !isWritableField(f) {
			deferred = append(deferred, k+" ("+string(f.Type)+")")
			continue
		}
		out[k] = v
	}

	if len(deferred) > 0 {
		return nil, &parseErr{
			Message: "field types not supported by generic CRUD in v0.3.1",
			Details: map[string]any{"deferred_fields": deferred},
		}
	}
	if len(unknown) > 0 {
		return nil, &parseErr{
			Message: "unknown fields in request body",
			Details: map[string]any{"unknown_fields": unknown},
		}
	}

	if create {
		var missing []string
		for _, f := range spec.Fields {
			if !f.Required || !isWritableField(f) {
				continue
			}
			if _, ok := out[f.Name]; !ok && !f.HasDefault {
				// AutoCreate dates fill themselves at the DB level.
				if f.Type == builder.TypeDate && f.AutoCreate {
					continue
				}
				// SequentialCode is server-filled (sequence-backed
				// column DEFAULT). Required just means NOT NULL at
				// the DB level, not "client must supply".
				if f.Type == builder.TypeSequentialCode {
					continue
				}
				// Status has a SQL-side DEFAULT of its first declared
				// state. If we got here without a value supplied, the
				// DB will fill in the initial state.
				if f.Type == builder.TypeStatus && len(f.StatusValues) > 0 {
					continue
				}
				// Slug with a `From` source field auto-derives at
				// preprocessInsertFields time. Skip the required check
				// here if the source field is present — the actual
				// "empty after derive" failure is surfaced later with
				// a clearer error.
				if f.Type == builder.TypeSlug && f.SlugFrom != "" {
					if _, hasSrc := out[f.SlugFrom]; hasSrc {
						continue
					}
				}
				missing = append(missing, f.Name)
			}
		}
		if len(missing) > 0 {
			return nil, &parseErr{
				Message: "required fields missing",
				Details: map[string]any{"missing_fields": missing},
			}
		}
	}

	return out, nil
}

// parseErr is the validation failure shape returned by parseInput.
// We keep it package-private and let the handler layer convert it
// into the canonical *errors.Error envelope.
type parseErr struct {
	Message string
	Details map[string]any
}

func (e *parseErr) Error() string { return fmt.Sprintf("parse: %s", e.Message) }
