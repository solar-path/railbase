package cli

// v1.7.51 — `railbase scim token <list|create|revoke|rotate>` CLI.
//
// SCIM bearer credentials for external IdPs (Okta / Azure AD /
// OneLogin / Auth0). Mirror image of `railbase auth token` (v1.7.3)
// but separate token store with separate prefix (rbsm_ vs rbat_) and
// separate audit semantics.
//
// Lifecycle:
//
//   create   → mint a fresh SCIM token; the raw value prints ONCE on
//              stdout. Operator pastes it into the IdP's SCIM config
//              and the IdP starts provisioning Users + Groups.
//   list     → show alive tokens for a collection
//   revoke   → mark token revoked (IdP can no longer provision)
//   rotate   → mint a successor; old token expires in 1 hour so the
//              IdP has a window to pick up the new credential

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/auth/secret"
)

// newSCIMCmd assembles `railbase scim ...` — currently just `token`.
func newSCIMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scim",
		Short: "Manage SCIM 2.0 inbound provisioning (RFC 7643/7644)",
		Long: strings.TrimSpace(`
SCIM is one-way provisioning from an external IdP into Railbase. The
IdP POSTs Users and Groups to /scim/v2/{Users,Groups}; Railbase
translates each operation into auth-collection row changes plus
SCIM-managed group memberships.

Authentication uses long-lived bearer credentials of the form
` + "`" + `rbsm_<43-char base64url>` + "`" + `. The 'token' subcommands manage them.
`),
	}
	cmd.AddCommand(newSCIMTokenCmd())
	return cmd
}

func newSCIMTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage SCIM bearer credentials",
	}
	cmd.AddCommand(newSCIMTokenCreateCmd())
	cmd.AddCommand(newSCIMTokenListCmd())
	cmd.AddCommand(newSCIMTokenRevokeCmd())
	cmd.AddCommand(newSCIMTokenRotateCmd())
	return cmd
}

func newSCIMTokenCreateCmd() *cobra.Command {
	var (
		name       string
		collection string
		ttl        time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a new SCIM bearer credential",
		Long: strings.TrimSpace(`
Creates a SCIM bearer credential. Print the raw token ONCE on
stdout — copy it into the IdP's SCIM endpoint configuration. The
raw value is not recoverable; rotate or revoke to invalidate.

Default TTL is 1 year; pass --ttl 0 for never-expires (revoke is
the only way to invalidate).
`),
		Example: strings.TrimSpace(`
  # 1-year token for Okta production
  railbase scim token create --name "okta-prod" --collection users

  # 30-day token (rotate via 'railbase scim token rotate')
  railbase scim token create --name "azure-ad" --collection users --ttl 720h
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if collection == "" {
				return fmt.Errorf("--collection is required")
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			store, err := scimTokenStore(rt)
			if err != nil {
				return err
			}
			realTTL := ttl
			if realTTL == 0 {
				realTTL = scimauth.DefaultTokenTTL
			}
			raw, rec, err := store.Create(cmd.Context(), scimauth.CreateInput{
				Name:       name,
				Collection: collection,
				TTL:        realTTL,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "SCIM bearer credential minted (DISPLAYED ONCE — copy now):")
			fmt.Fprintf(os.Stderr, "  id:         %s\n", rec.ID)
			fmt.Fprintf(os.Stderr, "  name:       %s\n", rec.Name)
			fmt.Fprintf(os.Stderr, "  collection: %s\n", rec.Collection)
			fmt.Fprintf(os.Stderr, "  expires:    %s\n", scimExpiresFmt(rec.ExpiresAt))
			fmt.Fprintln(os.Stderr)
			fmt.Println(raw)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Operator-readable label (e.g. okta-prod)")
	cmd.Flags().StringVar(&collection, "collection", "users", "Target auth-collection")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Lifetime; 0 = default 1y, use 'never' via direct DB only")
	return cmd
}

func newSCIMTokenListCmd() *cobra.Command {
	var (
		collection     string
		includeRevoked bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List SCIM tokens for a collection",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			store, err := scimTokenStore(rt)
			if err != nil {
				return err
			}
			rows, err := store.List(cmd.Context(), collection, includeRevoked)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stderr, "(no tokens)")
				return nil
			}
			fmt.Printf("%-36s  %-20s  %-12s  %s\n", "ID", "NAME", "STATUS", "EXPIRES")
			for _, r := range rows {
				status := "active"
				if r.RevokedAt != nil {
					status = "revoked"
				} else if r.ExpiresAt != nil && r.ExpiresAt.Before(time.Now()) {
					status = "expired"
				}
				fmt.Printf("%-36s  %-20s  %-12s  %s\n",
					r.ID, truncate(r.Name, 20), status, scimExpiresFmt(r.ExpiresAt))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&collection, "collection", "users", "Collection to list tokens for")
	cmd.Flags().BoolVar(&includeRevoked, "all", false, "Include revoked tokens")
	return cmd
}

func newSCIMTokenRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a SCIM token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("not a valid UUID: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			store, err := scimTokenStore(rt)
			if err != nil {
				return err
			}
			if err := store.Revoke(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "revoked: %s\n", id)
			return nil
		},
	}
	return cmd
}

func newSCIMTokenRotateCmd() *cobra.Command {
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   "rotate <id>",
		Short: "Mint a successor; old token expires in 1h overlap",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("not a valid UUID: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			store, err := scimTokenStore(rt)
			if err != nil {
				return err
			}
			realTTL := ttl
			if realTTL == 0 {
				realTTL = scimauth.DefaultTokenTTL
			}
			raw, rec, err := store.Rotate(cmd.Context(), id, realTTL)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "rotated — successor (DISPLAYED ONCE):")
			fmt.Fprintf(os.Stderr, "  predecessor: %s (expires in 1h)\n", id)
			fmt.Fprintf(os.Stderr, "  successor:   %s\n", rec.ID)
			fmt.Fprintf(os.Stderr, "  expires:     %s\n", scimExpiresFmt(rec.ExpiresAt))
			fmt.Println(raw)
			return nil
		},
	}
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Successor TTL; 0 = default 1y")
	return cmd
}

// --- helpers ---

func scimTokenStore(rt *runtimeContext) (*scimauth.TokenStore, error) {
	key, err := secret.LoadFromDataDir(rt.cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("load master secret: %w", err)
	}
	return scimauth.NewTokenStore(rt.pool.Pool, key), nil
}

func scimExpiresFmt(t *time.Time) string {
	if t == nil {
		return "never"
	}
	if t.Before(time.Now()) {
		return "expired " + t.UTC().Format(time.RFC3339)
	}
	return t.UTC().Format(time.RFC3339)
}
