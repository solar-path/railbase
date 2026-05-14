//go:build embed_pg

// v1.7.16 — docs/17 #13 pagination cursor stability.
//
// Asserts the LIST handler returns deterministic, complete pages under
// the documented ordering contract:
//
//   ORDER BY <user-sort> ... , id DESC   (created DESC, id DESC by default)
//
// Why the test matters:
//
//   - OFFSET-pagination is correct ONLY when the sort key is total AND
//     the table is stable between page requests. The existing default
//     appends `, id DESC` as a tie-breaker so two rows with identical
//     `created` timestamps still get a deterministic position. Without
//     that tie-break, page boundaries shift on replay → the same row
//     appears on two pages, or no page at all, EVEN WITH NO WRITES.
//
//   - This test seeds 100 rows that ALL share the same `created`
//     millisecond, forcing the tie-breaker to do work. If a future edit
//     drops the `, id DESC`, the page contents become PG-implementation
//     defined and this test starts seeing duplicates.
//
//   - Pagination IS NOT stable under concurrent head-inserts (OFFSET
//     pagination + new rows at the head shifts everything down by 1 →
//     the row at offset N is now at offset N+1; reading offset N+1 next
//     page re-reads it). That's documented as a known limitation. True
//     cursor stability under concurrent writes needs cursor mode
//     (`WHERE (created, id) < (last_created, last_id)`) which is
//     deferred to v1.1+. This test documents the limitation by
//     observing it, not asserting against it.
//
// Three scenarios:
//
//   (a) Deterministic ordering on identical `created`: paginate 100
//       rows in pages of 10; verify every seeded id appears exactly
//       once across the 10 pages. THIS is the cursor-stability property
//       OFFSET pagination CAN guarantee — given a total order.
//
//   (b) Stable repeated read: re-fetch page 1 multiple times; bytes
//       must be identical. Catches a flaky tie-breaker.
//
//   (c) Documented limitation under concurrent inserts: log (but don't
//       fail on) duplicate observations. This is the "we know OFFSET
//       has this property; here's a behavioural pin" guard.

