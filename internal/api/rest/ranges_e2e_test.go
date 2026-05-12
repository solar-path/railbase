//go:build embed_pg

// v1.5.10 domain-types E2E (Quantities completion: date_range +
// time_range). Closes §3.8 Quantities group at 4/4. Asserts:
//
//  1. date_range object form → canonical [start,end) string
//  2. date_range string form round-trip
//  3. date_range start > end → 400
//  4. time_range HH:MM normalised to HH:MM:SS
//  5. time_range start > end → 400
//  6. time_range hour > 23 → 400
//  7. Postgres @> operator works on stored daterange (interop check)
//  8. DB CHECK rejects raw time_range start > end
//  9. DB CHECK rejects raw bad time shape (e.g. "9:00")
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

func TestRanges2E2E(t *testing.T) {
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

	events := schemabuilder.NewCollection("events").
		Field("dates", schemabuilder.NewDateRange().Required()).
		Field("hours", schemabuilder.NewTimeRange().Required())
	registry.Reset()
	registry.Register(events)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(events.Spec())); err != nil {
		t.Fatalf("create events: %v", err)
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

	// === [1] date_range object form → canonical string ===
	status, p := do("POST", "/api/collections/events/records", map[string]any{
		"dates": map[string]any{"start": "2024-01-01", "end": "2024-12-31"},
		"hours": map[string]any{"start": "09:00", "end": "17:00"},
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, p)
	}
	if p["dates"] != "[2024-01-01,2024-12-31)" {
		t.Errorf("[1] dates: got %v, want [2024-01-01,2024-12-31)", p["dates"])
	}
	id1, _ := p["id"].(string)
	t.Logf("[1] object form → canonical: dates=%v", p["dates"])

	// === [2] date_range string form round-trip ===
	status, p = do("POST", "/api/collections/events/records", map[string]any{
		"dates": "[2025-06-01,2025-08-31)",
		"hours": map[string]any{"start": "10:00", "end": "18:00"},
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, p)
	}
	if p["dates"] != "[2025-06-01,2025-08-31)" {
		t.Errorf("[2] dates: got %v", p["dates"])
	}
	t.Logf("[2] string form round-trip")

	// === [3] date_range start > end → 400 ===
	status, _ = do("POST", "/api/collections/events/records", map[string]any{
		"dates": map[string]any{"start": "2024-12-31", "end": "2024-01-01"},
		"hours": map[string]any{"start": "09:00", "end": "17:00"},
	})
	if status != 400 {
		t.Errorf("[3] reversed date_range: got %d, want 400", status)
	}
	t.Logf("[3] date_range start > end rejected")

	// === [4] time_range HH:MM normalised to HH:MM:SS ===
	status, p = do("GET", "/api/collections/events/records/"+id1, nil)
	if status != 200 {
		t.Fatal("[4] view")
	}
	hours, _ := p["hours"].(map[string]any)
	if hours["start"] != "09:00:00" || hours["end"] != "17:00:00" {
		t.Errorf("[4] time normalisation: got %v", hours)
	}
	t.Logf("[4] HH:MM → HH:MM:SS normalised: %v", hours)

	// === [5] time_range start > end → 400 ===
	status, _ = do("POST", "/api/collections/events/records", map[string]any{
		"dates": map[string]any{"start": "2024-01-01", "end": "2024-12-31"},
		"hours": map[string]any{"start": "17:00", "end": "09:00"},
	})
	if status != 400 {
		t.Errorf("[5] reversed time_range: got %d, want 400", status)
	}
	t.Logf("[5] time_range start > end rejected")

	// === [6] hour > 23 → 400 ===
	status, _ = do("POST", "/api/collections/events/records", map[string]any{
		"dates": map[string]any{"start": "2024-01-01", "end": "2024-12-31"},
		"hours": map[string]any{"start": "25:00", "end": "26:00"},
	})
	if status != 400 {
		t.Errorf("[6] hour > 23: got %d, want 400", status)
	}
	t.Logf("[6] hour=25 rejected")

	// === [7] Postgres @> operator works on stored daterange ===
	// SELECT dates @> '2024-06-15'::date FROM events — should be true
	// for id1 (the [2024-01-01,2024-12-31) record).
	var contains bool
	err = pool.QueryRow(ctx,
		`SELECT dates @> '2024-06-15'::date FROM events WHERE id = $1`, id1,
	).Scan(&contains)
	if err != nil {
		t.Fatalf("[7] daterange contains: %v", err)
	}
	if !contains {
		t.Error("[7] daterange should contain 2024-06-15")
	}
	t.Logf("[7] Postgres daterange @> operator works: 2024-06-15 ∈ [2024-01-01,2024-12-31)")

	// === [8] DB CHECK rejects raw time_range start > end ===
	_, err = pool.Exec(ctx,
		`INSERT INTO events (dates, hours) VALUES (
			'[2024-01-01,2024-12-31)'::daterange,
			'{"start":"17:00:00","end":"09:00:00"}'::jsonb
		)`)
	if err == nil {
		t.Error("[8] DB CHECK should reject reversed time_range")
	} else {
		t.Logf("[8] DB CHECK rejected raw reversed time: %v", err)
	}

	// === [9] DB CHECK rejects raw bad time shape ===
	_, err = pool.Exec(ctx,
		`INSERT INTO events (dates, hours) VALUES (
			'[2024-01-01,2024-12-31)'::daterange,
			'{"start":"9:00","end":"17:00"}'::jsonb
		)`)
	if err == nil {
		t.Error("[9] DB CHECK should reject bad time shape")
	} else {
		t.Logf("[9] DB CHECK rejected '9:00' shape: %v", err)
	}
}
