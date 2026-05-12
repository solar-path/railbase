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
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
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
	cmd.AddCommand(newAdminCreateCmd(), newAdminListCmd(), newAdminDeleteCmd())
	return cmd
}

func newAdminCreateCmd() *cobra.Command {
	var pw string
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
			return nil
		},
	}
	cmd.Flags().StringVar(&pw, "password", "",
		"Password (insecure: prefer the interactive prompt)")
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
