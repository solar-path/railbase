package cli

// v1.7.3 — `railbase auth token <list|create|revoke|rotate>` CLI.
//
// Service-to-service credential management. The CLI is the operator
// surface; an admin-UI panel + per-user "my tokens" REST endpoints
// land in a follow-up slice. Tokens authenticate via the same
// `Authorization: Bearer <token>` header used by sessions, routed
// inside the auth middleware by the `rbat_` prefix.
//
// Lifecycle:
//
//   create   → mint a fresh token; output displayed ONCE on stdout
//   list     → enumerate live tokens for an owner or globally
//   revoke   → mark a token revoked; subsequent auth fails
//   rotate   → create a successor linked to a predecessor; operators
//              distribute the new token, then explicitly revoke the
//              predecessor when migration completes
//
// The create / rotate commands print the raw token on stdout alone
// (suitable for `tee` / `pbcopy` / pipeline use) and human-readable
// metadata on stderr. Subsequent invocations cannot recover the
// raw token — only its fingerprint.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/auth/apitoken"
	"github.com/railbase/railbase/internal/auth/secret"
)

// newTokenCmd assembles `railbase auth token ...`.
func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens (long-lived bearer credentials)",
		Long: strings.TrimSpace(`
API tokens are long-lived bearer credentials for service-to-service
auth. Distinct from session tokens (short-lived, browser-issued)
and record tokens (single-use, email-link).

Format on the wire: rbat_<43-char base64url>. Storage holds only the
HMAC-SHA-256 hash. Operators see the raw token ONCE at creation.
`),
	}
	cmd.AddCommand(newTokenCreateCmd())
	cmd.AddCommand(newTokenListCmd())
	cmd.AddCommand(newTokenRevokeCmd())
	cmd.AddCommand(newTokenRotateCmd())
	return cmd
}

