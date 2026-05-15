package rest

// Phase 3.x — server-side validation of JSONB array fields with
// references. Closes the "FK validity ... enforced client-side"
// papercut Sentinel's schema/tasks.go:15 documents:
//
//	"FK validity (predecessor exists, same project) is enforced
//	 client-side; cycles are detected at CPM compute time."
//
// Coupling client-side and server-side validation invites drift —
// any curl-from-the-shell write skips the JS check. With
// JSONField.ArrayOfUUIDReferences + SameValueAs, the validation runs
// in the same tx as the INSERT/UPDATE: a single SQL roundtrip per
// constrained field counts hits and returns a friendly error when
// the count differs from the input length.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/railbase/railbase/internal/schema/builder"
)

// validateJSONArrayRefs walks every TypeJSON field with
// ArrayOfUUIDReferences set and confirms each UUID element exists in
// the referenced collection. When SameValueAs is also set, it
// additionally requires that each referenced row's `peer` column
// equals THIS row's `peer` column.
//
// Runs INSIDE the caller's tx so a failed check rolls back the
// pending row write. Returns a validation error (caller wraps to 422)
// or nil on success.
//
// peerValue is the row's value for the SameValueAs column — pulled
// from `fields` before the function runs. nil signals "this row
// doesn't have a peer value yet" (typical UPDATE shape); the check
// degrades to existence-only.
func validateJSONArrayRefs(
	ctx context.Context,
	tx pgx.Tx,
	spec builder.CollectionSpec,
	fields map[string]any,
) error {
	for _, f := range spec.Fields {
		if f.Type != builder.TypeJSON {
			continue
		}
		if f.JSONElementRefCollection == "" {
			continue
		}
		raw, present := fields[f.Name]
		if !present {
			continue
		}
		ids, err := jsonArrayToIDs(raw)
		if err != nil {
			return fmt.Errorf("field %q: %w", f.Name, err)
		}
		if len(ids) == 0 {
			continue
		}
		if err := checkExistsAll(ctx, tx, f.JSONElementRefCollection, ids); err != nil {
			return fmt.Errorf("field %q: %w", f.Name, err)
		}
		if f.JSONElementPeerEqual != "" {
			peerVal, peerPresent := fields[f.JSONElementPeerEqual]
			if !peerPresent {
				// UPDATE that didn't touch the peer column — we'd need
				// to fetch the existing row to know what value to
				// match. Skip; the operator can add a peer check in
				// a CreateRule/UpdateRule if needed.
				continue
			}
			if err := checkPeerEqualAll(ctx, tx, f.JSONElementRefCollection,
				f.JSONElementPeerEqual, peerVal, ids); err != nil {
				return fmt.Errorf("field %q: %w", f.Name, err)
			}
		}
	}
	return nil
}

// jsonArrayToIDs accepts the json-decoded form of a JSONB array and
// returns the string IDs. Same coercion as M2M (relations.go) but
// gated to JSON arrays (TypeJSON), which carry no `[]string` typing
// guarantee.
func jsonArrayToIDs(v any) ([]string, error) {
	switch vv := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return vv, nil
	case []any:
		out := make([]string, 0, len(vv))
		for i, x := range vv {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, want string (UUID)", i, x)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a JSON array of UUID strings, got %T", v)
}

// checkExistsAll runs `SELECT COUNT(*) FROM <target> WHERE id = ANY($1)`
// and confirms it matches len(ids). Faster than N round-trips; one
// query regardless of array size.
func checkExistsAll(ctx context.Context, tx pgx.Tx, target string, ids []string) error {
	sql := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id::text = ANY($1)`, target)
	var got int64
	if err := tx.QueryRow(ctx, sql, ids).Scan(&got); err != nil {
		return fmt.Errorf("ref-existence check %q: %w", target, err)
	}
	if got != int64(len(ids)) {
		return fmt.Errorf("%d of %d referenced %s row(s) do not exist (or are not visible)",
			int64(len(ids))-got, len(ids), target)
	}
	return nil
}

// checkPeerEqualAll confirms every referenced row's peer column
// equals the supplied value. Single SELECT counts mismatches.
//
// peerVal is JSON-decoded (could be string, number, bool, …); we
// rely on pgx parameter binding to do the right thing for the
// target column's actual type. Mismatched types surface as a
// pgx-level error, which we wrap.
func checkPeerEqualAll(
	ctx context.Context,
	tx pgx.Tx,
	target string,
	peerCol string,
	peerVal any,
	ids []string,
) error {
	// We compare via column-cast to text to dodge pgx coercion quirks
	// for UUID-typed peer columns when the caller's value is a string.
	// The result correctness still depends on canonical text forms
	// matching — JSON-decoded UUIDs are canonical, so we're fine.
	sql := fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE id::text = ANY($1) AND %s::text = $2`,
		target, peerCol,
	)
	var matched int64
	if err := tx.QueryRow(ctx, sql, ids, fmt.Sprintf("%v", peerVal)).Scan(&matched); err != nil {
		return fmt.Errorf("peer-equal check %q.%q: %w", target, peerCol, err)
	}
	if matched != int64(len(ids)) {
		return fmt.Errorf("%d of %d referenced %s row(s) have %s != %v",
			int64(len(ids))-matched, len(ids), target, peerCol, peerVal)
	}
	return nil
}

// _ keeps the strings import alive for future expansion of helper
// joining ids into IN-lists. Today checkExistsAll uses ANY($1) so
// the import is unused inside the function bodies; rather than
// remove + re-add later, we mark intent.
var _ = strings.Join
