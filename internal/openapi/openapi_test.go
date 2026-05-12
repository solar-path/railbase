package openapi

// v1.7.1 — unit tests for the OpenAPI 3.1 generator. Pure functions,
// no DB / HTTP needed. Each test exercises one slice of the surface so
// regressions land surgically.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/sdkgen"
)

// fixtureSpecs returns a representative spec list: one plain
// collection (`posts`) with a mix of field types, one auth-collection
// (`users`) with required fields, one tenant + soft-delete + ordered
// adjacency collection (`comments`) to exercise the system-fields
// surface.
func fixtureSpecs() []builder.CollectionSpec {
	posts := builder.NewCollection("posts").
		Field("title", builder.NewText().Required()).
		Field("status", builder.NewSelect("draft", "published")).
		Field("body", builder.NewMarkdown()).
		Spec()

	users := builder.NewAuthCollection("users").Spec()

	comments := builder.NewCollection("comments").
		Field("body", builder.NewText().Required()).
		Tenant().
		SoftDelete().
		AdjacencyList().
		Ordered().
		Spec()

	return []builder.CollectionSpec{posts, users, comments}
}

func TestEmit_BasicShape(t *testing.T) {
	doc, err := Emit(fixtureSpecs(), Options{})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi version = %q, want 3.1.0", doc.OpenAPI)
	}
	if doc.Info.Title != "Railbase API" {
		t.Errorf("default title = %q", doc.Info.Title)
	}
	if len(doc.Servers) != 1 || doc.Servers[0].URL != "http://localhost:8090" {
		t.Errorf("default server = %+v", doc.Servers)
	}
}

func TestEmit_CollectionPaths(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, want := range []string{
		"/api/collections/posts/records",
		"/api/collections/posts/records/{id}",
		"/api/collections/users/records",
		"/api/collections/users/records/{id}",
		"/api/collections/comments/records",
	} {
		if _, ok := doc.Paths.Get(want); !ok {
			t.Errorf("missing path %s", want)
		}
	}
}

func TestEmit_AuthCollectionGetsAuthPaths(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, want := range []string{
		"/api/collections/users/auth-signup",
		"/api/collections/users/auth-with-password",
		"/api/collections/users/auth-refresh",
		"/api/collections/users/auth-logout",
		"/api/collections/users/auth-methods",
	} {
		if _, ok := doc.Paths.Get(want); !ok {
			t.Errorf("missing auth path %s", want)
		}
	}
}

func TestEmit_NonAuthCollectionGetsNoAuthPaths(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, bad := range []string{
		"/api/collections/posts/auth-signup",
		"/api/collections/posts/auth-with-password",
	} {
		if _, ok := doc.Paths.Get(bad); ok {
			t.Errorf("non-auth collection got auth path %s", bad)
		}
	}
}

func TestEmit_AuthCollectionRecordsHasNoCreate(t *testing.T) {
	// POST /records on an auth collection returns 403 at runtime
	// (clients use auth-signup instead). The OAS reflects that —
	// omit POST entirely so codegen tooling doesn't materialise a
	// dead method.
	doc, _ := Emit(fixtureSpecs(), Options{})
	users, _ := doc.Paths.Get("/api/collections/users/records")
	if users.Post != nil {
		t.Errorf("users/records.POST should be nil for auth-collection, got %+v", users.Post)
	}
	posts, _ := doc.Paths.Get("/api/collections/posts/records")
	if posts.Post == nil {
		t.Error("posts/records.POST should be present for non-auth collection")
	}
}

func TestEmit_SystemPaths(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, want := range []string{"/api/auth/me", "/healthz", "/readyz"} {
		if _, ok := doc.Paths.Get(want); !ok {
			t.Errorf("missing system path %s", want)
		}
	}
}

func TestEmit_TenantAddsTenantIDProperty(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	comments := doc.Components.Schemas["Comments"]
	if comments == nil {
		t.Fatal("Comments schema missing")
	}
	if _, ok := comments.Properties["tenant_id"]; !ok {
		t.Errorf("tenant collection should expose tenant_id property; got %v",
			propertyNames(comments))
	}
}

