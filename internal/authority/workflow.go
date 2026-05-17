package authority

// Workflow runtime — creates and progresses _doa_workflows instances
// against a matrix snapshot. Slice 0 implementation:
//
//   - CreateWorkflow: persists a new workflow row + sets current_level=1.
//   - RecordDecision: writes one approver's decision; evaluates level
//     completion (any/all/threshold modes); promotes current_level on
//     satisfaction OR transitions to rejected/completed/expired.
//   - GetWorkflow: full read with decisions populated.
//
// Slice 0 limits:
//   - No escalation reaper (single tick — no per-level escalation_hours).
//   - No reassign action.
//   - Approver qualification check (whether the signer can sign at all)
//     handled by the caller via ResolveApprovers; this Store trusts
//     callers to validate upstream.
//   - No audit chain extension — that's Slice 1+.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrWorkflowNotFound — get/transition returned no rows.
var ErrWorkflowNotFound = errors.New("authority: workflow not found")

// ErrWorkflowTerminal — attempted operation on a terminal workflow.
var ErrWorkflowTerminal = errors.New("authority: workflow is in terminal state")

// ErrDuplicateDecision — approver attempted to sign twice on same level.
var ErrDuplicateDecision = errors.New("authority: approver already decided this level")

// ErrApproverNotQualified — actor is not in the qualified approver pool
// for the workflow's current level. Slice 0-tightening hardening: was
// previously upstream-trust only; now enforced at RecordDecision time
// via a JOIN against matrix_approvers + RBAC.
var ErrApproverNotQualified = errors.New(
	"authority: actor is not in the qualified approver pool for this level")

// ErrWorkflowActiveConflict — uniq__doa_workflows_active blocks creation
// because there's already a running workflow on this (collection,
// record, action).
var ErrWorkflowActiveConflict = errors.New(
	"authority: a workflow is already running for this record + action")

// WorkflowCreateInput is the create-time payload.
type WorkflowCreateInput struct {
	TenantID      *uuid.UUID
	Matrix        *Matrix           // fully populated (Levels + Approvers); pinned by version snapshot
	Collection    string
	RecordID      uuid.UUID
	ActionKey     string
	RequestedDiff map[string]any    // serialized to JSONB
	Amount        *int64
	Currency      string
	InitiatorID   uuid.UUID
	Notes         string
	TTL           time.Duration     // overrides default if non-zero
}

// DefaultWorkflowTTL is the fallback expiration window when matrix
// doesn't pin one. Slice 0 uses 7 days; Slice 1 reads matrix-level
// TTL if/when added.
const DefaultWorkflowTTL = 7 * 24 * time.Hour

// CreateWorkflow inserts a new running workflow at level 1. The
// matrix snapshot pin happens via matrix_version column — matrices
// can later be revoked/versioned without affecting this workflow.
func (s *Store) CreateWorkflow(ctx context.Context, in WorkflowCreateInput) (*Workflow, error) {
	if in.Matrix == nil {
		return nil, fmt.Errorf("authority: CreateWorkflow: matrix is required")
	}
	if in.Collection == "" || in.ActionKey == "" {
		return nil, fmt.Errorf("authority: CreateWorkflow: collection + action_key required")
	}
	if in.RecordID == uuid.Nil {
		return nil, fmt.Errorf("authority: CreateWorkflow: record_id is required")
	}
	if in.InitiatorID == uuid.Nil {
		return nil, fmt.Errorf("authority: CreateWorkflow: initiator_id is required")
	}
	if len(in.Matrix.Levels) == 0 {
		return nil, fmt.Errorf("authority: CreateWorkflow: matrix has no levels")
	}

	diffJSON, err := json.Marshal(in.RequestedDiff)
	if err != nil {
		return nil, fmt.Errorf("authority: marshal requested_diff: %w", err)
	}

	ttl := in.TTL
	if ttl == 0 {
		ttl = DefaultWorkflowTTL
	}
	expiresAt := time.Now().Add(ttl)

	const stmt = `
		INSERT INTO _doa_workflows (
			tenant_id, matrix_id, matrix_version,
			collection, record_id, action_key, requested_diff,
			amount, currency, initiator_id, notes,
			status, current_level, expires_at
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7,
			$8, $9, $10, $11,
			'running', 1, $12
		)
		RETURNING id, created_at`

	wf := &Workflow{
		TenantID:      in.TenantID,
		MatrixID:      in.Matrix.ID,
		MatrixVersion: in.Matrix.Version,
		Collection:    in.Collection,
		RecordID:      in.RecordID,
		ActionKey:     in.ActionKey,
		RequestedDiff: diffJSON,
		Amount:        in.Amount,
		Currency:      in.Currency,
		InitiatorID:   in.InitiatorID,
		Notes:         in.Notes,
		Status:        WorkflowStatusRunning,
		ExpiresAt:     expiresAt,
	}
	current := 1
	wf.CurrentLevel = &current

	err = s.pool.QueryRow(ctx, stmt,
		wf.TenantID, wf.MatrixID, wf.MatrixVersion,
		wf.Collection, wf.RecordID, wf.ActionKey, diffJSON,
		wf.Amount, nullString(wf.Currency), wf.InitiatorID, nullString(wf.Notes),
		wf.ExpiresAt,
	).Scan(&wf.ID, &wf.CreatedAt)
	if err != nil {
		// Translate the partial unique index violation into a typed error.
		if isUniqueViolation(err) {
			return nil, ErrWorkflowActiveConflict
		}
		return nil, fmt.Errorf("authority: insert workflow: %w", err)
	}
	return wf, nil
}

