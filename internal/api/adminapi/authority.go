package adminapi

// v2.0-alpha — admin endpoints for the DoA (Delegation of Authority)
// approval-matrix subsystem (internal/authority).
//
// *** PROTOTYPE — NOT FOR PRODUCTION USE (Slice 0). ***
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /authority/matrices          list matrices (filterable by status/key)
//	POST   /authority/matrices          create a new draft matrix (with levels + approvers)
//	GET    /authority/matrices/{id}     full matrix body (levels + approvers populated)
//	PATCH  /authority/matrices/{id}     update DRAFT matrix (409 if approved/archived/revoked)
//	DELETE /authority/matrices/{id}     delete DRAFT matrix (409 if not draft)
//
// Deferred to subsequent ticks (Slice 0 progression):
//   - POST /authority/matrices/{id}/approve   draft → approved (immutable from then)
//   - POST /authority/matrices/{id}/revoke    approved → revoked (with required reason)
//   - POST /authority/matrices/{id}/version   create version+1 in draft from approved
//   - GET  /authority/matrices/{id}/versions  version history
//
// The full DoA workflow surface (workflow CRUD, /sign endpoints, gate
// middleware, consume validation) lives in its own ticks — see
// plan.md §6.1 + docs/26-authority.md.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/authority"
	rerr "github.com/railbase/railbase/internal/errors"
)

// mountAuthority registers the DoA admin surface when the Store is
// wired. Nil d.Authority (bare test Deps) skips every route — clean
// 404 instead of a nil-deref.
func (d *Deps) mountAuthority(r chi.Router) {
	if d.Authority == nil {
		return
	}
	r.Get("/authority/matrices", d.authorityMatricesListHandler)
	r.Post("/authority/matrices", d.authorityMatricesCreateHandler)
	r.Get("/authority/matrices/{id}", d.authorityMatricesGetHandler)
	r.Patch("/authority/matrices/{id}", d.authorityMatricesUpdateHandler)
	r.Delete("/authority/matrices/{id}", d.authorityMatricesDeleteHandler)

	// Lifecycle transitions.
	r.Post("/authority/matrices/{id}/approve", d.authorityMatricesApproveHandler)
	r.Post("/authority/matrices/{id}/revoke", d.authorityMatricesRevokeHandler)
	r.Post("/authority/matrices/{id}/version", d.authorityMatricesVersionHandler)

	// Operator debuggability — selection preview (Slice 0 findings §3.3).
	// "Given this filter, which matrix would the gate select right now?"
	r.Get("/authority/matrices/preview", d.authorityMatricesPreviewHandler)

	// v2.0-alpha — DoA delegation primitive (Slice 1 hardening).
	d.mountDelegations(r)
}

// ── request/response shapes ───────────────────────────────────────

// matrixApproverRequest is the JSON shape for one approver on a
// level. ApproverType must be "role" or "user" in Slice 0;
// "position" / "department_head" → 400.
type matrixApproverRequest struct {
	ApproverType string `json:"approver_type"`
	ApproverRef  string `json:"approver_ref"`
	AutoResolve  bool   `json:"auto_resolve,omitempty"`
}

// matrixLevelRequest is one approval level config.
type matrixLevelRequest struct {
	LevelN          int                     `json:"level_n"`
	Name            string                  `json:"name"`
	Mode            string                  `json:"mode"` // any | all | threshold
	MinApprovals    *int                    `json:"min_approvals,omitempty"`
	EscalationHours *int                    `json:"escalation_hours,omitempty"`
	Approvers       []matrixApproverRequest `json:"approvers"`
}

// matrixRequest is the create/update body. Fields are validated in
// handler body; an empty / zero-value field is a "leave as default"
// instruction except for required ones (Key, Name, Levels).
type matrixRequest struct {
	TenantID          *uuid.UUID `json:"tenant_id,omitempty"`
	Key               string     `json:"key"`
	Version           int        `json:"version,omitempty"`
	Name              string     `json:"name"`
	Description       string     `json:"description,omitempty"`

	EffectiveFrom     *string    `json:"effective_from,omitempty"`     // ISO 8601 — parsed
	EffectiveTo       *string    `json:"effective_to,omitempty"`

	MinAmount         *int64     `json:"min_amount,omitempty"`
	MaxAmount         *int64     `json:"max_amount,omitempty"`
	Currency          string     `json:"currency,omitempty"`
	ConditionExpr     string     `json:"condition_expr,omitempty"`

	OnFinalEscalation string     `json:"on_final_escalation,omitempty"` // expire | reject

	Levels            []matrixLevelRequest `json:"levels"`
}

// ── handlers ───────────────────────────────────────────────────────

