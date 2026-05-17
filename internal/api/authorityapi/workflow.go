// Package authorityapi exposes the public (principal-auth, not
// admin-only) DoA workflow surface — workflow creation by initiators
// and signing/rejecting by qualified approvers.
//
// *** PROTOTYPE — NOT FOR PRODUCTION USE (Slice 0). ***
//
// Routes (mounted at /api/authority, behind principal auth):
//
//	POST   /workflows                       create workflow (initiator)
//	GET    /workflows/{id}                  read workflow state
//	POST   /workflows/{id}/approve          approve at current level
//	POST   /workflows/{id}/reject           reject (terminal veto)
//	POST   /workflows/{id}/cancel           cancel (initiator only)
//	GET    /workflows/mine                  list workflows where current user is initiator or qualified approver
//
// Deferred:
//   - /workflows/{id}/reassign — reassign approver
//   - /workflows/{id}/comment  — comment without decision
//   - Audit chain integration (target='authority')
//   - Notifications fan-out
//   - Realtime channel publishes
//   - Delegation resolution (Slice 1+)
package authorityapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/authority"
	rerr "github.com/railbase/railbase/internal/errors"
)

// Deps bundles the request-scoped dependencies.
type Deps struct {
	Store *authority.Store
	// Audit is the optional DoA audit-chain hook (Slice 2). nil-safe;
	// every emission method short-circuits when Writer is nil.
	Audit *authority.AuditHook
}

// Mount registers the workflow surface under the given chi router.
// Caller is responsible for wrapping with principal-auth middleware.
func (d *Deps) Mount(r chi.Router) {
	if d.Store == nil {
		return
	}
	r.Post("/workflows", d.workflowCreateHandler)
	r.Get("/workflows/mine", d.workflowMineHandler)
	// Slice 1: approver inbox — workflows waiting for THIS user's signature.
	r.Get("/workflows/inbox", d.workflowInboxHandler)
	r.Get("/workflows/{id}", d.workflowGetHandler)
	r.Post("/workflows/{id}/approve", d.workflowApproveHandler)
	r.Post("/workflows/{id}/reject", d.workflowRejectHandler)
	r.Post("/workflows/{id}/cancel", d.workflowCancelHandler)
}

// ── request shapes ───────────────────────────────────────────────────

