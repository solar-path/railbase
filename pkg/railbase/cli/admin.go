package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac"
)

// newAdminCmd assembles the `railbase admin ...` subtree.
//
// Commands:
//
//	admin create <email> [--password=...]
//	admin list
//	admin delete <email-or-id>
//
// The CLI auto-applies system migrations before any subcommand so a
// fresh deployment doesn't need a manual `migrate up` pass to bring
// `_admins` online.
func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Manage system administrators",
	}
	cmd.AddCommand(newAdminCreateCmd(), newAdminListCmd(), newAdminDeleteCmd(), newAdminResetPasswordCmd())
	return cmd
}

func newAdminCreateCmd() *cobra.Command {
	var pw string
	var noEmail bool
	cmd := &cobra.Command{
		Use:   "create <email>",
		Short: "Create a system administrator",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()

			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			email := strings.TrimSpace(args[0])
			if pw == "" {
				p, err := readPasswordTwice("Password: ", "Confirm: ")
				if err != nil {
					return err
				}
				pw = p
			}
			if len(pw) < 8 {
				return errors.New("password must be at least 8 characters")
			}

			store := admins.NewStore(rt.pool.Pool)
			a, err := store.Create(cmd.Context(), email, pw)
			if err != nil {
				if isUniqueViolation(err) {
					return fmt.Errorf("admin with email %q already exists", email)
				}
				return err
			}
			fmt.Printf("created admin %s (id=%s)\n", a.Email, a.ID)

			// v1.x — auto-assign site:system_admin so the new admin
			// passes the RBAC gates on /api/_admin/*. Mirrors the
			// bootstrap handler. Best-effort: a missing assignment is
			// recoverable through the role-management UI, so we log
			// and continue rather than fail the CLI command.
			rbacStore := rbac.NewStore(rt.pool.Pool)
			if err := rbac.AssignSystemAdmin(cmd.Context(), rbacStore, a.ID); err != nil {
				fmt.Fprintf(os.Stderr,
					"warning: assign system_admin to %s failed: %v\n", a.Email, err)
			}

			// v1.7.43 — enqueue welcome + broadcast notice unless the
			// operator passed --no-email OR the mailer was explicitly
			// skipped during setup. We bypass the bootstrap-handler's
			// 412 gate here on purpose: the CLI is the operator's own
			// surface, and they can always re-trigger emails later via
			// `railbase jobs enqueue send_email_async ...` if they need
			// to. The CLI just notes when emails were skipped so a
			// distracted operator sees the consequence in stdout.
			if noEmail {
				fmt.Println("note: --no-email set — no welcome / broadcast emails enqueued")
				return nil
			}
			// FEEDBACK #10 — surface "mailer not configured" up-front so
			// the operator running `admin create` on a fresh box doesn't
			// wonder why the welcome email never arrives. We don't FAIL
			// — the welcome email is best-effort — but we do tell them
			// what to do next (configure or skip).
			if mailerUnconfiguredCLI(cmd.Context(), rt) {
				fmt.Println("note: mailer not configured — welcome email will be enqueued but won't deliver " +
					"until you set `mailer.from`. Pass --no-email to suppress, or finish setup in /_/settings/mailer.")
			}
			if err := enqueueAdminEmailsCLI(cmd.Context(), rt, a, "CLI (`railbase admin create`)", "operator-cli"); err != nil {
				// Best-effort — log + continue. The admin was created;
				// failing the command because of an email-queue error
				// would put the operator into a confusing state.
				fmt.Fprintf(os.Stderr, "warning: enqueue welcome email failed: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&pw, "password", "",
		"Password (insecure: prefer the interactive prompt)")
	cmd.Flags().BoolVar(&noEmail, "no-email", false,
		"Skip welcome + broadcast notice emails (use sparingly; default is to send)")
	return cmd
}

func newAdminListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List system administrators",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			store := admins.NewStore(rt.pool.Pool)
			list, err := store.List(cmd.Context())
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("(no admins yet — run `railbase admin create <email>`)")
				return nil
			}
			fmt.Printf("%-36s  %-30s  %s\n", "ID", "EMAIL", "CREATED")
			for _, a := range list {
				fmt.Printf("%-36s  %-30s  %s\n",
					a.ID, a.Email, a.Created.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
}

func newAdminDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <email-or-id>",
		Short: "Delete a system administrator",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			store := admins.NewStore(rt.pool.Pool)
			arg := args[0]
			var id uuid.UUID
			if u, err := uuid.Parse(arg); err == nil {
				id = u
			} else {
				a, err := store.GetByEmail(cmd.Context(), arg)
				if err != nil {
					return fmt.Errorf("admin %q not found", arg)
				}
				id = a.ID
			}
			if err := store.Delete(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Printf("deleted admin %s\n", id)
			return nil
		},
	}
}

