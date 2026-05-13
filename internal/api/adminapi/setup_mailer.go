package adminapi

// v1.7.43 §3.1 — first-run "Mailer configuration" wizard step. Sits
// between the v1.7.39 Database step and the existing Admin-account
// step. Mailer setup is gated on "mailer is mandatory before any
// admin can be created", with a documented skip path for legitimate
// "configure later" workflows.
//
// THREE public endpoints (no RequireAdmin guard — same justification
// as setup_db.go: no admin exists yet at this point in the wizard):
//
//	GET  /api/_admin/_setup/mailer-status — current configured/skipped
//	                                        state + masked config snapshot
//	POST /api/_admin/_setup/mailer-probe  — try config against an SMTP
//	                                        server (or console driver)
//	POST /api/_admin/_setup/mailer-save   — persist config to _settings
//	POST /api/_admin/_setup/mailer-skip   — record "I'll configure later"
//	                                        with operator reason
//
// Persistence strategy: everything lands in `_settings` via the
// existing Manager surface. That gives us automatic eventbus
// invalidation, audit (settings.changed → audit_settings_changed),
// and replication — same channel `railbase config set` / the admin
// settings screen already use.
//
// Key naming follows the existing v1.0 mailer convention
// (`mailer.smtp.host`, `mailer.from`, …). New v1.7.43 keys:
//   - `mailer.configured_at`     (timestamp string) — successful probe+save marker
//   - `mailer.setup_skipped_at`  (timestamp string) — operator explicitly skipped
//   - `mailer.setup_skipped_reason` (string)        — operator-supplied reason
//
// The bootstrap-admin handler (bootstrap.go) checks BOTH keys: if neither
// is set, admin-create is refused with 412 "Configure mailer first". The
// boot-time invariant in pkg/railbase/app.go does NOT enforce this — the
// gate is operator-facing at admin-create time, not at every boot. This
// avoids "operator deleted skip flag, now can't boot" footguns.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/mailer"
)

// mailerProbeTimeout caps the SMTP handshake+send round-trip. Higher
// than setup_db.go's 5s because SMTP TLS handshake + AUTH + recipient
// validation can legitimately take longer than a Postgres connect.
const mailerProbeTimeout = 15 * time.Second

// settingsKey* are the canonical names for the v1.7.43 status keys.
// Existing v1.0 mailer keys (mailer.driver, mailer.smtp.host, etc.)
// keep their names; these three are NEW.
const (
	settingsKeyConfiguredAt = "mailer.configured_at"
	settingsKeySkippedAt    = "mailer.setup_skipped_at"
	settingsKeySkippedReason = "mailer.setup_skipped_reason"
)

// setupMailerBody is the shape POSTed to probe + save. Mirror of the
// v1.0 mailer config layout so operators who know `mailer.smtp.*`
// keys recognise the field names.
type setupMailerBody struct {
	Driver      string `json:"driver"`        // "smtp" | "console"
	FromAddress string `json:"from_address"`  // From header default
	FromName    string `json:"from_name"`     // optional display name
	SMTPHost    string `json:"smtp_host"`
	SMTPPort    int    `json:"smtp_port"`
	SMTPUser    string `json:"smtp_user"`
	SMTPPass    string `json:"smtp_password"`
	TLS         string `json:"tls"` // "starttls" | "implicit" | "off"
	ProbeTo     string `json:"probe_to"` // recipient for the test email
}

// setupMailerSkipBody is the body for /_setup/mailer-skip. Reason is
// non-empty + bounded so the audit row stays readable.
type setupMailerSkipBody struct {
	Reason string `json:"reason"`
}

// setupMailerStatusResponse is the GET /_setup/mailer-status envelope.
// All timestamps are RFC3339 strings (empty when not set). The masked
// config snapshot helps the wizard pre-fill fields on a re-visit
// without exposing the SMTP password.
type setupMailerStatusResponse struct {
	ConfiguredAt   string `json:"configured_at,omitempty"`
	SkippedAt      string `json:"skipped_at,omitempty"`
	SkippedReason  string `json:"skipped_reason,omitempty"`
	// MailerRequired indicates whether the admin-bootstrap handler will
	// gate on this. true ALWAYS in v1.7.43; reserved for a future opt-out
	// setting if operators decide mandatory-email isn't right for their
	// org. The wizard renders the gate's friendliness based on this.
	MailerRequired bool          `json:"mailer_required"`
	Config         setupMailerSnapshot `json:"config"`
}

