//go:build embed_pg

// v1.4.5 domain-types E2E (slice 3: content). Boots embedded Postgres,
// registers a `themes` collection with color + cron + markdown fields,
// drives CRUD через REST and asserts:
//
//  1. Color: "#ABC" → "#aabbcc" canonicalisation
//  2. Color: "FF5733" (no #) → "#ff5733"
//  3. Color: bad input → 400
//  4. Cron: whitespace-collapsed valid expression round-trips
//  5. Cron: bad expression → 400
//  6. Markdown: passes through verbatim
//  7. DB CHECK rejects raw bad color even if app layer bypassed
//  8. Filter on color column works (TEXT equality)

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

func TestContentTypesE2E(t *testing.T) {
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

	themes := schemabuilder.NewCollection("themes").
		Field("name", schemabuilder.NewText().Required()).
		Field("accent", schemabuilder.NewColor().Required()).
		Field("schedule", schemabuilder.NewCron()).
		Field("notes", schemabuilder.NewMarkdown())
	registry.Reset()
	registry.Register(themes)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(themes.Spec())); err != nil {
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

	// === [1] Color shorthand expansion ===
	status, r1 := doJSON("POST", "/api/collections/themes/records", map[string]any{
		"name":    "Sunset",
		"accent": "#ABC",
		"notes":   "Warm tones",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	if r1["accent"] != "#aabbcc" {
		t.Errorf("[1] color shorthand: got %v, want #aabbcc", r1["accent"])
	}
	t.Logf("[1] color shorthand: #ABC → %v", r1["accent"])

	// === [2] Color missing-hash + uppercase ===
	status, r2 := doJSON("POST", "/api/collections/themes/records", map[string]any{
		"name":    "Forest",
		"accent": "FF5733",
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, r2)
	}
	if r2["accent"] != "#ff5733" {
		t.Errorf("[2] missing-# normalisation: got %v, want #ff5733", r2["accent"])
	}
	t.Logf("[2] missing-#: FF5733 → %v", r2["accent"])

	// === [3] Color bad input rejected ===
	status, _ = doJSON("POST", "/api/collections/themes/records", map[string]any{
		"name":    "Invalid",
		"accent": "not-a-color",
	})
	if status != 400 {
		t.Errorf("[3] bad color: expected 400, got %d", status)
	}
	t.Logf("[3] bad color rejected with %d", status)

	// === [4] Cron valid expression with whitespace ===
	status, r4 := doJSON("POST", "/api/collections/themes/records", map[string]any{
		"name":     "Daily",
		"accent":  "#000000",
		"schedule": "0  9-17  *  *  1-5", // double-spaces should collapse
	})
	if status != 200 {
		t.Fatalf("[4] create: %d %v", status, r4)
	}
	if r4["schedule"] != "0 9-17 * * 1-5" {
		t.Errorf("[4] cron whitespace-collapsed: got %v, want '0 9-17 * * 1-5'", r4["schedule"])
	}
	t.Logf("[4] cron normalised: %v", r4["schedule"])

	// === [5] Cron invalid expression ===
	status, _ = doJSON("POST", "/api/collections/themes/records", map[string]any{
		"name":     "Broken",
		"accent":  "#000000",
		"schedule": "every minute",
	})
	if status != 400 {
		t.Errorf("[5] bad cron: expected 400, got %d", status)
	}
	t.Logf("[5] bad cron rejected with %d", status)

	// === [6] Markdown passes through ===
	mdSource := "# Header\n\n- bullet 1\n- bullet 2\n\n**bold** _italic_"
	status, r6 := doJSON("POST", "/api/collections/themes/records", map[string]any{
		"name":    "Documented",
		"accent": "#000000",
		"notes":   mdSource,
	})
	if status != 200 {
		t.Fatalf("[6] create: %d %v", status, r6)
	}
	if r6["notes"] != mdSource {
		t.Errorf("[6] markdown not verbatim: got %q", r6["notes"])
	}
	t.Logf("[6] markdown verbatim (%d chars)", len(mdSource))

	// === [7] DB CHECK enforces canonical color ===
	_, err = pool.Exec(ctx, `INSERT INTO themes (name, accent) VALUES ('Raw', 'NOT-HEX')`)
	if err == nil {
		t.Errorf("[7] DB CHECK should reject raw bad color")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[7] unexpected error class: %v", err)
	} else {
		t.Logf("[7] DB CHECK enforces color (%s)", firstLineContent(err.Error()))
	}

	// === [8] Filter on color works ===
	q := url.Values{}
	q.Set("filter", `accent = '#aabbcc'`)
	status, list := doJSON("GET", "/api/collections/themes/records?"+q.Encode(), nil)
	if status != 200 {
		t.Fatalf("[8] list: %d %v", status, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[8] expected 1 hit, got %d", len(items))
	}
	t.Logf("[8] filter by color found %d row", len(items))

	t.Log("Content types E2E: 8/8 checks passed")
}

func firstLineContent(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