func TestEmit_SoftDeleteAddsDeletedProperty(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	comments := doc.Components.Schemas["Comments"]
	if comments == nil {
		t.Fatal("Comments schema missing")
	}
	d, ok := comments.Properties["deleted"]
	if !ok {
		t.Fatal("soft-delete collection should expose `deleted`")
	}
	if !d.Nullable {
		t.Error("deleted should be nullable")
	}
}

func TestEmit_AdjacencyListAddsParent(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	comments := doc.Components.Schemas["Comments"]
	if _, ok := comments.Properties["parent"]; !ok {
		t.Errorf("adjacency-list collection should expose `parent`; got %v",
			propertyNames(comments))
	}
}

func TestEmit_OrderedAddsSortIndex(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	comments := doc.Components.Schemas["Comments"]
	if _, ok := comments.Properties["sort_index"]; !ok {
		t.Errorf("ordered collection should expose `sort_index`; got %v",
			propertyNames(comments))
	}
}

func TestEmit_SoftDeleteAddsIncludeDeletedParam(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	comments, _ := doc.Paths.Get("/api/collections/comments/records")
	if comments.Get == nil {
		t.Fatal("comments list op missing")
	}
	found := false
	for _, p := range comments.Get.Parameters {
		if p.Name == "includeDeleted" {
			found = true
			break
		}
	}
	if !found {
		t.Error("soft-delete collection list op should accept includeDeleted")
	}
}

func TestEmit_PasswordNeverAppearsInRowSchema(t *testing.T) {
	// password is write-only. The auth-collection row schema should
	// not expose it (the server never returns it on read).
	doc, _ := Emit(fixtureSpecs(), Options{})
	users := doc.Components.Schemas["Users"]
	if users == nil {
		t.Fatal("Users schema missing")
	}
	if _, bad := users.Properties["password"]; bad {
		t.Error("auth-collection row schema must NOT expose `password`")
	}
	if _, bad := users.Properties["password_hash"]; bad {
		t.Error("auth-collection row schema must NOT expose `password_hash`")
	}
}

func TestEmit_AuthCollectionRowExposesEmailVerified(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	users := doc.Components.Schemas["Users"]
	for _, k := range []string{"email", "verified", "last_login_at"} {
		if _, ok := users.Properties[k]; !ok {
			t.Errorf("auth-collection row missing system field %q", k)
		}
	}
}

func TestEmit_SchemaHashMatchesSDKGen(t *testing.T) {
	// Paired drift detection: openapi + sdkgen + admin /api/_meta
	// MUST agree on the schema hash for one snapshot. This test
	// catches regressions where the openapi generator computes a
	// different canonical form.
	specs := fixtureSpecs()
	doc, _ := Emit(specs, Options{})
	if doc.XRailbase == nil {
		t.Fatal("x-railbase missing")
	}
	expected, _ := sdkgen.SchemaHash(specs)
	if doc.XRailbase.SchemaHash != expected {
		t.Errorf("schema hash drift: openapi=%s sdkgen=%s",
			doc.XRailbase.SchemaHash, expected)
	}
}

func TestEmit_SharedSchemasPresent(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, want := range []string{
		"ErrorEnvelope", "ListResponse", "AuthResponse", "AuthMethods", "FileRef",
	} {
		if _, ok := doc.Components.Schemas[want]; !ok {
			t.Errorf("shared schema %q missing", want)
		}
	}
}

func TestEmit_PerCollectionListSchema(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, want := range []string{"PostsList", "UsersList", "CommentsList"} {
		s, ok := doc.Components.Schemas[want]
		if !ok {
			t.Errorf("per-collection list schema %q missing", want)
			continue
		}
		items := s.Properties["items"]
		if items == nil || items.Items == nil {
			t.Errorf("%s.items.items missing", want)
			continue
		}
		// The items.items.$ref should target the row schema, e.g. Posts.
		bare := strings.TrimPrefix(want, "")
		bare = strings.TrimSuffix(bare, "List")
		wantRef := "#/components/schemas/" + bare
		if items.Items.Ref != wantRef {
			t.Errorf("%s.items.items.$ref = %q, want %q", want, items.Items.Ref, wantRef)
		}
	}
}

