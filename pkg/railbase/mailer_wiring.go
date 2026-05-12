package railbase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/mailer"
	"github.com/railbase/railbase/internal/settings"
)

// mailerSendAdapter satisfies jobs.MailerSender by translating the
// jobs package's local MailerAddress shape to mailer.Address. v1.7.30
// added send_email_async; this adapter keeps the dependency arrow
// clean (jobs doesn't import mailer, mailer doesn't import jobs).
//
// v1.7.32 — the adapter also lives at the boundary between the two
// permanent-error sentinels (mailer.ErrPermanent vs jobs.ErrPermanent).
// When the mailer flags a send as permanently doomed (e.g. invalid
// recipient address, fixed-credential auth failure), the adapter
// promotes that to jobs.ErrPermanent so the queue's retry engine
// terminates immediately instead of looping the doomed payload
// through MaxAttempts backoffs.
type mailerSendAdapter struct{ m *mailer.Mailer }

func (a mailerSendAdapter) SendTemplate(ctx context.Context, template string, to []jobs.MailerAddress, data map[string]any) error {
	addrs := make([]mailer.Address, len(to))
	for i, r := range to {
		addrs[i] = mailer.Address{Email: r.Email, Name: r.Name}
	}
	err := a.m.SendTemplate(ctx, template, addrs, data)
	if err == nil {
		return nil
	}
	// Cross-package permanent-error chain. errors.Is walks the wrapped
	// chain so this catches mailer.ErrPermanent regardless of how many
	// fmt.Errorf("%w") layers the mailer wraps around it.
	if errors.Is(err, mailer.ErrPermanent) {
		return fmt.Errorf("%w (%w)", err, jobs.ErrPermanent)
	}
	return err
}

// buildMailer reads the mailer config from settings (with env-var
// fallbacks for the SMTP secrets, since admins typically don't want
// SMTP passwords sitting in `_settings` JSONB).
//
// Resolution order per field:
//  1. `mailer.<field>` setting (admin UI / CLI / API)
//  2. corresponding env var (RAILBASE_MAILER_*)
//  3. hard-coded default (console driver, no rate limit)
//
// Returning a *Mailer instead of an interface keeps the type
// inspectable from app.go for future wiring (audit hook, hook
// dispatcher).
func buildMailer(ctx context.Context, mgr *settings.Manager, bus *eventbus.Bus, pool *pgxpool.Pool, log *slog.Logger, templatesDir string) *mailer.Mailer {
	driver := readSetting(ctx, mgr, "mailer.driver", "RAILBASE_MAILER_DRIVER", "console")
	defaultFrom := mailer.Address{
		Email: readSetting(ctx, mgr, "mailer.from", "RAILBASE_MAILER_FROM", ""),
		Name:  readSetting(ctx, mgr, "mailer.from_name", "RAILBASE_MAILER_FROM_NAME", ""),
	}

	var drv mailer.Driver
	switch driver {
	case "smtp":
		drv = mailer.NewSMTPDriver(mailer.SMTPConfig{
			Host:     readSetting(ctx, mgr, "mailer.smtp.host", "RAILBASE_MAILER_SMTP_HOST", ""),
			Port:     readIntSetting(ctx, mgr, "mailer.smtp.port", "RAILBASE_MAILER_SMTP_PORT", 587),
			Username: readSetting(ctx, mgr, "mailer.smtp.username", "RAILBASE_MAILER_SMTP_USER", ""),
			Password: readSetting(ctx, mgr, "mailer.smtp.password", "RAILBASE_MAILER_SMTP_PASS", ""),
			TLS:      readSetting(ctx, mgr, "mailer.smtp.tls", "RAILBASE_MAILER_SMTP_TLS", "starttls"),
		})
		log.Info("mailer: SMTP driver configured",
			"host", readSetting(ctx, mgr, "mailer.smtp.host", "RAILBASE_MAILER_SMTP_HOST", ""))
	case "console":
		drv = mailer.NewConsoleDriver(os.Stdout)
		log.Info("mailer: console driver (dev mode — emails printed to stdout)")
	default:
		log.Warn("mailer: unknown driver, falling back to console", "driver", driver)
		drv = mailer.NewConsoleDriver(os.Stdout)
	}

	tpl := mailer.NewTemplates(mailer.TemplatesOptions{DiskDir: templatesDir})

	// Per-recipient rate limit defaults: 5/hour, global 100/min.
	// Operator can override via mailer.rate_limit.*  settings keys.
	limiter := mailer.NewLimiter(mailer.LimiterConfig{
		GlobalPerMinute:  readIntSetting(ctx, mgr, "mailer.rate_limit.global_per_minute", "RAILBASE_MAILER_RL_GLOBAL", 100),
		PerRecipientHour: readIntSetting(ctx, mgr, "mailer.rate_limit.per_recipient_hour", "RAILBASE_MAILER_RL_RECIPIENT", 5),
	})

	// v1.7.34f §3.1.4 — persist per-recipient send outcomes into
	// `_email_events`. Lazy-construct: when the pool isn't available
	// (shouldn't happen in normal boot, but defensive) we fall back to
	// nil and the mailer keeps its v1.0 in-memory-only behaviour.
	var eventStore *mailer.EventStore
	if pool != nil {
		eventStore = mailer.NewEventStore(pool)
	}

	return mailer.New(mailer.Options{
		Driver:      drv,
		Templates:   tpl,
		Limiter:     limiter,
		Log:         log,
		DefaultFrom: defaultFrom,
		// v1.7.x §3.1.6 — thread the process event bus so subscribers
		// can hook mailer.before_send / mailer.after_send. nil bus
		// (tests, embedded callers) keeps the dispatcher dormant.
		Bus: bus,
		// v1.7.34f §3.1.4 — persist sent/failed events to `_email_events`.
		EventStore: eventStore,
	})
}

// readSetting picks up a string config value with the documented
// fallback order. Missing keys return defaultVal (no error logged —
// missing is fine, that's why fallbacks exist).
func readSetting(ctx context.Context, mgr *settings.Manager, key, envKey, defaultVal string) string {
	if mgr != nil {
		if s, ok, _ := mgr.GetString(ctx, key); ok {
			return s
		}
	}
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return defaultVal
}

// readIntSetting is the same shape for integer values.
func readIntSetting(ctx context.Context, mgr *settings.Manager, key, envKey string, defaultVal int) int {
	if mgr != nil {
		if n, ok, _ := mgr.GetInt(ctx, key); ok {
			return int(n)
		}
	}
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}
