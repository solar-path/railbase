package adminapi

// Admin REST for the v2.0-alpha DoA delegation primitive
// (internal/authority — Slice 1 hardening).
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /authority/delegations          list (filter by delegator/delegatee/tenant/status)
//	POST   /authority/delegations          create
//	GET    /authority/delegations/{id}     read
//	POST   /authority/delegations/{id}/revoke   active → revoked
//
// Delegations have no PATCH — they're immutable in shape once created
// (mirror of approval matrices' "approve = freeze" pattern). Edits =
// revoke + create a fresh one.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/authority"
	rerr "github.com/railbase/railbase/internal/errors"
)

func (d *Deps) mountDelegations(r chi.Router) {
	if d.Authority == nil {
		return
	}
	r.Get("/authority/delegations", d.delegationsListHandler)
	r.Post("/authority/delegations", d.delegationsCreateHandler)
	r.Get("/authority/delegations/{id}", d.delegationsGetHandler)
	r.Post("/authority/delegations/{id}/revoke", d.delegationsRevokeHandler)
}

// ── request shapes ───────────────────────────────────────────────────

type delegationRequest struct {
	DelegatorID      string     `json:"delegator_id"`
	DelegateeID      string     `json:"delegatee_id"`
	TenantID         string     `json:"tenant_id,omitempty"`
	SourceActionKeys []string   `json:"source_action_keys,omitempty"`
	MaxAmount        *int64     `json:"max_amount,omitempty"`
	EffectiveFrom    string     `json:"effective_from,omitempty"` // ISO 8601
	EffectiveTo      string     `json:"effective_to,omitempty"`
	Notes            string     `json:"notes,omitempty"`
}

type delegationRevokeRequest struct {
	Reason string `json:"reason"`
}

// ── handlers ────────────────────────────────────────────────────────

func (d *Deps) delegationsListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	f := authority.DelegationFilter{Status: r.URL.Query().Get("status")}
	if s := r.URL.Query().Get("delegator_id"); s != "" {
		u, err := uuid.Parse(s)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "delegator_id must be UUID"))
			return
		}
		f.DelegatorID = &u
	}
	if s := r.URL.Query().Get("delegatee_id"); s != "" {
		u, err := uuid.Parse(s)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "delegatee_id must be UUID"))
			return
		}
		f.DelegateeID = &u
	}
	if s := r.URL.Query().Get("tenant_id"); s != "" {
		u, err := uuid.Parse(s)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "tenant_id must be UUID"))
			return
		}
		f.TenantID = &u
	}

	rows, err := d.Authority.ListDelegations(r.Context(), f)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list delegations"))
		return
	}
	if rows == nil {
		rows = []authority.Delegation{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": rows})
}

func (d *Deps) delegationsCreateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	var req delegationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}

	p := AdminPrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "admin authentication required"))
		return
	}
	in, perr := buildDelegationInput(req, p.AdminID)
	if perr != nil {
		rerr.WriteJSON(w, perr)
		return
	}

	row, err := d.Authority.CreateDelegation(r.Context(), in)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	d.AuthorityAudit.DelegationCreated(r.Context(), authorityActor(r), row)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"record": row})
}

func (d *Deps) delegationsGetHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	row, err := d.Authority.GetDelegation(r.Context(), id)
	if err != nil {
		if errors.Is(err, authority.ErrDelegationNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "delegation not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "get delegation"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"record": row})
}

func (d *Deps) delegationsRevokeHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	var req delegationRevokeRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
			return
		}
	}
	if req.Reason == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "reason is required"))
		return
	}
	p := AdminPrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "admin principal required"))
		return
	}
	if err := d.Authority.RevokeDelegation(r.Context(), id, p.AdminID, req.Reason); err != nil {
		if errors.Is(err, authority.ErrDelegationNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "delegation not found"))
			return
		}
		if errors.Is(err, authority.ErrDelegationTerminal) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "delegation already revoked"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "revoke delegation"))
		return
	}
	d.AuthorityAudit.DelegationRevoked(r.Context(), authorityActor(r), id, req.Reason)
	row, _ := d.Authority.GetDelegation(r.Context(), id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"record": row})
}

// buildDelegationInput translates the wire request into the
// Store-input shape with full validation. Returns a structured
// rerr.Error on any malformed input.
func buildDelegationInput(req delegationRequest, createdBy uuid.UUID) (authority.DelegationCreateInput, *rerr.Error) {
	out := authority.DelegationCreateInput{
		SourceActionKeys: req.SourceActionKeys,
		MaxAmount:        req.MaxAmount,
		Notes:            req.Notes,
	}
	if req.DelegatorID == "" {
		return out, rerr.New(rerr.CodeValidation, "delegator_id is required")
	}
	if req.DelegateeID == "" {
		return out, rerr.New(rerr.CodeValidation, "delegatee_id is required")
	}
	delegator, err := uuid.Parse(req.DelegatorID)
	if err != nil {
		return out, rerr.New(rerr.CodeValidation, "delegator_id must be UUID")
	}
	delegatee, err := uuid.Parse(req.DelegateeID)
	if err != nil {
		return out, rerr.New(rerr.CodeValidation, "delegatee_id must be UUID")
	}
	if delegator == delegatee {
		return out, rerr.New(rerr.CodeValidation, "delegator must differ from delegatee")
	}
	out.DelegatorID = delegator
	out.DelegateeID = delegatee

	if req.TenantID != "" {
		t, err := uuid.Parse(req.TenantID)
		if err != nil {
			return out, rerr.New(rerr.CodeValidation, "tenant_id must be UUID")
		}
		out.TenantID = &t
	}
	if req.EffectiveFrom != "" {
		t, err := time.Parse(time.RFC3339, req.EffectiveFrom)
		if err != nil {
			return out, rerr.New(rerr.CodeValidation, "effective_from must be ISO 8601: %s", err.Error())
		}
		out.EffectiveFrom = &t
	}
	if req.EffectiveTo != "" {
		t, err := time.Parse(time.RFC3339, req.EffectiveTo)
		if err != nil {
			return out, rerr.New(rerr.CodeValidation, "effective_to must be ISO 8601: %s", err.Error())
		}
		out.EffectiveTo = &t
	}
	if createdBy != uuid.Nil {
		c := createdBy
		out.CreatedBy = &c
	}
	return out, nil
}
