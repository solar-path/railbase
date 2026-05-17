// Package authority is the v2.0 Slice 0 prototype implementation of
// Delegation of Authority — Railbase's runtime approval primitive.
//
// *** PROTOTYPE — NOT FOR PRODUCTION USE ***
//
// Slice 0 scope (see plan.md §6.3 + docs/26-authority.md):
//   - Hybrid schema-as-code (gate point declarations via
//     internal/schema/builder.Authority) + matrix-as-data
//     (runtime rules in _doa_matrices, edited via admin UI).
//   - Matrix CRUD via admin REST.
//   - DoA gate middleware (matrix selection, 409 envelope).
//   - Workflow create/approve/reject endpoints.
//   - ProtectedFields consume validation.
//   - Approver types role + user (position/department_head — v2.x with
//     org-chart primitive).
//
// Out of Slice 0 (deferred until Slice 0 validates design):
//   - Delegation primitive (_doa_delegations)
//   - Audit chain (_authority_audit with Ed25519 seals)
//   - Multi-level workflow runtime (Slice 0 = single level only)
//   - Escalation reaper, delegation expirer
//   - Tasks integration
//   - Admin UI screens
//   - i18n, locale-aware decision rendering
//
// The package is intentionally compact — Slice 0 throwaway-allowed.
// After Slice 0 review, this package either extends in-place to
// Slice 1 or gets replaced wholesale.
package authority

import (
	"time"

	"github.com/google/uuid"
)

// Matrix lifecycle states.
const (
	StatusDraft    = "draft"
	StatusApproved = "approved"
	StatusArchived = "archived"
	StatusRevoked  = "revoked"
)

// Per-level approval modes.
const (
	ModeAny       = "any"       // one qualifying approver enough
	ModeAll       = "all"       // every approver in set must sign
	ModeThreshold = "threshold" // ≥ MinApprovals approveds needed
)

// Approver types. v2.0 prototype supports `role` + `user` only;
// `position` + `department_head` reserve their value in the constraint
// but are rejected by admin REST until v2.x lands org-chart primitive
// (см. docs/26-org-structure-audit.md).
const (
	ApproverTypeRole             = "role"
	ApproverTypeUser             = "user"
	ApproverTypePosition         = "position"          // v2.x
	ApproverTypeDepartmentHead   = "department_head"   // v2.x
)

// Workflow lifecycle states.
const (
	WorkflowStatusRunning   = "running"
	WorkflowStatusCompleted = "completed"
	WorkflowStatusRejected  = "rejected"
	WorkflowStatusCancelled = "cancelled"
	WorkflowStatusExpired   = "expired"
)

