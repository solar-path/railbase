package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
)

// newTenantCmd assembles the `railbase tenant ...` subtree.
//
// v0.5 surface: create, list, delete. Per-tenant settings, billing,
// invites, etc. arrive in v1.1 with the railbase-orgs plugin.
func newTenantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenant",
		Short: "Manage tenants",
	}
	cmd.AddCommand(newTenantCreateCmd(), newTenantListCmd(), newTenantDeleteCmd())
	return cmd
}

func newTenantCreateCmd() *cobra.Command {
	var idFlag string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant",
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
			name := strings.TrimSpace(args[0])
			if name == "" {
				return errors.New("name cannot be empty")
			}
			id := uuid.Must(uuid.NewV7())
			if idFlag != "" {
				parsed, err := uuid.Parse(idFlag)
				if err != nil {
					return fmt.Errorf("--id: %w", err)
				}
				id = parsed
			}
			if _, err := rt.pool.Pool.Exec(cmd.Context(),
				`INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, name); err != nil {
				if isUniqueViolation(err) {
					return fmt.Errorf("tenant with name %q already exists", name)
				}
				return err
			}
			fmt.Printf("created tenant %s (id=%s)\n", name, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&idFlag, "id", "",
		"Use this UUID instead of a fresh one (useful for tests / migrations)")
	return cmd
}

func newTenantListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List tenants",
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
			rows, err := rt.pool.Pool.Query(cmd.Context(),
				`SELECT id, name, created FROM tenants ORDER BY lower(name)`)
			if err != nil {
				return err
			}
			defer rows.Close()
			var any bool
			fmt.Printf("%-36s  %-30s  %s\n", "ID", "NAME", "CREATED")
			for rows.Next() {
				any = true
				var id uuid.UUID
				var name string
				var created interface{}
				if err := rows.Scan(&id, &name, &created); err != nil {
					return err
				}
				fmt.Printf("%-36s  %-30s  %v\n", id, name, created)
			}
			if !any {
				fmt.Println("(no tenants yet — run `railbase tenant create <name>`)")
			}
			return rows.Err()
		},
	}
}

func newTenantDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id-or-name>",
		Short: "Delete a tenant (CASCADE removes all tenant rows)",
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
			arg := args[0]
			var id uuid.UUID
			if u, err := uuid.Parse(arg); err == nil {
				id = u
			} else {
				err := rt.pool.Pool.QueryRow(cmd.Context(),
					`SELECT id FROM tenants WHERE lower(name) = lower($1)`, arg).Scan(&id)
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("tenant %q not found", arg)
				}
				if err != nil {
					return err
				}
			}
			tag, err := rt.pool.Pool.Exec(cmd.Context(),
				`DELETE FROM tenants WHERE id = $1`, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("tenant %q not found", arg)
			}
			fmt.Printf("deleted tenant %s\n", id)
			return nil
		},
	}
}
