//go:build embed_pg

package jobs

// E2E tests for v1.7.43 retry_failed_welcome_emails sweeper.
//
// Strategy: stand up an embed-PG instance via the package-local TestMain,
// seed `_jobs` with a mix of welcome/non-welcome × pending/failed/permanent
// rows, run the sweeper once, assert which rows got resurrected.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

var (
	wePool *pgxpool.Pool
	weCtx  context.Context
)

func TestMain(m *testing.M) {
	// Same os.Exit-defers leak fix as v1.7.35d: wrap m.Run() so the
	// embedded-pg stopPG defer actually fires before process exit.
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir, err := os.MkdirTemp("", "railbase-jobs-welcome-retry-*")
	if err != nil {
		panic("jobs test: tempdir: " + err.Error())
	}
	defer os.RemoveAll(dataDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		panic("jobs test: embedded pg: " + err.Error())
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("jobs test: pgxpool: " + err.Error())
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		panic("jobs test: migrate: " + err.Error())
	}

	wePool = pool
	weCtx = ctx
	return m.Run()
}

// seedRow inserts one fake _jobs row with the given (kind, status,
// payload, last_error, ages). We hand-write the INSERT because the
// public Enqueue path doesn't let us forge the status / age columns
// the sweeper queries on.
//
// failedAgo / createdAgo are durations BEFORE now. `completed_at` is
// set when status='failed' so the sweeper's age filter sees the
// failure age, not the create age.
func seedRow(t *testing.T, kind string, template string, status string, lastErr string, failedAgo, createdAgo time.Duration) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"template": template})
	_, err := wePool.Exec(weCtx, `
		INSERT INTO _jobs (id, queue, kind, payload, status, max_attempts, attempts, run_after, last_error, created_at, completed_at)
		VALUES (gen_random_uuid(), 'default', $1, $2, $3, 24, 24, now(), $4, now() - $5::interval, now() - $6::interval)
	`, kind, payload, status, lastErr, createdAgo.String(), failedAgo.String())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// runSweeperOnce constructs a fresh Registry, registers the sweeper,
// dispatches one Job through it. We bypass the actual jobs.Runner +
// poller because we want a deterministic "fire once, assert state"
// flow.
func runSweeperOnce(t *testing.T) {
	t.Helper()
	reg := NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	RegisterWelcomeEmailRetryBuiltins(reg, wePool, nil)
	handler := reg.Lookup("retry_failed_welcome_emails")
	if handler == nil {
		t.Fatalf("sweeper not registered")
	}
	if err := handler(weCtx, &Job{}); err != nil {
		t.Fatalf("sweeper run: %v", err)
	}
}

