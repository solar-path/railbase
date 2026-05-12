package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/auth/origins"
)

// newAuthOriginsCmd assembles `railbase auth origins ...`.
//
// Commands:
//
//	auth origins list <user_id> [--collection users]
//	auth origins delete <origin_id>
//
// v1.7.36 §3.2.10 — operators reach the per-user device/location
// fingerprints from the terminal. Admin UI is deferred (the second
// half of the slice); this CLI gives ops an immediate path to
// inspect / revoke origins so a compromised account can be locked
// out of "known device" recognition without waiting for the UI.
func newAuthOriginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "origins",
		Short: "Inspect + revoke recorded signin origins (per-user devices/networks)",
		Long: `Per-user device + network fingerprints recorded on each successful
signin. Granularity is /24 (IPv4) or /48 (IPv6) for the network class
and a version-stripped sha256 for the User-Agent, so trivial point
upgrades / NAT lease changes don't re-trigger the new-device
notification.`,
	}
	cmd.AddCommand(
		newAuthOriginsListCmd(),
		newAuthOriginsDeleteCmd(),
	)
	return cmd
}

func newAuthOriginsListCmd() *cobra.Command {
	var collection string
	cmd := &cobra.Command{
		Use:   "list <user_id>",
		Short: "List recorded signin origins for one user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid user_id (must be UUID): %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := origins.NewStore(rt.pool.Pool)
			rows, err := store.ListForUser(cmd.Context(), uid, collection)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stderr, "(no recorded origins for this user)")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCOLLECTION\tIP_CLASS\tUA_HASH\tFIRST_SEEN\tLAST_SEEN")
			for _, o := range rows {
				// Truncate UA hash for legibility — the full 64-char hex is
				// rarely useful at the terminal; operators who need the
				// full value can read it back from `_auth_origins` directly.
				uaShort := o.UAHash
				if len(uaShort) > 12 {
					uaShort = uaShort[:12] + "…"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					o.ID, o.Collection, o.IPClass, uaShort,
					o.FirstSeenAt.Format(time.RFC3339),
					o.LastSeenAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&collection, "collection", "",
		"limit to one auth collection (default: list across all collections)")
	return cmd
}

func newAuthOriginsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <origin_id>",
		Short: "Revoke one origin (next signin from that ip_class+ua_hash re-notifies)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid origin_id: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := origins.NewStore(rt.pool.Pool)
			if err := store.Delete(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Println("deleted")
			return nil
		},
	}
}
