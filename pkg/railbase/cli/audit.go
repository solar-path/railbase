package cli

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/audit"
)

// newAuditCmd assembles the `railbase audit ...` subtree.
//
// v0.6 surface: `audit verify` walks the chain end-to-end and
// reports the first break. v1.x layers on:
//   - `audit verify` also verifies Ed25519 seals when present.
//   - `audit seal-keygen` writes a fresh keypair to
//     `<dataDir>/.audit_seal_key` (chmod 0600). Refuses to overwrite
//     without --force so operators don't accidentally invalidate
//     existing seals (the verify path uses each seal's persisted
//     public_key, so historical seals stay verifiable — but new seals
//     would shift to the new key, which is usually NOT what an
//     operator wants without thought).
func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Inspect the append-only audit log",
	}
	cmd.AddCommand(newAuditVerifyCmd())
	cmd.AddCommand(newAuditSealKeygenCmd())
	return cmd
}

func newAuditVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Walk the hash chain and report the first row whose hash doesn't match; also verify Ed25519 seals when present",
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
			w := audit.NewWriter(rt.pool.Pool)
			n, err := w.Verify(cmd.Context())
			if err != nil {
				var ce *audit.ChainError
				if errors.As(err, &ce) {
					fmt.Printf("FAIL  verified %d rows; chain breaks at seq=%d (%s)\n",
						n, ce.Seq, ce.Reason)
					return err
				}
				return err
			}
			fmt.Printf("OK    %d rows verified\n", n)

			// Seal verification is best-effort: when no seal key exists
			// (fresh deployment, or operator never opted in) we report
			// that fact rather than failing — the chain check above is
			// still the primary integrity statement.
			sealer, sealErr := audit.NewSealer(audit.SealerOptions{
				Pool:       rt.pool.Pool,
				KeyPath:    filepath.Join(rt.cfg.DataDir, ".audit_seal_key"),
				Production: rt.cfg.ProductionMode,
			})
			if sealErr != nil {
				fmt.Printf("SKIP  seal verification: %v\n", sealErr)
				return nil
			}
			sealsVerified, err := sealer.Verify(cmd.Context())
			if err != nil {
				var sve *audit.SealVerificationError
				if errors.As(err, &sve) {
					fmt.Printf("FAIL  seal verification: %s (id=%s)\n", sve.Reason, sve.SealID)
					return err
				}
				return err
			}
			if sealsVerified == 0 {
				fmt.Println("OK    0 seals (none recorded yet)")
			} else {
				fmt.Printf("OK    %d seals, all signatures valid\n", sealsVerified)
			}
			return nil
		},
	}
}

// newAuditSealKeygenCmd writes `<dataDir>/.audit_seal_key`. Refuses
// to overwrite unless --force; an operator passing --force has chosen
// to retire the old key. Historical seals stay verifiable because
// each seal stores its public_key column inline.
func newAuditSealKeygenCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "seal-keygen",
		Short: "Generate (or replace with --force) the Ed25519 keypair used by the audit_seal builtin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			path := filepath.Join(rt.cfg.DataDir, ".audit_seal_key")
			pub, err := audit.GenerateSealKey(path, force)
			if err != nil {
				return err
			}
			fmt.Printf("OK    wrote %s\n", path)
			fmt.Printf("PUB   %s\n", hex.EncodeToString(pub))
			fmt.Println("HINT  back up the private key out-of-band; rotating requires --force and DOES NOT invalidate past seals (each seal stores its public_key inline).")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing key file (new seals will use the new key; existing seals remain verifiable via their inline public_key)")
	return cmd
}