func TestEmit_StatusEnumSurfacedInSchema(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	posts := doc.Components.Schemas["Posts"]
	status := posts.Properties["status"]
	if status == nil {
		t.Fatal("posts.status property missing")
	}
	if len(status.Enum) != 2 || status.Enum[0] != "draft" || status.Enum[1] != "published" {
		t.Errorf("select enum = %v, want [draft published]", status.Enum)
	}
}

func TestEmit_DeterministicJSON(t *testing.T) {
	// Same input → same bytes. Regression guard against accidentally
	// using map iteration in a place that should be ordered.
	specs := fixtureSpecs()
	a, err := EmitJSON(specs, Options{SchemaHash: "sha256:test"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := EmitJSON(specs, Options{SchemaHash: "sha256:test"})
	if err != nil {
		t.Fatal(err)
	}
	// `generatedAt` is the only non-deterministic field (time.Now()).
	// Strip it before comparison.
	aStr := stripGeneratedAt(string(a))
	bStr := stripGeneratedAt(string(b))
	if aStr != bStr {
		t.Errorf("non-deterministic output:\n--- A ---\n%s\n--- B ---\n%s", aStr, bStr)
	}
}

func TestEmit_OperationIDsStable(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	want := map[string]string{
		"/api/collections/posts/records":            "listPosts",
		"/api/collections/posts/records/{id}":       "viewPosts",
		"/api/collections/users/auth-with-password": "signinWithPasswordUsers",
		"/api/collections/users/auth-methods":       "authMethodsUsers",
		"/healthz":                                  "healthz",
	}
	for path, wantOpID := range want {
		item, _ := doc.Paths.Get(path)
		var op *Operation
		if item.Get != nil {
			op = item.Get
		} else if item.Post != nil {
			op = item.Post
		}
		if op == nil {
			t.Errorf("path %s: no operation found", path)
			continue
		}
		if op.OperationID != wantOpID {
			t.Errorf("path %s: operationId=%q want %q", path, op.OperationID, wantOpID)
		}
	}
}

func TestEmit_TypeName(t *testing.T) {
	cases := map[string]string{
		"posts":     "Posts",
		"blog_post": "BlogPost",
		"user_2fa":  "User2fa",
		"":          "",
	}
	for in, want := range cases {
		if got := typeName(in); got != want {
			t.Errorf("typeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- helpers ---

func propertyNames(s *Schema) []string {
	out := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		out = append(out, k)
	}
	return out
}

func stripGeneratedAt(s string) string {
	// Strip the whole `"generatedAt": "..."` key-value pair plus the
	// trailing comma + whitespace, so the surrounding JSON stays well
	// formed for the equality check.
	keyIdx := strings.Index(s, `"generatedAt"`)
	if keyIdx < 0 {
		return s
	}
	// Find the colon after the key.
	colonRel := strings.Index(s[keyIdx:], ":")
	if colonRel < 0 {
		return s
	}
	// Skip past colon + whitespace + opening quote of the value.
	i := keyIdx + colonRel + 1
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) || s[i] != '"' {
		return s
	}
	// Walk to the closing quote of the value.
	valEnd := i + 1
	for valEnd < len(s) && s[valEnd] != '"' {
		if s[valEnd] == '\\' && valEnd+1 < len(s) {
			valEnd += 2
			continue
		}
		valEnd++
	}
	if valEnd >= len(s) {
		return s
	}
	// Past closing quote. Strip trailing comma + whitespace too.
	end := valEnd + 1
	for end < len(s) && (s[end] == ',' || s[end] == ' ' || s[end] == '\n' || s[end] == '\t') {
		end++
	}
	// Strip preceding whitespace back to the previous structural char
	// (so we don't leave the indent of the now-deleted line).
	start := keyIdx
	for start > 0 && (s[start-1] == ' ' || s[start-1] == '\t' || s[start-1] == '\n') {
		start--
	}
	return s[:start] + s[end:]
}

// --- v1.7.2 extensions: exports, realtime, multipart ---

// fixtureWithExports adds a `reports` collection that declares both
// XLSX and PDF export configs, plus a `media` collection with a single
// XLSX config. Used to exercise the conditional export-path emit.
func fixtureWithExports() []builder.CollectionSpec {
	reports := builder.NewCollection("reports").
		Field("title", builder.NewText().Required()).
		Export(
			builder.ExportXLSX(builder.XLSXExportConfig{Sheet: "Reports"}),
			builder.ExportPDF(builder.PDFExportConfig{Title: "Reports Report"}),
		).
		Spec()
	media := builder.NewCollection("media").
		Field("name", builder.NewText()).
		Export(builder.ExportXLSX(builder.XLSXExportConfig{})).
		Spec()
	return []builder.CollectionSpec{reports, media}
}

// fixtureWithFiles adds an `assets` collection with both a File and a
// Files field, plus a plain `tags` collection so we can assert the
// multipart variant lands on the right one and only the right one.
func fixtureWithFiles() []builder.CollectionSpec {
	assets := builder.NewCollection("assets").
		Field("name", builder.NewText().Required()).
		Field("cover", builder.NewFile()).
		Field("gallery", builder.NewFiles()).
		Spec()
	tags := builder.NewCollection("tags").
		Field("label", builder.NewText().Required()).
		Spec()
	return []builder.CollectionSpec{assets, tags}
}

func TestEmit_ExportPathsEmittedForExportConfiguredCollections(t *testing.T) {
	doc, _ := Emit(fixtureWithExports(), Options{})
	for _, want := range []string{
		"/api/collections/reports/export.xlsx",
		"/api/collections/reports/export.pdf",
		"/api/collections/media/export.xlsx",
	} {
		if _, ok := doc.Paths.Get(want); !ok {
			t.Errorf("missing export path %s", want)
		}
	}
	// media has no PDF config — must NOT emit a media/export.pdf path.
	if _, ok := doc.Paths.Get("/api/collections/media/export.pdf"); ok {
		t.Error("media has no PDF config but /export.pdf path was emitted")
	}
}

func TestEmit_ExportPathsAbsentForPlainCollections(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, bad := range []string{
		"/api/collections/posts/export.xlsx",
		"/api/collections/posts/export.pdf",
		"/api/collections/comments/export.xlsx",
	} {
		if _, ok := doc.Paths.Get(bad); ok {
			t.Errorf("plain collection got export path %s — should be absent without .Export()", bad)
		}
	}
}

func TestEmit_ExportXlsxResponseShape(t *testing.T) {
	doc, _ := Emit(fixtureWithExports(), Options{})
	item, ok := doc.Paths.Get("/api/collections/reports/export.xlsx")
	if !ok {
		t.Fatal("reports/export.xlsx missing")
	}
	if item.Get == nil {
		t.Fatal("export.xlsx GET missing")
	}
	if item.Get.OperationID != "exportXlsxReports" {
		t.Errorf("operationId = %q, want exportXlsxReports", item.Get.OperationID)
	}
	resp200 := item.Get.Responses["200"]
	mime, ok := resp200.Content["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"]
	if !ok {
		t.Fatalf("xlsx mime missing from 200; have %v", contentTypes(resp200))
	}
	if mime.Schema == nil || mime.Schema.Format != "binary" {
		t.Errorf("xlsx 200 schema = %+v; want binary string", mime.Schema)
	}
}

func TestEmit_ExportPdfResponseShape(t *testing.T) {
	doc, _ := Emit(fixtureWithExports(), Options{})
	item, ok := doc.Paths.Get("/api/collections/reports/export.pdf")
	if !ok {
		t.Fatal("reports/export.pdf missing")
	}
	if item.Get == nil || item.Get.OperationID != "exportPdfReports" {
		t.Errorf("pdf op missing / wrong id: %+v", item.Get)
	}
	resp200 := item.Get.Responses["200"]
	if _, ok := resp200.Content["application/pdf"]; !ok {
		t.Errorf("pdf 200 missing application/pdf content; have %v", contentTypes(resp200))
	}
}

func TestEmit_AsyncExportPathsPresent(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, want := range []string{
		"/api/exports",
		"/api/exports/{id}",
		"/api/exports/{id}/file",
	} {
		if _, ok := doc.Paths.Get(want); !ok {
			t.Errorf("missing async-export path %s", want)
		}
	}
	enqueue, _ := doc.Paths.Get("/api/exports")
	if enqueue.Post == nil || enqueue.Post.OperationID != "enqueueExport" {
		t.Errorf("enqueueExport op missing or misnamed: %+v", enqueue.Post)
	}
	if _, ok := enqueue.Post.Responses["202"]; !ok {
		t.Error("POST /api/exports must declare 202")
	}
	status, _ := doc.Paths.Get("/api/exports/{id}")
	if status.Get == nil || status.Get.OperationID != "getExport" {
		t.Errorf("getExport op missing: %+v", status.Get)
	}
	dl, _ := doc.Paths.Get("/api/exports/{id}/file")
	if dl.Get == nil || dl.Get.OperationID != "downloadExport" {
		t.Errorf("downloadExport op missing: %+v", dl.Get)
	}
	// 200 must declare both binary content types.
	resp200 := dl.Get.Responses["200"]
	if _, ok := resp200.Content["application/pdf"]; !ok {
		t.Error("downloadExport 200 missing application/pdf")
	}
	if _, ok := resp200.Content["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"]; !ok {
		t.Error("downloadExport 200 missing xlsx mime")
	}
}

func TestEmit_AsyncExportSchemasPresent(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	for _, want := range []string{
		"AsyncExportRequest", "AsyncExportAccepted", "AsyncExportStatus",
	} {
		if _, ok := doc.Components.Schemas[want]; !ok {
			t.Errorf("async-export shared schema %q missing", want)
		}
	}
	req := doc.Components.Schemas["AsyncExportRequest"]
	if len(req.Required) == 0 {
		t.Error("AsyncExportRequest should declare required fields")
	}
	format := req.Properties["format"]
	if format == nil || len(format.Enum) != 2 {
		t.Errorf("AsyncExportRequest.format should enumerate xlsx + pdf, got %+v", format)
	}
}

func TestEmit_RealtimePathPresent(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	item, ok := doc.Paths.Get("/api/realtime")
	if !ok {
		t.Fatal("/api/realtime missing")
	}
	if item.Get == nil || item.Get.OperationID != "subscribeRealtime" {
		t.Errorf("subscribeRealtime op missing or misnamed: %+v", item.Get)
	}
	resp200, ok := item.Get.Responses["200"]
	if !ok {
		t.Fatal("realtime GET missing 200 response")
	}
	if _, ok := resp200.Content["text/event-stream"]; !ok {
		t.Errorf("realtime 200 must declare text/event-stream; have %v", contentTypes(resp200))
	}
	// topics param is required.
	var topicsParam *Parameter
	for i := range item.Get.Parameters {
		p := &item.Get.Parameters[i]
		if p.Name == "topics" {
			topicsParam = p
			break
		}
	}
	if topicsParam == nil {
		t.Fatal("realtime missing topics parameter")
	}
	if !topicsParam.Required {
		t.Error("topics parameter must be Required")
	}
}

func TestEmit_RealtimeAndExportTagsPresent(t *testing.T) {
	doc, _ := Emit(fixtureSpecs(), Options{})
	seen := map[string]bool{}
	for _, t := range doc.Tags {
		seen[t.Name] = true
	}
	for _, want := range []string{"system", "export", "realtime"} {
		if !seen[want] {
			t.Errorf("tag %q missing from doc.Tags", want)
		}
	}
}

func TestEmit_MultipartVariantOnFileCollections(t *testing.T) {
	doc, _ := Emit(fixtureWithFiles(), Options{})
	item, ok := doc.Paths.Get("/api/collections/assets/records")
	if !ok {
		t.Fatal("assets/records path missing")
	}
	if item.Post == nil || item.Post.RequestBody == nil {
		t.Fatal("assets/records POST missing or has no requestBody")
	}
	content := item.Post.RequestBody.Content
	if _, ok := content["application/json"]; !ok {
		t.Error("assets create POST should keep application/json variant")
	}
	mp, ok := content["multipart/form-data"]
	if !ok {
		t.Fatalf("assets create POST should add multipart/form-data variant; have %v",
			contentKeys(content))
	}
	if mp.Schema == nil || mp.Schema.Ref != "#/components/schemas/AssetsCreateInputMultipart" {
		t.Errorf("multipart variant should $ref AssetsCreateInputMultipart, got %+v", mp.Schema)
	}
	// PATCH /records/{id} on assets gets the same treatment.
	byID, _ := doc.Paths.Get("/api/collections/assets/records/{id}")
	if byID.Patch == nil || byID.Patch.RequestBody == nil {
		t.Fatal("assets PATCH missing")
	}
	if _, ok := byID.Patch.RequestBody.Content["multipart/form-data"]; !ok {
		t.Error("assets update PATCH should add multipart variant for File field")
	}
}

func TestEmit_MultipartSchemaTypesBinary(t *testing.T) {
	doc, _ := Emit(fixtureWithFiles(), Options{})
	mp := doc.Components.Schemas["AssetsCreateInputMultipart"]
	if mp == nil {
		t.Fatal("AssetsCreateInputMultipart schema missing")
	}
	cover := mp.Properties["cover"]
	if cover == nil || cover.Type != "string" || cover.Format != "binary" {
		t.Errorf("cover (File) should be {type:string, format:binary}; got %+v", cover)
	}
	gallery := mp.Properties["gallery"]
	if gallery == nil || gallery.Type != "array" {
		t.Fatalf("gallery (Files) should be array; got %+v", gallery)
	}
	if gallery.Items == nil || gallery.Items.Format != "binary" {
		t.Errorf("gallery items should be binary; got %+v", gallery.Items)
	}
	// Non-file field stays typed as in JSON variant.
	if name := mp.Properties["name"]; name == nil || name.Type != "string" {
		t.Errorf("non-file field `name` should remain a string in multipart variant; got %+v", name)
	}
}

func TestEmit_NoMultipartVariantForPlainCollection(t *testing.T) {
	doc, _ := Emit(fixtureWithFiles(), Options{})
	item, ok := doc.Paths.Get("/api/collections/tags/records")
	if !ok {
		t.Fatal("tags/records missing")
	}
	if item.Post == nil || item.Post.RequestBody == nil {
		t.Fatal("tags create POST missing")
	}
	if _, bad := item.Post.RequestBody.Content["multipart/form-data"]; bad {
		t.Error("plain (no-File) collection must NOT emit multipart variant")
	}
	if _, ok := doc.Components.Schemas["TagsCreateInputMultipart"]; ok {
		t.Error("plain collection must NOT produce a *Multipart schema")
	}
}

// --- helpers for new tests ---

func contentTypes(r Response) []string {
	out := make([]string, 0, len(r.Content))
	for k := range r.Content {
		out = append(out, k)
	}
	return out
}

func contentKeys(c map[string]MediaType) []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

// Sanity: full JSON encoding doesn't error on representative input.
// Catches struct-tag bugs or unencodable values.
func TestEmitJSON_FullRoundTrip(t *testing.T) {
	body, err := EmitJSON(fixtureSpecs(), Options{
		Title:     "Test API",
		ServerURL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("EmitJSON: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if v["openapi"] != "3.1.0" {
		t.Errorf("round-trip openapi = %v", v["openapi"])
	}
}
