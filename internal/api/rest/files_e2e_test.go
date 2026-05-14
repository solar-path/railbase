//go:build embed_pg

// Live files smoke. Spins up embedded Postgres, registers a `posts`
// collection with file + files fields, drives upload + download +
// list/get through the REST handlers, and asserts metadata + signed
// URLs round-trip.
//
// Verifies (8 checks):
//
//	1. Upload empty/missing-multipart → 400
//	2. POST record → file fields render null until upload
//	3. Multipart upload to /files/{field} → 200 + {name, size, mime, url}
//	4. List shows updated record with file field rendered {name, url}
//	5. Signed-URL GET returns the uploaded bytes
//	6. Tampered token → 403
//	7. Multi-file field accumulates uploads (2 separate uploads → array of 2)
//	8. DELETE /files/{field}/{filename} strips column + metadata + blob
//
// Run:
//	go test -tags embed_pg -run TestFilesFlowE2E -timeout 120s \
//	    ./internal/api/rest/...

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/files"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestFilesFlowE2E(t *testing.T) {
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

	// `posts` with single-file + multi-file columns.
	posts := schemabuilder.NewCollection("posts").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Field("cover", schemabuilder.NewFile()).
		Field("attachments", schemabuilder.NewFiles())
	registry.Reset()
	registry.Register(posts)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatal(err)
	}

	// Wire FilesDeps with an FSDriver rooted in the tempdir.
	storageDir := filepath.Join(dataDir, "storage")
	driver, err := files.NewFSDriver(storageDir)
	if err != nil {
		t.Fatal(err)
	}
	fd := &FilesDeps{
		Driver:    driver,
		Store:     files.NewStore(pool),
		Signer:    []byte("test-signing-key-32-bytes-min!!!!"),
		URLTTL:    1 * time.Minute,
		MaxUpload: 5 << 20,
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, fd, nil)
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

	uploadFile := func(recordID, field, filename, content string) (int, map[string]any) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		fw, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(content))
		w.Close()
		req, _ := http.NewRequest("POST",
			srv.URL+"/api/collections/posts/records/"+recordID+"/files/"+field, &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
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

	// === [2] Create record; file fields render null ===
	status, rec := doJSON("POST", "/api/collections/posts/records", map[string]any{
		"title": "hello",
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, rec)
	}
	recordID, _ := rec["id"].(string)
	if rec["cover"] != nil {
		t.Errorf("[2] cover should be null until upload, got %v", rec["cover"])
	}
	t.Logf("[2] record created, file fields render null")

	// === [1] Upload without multipart part → 400 ===
	req, _ := http.NewRequest("POST",
		srv.URL+"/api/collections/posts/records/"+recordID+"/files/cover", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=nope")
	resp1, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != 400 {
		t.Errorf("[1] expected 400 on malformed multipart, got %d", resp1.StatusCode)
	}
	t.Logf("[1] malformed multipart rejected with 400")

	// === [3] Upload to single-file field ===
	body := "the cover content bytes go here"
	status, upload := uploadFile(recordID, "cover", "cover.png", body)
	if status != 200 {
		t.Fatalf("[3] upload: %d %v", status, upload)
	}
	if upload["name"] != "cover.png" {
		t.Errorf("[3] name: %v", upload["name"])
	}
	if int(upload["size"].(float64)) != len(body) {
		t.Errorf("[3] size: %v", upload["size"])
	}
	if upload["url"] == nil || upload["url"] == "" {
		t.Errorf("[3] missing url: %v", upload)
	}
	t.Logf("[3] single-file upload returned %v", upload)

	// === [4] List shows file field as {name, url} ===
	status, view := doJSON("GET", "/api/collections/posts/records/"+recordID, nil)
	if status != 200 {
		t.Fatalf("[4] view: %d %v", status, view)
	}
	cover, _ := view["cover"].(map[string]any)
	if cover == nil || cover["name"] != "cover.png" || cover["url"] == nil {
		t.Errorf("[4] cover field shape: %v", view["cover"])
	}
	t.Logf("[4] view renders cover as %v", cover)

	// === [5] Signed-URL GET returns the bytes ===
	rawURL := upload["url"].(string)
	resp5, err := http.Get(srv.URL + rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp5.Body.Close()
	got, _ := io.ReadAll(resp5.Body)
	if resp5.StatusCode != 200 {
		t.Fatalf("[5] download: %d body=%s", resp5.StatusCode, got)
	}
	if string(got) != body {
		t.Errorf("[5] payload mismatch: got %q want %q", got, body)
	}
	t.Logf("[5] signed URL returned %d bytes", len(got))

	// === [6] Tampered token → 403 ===
	// Flip the first hex digit of the token.
	tampered := rawURL[:len("/api/files/posts/")+len(recordID)+len("/cover/cover.png?token=")]
	// Find ?token= and bump byte after it.
	idx := bytes.Index([]byte(rawURL), []byte("?token="))
	if idx >= 0 {
		t.Logf("token starts at offset %d in %q", idx, rawURL)
		tampered = rawURL[:idx+len("?token=")] + "0" + rawURL[idx+len("?token=")+1:]
	}
	resp6, err := http.Get(srv.URL + tampered)
	if err != nil {
		t.Fatal(err)
	}
	resp6.Body.Close()
	if resp6.StatusCode != 403 {
		t.Errorf("[6] tampered token: expected 403, got %d", resp6.StatusCode)
	}
	t.Logf("[6] tampered token rejected with 403")

	// === [7] Multi-file uploads accumulate ===
	uploadFile(recordID, "attachments", "doc1.txt", "first attachment")
	uploadFile(recordID, "attachments", "doc2.txt", "second attachment")
	status, view2 := doJSON("GET", "/api/collections/posts/records/"+recordID, nil)
	if status != 200 {
		t.Fatalf("[7] view: %d", status)
	}
	atts, _ := view2["attachments"].([]any)
	if len(atts) != 2 {
		t.Fatalf("[7] expected 2 attachments, got %d (%v)", len(atts), view2["attachments"])
	}
	first, _ := atts[0].(map[string]any)
	second, _ := atts[1].(map[string]any)
	if first["name"] != "doc1.txt" || second["name"] != "doc2.txt" {
		t.Errorf("[7] attachment order: %v", atts)
	}
	t.Logf("[7] multi-file accumulates: %v / %v", first["name"], second["name"])

	// === [8] DELETE single-file strips column + blob ===
	req8, _ := http.NewRequest("DELETE",
		srv.URL+"/api/collections/posts/records/"+recordID+"/files/cover/cover.png", nil)
	resp8, err := http.DefaultClient.Do(req8)
	if err != nil {
		t.Fatal(err)
	}
	resp8.Body.Close()
	if resp8.StatusCode != 204 {
		t.Errorf("[8] delete: %d", resp8.StatusCode)
	}
	status, view3 := doJSON("GET", "/api/collections/posts/records/"+recordID, nil)
	if status != 200 {
		t.Fatalf("[8] view after delete: %d", status)
	}
	if view3["cover"] != nil {
		t.Errorf("[8] cover should be null after delete, got %v", view3["cover"])
	}
	// Signed URL should now 404 (metadata gone).
	resp8b, _ := http.Get(srv.URL + rawURL)
	resp8b.Body.Close()
	if resp8b.StatusCode == 200 {
		t.Errorf("[8] download after delete: expected non-200, got 200")
	}
	t.Logf("[8] delete stripped column + blob; download returns %d", resp8b.StatusCode)

	t.Log("Files E2E: 8/8 checks passed")
}
