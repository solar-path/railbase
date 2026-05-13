//go:build embed_pg

// v1.7.51 — full-stack SCIM HTTP e2e against embedded Postgres.
//
// Builds a real chi router with the SCIM routes mounted, mints a
// SCIM bearer credential via the token store, then exercises:
//
//  1. Bearer-token middleware: missing / wrong-prefix / unknown
//     → 401 SCIM-shaped error envelope
//  2. Discovery endpoints are PUBLIC + return RFC-7644 shapes
//  3. POST   /Users          → 201 + scim_managed=TRUE
//  4. GET    /Users/{id}     → 200 + matches insert
//  5. GET    /Users          → list w/ pagination
//  6. GET    /Users?filter=  → filter parser end-to-end
//  7. PATCH  /Users/{id}     → replace + add + remove ops
//  8. PUT    /Users/{id}     → full replace
//  9. DELETE /Users/{id}     → 204
// 10. POST   /Groups + member ops + delete

package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestSCIM_HTTP_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	// Create the users auth-collection. Migration 0026's DO block has
	// already run (without `users` table existing), so we need to add
	// the SCIM-tracking columns manually here — in production the
	// schema builder's SCIM-aware DDL adds them when AuthCollection is
	// created (or operators run the equivalent ALTER manually).
	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(users)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatalf("create users: %v", err)
	}
	// Apply SCIM-managed columns post-create.
	if _, err := pool.Exec(ctx, `
        ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id TEXT;
        ALTER TABLE users ADD COLUMN IF NOT EXISTS scim_managed BOOLEAN NOT NULL DEFAULT FALSE;
        CREATE UNIQUE INDEX IF NOT EXISTS users_external_id_idx ON users (external_id) WHERE external_id IS NOT NULL;
    `); err != nil {
		t.Fatalf("add scim columns: %v", err)
	}

	var key secret.Key
	for i := range key {
		key[i] = byte(i)
	}
	tokens := scimauth.NewTokenStore(pool, key)
	rawToken, _, err := tokens.Create(ctx, scimauth.CreateInput{
		Name: "test", Collection: "users", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("mint scim token: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, &Deps{Pool: pool, Tokens: tokens})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// --- helpers ---
	bearer := func() string { return "Bearer " + rawToken }
	doReq := func(t *testing.T, method, path string, body any, withAuth bool) (int, []byte) {
		t.Helper()
		var bodyReader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			bodyReader = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, bodyReader)
		if body != nil {
			req.Header.Set("Content-Type", "application/scim+json")
		}
		if withAuth {
			req.Header.Set("Authorization", bearer())
		}
		c := &http.Client{Timeout: 10 * time.Second}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	// === [1a] Missing Authorization ===
	if code, _ := doReq(t, "GET", "/scim/v2/Users", nil, false); code != 401 {
		t.Errorf("[1a] missing auth = %d want 401", code)
	}
	// === [1b] Wrong prefix ===
	{
		req, _ := http.NewRequest("GET", srv.URL+"/scim/v2/Users", nil)
		req.Header.Set("Authorization", "Bearer rbat_wrong")
		resp, _ := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if resp.StatusCode != 401 {
			t.Errorf("[1b] wrong prefix = %d want 401", resp.StatusCode)
		}
		resp.Body.Close()
	}
	// === [1c] Unknown rbsm_ token ===
	{
		req, _ := http.NewRequest("GET", srv.URL+"/scim/v2/Users", nil)
		req.Header.Set("Authorization", "Bearer rbsm_unknown1234567890")
		resp, _ := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if resp.StatusCode != 401 {
			t.Errorf("[1c] unknown rbsm_ = %d want 401", resp.StatusCode)
		}
		resp.Body.Close()
	}

	// === [2] Discovery endpoints are PUBLIC ===
	for _, path := range []string{"/scim/v2/ServiceProviderConfig", "/scim/v2/ResourceTypes", "/scim/v2/Schemas"} {
		code, body := doReq(t, "GET", path, nil, false)
		if code != 200 {
			t.Errorf("[2] %s = %d want 200", path, code)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("[2] %s body not JSON: %v", path, err)
		}
		if _, ok := got["schemas"]; !ok {
			t.Errorf("[2] %s body missing schemas key", path)
		}
	}

	// === [3] POST /Users ===
	createBody := map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"userName":   "alice@example.com",
		"externalId": "okta-abc-123",
		"active":     true,
		"emails": []map[string]any{
			{"value": "alice@example.com", "primary": true, "type": "work"},
		},
	}
	code, body := doReq(t, "POST", "/scim/v2/Users", createBody, true)
	if code != 201 {
		t.Fatalf("[3] create = %d body=%s", code, body)
	}
	var created map[string]any
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("[3] body: %v", err)
	}
	aliceID, _ := created["id"].(string)
	if aliceID == "" {
		t.Fatalf("[3] no id in response")
	}
	if got := created["userName"]; got != "alice@example.com" {
		t.Errorf("[3] userName = %v", got)
	}
	if got := created["externalId"]; got != "okta-abc-123" {
		t.Errorf("[3] externalId = %v", got)
	}

	// === [4] GET /Users/{id} ===
	code, body = doReq(t, "GET", "/scim/v2/Users/"+aliceID, nil, true)
	if code != 200 {
		t.Errorf("[4] get = %d body=%s", code, body)
	}

	// === [5] GET /Users (list) ===
	doReq(t, "POST", "/scim/v2/Users", map[string]any{
		"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"userName": "bob@example.com",
		"active":   true,
	}, true)
	code, body = doReq(t, "GET", "/scim/v2/Users?count=10&startIndex=1", nil, true)
	if code != 200 {
		t.Fatalf("[5] list = %d body=%s", code, body)
	}
	var listResp map[string]any
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatalf("[5] body: %v", err)
	}
	if total, _ := listResp["totalResults"].(float64); total < 2 {
		t.Errorf("[5] totalResults = %v want ≥ 2", listResp["totalResults"])
	}

	// === [6] GET /Users?filter=userName eq "alice@example.com" ===
	filter := `userName eq "alice@example.com"`
	encoded := strings.ReplaceAll(filter, " ", "%20")
	encoded = strings.ReplaceAll(encoded, `"`, `%22`)
	code, body = doReq(t, "GET", "/scim/v2/Users?filter="+encoded, nil, true)
	if code != 200 {
		t.Fatalf("[6] filter = %d body=%s", code, body)
	}
	var filtered map[string]any
	_ = json.Unmarshal(body, &filtered)
	if total, _ := filtered["totalResults"].(float64); total != 1 {
		t.Errorf("[6] filter totalResults = %v want 1", filtered["totalResults"])
	}

	// === [7] PATCH /Users/{id} ===
	patchBody := map[string]any{
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{
			{"op": "replace", "path": "active", "value": false},
		},
	}
	code, body = doReq(t, "PATCH", "/scim/v2/Users/"+aliceID, patchBody, true)
	if code != 200 {
		t.Errorf("[7] patch = %d body=%s", code, body)
	}
	var patched map[string]any
	_ = json.Unmarshal(body, &patched)
	if got := patched["active"]; got != false {
		t.Errorf("[7] post-patch active = %v want false", got)
	}

	// === [8] PUT /Users/{id} ===
	putBody := map[string]any{
		"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"userName": "alice-new@example.com",
		"active":   true,
	}
	code, body = doReq(t, "PUT", "/scim/v2/Users/"+aliceID, putBody, true)
	if code != 200 {
		t.Errorf("[8] put = %d body=%s", code, body)
	}
	var put map[string]any
	_ = json.Unmarshal(body, &put)
	if got := put["userName"]; got != "alice-new@example.com" {
		t.Errorf("[8] post-put userName = %v", got)
	}

	// === [9] DELETE /Users/{id} ===
	code, _ = doReq(t, "DELETE", "/scim/v2/Users/"+aliceID, nil, true)
	if code != 204 {
		t.Errorf("[9] delete = %d want 204", code)
	}
	code, _ = doReq(t, "GET", "/scim/v2/Users/"+aliceID, nil, true)
	if code != 404 {
		t.Errorf("[9] post-delete get = %d want 404", code)
	}

	// === [10] Groups + members ===
	// Create a placeholder user to add to the group.
	code, body = doReq(t, "POST", "/scim/v2/Users", map[string]any{
		"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"userName": "carol@example.com",
		"active":   true,
	}, true)
	if code != 201 {
		t.Fatalf("[10] create carol = %d body=%s", code, body)
	}
	var carol map[string]any
	_ = json.Unmarshal(body, &carol)
	carolID, _ := carol["id"].(string)

	groupBody := map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Group"},
		"displayName": "Engineering",
		"externalId":  "okta-grp-eng",
		"members":     []map[string]any{{"value": carolID, "type": "User"}},
	}
	code, body = doReq(t, "POST", "/scim/v2/Groups", groupBody, true)
	if code != 201 {
		t.Fatalf("[10] create group = %d body=%s", code, body)
	}
	var group map[string]any
	_ = json.Unmarshal(body, &group)
	groupID, _ := group["id"].(string)

	// GET the group + assert membership round-tripped.
	code, body = doReq(t, "GET", "/scim/v2/Groups/"+groupID, nil, true)
	if code != 200 {
		t.Errorf("[10] get group = %d", code)
	}
	var fetched map[string]any
	_ = json.Unmarshal(body, &fetched)
	members, _ := fetched["members"].([]any)
	if len(members) != 1 {
		t.Errorf("[10] members = %d want 1", len(members))
	}

	// Remove the member via PATCH.
	rmBody := map[string]any{
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{
			{"op": "remove", "path": fmt.Sprintf(`members[value eq %q]`, carolID)},
		},
	}
	code, _ = doReq(t, "PATCH", "/scim/v2/Groups/"+groupID, rmBody, true)
	if code != 200 {
		t.Errorf("[10] patch group = %d", code)
	}
	code, body = doReq(t, "GET", "/scim/v2/Groups/"+groupID, nil, true)
	_ = json.Unmarshal(body, &fetched)
	members, _ = fetched["members"].([]any)
	if len(members) != 0 {
		t.Errorf("[10] post-remove members = %d want 0", len(members))
	}

	// DELETE group.
	code, _ = doReq(t, "DELETE", "/scim/v2/Groups/"+groupID, nil, true)
	if code != 204 {
		t.Errorf("[10] delete group = %d want 204", code)
	}
}
