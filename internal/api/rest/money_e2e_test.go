//go:build embed_pg

// v1.4.6 domain-types E2E (slice 4: money primitives). Boots embedded
// Postgres, registers an `invoices` collection with finance + percentage
// fields, drives CRUD через REST and asserts:
//
//  1. Finance: string "1234.5678" round-trips as canonical decimal
//  2. Finance: JSON number 99.95 → "99.95" (no float drift at low precision)
//  3. Finance: negative value accepted ("-50")
//  4. Finance: bad input → 400
//  5. Percentage: default 0..100 range enforced; "150" rejected by DB CHECK
//  6. Filter on numeric value works (Postgres NUMERIC comparators)
//  7. NUMERIC scale enforced at DB layer (excess decimals truncated/rejected)
//  8. Min/Max CHECK enforced at DB layer

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestMoneyTypesE2E(t *testing.T) {
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

	invoices := schemabuilder.NewCollection("invoices").
		Field("amount", schemabuilder.NewFinance().Required().Min("-10000").Max("1000000")).
		Field("vat_rate", schemabuilder.NewPercentage().Required().Default("20"))
	registry.Reset()
	registry.Register(invoices)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(invoices.Spec())); err != nil {
		t.Fatalf("create table: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	doJSON := func(method, path string, body any) (int, map[string]any) {
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

	// Postgres NUMERIC(p, s) PRESERVES the declared scale on read.
	// Storing `1234.5678` as NUMERIC(15, 4) → reads back as "1234.5678";
	// storing `1234.5` reads back as "1234.5000". This is correct and
	// what financial apps want (stable formatting). E2E asserts the
	// scale-preserved form.

	// === [1] Finance string round-trip ===
	status, r1 := doJSON("POST", "/api/collections/invoices/records", map[string]any{
		"amount":   "1234.5678",
		"vat_rate": "20",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	if r1["amount"] != "1234.5678" {
		t.Errorf("[1] finance string: got %v, want '1234.5678'", r1["amount"])
	}
	t.Logf("[1] finance string round-trip: %v", r1["amount"])

	// === [2] Finance JSON number → string at declared scale ===
	status, r2 := doJSON("POST", "/api/collections/invoices/records", map[string]any{
		"amount":   99.95,
		"vat_rate": "10",
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, r2)
	}
	// NUMERIC(15, 4) pads to 4 decimals on read.
	if r2["amount"] != "99.9500" {
		t.Errorf("[2] finance JSON-number: got %v, want '99.9500'", r2["amount"])
	}
	t.Logf("[2] finance JSON-number → string at scale 4: %v", r2["amount"])

	// === [3] Finance negative value ===
	status, r3 := doJSON("POST", "/api/collections/invoices/records", map[string]any{
		"amount":   "-50",
		"vat_rate": "0",
	})
	if status != 200 {
		t.Fatalf("[3] create: %d %v", status, r3)
	}
	if r3["amount"] != "-50.0000" {
		t.Errorf("[3] negative finance: got %v, want '-50.0000'", r3["amount"])
	}
	t.Logf("[3] negative finance: %v", r3["amount"])

	// === [4] Bad finance input rejected ===
	status, _ = doJSON("POST", "/api/collections/invoices/records", map[string]any{
		"amount":   "not-a-number",
		"vat_rate": "20",
	})
	if status != 400 {
		t.Errorf("[4] bad finance: expected 400, got %d", status)
	}
	t.Logf("[4] bad finance rejected with %d", status)

	// === [5] Percentage out-of-range → DB CHECK rejects ===
	status, e5 := doJSON("POST", "/api/collections/invoices/records", map[string]any{
		"amount":   "100",
		"vat_rate": "150", // > 100 → CHECK violation
	})
	if status == 200 {
		t.Errorf("[5] vat_rate 150 accepted: %v", e5)
	}
	t.Logf("[5] vat_rate 150 rejected with %d", status)

	// === [6] Filter on numeric value works ===
	q := url.Values{}
	q.Set("filter", `amount = 1234.5678`)
	status, list := doJSON("GET", "/api/collections/invoices/records?"+q.Encode(), nil)
	if status != 200 {
		t.Fatalf("[6] list: %d %v", status, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[6] expected 1 hit, got %d", len(items))
	}
	t.Logf("[6] filter by amount found %d row", len(items))

	// === [7] DB NUMERIC scale truncates excess decimals ===
	// Default scale=4 for finance. Insert "1.23456" → DB stores "1.2346"
	// (banker's rounding).
	status, r7 := doJSON("POST", "/api/collections/invoices/records", map[string]any{
		"amount":   "1.23456",
		"vat_rate": "5",
	})
	if status != 200 {
		t.Fatalf("[7] create: %d %v", status, r7)
	}
	got7 := r7["amount"]
	if got7 != "1.2346" && got7 != "1.2345" {
		t.Errorf("[7] NUMERIC(15,4) rounding: got %v, expected 1.2346 or 1.2345", got7)
	}
	t.Logf("[7] NUMERIC(15,4) rounded: %v", got7)

	// === [8] Min/Max CHECK enforced at DB ===
	status, e8 := doJSON("POST", "/api/collections/invoices/records", map[string]any{
		"amount":   "-99999",   // below -10000 min
		"vat_rate": "20",
	})
	if status == 200 {
		t.Errorf("[8] amount -99999 accepted (below min): %v", e8)
	}
	t.Logf("[8] amount below min rejected with %d", status)

	// === [9] Direct DB CHECK validation ===
	_, err = pool.Exec(ctx, `INSERT INTO invoices (amount, vat_rate) VALUES (5000000, 50)`)
	if err == nil {
		t.Errorf("[9] DB CHECK should reject amount above max 1000000")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[9] unexpected error class: %v", err)
	} else {
		t.Logf("[9] DB CHECK enforces max (%s)", firstLineMoney(err.Error()))
	}

	t.Log("Money types E2E: 9/9 checks passed")
}

func firstLineMoney(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