type workflowCreateRequest struct {
	Collection    string         `json:"collection"`
	RecordID      string         `json:"record_id"`
	ActionKey     string         `json:"action_key"`
	Diff          map[string]any `json:"diff"`
	Amount        *int64         `json:"amount,omitempty"`
	Currency      string         `json:"currency,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	TenantID      string         `json:"tenant_id,omitempty"`
}

type workflowDecisionRequest struct {
	Memo string `json:"memo,omitempty"`
}

type workflowRejectRequest struct {
	Reason string `json:"reason"` // technically optional but encouraged
}

type workflowCancelRequest struct {
	Reason string `json:"reason,omitempty"`
}

// ── handlers ─────────────────────────────────────────────────────────

func (d *Deps) workflowCreateHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "authentication required"))
		return
	}

	var req workflowCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if req.Collection == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "collection is required"))
		return
	}
	if req.ActionKey == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "action_key is required"))
		return
	}
	recordID, err := uuid.Parse(req.RecordID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid record_id (must be UUID)"))
		return
	}

	var tenantID *uuid.UUID
	if req.TenantID != "" {
		t, err := uuid.Parse(req.TenantID)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant_id (must be UUID)"))
			return
		}
		tenantID = &t
	}

	// Matrix selection — required to create a workflow.
	matrix, err := d.Store.SelectActiveMatrix(r.Context(), authority.SelectFilter{
		Key:      req.ActionKey,
		TenantID: tenantID,
		Amount:   req.Amount,
		Currency: req.Currency,
	})
	if err != nil {
		if errors.Is(err, authority.ErrMatrixNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
				"no applicable approved matrix found for action_key %q "+
					"(tenant + amount + currency match required)",
				req.ActionKey))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "select matrix"))
		return
	}

	wf, err := d.Store.CreateWorkflow(r.Context(), authority.WorkflowCreateInput{
		TenantID:      tenantID,
		Matrix:        matrix,
		Collection:    req.Collection,
		RecordID:      recordID,
		ActionKey:     req.ActionKey,
		RequestedDiff: req.Diff,
		Amount:        req.Amount,
		Currency:      req.Currency,
		InitiatorID:   p.UserID,
		Notes:         req.Notes,
	})
	if err != nil {
		if errors.Is(err, authority.ErrWorkflowActiveConflict) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
				"a workflow is already running on this record + action"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create workflow"))
		return
	}
	d.Audit.WorkflowCreated(r.Context(), workflowActor(r), wf)
	writeJSON(w, http.StatusCreated, map[string]any{"record": wf})
}

func (d *Deps) workflowGetHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "authentication required"))
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	wf, err := d.Store.GetWorkflow(r.Context(), id)
	if err != nil {
		if errors.Is(err, authority.ErrWorkflowNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "workflow not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "get workflow"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": wf})
}

func (d *Deps) workflowApproveHandler(w http.ResponseWriter, r *http.Request) {
	d.workflowDecisionHandler(w, r, authority.DecisionApproved)
}

func (d *Deps) workflowRejectHandler(w http.ResponseWriter, r *http.Request) {
	d.workflowDecisionHandler(w, r, authority.DecisionRejected)
}

func (d *Deps) workflowDecisionHandler(w http.ResponseWriter, r *http.Request, decision string) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "authentication required"))
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var memo string
	if decision == authority.DecisionApproved {
		var req workflowDecisionRequest
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
				return
			}
		}
		memo = req.Memo
	} else {
		// reject — encourage reason but Slice 0 doesn't hard-require.
		var req workflowRejectRequest
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
				return
			}
		}
		memo = req.Reason
	}

	// Load workflow to qualify the signer against the matrix.
	wf, err := d.Store.GetWorkflow(r.Context(), id)
	if err != nil {
		if errors.Is(err, authority.ErrWorkflowNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "workflow not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load workflow"))
		return
	}
	if wf.Status != authority.WorkflowStatusRunning || wf.CurrentLevel == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
			"workflow is %s; cannot record new decision", wf.Status))
		return
	}

	// Qualify signer: load matrix, current level, resolve approvers.
	matrix, err := d.Store.GetMatrix(r.Context(), wf.MatrixID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load matrix"))
		return
	}
	var currentLevel *authority.Level
	for i := range matrix.Levels {
		if matrix.Levels[i].LevelN == *wf.CurrentLevel {
			currentLevel = &matrix.Levels[i]
			break
		}
	}
	if currentLevel == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal,
			"matrix has no level %d (inconsistent state)", *wf.CurrentLevel))
		return
	}
	qualified, err := d.Store.ResolveApprovers(r.Context(), currentLevel, wf.TenantID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve approvers"))
		return
	}
	qualified = filterOutInitiator(qualified, wf.InitiatorID)
	if !containsUUID(qualified, p.UserID) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"current principal does not qualify as approver for level %d", *wf.CurrentLevel))
		return
	}

	updated, err := d.Store.RecordDecision(r.Context(), authority.DecisionInput{
		WorkflowID:         id,
		LevelN:             *wf.CurrentLevel,
		ApproverID:         p.UserID,
		ApproverRole:       "", // Slice 0 doesn't capture role at sign time; future enhancement
		ApproverResolution: resolutionString(p.UserID, currentLevel, qualified),
		Decision:           decision,
		Memo:               memo,
	})
	if err != nil {
		if errors.Is(err, authority.ErrWorkflowNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "workflow not found"))
			return
		}
		if errors.Is(err, authority.ErrWorkflowTerminal) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "workflow already terminal"))
			return
		}
		if errors.Is(err, authority.ErrDuplicateDecision) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "you already decided this level"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "record decision"))
		return
	}
	d.Audit.WorkflowDecision(r.Context(), workflowActor(r), updated, decision, wf.CurrentLevelOrZero(), memo)
	writeJSON(w, http.StatusOK, map[string]any{"record": updated})
}

func (d *Deps) workflowCancelHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "authentication required"))
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req workflowCancelRequest
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req) // best-effort; empty body OK
	}

	// Only initiator can cancel.
	wf, err := d.Store.GetWorkflow(r.Context(), id)
	if err != nil {
		if errors.Is(err, authority.ErrWorkflowNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "workflow not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load workflow"))
		return
	}
	if wf.InitiatorID != p.UserID {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"only the initiator can cancel a workflow"))
		return
	}

	updated, err := d.Store.CancelWorkflow(r.Context(), id, p.UserID, req.Reason)
	if err != nil {
		if errors.Is(err, authority.ErrWorkflowTerminal) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeConflict, "workflow already terminal"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "cancel workflow"))
		return
	}
	d.Audit.WorkflowCancelled(r.Context(), workflowActor(r), id, req.Reason)
	writeJSON(w, http.StatusOK, map[string]any{"record": updated})
}

// workflowInboxHandler — Slice 1: workflows where the current user
// can sign at the workflow's current level AND hasn't yet decided.
// Joins through matrix → matrix_approvers → user_roles + delegations.
//
// SoD enforcement: workflows where the user is the INITIATOR are
// included here (since they may also be qualified to sign elsewhere),
// but the decision endpoint blocks self-approval — so the UI is
// expected to dim self-initiated rows.
func (d *Deps) workflowInboxHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "authentication required"))
		return
	}
	rows, err := d.Store.ListWorkflowsForApprover(r.Context(), p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list inbox"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// workflowMineHandler — list workflows where the current principal
// is the INITIATOR. Pair to workflowInboxHandler for the "what's
// outstanding I need to track?" view. Both endpoints needed: UIs
// typically show "my requests" and "awaiting my signature" as
// distinct tabs.
func (d *Deps) workflowMineHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "authentication required"))
		return
	}
	rows, err := d.Store.ListWorkflowsForInitiator(r.Context(), p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list workflows"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// ── helpers ──────────────────────────────────────────────────────────

// workflowActor extracts the ActorContext for DoA audit hooks from
// a request authenticated via the public principal middleware.
func workflowActor(r *http.Request) authority.ActorContext {
	p := authmw.PrincipalFrom(r.Context())
	return authority.ActorContext{
		UserID:         p.UserID,
		UserCollection: p.CollectionName,
		IP:             clientIP(r),
		UserAgent:      r.UserAgent(),
	}
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		for i := 0; i < len(fwd); i++ {
			if fwd[i] == ',' {
				return fwd[:i]
			}
		}
		return fwd
	}
	return r.RemoteAddr
}

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	s := chi.URLParam(r, "id")
	id, err := uuid.Parse(s)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid id (must be UUID)"))
		return uuid.Nil, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func containsUUID(s []uuid.UUID, target uuid.UUID) bool {
	for _, u := range s {
		if u == target {
			return true
		}
	}
	return false
}

func filterOutInitiator(s []uuid.UUID, initiator uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(s))
	for _, u := range s {
		if u != initiator {
			out = append(out, u)
		}
	}
	return out
}

// resolutionString builds the snapshot string for the approver_resolution
// column — for Slice 0 we just record "user:<id>" if the principal
// matched a direct user approver, else "role:<unknown>" (Slice 0
// doesn't capture which specific role row matched — Slice 1 polish).
func resolutionString(userID uuid.UUID, level *authority.Level, qualified []uuid.UUID) string {
	for _, ap := range level.Approvers {
		if ap.ApproverType == authority.ApproverTypeUser && ap.ApproverRef == userID.String() {
			return "user:" + userID.String()
		}
	}
	return "role-pool"
}