// Decision values.
const (
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

// On-final-escalation policy on a matrix.
const (
	FinalEscalationExpire = "expire"
	FinalEscalationReject = "reject"
)

// Matrix is one approval matrix declaration row. Versioned —
// approving a matrix freezes its rules; further edits create a new
// version row in `draft` status.
type Matrix struct {
	ID                  uuid.UUID  `json:"id"`
	TenantID            *uuid.UUID `json:"tenant_id,omitempty"`
	Key                 string     `json:"key"`
	Version             int        `json:"version"`
	Name                string     `json:"name"`
	Description         string     `json:"description,omitempty"`

	Status              string     `json:"status"`
	RevokedReason       string     `json:"revoked_reason,omitempty"`
	ApprovedBy          *uuid.UUID `json:"approved_by,omitempty"`
	ApprovedAt          *time.Time `json:"approved_at,omitempty"`

	EffectiveFrom       *time.Time `json:"effective_from,omitempty"`
	EffectiveTo         *time.Time `json:"effective_to,omitempty"`

	MinAmount           *int64     `json:"min_amount,omitempty"`
	MaxAmount           *int64     `json:"max_amount,omitempty"`
	Currency            string     `json:"currency,omitempty"`
	ConditionExpr       string     `json:"condition_expr,omitempty"`

	OnFinalEscalation   string     `json:"on_final_escalation"`

	CreatedAt           time.Time  `json:"created_at"`
	CreatedBy           *uuid.UUID `json:"created_by,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`

	// Levels is populated on full reads. List queries leave it empty.
	Levels              []Level    `json:"levels,omitempty"`
}

// Level is one approval level on a matrix.
type Level struct {
	ID               uuid.UUID  `json:"id"`
	MatrixID         uuid.UUID  `json:"matrix_id"`
	LevelN           int        `json:"level_n"`
	Name             string     `json:"name"`

	Mode             string     `json:"mode"`
	MinApprovals     *int       `json:"min_approvals,omitempty"`

	EscalationHours  *int       `json:"escalation_hours,omitempty"`

	// Approvers populated on full reads.
	Approvers        []Approver `json:"approvers,omitempty"`
}

// Approver is one qualified-approver declaration on a level.
type Approver struct {
	ID           uuid.UUID `json:"id"`
	LevelID      uuid.UUID `json:"level_id"`
	ApproverType string    `json:"approver_type"`
	ApproverRef  string    `json:"approver_ref"`
	AutoResolve  bool      `json:"auto_resolve,omitempty"`
}

// Workflow is a runtime DoA workflow instance — one row per gated
// mutation request. Snapshotted matrix_version pinned at creation.
type Workflow struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       *uuid.UUID `json:"tenant_id,omitempty"`

	MatrixID       uuid.UUID  `json:"matrix_id"`
	MatrixVersion  int        `json:"matrix_version"`

	Collection     string     `json:"collection"`
	RecordID       uuid.UUID  `json:"record_id"`
	ActionKey      string     `json:"action_key"`
	RequestedDiff  []byte     `json:"requested_diff"` // raw JSON
	Amount         *int64     `json:"amount,omitempty"`
	Currency       string     `json:"currency,omitempty"`
	InitiatorID    uuid.UUID  `json:"initiator_id"`
	Notes          string     `json:"notes,omitempty"`

	Status         string     `json:"status"`
	CurrentLevel   *int       `json:"current_level,omitempty"`

	TerminalReason string     `json:"terminal_reason,omitempty"`
	TerminalBy     *uuid.UUID `json:"terminal_by,omitempty"`
	TerminalAt     *time.Time `json:"terminal_at,omitempty"`

	ConsumedAt     *time.Time `json:"consumed_at,omitempty"`

	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`

	Decisions      []Decision `json:"decisions,omitempty"`
}

// OnLevel returns the workflow's current level along with ok=true
// when the workflow is still running. Terminal workflows (completed/
// rejected/cancelled/expired) return (0, false) so callers don't have
// to manually nil-check CurrentLevel.
//
// This is the canonical accessor for level-aware code — UI rendering,
// reminder dispatch, escalation reaper. Direct CurrentLevel reads are
// fine for storage layers but should not leak into business code.
//
// Findings §3.2 — Slice 0 hardening (2026-05-16).
func (w *Workflow) OnLevel() (int, bool) {
	if w == nil || w.CurrentLevel == nil {
		return 0, false
	}
	return *w.CurrentLevel, true
}

// CurrentLevelOrZero returns the current level number or 0 when the
// workflow is terminal. Sugar for callers that want to record a
// level-bearing event without worrying about the nil-pointer dance —
// audit hooks, log lines, telemetry.
func (w *Workflow) CurrentLevelOrZero() int {
	if w == nil || w.CurrentLevel == nil {
		return 0
	}
	return *w.CurrentLevel
}

// IsTerminal reports whether the workflow has reached a terminal
// state (no further decisions accepted). Mirror of OnLevel() for
// the negative form — sometimes the "is it done?" question reads
// more naturally than "is it still running?".
func (w *Workflow) IsTerminal() bool {
	if w == nil {
		return true
	}
	switch w.Status {
	case WorkflowStatusCompleted, WorkflowStatusRejected,
		WorkflowStatusCancelled, WorkflowStatusExpired:
		return true
	}
	return false
}

// Decision is one approver's recorded decision on one level of a
// workflow.
type Decision struct {
	ID                 uuid.UUID  `json:"id"`
	WorkflowID         uuid.UUID  `json:"workflow_id"`
	LevelN             int        `json:"level_n"`
	ApproverID         uuid.UUID  `json:"approver_id"`
	ApproverRole       string     `json:"approver_role,omitempty"`
	ApproverResolution string     `json:"approver_resolution,omitempty"`

	// Forward-compat v2.x — populated only when position / department_head
	// approver types ship (see docs/26-org-structure-audit.md).
	ApproverPosition string  `json:"approver_position,omitempty"`
	ApproverOrgPath  string  `json:"approver_org_path,omitempty"`
	ApproverActing   *bool   `json:"approver_acting,omitempty"`

	Decision    string    `json:"decision"`
	Memo        string    `json:"memo,omitempty"`
	DecidedAt   time.Time `json:"decided_at"`
}