func newTokenCreateCmd() *cobra.Command {
	var (
		ownerID         string
		ownerCollection string
		name            string
		scopesCSV       string
		ttl             time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a new API token",
		Long: strings.TrimSpace(`
Creates an API token impersonating the given owner. The raw token
is printed on stdout (suitable for piping). Subsequent commands
cannot recover the raw value — copy it now or rotate.

The token's permissions are bounded by the owner's permissions —
a token NEVER exceeds its owner.
`),
		Example: strings.TrimSpace(`
  # 30-day token for a service account
  railbase auth token create \
    --owner 019e8a72-...  \
    --collection users \
    --name "CI deploy bot" \
    --ttl 720h

  # Non-expiring token (revoke is the only way to invalidate)
  railbase auth token create --owner ... --collection users --name "edge worker"
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if ownerID == "" {
				return fmt.Errorf("--owner is required (UUID)")
			}
			oid, err := uuid.Parse(ownerID)
			if err != nil {
				return fmt.Errorf("--owner is not a valid UUID: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()

			store, err := apiTokenStore(rt)
			if err != nil {
				return err
			}
			raw, rec, err := store.Create(cmd.Context(), apitoken.CreateInput{
				Name:            name,
				OwnerID:         oid,
				OwnerCollection: ownerCollection,
				Scopes:          splitCSVTrim(scopesCSV),
				TTL:             ttl,
			})
			if err != nil {
				return err
			}
			// Operator-friendly output: raw token alone on stdout,
			// human-readable metadata on stderr.
			fmt.Fprintln(os.Stderr, "API token minted (DISPLAYED ONCE — copy now):")
			fmt.Fprintf(os.Stderr, "  id:          %s\n", rec.ID)
			fmt.Fprintf(os.Stderr, "  name:        %s\n", rec.Name)
			fmt.Fprintf(os.Stderr, "  owner:       %s/%s\n", rec.OwnerCollection, rec.OwnerID)
			fmt.Fprintf(os.Stderr, "  scopes:      %s\n", scopesFmt(rec.Scopes))
			fmt.Fprintf(os.Stderr, "  expires:     %s\n", expiresFmt(rec.ExpiresAt))
			fmt.Fprintf(os.Stderr, "  fingerprint: %s\n", apitoken.Fingerprint(raw, mustKey(rt)))
			fmt.Println(raw)
			return nil
		},
	}
	cmd.Flags().StringVar(&ownerID, "owner", "", "Owner record UUID (required)")
	cmd.Flags().StringVar(&ownerCollection, "collection", "users", "Owner's auth-collection name")
	cmd.Flags().StringVar(&name, "name", "", "Human-readable label (required)")
	cmd.Flags().StringVar(&scopesCSV, "scopes", "", "Comma-separated action-key scopes (advisory; v1 ignored at runtime)")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Lifetime (e.g. 720h for 30 days). 0 = never expires.")
	return cmd
}

func newTokenListCmd() *cobra.Command {
	var (
		ownerID         string
		ownerCollection string
		all             bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API tokens (default: all tokens system-wide)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			store, err := apiTokenStore(rt)
			if err != nil {
				return err
			}
			var rows []*apitoken.Record
			if ownerID != "" {
				oid, err := uuid.Parse(ownerID)
				if err != nil {
					return fmt.Errorf("--owner not a valid UUID: %w", err)
				}
				rows, err = store.List(cmd.Context(), ownerCollection, oid)
				if err != nil {
					return err
				}
			} else if all {
				rows, err = store.ListAll(cmd.Context())
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("specify --owner <id> [--collection <name>] OR --all")
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stderr, "(no tokens)")
				return nil
			}
			fmt.Printf("%-36s  %-20s  %-12s  %-10s  %s\n",
				"ID", "NAME", "STATUS", "OWNER", "EXPIRES")
			for _, r := range rows {
				status := "active"
				if r.RevokedAt != nil {
					status = "revoked"
				} else if r.ExpiresAt != nil && r.ExpiresAt.Before(time.Now()) {
					status = "expired"
				}
				fmt.Printf("%-36s  %-20s  %-12s  %-10s  %s\n",
					r.ID, truncate(r.Name, 20), status,
					truncate(r.OwnerCollection, 10), expiresFmt(r.ExpiresAt))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ownerID, "owner", "", "Filter by owner UUID")
	cmd.Flags().StringVar(&ownerCollection, "collection", "users", "Owner's auth-collection (when --owner is set)")
	cmd.Flags().BoolVar(&all, "all", false, "List every token system-wide (admin surface)")
	return cmd
}

func newTokenRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke a token by id (idempotent)",
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
			store, err := apiTokenStore(rt)
			if err != nil {
				return err
			}
			if err := store.Revoke(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "revoked", id)
			return nil
		},
	}
	return cmd
}

func newTokenRotateCmd() *cobra.Command {
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   "rotate <token-id>",
		Short: "Mint a successor token (rotation — predecessor stays active)",
		Long: strings.TrimSpace(`
Creates a new token linked to the predecessor via rotated_from.
The predecessor remains active so operators can stage the rollout
(distribute the new token, then revoke the predecessor explicitly).

Inherits name / owner / scopes from the predecessor. TTL: if
--ttl is given, that wins; else reuses the predecessor's remaining
TTL; else 30 days for non-expiring predecessors.
`),
		Args: cobra.ExactArgs(1),
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
			store, err := apiTokenStore(rt)
			if err != nil {
				return err
			}
			raw, rec, err := store.Rotate(cmd.Context(), id, ttl)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Successor minted (DISPLAYED ONCE — copy now):")
			fmt.Fprintf(os.Stderr, "  id:           %s\n", rec.ID)
			fmt.Fprintf(os.Stderr, "  name:         %s\n", rec.Name)
			fmt.Fprintf(os.Stderr, "  rotated_from: %s\n", *rec.RotatedFrom)
			fmt.Fprintf(os.Stderr, "  expires:      %s\n", expiresFmt(rec.ExpiresAt))
			fmt.Fprintf(os.Stderr, "  next step:    revoke predecessor with `railbase auth token revoke %s`\n", id)
			fmt.Println(raw)
			return nil
		},
	}
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Override TTL (default: inherit from predecessor)")
	return cmd
}

// --- helpers ---

// apiTokenStore loads the master key and constructs the apitoken
// Store. Shared across the CLI commands to centralise the secret
// loading + error wrapping.
func apiTokenStore(rt *runtimeContext) (*apitoken.Store, error) {
	key, err := secret.LoadFromDataDir(rt.cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("load master secret: %w", err)
	}
	return apitoken.NewStore(rt.pool.Pool, key), nil
}

// mustKey loads the master key for the fingerprint helper. Returns a
// zero key on error — fingerprint becomes a fixed deterministic value
// per token but operators see "no key" warning on stderr through the
// runtime log.
func mustKey(rt *runtimeContext) secret.Key {
	k, err := secret.LoadFromDataDir(rt.cfg.DataDir)
	if err != nil {
		rt.log.Warn("fingerprint requested without secret", "err", err)
		return secret.Key{}
	}
	return k
}

func splitCSVTrim(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func scopesFmt(s []string) string {
	if len(s) == 0 {
		return "(owner-bounded, no scope restrictions)"
	}
	return strings.Join(s, ",")
}

func expiresFmt(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// Silence unused-import warning when only some helpers ship — context
// is used transitively via cmd.Context(). Kept here for clarity if a
// future helper needs a direct context.
var _ = context.Background
