package rest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/schema/builder"
)

func samplePostsSpec() builder.CollectionSpec {
	c := builder.NewCollection("posts").
		Field("title", builder.NewText().Required().MinLen(3)).
		Field("body", builder.NewText()).
		Field("status", builder.NewSelect("draft", "published").Default("draft").Required()).
		Field("hits", builder.NewNumber().Int()).
		Field("public", builder.NewBool()).
		Field("tags", builder.NewMultiSelect("a", "b", "c")).
		Field("meta", builder.NewJSON()).
		Field("password", builder.NewPassword()) // deferred — must not appear in output
	return c.Spec()
}

func TestMarshalRecord_PocketBaseShape(t *testing.T) {
	spec := samplePostsSpec()
	now := time.Date(2026, 5, 10, 12, 34, 56, 789_000_000, time.UTC)
	row := map[string]any{
		"id":       "0190a3a8-0000-7000-8000-000000000001",
		"created":  now,
		"updated":  now,
		"title":    "Hello",
		"body":     "world",
		"status":   "published",
		"hits":     int64(42),
		"public":   true,
		"tags":     []any{"a", "b"},
		"meta":     []byte(`{"k":"v"}`),
		"password": "should-not-appear",
	}

	buf, err := marshalRecord(spec, row, nil)
	if err != nil {
		t.Fatalf("marshalRecord: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, buf)
	}

	if got["id"] != "0190a3a8-0000-7000-8000-000000000001" {
		t.Errorf("id mismatch: %v", got["id"])
	}
	if got["collectionName"] != "posts" {
		t.Errorf("collectionName mismatch: %v", got["collectionName"])
	}
	if got["created"] != "2026-05-10 12:34:56.789Z" {
		t.Errorf("created not in PB-format: %v", got["created"])
	}
	if got["title"] != "Hello" {
		t.Errorf("title mismatch: %v", got["title"])
	}
	if got["status"] != "published" {
		t.Errorf("status mismatch: %v", got["status"])
	}
	if _, leaked := got["password"]; leaked {
		t.Errorf("password field must not appear in output: %v", got)
	}
	// JSON column round-trip stays as object (not base64).
	meta, ok := got["meta"].(map[string]any)
	if !ok || meta["k"] != "v" {
		t.Errorf("meta did not round-trip as object: %v", got["meta"])
	}
	// MultiSelect comes through as JSON array.
	tags, ok := got["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" {
		t.Errorf("tags round-trip wrong: %v", got["tags"])
	}
}

func TestParseInput_RejectsUnknown(t *testing.T) {
	spec := samplePostsSpec()
	_, perr := parseInput(spec, []byte(`{"title":"x","not_a_field":1}`), false)
	if perr == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(perr.Message, "unknown") {
		t.Errorf("message: %s", perr.Message)
	}
	got := perr.Details["unknown_fields"].([]string)
	if len(got) != 1 || got[0] != "not_a_field" {
		t.Errorf("details unexpected: %v", got)
	}
}

func TestParseInput_RejectsDeferredFields(t *testing.T) {
	spec := samplePostsSpec()
	_, perr := parseInput(spec, []byte(`{"password":"hunter2"}`), false)
	if perr == nil {
		t.Fatal("expected error writing to deferred field")
	}
	if !strings.Contains(perr.Message, "not supported") {
		t.Errorf("message: %s", perr.Message)
	}
}

func TestParseInput_AllowsSystemKeysButDropsThem(t *testing.T) {
	spec := samplePostsSpec()
	got, perr := parseInput(spec, []byte(`{"id":"x","collectionName":"posts","title":"hi","status":"draft"}`), true)
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if _, ok := got["id"]; ok {
		t.Errorf("id should be dropped, got: %v", got)
	}
	if _, ok := got["collectionName"]; ok {
		t.Errorf("collectionName should be dropped")
	}
	if got["title"] != "hi" {
		t.Errorf("title not preserved: %v", got)
	}
}

func TestParseInput_RequiredOnCreate(t *testing.T) {
	spec := samplePostsSpec()
	// Missing required `title` — `status` has a default so it doesn't count.
	_, perr := parseInput(spec, []byte(`{}`), true)
	if perr == nil {
		t.Fatal("expected required-field error")
	}
	missing, _ := perr.Details["missing_fields"].([]string)
	if len(missing) != 1 || missing[0] != "title" {
		t.Errorf("expected missing_fields=[title], got %v", perr.Details)
	}
}

func TestParseInput_PartialUpdateNoRequiredCheck(t *testing.T) {
	spec := samplePostsSpec()
	_, perr := parseInput(spec, []byte(`{"body":"new text"}`), false) // create=false
	if perr != nil {
		t.Fatalf("partial update should not require fields: %v", perr)
	}
}

func TestParseInput_EmptyBody(t *testing.T) {
	spec := samplePostsSpec()
	got, perr := parseInput(spec, []byte(`   `), false)
	if perr != nil {
		t.Fatalf("empty body should be valid for partial update: %v", perr)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestFormatTime(t *testing.T) {
	in := time.Date(2026, 5, 10, 12, 34, 56, 789_000_000, time.FixedZone("X", 5*3600))
	got := formatTime(in)
	want := "2026-05-10 07:34:56.789Z"
	if got != want {
		t.Errorf("formatTime: got %q want %q", got, want)
	}
}

// --- Translatable field (§3.9.3 i18n follow-up) ---

func translatableSpec() builder.CollectionSpec {
	return builder.NewCollection("articles").
		Field("title", builder.NewText().Required().Translatable()).
		Field("body", builder.NewText()).
		Spec()
}

func TestMarshalRecord_Translatable_NoLocale_EmitsFullMap(t *testing.T) {
	spec := translatableSpec()
	now := time.Date(2026, 5, 10, 12, 34, 56, 789_000_000, time.UTC)
	row := map[string]any{
		"id":      "0190a3a8-0000-7000-8000-000000000001",
		"created": now, "updated": now,
		"title": []byte(`{"en":"Hello","ru":"Привет"}`),
		"body":  "plain",
	}
	buf, err := marshalRecord(spec, row, nil)
	if err != nil {
		t.Fatalf("marshalRecord: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, buf)
	}
	titleMap, ok := got["title"].(map[string]any)
	if !ok {
		t.Fatalf("title should be object when no locale supplied: %T %v", got["title"], got["title"])
	}
	if titleMap["en"] != "Hello" || titleMap["ru"] != "Привет" {
		t.Errorf("full map round-trip mismatch: %v", titleMap)
	}
}

func TestMarshalRecord_Translatable_PicksRequestedLocale(t *testing.T) {
	spec := translatableSpec()
	now := time.Date(2026, 5, 10, 12, 34, 56, 789_000_000, time.UTC)
	row := map[string]any{
		"id":      "0190a3a8-0000-7000-8000-000000000001",
		"created": now, "updated": now,
		"title": []byte(`{"en":"Hello","ru":"Привет"}`),
	}
	buf, err := marshalRecordLoc(spec, row, nil, "ru")
	if err != nil {
		t.Fatalf("marshalRecordLoc: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(buf, &got)
	if got["title"] != "Привет" {
		t.Errorf("expected Привет for ru locale; got %v", got["title"])
	}
}

func TestMarshalRecord_Translatable_FallsBackToBase(t *testing.T) {
	spec := translatableSpec()
	now := time.Date(2026, 5, 10, 12, 34, 56, 789_000_000, time.UTC)
	row := map[string]any{
		"id":      "0190a3a8-0000-7000-8000-000000000001",
		"created": now, "updated": now,
		"title": []byte(`{"en":"Hello","pt":"Olá"}`),
	}
	// pt-BR has no exact entry → falls back to base "pt".
	buf, err := marshalRecordLoc(spec, row, nil, "pt-BR")
	if err != nil {
		t.Fatalf("marshalRecordLoc: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(buf, &got)
	if got["title"] != "Olá" {
		t.Errorf("expected base-language fallback to pt; got %v", got["title"])
	}
}

func TestMarshalRecord_Translatable_FallsBackAlphabeticallyOnUnknownLocale(t *testing.T) {
	spec := translatableSpec()
	now := time.Date(2026, 5, 10, 12, 34, 56, 789_000_000, time.UTC)
	row := map[string]any{
		"id":      "0190a3a8-0000-7000-8000-000000000001",
		"created": now, "updated": now,
		"title": []byte(`{"zz":"Z","aa":"A","mm":"M"}`),
	}
	buf, err := marshalRecordLoc(spec, row, nil, "fr")
	if err != nil {
		t.Fatalf("marshalRecordLoc: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(buf, &got)
	if got["title"] != "A" {
		t.Errorf("expected alphabetical-first fallback (aa=A); got %v", got["title"])
	}
}

func TestCoerceForPG_Translatable_AcceptsValidShape(t *testing.T) {
	spec := translatableSpec()
	got, err := coerceForPG(spec, "title", map[string]any{"en": "Hello", "ru": "Привет"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buf, ok := got.([]byte)
	if !ok {
		t.Fatalf("expected []byte, got %T", got)
	}
	var m map[string]string
	if err := json.Unmarshal(buf, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if m["en"] != "Hello" || m["ru"] != "Привет" {
		t.Errorf("round-trip mismatch: %v", m)
	}
}

func TestCoerceForPG_Translatable_RejectsInvalidLocaleKey(t *testing.T) {
	spec := translatableSpec()
	cases := []map[string]any{
		{"xx-YYY": "v"},   // 3-letter region
		{"EN": "v"},        // uppercase language
		{"en_US": "v"},     // underscore separator
		{"english": "v"},   // full name
		{"e": "v"},         // 1-letter language
	}
	for _, c := range cases {
		_, err := coerceForPG(spec, "title", c)
		if err == nil {
			t.Errorf("expected rejection for %v", c)
		}
	}
}

func TestCoerceForPG_Translatable_RejectsNonStringValue(t *testing.T) {
	spec := translatableSpec()
	_, err := coerceForPG(spec, "title", map[string]any{"en": 42})
	if err == nil {
		t.Fatal("expected rejection for non-string value")
	}
	if !strings.Contains(err.Error(), "string") {
		t.Errorf("error should mention string; got %v", err)
	}
}

func TestCoerceForPG_Translatable_RejectsScalarInput(t *testing.T) {
	spec := translatableSpec()
	_, err := coerceForPG(spec, "title", "just a string")
	if err == nil {
		t.Fatal("expected rejection for plain string on translatable field")
	}
}

func TestCoerceForPG_Translatable_RejectsEmptyMap(t *testing.T) {
	spec := translatableSpec()
	_, err := coerceForPG(spec, "title", map[string]any{})
	if err == nil {
		t.Fatal("expected rejection for empty translatable map")
	}
}