// GetWorkflow loads a workflow by id with all decisions populated.
func (s *Store) GetWorkflow(ctx context.Context, id uuid.UUID) (*Workflow, error) {
	const selectWF = `
		SELECT id, tenant_id, matrix_id, matrix_version,
		       collection, record_id, action_key, requested_diff,
		       amount, COALESCE(currency, ''),
		       initiator_id, COALESCE(notes, ''),
		       status, current_level,
		       COALESCE(terminal_reason, ''), terminal_by, terminal_at,
		       consumed_at,
		       created_at, expires_at
		FROM _doa_workflows
		WHERE id = $1`

	wf := &Workflow{}
	var diffRaw []byte
	err := s.pool.QueryRow(ctx, selectWF, id).Scan(
		&wf.ID, &wf.TenantID, &wf.MatrixID, &wf.MatrixVersion,
		&wf.Collection, &wf.RecordID, &wf.ActionKey, &diffRaw,
		&wf.Amount, &wf.Currency,
		&wf.InitiatorID, &wf.Notes,
		&wf.Status, &wf.CurrentLevel,
		&wf.TerminalReason, &wf.TerminalBy, &wf.TerminalAt,
		&wf.ConsumedAt,
		&wf.CreatedAt, &wf.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authority: select workflow: %w", err)
	}
	wf.RequestedDiff = diffRaw

	// Load decisions.
	const selectDecisions = `
		SELECT id, level_n, approver_id,
		       COALESCE(approver_role, ''),
		       COALESCE(approver_resolution, ''),
		       COALESCE(approver_position, ''),
		       COALESCE(approver_org_path, ''),
		       approver_acting,
		       decision, COALESCE(memo, ''), decided_at
		FROM _doa_workflow_decisions
		WHERE workflow_id = $1
		ORDER BY level_n, decided_at`

	rows, err := s.pool.Query(ctx, selectDecisions, id)
	if err != nil {
		return nil, fmt.Errorf("authority: list decisions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d Decision
		d.WorkflowID = id
		if err := rows.Scan(
			&d.ID, &d.LevelN, &d.ApproverID,
			&d.ApproverRole, &d.ApproverResolution,
			&d.ApproverPosition, &d.ApproverOrgPath, &d.ApproverActing,
			&d.Decision, &d.Memo, &d.DecidedAt,
		); err != nil {
			return nil, fmt.Errorf("authority: scan decision: %w", err)
		}
		wf.Decisions = append(wf.Decisions, d)
	}
	return wf, nil
}

// DecisionInput is the data of one approver's vote on a workflow level.
type DecisionInput struct {
	WorkflowID         uuid.UUID
	LevelN             int     // must match workflow.current_level
	ApproverID         uuid.UUID
	ApproverRole       string  // snapshot
	ApproverResolution string  // "role:editor" / "user:abc"
	Decision           string  // DecisionApproved | DecisionRejected
	Memo               string
}

// RecordDecision persists one approver's decision on a workflow's
// current level and transitions the workflow state if the level
// satisfies its mode. Returns the (possibly transitioned) workflow.
//
// Transition rules (Slice 0 single-tick implementation):
//   - decision='rejected' → workflow status = 'rejected' (immediate veto)
//   - decision='approved' AND level satisfies mode → advance to next level
//     OR if this was the final level, status = 'completed' but
//     NOT consumed (consume validation happens at handler-write time)
//
// Slice 0 NOTE: completed != consumed. Workflow becomes 'completed'
// when all levels passed; downstream handler then performs the
// actual mutation + consume validation + sets consumed_at. Slice 0
// stops at the 'completed' transition; consume integration is a
// later phase.
func (s *Store) RecordDecision(ctx context.Context, in DecisionInput) (*Workflow, error) {
	if in.Decision != DecisionApproved && in.Decision != DecisionRejected {
		return nil, fmt.Errorf("authority: invalid decision %q (want approved|rejected)",
			in.Decision)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("authority: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the workflow row for the duration of the transaction.
	// Also load action_key + amount so the qualification check can
	// expand delegations correctly.
	const lockStmt = `
		SELECT status, current_level, matrix_id, matrix_version, tenant_id,
		       action_key, amount
		FROM _doa_workflows
		WHERE id = $1
		FOR UPDATE`

	var status string
	var currentLevel *int
	var matrixID uuid.UUID
	var matrixVersion int
	var tenantID *uuid.UUID
	var actionKey string
	var amount *int64
	err = tx.QueryRow(ctx, lockStmt, in.WorkflowID).Scan(
		&status, &currentLevel, &matrixID, &matrixVersion, &tenantID,
		&actionKey, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authority: lock workflow: %w", err)
	}
	if status != WorkflowStatusRunning {
		return nil, ErrWorkflowTerminal
	}
	if currentLevel == nil {
		// Inconsistent — running workflow must have a level.
		return nil, fmt.Errorf("authority: running workflow has no current_level (data corruption?)")
	}
	if in.LevelN != *currentLevel {
		return nil, fmt.Errorf(
			"authority: decision targets level %d but workflow is on level %d",
			in.LevelN, *currentLevel)
	}

	// Approver qualification check — was previously upstream-trust only
	// (Slice 0 limitation surfaced in Slice 0 findings §3.1). Now enforced
	// here at RecordDecision time so any caller path is safe.
	//
	// Resolve the level's qualified pool (role expansion + direct users)
	// and check the actor's ID is in the set. Uses the tx-bound level
	// load so we see the same data the rest of this RecordDecision call
	// uses for satisfaction evaluation.
	qLevel, err := s.getLevelByMatrixAndN(ctx, tx, matrixID, in.LevelN)
	if err != nil {
		return nil, fmt.Errorf("authority: qualification: load level: %w", err)
	}
	// Slice 1: delegation-aware pool expansion. ResolveApproversWithDelegation
	// adds delegatees who hold authority from anyone in the base pool.
	qualified, err := s.ResolveApproversWithDelegation(ctx, qLevel, tenantID, actionKey, amount)
	if err != nil {
		return nil, fmt.Errorf("authority: qualification: resolve approvers: %w", err)
	}
	qualifiedSet := make(map[uuid.UUID]struct{}, len(qualified))
	for _, uid := range qualified {
		qualifiedSet[uid] = struct{}{}
	}
	if _, ok := qualifiedSet[in.ApproverID]; !ok {
		return nil, fmt.Errorf("%w (approver %s, level %d)",
			ErrApproverNotQualified, in.ApproverID, in.LevelN)
	}

	// Insert the decision row. UNIQUE (workflow_id, level_n, approver_id)
	// catches duplicates → ErrDuplicateDecision.
	const insertDecision = `
		INSERT INTO _doa_workflow_decisions (
			workflow_id, level_n, approver_id,
			approver_role, approver_resolution,
			decision, memo
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`
	var decisionID uuid.UUID
	err = tx.QueryRow(ctx, insertDecision,
		in.WorkflowID, in.LevelN, in.ApproverID,
		nullString(in.ApproverRole), nullString(in.ApproverResolution),
		in.Decision, nullString(in.Memo),
	).Scan(&decisionID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateDecision
		}
		return nil, fmt.Errorf("authority: insert decision: %w", err)
	}

	// Reject = immediate terminal veto.
	if in.Decision == DecisionRejected {
		const rejectStmt = `
			UPDATE _doa_workflows
			SET status = 'rejected',
			    current_level = NULL,
			    terminal_reason = 'rejected by approver',
			    terminal_by = $2,
			    terminal_at = now()
			WHERE id = $1`
		if _, err := tx.Exec(ctx, rejectStmt, in.WorkflowID, in.ApproverID); err != nil {
			return nil, fmt.Errorf("authority: reject workflow: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("authority: commit reject: %w", err)
		}
		return s.GetWorkflow(ctx, in.WorkflowID)
	}

	// Approved path — evaluate level satisfaction. Reuse the level
	// loaded for qualification check above to avoid a redundant query.
	level := qLevel
	satisfied, err := s.evalLevelSatisfaction(ctx, tx, in.WorkflowID, level, tenantID)
	if err != nil {
		return nil, err
	}
	if !satisfied {
		// Level needs more decisions — leave workflow running on same level.
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("authority: commit pending: %w", err)
		}
		return s.GetWorkflow(ctx, in.WorkflowID)
	}

	// Level satisfied — does the matrix have a next level?
	nextLevel, err := s.getLevelByMatrixAndN(ctx, tx, matrixID, in.LevelN+1)
	if err != nil && !errors.Is(err, ErrMatrixNotFound) {
		return nil, err
	}
	if nextLevel == nil {
		// Final level reached → workflow completed (still NOT consumed).
		const completeStmt = `
			UPDATE _doa_workflows
			SET status = 'completed',
			    current_level = NULL,
			    terminal_at = now()
			WHERE id = $1`
		if _, err := tx.Exec(ctx, completeStmt, in.WorkflowID); err != nil {
			return nil, fmt.Errorf("authority: complete workflow: %w", err)
		}
	} else {
		// Advance to next level.
		const advanceStmt = `
			UPDATE _doa_workflows
			SET current_level = $2
			WHERE id = $1`
		if _, err := tx.Exec(ctx, advanceStmt, in.WorkflowID, in.LevelN+1); err != nil {
			return nil, fmt.Errorf("authority: advance workflow: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("authority: commit advance: %w", err)
	}
	return s.GetWorkflow(ctx, in.WorkflowID)
}

// CancelWorkflow transitions a running workflow to 'cancelled' (only
// the initiator can do this — caller enforces ACL upstream). Reason
// optional.
func (s *Store) CancelWorkflow(ctx context.Context, id uuid.UUID, by uuid.UUID,
	reason string) (*Workflow, error) {
	const stmt = `
		UPDATE _doa_workflows
		SET status = 'cancelled',
		    current_level = NULL,
		    terminal_reason = $2,
		    terminal_by = $3,
		    terminal_at = now()
		WHERE id = $1 AND status = 'running'`
	tag, err := s.pool.Exec(ctx, stmt, id, nullString(reason), by)
	if err != nil {
		return nil, fmt.Errorf("authority: cancel workflow: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Differentiate missing vs terminal.
		var status string
		probeErr := s.pool.QueryRow(ctx,
			`SELECT status FROM _doa_workflows WHERE id = $1`, id,
		).Scan(&status)
		if errors.Is(probeErr, pgx.ErrNoRows) {
			return nil, ErrWorkflowNotFound
		}
		return nil, ErrWorkflowTerminal
	}
	return s.GetWorkflow(ctx, id)
}

// ListWorkflowsForApprover returns RUNNING workflows where the given
// user is in the qualified-approver pool of the workflow's current
// level AND hasn't already decided at that level. This is the inbox
// query — "what's waiting for my signature?".
//
// Slice 1 implementation: joins _doa_workflows → _doa_matrix_levels →
// _doa_matrix_approvers, expands roles via _user_roles, expands
// delegations via _doa_delegations. NOT excluded yet: initiator
// self-approval (Slice 1 enhancement); the caller (authorityapi)
// filters initiator out post-hoc to preserve SoD.
//
// Slice 2+ enhancement: pagination + sort by oldest-pending first
// (so signers see the most-urgent items at the top).
func (s *Store) ListWorkflowsForApprover(ctx context.Context, userID uuid.UUID) ([]Workflow, error) {
	// The query has three components:
	//   1. Base set: running workflows.
	//   2. Pool membership: at least one approver entry on the workflow's
	//      current level resolves (via role or user or delegation) to userID.
	//   3. Not-yet-decided at current_level by userID.
	//
	// We build it with three EXISTS clauses + one NOT EXISTS to keep
	// it linear over the (small) workflow set.
	// Note: $1 = user UUID, $2 = same user as TEXT (for matrix_approvers.
	// approver_ref comparison since that column is TEXT), $3 = tenantID.
	const stmt = `
		SELECT w.id, w.tenant_id, w.matrix_id, w.matrix_version,
		       w.collection, w.record_id, w.action_key, w.requested_diff,
		       w.amount, COALESCE(w.currency, ''),
		       w.initiator_id, COALESCE(w.notes, ''),
		       w.status, w.current_level,
		       COALESCE(w.terminal_reason, ''), w.terminal_by, w.terminal_at,
		       w.consumed_at,
		       w.created_at, w.expires_at
		FROM _doa_workflows w
		WHERE w.status = 'running'
		  AND w.current_level IS NOT NULL
		  AND EXISTS (
		    SELECT 1
		    FROM _doa_matrix_levels lvl
		    JOIN _doa_matrix_approvers ap ON ap.level_id = lvl.id
		    WHERE lvl.matrix_id = w.matrix_id
		      AND lvl.level_n = w.current_level
		      AND (
		        (ap.approver_type = 'user' AND ap.approver_ref = $2)
		        OR
		        (ap.approver_type = 'role'
		         AND EXISTS (
		           SELECT 1 FROM _user_roles ur
		           JOIN _roles r ON r.id = ur.role_id
		           WHERE ur.record_id = $1
		             AND r.name = ap.approver_ref
		             AND ($3::uuid IS NULL OR ur.tenant_id = $3 OR ur.tenant_id IS NULL)
		         ))
		        OR
		        EXISTS (
		          SELECT 1 FROM _doa_delegations d
		          WHERE d.delegatee_id = $1
		            AND d.status = 'active'
		            AND d.effective_from <= now()
		            AND (d.effective_to IS NULL OR d.effective_to > now())
		            AND (w.tenant_id IS NULL OR d.tenant_id IS NULL OR d.tenant_id = w.tenant_id)
		            AND (d.source_action_keys IS NULL OR w.action_key = ANY(d.source_action_keys))
		            AND (w.amount IS NULL OR d.max_amount IS NULL OR d.max_amount >= w.amount)
		            AND (
		              (ap.approver_type = 'user' AND ap.approver_ref = d.delegator_id::text)
		              OR
		              (ap.approver_type = 'role' AND EXISTS (
		                SELECT 1 FROM _user_roles ur2
		                JOIN _roles r2 ON r2.id = ur2.role_id
		                WHERE ur2.record_id = d.delegator_id
		                  AND r2.name = ap.approver_ref
		                  AND ($3::uuid IS NULL OR ur2.tenant_id = $3 OR ur2.tenant_id IS NULL)
		              ))
		            )
		        )
		      )
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM _doa_workflow_decisions dec
		    WHERE dec.workflow_id = w.id
		      AND dec.level_n = w.current_level
		      AND dec.approver_id = $1
		  )
		ORDER BY w.created_at ASC
		LIMIT 100`

	rows, err := s.pool.Query(ctx, stmt, userID, userID.String(), (*uuid.UUID)(nil))
	if err != nil {
		return nil, fmt.Errorf("authority: list workflows for approver: %w", err)
	}
	defer rows.Close()

	out := make([]Workflow, 0, 16)
	for rows.Next() {
		var wf Workflow
		var diffRaw []byte
		err := rows.Scan(
			&wf.ID, &wf.TenantID, &wf.MatrixID, &wf.MatrixVersion,
			&wf.Collection, &wf.RecordID, &wf.ActionKey, &diffRaw,
			&wf.Amount, &wf.Currency,
			&wf.InitiatorID, &wf.Notes,
			&wf.Status, &wf.CurrentLevel,
			&wf.TerminalReason, &wf.TerminalBy, &wf.TerminalAt,
			&wf.ConsumedAt,
			&wf.CreatedAt, &wf.ExpiresAt,
		)
		if err != nil {
			return nil, fmt.Errorf("authority: scan workflow: %w", err)
		}
		wf.RequestedDiff = diffRaw
		out = append(out, wf)
	}
	return out, nil
}

// ListWorkflowsForInitiator returns workflows where the given user
// is the initiator, ordered by creation time descending. Slice 0
// minimum — full inbox (qualified-approver filtering) lands in Slice 1.
func (s *Store) ListWorkflowsForInitiator(ctx context.Context, userID uuid.UUID) ([]Workflow, error) {
	const stmt = `
		SELECT id, tenant_id, matrix_id, matrix_version,
		       collection, record_id, action_key, requested_diff,
		       amount, COALESCE(currency, ''),
		       initiator_id, COALESCE(notes, ''),
		       status, current_level,
		       COALESCE(terminal_reason, ''), terminal_by, terminal_at,
		       consumed_at,
		       created_at, expires_at
		FROM _doa_workflows
		WHERE initiator_id = $1
		ORDER BY created_at DESC
		LIMIT 100`

	rows, err := s.pool.Query(ctx, stmt, userID)
	if err != nil {
		return nil, fmt.Errorf("authority: list workflows by initiator: %w", err)
	}
	defer rows.Close()

	out := make([]Workflow, 0, 16)
	for rows.Next() {
		var wf Workflow
		var diffRaw []byte
		err := rows.Scan(
			&wf.ID, &wf.TenantID, &wf.MatrixID, &wf.MatrixVersion,
			&wf.Collection, &wf.RecordID, &wf.ActionKey, &diffRaw,
			&wf.Amount, &wf.Currency,
			&wf.InitiatorID, &wf.Notes,
			&wf.Status, &wf.CurrentLevel,
			&wf.TerminalReason, &wf.TerminalBy, &wf.TerminalAt,
			&wf.ConsumedAt,
			&wf.CreatedAt, &wf.ExpiresAt,
		)
		if err != nil {
			return nil, fmt.Errorf("authority: scan workflow: %w", err)
		}
		wf.RequestedDiff = diffRaw
		out = append(out, wf)
	}
	return out, nil
}

// MarkConsumed sets consumed_at on a completed workflow. Called by
// the gate / consume validation path after the actual DB write
// commits (in the SAME tx — see ProtectedFields consume validation
// invariant from docs/26 §Lifecycle).
//
// Slice 0 — this is the integration point the gate middleware will
// drive once it's wired. Standalone callable for now.
func (s *Store) MarkConsumed(ctx context.Context, q Querier, id uuid.UUID) error {
	const stmt = `
		UPDATE _doa_workflows
		SET consumed_at = now()
		WHERE id = $1 AND status = 'completed' AND consumed_at IS NULL`
	tag, err := q.Exec(ctx, stmt, id)
	if err != nil {
		return fmt.Errorf("authority: mark consumed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("authority: workflow not eligible for consume (status != completed or already consumed)")
	}
	return nil
}

// ── helpers ────────────────────────────────────────────────────────

// getLevelByMatrixAndN looks up a single level by (matrix_id, level_n)
// using the supplied transaction (so it sees uncommitted changes).
func (s *Store) getLevelByMatrixAndN(ctx context.Context, tx pgx.Tx, matrixID uuid.UUID,
	levelN int) (*Level, error) {
	const stmt = `
		SELECT id, level_n, name, mode, min_approvals, escalation_hours
		FROM _doa_matrix_levels
		WHERE matrix_id = $1 AND level_n = $2`
	lvl := &Level{MatrixID: matrixID}
	err := tx.QueryRow(ctx, stmt, matrixID, levelN).Scan(
		&lvl.ID, &lvl.LevelN, &lvl.Name, &lvl.Mode,
		&lvl.MinApprovals, &lvl.EscalationHours,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMatrixNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authority: get level: %w", err)
	}
	// Load approvers (needed for evalLevelSatisfaction with mode=all).
	const apStmt = `
		SELECT id, approver_type, approver_ref, auto_resolve
		FROM _doa_matrix_approvers
		WHERE level_id = $1`
	rows, err := tx.Query(ctx, apStmt, lvl.ID)
	if err != nil {
		return nil, fmt.Errorf("authority: list approvers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ap Approver
		ap.LevelID = lvl.ID
		if err := rows.Scan(&ap.ID, &ap.ApproverType, &ap.ApproverRef, &ap.AutoResolve); err != nil {
			return nil, fmt.Errorf("authority: scan approver: %w", err)
		}
		lvl.Approvers = append(lvl.Approvers, ap)
	}
	return lvl, nil
}

// evalLevelSatisfaction returns true when the workflow's decisions
// at the level meet the level's mode requirement.
//   - any:       ≥1 approved decision
//   - all:       every qualified approver has approved
//   - threshold: ≥ min_approvals approved decisions
func (s *Store) evalLevelSatisfaction(ctx context.Context, tx pgx.Tx, workflowID uuid.UUID,
	level *Level, tenantID *uuid.UUID) (bool, error) {
	// Count approveds at this level.
	const countStmt = `
		SELECT COUNT(*) FROM _doa_workflow_decisions
		WHERE workflow_id = $1 AND level_n = $2 AND decision = 'approved'`
	var approvedCount int
	if err := tx.QueryRow(ctx, countStmt, workflowID, level.LevelN).Scan(&approvedCount); err != nil {
		return false, fmt.Errorf("authority: count approveds: %w", err)
	}

	switch level.Mode {
	case ModeAny:
		return approvedCount >= 1, nil
	case ModeThreshold:
		if level.MinApprovals == nil {
			return false, fmt.Errorf("authority: threshold mode on level %d missing min_approvals",
				level.LevelN)
		}
		return approvedCount >= *level.MinApprovals, nil
	case ModeAll:
		// Need every qualified approver to have approved. Resolve full
		// pool and check each is present in decisions.
		qualified, err := s.ResolveApprovers(ctx, level, tenantID)
		if err != nil {
			return false, fmt.Errorf("authority: resolve approvers: %w", err)
		}
		if len(qualified) == 0 {
			// Defensive — empty approver pool can never satisfy mode=all.
			return false, nil
		}
		// Fetch approved approver_ids on this level.
		const approverIDStmt = `
			SELECT approver_id FROM _doa_workflow_decisions
			WHERE workflow_id = $1 AND level_n = $2 AND decision = 'approved'`
		rows, err := tx.Query(ctx, approverIDStmt, workflowID, level.LevelN)
		if err != nil {
			return false, fmt.Errorf("authority: list approver_ids: %w", err)
		}
		defer rows.Close()
		approved := make(map[uuid.UUID]struct{}, len(qualified))
		for rows.Next() {
			var uid uuid.UUID
			if err := rows.Scan(&uid); err != nil {
				return false, fmt.Errorf("authority: scan approver_id: %w", err)
			}
			approved[uid] = struct{}{}
		}
		for _, uid := range qualified {
			if _, ok := approved[uid]; !ok {
				return false, nil // missing approval from this qualified actor
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("authority: unknown level mode %q on level %d",
			level.Mode, level.LevelN)
	}
}

// isUniqueViolation checks pgx errors for SQLSTATE 23505.
func isUniqueViolation(err error) bool {
	// Use string match — keeps us from pulling pgconn just for this.
	// pgx wraps pgconn errors с message containing "23505".
	return err != nil && (errMessageContains(err, "23505") ||
		errMessageContains(err, "duplicate key"))
}

func errMessageContains(err error, sub string) bool {
	if err == nil {
		return false
	}
	return containsSubstring(err.Error(), sub)
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
