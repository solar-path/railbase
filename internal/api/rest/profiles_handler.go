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
func (d *handlerDeps) publicProfileListHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := publicProfileSpec(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	cols := publicProfileColumns(spec)
	// Plain LIST — no pagination, no filter. Auth-collection profile
	// directories are typically small (10s-100s of rows) and embedders
	// can wire their own paginator atop if needed.
	q := fmt.Sprintf("SELECT %s FROM %s ORDER BY id ASC LIMIT 1000",
		quoteColumns(cols), spec.Name)
	rows, err := d.pool.Query(r.Context(), q)
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
	writeProfileJSON(w, map[string]any{
		"items": items,
		"count": len(items),
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
