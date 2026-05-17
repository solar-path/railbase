//go:build embed_pg

// Slice 0 latency probe — gate.Check overhead on a hot path.
//
// The gate runs on every mutation that potentially crosses an Authority
// declaration. The hot path performs:
//   1. In-memory onMatchesMutation (zero IO).
//   2. SELECT on _doa_workflows by (collection, record_id, action_key)
//      with status filter — index hit via _doa_workflows_lookup_idx.
//   3. If no approved workflow → SelectActiveMatrix query.
//   4. JSON marshal of suggested_diff for the 409 envelope.
//
// Production target: gate overhead < 5ms p99 on a small DB. Slice 0
// just needs to confirm we're in the right order of magnitude (sub-10ms).

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
	"github.com/railbase/railbase/internal/schema/builder"
)

func BenchmarkGateCheck_NoMatch(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dataDir := b.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		b.Fatal(err)
	}

	store := NewStore(pool)
	rb := rbac.NewStore(pool)
	admin := uuid.New()
	editorRole, _ := rb.CreateRole(ctx, "editor", rbac.ScopeSite, "")
	user := uuid.New()
	_, _ = rb.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: user, RoleID: editorRole.ID,
	})

	m := &Matrix{
		Key: "articles.publish", Name: "x", CreatedBy: &admin,
		Status: StatusDraft, OnFinalEscalation: FinalEscalationExpire,
		Levels: []Level{{LevelN: 1, Name: "L1", Mode: ModeAny,
			Approvers: []Approver{{ApproverType: ApproverTypeRole, ApproverRef: "editor"}}}},
	}
	if err := store.CreateMatrix(ctx, nil, m); err != nil {
		b.Fatal(err)
	}
	if err := store.ApproveMatrix(ctx, m.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		b.Fatal(err)
	}

	cfg := builder.AuthorityConfig{
		Name:   "publish",
		Matrix: "articles.publish",
		On:     builder.AuthorityOn{Op: "update", Field: "status", To: []string{"published"}},
	}

	in := CheckInput{
		Op:         "update",
		Collection: "articles",
		RecordID:   uuid.New(),
		BeforeFields: map[string]any{"status": "draft"},
		AfterFields:  map[string]any{"status": "published", "title": "x"},
		Authorities:  []builder.AuthorityConfig{cfg},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Check(ctx, in)
	}
}