// setupMailerSnapshot mirrors setupMailerBody MINUS the password,
// which we never echo back. Empty fields = key absent in _settings.
type setupMailerSnapshot struct {
	Driver      string `json:"driver,omitempty"`
	FromAddress string `json:"from_address,omitempty"`
	FromName    string `json:"from_name,omitempty"`
	SMTPHost    string `json:"smtp_host,omitempty"`
	SMTPPort    int    `json:"smtp_port,omitempty"`
	SMTPUser    string `json:"smtp_user,omitempty"`
	TLS         string `json:"tls,omitempty"`
	// SMTPPasswordSet is true when a password setting exists; the
	// actual value is never echoed. UI uses this to render the
	// password field as "•••• (unchanged)" vs an empty placeholder.
	SMTPPasswordSet bool `json:"smtp_password_set"`
}

// setupMailerProbeResponse is /_setup/mailer-probe's envelope. Same
// "ok + error + hint" shape as setup_db.go so the wizard's banner
// component is reusable across both steps.
type setupMailerProbeResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
	// Driver echoes back the driver used for the probe so the UI can
	// distinguish "SMTP probe failed" from "console probe succeeded
	// (and you should see the email in railbase logs)".
	Driver string `json:"driver,omitempty"`
}

// setupMailerSaveResponse is /_setup/mailer-save's envelope. Mirrors
// the v1.7.39 setup_db save shape (ok + note + restart_required).
// restart_required is ALWAYS false here — settings live in the DB and
// take effect on next mailer.SendTemplate call (~lazy reload).
type setupMailerSaveResponse struct {
	OK   bool   `json:"ok"`
	Note string `json:"note"`
}

// mountSetupMailer wires the four setup-mailer endpoints onto r.
// Called from mountSetupDB's sibling — both belong in the same
// public sub-tree because both run pre-admin.
func (d *Deps) mountSetupMailer(r chi.Router) {
	r.Get("/_setup/mailer-status", d.setupMailerStatusHandler)
	r.Post("/_setup/mailer-probe", d.setupMailerProbeHandler)
	r.Post("/_setup/mailer-save", d.setupMailerSaveHandler)
	r.Post("/_setup/mailer-skip", d.setupMailerSkipHandler)
}

// setupMailerStatusHandler — GET /_setup/mailer-status.
//
// Reports whether the operator has either configured OR explicitly
// skipped the mailer setup. The wizard reads this to decide whether
// it can advance to the Admin step (configured OR skipped → yes;
// neither → no).
//
// Also returns a masked snapshot of the current config so the wizard
// can pre-populate fields on a re-visit without ever transmitting the
// password back to the client.
func (d *Deps) setupMailerStatusHandler(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		// Settings manager NOT wired (setup-mode boot path). Report
		// fully-clean state so the wizard renders the form. The save
		// endpoint will fail with the same nil-guard.
		writeJSON(w, http.StatusOK, setupMailerStatusResponse{
			MailerRequired: true,
		})
		return
	}

	resp := setupMailerStatusResponse{MailerRequired: true}
	if v, ok, _ := d.Settings.GetString(r.Context(), settingsKeyConfiguredAt); ok {
		resp.ConfiguredAt = v
	}
	if v, ok, _ := d.Settings.GetString(r.Context(), settingsKeySkippedAt); ok {
		resp.SkippedAt = v
	}
	if v, ok, _ := d.Settings.GetString(r.Context(), settingsKeySkippedReason); ok {
		resp.SkippedReason = v
	}

	// Masked snapshot.
	snap := setupMailerSnapshot{}
	if v, ok, _ := d.Settings.GetString(r.Context(), "mailer.driver"); ok {
		snap.Driver = v
	}
	if v, ok, _ := d.Settings.GetString(r.Context(), "mailer.from"); ok {
		snap.FromAddress = v
	}
	if v, ok, _ := d.Settings.GetString(r.Context(), "mailer.from_name"); ok {
		snap.FromName = v
	}
	if v, ok, _ := d.Settings.GetString(r.Context(), "mailer.smtp.host"); ok {
		snap.SMTPHost = v
	}
	if n, ok, _ := d.Settings.GetInt(r.Context(), "mailer.smtp.port"); ok {
		snap.SMTPPort = int(n)
	}
	if v, ok, _ := d.Settings.GetString(r.Context(), "mailer.smtp.username"); ok {
		snap.SMTPUser = v
	}
	if v, ok, _ := d.Settings.GetString(r.Context(), "mailer.smtp.tls"); ok {
		snap.TLS = v
	}
	if _, ok, _ := d.Settings.GetString(r.Context(), "mailer.smtp.password"); ok {
		snap.SMTPPasswordSet = true
	}
	resp.Config = snap

	writeJSON(w, http.StatusOK, resp)
}

