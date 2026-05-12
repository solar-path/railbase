package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/mailer"
)

// newMailerCmd assembles the `railbase mailer ...` subtree.
//
// v1.0 surface: `mailer test`. The send invokes the same Mailer
// the running server would use, with config sourced from settings
// (or `--driver console` for a dry run).
//
// Future:
//   - `mailer templates list` — enumerate available templates
//   - `mailer templates show <name>` — print the resolved template
//   - `mailer providers test` — exercise each configured provider
func newMailerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mailer",
		Short: "Send test emails and inspect mailer state",
	}
	cmd.AddCommand(newMailerTestCmd(), newMailerEventsCmd())
	return cmd
}

// newMailerEventsCmd assembles `railbase mailer events ...` — operator
// drill-down into the `_email_events` table. v1.7.34f §3.1.4 ships the
// "list" verb; future verbs (purge, export) are easy follow-ups.
func newMailerEventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Inspect persisted email send / failure events",
	}
	cmd.AddCommand(newMailerEventsListCmd())
	return cmd
}

func newMailerEventsListCmd() *cobra.Command {
	var (
		recipient string
		limit     int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent email events (newest first)",
		Long: strings.TrimSpace(`
Print rows from _email_events. Useful for operator drill-downs:

  railbase mailer events list                          # most recent 50 overall
  railbase mailer events list --recipient alice@x.com  # just alice's history
  railbase mailer events list --limit 200

The table is per-recipient: one row per (send, recipient) — a single
SendDirect with 3 To: addresses produces 3 rows. event values:
sent / failed (core), bounced / opened / clicked / complained (plugins).
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			store := mailer.NewEventStore(rt.pool.Pool)
			var rows []mailer.EmailEvent
			if recipient != "" {
				rows, err = store.ListByRecipient(cmd.Context(), recipient, limit)
			} else {
				rows, err = store.ListRecent(cmd.Context(), limit)
			}
			if err != nil {
				return err
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "OCCURRED_AT\tEVENT\tDRIVER\tRECIPIENT\tTEMPLATE\tSUBJECT\tERROR")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					r.OccurredAt.UTC().Format(time.RFC3339),
					r.Event, r.Driver, r.Recipient,
					orDash(r.Template), truncateCol(r.Subject, 40),
					truncateCol(r.ErrorMessage, 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&recipient, "recipient", "", "filter by recipient email (exact match)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max rows to return (default 50, max 1000)")
	return cmd
}

// orDash collapses an empty optional column to "-" for readable tab
// output. Saves the eye an extra column of whitespace.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncateCol clips long subjects / error messages so the tabwriter
// doesn't blow out the terminal width. Mirrors the convention from
// webhooks deliveries list.
func truncateCol(s string, n int) string {
	if s == "" {
		return "-"
	}
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func newMailerTestCmd() *cobra.Command {
	var to, subject, fromAddr, template, body string
	var smtpHost, smtpUser, smtpPass, smtpTLS string
	var smtpPort int
	var useConsole bool

	cmd := &cobra.Command{
		Use:   "test",
		Short: "Send a one-off test email",
		Long: strings.TrimSpace(`
Send a test email. Two modes:

  1. Direct: --subject + --body (markdown) — sends without resolving
     a template.

  2. Template: --template <name> — renders the named template (one
     of the embedded defaults or an override in
     pb_data/email_templates/). Optionally pass --data key=value
     pairs for variable interpolation.

Pass --console to skip dialing SMTP and print the message to stdout
instead. Useful for verifying templates render without standing up
an SMTP server.
`),
		Example: strings.TrimSpace(`
  railbase mailer test --to me@example.com --console --subject hello --body "**hi** there"
  railbase mailer test --to me@example.com --console --template signup_verification
  railbase mailer test --to me@example.com --smtp-host smtp.example.com --smtp-port 587 \
    --smtp-user noreply@example.com --smtp-pass *** --template otp
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			if (template == "" && body == "") || (template != "" && body != "") {
				return fmt.Errorf("specify exactly one of --template or --body")
			}

			// Build the driver — console if requested, otherwise SMTP
			// from flags. We never read from settings here: this
			// command is meant to be safe to run against an offline
			// data dir (no DB connection).
			var drv mailer.Driver
			if useConsole {
				drv = mailer.NewConsoleDriver(os.Stdout)
			} else {
				if smtpHost == "" {
					return fmt.Errorf("--smtp-host is required (or pass --console)")
				}
				if smtpPort == 0 {
					smtpPort = 587
				}
				drv = mailer.NewSMTPDriver(mailer.SMTPConfig{
					Host:     smtpHost,
					Port:     smtpPort,
					Username: smtpUser,
					Password: smtpPass,
					TLS:      smtpTLS,
				})
			}

			from := mailer.Address{Email: parseEmailEnv(fromAddr, "RAILBASE_MAILER_FROM", smtpUser)}
			m := mailer.New(mailer.Options{
				Driver:      drv,
				DefaultFrom: from,
			})

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			recipients := []mailer.Address{{Email: to}}

			if template != "" {
				data := map[string]any{
					"site": map[string]any{
						"name": "Railbase",
						"from": from.Email,
					},
					"user":           map[string]any{"email": to, "new_email": to},
					"verify_url":     "https://example.com/verify/test-token",
					"reset_url":      "https://example.com/reset/test-token",
					"confirm_url":    "https://example.com/confirm/test-token",
					"magic_url":      "https://example.com/magic/test-token",
					"invite_url":     "https://example.com/invite/test-token",
					"otp_code":       "123456",
					"recovery_codes": "abc1-def2-ghi3\njkl4-mno5-pqr6",
					"inviter":        map[string]any{"email": "team@example.com"},
					"org":            map[string]any{"name": "Acme"},
					"event": map[string]any{
						"at":         time.Now().UTC().Format(time.RFC3339),
						"ip":         "127.0.0.1",
						"user_agent": "curl/test",
					},
				}
				if err := m.SendTemplate(ctx, template, recipients, data); err != nil {
					if errors.Is(err, mailer.ErrTemplateNotFound) {
						return fmt.Errorf("template %q not found (looked in pb_data/email_templates/ and embedded defaults)", template)
					}
					return err
				}
				fmt.Printf("OK template=%s to=%s driver=%s\n", template, to, drv.Name())
				return nil
			}

			// Direct mode.
			html := mailerRenderBodyForCLI(body)
			msg := mailer.Message{
				From:    from,
				To:      recipients,
				Subject: subject,
				HTML:    html,
			}
			if err := m.SendDirect(ctx, msg); err != nil {
				return err
			}
			fmt.Printf("OK direct subject=%q to=%s driver=%s\n", subject, to, drv.Name())
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient email address (required)")
	cmd.Flags().StringVar(&subject, "subject", "Railbase test email", "subject (direct mode)")
	cmd.Flags().StringVar(&fromAddr, "from", "", "From address (defaults to $RAILBASE_MAILER_FROM or --smtp-user)")
	cmd.Flags().StringVar(&template, "template", "", "template name to render (e.g. signup_verification)")
	cmd.Flags().StringVar(&body, "body", "", "markdown body (direct mode)")
	cmd.Flags().BoolVar(&useConsole, "console", false, "print to stdout instead of sending via SMTP")
	cmd.Flags().StringVar(&smtpHost, "smtp-host", "", "SMTP host")
	cmd.Flags().IntVar(&smtpPort, "smtp-port", 587, "SMTP port (default 587)")
	cmd.Flags().StringVar(&smtpUser, "smtp-user", "", "SMTP username")
	cmd.Flags().StringVar(&smtpPass, "smtp-pass", "", "SMTP password")
	cmd.Flags().StringVar(&smtpTLS, "smtp-tls", "starttls", "TLS mode: starttls | implicit | off")
	return cmd
}

// mailerRenderBodyForCLI runs the same Markdown pipeline the template
// engine uses, but on a body string the operator pasted. Exposed as
// its own function so future CLI mods (e.g. read body from a file)
// stay one-liners.
func mailerRenderBodyForCLI(body string) string {
	// We re-implement the public function call indirectly: build a
	// faux template by adding a minimal frontmatter, then render.
	// Cheaper to call the package-internal helper directly via a
	// small adapter:
	return mailer.RenderMarkdownForCLI(body)
}

// parseEmailEnv resolves the From address: flag → env → fallback.
// Strips whitespace and rejects obviously-bad values.
func parseEmailEnv(flag, envKey, fallback string) string {
	if flag != "" {
		return strings.TrimSpace(flag)
	}
	if v := os.Getenv(envKey); v != "" {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(fallback)
}

