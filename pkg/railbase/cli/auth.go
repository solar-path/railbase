package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/auth/oauth"
	"github.com/railbase/railbase/internal/auth/password"
)

// newAuthCmd is the `railbase auth ...` subtree. Currently houses the
// one command Apple's Sign-In flow needs — `apple-secret` — but the
// namespace is the natural home for future helpers like `auth list-
// providers`, `auth rotate-secret`, etc.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Identity helpers (OAuth credential rotation, API tokens, ...)",
	}
	cmd.AddCommand(newAppleSecretCmd())
	cmd.AddCommand(newTokenCmd())
	// v1.7.36 §3.2.10 — per-user device/location origin inspection.
	cmd.AddCommand(newAuthOriginsCmd())
	// FEEDBACK blogger N6 — password reset for collection users.
	cmd.AddCommand(newAuthSetPasswordCmd())
	return cmd
}

// newAuthSetPasswordCmd implements `railbase auth set-password
// <collection> <email> <password>`. Mirrors `admin reset-password` but
// for non-admin auth collections (users, authors, ...). FEEDBACK
// blogger N6.
//
// On success the row's `password_hash` is updated to a fresh
// argon2id PHC and `verified` is set to TRUE so the actor can log in
// immediately (typical post-bootstrap need: backfill migration
// inserted skeleton rows; this command makes them logabble).
func newAuthSetPasswordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-password <collection> <email> <password>",
		Short: "Set a password on an auth collection record",
		Long: strings.TrimSpace(`
Set or reset the password for a user in a non-admin auth collection.

  collection — the auth collection name (e.g. users, authors)
  email      — the row's email column value
  password   — the new plaintext password (will be argon2id-hashed)

Verifies the collection exists, has auth=true, has email + password_hash
columns, then UPDATEs the matching row. Sets verified=true alongside
the hash so the user can log in without a second email-verification
step (post-bootstrap default).

  railbase auth set-password authors margaret@example.com hunter2

For operators bringing collection users out of a backfill migration
into logon-ready state without 8 individual request-password-reset
flows.
`),
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			collection := args[0]
			email := args[1]
			pw := args[2]
			if collection == "" || email == "" || pw == "" {
				return errors.New("collection, email, password are all required")
			}
			hashed, err := password.Hash(pw)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			// Defensive check: refuse non-auth collections so a typo
			// (`users` vs `_users`) doesn't UPDATE a random table.
			var auth bool
			err = rt.pool.Pool.QueryRow(cmd.Context(),
				`SELECT (spec->>'auth')::boolean
				   FROM _admin_collections
				  WHERE name = $1`, collection,
			).Scan(&auth)
			if err != nil {
				// If _admin_collections doesn't have the row, the
				// collection might be code-defined. Check via
				// information_schema for a password_hash column as a
				// proxy.
				var hasCol int
				probeErr := rt.pool.Pool.QueryRow(cmd.Context(), `
					SELECT 1 FROM information_schema.columns
					WHERE table_schema = current_schema()
					  AND table_name = $1
					  AND column_name = 'password_hash'
				`, collection).Scan(&hasCol)
				if probeErr != nil {
					return fmt.Errorf("collection %q does not exist or is not an auth collection (no password_hash column)", collection)
				}
				auth = true
			}
			if !auth {
				return fmt.Errorf("collection %q is not an auth collection — set-password only operates on auth-enabled tables", collection)
			}

			tag, err := rt.pool.Pool.Exec(cmd.Context(),
				fmt.Sprintf(`UPDATE %q SET password_hash = $1, verified = TRUE WHERE email = $2`,
					collection),
				hashed, email,
			)
			if err != nil {
				return fmt.Errorf("update %s: %w", collection, err)
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("no row in %s with email %q", collection, email)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"OK    password updated for %s.%s\n", collection, email)
			return nil
		},
	}
	return cmd
}

func newAppleSecretCmd() *cobra.Command {
	var (
		teamID   string
		clientID string
		keyID    string
		keyFile  string
		validDur time.Duration
	)
	cmd := &cobra.Command{
		Use:   "apple-secret",
		Short: "Mint an Apple Sign-In client_secret JWT",
		Long: strings.TrimSpace(`
Apple Sign-In requires the OAuth client_secret to be a short-lived
ES256-signed JWT — not a static string like other providers. This
command produces one from your developer credentials:

  --team-id     Apple Developer Team ID (10 chars, from developer.apple.com)
  --client-id   Services ID you registered for Sign In with Apple
                (e.g. com.example.web.signin)
  --key-id      Key ID of the .p8 you downloaded
  --key-file    Path to the .p8 private-key file (PKCS#8 PEM)
  --valid       JWT validity (Apple maxes at 6 months; default: 180 days)

The output is a JWT. Drop it into _settings under
oauth.apple.client_secret (or env RAILBASE_OAUTH_APPLE_CLIENT_SECRET)
and restart railbase. Rotate before --valid elapses.
`),
		Example: strings.TrimSpace(`
  railbase auth apple-secret \
    --team-id ABCDE12345 \
    --client-id com.example.web.signin \
    --key-id F6G7H8I9J0 \
    --key-file ./AuthKey_F6G7H8I9J0.p8 \
    --valid 4320h    # 180 days
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if teamID == "" || clientID == "" || keyID == "" || keyFile == "" {
				return fmt.Errorf("--team-id, --client-id, --key-id and --key-file are all required")
			}
			pem, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("read key file: %w", err)
			}
			tok, err := oauth.MintAppleClientSecret(teamID, clientID, keyID, pem, validDur)
			if err != nil {
				return err
			}
			// Operator-friendly output: print the token alone on stdout
			// so you can pipe it into `railbase config set`. Print a
			// human-readable hint on stderr so direct use is also clear.
			fmt.Fprintln(os.Stderr, "Apple client_secret JWT (valid until", time.Now().Add(validDur).UTC().Format(time.RFC3339)+"):")
			fmt.Println(tok)
			return nil
		},
	}
	cmd.Flags().StringVar(&teamID, "team-id", "", "Apple Team ID (required)")
	cmd.Flags().StringVar(&clientID, "client-id", "", "Apple Services ID / client_id (required)")
	cmd.Flags().StringVar(&keyID, "key-id", "", "Key ID for the .p8 file (required)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "Path to AuthKey_*.p8 PEM file (required)")
	cmd.Flags().DurationVar(&validDur, "valid", 180*24*time.Hour, "JWT validity (Apple max: 6 months)")
	return cmd
}
