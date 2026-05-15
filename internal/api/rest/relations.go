package rest

// Phase 3.x — M2M generic CRUD via implicit junction tables.
//
// Sentinel's `tasks.predecessors` had to be JSONB because the v0.3
// generic CRUD didn't understand `.Relations()`. With junction tables
// generated in DDL (see internal/schema/gen/sql.go::createJunction)
// and these helpers, the REST handler now:
//
//   1. On CREATE/UPDATE — pops every TypeRelations field out of the
//      body BEFORE building the row INSERT/UPDATE. Stores the popped
//      arrays in a side map.
//   2. After the main row INSERT/UPDATE commits, runs replace-mode
//      junction updates (DELETE owner_id=? + INSERT new edges) inside
//      the same tx. Sort order is preserved via sort_index.
//   3. On READ — `buildSelectColumns` emits a `COALESCE(array_agg ...
//      junction.ref_id ORDER BY sort_index)` subquery per Relations
//      field, returning a `[]string` UUID list. Clients see a stable
//      shape on the wire.
//
// Why replace-mode (DELETE+INSERT) instead of diff: a 100-edge update
// runs 1 DELETE + 1 INSERT instead of N upserts. Hash-based diffing
// would beat it for tiny incremental changes but the code complexity
// isn't worth it for v3. If a future profiler shows hot-path pain,
// the helper interface stays — only the implementation changes.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/railbase/railbase/internal/schema/builder"
)

// stripRelationsFromInsert removes every TypeRelations key from
// fields, side-stashing them in a map the caller picks up via
// extractRelationsPayload BEFORE calling buildInsert. Mutates in
// place. Idempotent; safe to call before BeforeCreate hooks run too.
//
// Why mutate instead of returning the side-map: buildInsert is called
// from multiple paths (create, batch-create, hook-tx) and threading a
// second return value through each would multiply call-site noise.
// The caller extracts the relations BEFORE this function is invoked
// (see createHandler) and stashes them where needed.
func stripRelationsFromInsert(spec builder.CollectionSpec, fields map[string]any) {
	for _, f := range spec.Fields {
		if f.Type == builder.TypeRelations {
			delete(fields, f.Name)
		}
	}
}

// extractRelationsPayload walks the request body BEFORE the INSERT
// is built and pulls out each TypeRelations field's value as a
// `[]string` of related IDs. Missing fields are absent from the
// returned map (NOT empty slice — empty slice means "replace with
// nothing"). Returns nil if the spec has no Relations fields.
//
// Validation: each value must be a JSON array of strings. Anything
// else is a 400 — silent coercion would hide bugs in the client.
func extractRelationsPayload(spec builder.CollectionSpec, fields map[string]any) (map[string][]string, error) {
	var out map[string][]string
	for _, f := range spec.Fields {
		if f.Type != builder.TypeRelations {
			continue
		}
		raw, present := fields[f.Name]
		if !present {
			continue // no change — junction stays as it was
		}
		ids, err := toRelationIDs(raw)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		if out == nil {
			out = make(map[string][]string)
		}
		out[f.Name] = ids
	}
	return out, nil
}

// toRelationIDs accepts the JSON-decoded representation of a
// TypeRelations input and returns the canonical []string of UUIDs.
// Accepts `[]any` (the encoding/json default for arrays) and
// `[]string` (forward-compat if a future codec hands us typed).
func toRelationIDs(v any) ([]string, error) {
	switch vv := v.(type) {
	case nil:
		return nil, nil // null ≡ empty replacement
	case []string:
		return vv, nil
	case []any:
		out := make([]string, len(vv))
		for i, x := range vv {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, want string (UUID)", i, x)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be an array of UUID strings, got %T", v)
}

// applyRelationsAfterWrite replaces junction-table rows for every
// (field → []uuid) pair the client supplied. Runs inside the
// caller's tx so atomicity matches the main row INSERT/UPDATE: a
// failed junction write rolls the whole record back.
//
// Empty value (`field: []`) wipes the relation set. Absent field
// (not in payload) leaves the existing rows alone — partial-PATCH
// semantics.
//
// Order preservation: each row's sort_index is its position in the
// supplied array, so a future SELECT `array_agg(... ORDER BY
// sort_index)` returns the client's original ordering.
func applyRelationsAfterWrite(
	ctx context.Context,
	tx pgx.Tx,
	owner string,
	ownerID string,
	payload map[string][]string,
) error {
	for field, ids := range payload {
		junction := owner + "_" + field
		// Replace mode: clear, then insert. The PRIMARY KEY guard on
		// (owner_id, ref_id) would refuse a stale-then-re-add via
		// upsert; cleaner to wipe-and-fill.
		if _, err := tx.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE owner_id = $1", junction),
			ownerID,
		); err != nil {
			return fmt.Errorf("relations %q: clear junction: %w", field, err)
		}
		if len(ids) == 0 {
			continue
		}
		// Bulk INSERT via VALUES list. pgx unfortunately doesn't have
		// a parametric-VALUES variadic, so we build $1, $2, ... ourselves.
		// All values are owner-scoped (single owner_id) so the args
		// layout is: [ownerID, ref0, ref1, ...].
		args := make([]any, 0, 1+len(ids))
		args = append(args, ownerID)
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = fmt.Sprintf("($1, $%d, %d)", i+2, i)
			args = append(args, id)
		}
		sql := fmt.Sprintf(
			"INSERT INTO %s (owner_id, ref_id, sort_index) VALUES %s",
			junction, joinComma(placeholders),
		)
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("relations %q: insert junction: %w", field, err)
		}
	}
	return nil
}

// joinComma is strings.Join(s, ", ") inlined — avoids a strings
// import here when this is the only call. Three-line helper; clearer
// than a global utility shim.
func joinComma(s []string) string {
	if len(s) == 0 {
		return ""
	}
	out := s[0]
	for i := 1; i < len(s); i++ {
		out += ", " + s[i]
	}
	return out
}
