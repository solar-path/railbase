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
// v1.7.43 gate: refuses to run unless either
//   - mailer.configured_at IS SET (mailer successfully configured), OR
//   - mailer.setup_skipped_at IS SET (operator explicitly skipped)
//
// Without either, 412 Precondition Failed with a clear "Configure
// mailer first" message. The wizard front-end blocks the Admin step
// when mailer-status reports neither flag, so a well-behaved client
// never hits this branch — but the server-side check is the
// authoritative guard (defends against direct-API misuse).
//
// On success enqueues TWO email jobs (provided mailer NOT skipped):
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

	// v1.7.43 mailer-gate: BEFORE any work, check that the operator
	// either configured the mailer OR explicitly skipped it. Returning
	// 412 here lets the wizard front-end re-route the operator back
	// to the Mailer step without re-rendering the form.
	if blocker := mailerGateError(r.Context(), d); blocker != nil {
		rerr.WriteJSON(w, blocker)
		return
	}
	// v1.7.47 auth-gate: same shape as the mailer-gate above. The Auth
	// Methods step sits between Database and Mailer in the wizard, and
	// the front-end's "Create admin" submit must NOT reach the DB
	// without the operator either having configured methods OR
	// explicitly skipped them (in which case password-only is the
	// recorded safe default).
	if blocker := authGateError(r.Context(), d); blocker != nil {
		rerr.WriteJSON(w, blocker)
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
	writeAuditOK(r.Context(), d, "admin.bootstrap", admin.ID, admin.Email, "", r)

	// v1.7.43 — enqueue welcome + broadcast notice. Best-effort: a
	// failure to enqueue is LOGGED but does NOT fail admin creation.
	// The retry_failed_welcome_emails cron picks up enqueued-but-
	// dispatched-failed jobs; we just need the rows in `_jobs`.
	enqueueAdminEmails(r.Context(), d, admin, "wizard (POST /_bootstrap)", "bootstrap-self")

	d.writeAdminAuth(w, tok, admin)
}

// mailerGateError returns a typed 412 error when neither
// mailer.configured_at nor mailer.setup_skipped_at is set in settings.
// nil → operator has either configured OR skipped, admin-create allowed.
//
// Why 412 (not 400 or 403): 412 Precondition Failed is the standard
// semantic for "your request is well-formed but a server-side state
// invariant isn't met". The wizard front-end maps this to "go back
// to the Mailer step".
func mailerGateError(ctx context.Context, d *Deps) *rerr.Error {
	if d.Settings == nil {
		// Settings manager missing — likely setup-mode boot path. We
		// can't enforce the gate; bypass it but log a warning so the
		// operator notices the unusual state.
		if d.Log != nil {
			d.Log.Warn("bootstrap mailer-gate: Settings manager nil, bypassing check")
		}
		return nil
	}
	configured, _, _ := d.Settings.GetString(ctx, settingsKeyConfiguredAt)
	skipped, _, _ := d.Settings.GetString(ctx, settingsKeySkippedAt)
	if configured != "" || skipped != "" {
		return nil
	}
	return rerr.New(rerr.CodePreconditionFailed,
		"mailer is not configured yet. Complete the mailer step of the setup wizard, or explicitly skip it. Admin creation requires either a working mailer OR a recorded skip-decision so welcome notifications and compromise-detection broadcasts can fire.")
}

// authGateError mirrors mailerGateError for the v1.7.47 auth-methods
// step. The wizard front-end maps this 412 to "go back to the Auth
// Methods step". Two flags accept the request:
//
//   - auth.configured_at  — operator went through the step + saved
//   - auth.setup_skipped_at — operator explicitly skipped (password
//     remains on as the safe default; see setup_auth.go skip handler)
//
// nil → either flag set, admin-create proceeds.
func authGateError(ctx context.Context, d *Deps) *rerr.Error {
	if d.Settings == nil {
		// Same setup-mode bypass logic as mailerGateError. Without a
		// Settings manager we can't enforce; let it through but log.
		if d.Log != nil {
			d.Log.Warn("bootstrap auth-gate: Settings manager nil, bypassing check")
		}
		return nil
	}
	configured, _, _ := d.Settings.GetString(ctx, settingsKeyAuthConfiguredAt)
	skipped, _, _ := d.Settings.GetString(ctx, settingsKeyAuthSkippedAt)
	if configured != "" || skipped != "" {
		return nil
	}
	return rerr.New(rerr.CodePreconditionFailed,
		"authentication methods are not configured yet. Complete the auth-methods step of the setup wizard, or explicitly skip it. Admin creation requires either a configured method set OR a recorded skip-decision so the install isn't created with zero sign-in paths.")
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
