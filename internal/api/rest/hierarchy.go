package rest

import (
	"context"
	"fmt"

	"github.com/railbase/railbase/internal/schema/builder"
)

// hierarchyPreInsert runs the v1.5.12 AdjacencyList/Ordered preprocessing
// for INSERT requests:
//
//  1. AdjacencyList cycle/depth check — if `parent` is set, walk the
//     candidate parent chain via recursive CTE; reject if the chain
//     exceeds MaxDepth (since brand-new rows can't form cycles with
//     themselves, the only failure path here is "parent chain already
//     too deep"). The candidate self-loop case (`parent = own id`) is
//     impossible on INSERT — the id is server-assigned.
//
//  2. Ordered auto-assignment — if `sort_index` is absent (or absent
//     for this parent), assign MAX(sort_index)+1 within the same
//     parent (or globally when AdjacencyList is off).
//
// Mutates `fields` in place. Returns a 400-friendly error on policy
// violation; nil otherwise.
func hierarchyPreInsert(ctx context.Context, q pgQuerier, spec builder.CollectionSpec, fields map[string]any) error {
	if spec.AdjacencyList {
		if pv, ok := fields["parent"]; ok && pv != nil {
			pid, isStr := pv.(string)
			if !isStr || pid == "" {
				return fmt.Errorf("field \"parent\": expected non-empty UUID string")
			}
			if err := walkParentDepth(ctx, q, spec, pid, spec.MaxDepth); err != nil {
				return err
			}
		}
	}
	if spec.Ordered {
		if _, ok := fields["sort_index"]; !ok {
			next, err := nextSortIndex(ctx, q, spec, fields["parent"])
			if err != nil {
				return err
			}
			fields["sort_index"] = next
		}
	}
	return nil
}

// hierarchyPreUpdate runs the cycle / depth check for UPDATE. Unlike
// INSERT, here the candidate parent can ALSO equal the row being
// updated, OR be any descendant of it — both form a cycle. We walk
// the candidate's chain looking for either:
//
//   - the row's own id → direct or indirect cycle
//   - chain length > MaxDepth → depth violation
//
// Sort_index has no auto-assign on UPDATE — clients are explicit.
//
// `id` is the row being updated. `fields` is the patch (only contains
// keys the client sent). Returns 400 on violation.
func hierarchyPreUpdate(ctx context.Context, q pgQuerier, spec builder.CollectionSpec, id string, fields map[string]any) error {
	if !spec.AdjacencyList {
		return nil
	}
	pv, ok := fields["parent"]
	if !ok {
		return nil // parent unchanged
	}
	if pv == nil {
		return nil // clearing parent — always safe
	}
	pid, isStr := pv.(string)
	if !isStr || pid == "" {
		return fmt.Errorf("field \"parent\": expected non-empty UUID string")
	}
	if pid == id {
		return fmt.Errorf("field \"parent\": cycle (cannot parent a row to itself)")
	}
	// Walk pid's chain — if we hit `id` it's a cycle. Bound by MaxDepth.
	return walkParentForCycle(ctx, q, spec, pid, id, spec.MaxDepth)
}

// walkParentDepth verifies that the chain starting at `start` (the
// candidate parent) plus one more level (the new row itself) doesn't
// exceed maxDepth. maxDepth == 0 means unbounded.
//
// The recursive CTE caps itself one step past max so we can detect
// overflow without scanning the whole chain.
func walkParentDepth(ctx context.Context, q pgQuerier, spec builder.CollectionSpec, start string, maxDepth int) error {
	if maxDepth <= 0 {
		// Unbounded — only need to verify the parent exists. A missing
		// parent would surface as an FK error from PG anyway, but
		// catching it here gives a friendlier message.
		var exists bool
		row := q.QueryRow(ctx,
			fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE id = $1::uuid)", quoteIdent(spec.Name)),
			start)
		if err := row.Scan(&exists); err != nil {
			return fmt.Errorf("field \"parent\": lookup failed: %w", err)
		}
		if !exists {
			return fmt.Errorf("field \"parent\": no row with id %q", start)
		}
		return nil
	}
	// Recursive CTE that walks parent links up to maxDepth+1 levels.
	// If the chain length equals maxDepth (no further parent) the
	// candidate sits at depth (maxDepth - 1), so adding our new child
	// would push to maxDepth → REJECT.
	//
	// Convention: depth 1 == the candidate itself, depth 2 == its parent,
	// etc. The new child would be at depth 0 (one below the candidate),
	// so the chain length we report includes the new child.
	var depth int
	row := q.QueryRow(ctx,
		fmt.Sprintf(`
WITH RECURSIVE chain(id, depth) AS (
  SELECT id, 1 FROM %s WHERE id = $1::uuid
  UNION ALL
  SELECT t.parent, c.depth + 1
  FROM chain c JOIN %s t ON t.id = c.id
  WHERE t.parent IS NOT NULL AND c.depth < $2
)
SELECT COALESCE(MAX(depth), 0) FROM chain`,
			quoteIdent(spec.Name), quoteIdent(spec.Name)),
		start, maxDepth+1)
	if err := row.Scan(&depth); err != nil {
		return fmt.Errorf("field \"parent\": chain walk failed: %w", err)
	}
	if depth == 0 {
		return fmt.Errorf("field \"parent\": no row with id %q", start)
	}
	// chain depth already includes the candidate row. +1 for the new
	// child → reject if that exceeds maxDepth.
	if depth+1 > maxDepth {
		return fmt.Errorf("field \"parent\": depth %d would exceed MaxDepth %d", depth+1, maxDepth)
	}
	return nil
}

