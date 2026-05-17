package rest

// FEEDBACK #B2 — read-only public-profile endpoints for AuthCollections
// that opt-in via `.PublicProfile()`. The blogger project needed to
// render bylines (name + avatar) for anonymous visitors but the
// canonical /records CRUD is forbidden on auth collections (correctly:
// password_hash/token_key shouldn't be writable through it). This
// surface fills the gap by exposing ONLY the non-secret user-declared
// columns.
//
//   GET /api/collections/{name}/profiles      → list
//   GET /api/collections/{name}/profiles/{id} → single record
//
// Both 404 when the collection isn't an opted-in AuthCollection — same
// shape as a missing collection, so probers can't enumerate which auth
// collections exist by checking for 404 vs 403.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// publicProfileSpec returns the collection spec if the named
// collection is an AuthCollection with PublicProfile() opted in;
// otherwise an error suitable for a 404. Probers can't tell which
// branch fired — the response shape is the same.
func publicProfileSpec(name string) (builder.CollectionSpec, error) {
	c := registry.Get(name)
	if c == nil {
		return builder.CollectionSpec{}, rerr.New(rerr.CodeNotFound, "profile collection %q not found", name)
	}
	spec := c.Spec()
	if !spec.Auth || !spec.PublicProfile {
		return builder.CollectionSpec{}, rerr.New(rerr.CodeNotFound, "profile collection %q not found", name)
	}
	return spec, nil
}

// publicProfileColumns returns the set of columns the profile
// endpoint exposes: `id` + every user-declared field except M2M
// relations (which live in junction tables). System columns like
// email, password_hash, token_key, verified, last_login_at, created,
// updated are NEVER returned by these handlers.
func publicProfileColumns(spec builder.CollectionSpec) []string {
	cols := []string{"id"}
	for _, f := range spec.Fields {
		if f.Type == builder.TypeRelations {
			continue
		}
		cols = append(cols, f.Name)
	}
	return cols
}

// quoteColumns produces `"a", "b", ...` from a column-name slice.
// Caller ensures names came from a validated spec.
func quoteColumns(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	return strings.Join(out, ", ")
}

// publicProfileListHandler — GET /api/collections/{name}/profiles
//
// FEEDBACK blogger N1/N2 — accepts the same page/perPage/sort that
// /records does and emits the same response envelope shape (page,
// perPage, totalItems, totalPages, items). Filter is intentionally
// NOT supported here: the profile surface is meant to be a
// publicly-readable directory, not a filtered query target — embedders
// who need filtering should expose a regular collection endpoint with
// a ListRule.
//
// Legacy "count" field is retained alongside the new fields so existing
// clients keep working through a transition window.
func (d *handlerDeps) publicProfileListHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := publicProfileSpec(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	cols := publicProfileColumns(spec)

	// Parse pagination — mirror the /records handler bounds.
	uq := r.URL.Query()
	page, _ := strconv.Atoi(uq.Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(uq.Get("perPage"))
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	offset := (page - 1) * perPage

	// totalItems via COUNT — profile tables are typically small (10s
	// to low 1000s) so the cost is bounded. Embedders with large
	// auth tables shouldn't use /profiles directly anyway.
	var total int64
	if err := d.pool.QueryRow(r.Context(),
		fmt.Sprintf("SELECT COUNT(*) FROM %s", spec.Name)).Scan(&total); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count profiles"))
		return
	}

	q := fmt.Sprintf("SELECT %s FROM %s ORDER BY id ASC LIMIT $1 OFFSET $2",
		quoteColumns(cols), spec.Name)
	rows, err := d.pool.Query(r.Context(), q, perPage, offset)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query profile list"))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0, 64)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan profile row"))
			return
		}
		out := make(map[string]any, len(cols)+1)
		out["collectionName"] = spec.Name
		for i, name := range cols {
			out[name] = stringifyUUID(values[i])
		}
		items = append(items, out)
	}

	totalPages := int64(0)
	if perPage > 0 {
		totalPages = (total + int64(perPage) - 1) / int64(perPage)
	}
	writeProfileJSON(w, map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": total,
		"totalPages": totalPages,
		"items":      items,
		// Legacy field — number of items in this page. Same value as
		// `len(items)`; preserved so consumers using `{count, items}`
		// (pre-v0.6) keep working without changes.
		"count":      len(items),
	})
}

// publicProfileGetHandler — GET /api/collections/{name}/profiles/{id}
func (d *handlerDeps) publicProfileGetHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := publicProfileSpec(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "profile not found"))
		return
	}
	cols := publicProfileColumns(spec)
	q := fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", quoteColumns(cols), spec.Name)
	rows, err := d.pool.Query(r.Context(), q, id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query profile"))
		return
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil && !errors.Is(rows.Err(), pgx.ErrNoRows) {
			rerr.WriteJSON(w, rerr.Wrap(rows.Err(), rerr.CodeInternal, "scan profile"))
			return
		}
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "profile not found"))
		return
	}
	values, err := rows.Values()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan profile"))
		return
	}
	out := make(map[string]any, len(cols)+1)
	out["collectionName"] = spec.Name
	for i, name := range cols {
		out[name] = stringifyUUID(values[i])
	}
	writeProfileJSON(w, out)
}

// writeProfileJSON encodes v as JSON and sets the content-type. Tiny
// helper to avoid sprinkling boilerplate across the two handlers.
func writeProfileJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// stringifyUUID converts a pgx UUID value (often returned as [16]byte)
// to its canonical text form so the JSON response matches the rest
// of the API. Other types pass through unchanged.
func stringifyUUID(v any) any {
	switch u := v.(type) {
	case [16]byte:
		return uuid.UUID(u).String()
	case uuid.UUID:
		return u.String()
	default:
		return v
	}
}