// setupMailerProbeHandler — POST /_setup/mailer-probe.
//
// Builds an ephemeral mailer.Driver from the body, attempts to send a
// short test email to body.ProbeTo, returns the result. NEVER writes
// to settings — operator must explicitly POST mailer-save after a
// successful probe (mirrors setup_db.go's probe / save separation).
//
// For driver=console we just verify the driver constructs cleanly +
// "send" the email to stdout. The test address is included in the
// console output so the operator can tell their probe was processed.
//
// Returns 200 with ok-flagged envelope on success OR transport failure.
// 400 only for body-validation errors (malformed JSON, missing fields).
func (d *Deps) setupMailerProbeHandler(w http.ResponseWriter, r *http.Request) {
	var body setupMailerBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if err := validateSetupMailerBody(body, true); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), mailerProbeTimeout)
	defer cancel()

	drv, drvErr := buildProbeDriver(body)
	if drvErr != nil {
		writeJSON(w, http.StatusOK, setupMailerProbeResponse{
			OK:     false,
			Driver: body.Driver,
			Error:  drvErr.Error(),
			Hint:   mailerProbeHint(drvErr.Error()),
		})
		return
	}

	msg := buildProbeMessage(body)
	if err := drv.Send(ctx, msg); err != nil {
		writeJSON(w, http.StatusOK, setupMailerProbeResponse{
			OK:     false,
			Driver: body.Driver,
			Error:  err.Error(),
			Hint:   mailerProbeHint(err.Error()),
		})
		return
	}

	writeJSON(w, http.StatusOK, setupMailerProbeResponse{
		OK:     true,
		Driver: body.Driver,
	})
}

// setupMailerSaveHandler — POST /_setup/mailer-save.
//
// Persists the body's config keys into `_settings` AND stamps
// mailer.configured_at. The wizard MUST have probed successfully
// first — saving with no probe is allowed (operator can save and
// fix later), but the operator-facing UX advises probe-first.
//
// On save success we ALSO clear any previously-set
// mailer.setup_skipped_at: configuring the mailer overrides a prior
// skip decision.
func (d *Deps) setupMailerSaveHandler(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal,
			"settings manager not wired (setup-mode boot path?)"))
		return
	}
	var body setupMailerBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if err := validateSetupMailerBody(body, false); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	if err := saveMailerSettings(r.Context(), d, body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "save mailer settings"))
		return
	}

	writeJSON(w, http.StatusOK, setupMailerSaveResponse{
		OK: true,
		Note: "Mailer configuration saved. Welcome emails will use this driver starting with the next admin creation.",
	})
}