// newAdminResetPasswordCmd is the operator-grade escape hatch for
// admin password recovery: works WITHOUT a configured mailer (which
// the HTTP forgot-password endpoint cannot, by design). Operator
// must have shell access to the host.
//
// Usage:
//
//	railbase admin reset-password <email>           # interactive
//	railbase admin reset-password <email> -p <pw>   # scripted
//
// On success: sets the new password hash + invalidates every live
// session for that admin (so an attacker holding a stale cookie is
// kicked out alongside the legitimate operator who's now reset).
func newAdminResetPasswordCmd() *cobra.Command {
	var pw string
	cmd := &cobra.Command{
		Use:   "reset-password <email-or-id>",
		Short: "Reset an administrator's password (operator escape hatch — no mailer required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			store := admins.NewStore(rt.pool.Pool)
			arg := strings.TrimSpace(args[0])
			var target *admins.Admin
			if u, err := uuid.Parse(arg); err == nil {
				target, err = store.GetByID(cmd.Context(), u)
				if err != nil {
					return fmt.Errorf("admin %q not found", arg)
				}
			} else {
				target, err = store.GetByEmail(cmd.Context(), arg)
				if err != nil {
					return fmt.Errorf("admin %q not found", arg)
				}
			}

			if pw == "" {
				p, err := readPasswordTwice("New password: ", "Confirm: ")
				if err != nil {
					return err
				}
				pw = p
			}
			if len(pw) < 8 {
				return errors.New("password must be at least 8 characters")
			}

			if err := store.SetPassword(cmd.Context(), target.ID, pw); err != nil {
				return fmt.Errorf("set password: %w", err)
			}

			// Best-effort: revoke every live session belonging to the
			// admin so cookies stolen pre-reset can't survive. Failure
			// here is non-fatal — the password is already changed; we
			// just warn so the operator knows.
			masterKey, mkErr := secret.LoadFromDataDir(rt.cfg.DataDir)
			if mkErr == nil {
				sessions := admins.NewSessionStore(rt.pool.Pool, masterKey)
				revoked, rerr := sessions.RevokeAllFor(cmd.Context(), target.ID)
				if rerr != nil {
					fmt.Fprintf(os.Stderr, "warning: revoke live sessions failed: %v\n", rerr)
				} else {
					fmt.Printf("revoked %d live session(s)\n", revoked)
				}
			} else {
				fmt.Fprintf(os.Stderr, "warning: cannot read .secret, sessions not revoked: %v\n", mkErr)
			}

			fmt.Printf("password reset for admin %s (id=%s)\n", target.Email, target.ID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&pw, "password", "p", "",
		"New password (skips interactive prompt; useful for scripts but avoid in shell history)")
	return cmd
}

// readPasswordTwice prompts the user twice and confirms the entries
// match. Reads from /dev/tty when available so piped stdin doesn't
// silently feed the password.
func readPasswordTwice(prompt1, prompt2 string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Pipeable fallback: read a single line from stdin without
		// echo suppression. Useful in scripted setups (`echo pw |
		// railbase admin create ...`).
		fmt.Fprint(os.Stderr, prompt1)
		s := bufio.NewScanner(os.Stdin)
		if !s.Scan() {
			return "", errors.New("password: no input")
		}
		return s.Text(), nil
	}
	fmt.Fprint(os.Stderr, prompt1)
	pw1, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Fprint(os.Stderr, prompt2)
	pw2, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read confirmation: %w", err)
	}
	if string(pw1) != string(pw2) {
		return "", errors.New("passwords do not match")
	}
	return string(pw1), nil
}

// applySysMigrations runs the embedded system migrations. Subcommands
// that touch system tables call this so a brand-new database becomes
// usable in one CLI invocation. User migrations stay manual via
// `railbase migrate up`.
func applySysMigrations(ctx context.Context, rt *runtimeContext) error {
	sys, err := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err != nil {
		return fmt.Errorf("discover system migrations: %w", err)
	}
	runner := &migrate.Runner{Pool: rt.pool.Pool, Log: rt.log}
	if err := runner.Apply(ctx, sys); err != nil {
		return fmt.Errorf("apply system migrations: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
