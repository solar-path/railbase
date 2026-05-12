package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/auth/oauth"
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
