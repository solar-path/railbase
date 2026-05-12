package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/webhooks"
)

// newWebhooksCmd assembles `railbase webhooks ...`.
//
// Commands:
//
//	webhooks list                              — show all configured
//	webhooks create --name N --url U --events E1,E2 [--secret S] [--inactive]
//	webhooks delete <name|id>
//	webhooks pause <name|id>
//	webhooks resume <name|id>
//	webhooks deliveries <name|id> [--limit N]   — recent attempts
//	webhooks reveal-secret <name|id>            — print stored secret
//
// Notes: secrets are auto-generated on `create` when --secret is
// omitted. `reveal-secret` is its only retrieval path — list/show
// never include the secret.
func newWebhooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhooks",
		Short: "Manage outbound webhook subscriptions",
	}
	cmd.AddCommand(
		newWebhooksListCmd(),
		newWebhooksCreateCmd(),
		newWebhooksDeleteCmd(),
		newWebhooksPauseCmd(),
		newWebhooksResumeCmd(),
		newWebhooksDeliveriesCmd(),
		newWebhooksRevealSecretCmd(),
	)
	return cmd
}

func newWebhooksListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured webhooks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := webhooks.NewStore(rt.pool.Pool)
			rows, err := store.List(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tACTIVE\tEVENTS\tURL")
			for _, w := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\n",
					w.ID, w.Name, w.Active, strings.Join(w.Events, ","), w.URL)
			}
			return tw.Flush()
		},
	}
}

func newWebhooksCreateCmd() *cobra.Command {
	var (
		name     string
		url      string
		events   string
		secret   string
		inactive bool
		timeout  int
		retries  int
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new outbound webhook",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" || url == "" {
				return fmt.Errorf("--name and --url are required")
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := webhooks.NewStore(rt.pool.Pool)
			active := !inactive
			in := webhooks.CreateInput{
				Name:        name,
				URL:         url,
				SecretB64:   secret,
				Events:      splitCommas(events),
				Active:      &active,
				MaxAttempts: retries,
				TimeoutMS:   timeout,
			}
			w, err := store.Create(cmd.Context(), in)
			if err != nil {
				return err
			}
			fmt.Printf("Created webhook %s (%s)\n", w.Name, w.ID)
			if secret == "" {
				fmt.Printf("Secret (b64): %s\n", w.SecretB64)
				fmt.Println("⚠ Save this secret now — it is NOT shown again unless you reveal-secret.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "unique handle")
	cmd.Flags().StringVar(&url, "url", "", "destination URL (http/https)")
	cmd.Flags().StringVar(&events, "events", "", "comma-separated event patterns (e.g. record.created.posts,record.*.tags)")
	cmd.Flags().StringVar(&secret, "secret", "", "base64-encoded HMAC key (auto-generated if blank)")
	cmd.Flags().BoolVar(&inactive, "inactive", false, "create paused")
	cmd.Flags().IntVar(&timeout, "timeout-ms", 0, "request timeout (default 30000)")
	cmd.Flags().IntVar(&retries, "max-attempts", 0, "retry budget (default 5)")
	return cmd
}

func newWebhooksDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a webhook (cascades deliveries)",
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
			store := webhooks.NewStore(rt.pool.Pool)
			w, err := resolveWebhook(cmd.Context(), store, args[0])
			if err != nil {
				return err
			}
			ok, err := store.Delete(cmd.Context(), w.ID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("not found: %s", args[0])
			}
			fmt.Printf("Deleted webhook %s\n", w.Name)
			return nil
		},
	}
}

func newWebhooksPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name|id>",
		Short: "Pause a webhook (skip dispatch without deleting)",
		Args:  cobra.ExactArgs(1),
		RunE:  toggleActive(false),
	}
}

func newWebhooksResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name|id>",
		Short: "Re-enable a paused webhook",
		Args:  cobra.ExactArgs(1),
		RunE:  toggleActive(true),
	}
}

func toggleActive(active bool) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		rt, err := openRuntime(cmd.Context())
		if err != nil {
			return err
		}
		defer rt.cleanup()
		if err := applySysMigrations(cmd.Context(), rt); err != nil {
			return err
		}
		store := webhooks.NewStore(rt.pool.Pool)
		w, err := resolveWebhook(cmd.Context(), store, args[0])
		if err != nil {
			return err
		}
		if err := store.SetActive(cmd.Context(), w.ID, active); err != nil {
			return err
		}
		verb := "paused"
		if active {
			verb = "resumed"
		}
		fmt.Printf("Webhook %s %s\n", w.Name, verb)
		return nil
	}
}

func newWebhooksDeliveriesCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "deliveries <name|id>",
		Short: "Show recent delivery attempts for a webhook",
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
			store := webhooks.NewStore(rt.pool.Pool)
			w, err := resolveWebhook(cmd.Context(), store, args[0])
			if err != nil {
				return err
			}
			rows, err := store.ListDeliveries(cmd.Context(), w.ID, limit)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "CREATED\tEVENT\tATTEMPT\tSTATUS\tCODE\tERROR")
			for _, d := range rows {
				code := "-"
				if d.ResponseCode != nil {
					code = fmt.Sprintf("%d", *d.ResponseCode)
				}
				errMsg := d.ErrorMsg
				if len(errMsg) > 60 {
					errMsg = errMsg[:60] + "…"
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
					d.CreatedAt.Format(time.RFC3339), d.Event, d.Attempt, d.Status, code, errMsg)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "row limit (default 50, max 1000)")
	return cmd
}

func newWebhooksRevealSecretCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reveal-secret <name|id>",
		Short: "Print the HMAC secret (use rarely — it's the integration key)",
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
			store := webhooks.NewStore(rt.pool.Pool)
			w, err := resolveWebhook(cmd.Context(), store, args[0])
			if err != nil {
				return err
			}
			fmt.Println(w.SecretB64)
			return nil
		},
	}
}

// resolveWebhook accepts either a UUID or a name. CLI commands take
// `<name|id>` so users can paste either; we try UUID first, fall back
// to name lookup.
func resolveWebhook(ctx context.Context, store *webhooks.Store, ref string) (*webhooks.Webhook, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return store.GetByID(ctx, id)
	}
	w, err := store.GetByName(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("webhook %q not found: %w", ref, err)
	}
	return w, nil
}

func splitCommas(s string) []string {
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
