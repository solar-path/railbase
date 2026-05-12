package adminapi

// v1.7.x §3.11 deferred — admin endpoint for browsing soft-deleted
// records across every collection that declared `.SoftDelete()`.
// Read-only counterpart to the per-collection REST restore endpoint
// (POST /api/collections/{name}/records/{id}/restore, exposed in
// v1.4.12). The admin UI surfaces a "Trash" screen on top of this
// listing; the per-row restore button hits the existing REST endpoint
// directly with the admin bearer.
//
// One endpoint:
//
//	GET /api/_admin/trash       — flat, cross-collection listing
//
// Query params:
//
//	page         1-indexed (default 1)
//	perPage      default 50, max 200
//	collection   filter to one collection (empty = all soft-delete
//	             collections)
//
// Response shape:
//
//	{
//	  "page": 1,
//	  "perPage": 50,
//	  "totalItems": 42,
//	  "items": [
//	    {"collection":"posts","id":"...","created":"...",
//	     "updated":"...","deleted":"..."},
//	    ...
//	  ],
//	  "collections": ["posts","comments"]
//	}
//
// `collections` at the envelope top level lists every collection that
// has `.SoftDelete()=true` in the registry, in deterministic order.
// The React filter dropdown reads this directly without a second
// round-trip; the field is independent of which rows are visible on
// the current page.
//
// Cross-collection ordering: we issue one SELECT per soft-delete
// collection (LIMIT page*perPage, ORDER BY deleted DESC) and merge
// the rows in Go by `deleted DESC`. A SQL `UNION ALL` would push the
// merge into the planner, but every soft-delete column lives in a
// different user-defined table so the UNION arms still each scan
// their own index — the win is theoretical, and the Go merge stays
// closer to how logs.go / jobs.go paginate today (slice in Go after
// over-fetching). For tiny `perPage` (≤200) and small N collections,
// the merge cost is negligible.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// trashItem is one entry in the listing — the four timestamps + id
// + the source collection name. We deliberately do NOT return the
// full row payload here: the table can be wide, the column types
// vary across collections, and the trash screen only needs enough
// to render "deleted X ago — collection/id — [Restore]".
type trashItem struct {
	Collection string     `json:"collection"`
	ID         string     `json:"id"`
	Created    time.Time  `json:"created"`
	Updated    time.Time  `json:"updated"`
	Deleted    time.Time  `json:"deleted"`
}

// specsForTrash returns the subset of registered collections that
// have `.SoftDelete()=true`, in registry order. Split out for easy
// stubbing under tests via the registryFn hook.
func specsForTrash() []builder.CollectionSpec {
	all := registry.Specs()
	out := make([]builder.CollectionSpec, 0, len(all))
	for _, s := range all {
		if s.SoftDelete {
			out = append(out, s)
		}
	}
	return out
}

