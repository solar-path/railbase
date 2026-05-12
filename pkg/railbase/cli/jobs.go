package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/jobs"
)

// newJobsCmd assembles `railbase jobs ...`.
//
// Commands:
//
//	jobs list [--status pending|running|completed|failed|cancelled] [--limit N]
//	jobs show <id>
//	jobs cancel <id>
//	jobs run-now <id>      — set run_after = now() on a pending row
//	jobs reset <id>        — failed/cancelled → pending, attempts=0
//	jobs recover           — sweep stuck running rows back to pending
//	jobs enqueue <kind> [--payload JSON] [--queue Q] [--max-attempts N]
func newJobsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Inspect + manage the background jobs queue",
	}
	cmd.AddCommand(
		newJobsListCmd(),
		newJobsShowCmd(),
		newJobsCancelCmd(),
		newJobsRunNowCmd(),
		newJobsResetCmd(),
		newJobsRecoverCmd(),
		newJobsEnqueueCmd(),
	)
	return cmd
}

func newJobsListCmd() *cobra.Command {
	var status string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent jobs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := jobs.NewStore(rt.pool.Pool)
			rows, err := store.List(cmd.Context(), jobs.Status(status), limit)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tQUEUE\tSTATUS\tATTEMPTS\tRUN_AFTER\tCREATED")
			for _, j := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\n",
					j.ID, j.Kind, j.Queue, j.Status,
					j.Attempts, j.MaxAttempts,
					j.RunAfter.Format(time.RFC3339),
					j.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status: pending|running|completed|failed|cancelled")
	cmd.Flags().IntVar(&limit, "limit", 50, "row limit (default 50, max 500)")
	return cmd
}

func newJobsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one job with payload + error detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := jobs.NewStore(rt.pool.Pool)
			j, err := store.Get(cmd.Context(), id)
			if err != nil {
				return err
			}
			fmt.Printf("Job %s\n", j.ID)
			fmt.Printf("  kind:         %s\n", j.Kind)
			fmt.Printf("  queue:        %s\n", j.Queue)
			fmt.Printf("  status:       %s\n", j.Status)
			fmt.Printf("  attempts:     %d / %d\n", j.Attempts, j.MaxAttempts)
			fmt.Printf("  run_after:    %s\n", j.RunAfter.Format(time.RFC3339))
			fmt.Printf("  created_at:   %s\n", j.CreatedAt.Format(time.RFC3339))
			if j.StartedAt != nil {
				fmt.Printf("  started_at:   %s\n", j.StartedAt.Format(time.RFC3339))
			}
			if j.CompletedAt != nil {
				fmt.Printf("  completed_at: %s\n", j.CompletedAt.Format(time.RFC3339))
			}
			if j.LockedBy != nil {
				fmt.Printf("  locked_by:    %s\n", *j.LockedBy)
			}
			if j.LockedUntil != nil {
				fmt.Printf("  locked_until: %s\n", j.LockedUntil.Format(time.RFC3339))
			}
			if j.CronID != nil {
				fmt.Printf("  cron_id:      %s\n", j.CronID)
			}
			if j.LastError != "" {
				fmt.Printf("  last_error:   %s\n", j.LastError)
			}
			fmt.Printf("  payload:      %s\n", string(j.Payload))
			return nil
		},
	}
}

func newJobsCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a pending or running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := jobs.NewStore(rt.pool.Pool)
			ok, err := store.Cancel(cmd.Context(), id)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("no-op: job is not pending or running")
				return nil
			}
			fmt.Println("cancelled")
			return nil
		},
	}
}

func newJobsRunNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-now <id>",
		Short: "Skip the backoff and re-eligible a pending job immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := jobs.NewStore(rt.pool.Pool)
			ok, err := store.RunNow(cmd.Context(), id)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("no-op: job is not pending")
				return nil
			}
			fmt.Println("run_after set to now()")
			return nil
		},
	}
}

func newJobsResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset <id>",
		Short: "Reset a failed/cancelled job to pending, attempts=0",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := jobs.NewStore(rt.pool.Pool)
			ok, err := store.Reset(cmd.Context(), id)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("no-op: job is not failed or cancelled")
				return nil
			}
			fmt.Println("reset to pending")
			return nil
		},
	}
}

func newJobsRecoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover",
		Short: "Sweep stuck 'running' jobs (locked_until elapsed) back to pending",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			store := jobs.NewStore(rt.pool.Pool)
			n, err := store.Recover(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("recovered %d row(s)\n", n)
			return nil
		},
	}
}

func newJobsEnqueueCmd() *cobra.Command {
	var queue, payload string
	var maxAttempts int
	var delay time.Duration
	cmd := &cobra.Command{
		Use:   "enqueue <kind>",
		Short: "Enqueue a job by handler kind",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[0]
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
			store := jobs.NewStore(rt.pool.Pool)
			j, err := store.Enqueue(cmd.Context(), kind, payloadAny, jobs.EnqueueOptions{
				Queue:       queue,
				MaxAttempts: maxAttempts,
				Delay:       delay,
			})
			if err != nil {
				return err
			}
			fmt.Println(j.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&queue, "queue", "", "queue name (default \"default\")")
	cmd.Flags().StringVar(&payload, "payload", "", "JSON payload")
	cmd.Flags().IntVar(&maxAttempts, "max-attempts", 0, "max attempts before terminal fail (default 5)")
	cmd.Flags().DurationVar(&delay, "delay", 0, "delay before first eligible run (e.g. 30s, 5m)")
	return cmd
}

// Compile-time interface check so `strconv` import stays used even if
// a future refactor drops the parse path.
var _ = strconv.Itoa
