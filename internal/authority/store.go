package authority

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the minimal pgx surface the Store needs. *pgxpool.Pool
// and pgx.Tx both satisfy it — handlers can begin a tx and thread it
// through where multi-row writes need atomicity (matrix + levels +
// approvers must commit together).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is the CRUD layer over _doa_matrices / _doa_matrix_levels /
// _doa_matrix_approvers / _doa_workflows / _doa_workflow_decisions.
// Constructed once on boot, holds for process lifetime; goroutine-safe.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store bound to the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool for callers that need to begin a
// transaction (matrix creation with nested levels + approvers).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Slice 0 limit: ErrUnsupportedApproverType is returned when admin
// attempts to register position / department_head approvers. The DB
// constraint allows the values (forward-compat); the application
// rejects them at Slice 0 boundary.
var ErrUnsupportedApproverType = errors.New(
	"authority: approver types position/department_head require org-chart primitive (v2.x); use role or user")

// ErrMatrixImmutable is returned when admin attempts to PATCH a
// matrix in non-draft status. To edit an approved matrix, the
// caller must create a new version.
var ErrMatrixImmutable = errors.New(
	"authority: cannot edit matrix in non-draft status; create a new version")

// ErrMatrixNotFound — selection / get returned no rows.
var ErrMatrixNotFound = errors.New("authority: matrix not found")

