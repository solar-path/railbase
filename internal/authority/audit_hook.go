package authority

// Audit-chain emission hooks for the v2.0-alpha DoA subsystem.
//
// Architecture: Slice 2 hardening of Slice 0/1 prototype. The Store
// methods don't emit audit themselves — they don't have the request-
// scoped Actor context (UserID, UserCollection, IP, UserAgent). Instead
// callers (admin REST + workflow REST + REST CRUD consume path) call
// into this helper after a successful Store mutation to write the
// chain row.
//
// Bare-pool rule (см. internal/audit/audit.go header): audit writes go
// through audit.Writer's own pool acquisition. We DO NOT thread a tx
// through here. If a DoA business write rolled back, the caller never
// reaches the audit emission — exactly the behaviour we want.
//
// Event names follow `authority.<entity>.<verb>`:
//   - authority.matrix.create
//   - authority.matrix.approve
//   - authority.matrix.revoke
//   - authority.matrix.version
//   - authority.matrix.delete
//   - authority.workflow.create
//   - authority.workflow.decision.approved
//   - authority.workflow.decision.rejected
//   - authority.workflow.cancel
//   - authority.workflow.expire
//   - authority.workflow.consume
//   - authority.delegation.create
//   - authority.delegation.revoke

import (
	"context"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
)

// AuditHook is the entry point callers use to fan a DoA lifecycle
// event into the audit chain. nil-safe: every method short-circuits
// when w is nil so tests / non-audited deployments don't pay overhead.
type AuditHook struct {
	Writer *audit.Writer
}

// NewAuditHook wraps a Writer. Pass nil to short-circuit emission.
func NewAuditHook(w *audit.Writer) *AuditHook {
	return &AuditHook{Writer: w}
}

// ActorContext is the minimal request-scoped identity needed to emit
// an audit row. Callers populate from the HTTP layer (principal middleware,
// admin auth, REST handler request).
type ActorContext struct {
	UserID         uuid.UUID
	UserCollection string
	TenantID       uuid.UUID
	IP             string
	UserAgent      string
}

// MatrixCreated emits authority.matrix.create.
func (h *AuditHook) MatrixCreated(ctx context.Context, actor ActorContext, m *Matrix) {
	if h == nil || h.Writer == nil || m == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.matrix.create",
		Outcome:        audit.OutcomeSuccess,
		After:          summariseMatrix(m),
		IP:             actor.IP,
		UserAgent:      actor.UserAgent,
	})
}

// MatrixApproved emits authority.matrix.approve.
func (h *AuditHook) MatrixApproved(ctx context.Context, actor ActorContext,
	matrixID uuid.UUID, key string, version int) {
	if h == nil || h.Writer == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.matrix.approve",
		Outcome:        audit.OutcomeSuccess,
		After: map[string]any{
			"matrix_id": matrixID.String(),
			"key":       key,
			"version":   version,
		},
		IP:        actor.IP,
		UserAgent: actor.UserAgent,
	})
}

// MatrixRevoked emits authority.matrix.revoke.
func (h *AuditHook) MatrixRevoked(ctx context.Context, actor ActorContext,
	matrixID uuid.UUID, reason string) {
	if h == nil || h.Writer == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.matrix.revoke",
		Outcome:        audit.OutcomeSuccess,
		After: map[string]any{
			"matrix_id": matrixID.String(),
			"reason":    reason,
		},
		IP:        actor.IP,
		UserAgent: actor.UserAgent,
	})
}

// WorkflowCreated emits authority.workflow.create.
func (h *AuditHook) WorkflowCreated(ctx context.Context, actor ActorContext, wf *Workflow) {
	if h == nil || h.Writer == nil || wf == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.workflow.create",
		Outcome:        audit.OutcomeSuccess,
		After:          summariseWorkflow(wf),
		IP:             actor.IP,
		UserAgent:      actor.UserAgent,
	})
}

// WorkflowDecision emits authority.workflow.decision.{approved,rejected}.
// The discriminator is on the event name (so chain queries can pivot
// on it without parsing JSON), but the full decision payload sits in
// `After` for full traceability.
func (h *AuditHook) WorkflowDecision(ctx context.Context, actor ActorContext,
	wf *Workflow, decision string, levelN int, memo string) {
	if h == nil || h.Writer == nil || wf == nil {
		return
	}
	eventName := "authority.workflow.decision.approved"
	if decision == DecisionRejected {
		eventName = "authority.workflow.decision.rejected"
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          eventName,
		Outcome:        audit.OutcomeSuccess,
		After: map[string]any{
			"workflow_id":     wf.ID.String(),
			"workflow_status": wf.Status,
			"level_n":         levelN,
			"decision":        decision,
			"memo":            memo,
		},
		IP:        actor.IP,
		UserAgent: actor.UserAgent,
	})
}

