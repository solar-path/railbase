package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/jobs"
)

// newCronCmd assembles `railbase cron ...`.
//
// Commands:
//
//	cron list
//	cron show <name>
//	cron upsert <name> <expression> <kind> [--payload JSON]
//	cron delete <name>
//	cron enable <name>
//	cron disable <name>
//	cron run-now <name>     — materialise a job immediately; next_run_at unchanged
func newCronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage persisted cron schedules",
	}
	cmd.AddCommand(
		newCronListCmd(),
		newCronShowCmd(),
		newCronUpsertCmd(),
		newCronDeleteCmd(),
		newCronEnableCmd(),
		newCronDisableCmd(),
		newCronRunNowCmd(),
	)
	return cmd
}

func newCronListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every cron schedule",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := jobs.NewCronStore(rt.pool.Pool)
			rows, err := store.List(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tEXPRESSION\tKIND\tENABLED\tNEXT_RUN_AT\tLAST_RUN_AT")
			for _, r := range rows {
				next := "—"
				if r.NextRunAt != nil {
					next = r.NextRunAt.Format(time.RFC3339)
				}
				last := "—"
				if r.LastRunAt != nil {
					last = r.LastRunAt.Format(time.RFC3339)
				}
				enabled := "no"
				if r.Enabled {
					enabled = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					r.Name, r.Expression, r.Kind, enabled, next, last)
			}
			return tw.Flush()
		},
	}
}

func newCronShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a single cron schedule",
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
			store := jobs.NewCronStore(rt.pool.Pool)
			r, err := store.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Schedule %s\n", r.Name)
			fmt.Printf("  expression: %s\n", r.Expression)
			fmt.Printf("  kind:       %s\n", r.Kind)
			fmt.Printf("  enabled:    %t\n", r.Enabled)
			if r.NextRunAt != nil {
				fmt.Printf("  next_run:   %s\n", r.NextRunAt.Format(time.RFC3339))
			}
			if r.LastRunAt != nil {
				fmt.Printf("  last_run:   %s\n", r.LastRunAt.Format(time.RFC3339))
			}
			fmt.Printf("  payload:    %s\n", string(r.Payload))
			fmt.Printf("  created_at: %s\n", r.CreatedAt.Format(time.RFC3339))
			fmt.Printf("  updated_at: %s\n", r.UpdatedAt.Format(time.RFC3339))
			return nil
		},
	}
}

func newCronUpsertCmd() *cobra.Command {
	var payload string
	cmd := &cobra.Command{
		Use:   "upsert <name> <expression> <kind>",
		Short: "Create or update a schedule (validates expression)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			var payloadAny any
			if payload != "" {
				if err := json.Unmarshal([]byte(payload), &payloadAny); err != nil {
					return fmt.Errorf("--payload must be valid JSON: %w", err)
				}
			}
			store := jobs.NewCronStore(rt.pool.Pool)
			r, err := store.Upsert(cmd.Context(), args[0], args[1], args[2], payloadAny)
			if err != nil {
				return err
			}
			fmt.Printf("upserted %s; next_run_at=%s\n",
				r.Name, r.NextRunAt.Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&payload, "payload", "", "JSON payload")
	return cmd
}

func newCronDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a schedule by name (idempotent)",
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
			store := jobs.NewCronStore(rt.pool.Pool)
			if err := store.Delete(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Println("deleted")
			return nil
		},
	}
}

func newCronEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a schedule (next_run_at preserved)",
		Args:  cobra.ExactArgs(1),
		RunE:  cronToggle(true),
	}
}

func newCronDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Pause a schedule (no backfill on re-enable)",
		Args:  cobra.ExactArgs(1),
		RunE:  cronToggle(false),
	}
}

func cronToggle(enabled bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		rt, err := openRuntime(cmd.Context())
		if err != nil {
			return err
		}
		defer rt.cleanup()
		if err := applySysMigrations(cmd.Context(), rt); err != nil {
			return err
		}
		store := jobs.NewCronStore(rt.pool.Pool)
		if err := store.SetEnabled(cmd.Context(), args[0], enabled); err != nil {
			return err
		}
		if enabled {
			fmt.Println("enabled")
		} else {
			fmt.Println("disabled")
		}
		return nil
	}
}

func newCronRunNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-now <name>",
		Short: "Materialise one job from the schedule now; next_run_at unchanged",
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
			store := jobs.NewCronStore(rt.pool.Pool)
			id, ok, err := store.RunNow(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("no-op: schedule missing or disabled")
				return nil
			}
			fmt.Printf("enqueued job %s\n", id)
			return nil
		},
	}
}