func (d *Deps) authorityMatricesListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	f := authority.ListFilter{
		Status: r.URL.Query().Get("status"),
		Key:    r.URL.Query().Get("key"),
	}
	if tStr := r.URL.Query().Get("tenant_id"); tStr != "" {
		if t, err := uuid.Parse(tStr); err == nil {
			f.TenantID = &t
		}
	}
	rows, err := d.Authority.ListMatrices(r.Context(), f)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list matrices"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orEmptyMatrices(rows)})
}

func (d *Deps) authorityMatricesCreateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	var req matrixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	m, validationErr := buildMatrixFromRequest(&req)
	if validationErr != nil {
		rerr.WriteJSON(w, validationErr)
		return
	}
	if err := d.Authority.CreateMatrix(r.Context(), nil, m); err != nil {
		if errors.Is(err, authority.ErrUnsupportedApproverType) {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "unsupported approver type"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create matrix"))
		return
	}
	d.AuthorityAudit.MatrixCreated(r.Context(), authorityActor(r), m)
	writeJSON(w, http.StatusCreated, map[string]any{"record": m})
}

func (d *Deps) authorityMatricesGetHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	m, err := d.Authority.GetMatrix(r.Context(), id)
	if err != nil {
		if errors.Is(err, authority.ErrMatrixNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "matrix not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "get matrix"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": m})
}

func (d *Deps) authorityMatricesUpdateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	var req matrixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	m, validationErr := buildMatrixFromRequest(&req)
	if validationErr != nil {
		rerr.WriteJSON(w, validationErr)
		return
	}
	m.ID = id
	if err := d.Authority.UpdateDraftMatrix(r.Context(), m); err != nil {
		if errors.Is(err, authority.ErrMatrixNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "matrix not found"))
			return
		}
		if errors.Is(err, authority.ErrMatrixImmutable) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
				"matrix is not in draft status; create a new version to edit"))
			return
		}
		if errors.Is(err, authority.ErrUnsupportedApproverType) {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "unsupported approver type"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update matrix"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": m})
}

func (d *Deps) authorityMatricesDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	if err := d.Authority.DeleteDraftMatrix(r.Context(), id); err != nil {
		if errors.Is(err, authority.ErrMatrixNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "matrix not found"))
			return
		}
		if errors.Is(err, authority.ErrMatrixImmutable) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
				"matrix is not in draft status; revoke instead of delete"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete matrix"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── lifecycle handlers ────────────────────────────────────────────

type matrixApproveRequest struct {
	EffectiveFrom string `json:"effective_from"` // required, ISO 8601
	EffectiveTo   string `json:"effective_to,omitempty"`
}

func (d *Deps) authorityMatricesApproveHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	p := AdminPrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "admin authentication required"))
		return
	}
	var req matrixApproveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if req.EffectiveFrom == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "effective_from is required"))
		return
	}
	effFrom, err := time.Parse(time.RFC3339, req.EffectiveFrom)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"effective_from: invalid ISO 8601: %v", err))
		return
	}
	var effTo *time.Time
	if req.EffectiveTo != "" {
		t, err := time.Parse(time.RFC3339, req.EffectiveTo)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"effective_to: invalid ISO 8601: %v", err))
			return
		}
		if !t.After(effFrom) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"effective_to must be after effective_from"))
			return
		}
		effTo = &t
	}

	if err := d.Authority.ApproveMatrix(r.Context(), id, p.AdminID, effFrom, effTo); err != nil {
		if errors.Is(err, authority.ErrMatrixNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "matrix not found"))
			return
		}
		if errors.Is(err, authority.ErrMatrixImmutable) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
				"matrix is not in draft status; only drafts can be approved"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "approve matrix"))
		return
	}
	m, _ := d.Authority.GetMatrix(r.Context(), id)
	if m != nil {
		d.AuthorityAudit.MatrixApproved(r.Context(), authorityActor(r), m.ID, m.Key, m.Version)
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": m})
}

type matrixRevokeRequest struct {
	Reason string `json:"reason"` // required
}

func (d *Deps) authorityMatricesRevokeHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	p := AdminPrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "admin authentication required"))
		return
	}
	var req matrixRevokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if req.Reason == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"reason is required (revoke is a permanent state change)"))
		return
	}
	if err := d.Authority.RevokeMatrix(r.Context(), id, p.AdminID, req.Reason); err != nil {
		if errors.Is(err, authority.ErrMatrixNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "matrix not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeConflict, "revoke matrix"))
		return
	}
	d.AuthorityAudit.MatrixRevoked(r.Context(), authorityActor(r), id, req.Reason)
	m, _ := d.Authority.GetMatrix(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"record": m})
}