// WorkflowCancelled emits authority.workflow.cancel.
func (h *AuditHook) WorkflowCancelled(ctx context.Context, actor ActorContext,
	workflowID uuid.UUID, reason string) {
	if h == nil || h.Writer == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.workflow.cancel",
		Outcome:        audit.OutcomeSuccess,
		After: map[string]any{
			"workflow_id": workflowID.String(),
			"reason":      reason,
		},
		IP:        actor.IP,
		UserAgent: actor.UserAgent,
	})
}

// WorkflowConsumed emits authority.workflow.consume. Fired by the
// gate-wired CRUD handler when MarkConsumed succeeds in-tx.
func (h *AuditHook) WorkflowConsumed(ctx context.Context, actor ActorContext,
	workflowID uuid.UUID, collection, recordID, actionKey string) {
	if h == nil || h.Writer == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.workflow.consume",
		Outcome:        audit.OutcomeSuccess,
		After: map[string]any{
			"workflow_id": workflowID.String(),
			"collection":  collection,
			"record_id":   recordID,
			"action_key":  actionKey,
		},
		IP:        actor.IP,
		UserAgent: actor.UserAgent,
	})
}

// DelegationCreated emits authority.delegation.create.
func (h *AuditHook) DelegationCreated(ctx context.Context, actor ActorContext, d *Delegation) {
	if h == nil || h.Writer == nil || d == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.delegation.create",
		Outcome:        audit.OutcomeSuccess,
		After: map[string]any{
			"delegation_id":      d.ID.String(),
			"delegator_id":       d.DelegatorID.String(),
			"delegatee_id":       d.DelegateeID.String(),
			"source_action_keys": d.SourceActionKeys,
			"max_amount":         d.MaxAmount,
			"effective_to":       d.EffectiveTo,
		},
		IP:        actor.IP,
		UserAgent: actor.UserAgent,
	})
}

// DelegationRevoked emits authority.delegation.revoke.
func (h *AuditHook) DelegationRevoked(ctx context.Context, actor ActorContext,
	delegationID uuid.UUID, reason string) {
	if h == nil || h.Writer == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         actor.UserID,
		UserCollection: actor.UserCollection,
		TenantID:       actor.TenantID,
		Event:          "authority.delegation.revoke",
		Outcome:        audit.OutcomeSuccess,
		After: map[string]any{
			"delegation_id": delegationID.String(),
			"reason":        reason,
		},
		IP:        actor.IP,
		UserAgent: actor.UserAgent,
	})
}

func summariseMatrix(m *Matrix) map[string]any {
	if m == nil {
		return nil
	}
	out := map[string]any{
		"matrix_id":    m.ID.String(),
		"key":          m.Key,
		"version":      m.Version,
		"name":         m.Name,
		"status":       m.Status,
		"level_count":  len(m.Levels),
	}
	if m.MinAmount != nil {
		out["min_amount"] = *m.MinAmount
	}
	if m.MaxAmount != nil {
		out["max_amount"] = *m.MaxAmount
	}
	if m.Currency != "" {
		out["currency"] = m.Currency
	}
	if m.TenantID != nil {
		out["tenant_id"] = m.TenantID.String()
	}
	return out
}

func summariseWorkflow(wf *Workflow) map[string]any {
	if wf == nil {
		return nil
	}
	out := map[string]any{
		"workflow_id":    wf.ID.String(),
		"matrix_id":      wf.MatrixID.String(),
		"matrix_version": wf.MatrixVersion,
		"collection":     wf.Collection,
		"record_id":      wf.RecordID.String(),
		"action_key":     wf.ActionKey,
		"initiator_id":   wf.InitiatorID.String(),
		"status":         wf.Status,
	}
	if wf.Amount != nil {
		out["amount"] = *wf.Amount
	}
	if wf.Currency != "" {
		out["currency"] = wf.Currency
	}
	if wf.TenantID != nil {
		out["tenant_id"] = wf.TenantID.String()
	}
	return out
}
