package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/railbase/railbase/internal/admins"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/rbac"
)

// bootstrapProbeHandler reports whether the system has zero admins.
// Open endpoint (no auth) so the admin UI's first paint can decide
// whether to render the login screen or the bootstrap wizard.
//
// Returning {needsBootstrap: bool} keeps the response small and lets
// future versions add fields without breaking older clients.
func (d *Deps) bootstrapProbeHandler(w http.ResponseWriter, r *http.Request) {
	count, err := d.Admins.Count(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "admin count"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"needsBootstrap": count == 0,
		"adminCount":     count,
	})
}

// bootstrapCreateHandler creates the first admin AND signs them in.
// Refuses to run when any admin already exists — once the system is
// bootstrapped, further admins must be created via authenticated
// CLI or admin API endpoints (the latter not in v0.8 scope).
//
// v0.9 — auth-methods and mailer configuration moved out of the
// first-run wizard into Settings. The two server-side preconditions
// that previously enforced "wizard step touched" (mailerGateError +
// authGateError) were removed: admin creation now depends only on the
// admin count being zero. Mailer / auth surfaces are still managed via
// `_settings` keys (read/written through the authenticated KV
// endpoint), but they are no longer admission gates for bootstrap.
//
// On success enqueues TWO email jobs (provided mailer NOT skipped via
// `mailer.setup_skipped_at`):
//   - admin_welcome to the new admin (login URL + onboarding)
//   - admin_created_notice broadcast to every EXISTING admin (compromise
//     detection). On bootstrap (first admin) the broadcast set is empty
//     so only the welcome lands.
//
// Race window: between the count check and the insert, two parallel
// requests could both pass the check. The unique(lower(email)) index
// catches the second one, but they could end up with two distinct
// admins. We accept this as a v0.8 limitation; v0.9 will gate on a
// row-level lock.
func (d *Deps) bootstrapCreateHandler(w http.ResponseWriter, r *http.Request) {
	count, err := d.Admins.Count(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "admin count"))
		return
	}
	if count > 0 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"bootstrap refused: %d admin(s) already exist; use `railbase admin create` instead", count))
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if body.Email == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "email is required"))
		return
	}
	if len(body.Password) < 8 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "password must be at least 8 chars"))
		return
	}

	admin, err := d.Admins.Create(r.Context(), body.Email, body.Password)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeConflict, "an admin with that email already exists"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create admin"))
		return
	}

	tok, _, err := d.Sessions.Create(r.Context(), admins.CreateSessionInput{
		AdminID:   admin.ID,
		IP:        clientIP(r),
		UserAgent: r.Header.Get("User-Agent"),
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "session create"))
		return
	}
	// v1.x — assign the new admin the site `system_admin` role so the
	// rbac-gated handlers (PATCH /settings, etc.) recognise them as
	// full-access. Best-effort: a failure here is LOGGED but does NOT
	// fail bootstrap — the operator can re-assign via the role UI if
	// the row goes missing. Without it, a fresh deployment would see
	// the bootstrap admin denied at PATCH /settings, which would be a
	// strictly worse UX than the pre-v1.x "any admin can do anything"
	// behaviour.
	if d.RBAC != nil {
		if err := rbac.AssignSystemAdmin(r.Context(), d.RBAC, admin.ID); err != nil {
			d.Log.Warn("bootstrap: failed to assign system_admin to new admin",
				"admin_id", admin.ID, "err", err)
		}
	}
	writeAuditOK(r.Context(), d, "admin.bootstrap", admin.ID, admin.Email, "", r)

	// v1.7.43 — enqueue welcome + broadcast notice. Best-effort: a
	// failure to enqueue is LOGGED but does NOT fail admin creation.
	// The retry_failed_welcome_emails cron picks up enqueued-but-
	// dispatched-failed jobs; we just need the rows in `_jobs`.
	enqueueAdminEmails(r.Context(), d, admin, "wizard (POST /_bootstrap)", "bootstrap-self")

	d.writeAdminAuth(w, tok, admin)
}

// adminCreationViaCLI is the audit/payload "via" tag for CLI-side
// admin creates. Re-used by pkg/railbase/cli/admin.go via
// EnqueueAdminEmails so the welcome email lists the right channel.
const adminCreationViaCLI = "CLI (`railbase admin create`)"

// EnqueueAdminEmails is the exported entry point for admin-welcome +
// broadcast-notice enqueueing. CLI admin-create + bootstrap handler
// both call this so the welcome content stays consistent.
//
// Skipping behaviour:
//   - mailer.setup_skipped_at IS SET → no emails enqueued (operator
//     opted out + the gate let admin-create through on the skip flag);
//   - settings absent OR mailer.configured_at set → emails enqueued.
//
// `via` is a free-form string describing how the admin was created
// (e.g. "wizard (POST /_bootstrap)", CLI tag, "API"). Ends up in the
// audit row payload AND in the email body via {{ event.via }}.
//
// `createdBy` is the actor identifier — admin email for authenticated
// CLI/API paths, "bootstrap-self" for the first admin who created
// themselves. Ends up in {{ event.created_by }}.
func EnqueueAdminEmails(ctx context.Context, d *Deps, newAdmin *admins.Admin, via, createdBy string) {
	enqueueAdminEmails(ctx, d, newAdmin, via, createdBy)
}