func (d *Deps) trashListHandler(w http.ResponseWriter, r *http.Request) {
	const defaultPerPage = 50
	const maxPerPage = 200

	perPage := parseIntParam(r, "perPage", defaultPerPage)
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	page := parseIntParam(r, "page", 1)
	if page < 1 {
		page = 1
	}
	collectionFilter := r.URL.Query().Get("collection")

	// Enumerate soft-delete collections first — the response always
	// carries this list so the React filter dropdown can render even
	// when the current view has zero rows.
	softDelete := specsForTrash()
	collectionNames := make([]string, 0, len(softDelete))
	for _, s := range softDelete {
		collectionNames = append(collectionNames, s.Name)
	}

	// Apply the optional ?collection= filter. An unknown name yields
	// zero results (and a zero count) rather than 404 — the dropdown
	// stays useful across registry edits without redirecting.
	specsToQuery := softDelete
	if collectionFilter != "" {
		filtered := specsToQuery[:0:0]
		for _, s := range softDelete {
			if s.Name == collectionFilter {
				filtered = append(filtered, s)
				break
			}
		}
		specsToQuery = filtered
	}

	pool := d.Pool
	if pool == nil {
		// Empty registry path is harmless without a pool — but any
		// soft-delete collection requires a DB hit, so guard here
		// the same way jobs/notifications do.
		if len(specsToQuery) == 0 {
			writeTrashEmpty(w, page, perPage, collectionNames)
			return
		}
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "trash not configured"))
		return
	}

	// Over-fetch limit: same trick as logs/jobs. We pull page*perPage
	// from each collection, merge, then slice — keeps deep paging
	// bounded.
	limit := page * perPage
	if limit > maxPerPage {
		// Hard cap so a request for ?page=1000 doesn't melt the DB.
		// totalItems still reflects reality; the page just returns
		// nothing past the cap.
		limit = maxPerPage
	}

	var merged []trashItem
	var total int64
	for _, s := range specsToQuery {
		rows, err := pool.Query(r.Context(),
			fmt.Sprintf(
				"SELECT id::text, created, updated, deleted FROM %s "+
					"WHERE deleted IS NOT NULL "+
					"ORDER BY deleted DESC LIMIT $1",
				s.Name,
			),
			limit,
		)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query trash %s", s.Name))
			return
		}
		items, scanErr := scanTrashRows(rows, s.Name)
		if scanErr != nil {
			rerr.WriteJSON(w, rerr.Wrap(scanErr, rerr.CodeInternal, "scan trash %s", s.Name))
			return
		}
		merged = append(merged, items...)

		// Per-collection count for the totalItems aggregate. Cheap
		// because the partial alive-index leaves the dead set on a
		// covering scan; even on a million-row table the count is
		// bounded by the trash size, not the table size. Non-fatal
		// on error — fall back to len(merged) downstream.
		var c int64
		if err := pool.QueryRow(r.Context(),
			fmt.Sprintf("SELECT count(*) FROM %s WHERE deleted IS NOT NULL", s.Name),
		).Scan(&c); err == nil {
			total += c
		}
	}

	// Merge sort by deleted DESC across collections. After
	// per-collection ORDER BY, each slice is already sorted; a stable
	// sort here gives deterministic ordering when two rows share the
	// same deleted timestamp (rare but possible — the ordering then
	// falls back to insertion order, which is collection order from
	// the registry).
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Deleted.After(merged[j].Deleted)
	})

	// If no count succeeded, fall back to what we saw so the page
	// still renders. Logs/jobs use the same fallback.
	if total == 0 {
		total = int64(len(merged))
	}

	// Slice to the requested page window. Same as logs/jobs.
	start := (page - 1) * perPage
	if start > len(merged) {
		merged = nil
	} else {
		merged = merged[start:]
		if len(merged) > perPage {
			merged = merged[:perPage]
		}
	}
	if merged == nil {
		merged = []trashItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":        page,
		"perPage":     perPage,
		"totalItems":  total,
		"items":       merged,
		"collections": collectionNames,
	})
}

// scanTrashRows materialises rows from one collection's trash SELECT.
// Split out for testability — the SQL is fixed-column (id, created,
// updated, deleted), so the scan is trivial.
func scanTrashRows(rows pgx.Rows, collection string) ([]trashItem, error) {
	defer rows.Close()
	var out []trashItem
	for rows.Next() {
		var (
			id                      string
			created, updated, deleted time.Time
		)
		if err := rows.Scan(&id, &created, &updated, &deleted); err != nil {
			return nil, err
		}
		out = append(out, trashItem{
			Collection: collection,
			ID:         id,
			Created:    created,
			Updated:    updated,
			Deleted:    deleted,
		})
	}
	return out, rows.Err()
}

// writeTrashEmpty is the early-return path when the registry has no
// soft-delete collections at all (pool may also be nil). Keeps the
// nil-pool branch tidy in the main handler.
func writeTrashEmpty(w http.ResponseWriter, page, perPage int, collections []string) {
	if collections == nil {
		collections = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":        page,
		"perPage":     perPage,
		"totalItems":  0,
		"items":       []trashItem{},
		"collections": collections,
	})
}