// countByStatus returns the number of jobs in the given status.
func countByStatus(t *testing.T, status string) int {
	t.Helper()
	var n int
	if err := wePool.QueryRow(weCtx, `SELECT count(*) FROM _jobs WHERE status = $1`, status).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// cleanJobs wipes all _jobs rows so test cases don't pollute each
// other. Cheap on the small fixture sizes here.
func cleanJobs(t *testing.T) {
	t.Helper()
	if _, err := wePool.Exec(weCtx, `DELETE FROM _jobs`); err != nil {
		t.Fatalf("clean: %v", err)
	}
}

// TestRetryWelcome_ResurrectsFailedWelcome — a failed admin_welcome
// row older than 15 min and younger than 7 days gets flipped back to
// pending.
func TestRetryWelcome_ResurrectsFailedWelcome(t *testing.T) {
	cleanJobs(t)
	seedRow(t, "send_email_async", "admin_welcome", "failed",
		"SMTP timeout", 30*time.Minute, 1*time.Hour)

	if countByStatus(t, "pending") != 0 {
		t.Fatalf("pre: want 0 pending, got %d", countByStatus(t, "pending"))
	}
	if countByStatus(t, "failed") != 1 {
		t.Fatalf("pre: want 1 failed, got %d", countByStatus(t, "failed"))
	}

	runSweeperOnce(t)

	if countByStatus(t, "pending") != 1 {
		t.Errorf("post: want 1 pending (resurrected), got %d", countByStatus(t, "pending"))
	}
	if countByStatus(t, "failed") != 0 {
		t.Errorf("post: want 0 failed, got %d", countByStatus(t, "failed"))
	}
}

// TestRetryWelcome_ResurrectsBroadcastNotice — same logic for
// admin_created_notice.
func TestRetryWelcome_ResurrectsBroadcastNotice(t *testing.T) {
	cleanJobs(t)
	seedRow(t, "send_email_async", "admin_created_notice", "failed",
		"SMTP timeout", 30*time.Minute, 1*time.Hour)

	runSweeperOnce(t)

	if countByStatus(t, "pending") != 1 {
		t.Errorf("post: want 1 pending, got %d", countByStatus(t, "pending"))
	}
}

// TestRetryWelcome_LeavesPasswordResetAlone — sweeper is welcome-only.
// password_reset emails older than X don't get resurrected because
// the link is likely expired by then.
func TestRetryWelcome_LeavesPasswordResetAlone(t *testing.T) {
	cleanJobs(t)
	seedRow(t, "send_email_async", "password_reset", "failed",
		"SMTP timeout", 30*time.Minute, 1*time.Hour)

	runSweeperOnce(t)

	if countByStatus(t, "pending") != 0 {
		t.Errorf("post: password_reset was resurrected — sweeper should be welcome-only, got %d pending",
			countByStatus(t, "pending"))
	}
	if countByStatus(t, "failed") != 1 {
		t.Errorf("post: want 1 failed (untouched), got %d", countByStatus(t, "failed"))
	}
}

// TestRetryWelcome_IgnoresPermanentFailures — rows with "permanent
// failure" in last_error are doomed; resurrecting them just wastes
// retries.
func TestRetryWelcome_IgnoresPermanentFailures(t *testing.T) {
	cleanJobs(t)
	seedRow(t, "send_email_async", "admin_welcome", "failed",
		"send_email_async: missing 'to' recipients: permanent failure",
		30*time.Minute, 1*time.Hour)

	runSweeperOnce(t)

	if countByStatus(t, "pending") != 0 {
		t.Errorf("post: permanent-failure resurrected, got %d pending",
			countByStatus(t, "pending"))
	}
}

// TestRetryWelcome_IgnoresFreshFailures — rows that failed less than
// 15 min ago are still in the standard exp-backoff window; sweeper
// stays out of their way.
func TestRetryWelcome_IgnoresFreshFailures(t *testing.T) {
	cleanJobs(t)
	seedRow(t, "send_email_async", "admin_welcome", "failed",
		"SMTP timeout", 5*time.Minute, 1*time.Hour)

	runSweeperOnce(t)

	if countByStatus(t, "pending") != 0 {
		t.Errorf("post: fresh failure resurrected, got %d pending",
			countByStatus(t, "pending"))
	}
	if countByStatus(t, "failed") != 1 {
		t.Errorf("post: want 1 failed (untouched), got %d", countByStatus(t, "failed"))
	}
}

// TestRetryWelcome_IgnoresStaleWelcome — rows older than 7 days are
// "welcome content went stale" territory; operator should re-trigger
// manually with fresh content.
func TestRetryWelcome_IgnoresStaleWelcome(t *testing.T) {
	cleanJobs(t)
	seedRow(t, "send_email_async", "admin_welcome", "failed",
		"SMTP timeout", 30*time.Minute, 30*24*time.Hour)

	runSweeperOnce(t)

	if countByStatus(t, "pending") != 0 {
		t.Errorf("post: stale (>7d) welcome resurrected, got %d pending",
			countByStatus(t, "pending"))
	}
}

// TestRetryWelcome_EmptyTable — sweeper on an empty _jobs is a clean
// no-op (no panic, no error).
func TestRetryWelcome_EmptyTable(t *testing.T) {
	cleanJobs(t)
	runSweeperOnce(t)
	if countByStatus(t, "pending") != 0 {
		t.Errorf("post: empty table → 0 pending, got %d", countByStatus(t, "pending"))
	}
}
