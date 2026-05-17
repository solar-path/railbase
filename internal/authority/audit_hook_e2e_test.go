//go:build embed_pg

// E2E for v2.0-alpha DoA audit-chain integration (Slice 2).
// Verifies that AuditHook fires the right events into the existing
// audit.Writer chain with the right payloads.

package authority

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

func TestAuditHook_EmitsDoALifecycle(t *testing.T) {
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

	writer := audit.NewWriter(pool)
	if err := writer.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	hook := NewAuditHook(writer)
	actor := ActorContext{
		UserID:         uuid.New(),
		UserCollection: "_admins",
		IP:             "127.0.0.1",
		UserAgent:      "test/1.0",
	}

	// Synthetic objects — the hook doesn't depend on the actual Store.
	matrix := &Matrix{
		ID: uuid.New(), Key: "expenses.approve", Version: 1, Name: "v1",
		Status: StatusDraft, Levels: []Level{{LevelN: 1}},
	}
	wf := &Workflow{
		ID: uuid.New(), MatrixID: matrix.ID, MatrixVersion: 1,
		Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve", InitiatorID: actor.UserID,
		Status: WorkflowStatusRunning,
	}
	deleg := &Delegation{
		ID: uuid.New(), DelegatorID: uuid.New(), DelegateeID: uuid.New(),
	}

	// Fire all 8 emission types in sequence.
	hook.MatrixCreated(ctx, actor, matrix)
	hook.MatrixApproved(ctx, actor, matrix.ID, matrix.Key, matrix.Version)
	hook.MatrixRevoked(ctx, actor, matrix.ID, "test revoke")
	hook.WorkflowCreated(ctx, actor, wf)
	hook.WorkflowDecision(ctx, actor, wf, DecisionApproved, 1, "ok")
	hook.WorkflowConsumed(ctx, actor, wf.ID, wf.Collection, wf.RecordID.String(), wf.ActionKey)
	hook.WorkflowCancelled(ctx, actor, wf.ID, "second thought")
	hook.DelegationCreated(ctx, actor, deleg)
	hook.DelegationRevoked(ctx, actor, deleg.ID, "vacation over")

	// Query the audit chain — confirm all events landed in order.
	const stmt = `
		SELECT event, user_id, after::text
		FROM _audit_log
		WHERE event LIKE 'authority.%'
		ORDER BY seq`
	rows, err := pool.Query(ctx, stmt)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		event string
		uid   uuid.UUID
		after string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.event, &r.uid, &r.after); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}

	want := []string{
		"authority.matrix.create",
		"authority.matrix.approve",
		"authority.matrix.revoke",
		"authority.workflow.create",
		"authority.workflow.decision.approved",
		"authority.workflow.consume",
		"authority.workflow.cancel",
		"authority.delegation.create",
		"authority.delegation.revoke",
	}
	if len(got) != len(want) {
		t.Fatalf("event count: want %d, got %d (%v)", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i].event != w {
			t.Errorf("event[%d]: want %q, got %q", i, w, got[i].event)
		}
		if got[i].uid != actor.UserID {
			t.Errorf("event[%d] user_id: want %s, got %s", i, actor.UserID, got[i].uid)
		}
	}

	// Sample-decode one row's `after` to confirm the payload structure.
	var consumePayload map[string]any
	for _, r := range got {
		if r.event == "authority.workflow.consume" {
			_ = json.Unmarshal([]byte(r.after), &consumePayload)
			break
		}
	}
	if consumePayload["workflow_id"] == nil {
		t.Errorf("consume payload missing workflow_id: %v", consumePayload)
	}

	// === Chain integrity check ===
	// The chain is hash-linked: every row's hash = sha256(prev_hash || canonical(row)).
	// We just verify the chain isn't corrupted by walking it.
	const verifyStmt = `
		SELECT seq, prev_hash, hash FROM _audit_log ORDER BY seq`
	chainRows, err := pool.Query(ctx, verifyStmt)
	if err != nil {
		t.Fatal(err)
	}
	defer chainRows.Close()
	var prev []byte
	first := true
	for chainRows.Next() {
		var seq int64
		var prevHash, hash []byte
		if err := chainRows.Scan(&seq, &prevHash, &hash); err != nil {
			t.Fatal(err)
		}
		if first {
			first = false
		} else {
			if string(prevHash) != string(prev) {
				t.Errorf("chain integrity broken at seq=%d", seq)
			}
		}
		prev = hash
	}
	t.Log("=== Audit chain integrity confirmed across all DoA events ===")
}

// TestAuditHook_NilSafe verifies that all emission methods short-circuit
// when the hook or its Writer is nil — no panic.
func TestAuditHook_NilSafe(t *testing.T) {
	var nilHook *AuditHook
	actor := ActorContext{}
	nilHook.MatrixCreated(context.Background(), actor, nil)
	nilHook.MatrixApproved(context.Background(), actor, uuid.Nil, "", 0)
	nilHook.MatrixRevoked(context.Background(), actor, uuid.Nil, "")
	nilHook.WorkflowCreated(context.Background(), actor, nil)
	nilHook.WorkflowDecision(context.Background(), actor, nil, "", 0, "")
	nilHook.WorkflowConsumed(context.Background(), actor, uuid.Nil, "", "", "")
	nilHook.WorkflowCancelled(context.Background(), actor, uuid.Nil, "")
	nilHook.DelegationCreated(context.Background(), actor, nil)
	nilHook.DelegationRevoked(context.Background(), actor, uuid.Nil, "")

	hookWithNilWriter := &AuditHook{Writer: nil}
	hookWithNilWriter.MatrixCreated(context.Background(), actor, nil)
	hookWithNilWriter.WorkflowCreated(context.Background(), actor, nil)
}