// walkParentForCycle walks `start`'s parent chain looking for
// `forbidden` (the row being updated). Returns an error if found, or
// if depth exceeds maxDepth. maxDepth == 0 → unbounded.
func walkParentForCycle(ctx context.Context, q pgQuerier, spec builder.CollectionSpec, start, forbidden string, maxDepth int) error {
	limit := maxDepth + 1
	if maxDepth <= 0 {
		// Safety ceiling for unbounded mode — pathological data could
		// otherwise infinite-loop. 1024 is well past any sane tree depth.
		limit = 1024
	}
	var hit bool
	row := q.QueryRow(ctx,
		fmt.Sprintf(`
WITH RECURSIVE chain(id, depth) AS (
  SELECT id, 1 FROM %s WHERE id = $1::uuid
  UNION ALL
  SELECT t.parent, c.depth + 1
  FROM chain c JOIN %s t ON t.id = c.id
  WHERE t.parent IS NOT NULL AND c.depth < $2
)
SELECT EXISTS(SELECT 1 FROM chain WHERE id = $3::uuid)`,
			quoteIdent(spec.Name), quoteIdent(spec.Name)),
		start, limit, forbidden)
	if err := row.Scan(&hit); err != nil {
		return fmt.Errorf("field \"parent\": cycle check failed: %w", err)
	}
	if hit {
		return fmt.Errorf("field \"parent\": cycle (parent chain would loop through this row)")
	}
	if maxDepth > 0 {
		var depth int
		row := q.QueryRow(ctx,
			fmt.Sprintf(`
WITH RECURSIVE chain(id, depth) AS (
  SELECT id, 1 FROM %s WHERE id = $1::uuid
  UNION ALL
  SELECT t.parent, c.depth + 1
  FROM chain c JOIN %s t ON t.id = c.id
  WHERE t.parent IS NOT NULL AND c.depth < $2
)
SELECT COALESCE(MAX(depth), 0) FROM chain`,
				quoteIdent(spec.Name), quoteIdent(spec.Name)),
			start, maxDepth+1)
		if err := row.Scan(&depth); err != nil {
			return fmt.Errorf("field \"parent\": chain walk failed: %w", err)
		}
		if depth+1 > maxDepth {
			return fmt.Errorf("field \"parent\": depth %d would exceed MaxDepth %d", depth+1, maxDepth)
		}
	}
	return nil
}

// nextSortIndex returns MAX(sort_index)+1 within the same parent
// scope. When AdjacencyList is off, scope is the whole collection.
// When parent is nil/missing, scope is "rows with parent IS NULL".
func nextSortIndex(ctx context.Context, q pgQuerier, spec builder.CollectionSpec, parent any) (int64, error) {
	var max int64
	if spec.AdjacencyList && parent != nil {
		pid, ok := parent.(string)
		if !ok || pid == "" {
			// shouldn't happen — coerceForPG would have rejected — but
			// fall back to the no-parent scope rather than crashing.
			row := q.QueryRow(ctx,
				fmt.Sprintf("SELECT COALESCE(MAX(sort_index), -1) FROM %s WHERE parent IS NULL",
					quoteIdent(spec.Name)))
			if err := row.Scan(&max); err != nil {
				return 0, fmt.Errorf("sort_index lookup failed: %w", err)
			}
			return max + 1, nil
		}
		row := q.QueryRow(ctx,
			fmt.Sprintf("SELECT COALESCE(MAX(sort_index), -1) FROM %s WHERE parent = $1::uuid",
				quoteIdent(spec.Name)),
			pid)
		if err := row.Scan(&max); err != nil {
			return 0, fmt.Errorf("sort_index lookup failed: %w", err)
		}
		return max + 1, nil
	}
	if spec.AdjacencyList {
		row := q.QueryRow(ctx,
			fmt.Sprintf("SELECT COALESCE(MAX(sort_index), -1) FROM %s WHERE parent IS NULL",
				quoteIdent(spec.Name)))
		if err := row.Scan(&max); err != nil {
			return 0, fmt.Errorf("sort_index lookup failed: %w", err)
		}
		return max + 1, nil
	}
	row := q.QueryRow(ctx,
		fmt.Sprintf("SELECT COALESCE(MAX(sort_index), -1) FROM %s",
			quoteIdent(spec.Name)))
	if err := row.Scan(&max); err != nil {
		return 0, fmt.Errorf("sort_index lookup failed: %w", err)
	}
	return max + 1, nil
}

// quoteIdent quotes a SQL identifier. Mirrors the gen-package helper
// (kept here so the rest package doesn't import gen for one helper).
// The schema validator already rejects identifiers containing `"` so
// this is a pure quote-wrap.
func quoteIdent(name string) string {
	return "\"" + name + "\""
}
