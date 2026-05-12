package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/openapi"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/sdkgen"
	"github.com/railbase/railbase/internal/sdkgen/ts"
)

// newGenerateCmd assembles the `railbase generate ...` subtree.
//
// v0.7 surface: `generate sdk [--out path] [--lang ts] [--check]`.
// v1.7.1 adds: `generate openapi [--out openapi.json] [--server <url>]`.
// Future: `generate mock-server`, additional language targets (Swift,
// Kotlin, Dart per docs/11).
func newGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Code-generation utilities (TS SDK, OpenAPI, schema-json, ...)",
	}
	cmd.AddCommand(newGenerateSdkCmd())
	cmd.AddCommand(newGenerateOpenAPICmd())
	cmd.AddCommand(newGenerateSchemaJSONCmd())
	return cmd
}

// newGenerateSchemaJSONCmd writes the registered schema as a JSON
// document — designed for LLM tooling (Claude, GPT, Cursor) that wants
// to read the schema to suggest new collections, queries, or hooks.
//
// docs/17 #125 + docs/15 §LLM-tooling. Distinct from OpenAPI (which
// describes the HTTP surface) and SDK (which is typed client code) —
// schema-json is a flat description of every collection's fields,
// rules, and metadata. Pure Go struct → JSON; no transformation.
//
// `--check` is a drift gate that compares the on-disk file's
// `schema_hash` against the live registry, same shape as `generate sdk`.
func newGenerateSchemaJSONCmd() *cobra.Command {
	var out string
	var check bool
	cmd := &cobra.Command{
		Use:   "schema-json",
		Short: "Generate a JSON description of the registered schema (for LLM tooling)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs := registry.Specs()
			if len(specs) == 0 {
				return fmt.Errorf("generate schema-json: no collections registered — import your schema package from main.go")
			}
			liveHash, err := sdkgen.SchemaHash(specs)
			if err != nil {
				return err
			}
			doc := map[string]any{
				"schema_hash":  liveHash,
				"generated_at": nowUTC(),
				"collections":  specs,
			}

			if check {
				if out == "" {
					return fmt.Errorf("generate schema-json --check: --out path required")
				}
				body, err := os.ReadFile(out)
				if err != nil {
					return fmt.Errorf("generate schema-json --check: %w (run without --check to create)", err)
				}
				var on struct {
					SchemaHash string `json:"schema_hash"`
				}
				if err := json.Unmarshal(body, &on); err != nil {
					return fmt.Errorf("generate schema-json --check: parse %s: %w", out, err)
				}
				if on.SchemaHash != liveHash {
					return fmt.Errorf("generate schema-json --check: drift detected\n  live: %s\n  disk: %s\n  → run `railbase generate schema-json --out %s` and commit",
						liveHash, on.SchemaHash, out)
				}
				fmt.Printf("OK    schema-json in sync with live schema (%s)\n", liveHash)
				return nil
			}

			body, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return fmt.Errorf("generate schema-json: marshal: %w", err)
			}
			if out == "" || out == "-" {
				_, err = os.Stdout.Write(body)
				if err == nil {
					_, _ = os.Stdout.Write([]byte("\n"))
				}
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return fmt.Errorf("generate schema-json: mkdir: %w", err)
			}
			if err := os.WriteFile(out, append(body, '\n'), 0o644); err != nil {
				return fmt.Errorf("generate schema-json: write: %w", err)
			}
			fmt.Fprintf(os.Stderr, "OK    %d collections (%s) → %s\n", len(specs), liveHash, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "",
		"Output path (default stdout; - for explicit stdout)")
	cmd.Flags().BoolVar(&check, "check", false,
		"Verify on-disk schema_hash matches live registry; non-zero exit on drift")
	return cmd
}