package rest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestPaginationStability_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	// Sample collection: just `name` for human readability.
	items := schemabuilder.NewCollection("pgitems").PublicRules().
		Field("name", schemabuilder.NewText().Required())
	registry.Reset()
	registry.Register(items)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(items.Spec())); err != nil {
		t.Fatalf("create table: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// fetchPage returns the ids on one page. perPage=10 so 100 seeded
	// rows fit in exactly 10 pages.
	fetchPage := func(page int) []string {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/api/collections/pgitems/records?page="+itoa(page)+"&perPage=10", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("page %d: status %d body=%s", page, resp.StatusCode, b)
		}
		var out struct {
			Items []map[string]any `json:"items"`
		}
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("page %d unmarshal: %v body=%s", page, err, raw)
		}
		ids := make([]string, 0, len(out.Items))
		for _, it := range out.Items {
			if id, ok := it["id"].(string); ok {
				ids = append(ids, id)
			}
		}
		return ids
	}

	// === (a) No-writes scenario ===
	//
	// Bulk-insert 100 rows with identical `created` (now() resolves once
	// per statement, so a single multi-VALUES INSERT gives them all the
	// same created timestamp). Force the `, id DESC` tie-breaker to do
	// all the ordering work.
	t.Run("no_writes_full_traversal", func(t *testing.T) {
		// Wipe + reseed so this subtest is independent.
		if _, err := pool.Exec(ctx, "DELETE FROM pgitems"); err != nil {
			t.Fatal(err)
		}
		const seed = 100
		rows := make([]any, 0, seed)
		for i := 0; i < seed; i++ {
			rows = append(rows, "row-"+itoa(i))
		}
		// Single statement → single now() → all rows share `created` to the µs.
		// We use generate_series to make this efficient.
		_, err := pool.Exec(ctx, `
			INSERT INTO pgitems (name)
			SELECT 'row-' || g::text FROM generate_series(0, 99) AS g`)
		if err != nil {
			t.Fatal(err)
		}

		seen := map[string]int{}
		for page := 1; page <= 10; page++ {
			for _, id := range fetchPage(page) {
				seen[id]++
			}
		}
		if len(seen) != seed {
			t.Errorf("observed %d distinct ids, want %d", len(seen), seed)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("id %s observed %d times (want exactly 1)", id, n)
			}
		}

		// Re-fetch page 1 — must be byte-identical (same id set in same
		// order). Stronger than uniqueness: the tie-breaker must produce
		// stable ordering across repeated requests.
		first := fetchPage(1)
		second := fetchPage(1)
		if len(first) != len(second) {
			t.Fatalf("page 1 length flipped: %d → %d", len(first), len(second))
		}
		for i := range first {
			if first[i] != second[i] {
				t.Errorf("page 1 order changed at idx %d: %s → %s", i, first[i], second[i])
			}
		}
	})

	// === (b) Documented limitation: concurrent inserts at head ===
	//
	// Goroutine A: pages through the table (page 1..15 of 10 perPage).
	// Goroutine B: inserts 50 new rows with `created = now()` (later than
	// the original seed) interleaved with the pagination.
	//
	// Under OFFSET pagination, each new INSERT at the head shifts all
	// existing rows DOWN by one offset position. A row that was at
	// offset 9 (end of page 1) ends up at offset 10 (start of page 2);
	// reading page 2 next will re-read it. This is a KNOWN limitation
	// of OFFSET pagination; cursor mode (`WHERE (created, id) < ...`)
	// solves it but is deferred to v1.1+.
	//
	// This subtest LOGS duplicates rather than failing on them — the
	// test exists to PIN this behaviour so a future edit that introduces
	// proper cursor mode (or accidentally changes OFFSET semantics)
	// produces a visible regression signal in test output. The test
	// still FAILS if zero rows were observed (sanity bound).
	t.Run("documented_offset_limitation_under_inserts", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "DELETE FROM pgitems"); err != nil {
			t.Fatal(err)
		}
		// Seed 100 baseline rows.
		_, err := pool.Exec(ctx, `
			INSERT INTO pgitems (name)
			SELECT 'seed-' || g::text FROM generate_series(0, 99) AS g`)
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		seen := map[string]int{}
		var mu sync.Mutex

		// Inserter goroutine: 50 new rows, paced so they interleave with
		// pagination requests. Each insert gets a fresh now() so created
		// strictly increases.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := pool.Exec(ctx, `INSERT INTO pgitems (name) VALUES ($1)`,
					"concurrent-"+itoa(i)); err != nil {
					t.Errorf("concurrent insert %d: %v", i, err)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()

		// Pager goroutine: page 1..10. Larger pages than rows to ensure
		// we keep paging even as the table grows. Pages 11..15 catch
		// rows that landed at the head during the sweep.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for page := 1; page <= 15; page++ {
				for _, id := range fetchPage(page) {
					mu.Lock()
					seen[id]++
					mu.Unlock()
				}
				time.Sleep(7 * time.Millisecond)
			}
		}()
		wg.Wait()

		// Behavioural pin — count duplicates (do NOT fail). OFFSET
		// pagination + concurrent head-inserts SHIFTS rows across page
		// boundaries; the same row can land on two consecutive pages.
		// A future cursor-mode implementation would drive `dupes` to 0.
		mu.Lock()
		defer mu.Unlock()
		var dupes, total int
		for _, n := range seen {
			total += n
			if n > 1 {
				dupes += n - 1
			}
		}
		t.Logf("offset-pagination under concurrent inserts: observed=%d distinct=%d dupes=%d (dupes>0 is expected; cursor mode would drive to 0)",
			total, len(seen), dupes)
		if len(seen) == 0 {
			t.Errorf("observed 0 rows; something is broken in the paging path")
		}
	})

	// === (c) Default sort tie-breaker present ===
	//
	// Regression guard: the default ORDER BY MUST include `id DESC` as
	// the final tie-breaker. We assert this at the buildList level (see
	// queries_test.go for the unit test) AND here at the wire level by
	// confirming all 10 pages over identical-created rows are
	// exhaustive + unique — which is only possible with a total order.
	// The earlier "no_writes_full_traversal" subtest already enforces
	// this; this third subtest pins the SQL-shape contract explicitly so
	// a future edit that drops the tie-breaker fails this test with a
	// clear signal rather than silently flaking under offset shifts.
	t.Run("default_sort_includes_id_tiebreaker", func(t *testing.T) {
		spec := items.Spec()
		sql, _, _, _ := buildList(spec, listQuery{})
		if !contains(sql, "id DESC") {
			t.Errorf("default sort must end with `id DESC` for cursor stability; got: %s", sql)
		}
	})
}

// itoa is the tiny strconv.Itoa stand-in this file uses to avoid a
// per-test strconv import.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// contains is a tiny strings.Contains shim — same justification as itoa.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
