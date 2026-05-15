// Regression test for FEEDBACK #23 — `_stripe_products` now ships an
// `external_id TEXT` column with a partial unique index, so
// embedders maintaining their own product table (shopper-class
// upstream-id flow) can hand-roll an idempotent upsert against
// `_stripe_products`. The migration is part of the sys FS, so a
// fresh boot picks it up automatically.
//
// We can't easily run the SQL against a real Postgres from inside the
// migrate/sys package (no DB harness here), so we shape-check the
// migration body. The full-runtime guarantee comes from the e2e
// stripe tests downstream once they reach this slice.
package sys

import (
	"strings"
	"testing"
)

const migration0033 = "0033_stripe_products_external_id"

func TestMigration0033_Up_Shape(t *testing.T) {
	body, err := FS.ReadFile(migration0033 + ".up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	src := string(body)

	for _, want := range []string{
		// 1. The new column must be added to _stripe_products.
		"ALTER TABLE _stripe_products",
		"ADD COLUMN external_id TEXT",
		// 2. A unique index keyed on external_id.
		"CREATE UNIQUE INDEX uniq__stripe_products_external_id",
		"ON _stripe_products (external_id)",
		// 3. The partial-index predicate so NULL values don't collide.
		"WHERE external_id IS NOT NULL",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("migration 0033 missing %q\nbody:\n%s", want, src)
		}
	}

	// 4. Must NOT default external_id to anything — the column is
	//    opt-in. A DEFAULT '' would defeat the partial-index pattern
	//    by making every row collide on the empty string.
	if strings.Contains(strings.ToUpper(src), "DEFAULT ''") {
		t.Errorf("external_id must NOT have a DEFAULT; partial index requires NULLs:\n%s", src)
	}
}

func TestMigration0033_Down_Reversible(t *testing.T) {
	body, err := FS.ReadFile(migration0033 + ".down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	src := string(body)
	for _, want := range []string{
		"DROP INDEX IF EXISTS uniq__stripe_products_external_id",
		"ALTER TABLE _stripe_products DROP COLUMN IF EXISTS external_id",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("down migration missing %q\nbody:\n%s", want, src)
		}
	}
}

// TestMigration0033_NumberingContiguous — the migration is numbered
// 0033, one above the previous max (0032). If someone adds 0034 later
// and we forget to bump this test, fine — but at LEAST 0033 must
// exist and 0032 must still exist (no accidental delete).
func TestMigration0033_NumberingContiguous(t *testing.T) {
	for _, name := range []string{
		"0032_tenants.up.sql",
		"0033_stripe_products_external_id.up.sql",
	} {
		if _, err := FS.ReadFile(name); err != nil {
			t.Errorf("expected migration %q in sys FS: %v", name, err)
		}
	}
}
