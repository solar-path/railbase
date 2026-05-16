package cli

// v1.7.8 — `railbase import schema --from-pb <url>` translates a
// remote PocketBase v0.22+ collection schema into Railbase Go source.
// v1.7.19 — `railbase import data <collection> --file <path.csv>` bulk-
// loads CSV rows into a registered collection via Postgres COPY FROM.
//
// `import schema` does NOT touch the local DB — it's a pure
// stdout/file generator. `import data` DOES write to the local DB
// (operators take backups before running it on production).

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/pbimport"
)

func newImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import schema or data from external sources",
		Long: `Subcommands:
  schema   Translate a remote PocketBase schema into Railbase Go code
  data     Bulk-load CSV rows into a registered collection`,
	}
	cmd.AddCommand(newImportSchemaCmd())
	cmd.AddCommand(newImportDataCmd())
	return cmd
}

func newImportSchemaCmd() *cobra.Command {
	var (
		fromPB    string
		token     string
		out       string
		pkgName   string
	)
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Translate a remote PocketBase schema into Railbase Go code",
		Long: `Fetches /api/collections from --from-pb and emits Go source using
the Railbase schema builder. Operators drop the output file into
their project and ` + "`import _`" + ` from main.

The translation is conservative: unsupported field types fall back to
schema.JSON() with a TODO comment; rules are copied verbatim with a
"// TODO: verify PB filter syntax" hint. System and view collections
are skipped.

Example:

    railbase import schema \
      --from-pb https://my.pocketbase.io \
      --token "$PB_ADMIN_TOKEN" \
      --out internal/schema/collections.go`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromPB == "" {
				return fmt.Errorf("--from-pb is required")
			}
			list, err := pbimport.Fetch(cmd.Context(), pbimport.FetchOptions{
				BaseURL: fromPB,
				Token:   token,
			})
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			var w *os.File
			if out == "" || out == "-" {
				w = os.Stdout
			} else {
				w, err = os.Create(out)
				if err != nil {
					return fmt.Errorf("create %s: %w", out, err)
				}
				defer w.Close()
			}
			if err := pbimport.Emit(w, list, pbimport.EmitOptions{
				Package: pkgName,
				Source:  fromPB,
			}); err != nil {
				return fmt.Errorf("emit: %w", err)
			}
			if out != "" && out != "-" {
				// Stderr so piping `--out -` stays clean.
				fmt.Fprintf(os.Stderr, "OK    %d collections translated → %s\n",
					countNonSystem(list), out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fromPB, "from-pb", "",
		"PocketBase instance root URL (required)")
	cmd.Flags().StringVar(&token, "token", "",
		"Admin auth token (Authorization header value)")
	cmd.Flags().StringVar(&out, "out", "",
		"Output file path (default stdout; - for explicit stdout)")
	cmd.Flags().StringVar(&pkgName, "package", "schema",
		"Go package name for the emitted file")
	return cmd
}

// countNonSystem reports how many non-system collections we'd emit —
// used only for the operator-facing OK line so the count matches the
// "real" surface, not the PB system-collection count.
func countNonSystem(list *pbimport.CollectionsList) int {
	n := 0
	for _, c := range list.Items {
		if !c.System {
			n++
		}
	}
	return n
}

// newImportDataCmd ships `railbase import data <collection> --file
// <path.csv>` per docs/17 v1 SHIP test-debt. Bulk loads CSV into a
// registered collection via Postgres `COPY FROM STDIN` — the fastest
// path for >1k rows (single round-trip, server-side parsing, no
// per-row INSERT overhead).
//
// Trade-offs (documented for v1; can lift in v1.x without breaking the
// CLI contract):
//
//   - **Insert mode only.** Upsert / update modes deferred — they
//     require a staging table + `INSERT ... ON CONFLICT` dance; the
//     v1 contract is "load fresh data into an empty or append-only
//     table". Operators wanting upsert chain the CLI with `psql`
//     today.
//   - **No client-side validation.** Postgres column types + CHECK
//     constraints do the work; bad rows surface as a single COPY
//     error from PG (line number included). This matches the design
//     trade-off of v1.6.6 export-CLI: trust the schema layer.
//   - **Header row required.** First CSV line must list field names
//     matching the collection's columns. Unknown header → error
//     BEFORE we touch the DB.
//   - **No tenant injection.** Operators wanting tenant scoping
//     include a `tenant_id` column in the CSV. The CLI runs as the
//     binary owner; RLS / RBAC don't apply (same surface contract as
//     `railbase export collection`).
//   - **Gzipped CSV auto-detected** via the `.csv.gz` suffix — useful
//     for streaming large dumps from object stores.
func newImportDataCmd() *cobra.Command {
	var (
		filePath  string
		delimiter string
		nullStr   string
		quote     string
		header    bool
	)
	cmd := &cobra.Command{
		Use:   "data <collection>",
		Short: "Bulk-load CSV rows into a registered collection via COPY FROM",
		Long: `Reads a CSV file and bulk-inserts rows into the named collection.
Uses Postgres COPY FROM STDIN — fastest path for >1k rows.

Header row REQUIRED: first line lists the collection's field names.
Unknown headers fail BEFORE the DB is touched, so a typo can't
half-load. Server-side type coercion + CHECK constraints validate
each row; a bad row aborts the entire COPY (no partial commit).

Column-type cheatsheet for CSV cells:

  - Number / Int            → bare digits, no thousand separators: 42, -7, 1234
  - Bool                     → true / false (also 1 / 0)
  - Date / DateTime          → ISO-8601: 2026-05-16, 2026-05-16T01:13:58Z
  - Tags / Relations (M2M)   → Postgres array literal: "{tag1,tag2,tag3}"
                               Quote-wrap if any tag contains commas/spaces.
                               FEEDBACK #B10 — the array-literal shape is what
                               COPY FROM expects for text[] columns; an embedder
                               passing "tag1,tag2" gets a 3-column row, not a tag list.
  - JSON / Translatable      → quoted JSON object: "{""key"":""val""}"
  - File                     → import via REST multipart, not CSV
  - NULL                     → use --null '\\N' (or whatever sentinel you pick)

Examples:

    railbase import data posts --file posts.csv
    railbase import data orders --file orders.csv.gz --delimiter ';'
    railbase import data items  --file items.tsv  --delimiter $'\t'

    # Tags column ("{tag1,tag2}") inside a CSV — note the {} braces:
    #   id,title,tags
    #   uuid-1,Hello,"{tutorial,intro}"
    #   uuid-2,World,"{news}"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}
			if len(delimiter) != 1 {
				return fmt.Errorf("--delimiter must be a single character (got %q)", delimiter)
			}
			return runImportData(cmd, args[0], importDataOptions{
				filePath:  filePath,
				delimiter: delimiter[0],
				nullStr:   nullStr,
				quote:     quote,
				header:    header,
			})
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "",
		"CSV file path (.csv or .csv.gz; required)")
	cmd.Flags().StringVar(&delimiter, "delimiter", ",",
		"Field delimiter (single character; default ',')")
	cmd.Flags().StringVar(&nullStr, "null", "",
		"String that represents NULL in the CSV (default empty string)")
	cmd.Flags().StringVar(&quote, "quote", `"`,
		"Quote character (default '\"')")
	cmd.Flags().BoolVar(&header, "header", true,
		"First row is a header listing field names (default true)")
	return cmd
}
