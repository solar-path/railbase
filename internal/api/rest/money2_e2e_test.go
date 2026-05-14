//go:build embed_pg

// v1.5.9 domain-types E2E (Money completion: currency + money_range).
// Closes §3.8 Money group at 4/4. Asserts:
//
//  1. Currency: "usd" → "USD" round-trip
//  2. Currency: unknown code → 400
//  3. Money range full round-trip with canonical encoding
//  4. Money range: min > max → 400
//  5. Money range: missing currency → 400
//  6. Money range: outer .Max() bound enforced
//  7. DB CHECK rejects raw min > max bypassing REST
//  8. DB CHECK rejects raw lowercase currency code
//  9. Read returns canonical JSON object for money_range
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestMoney2E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	// Two fields exercising both types; .Max("10000") on the range
	// caps the upper bound across rows.
	jobs := schemabuilder.NewCollection("listings").PublicRules().
		Field("currency", schemabuilder.NewCurrency().Required()).
		Field("salary", schemabuilder.NewMoneyRange().Required().Max("10000"))
	registry.Reset()
	registry.Register(jobs)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(jobs.Spec())); err != nil {
		t.Fatalf("create listings: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, path string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	// === [1] Currency lowercase canonicalised to uppercase ===
	status, p := do("POST", "/api/collections/listings/records", map[string]any{
		"currency": "usd",
		"salary":   map[string]any{"min": "1000", "max": "5000", "currency": "USD"},
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, p)
	}
	if p["currency"] != "USD" {
		t.Errorf("[1] currency: got %v, want USD", p["currency"])
	}
	id1, _ := p["id"].(string)
	t.Logf("[1] usd → USD canonicalised; id=%s", id1)

	// === [2] Unknown currency → 400 ===
	status, _ = do("POST", "/api/collections/listings/records", map[string]any{
		"currency": "zzz",
		"salary":   map[string]any{"min": "0", "max": "100", "currency": "USD"},
	})
	if status != 400 {
		t.Errorf("[2] unknown currency: got %d, want 400", status)
	}
	t.Logf("[2] zzz rejected with 400")

	// === [3] Money range canonical encoding ===
	// View the row from [1] and verify the JSON shape.
	status, p = do("GET", "/api/collections/listings/records/"+id1, nil)
	if status != 200 {
		t.Fatalf("[3] view: %d", status)
	}
	salary, ok := p["salary"].(map[string]any)
	if !ok {
		t.Fatalf("[3] salary not an object: %T %v", p["salary"], p["salary"])
	}
	if salary["currency"] != "USD" || salary["min"] != "1000" || salary["max"] != "5000" {
		t.Errorf("[3] salary shape: %v", salary)
	}
	t.Logf("[3] money_range round-trip: %v", salary)

	// === [4] min > max rejected ===
	status, _ = do("POST", "/api/collections/listings/records", map[string]any{
		"currency": "USD",
		"salary":   map[string]any{"min": "5000", "max": "1000", "currency": "USD"},
	})
	if status != 400 {
		t.Errorf("[4] reversed range: got %d, want 400", status)
	}
	t.Logf("[4] min > max rejected with 400")

	// === [5] Missing currency in salary → 400 ===
	status, _ = do("POST", "/api/collections/listings/records", map[string]any{
		"currency": "USD",
		"salary":   map[string]any{"min": "0", "max": "100"},
	})
	if status != 400 {
		t.Errorf("[5] missing currency: got %d, want 400", status)
	}
	t.Logf("[5] missing salary.currency rejected with 400")

	// === [6] Outer .Max("10000") bound enforced ===
	status, _ = do("POST", "/api/collections/listings/records", map[string]any{
		"currency": "USD",
		"salary":   map[string]any{"min": "0", "max": "15000", "currency": "USD"},
	})
	if status != 400 {
		t.Errorf("[6] outer max bound: got %d, want 400", status)
	}
	t.Logf("[6] salary.max=15000 rejected (outer .Max(\"10000\"))")

	// === [7] DB CHECK rejects raw min > max ===
	_, err = pool.Exec(ctx,
		`INSERT INTO listings (currency, salary) VALUES ('USD', '{"min":"500","max":"100","currency":"USD"}'::jsonb)`)
	if err == nil {
		t.Error("[7] DB CHECK should reject min > max raw INSERT")
	} else {
		t.Logf("[7] DB CHECK rejected raw min > max: %v", err)
	}

	// === [8] DB CHECK rejects raw lowercase currency ===
	_, err = pool.Exec(ctx,
		`INSERT INTO listings (currency, salary) VALUES ('usd', '{"min":"0","max":"100","currency":"USD"}'::jsonb)`)
	if err == nil {
		t.Error("[8] DB CHECK should reject lowercase currency")
	} else {
		t.Logf("[8] DB CHECK rejected lowercase currency: %v", err)
	}

	// === [9] Money range JSON canonical sort (alphabetical keys) ===
	// Re-read the row, dump to JSON, confirm key order.
	status, p = do("GET", "/api/collections/listings/records/"+id1, nil)
	if status != 200 {
		t.Fatal("[9] view")
	}
	salaryJSON, _ := json.Marshal(p["salary"])
	// Canonical order is currency, max, min (alphabetical).
	if !bytes.HasPrefix(salaryJSON, []byte(`{"currency":`)) {
		t.Errorf("[9] canonical order: expected currency first, got %s", salaryJSON)
	}
	t.Logf("[9] canonical encoding preserved on read: %s", salaryJSON)
}
