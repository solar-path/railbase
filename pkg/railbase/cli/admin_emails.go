package cli

// v1.7.43 — CLI-side helper that enqueues admin-welcome + broadcast
// emails for `railbase admin create`. Mirrors the bootstrap-handler
// path in adminapi.bootstrap.go::EnqueueAdminEmails but lives here
// to keep the CLI free of adminapi dependencies (which pulls in chi,
// every admin route, etc.).
//
// The wire payload + template names are identical to the handler
// path, so consumers of `_jobs` rows can't tell which surface
// enqueued a given welcome email except via the {{ event.via }} tag.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/settings"
)

// enqueueAdminEmailsCLI is the CLI-local twin of
// adminapi.EnqueueAdminEmails. It constructs a transient
// settings.Manager + admins.Store off the runtime pool to read the
// skip flag + the existing-admin list, then issues two kinds of
// send_email_async job rows.
//
// Returns nil if the mailer was explicitly skipped (no work to do)
// OR if at least the welcome email was enqueued. Broadcast-fan-out
// failures are logged to the runtime's slog but do NOT cause an
// overall error — the welcome email is the important one.
func enqueueAdminEmailsCLI(ctx context.Context, rt *runtimeContext, newAdmin *admins.Admin, via, createdBy string) error {
	mgr := settings.New(settings.Options{Pool: rt.pool.Pool}) // nil bus — CLI doesn't invalidate live caches
	if mgr != nil {
		if skipped, _, _ := mgr.GetString(ctx, "mailer.setup_skipped_at"); skipped != "" {
			fmt.Println("note: mailer.setup_skipped_at is set — no welcome / broadcast emails enqueued")
			return nil
		}
	}

	siteName := readSiteNameCLI(ctx, mgr)
	fromAddr := readFromAddrCLI(ctx, mgr)
	adminURL := readAdminURLCLI(ctx, mgr)
	now := time.Now().UTC().Format(time.RFC3339)

	store := jobs.NewStore(rt.pool.Pool)

	welcomePayload := map[string]any{
		"template": "admin_welcome",
		"to": []map[string]any{
			{"email": newAdmin.Email},
		},
		"data": map[string]any{
			"admin": map[string]any{
				"email": newAdmin.Email,
				"id":    newAdmin.ID.String(),
			},
			"event": map[string]any{
				"at":         now,
				"created_by": createdBy,
				"via":        via,
			},
			"site": map[string]any{"name": siteName, "from": fromAddr},
			"admin_url": adminURL,
		},
	}
	if _, err := store.Enqueue(ctx, "send_email_async", welcomePayload, jobs.EnqueueOptions{
		Queue:       "default",
		MaxAttempts: 24,
	}); err != nil {
		return fmt.Errorf("enqueue welcome: %w", err)
	}

	// Broadcast notice to every OTHER admin. Best-effort; one failure
	// doesn't tank the rest.
	adminStore := admins.NewStore(rt.pool.Pool)
	others, err := adminStore.List(ctx)
	if err != nil {
		rt.log.Warn("admin email: list-other-admins failed, broadcast skipped", "err", err)
		return nil
	}
	for _, other := range others {
		if other.ID == newAdmin.ID {
			continue
		}
		noticePayload := map[string]any{
			"template": "admin_created_notice",
			"to": []map[string]any{
				{"email": other.Email},
			},
			"data": map[string]any{
				"recipient": map[string]any{"email": other.Email},
				"new_admin": map[string]any{
					"email": newAdmin.Email,
					"id":    newAdmin.ID.String(),
				},
				"event": map[string]any{
					"at":         now,
					"created_by": createdBy,
					"via":        via,
				},
				"site":      map[string]any{"name": siteName, "from": fromAddr},
				"admin_url": adminURL,
			},
		}
		if _, err := store.Enqueue(ctx, "send_email_async", noticePayload, jobs.EnqueueOptions{
			Queue:       "default",
			MaxAttempts: 24,
		}); err != nil {
			rt.log.Warn("admin email: broadcast notice enqueue failed",
				"recipient", other.Email, "new_admin", newAdmin.Email, "err", err)
		}
	}
	return nil
}

// readSiteNameCLI mirrors readAdminSiteName from adminapi/bootstrap.go.
// Hand-rolled here so the CLI doesn't depend on adminapi.
func readSiteNameCLI(ctx context.Context, mgr *settings.Manager) string {
	if mgr != nil {
		if v, ok, _ := mgr.GetString(ctx, "site.name"); ok && v != "" {
			return v
		}
	}
	return "Railbase"
}

// readFromAddrCLI resolves the mailer.from setting. Empty if not set —
// the template will render an empty From which the mailer driver may
// reject; that's expected (operator hasn't completed mailer setup
// or chose to skip it).
func readFromAddrCLI(ctx context.Context, mgr *settings.Manager) string {
	if mgr != nil {
		if v, ok, _ := mgr.GetString(ctx, "mailer.from"); ok && v != "" {
			return v
		}
	}
	return ""
}

// stringGetter is the subset of settings.Manager the
// mailer-unconfigured probe needs. Narrow interface so the logic is
// pure-function-testable without spinning up a real Postgres pool.
type stringGetter interface {
	GetString(ctx context.Context, key string) (string, bool, error)
}

// mailerUnconfiguredCLI returns true iff the operator hasn't set
// `mailer.from` AND hasn't marked mailer setup as skipped. Used by
// `admin create` to print a one-line "your welcome email won't
// deliver" note up-front (FEEDBACK #10). A fresh `railbase init` ends
// up here on first admin creation — without this, the welcome email
// gets enqueued silently and the operator wonders why their inbox is
// empty.
func mailerUnconfiguredCLI(ctx context.Context, rt *runtimeContext) bool {
	mgr := settings.New(settings.Options{Pool: rt.pool.Pool})
	if mgr == nil {
		return false
	}
	return mailerUnconfiguredFrom(ctx, mgr)
}

// mailerUnconfiguredFrom is the testable body of mailerUnconfiguredCLI.
// Receives any settings-shaped getter so unit tests can pass a stub
// without touching the database.
func mailerUnconfiguredFrom(ctx context.Context, g stringGetter) bool {
	if v, ok, _ := g.GetString(ctx, "mailer.setup_skipped_at"); ok && v != "" {
		return false // explicit skip — operator knows
	}
	from, _, _ := g.GetString(ctx, "mailer.from")
	return strings.TrimSpace(from) == ""
}

// readAdminURLCLI mirrors readAdminURL from adminapi/bootstrap.go.
func readAdminURLCLI(ctx context.Context, mgr *settings.Manager) string {
	if mgr != nil {
		if v, ok, _ := mgr.GetString(ctx, "site.admin_url"); ok && v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "http://localhost:8095/_/"
}
