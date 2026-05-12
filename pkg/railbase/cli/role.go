package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/rbac/actionkeys"
)

// newRoleCmd assembles the `railbase role ...` subtree.
//
// Commands:
//
//   role list                                 — every role + scope
//   role show <site|tenant> <name>            — grants on a role
//   role create <site|tenant> <name> [--desc]
//   role delete <site|tenant> <name>          — refuses system roles
//   role grant <site|tenant> <role> <action>
//   role revoke <site|tenant> <role> <action>
//   role assign <user-collection>/<user-id> <site|tenant> <role> [--tenant <uuid>]
//   role unassign <user-collection>/<user-id> <site|tenant> <role> [--tenant <uuid>]
//   role list-for <user-collection>/<user-id>
func newRoleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "role",
		Short: "Manage RBAC roles, grants, and assignments",
	}
	cmd.AddCommand(
		newRoleListCmd(),
		newRoleShowCmd(),
		newRoleCreateCmd(),
		newRoleDeleteCmd(),
		newRoleGrantCmd(),
		newRoleRevokeCmd(),
		newRoleAssignCmd(),
		newRoleUnassignCmd(),
		newRoleListForCmd(),
	)
	return cmd
}

func newRoleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every role",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			roles, err := store.ListRoles(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SCOPE\tNAME\tSYSTEM\tDESCRIPTION")
			for _, r := range roles {
				flag := ""
				if r.IsSystem {
					flag = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Scope, r.Name, flag, r.Description)
			}
			return tw.Flush()
		},
	}
}

func newRoleShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <scope> <name>",
		Short: "Show all action grants on a role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, name := rbac.Scope(args[0]), args[1]
			if scope != rbac.ScopeSite && scope != rbac.ScopeTenant {
				return fmt.Errorf("scope must be 'site' or 'tenant'")
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			role, err := store.GetRole(cmd.Context(), name, scope)
			if err != nil {
				return err
			}
			fmt.Printf("Role %s:%s (%s)\n", role.Scope, role.Name, role.Description)
			if role.IsSystem {
				fmt.Println("  [system role — name/scope immutable]")
			}
			fmt.Println()
			actions, err := store.ListActions(cmd.Context(), role.ID)
			if err != nil {
				return err
			}
			if name == "system_admin" && scope == rbac.ScopeSite {
				fmt.Println("  (bypass role — implicitly grants every action)")
				return nil
			}
			if scope == rbac.ScopeTenant && name == "owner" {
				fmt.Println("  (bypass role — implicitly grants every tenant.* action)")
				return nil
			}
			if len(actions) == 0 {
				fmt.Println("  (no actions granted)")
				return nil
			}
			for _, a := range actions {
				fmt.Printf("  - %s\n", a)
			}
			return nil
		},
	}
}

func newRoleCreateCmd() *cobra.Command {
	var desc string
	cmd := &cobra.Command{
		Use:   "create <scope> <name>",
		Short: "Create a custom role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, name := rbac.Scope(args[0]), args[1]
			if scope != rbac.ScopeSite && scope != rbac.ScopeTenant {
				return fmt.Errorf("scope must be 'site' or 'tenant'")
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			r, err := store.CreateRole(cmd.Context(), name, scope, desc)
			if err != nil {
				return err
			}
			fmt.Printf("Created role %s:%s (id=%s)\n", r.Scope, r.Name, r.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&desc, "desc", "", "human-readable description")
	return cmd
}

func newRoleDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <scope> <name>",
		Short: "Delete a custom role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, name := rbac.Scope(args[0]), args[1]
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			role, err := store.GetRole(cmd.Context(), name, scope)
			if err != nil {
				return err
			}
			if err := store.DeleteRole(cmd.Context(), role.ID); err != nil {
				return err
			}
			fmt.Printf("Deleted role %s:%s\n", scope, name)
			return nil
		},
	}
}

func newRoleGrantCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <scope> <role> <action>",
		Short: "Grant an action key to a role",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, name, action := rbac.Scope(args[0]), args[1], actionkeys.ActionKey(args[2])
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			role, err := store.GetRole(cmd.Context(), name, scope)
			if err != nil {
				return err
			}
			if err := store.Grant(cmd.Context(), role.ID, action); err != nil {
				return err
			}
			fmt.Printf("Granted %s to %s:%s\n", action, scope, name)
			return nil
		},
	}
}

func newRoleRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <scope> <role> <action>",
		Short: "Revoke an action key from a role",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, name, action := rbac.Scope(args[0]), args[1], actionkeys.ActionKey(args[2])
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			role, err := store.GetRole(cmd.Context(), name, scope)
			if err != nil {
				return err
			}
			if err := store.Revoke(cmd.Context(), role.ID, action); err != nil {
				return err
			}
			fmt.Printf("Revoked %s from %s:%s\n", action, scope, name)
			return nil
		},
	}
}

func newRoleAssignCmd() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{
		Use:   "assign <user-collection/user-id> <scope> <role>",
		Short: "Assign a role to a user (use --tenant for tenant assignments)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			coll, uid, err := parseUserRef(args[0])
			if err != nil {
				return err
			}
			scope, name := rbac.Scope(args[1]), args[2]

			var tID *uuid.UUID
			if tenant != "" {
				p, err := uuid.Parse(tenant)
				if err != nil {
					return fmt.Errorf("--tenant must be a UUID: %w", err)
				}
				tID = &p
			}
			if scope == rbac.ScopeTenant && tID == nil {
				return fmt.Errorf("tenant-scoped assignment requires --tenant <uuid>")
			}
			if scope == rbac.ScopeSite && tID != nil {
				return fmt.Errorf("site-scoped assignment must omit --tenant")
			}

			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			role, err := store.GetRole(cmd.Context(), name, scope)
			if err != nil {
				return err
			}
			a, err := store.Assign(cmd.Context(), rbac.AssignInput{
				CollectionName: coll,
				RecordID:       uid,
				RoleID:         role.ID,
				TenantID:       tID,
			})
			if err != nil {
				return err
			}
			tlabel := "site"
			if tID != nil {
				tlabel = "tenant=" + tID.String()
			}
			fmt.Printf("Assigned %s:%s to %s/%s [%s] (assignment_id=%s)\n",
				scope, name, coll, uid, tlabel, a.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant UUID (required for tenant-scoped roles)")
	return cmd
}

func newRoleUnassignCmd() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{
		Use:   "unassign <user-collection/user-id> <scope> <role>",
		Short: "Remove a role from a user",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			coll, uid, err := parseUserRef(args[0])
			if err != nil {
				return err
			}
			scope, name := rbac.Scope(args[1]), args[2]
			var tID *uuid.UUID
			if tenant != "" {
				p, err := uuid.Parse(tenant)
				if err != nil {
					return err
				}
				tID = &p
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			role, err := store.GetRole(cmd.Context(), name, scope)
			if err != nil {
				return err
			}
			if err := store.Unassign(cmd.Context(), coll, uid, role.ID, tID); err != nil {
				return err
			}
			fmt.Printf("Unassigned %s:%s from %s/%s\n", scope, name, coll, uid)
			return nil
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant UUID")
	return cmd
}

func newRoleListForCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-for <user-collection/user-id>",
		Short: "List every role assigned to a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			coll, uid, err := parseUserRef(args[0])
			if err != nil {
				return err
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := rbac.NewStore(rt.pool.Pool)
			list, err := store.ListAssignmentsFor(cmd.Context(), coll, uid)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("(no roles)")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SCOPE\tNAME\tTENANT\tGRANTED")
			for _, a := range list {
				t := "(site)"
				if a.TenantID != nil {
					t = a.TenantID.String()
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Role.Scope, a.Role.Name, t, a.GrantedAt.Format("2006-01-02"))
			}
			return tw.Flush()
		},
	}
}

// parseUserRef accepts "<collection>/<uuid>".
func parseUserRef(s string) (string, uuid.UUID, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", uuid.Nil, errors.New("user ref must be <collection>/<uuid>")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("user uuid: %w", err)
	}
	return parts[0], id, nil
}