func (d *Deps) authorityMatricesVersionHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	id, ok := parseAuthorityID(w, r)
	if !ok {
		return
	}
	p := AdminPrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "admin authentication required"))
		return
	}
	newM, err := d.Authority.CreateVersionFromApproved(r.Context(), id, p.AdminID)
	if err != nil {
		if errors.Is(err, authority.ErrMatrixNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "matrix not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeConflict, "create new version"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"record": newM})
}

// ── helpers ────────────────────────────────────────────────────────

// buildMatrixFromRequest applies request validation and constructs a
// populated *authority.Matrix ready for store insertion. Returns a
// typed error envelope on validation failures.
func buildMatrixFromRequest(req *matrixRequest) (*authority.Matrix, *rerr.Error) {
	if req.Key == "" {
		return nil, rerr.New(rerr.CodeValidation, "key is required")
	}
	if req.Name == "" {
		return nil, rerr.New(rerr.CodeValidation, "name is required")
	}
	if len(req.Levels) == 0 {
		return nil, rerr.New(rerr.CodeValidation, "at least one level is required")
	}

	m := &authority.Matrix{
		TenantID:          req.TenantID,
		Key:               req.Key,
		Version:           req.Version,
		Name:              req.Name,
		Description:       req.Description,
		MinAmount:         req.MinAmount,
		MaxAmount:         req.MaxAmount,
		Currency:          req.Currency,
		ConditionExpr:     req.ConditionExpr,
		OnFinalEscalation: req.OnFinalEscalation,
	}

	if req.EffectiveFrom != nil && *req.EffectiveFrom != "" {
		t, err := time.Parse(time.RFC3339, *req.EffectiveFrom)
		if err != nil {
			return nil, rerr.New(rerr.CodeValidation, "effective_from: invalid ISO 8601: %v", err)
		}
		m.EffectiveFrom = &t
	}
	if req.EffectiveTo != nil && *req.EffectiveTo != "" {
		t, err := time.Parse(time.RFC3339, *req.EffectiveTo)
		if err != nil {
			return nil, rerr.New(rerr.CodeValidation, "effective_to: invalid ISO 8601: %v", err)
		}
		m.EffectiveTo = &t
	}

	// Validate + convert levels + approvers.
	for i, lvlReq := range req.Levels {
		if lvlReq.LevelN < 1 {
			return nil, rerr.New(rerr.CodeValidation,
				"levels[%d].level_n must be >= 1", i)
		}
		if lvlReq.Name == "" {
			return nil, rerr.New(rerr.CodeValidation,
				"levels[%d].name is required", i)
		}
		switch lvlReq.Mode {
		case authority.ModeAny, authority.ModeAll, authority.ModeThreshold:
		default:
			return nil, rerr.New(rerr.CodeValidation,
				"levels[%d].mode must be one of any|all|threshold (got %q)", i, lvlReq.Mode)
		}
		if lvlReq.Mode == authority.ModeThreshold {
			if lvlReq.MinApprovals == nil || *lvlReq.MinApprovals < 1 {
				return nil, rerr.New(rerr.CodeValidation,
					"levels[%d].min_approvals required when mode='threshold' (must be >= 1)", i)
			}
		}
		if len(lvlReq.Approvers) == 0 {
			return nil, rerr.New(rerr.CodeValidation,
				"levels[%d] requires at least one approver", i)
		}

		lvl := authority.Level{
			LevelN:          lvlReq.LevelN,
			Name:            lvlReq.Name,
			Mode:            lvlReq.Mode,
			MinApprovals:    lvlReq.MinApprovals,
			EscalationHours: lvlReq.EscalationHours,
		}
		for j, apReq := range lvlReq.Approvers {
			// Slice 0 — reject unsupported types upfront with a clear
			// hint rather than letting the Store error wrap it.
			switch apReq.ApproverType {
			case authority.ApproverTypeRole, authority.ApproverTypeUser:
			case authority.ApproverTypePosition, authority.ApproverTypeDepartmentHead:
				return nil, rerr.New(rerr.CodeValidation,
					"levels[%d].approvers[%d].approver_type %q requires org-chart primitive (v2.x); use role or user",
					i, j, apReq.ApproverType)
			default:
				return nil, rerr.New(rerr.CodeValidation,
					"levels[%d].approvers[%d].approver_type must be role|user (got %q)",
					i, j, apReq.ApproverType)
			}
			if apReq.ApproverRef == "" {
				return nil, rerr.New(rerr.CodeValidation,
					"levels[%d].approvers[%d].approver_ref is required", i, j)
			}
			lvl.Approvers = append(lvl.Approvers, authority.Approver{
				ApproverType: apReq.ApproverType,
				ApproverRef:  apReq.ApproverRef,
				AutoResolve:  apReq.AutoResolve,
			})
		}
		m.Levels = append(m.Levels, lvl)
	}
	return m, nil
}

func parseAuthorityID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	s := chi.URLParam(r, "id")
	id, err := uuid.Parse(s)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid id (must be UUID)"))
		return uuid.Nil, false
	}
	return id, true
}

