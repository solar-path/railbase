package adminapi

// v0.9 — runtime collection management.
//
// These handlers let the admin UI create / edit / drop collections
// without a code deploy. They're a thin HTTP shell over
// internal/schema/live, which owns the transactional DDL + persistence
// + registry mutation. Code-defined collections are untouchable here —
// live.Update / live.Delete refuse anything without an
// _admin_collections row.
//
// All three routes sit inside the RequireAdmin group (see adminapi.go).

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/live"
)

// mountCollections wires the runtime collection routes. Nil-guarded on
// Deps.Pool so test Deps that omit the pool stay functional (same
// pattern as the api-tokens block in adminapi.go).
func (d *Deps) mountCollections(r chi.Router) {
	if d.Pool == nil {
		return
	}
	r.Post("/collections", d.collectionsCreateHandler)
	r.Patch("/collections/{name}", d.collectionsUpdateHandler)
	r.Delete("/collections/{name}", d.collectionsDeleteHandler)
}

// collectionsCreateHandler — POST /api/_admin/collections. Body is a
// serialised builder.CollectionSpec. On success the data table exists,
// the spec is persisted, and the collection is live for CRUD.
func (d *Deps) collectionsCreateHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := decodeSpec(r)
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	if err := live.Create(r.Context(), d.Pool, spec); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(spec)
}

// collectionsUpdateHandler — PATCH /api/_admin/collections/{name}. Body
// is the FULL desired spec (not a delta). live.Update diffs it against
// the persisted spec and refuses incompatible changes.
func (d *Deps) collectionsUpdateHandler(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "collection name is required"))
		return
	}
	spec, err := decodeSpec(r)
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	if err := live.Update(r.Context(), d.Pool, name, spec); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	spec.Name = name
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(spec)
}

// collectionsDeleteHandler — DELETE /api/_admin/collections/{name}.
// Drops the data table AND the persisted spec in one transaction.
// Idempotency is NOT offered: deleting an unknown / code-defined
// collection is a 400 so the operator notices a mistake.
func (d *Deps) collectionsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "collection name is required"))
		return
	}
	if err := live.Delete(r.Context(), d.Pool, name); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeSpec reads the request body into a CollectionSpec. Returns a
// ready-to-write *rerr.Error on malformed input.
func decodeSpec(r *http.Request) (builder.CollectionSpec, error) {
	body, err := readBody(r)
	if err != nil {
		return builder.CollectionSpec{}, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error())
	}
	var spec builder.CollectionSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		return builder.CollectionSpec{}, rerr.Wrap(err, rerr.CodeValidation,
			"body must be a valid collection spec: %s", err.Error())
	}
	return spec, nil
}