// CreateMatrix inserts a new matrix row (status='draft') with its
// levels + approvers in a single transaction. All approvers must be
// type=role or type=user (Slice 0 limit); position / department_head
// → ErrUnsupportedApproverType. Returns the populated Matrix on
// success (id assigned, timestamps filled).
func (s *Store) CreateMatrix(ctx context.Context, q Querier, m *Matrix) error {
	// Slice 0 validation — reject unsupported approver types upfront
	// so we don't half-commit a matrix with rejected levels.
	for _, lvl := range m.Levels {
		for _, ap := range lvl.Approvers {
			if ap.ApproverType != ApproverTypeRole && ap.ApproverType != ApproverTypeUser {
				return fmt.Errorf("level %d: %w (got %q)",
					lvl.LevelN, ErrUnsupportedApproverType, ap.ApproverType)
			}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authority: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best effort

	// Default version = 1 if zero-value.
	if m.Version == 0 {
		m.Version = 1
	}
	if m.OnFinalEscalation == "" {
		m.OnFinalEscalation = FinalEscalationExpire
	}

	const insertMatrix = `
		INSERT INTO _doa_matrices (
			tenant_id, key, version, name, description,
			status, effective_from, effective_to,
			min_amount, max_amount, currency, condition_expr,
			on_final_escalation, created_by
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10, $11, $12,
			$13, $14
		)
		RETURNING id, created_at, updated_at, status`

	err = tx.QueryRow(ctx, insertMatrix,
		m.TenantID, m.Key, m.Version, m.Name, nullString(m.Description),
		coalesceStatus(m.Status), m.EffectiveFrom, m.EffectiveTo,
		m.MinAmount, m.MaxAmount, nullString(m.Currency), nullString(m.ConditionExpr),
		m.OnFinalEscalation, m.CreatedBy,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt, &m.Status)
	if err != nil {
		return fmt.Errorf("authority: insert matrix: %w", err)
	}

	// Insert levels + approvers under the new matrix id.
	for i := range m.Levels {
		lvl := &m.Levels[i]
		lvl.MatrixID = m.ID

		const insertLevel = `
			INSERT INTO _doa_matrix_levels (
				matrix_id, level_n, name, mode, min_approvals, escalation_hours
			) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`

		err = tx.QueryRow(ctx, insertLevel,
			lvl.MatrixID, lvl.LevelN, lvl.Name, lvl.Mode,
			lvl.MinApprovals, lvl.EscalationHours,
		).Scan(&lvl.ID)
		if err != nil {
			return fmt.Errorf("authority: insert level %d: %w", lvl.LevelN, err)
		}

		for j := range lvl.Approvers {
			ap := &lvl.Approvers[j]
			ap.LevelID = lvl.ID

			const insertApprover = `
				INSERT INTO _doa_matrix_approvers (
					level_id, approver_type, approver_ref, auto_resolve
				) VALUES ($1, $2, $3, $4)
				RETURNING id`

			err = tx.QueryRow(ctx, insertApprover,
				ap.LevelID, ap.ApproverType, ap.ApproverRef, ap.AutoResolve,
			).Scan(&ap.ID)
			if err != nil {
				return fmt.Errorf("authority: insert approver level=%d: %w",
					lvl.LevelN, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("authority: commit: %w", err)
	}
	return nil
}

// GetMatrix loads a matrix by id, with its levels + approvers fully
// populated. ErrMatrixNotFound if absent.
func (s *Store) GetMatrix(ctx context.Context, id uuid.UUID) (*Matrix, error) {
	const selectMatrix = `
		SELECT id, tenant_id, key, version, name, COALESCE(description, ''),
		       status, COALESCE(revoked_reason, ''), approved_by, approved_at,
		       effective_from, effective_to,
		       min_amount, max_amount, COALESCE(currency, ''), COALESCE(condition_expr, ''),
		       on_final_escalation,
		       created_at, created_by, updated_at
		FROM _doa_matrices
		WHERE id = $1`

	m := &Matrix{}
	err := s.pool.QueryRow(ctx, selectMatrix, id).Scan(
		&m.ID, &m.TenantID, &m.Key, &m.Version, &m.Name, &m.Description,
		&m.Status, &m.RevokedReason, &m.ApprovedBy, &m.ApprovedAt,
		&m.EffectiveFrom, &m.EffectiveTo,
		&m.MinAmount, &m.MaxAmount, &m.Currency, &m.ConditionExpr,
		&m.OnFinalEscalation,
		&m.CreatedAt, &m.CreatedBy, &m.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMatrixNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authority: select matrix: %w", err)
	}

	if err := s.loadLevelsAndApprovers(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// ListMatrices returns a flat list of matrices filtered by status
// and/or key. Levels + approvers are NOT populated; caller fetches
// full bodies via GetMatrix when needed.
//
// Slice 0 omits pagination; embedders will rarely have thousands of
// matrices. If volume becomes a concern, pagination lands в Slice 1.
type ListFilter struct {
	Status   string     // empty = any
	Key      string     // empty = any
	TenantID *uuid.UUID // nil = any
}

func (s *Store) ListMatrices(ctx context.Context, f ListFilter) ([]Matrix, error) {
	const selectList = `
		SELECT id, tenant_id, key, version, name, COALESCE(description, ''),
		       status, COALESCE(revoked_reason, ''),
		       effective_from, effective_to,
		       min_amount, max_amount, COALESCE(currency, ''),
		       on_final_escalation,
		       created_at, updated_at
		FROM _doa_matrices
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR key = $2)
		  AND ($3::uuid IS NULL OR tenant_id = $3)
		ORDER BY key, version DESC, created_at DESC`

	rows, err := s.pool.Query(ctx, selectList, f.Status, f.Key, f.TenantID)
	if err != nil {
		return nil, fmt.Errorf("authority: list matrices: %w", err)
	}
	defer rows.Close()

	out := make([]Matrix, 0, 32)
	for rows.Next() {
		var m Matrix
		err := rows.Scan(
			&m.ID, &m.TenantID, &m.Key, &m.Version, &m.Name, &m.Description,
			&m.Status, &m.RevokedReason,
			&m.EffectiveFrom, &m.EffectiveTo,
			&m.MinAmount, &m.MaxAmount, &m.Currency,
			&m.OnFinalEscalation,
			&m.CreatedAt, &m.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("authority: scan matrix: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authority: list rows: %w", err)
	}
	return out, nil
}

// UpdateDraftMatrix replaces the matrix metadata + levels + approvers
// for a draft matrix. Approved/archived/revoked matrices are
// immutable — ErrMatrixImmutable returned without write.
//
// Implementation: delete existing levels + approvers (cascade), then
// re-insert from the supplied slices. Simple, but means every PATCH
// is a full replace. For Slice 0 acceptable; finer-grained patching
// can land if pressure surfaces.
func (s *Store) UpdateDraftMatrix(ctx context.Context, m *Matrix) error {
	// Pre-check status — fail fast without holding a tx open.
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM _doa_matrices WHERE id = $1`, m.ID,
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMatrixNotFound
	}
	if err != nil {
		return fmt.Errorf("authority: status precheck: %w", err)
	}
	if status != StatusDraft {
		return ErrMatrixImmutable
	}

	// Slice 0 validation — same approver-type restriction as Create.
	for _, lvl := range m.Levels {
		for _, ap := range lvl.Approvers {
			if ap.ApproverType != ApproverTypeRole && ap.ApproverType != ApproverTypeUser {
				return fmt.Errorf("level %d: %w (got %q)",
					lvl.LevelN, ErrUnsupportedApproverType, ap.ApproverType)
			}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authority: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const updateMatrix = `
		UPDATE _doa_matrices
		SET name = $2,
		    description = $3,
		    effective_from = $4,
		    effective_to = $5,
		    min_amount = $6,
		    max_amount = $7,
		    currency = $8,
		    condition_expr = $9,
		    on_final_escalation = $10,
		    updated_at = now()
		WHERE id = $1 AND status = 'draft'
		RETURNING updated_at`

	err = tx.QueryRow(ctx, updateMatrix,
		m.ID, m.Name, nullString(m.Description),
		m.EffectiveFrom, m.EffectiveTo,
		m.MinAmount, m.MaxAmount, nullString(m.Currency), nullString(m.ConditionExpr),
		m.OnFinalEscalation,
	).Scan(&m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("authority: update matrix: %w", err)
	}

	// Wipe levels + approvers (cascade) and re-insert.
	if _, err := tx.Exec(ctx,
		`DELETE FROM _doa_matrix_levels WHERE matrix_id = $1`, m.ID,
	); err != nil {
		return fmt.Errorf("authority: delete levels: %w", err)
	}

	for i := range m.Levels {
		lvl := &m.Levels[i]
		lvl.MatrixID = m.ID

		err = tx.QueryRow(ctx, `
			INSERT INTO _doa_matrix_levels (
				matrix_id, level_n, name, mode, min_approvals, escalation_hours
			) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			lvl.MatrixID, lvl.LevelN, lvl.Name, lvl.Mode,
			lvl.MinApprovals, lvl.EscalationHours,
		).Scan(&lvl.ID)
		if err != nil {
			return fmt.Errorf("authority: insert level %d: %w", lvl.LevelN, err)
		}

		for j := range lvl.Approvers {
			ap := &lvl.Approvers[j]
			ap.LevelID = lvl.ID
			err = tx.QueryRow(ctx, `
				INSERT INTO _doa_matrix_approvers (
					level_id, approver_type, approver_ref, auto_resolve
				) VALUES ($1, $2, $3, $4) RETURNING id`,
				ap.LevelID, ap.ApproverType, ap.ApproverRef, ap.AutoResolve,
			).Scan(&ap.ID)
			if err != nil {
				return fmt.Errorf("authority: insert approver level=%d: %w",
					lvl.LevelN, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("authority: commit: %w", err)
	}
	return nil
}

// ApproveMatrix transitions a draft matrix to approved status.
// Required parameters: approvedBy (admin actor), effectiveFrom (when
// matrix becomes active). effectiveTo optional (NULL = open-ended).
// After approve, the matrix is IMMUTABLE — further edits require
// creating a new version. Returns ErrMatrixImmutable if not in draft.
func (s *Store) ApproveMatrix(ctx context.Context, id uuid.UUID, approvedBy uuid.UUID,
	effectiveFrom time.Time, effectiveTo *time.Time) error {
	const stmt = `
		UPDATE _doa_matrices
		SET status = 'approved',
		    approved_by = $2,
		    approved_at = now(),
		    effective_from = $3,
		    effective_to = $4,
		    updated_at = now()
		WHERE id = $1 AND status = 'draft'`
	tag, err := s.pool.Exec(ctx, stmt, id, approvedBy, effectiveFrom, effectiveTo)
	if err != nil {
		return fmt.Errorf("authority: approve matrix: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s.classifyMissingDraft(ctx, id)
	}
	return nil
}

// RevokeMatrix transitions an approved matrix to revoked status with
// a required reason. Existing running workflows snapshot the matrix
// version at creation, so revoke does NOT cancel them — they finish
// against their version snapshot. New workflows for this key won't
// match the revoked row (selection filters status='approved').
func (s *Store) RevokeMatrix(ctx context.Context, id uuid.UUID, revokedBy uuid.UUID,
	reason string) error {
	if reason == "" {
		return fmt.Errorf("authority: revoke matrix: reason is required")
	}
	const stmt = `
		UPDATE _doa_matrices
		SET status = 'revoked',
		    revoked_reason = $2,
		    updated_at = now()
		WHERE id = $1 AND status = 'approved'`
	tag, err := s.pool.Exec(ctx, stmt, id, reason)
	if err != nil {
		return fmt.Errorf("authority: revoke matrix: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either missing or not in approved status.
		var status string
		probeErr := s.pool.QueryRow(ctx,
			`SELECT status FROM _doa_matrices WHERE id = $1`, id,
		).Scan(&status)
		if errors.Is(probeErr, pgx.ErrNoRows) {
			return ErrMatrixNotFound
		}
		return fmt.Errorf("authority: cannot revoke matrix in status %q (need approved)", status)
	}
	_ = revokedBy // reserved for audit chain in subsequent slice
	return nil
}

// CreateVersionFromApproved duplicates an approved matrix as a new
// draft row with version=N+1. Levels + approvers are copied with
// fresh ids. The returned matrix is fully populated, ready to PATCH.
func (s *Store) CreateVersionFromApproved(ctx context.Context, sourceID uuid.UUID,
	createdBy uuid.UUID) (*Matrix, error) {
	source, err := s.GetMatrix(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if source.Status != StatusApproved {
		return nil, fmt.Errorf(
			"authority: can only version approved matrices (got status %q)", source.Status)
	}

	newM := &Matrix{
		TenantID:          source.TenantID,
		Key:               source.Key,
		Version:           source.Version + 1,
		Name:              source.Name,
		Description:       source.Description,
		MinAmount:         source.MinAmount,
		MaxAmount:         source.MaxAmount,
		Currency:          source.Currency,
		ConditionExpr:     source.ConditionExpr,
		OnFinalEscalation: source.OnFinalEscalation,
		CreatedBy:         &createdBy,
		// Deliberately NOT copying effective_from/to — operator must set
		// new window when approving the new version.
	}
	// Deep-copy levels (without ids — Create assigns fresh ones).
	for _, srcLvl := range source.Levels {
		newLvl := Level{
			LevelN:          srcLvl.LevelN,
			Name:            srcLvl.Name,
			Mode:            srcLvl.Mode,
			MinApprovals:    srcLvl.MinApprovals,
			EscalationHours: srcLvl.EscalationHours,
		}
		for _, srcAp := range srcLvl.Approvers {
			newLvl.Approvers = append(newLvl.Approvers, Approver{
				ApproverType: srcAp.ApproverType,
				ApproverRef:  srcAp.ApproverRef,
				AutoResolve:  srcAp.AutoResolve,
			})
		}
		newM.Levels = append(newM.Levels, newLvl)
	}

	if err := s.CreateMatrix(ctx, nil, newM); err != nil {
		return nil, err
	}
	return newM, nil
}

// classifyMissingDraft determines whether ApproveMatrix returning 0
// rows is "missing" or "not in draft" — gives the caller a precise
// error envelope.
func (s *Store) classifyMissingDraft(ctx context.Context, id uuid.UUID) error {
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM _doa_matrices WHERE id = $1`, id,
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMatrixNotFound
	}
	if err != nil {
		return fmt.Errorf("authority: status probe: %w", err)
	}
	return ErrMatrixImmutable
}

// DeleteDraftMatrix removes a draft matrix entirely (cascades to
// levels + approvers). Approved matrices cannot be deleted — use
// Revoke instead. Slice 0 implements only the draft-delete path.
func (s *Store) DeleteDraftMatrix(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM _doa_matrices WHERE id = $1 AND status = 'draft'`, id)
	if err != nil {
		return fmt.Errorf("authority: delete matrix: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either missing or not in draft. Probe to disambiguate.
		var status string
		probeErr := s.pool.QueryRow(ctx,
			`SELECT status FROM _doa_matrices WHERE id = $1`, id,
		).Scan(&status)
		if errors.Is(probeErr, pgx.ErrNoRows) {
			return ErrMatrixNotFound
		}
		return ErrMatrixImmutable
	}
	return nil
}

// ── helpers ────────────────────────────────────────────────────────

func (s *Store) loadLevelsAndApprovers(ctx context.Context, m *Matrix) error {
	const selectLevels = `
		SELECT id, level_n, name, mode, min_approvals, escalation_hours
		FROM _doa_matrix_levels
		WHERE matrix_id = $1
		ORDER BY level_n`

	rows, err := s.pool.Query(ctx, selectLevels, m.ID)
	if err != nil {
		return fmt.Errorf("authority: list levels: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var lvl Level
		lvl.MatrixID = m.ID
		if err := rows.Scan(
			&lvl.ID, &lvl.LevelN, &lvl.Name, &lvl.Mode,
			&lvl.MinApprovals, &lvl.EscalationHours,
		); err != nil {
			return fmt.Errorf("authority: scan level: %w", err)
		}
		m.Levels = append(m.Levels, lvl)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("authority: rows err: %w", err)
	}

	// Second pass — load approvers for each level. Could be one
	// JOIN, but two queries is cleaner for prototype's purposes.
	for i := range m.Levels {
		lvl := &m.Levels[i]
		const selectApprovers = `
			SELECT id, approver_type, approver_ref, auto_resolve
			FROM _doa_matrix_approvers
			WHERE level_id = $1
			ORDER BY id`
		apRows, err := s.pool.Query(ctx, selectApprovers, lvl.ID)
		if err != nil {
			return fmt.Errorf("authority: list approvers: %w", err)
		}
		for apRows.Next() {
			var ap Approver
			ap.LevelID = lvl.ID
			if err := apRows.Scan(
				&ap.ID, &ap.ApproverType, &ap.ApproverRef, &ap.AutoResolve,
			); err != nil {
				apRows.Close()
				return fmt.Errorf("authority: scan approver: %w", err)
			}
			lvl.Approvers = append(lvl.Approvers, ap)
		}
		apRows.Close()
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func coalesceStatus(s string) string {
	if s == "" {
		return StatusDraft
	}
	return s
}