// authorityActor extracts the ActorContext for DoA audit hooks from
// an admin request — admins live in the `_admins` collection.
func authorityActor(r *http.Request) authority.ActorContext {
	p := AdminPrincipalFrom(r.Context())
	return authority.ActorContext{
		UserID:         p.AdminID,
		UserCollection: "_admins",
		IP:             clientIP(r),
		UserAgent:      r.UserAgent(),
	}
}

func orEmptyMatrices(rows []authority.Matrix) []authority.Matrix {
	if rows == nil {
		return []authority.Matrix{}
	}
	return rows
}

// ── §3.3 selection preview ────────────────────────────────────────

// matrixPreviewResponse describes what the gate would select under a
// supplied filter, along with the runners-up + tiebreaker rationale.
// Cosmetic operator-debuggability endpoint; not on any hot path.
type matrixPreviewResponse struct {
	Filter struct {
		Key      string `json:"key"`
		TenantID string `json:"tenant_id,omitempty"`
		Amount   *int64 `json:"amount,omitempty"`
		Currency string `json:"currency,omitempty"`
	} `json:"filter"`
	Selected   *authority.Matrix  `json:"selected,omitempty"`
	Candidates []authority.Matrix `json:"candidates"`
	Reason     string             `json:"reason"`
}

// authorityMatricesPreviewHandler handles GET
// /authority/matrices/preview?key=<k>&tenant_id=<u>&amount=<n>&currency=<c>
//
// Returns the matrix the selector would pick + every candidate that
// matches the same filter (status='approved' + window) so the operator
// can see WHY a particular matrix won.
func (d *Deps) authorityMatricesPreviewHandler(w http.ResponseWriter, r *http.Request) {
	if d.Authority == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "authority not configured"))
		return
	}
	q := r.URL.Query()
	key := q.Get("key")
	if key == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "key is required"))
		return
	}
	filter := authority.SelectFilter{Key: key, Currency: q.Get("currency")}
	if tStr := q.Get("tenant_id"); tStr != "" {
		t, err := uuid.Parse(tStr)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "tenant_id must be UUID"))
			return
		}
		filter.TenantID = &t
	}
	if aStr := q.Get("amount"); aStr != "" {
		a, err := strconv.ParseInt(aStr, 10, 64)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "amount must be integer"))
			return
		}
		filter.Amount = &a
	}

	// Selection (may return ErrMatrixNotFound — that's expected: caller
	// learns no matrix is applicable under this filter).
	selected, selErr := d.Authority.SelectActiveMatrix(r.Context(), filter)
	if selErr != nil && !errors.Is(selErr, authority.ErrMatrixNotFound) {
		rerr.WriteJSON(w, rerr.Wrap(selErr, rerr.CodeInternal, "preview select"))
		return
	}

	// Candidates = all approved matrices on this key (we filter post-hoc
	// to keep ListFilter simple; in practice the matrix count per key is
	// tiny, so this is fine).
	rows, err := d.Authority.ListMatrices(r.Context(), authority.ListFilter{
		Status:   authority.StatusApproved,
		Key:      key,
		TenantID: filter.TenantID,
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "preview list"))
		return
	}

	resp := matrixPreviewResponse{Selected: selected, Candidates: orEmptyMatrices(rows)}
	resp.Filter.Key = key
	if filter.TenantID != nil {
		resp.Filter.TenantID = filter.TenantID.String()
	}
	resp.Filter.Amount = filter.Amount
	resp.Filter.Currency = filter.Currency
	resp.Reason = previewReason(selected, rows)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// previewReason returns a human-readable explanation of the
// tiebreaker logic that picked `selected` over the others. Selection
// priority (see selector.go): tenant_id non-NULL > NULL, then
// min_amount DESC, then version DESC.
func previewReason(selected *authority.Matrix, all []authority.Matrix) string {
	if selected == nil {
		return "no matrix matches the supplied filter (status=approved + effective window + tenant/amount/currency constraints)"
	}
	if len(all) <= 1 {
		return "only one candidate matrix is active under this filter"
	}
	tenantPreferred := selected.TenantID != nil
	floorWin := false
	for _, m := range all {
		if m.ID == selected.ID {
			continue
		}
		if selected.MinAmount != nil && (m.MinAmount == nil ||
			*m.MinAmount < *selected.MinAmount) {
			floorWin = true
			break
		}
	}
	switch {
	case tenantPreferred && floorWin:
		return "selected: tenant-specific match + highest min_amount floor among candidates"
	case tenantPreferred:
		return "selected: tenant-specific match preferred over site-scope candidates"
	case floorWin:
		return "selected: highest min_amount floor among candidates"
	default:
		return "selected: latest version among same-scope candidates"
	}
}