// enqueueAdminEmails — internal twin of EnqueueAdminEmails. Same
// signature, kept un-exported so handler call-sites stay free of the
// "Deps" indirection symbols. The Exported wrapper exists for CLI
// callers that live in a different package.
func enqueueAdminEmails(ctx context.Context, d *Deps, newAdmin *admins.Admin, via, createdBy string) {
	if d.Pool == nil {
		// Nothing to do — Pool absent means this is a test or setup-mode
		// fast path where the jobs table doesn't exist.
		return
	}
	// Honour an explicit skip — operator opted out, don't enqueue.
	if d.Settings != nil {
		skipped, _, _ := d.Settings.GetString(ctx, settingsKeySkippedAt)
		if skipped != "" {
			if d.Log != nil {
				d.Log.Info("admin email: mailer skipped, no welcome enqueued",
					"admin_id", newAdmin.ID, "admin_email", newAdmin.Email)
			}
			return
		}
	}

	siteName := readAdminSiteName(ctx, d)
	adminURL := readAdminURL(ctx, d)
	now := time.Now().UTC().Format(time.RFC3339)

	store := jobs.NewStore(d.Pool)

	// (a) Welcome to the NEW admin. Always enqueued (unless mailer skipped).
	welcomeData := map[string]any{
		"admin": map[string]any{
			"email": newAdmin.Email,
			"id":    newAdmin.ID.String(),
		},
		"event": map[string]any{
			"at":         now,
			"created_by": createdBy,
			"via":        via,
		},
		"site": map[string]any{
			"name": siteName,
			"from": readSiteFrom(ctx, d),
		},
		"admin_url": adminURL,
	}
	welcomePayload := map[string]any{
		"template": "admin_welcome",
		"to": []map[string]any{
			{"email": newAdmin.Email},
		},
		"data": welcomeData,
	}
	if _, err := store.Enqueue(ctx, "send_email_async", welcomePayload, jobs.EnqueueOptions{
		Queue: "default",
		// 24 attempts × exp-backoff capped at 1h means up to ~24h of
		// covered downtime. Longer than the standard 5 because welcome
		// emails are once-per-admin AND the cron sweeper picks up the
		// rest from `status='failed'` after that.
		MaxAttempts: 24,
	}); err != nil {
		if d.Log != nil {
			d.Log.Warn("admin email: welcome enqueue failed",
				"admin_id", newAdmin.ID, "admin_email", newAdmin.Email, "err", err)
		}
	}

	// (b) Broadcast notice to every OTHER existing admin. On bootstrap
	// the list is empty (we just created the first admin) so this is a
	// no-op; on subsequent creates this fan-outs compromise-detection
	// notices to the rest of the admin team.
	otherAdmins, err := d.Admins.List(ctx)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("admin email: list-other-admins failed, broadcast skipped",
				"err", err)
		}
		return
	}
	for _, other := range otherAdmins {
		if other.ID == newAdmin.ID {
			continue
		}
		noticeData := map[string]any{
			"recipient": map[string]any{
				"email": other.Email,
			},
			"new_admin": map[string]any{
				"email": newAdmin.Email,
				"id":    newAdmin.ID.String(),
			},
			"event": map[string]any{
				"at":         now,
				"created_by": createdBy,
				"via":        via,
			},
			"site": map[string]any{
				"name": siteName,
				"from": readSiteFrom(ctx, d),
			},
			"admin_url": adminURL,
		}
		noticePayload := map[string]any{
			"template": "admin_created_notice",
			"to": []map[string]any{
				{"email": other.Email},
			},
			"data": noticeData,
		}
		if _, err := store.Enqueue(ctx, "send_email_async", noticePayload, jobs.EnqueueOptions{
			Queue:       "default",
			MaxAttempts: 24,
		}); err != nil {
			if d.Log != nil {
				d.Log.Warn("admin email: broadcast notice enqueue failed",
					"recipient", other.Email, "new_admin", newAdmin.Email, "err", err)
			}
		}
	}
}

// readAdminSiteName picks up the operator's branded name for the
// instance. Falls back to "Railbase" if the operator hasn't set it.
func readAdminSiteName(ctx context.Context, d *Deps) string {
	if d.Settings != nil {
		if v, ok, _ := d.Settings.GetString(ctx, "site.name"); ok && v != "" {
			return v
		}
	}
	return "Railbase"
}

// readSiteFrom resolves the From address for emails. Falls back to the
// configured mailer.from setting.
func readSiteFrom(ctx context.Context, d *Deps) string {
	if d.Settings != nil {
		if v, ok, _ := d.Settings.GetString(ctx, "mailer.from"); ok && v != "" {
			return v
		}
	}
	return ""
}

// readAdminURL resolves the admin UI URL. Operator-configurable via
// settings; fallback is the RAILBASE_ADMIN_URL env var, then a
// reasonable default for local dev. Used in welcome templates so the
// recipient can click through to login.
func readAdminURL(ctx context.Context, d *Deps) string {
	if d.Settings != nil {
		if v, ok, _ := d.Settings.GetString(ctx, "site.admin_url"); ok && v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	if v := os.Getenv("RAILBASE_ADMIN_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:8095/_/"
}