// setupMailerSkipHandler — POST /_setup/mailer-skip.
//
// Records "operator chose to skip mailer setup". Stamps both
// mailer.setup_skipped_at (timestamp) and mailer.setup_skipped_reason
// (free-text from body). The bootstrap-admin handler will allow
// admin-create with neither welcome email NOR broadcast notice when
// this flag is set.
//
// Requires a non-empty reason — operator-action that weakens the
// safety invariant should leave a forensic trail. The reason ends up
// in the audit log via settings.changed → audit subscriber chain.
func (d *Deps) setupMailerSkipHandler(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal,
			"settings manager not wired (setup-mode boot path?)"))
		return
	}
	var body setupMailerSkipBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"reason is required so operator-skip leaves an audit trail"))
		return
	}
	if len(body.Reason) > 500 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"reason must be 500 characters or less"))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := d.Settings.Set(r.Context(), settingsKeySkippedAt, now); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "record skip"))
		return
	}
	if err := d.Settings.Set(r.Context(), settingsKeySkippedReason, body.Reason); err != nil {
		// Best-effort: skip timestamp already wrote; we don't roll back.
		// Log and continue — the timestamp alone is enough for the gate.
		if d.Log != nil {
			d.Log.Warn("setup mailer skip: reason write failed", "err", err)
		}
	}

	writeJSON(w, http.StatusOK, setupMailerSaveResponse{
		OK:   true,
		Note: "Mailer setup skipped. Admin creation will not send welcome emails until you configure the mailer.",
	})
}

// validateSetupMailerBody enforces per-driver required-field rules.
// requireProbeTo=true forces ProbeTo non-empty (probe path); false
// allows omission (save path — operator already probed earlier).
func validateSetupMailerBody(body setupMailerBody, requireProbeTo bool) error {
	switch body.Driver {
	case "smtp":
		if body.SMTPHost == "" {
			return fmt.Errorf("smtp_host is required for driver=smtp")
		}
		if body.SMTPPort <= 0 || body.SMTPPort > 65535 {
			return fmt.Errorf("smtp_port must be 1..65535 (got %d)", body.SMTPPort)
		}
		if body.TLS != "" && body.TLS != "starttls" && body.TLS != "implicit" && body.TLS != "off" {
			return fmt.Errorf("tls must be one of: starttls, implicit, off")
		}
	case "console":
		// No required fields beyond from_address.
	case "":
		return fmt.Errorf("driver is required (smtp | console)")
	default:
		return fmt.Errorf("driver must be one of: smtp, console")
	}
	if body.FromAddress == "" {
		return fmt.Errorf("from_address is required")
	}
	if requireProbeTo && strings.TrimSpace(body.ProbeTo) == "" {
		return fmt.Errorf("probe_to is required (we'll send a test email here)")
	}
	return nil
}

// buildProbeDriver constructs a Driver for the probe. console uses an
// io.Discard writer so probe output doesn't pollute stdout (the probe
// is testing the wiring, not generating real operator-visible logs).
func buildProbeDriver(body setupMailerBody) (mailer.Driver, error) {
	switch body.Driver {
	case "smtp":
		return mailer.NewSMTPDriver(mailer.SMTPConfig{
			Host:     body.SMTPHost,
			Port:     body.SMTPPort,
			Username: body.SMTPUser,
			Password: body.SMTPPass,
			TLS:      body.TLS,
		}), nil
	case "console":
		// Discard the body — we just want to verify the driver
		// constructs and "delivers" without erroring. Send still
		// validates the message shape via the engine's Send path.
		return mailer.NewConsoleDriver(discardWriter{}), nil
	default:
		return nil, fmt.Errorf("unknown driver %q", body.Driver)
	}
}

// buildProbeMessage assembles the test email. Short, identifies itself
// as a probe so operators receiving it understand "this is the wizard
// pinging me, not a real notification".
func buildProbeMessage(body setupMailerBody) mailer.Message {
	from := mailer.Address{Email: body.FromAddress, Name: body.FromName}
	to := mailer.Address{Email: body.ProbeTo}
	return mailer.Message{
		From:    from,
		To:      []mailer.Address{to},
		Subject: "[Railbase] Mailer configuration test",
		Text:    "This is a test email from the Railbase setup wizard. If you can read this, your mailer is configured correctly. You can ignore this message.",
		HTML:    "<p>This is a test email from the Railbase setup wizard. If you can read this, your mailer is configured correctly. You can ignore this message.</p>",
	}
}

