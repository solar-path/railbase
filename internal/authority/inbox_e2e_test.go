//go:build embed_pg

// E2E for the v2.0-alpha approver inbox query
// (Slice 1: ListWorkflowsForApprover).

package authority

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac"
)

func TestInbox_RoleAndUserAndDelegationMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stopPG() }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	authStore := NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	admin := uuid.New()
	chief := uuid.New()
	deputy := uuid.New()
	directUser := uuid.New()
	uninvolved := uuid.New()

	chiefRole, _ := rbacStore.CreateRole(ctx, "chief", rbac.ScopeSite, "")
	_, _ = rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: chief, RoleID: chiefRole.ID,
	})

	// === Matrix with both a role approver AND a direct user approver ===
	m := &Matrix{
		Key: "expenses.approve", Name: "x", CreatedBy: &admin,
		Status: StatusDraft, OnFinalEscalation: FinalEscalationExpire,
		Levels: []Level{{LevelN: 1, Name: "L1", Mode: ModeAny,
			Approvers: []Approver{
				{ApproverType: ApproverTypeRole, ApproverRef: "chief"},
				{ApproverType: ApproverTypeUser, ApproverRef: directUser.String()},
			}}},
	}
	if err := authStore.CreateMatrix(ctx, nil, m); err != nil {
		t.Fatal(err)
	}
	if err := authStore.ApproveMatrix(ctx, m.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	matrix, _ := authStore.SelectActiveMatrix(ctx, SelectFilter{Key: "expenses.approve"})

	// === Delegation: chief → deputy ===
	if _, err := authStore.CreateDelegation(ctx, DelegationCreateInput{
		DelegatorID: chief, DelegateeID: deputy, CreatedBy: &admin,
	}); err != nil {
		t.Fatal(err)
	}

	// === Create 3 running workflows ===
	wf1, _ := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve", RequestedDiff: map[string]any{"x": 1},
		InitiatorID: admin,
	})
	wf2, _ := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve", RequestedDiff: map[string]any{"x": 2},
		InitiatorID: admin,
	})
	wf3, _ := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve", RequestedDiff: map[string]any{"x": 3},
		InitiatorID: admin,
	})

	// === Chief inbox: all 3 (direct role match) ===
	chiefInbox, err := authStore.ListWorkflowsForApprover(ctx, chief)
	if err != nil {
		t.Fatal(err)
	}
	if got := containsAllWFs(chiefInbox, wf1.ID, wf2.ID, wf3.ID); !got {
		t.Errorf("chief inbox: expected all 3 workflows, got %d", len(chiefInbox))
	}

	// === Direct user inbox: all 3 (direct user approver) ===
	directInbox, _ := authStore.ListWorkflowsForApprover(ctx, directUser)
	if !containsAllWFs(directInbox, wf1.ID, wf2.ID, wf3.ID) {
		t.Errorf("directUser inbox: expected all 3, got %d", len(directInbox))
	}

	// === Deputy inbox: all 3 (delegation from chief) ===
	deputyInbox, _ := authStore.ListWorkflowsForApprover(ctx, deputy)
	if !containsAllWFs(deputyInbox, wf1.ID, wf2.ID, wf3.ID) {
		t.Errorf("deputy inbox: expected all 3 via delegation, got %d", len(deputyInbox))
	}

	// === Uninvolved user: empty inbox ===
	uninvolvedInbox, _ := authStore.ListWorkflowsForApprover(ctx, uninvolved)
	if len(uninvolvedInbox) != 0 {
		t.Errorf("uninvolved inbox: expected 0, got %d", len(uninvolvedInbox))
	}

	// === After chief decides on wf1, chief's inbox shrinks but
	// deputy's still includes wf1 (chief decided as chief, not as
	// deputy's-via-delegation). ===
	if _, err := authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf1.ID, LevelN: 1, ApproverID: chief, Decision: DecisionApproved,
	}); err != nil {
		t.Fatal(err)
	}
	// wf1 is now completed — drops out of EVERYONE's inbox.
	post, _ := authStore.ListWorkflowsForApprover(ctx, chief)
	for _, w := range post {
		if w.ID == wf1.ID {
			t.Errorf("chief inbox: wf1 should be gone (workflow completed)")
		}
	}
	post2, _ := authStore.ListWorkflowsForApprover(ctx, deputy)
	for _, w := range post2 {
		if w.ID == wf1.ID {
			t.Errorf("deputy inbox: wf1 should be gone (workflow completed)")
		}
	}

	t.Log("=== Approver inbox E2E green ===")
}

func containsAllWFs(rows []Workflow, ids ...uuid.UUID) bool {
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, w := range rows {
		seen[w.ID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			return false
		}
	}
	return true
}
