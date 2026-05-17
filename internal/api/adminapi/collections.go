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
//
// FEEDBACK loadtest #7 — auth collections (auth: true) are accepted
// when the body also carries `"confirm_irreversible": true`. The flag
// is consumed here; the spec proceeds unchanged.
func (d *Deps) collectionsCreateHandler(w http.ResponseWriter, r *http.Request) {
	spec, confirm, err := decodeSpecWithConfirm(r)
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	if err := live.CreateWithOptions(r.Context(), d.Pool, spec,
		live.CreateOptions{AuthConfirmIrreversible: confirm},
	); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	// FEEDBACK loadtest #5 — surface a hint when the spec was created
	// with empty rules. Comments in code said "empty = no rule"; reality
	// is "empty = LOCKED". Sending the spec back with `warnings` makes
	// the gap impossible to miss in the admin UI.
	resp := map[string]any{
		"record": spec,
	}
	if warns := lockedRuleWarnings(spec); len(warns) > 0 {
		resp["warnings"] = warns
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// lockedRuleWarnings returns one warning per CRUD action whose rule
// is empty (= constant-FALSE predicate = action locked for all
// callers). FEEDBACK loadtest #5.
func lockedRuleWarnings(spec builder.CollectionSpec) []map[string]string {
	type pair struct{ name, rule string }
	checks := []pair{
		{"list", spec.Rules.List},
		{"view", spec.Rules.View},
		{"create", spec.Rules.Create},
		{"update", spec.Rules.Update},
		{"delete", spec.Rules.Delete},
	}
	var out []map[string]string
	for _, c := range checks {
		if c.rule == "" {
			out = append(out, map[string]string{
				"code":   "rule_locked",
				"action": c.name,
				"hint":   `empty rule = LOCKED (no API access). Set rule to "true" for unconditional access, or to a filter like '@request.auth.id != ""' for any authenticated caller.`,
			})
		}
	}
	return out
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
	spec, _, err := decodeSpecWithConfirm(r)
	return spec, err
}

// decodeSpecWithConfirm parses the request body into a CollectionSpec
// and extracts the auxiliary `confirm_irreversible` flag (which is
// NOT part of the spec but rides on the same JSON body for ergonomic
// reasons). FEEDBACK loadtest #7.
func decodeSpecWithConfirm(r *http.Request) (builder.CollectionSpec, bool, error) {
	body, err := readBody(r)
	if err != nil {
		return builder.CollectionSpec{}, false, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error())
	}
	var spec builder.CollectionSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		return builder.CollectionSpec{}, false, rerr.Wrap(err, rerr.CodeValidation,
			"body must be a valid collection spec: %s", err.Error())
	}
	// Optional confirm flag — decoded separately so we don't pollute
	// the spec struct with a write-only request field.
	var aux struct {
		ConfirmIrreversible bool `json:"confirm_irreversible"`
	}
	_ = json.Unmarshal(body, &aux)
	return spec, aux.ConfirmIrreversible, nil
}
