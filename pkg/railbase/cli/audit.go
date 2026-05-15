package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	var target string
	var includeArchive bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Walk the audit hash chain(s) and verify Ed25519 seals",
		Long: `Walks the SHA-256 hash chain of the requested audit table and
reports the first row whose recomputed hash doesn't match the
persisted one. Also verifies Ed25519 seals when the audit_seal
builtin has signed any.

--target controls which chain(s) to walk:

  all     (default) walks legacy + site + every per-tenant chain
  legacy  walks _audit_log only (chain v1; pre-v3 deployments)
  site    walks _audit_log_site (system + admin actions, chain v2)
  tenant  walks every per-tenant chain in _audit_log_tenant

Non-zero exit code on the first chain break.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			switch target {
			case "all", "legacy", "site", "tenant":
				// ok
			default:
				return fmt.Errorf("--target must be one of: all, legacy, site, tenant (got %q)", target)
			}

			var firstErr error
			runLegacy := target == "all" || target == "legacy"
			runSite := target == "all" || target == "site"
			runTenant := target == "all" || target == "tenant"

			if runLegacy {
				w := audit.NewWriter(rt.pool.Pool)
				n, err := w.Verify(cmd.Context())
				if err != nil {
					var ce *audit.ChainError
					if errors.As(err, &ce) {
						fmt.Printf("FAIL  legacy: verified %d rows; chain breaks at seq=%d (%s)\n",
							n, ce.Seq, ce.Reason)
					} else {
						fmt.Printf("ERROR legacy: %v\n", err)
					}
					if firstErr == nil {
						firstErr = err
					}
				} else {
					fmt.Printf("OK    legacy: %d rows verified\n", n)
				}
			}

			if runSite || runTenant {
				store, err := audit.NewStore(cmd.Context(), rt.pool.Pool)
				if err != nil {
					return fmt.Errorf("audit verify: open store: %w", err)
				}

				if runSite {
					n, err := store.VerifySite(cmd.Context())
					if err != nil {
						var ce *audit.ChainError
						if errors.As(err, &ce) {
							fmt.Printf("FAIL  site: verified %d rows; chain breaks at seq=%d (%s)\n",
								n, ce.Seq, ce.Reason)
						} else {
							fmt.Printf("ERROR site: %v\n", err)
						}
						if firstErr == nil {
							firstErr = err
						}
					} else {
						fmt.Printf("OK    site: %d rows verified\n", n)
					}
				}

				if runTenant {
					results, err := store.VerifyAllTenants(cmd.Context())
					if err != nil {
						fmt.Printf("ERROR tenant enumeration: %v\n", err)
						if firstErr == nil {
							firstErr = err
						}
					} else if len(results) == 0 {
						fmt.Println("OK    tenant: 0 tenants with audit rows")
					} else {
						for _, r := range results {
							if r.Err != nil {
								var ce *audit.ChainError
								if errors.As(r.Err, &ce) {
									fmt.Printf("FAIL  tenant %s: verified %d rows; chain breaks at tenant_seq=%d (%s)\n",
										r.TenantID, r.Rows, ce.Seq, ce.Reason)
								} else {
									fmt.Printf("ERROR tenant %s: %v\n", r.TenantID, r.Err)
								}
								if firstErr == nil {
									firstErr = r.Err
								}
							} else {
								fmt.Printf("OK    tenant %s: %d rows verified\n", r.TenantID, r.Rows)
							}
						}
					}
				}
			}

			// Seal verification (Ed25519). Best-effort: fresh deploys or
			// operators who never opted into sealing report SKIP rather
			// than FAIL — the chain checks above are still the primary
			// integrity statement.
			sealer, sealErr := audit.NewSealer(audit.SealerOptions{
				Pool:       rt.pool.Pool,
				KeyPath:    filepath.Join(rt.cfg.DataDir, ".audit_seal_key"),
				Production: rt.cfg.ProductionMode,
			})
			if sealErr != nil {
				fmt.Printf("SKIP  seal verification: %v\n", sealErr)
				return firstErr
			}
			sealsVerified, err := sealer.Verify(cmd.Context())
			if err != nil {
				var sve *audit.SealVerificationError
				if errors.As(err, &sve) {
					fmt.Printf("FAIL  seal verification: %s (id=%s)\n", sve.Reason, sve.SealID)
					if firstErr == nil {
						firstErr = err
					}
				} else {
					fmt.Printf("ERROR seal verification: %v\n", err)
					if firstErr == nil {
						firstErr = err
					}
				}
			} else if sealsVerified == 0 {
				fmt.Println("OK    0 seals (none recorded yet)")
			} else {
				fmt.Printf("OK    %d seals, all signatures valid\n", sealsVerified)
			}

			// v3.x Phase 2 — archive verification. Walks every
			// .seal.json under <dataDir>/audit/ and re-reads the
			// paired .jsonl.gz to confirm row count + structural
			// integrity. The archive's chain hashes are signed by
			// the seals embedded in the manifest, so signature
			// validation here is a future enhancement (Phase 2.1).
			if includeArchive {
				dir := filepath.Join(rt.cfg.DataDir, "audit")
				count, totalRows, err := verifyArchiveDir(cmd.Context(), dir)
				if err != nil {
					fmt.Printf("ERROR archive verification: %v\n", err)
					if firstErr == nil {
						firstErr = err
					}
				} else if count == 0 {
					fmt.Println("OK    0 archive segments (none recorded yet)")
				} else {
					fmt.Printf("OK    %d archive segments, %d rows verified\n", count, totalRows)
				}
			}

			return firstErr
		},
	}
	cmd.Flags().StringVar(&target, "target", "all",
		"which chain(s) to verify: all | legacy | site | tenant")
	cmd.Flags().BoolVar(&includeArchive, "include-archive", false,
		"also walk gzipped archive files under <dataDir>/audit/")
	return cmd
}

// verifyArchiveDir walks <dataDir>/audit/ looking for .seal.json
// manifests, then calls audit.VerifyArchive on each. Returns the
// number of manifests verified + total rows.
func verifyArchiveDir(ctx context.Context, root string) (int, int64, error) {
	st, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if !st.IsDir() {
		return 0, 0, fmt.Errorf("%s is not a directory", root)
	}
	var count int
	var rows int64
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".seal.json") {
			return nil
		}
		n, vErr := audit.VerifyArchive(ctx, path)
		if vErr != nil {
			fmt.Printf("FAIL  archive %s: %v\n", path, vErr)
			return vErr
		}
		fmt.Printf("OK    archive %s: %d rows\n", path, n)
		count++
		rows += n
		return nil
	})
	return count, rows, walkErr
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
