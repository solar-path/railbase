package jobs

// v1.7.43 §3.1 — retry sweeper for failed welcome emails.
//
// Background. Welcome emails sit in two buckets after the normal
// send_email_async runner finishes with them:
//
//   - status='completed' — happy path, nothing for the sweeper to do
//   - status='failed'    — exhausted MaxAttempts (24 for welcomes per
//                          bootstrap.go), last_error populated. WITHOUT
//                          this sweeper, those rows would sit forever.
//
// The common cause of welcome-email failure is SMTP being
// mis-configured at create-time + operator fixing it later. The
// sweeper periodically resurrects failed welcomes so the next worker
// poll picks them up and they finally land — usually 30 minutes
// after the operator fixes the config.
//
// What it does NOT do:
//
//   - It does NOT resurrect non-welcome send_email_async jobs. A failed
//     password_reset two days late is worse than no email — the reset
//     link has likely expired and the user already requested another.
//   - It does NOT touch jobs older than 7 days. Welcome content (login
//     URL, getting-started links) goes stale; if it hasn't landed in a
//     week, an operator-initiated re-send with fresh content is the
//     right escape hatch.
//   - It does NOT resurrect permanent-failure jobs (ones marked via
//     `last_error LIKE '%permanent failure%'`). Those are doomed by
//     payload shape, not transient config.
//
// Safe to run on an empty `_jobs` table — the UPDATE finds zero rows
// and the job logs "0 resurrected" and exits clean.

import (
	"context"
	"fmt"
	"log/slog"
)

// RegisterWelcomeEmailRetryBuiltins installs the
// `retry_failed_welcome_emails` job kind on reg. Called from app.go
// alongside the other RegisterMailerBuiltins / RegisterFileBuiltins
// shape. nil pool → no-op (mirrors the other registrars).
func RegisterWelcomeEmailRetryBuiltins(reg *Registry, db ExecQuerier, log *slog.Logger) {
	if db == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("retry_failed_welcome_emails", func(ctx context.Context, j *Job) error {
		// One UPDATE flips status='failed' → 'pending' for welcome
		// kinds that meet ALL of:
		//   - kind = 'send_email_async'
		//   - payload's `template` field is admin_welcome or
		//     admin_created_notice (the v1.7.43 welcome family)
		//   - status = 'failed'
		//   - last_error doesn't carry the "permanent failure" sentinel
		//   - the failure landed > 15 minutes ago (don't dogpile fresh
		//     fails — let the standard exp-backoff retry layer have its
		//     turn first). `completed_at` is populated by Store.Fail
		//     so it's the canonical "when did this fail" timestamp.
		//   - the job was created < 7 days ago (welcome content goes
		//     stale beyond that)
		//
		// run_after = now() so the next worker poll picks them up
		// immediately. completed_at = NULL because we're un-completing
		// the job (consistent with the in-flight state machine).
		// We DON'T reset attempts to 0 — keeping the counter at
		// MaxAttempts means "this is a re-issue, not a fresh job"
		// for the audit story.
		tag, err := db.Exec(ctx, `
			UPDATE _jobs
			   SET status       = 'pending',
			       run_after    = now(),
			       last_error   = NULL,
			       completed_at = NULL
			 WHERE kind   = 'send_email_async'
			   AND status = 'failed'
			   AND payload->>'template' IN ('admin_welcome', 'admin_created_notice')
			   AND COALESCE(last_error, '') NOT ILIKE '%permanent failure%'
			   AND completed_at IS NOT NULL
			   AND completed_at < now() - interval '15 minutes'
			   AND created_at > now() - interval '7 days'
		`)
		if err != nil {
			return fmt.Errorf("retry_failed_welcome_emails: %w", err)
		}
		n := tag.RowsAffected()
		if n > 0 {
			log.Info("jobs: retry_failed_welcome_emails", "resurrected", n)
		}
		return nil
	})
}