// nowUTC returns the current time in RFC3339-zulu shape for the
// schema-json document's `generated_at` field.
func nowUTC() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// newGenerateOpenAPICmd writes an OpenAPI 3.1 specification for the
// registered schema. Static generation — no runtime / server is
// required; the schema lives in Go, we serialise it to JSON.
//
// `--check` is a drift gate that compares the on-disk spec's
// `x-railbase.schemaHash` against the live registry hash. CI uses it
// the same way `generate sdk --check` works.
func newGenerateOpenAPICmd() *cobra.Command {
	var out, serverURL, title, description string
	var check bool

	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "Generate an OpenAPI 3.1 spec from the registered schema",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs := registry.Specs()
			if len(specs) == 0 {
				return fmt.Errorf("generate openapi: no collections registered — import your schema package from main.go")
			}

			abs, err := filepath.Abs(out)
			if err != nil {
				return fmt.Errorf("generate openapi: resolve out: %w", err)
			}

			if check {
				live, err := sdkgen.SchemaHash(specs)
				if err != nil {
					return err
				}
				disk, err := readOpenAPIHash(abs)
				if err != nil {
					return fmt.Errorf("generate openapi --check: %w (run `railbase generate openapi` to create it)", err)
				}
				if live != disk {
					return fmt.Errorf("generate openapi --check: schema drift detected\n  live:  %s\n  disk:  %s\n  → run `railbase generate openapi` and commit the result", live, disk)
				}
				fmt.Printf("OK    OpenAPI in sync with live schema (%s)\n", live)
				return nil
			}

			body, err := openapi.EmitJSON(specs, openapi.Options{
				Title:       title,
				Description: description,
				ServerURL:   serverURL,
			})
			if err != nil {
				return fmt.Errorf("generate openapi: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return fmt.Errorf("generate openapi: mkdir: %w", err)
			}
			if err := os.WriteFile(abs, body, 0o644); err != nil {
				return fmt.Errorf("generate openapi: write: %w", err)
			}
			fmt.Printf("Wrote OpenAPI 3.1 spec (%d bytes) to %s\n", len(body), abs)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "./openapi.json", "Output file path.")
	cmd.Flags().StringVar(&serverURL, "server", "", "Server URL embedded in the spec (default `http://localhost:8090`).")
	cmd.Flags().StringVar(&title, "title", "", "Spec `info.title` (default `Railbase API`).")
	cmd.Flags().StringVar(&description, "description", "", "Spec `info.description`.")
	cmd.Flags().BoolVar(&check, "check", false, "Exit non-zero if the on-disk spec is out of sync with the registered schema.")
	return cmd
}

// readOpenAPIHash extracts `x-railbase.schemaHash` from a previously
// written OpenAPI document. We don't round-trip through the openapi
// package's Spec type so future field additions (under x-railbase or
// elsewhere) don't break old --check runs.
func readOpenAPIHash(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var m struct {
		XRailbase struct {
			SchemaHash string `json:"schemaHash"`
		} `json:"x-railbase"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if m.XRailbase.SchemaHash == "" {
		return "", fmt.Errorf("%s: missing x-railbase.schemaHash", path)
	}
	return m.XRailbase.SchemaHash, nil
}

// newGenerateSdkCmd writes a typed client SDK from the registered
// schema. Defaults to `./client/` (relative to cwd) and TypeScript;
// other languages will plug in here as their sub-packages land.
//
// `--check` exits non-zero if the live registry's schemaHash differs
// from the on-disk `_meta.json`. Useful in CI: a developer who
// changed the schema without regenerating the SDK fails the build.
func newGenerateSdkCmd() *cobra.Command {
	var outDir, lang string
	var check bool

	cmd := &cobra.Command{
		Use:   "sdk",
		Short: "Generate a typed client SDK from the registered schema",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs := registry.Specs()
			if len(specs) == 0 {
				return fmt.Errorf("generate sdk: no collections registered — import your schema package from main.go")
			}

			abs, err := filepath.Abs(outDir)
			if err != nil {
				return fmt.Errorf("generate sdk: resolve out: %w", err)
			}

			if check {
				live, err := sdkgen.SchemaHash(specs)
				if err != nil {
					return err
				}
				disk, err := readMetaHash(abs)
				if err != nil {
					return fmt.Errorf("generate sdk --check: %w (run `railbase generate sdk` to create it)", err)
				}
				if live != disk {
					return fmt.Errorf("generate sdk --check: schema drift detected\n  live:  %s\n  disk:  %s\n  → run `railbase generate sdk` and commit the result", live, disk)
				}
				fmt.Printf("OK    SDK in sync with live schema (%s)\n", live)
				return nil
			}

			switch lang {
			case "ts", "typescript":
				files, err := ts.Generate(specs, ts.Options{OutDir: abs})
				if err != nil {
					return err
				}
				fmt.Printf("Wrote %d files to %s:\n", len(files), abs)
				for _, f := range files {
					fmt.Printf("  %s\n", f)
				}
				return nil
			default:
				return fmt.Errorf("generate sdk: --lang %q not supported (only \"ts\" in v0.7)", lang)
			}
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "./client", "Output directory (relative to cwd or absolute).")
	cmd.Flags().StringVar(&lang, "lang", "ts", "Target language (only \"ts\" in v0.7).")
	cmd.Flags().BoolVar(&check, "check", false, "Exit non-zero if the on-disk SDK is out of sync with the registered schema.")
	return cmd
}

// readMetaHash pulls the schemaHash field out of a previously-written
// _meta.json without round-tripping through the full Meta type
// (forward compat: future versions may add fields we don't know about).
func readMetaHash(outDir string) (string, error) {
	path := filepath.Join(outDir, "_meta.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var m struct {
		SchemaHash string `json:"schemaHash"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if m.SchemaHash == "" {
		return "", fmt.Errorf("%s: missing schemaHash", path)
	}
	return m.SchemaHash, nil
}