// saveMailerSettings writes every field from body into `_settings`.
// Done as separate Set calls because the v1.0 mailer wiring reads
// them as discrete keys (see pkg/railbase/mailer_wiring.go::buildMailer).
// Each Set fires its own settings.changed event — admin UI / audit /
// caches all react accordingly.
//
// Stamps mailer.configured_at AND clears mailer.setup_skipped_at:
// successful configuration overrides a prior skip.
func saveMailerSettings(ctx context.Context, d *Deps, body setupMailerBody) error {
	if err := d.Settings.Set(ctx, "mailer.driver", body.Driver); err != nil {
		return err
	}
	if err := d.Settings.Set(ctx, "mailer.from", body.FromAddress); err != nil {
		return err
	}
	if body.FromName != "" {
		if err := d.Settings.Set(ctx, "mailer.from_name", body.FromName); err != nil {
			return err
		}
	}
	if body.Driver == "smtp" {
		if err := d.Settings.Set(ctx, "mailer.smtp.host", body.SMTPHost); err != nil {
			return err
		}
		if err := d.Settings.Set(ctx, "mailer.smtp.port", body.SMTPPort); err != nil {
			return err
		}
		if body.SMTPUser != "" {
			if err := d.Settings.Set(ctx, "mailer.smtp.username", body.SMTPUser); err != nil {
				return err
			}
		}
		if body.SMTPPass != "" {
			if err := d.Settings.Set(ctx, "mailer.smtp.password", body.SMTPPass); err != nil {
				return err
			}
		}
		tls := body.TLS
		if tls == "" {
			tls = "starttls"
		}
		if err := d.Settings.Set(ctx, "mailer.smtp.tls", tls); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := d.Settings.Set(ctx, settingsKeyConfiguredAt, now); err != nil {
		return err
	}
	// Successful configure clears any earlier skip decision.
	_ = d.Settings.Delete(ctx, settingsKeySkippedAt)
	_ = d.Settings.Delete(ctx, settingsKeySkippedReason)
	return nil
}

// mailerProbeHint maps probe-error text to a one-sentence
// operator-actionable hint. Same pattern as setup_db.go's
// setupProbeHint — substring matching against the underlying SMTP
// error since net/smtp's error surface isn't typed stably.
func mailerProbeHint(errMsg string) string {
	low := strings.ToLower(errMsg)
	switch {
	case strings.Contains(low, "no such host"), strings.Contains(low, "name resolution"):
		return "SMTP host couldn't be resolved. Check the hostname (no protocol prefix; e.g. 'smtp.gmail.com' not 'smtp://smtp.gmail.com')."
	case strings.Contains(low, "connection refused"):
		return "Nothing is listening on that host:port. Verify the SMTP port (commonly 587 for STARTTLS, 465 for implicit TLS, 25 for unencrypted)."
	case strings.Contains(low, "authentication"), strings.Contains(low, "auth"), strings.Contains(low, "535"):
		return "SMTP authentication failed. For Gmail-style providers, use an app-specific password rather than your account password."
	case strings.Contains(low, "tls"), strings.Contains(low, "x509"), strings.Contains(low, "certificate"):
		return "TLS handshake failed. Try tls=implicit (port 465) or tls=starttls (port 587); verify the server certificate isn't expired/self-signed."
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline exceeded"), strings.Contains(low, "i/o timeout"):
		return "Connection timed out. Check firewall rules — many networks block outbound port 25; 587 is more reliable."
	case strings.Contains(low, "5") && strings.Contains(low, "relay"):
		return "Server refused to relay your message. Verify from_address belongs to a domain the SMTP server is authorised to send for."
	case strings.Contains(low, "from"), strings.Contains(low, "sender"):
		return "Server rejected the From address. Use an address on a domain you control (and have configured SPF/DKIM for)."
	}
	return "See the error message above. Common fixes: verify host/port/credentials and try a different TLS mode."
}

// discardWriter is io.Discard with a name we own — avoids importing
// io for one constant.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// ensure json import used for body decoding stays referenced even if
// future refactors split the file.
var _ = json.Unmarshal
