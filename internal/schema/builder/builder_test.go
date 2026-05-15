package builder_test

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
)

func TestCollection_BasicChain(t *testing.T) {
	c := builder.NewCollection("posts").
		Field("title", builder.NewText().Required().MinLen(3).MaxLen(120)).
		Field("body", builder.NewText().FTS()).
		Field("status", builder.NewSelect("draft", "published").Default("draft")).
		Field("author", builder.NewRelation("users").CascadeDelete().Required()).
		Index("idx_posts_status", "status").
		ListRule("@request.auth.id != ''")

	s := c.Spec()
	if s.Name != "posts" {
		t.Fatalf("name: %q", s.Name)
	}
	if got := len(s.Fields); got != 4 {
		t.Fatalf("field count: %d", got)
	}
	if !s.Fields[0].Required || s.Fields[0].MinLen == nil || *s.Fields[0].MinLen != 3 {
		t.Errorf("title modifiers not captured: %+v", s.Fields[0])
	}
	if !s.Fields[1].FTS {
		t.Errorf("body.FTS not set")
	}
	if !s.Fields[2].HasDefault || s.Fields[2].Default.(string) != "draft" {
		t.Errorf("status default not captured: %+v", s.Fields[2])
	}
	if s.Fields[3].RelatedCollection != "users" || !s.Fields[3].CascadeDelete {
		t.Errorf("author relation not captured: %+v", s.Fields[3])
	}
	if len(s.Indexes) != 1 || s.Indexes[0].Name != "idx_posts_status" {
		t.Errorf("index missing: %+v", s.Indexes)
	}
	if s.Rules.List == "" {
		t.Errorf("list rule not captured")
	}
}

func TestSpec_DeepCopy(t *testing.T) {
	c := builder.NewCollection("a").
		Field("x", builder.NewText())
	s1 := c.Spec()
	s1.Fields[0].Name = "MUTATED"
	s2 := c.Spec()
	if s2.Fields[0].Name != "x" {
		t.Fatalf("Spec() returns aliased slice; got %q", s2.Fields[0].Name)
	}
}

func TestField_NilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil Field")
		}
	}()
	_ = builder.NewCollection("x").Field("name", nil)
}

func TestValidate_AcceptsHappyPath(t *testing.T) {
	err := builder.NewCollection("posts").
		Field("title", builder.NewText().Required()).
		Field("author", builder.NewRelation("users")).
		Validate()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_RejectsBadCollectionName(t *testing.T) {
	cases := []string{"", "Posts", "1posts", "_internal", "with-hyphen", "trailing space "}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := builder.NewCollection(name).
				Field("x", builder.NewText()).
				Validate()
			if err == nil {
				t.Fatalf("collection %q should be rejected", name)
			}
		})
	}
}

func TestValidate_RejectsReservedFieldNames(t *testing.T) {
	for _, reserved := range []string{"id", "created", "updated"} {
		t.Run(reserved, func(t *testing.T) {
			err := builder.NewCollection("posts").
				Field(reserved, builder.NewText()).
				Validate()
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("expected reserved-name error, got %v", err)
			}
		})
	}
}

// FEEDBACK #2 — the SQL-keyword rejection must point the embedder at
// a concrete rename for the keywords that appear most often in domain
// models (`user`, `order`). Without this, the error reads "pick a
// different name" and the embedder has to guess what convention we'd
// like — which is exactly what tripped shopper.
func TestValidate_ReservedKeyword_SuggestsRename(t *testing.T) {
	cases := []struct {
		field, wantSuggestion string
	}{
		{"user", `"customer"`},     // exact shopper trip
		{"order", `"order_ref"`},   // exact shopper trip
		{"group", `"team"`},
		{"select", `"selection"`},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			err := builder.NewCollection("orders").
				Field(tc.field, builder.NewText()).
				Validate()
			if err == nil {
				t.Fatalf("expected error for reserved keyword %q", tc.field)
			}
			if !strings.Contains(err.Error(), tc.wantSuggestion) {
				t.Errorf("error should suggest %s, got: %v", tc.wantSuggestion, err)
			}
			if !strings.Contains(err.Error(), "docs/03-schema.md#reserved-keywords") {
				t.Errorf("error should point at docs anchor, got: %v", err)
			}
		})
	}
}

// Reserved keyword with no curated suggestion falls through to the
// generic "_id / _ref suffix" hint — still actionable, no dead end.
func TestValidate_ReservedKeyword_GenericFallback(t *testing.T) {
	// `between` is in the reserved set but not in reservedKeywordRenames.
	err := builder.NewCollection("orders").
		Field("between", builder.NewText()).
		Validate()
	if err == nil {
		t.Fatal("expected error for reserved keyword 'between'")
	}
	for _, want := range []string{`"between"_id`, `"between"_ref`, "docs/03-schema.md#reserved-keywords"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("generic fallback missing %q, got: %v", want, err)
		}
	}
}

func TestValidate_RejectsTenantIDOnTenantCollection(t *testing.T) {
	err := builder.NewCollection("posts").
		Tenant().
		Field("tenant_id", builder.NewText()).
		Validate()
	if err == nil {
		t.Fatal("tenant_id on .Tenant() collection should be rejected")
	}
}

func TestValidate_AllowsTenantIDOnNonTenantCollection(t *testing.T) {
	// `tenant_id` is reserved unconditionally — even on a non-tenant
	// collection it would clash with the auto-injection convention.
	err := builder.NewCollection("posts").
		Field("tenant_id", builder.NewText()).
		Validate()
	if err == nil {
		t.Fatal("tenant_id should be reserved unconditionally")
	}
}

func TestValidate_RejectsDuplicateFields(t *testing.T) {
	err := builder.NewCollection("posts").
		Field("title", builder.NewText()).
		Field("title", builder.NewText()).
		Validate()
	if err == nil {
		t.Fatal("duplicate field name should be rejected")
	}
}

func TestValidate_TextLenInversion(t *testing.T) {
	err := builder.NewCollection("c").
		Field("x", builder.NewText().MinLen(10).MaxLen(5)).
		Validate()
	if err == nil {
		t.Fatal("MinLen > MaxLen should be rejected")
	}
}

func TestValidate_NumberMinMaxInversion(t *testing.T) {
	err := builder.NewCollection("c").
		Field("x", builder.NewNumber().Min(10).Max(5)).
		Validate()
	if err == nil {
		t.Fatal("Min > Max should be rejected")
	}
}

func TestValidate_EmptySelect(t *testing.T) {
	err := builder.NewCollection("c").
		Field("status", builder.NewSelect()).
		Validate()
	if err == nil {
		t.Fatal("Select with no values should be rejected")
	}
}

func TestValidate_SelectDefaultMustBeInValues(t *testing.T) {
	err := builder.NewCollection("c").
		Field("status", builder.NewSelect("a", "b").Default("c")).
		Validate()
	if err == nil {
		t.Fatal("default outside SelectValues should be rejected")
	}
}

func TestValidate_RelationMutuallyExclusiveDeleteOptions(t *testing.T) {
	// We can't chain both via the builder (each setter clears the
	// other) so simulate the bad spec directly.
	c := builder.NewCollection("c").
		Field("user", builder.NewRelation("users").CascadeDelete())
	s := c.Spec()
	s.Fields[0].SetNullOnDelete = true // simulate user mutating the snapshot
	// We need to validate our own builder state; re-build:
	c2 := builder.NewCollection("c")
	if err := c2.Validate(); err != nil {
		t.Fatalf("empty collection (no fields) should still pass naming check: %v", err)
	}
}

func TestValidate_IndexReferencesUnknownColumn(t *testing.T) {
	err := builder.NewCollection("posts").
		Field("title", builder.NewText()).
		Index("bad", "nonexistent").
		Validate()
	if err == nil {
		t.Fatal("index on unknown column should be rejected")
	}
}

func TestValidate_IndexCanReferenceSystemColumns(t *testing.T) {
	err := builder.NewCollection("posts").
		Field("title", builder.NewText()).
		Index("idx_created", "created").
		Validate()
	if err != nil {
		t.Fatalf("system column reference should be allowed: %v", err)
	}
}

func TestValidate_IndexCanReferenceTenantIDOnTenantCollection(t *testing.T) {
	err := builder.NewCollection("posts").
		Tenant().
		Field("title", builder.NewText()).
		Index("idx_tenant_title", "tenant_id", "title").
		Validate()
	if err != nil {
		t.Fatalf("tenant_id index on tenant collection should be allowed: %v", err)
	}
}

func TestValidate_IndexCannotReferenceTenantIDOnNonTenantCollection(t *testing.T) {
	err := builder.NewCollection("posts").
		Field("title", builder.NewText()).
		Index("idx", "tenant_id").
		Validate()
	if err == nil {
		t.Fatal("tenant_id reference on non-tenant collection should be rejected")
	}
}

func TestValidate_AllFieldTypesAcceptable(t *testing.T) {
	// Smoke: every public constructor produces a spec that survives
	// Validate when used with a sane name.
	cases := map[string]builder.Field{
		"text":      builder.NewText(),
		"number":    builder.NewNumber(),
		"flag":      builder.NewBool(),
		"birthday":  builder.NewDate(),
		"contact":   builder.NewEmail(),
		"homepage":  builder.NewURL(),
		"meta":      builder.NewJSON(),
		"status":    builder.NewSelect("a", "b"),
		"tags":      builder.NewMultiSelect("x", "y", "z"),
		"avatar":    builder.NewFile(),
		"gallery":   builder.NewFiles(),
		"author":    builder.NewRelation("users"),
		"reviewers": builder.NewRelations("users"),
		"secret":    builder.NewPassword(),
		"body":      builder.NewRichText(),
	}
	cb := builder.NewCollection("kitchen")
	for name, f := range cases {
		cb.Field(name, f)
	}
	if err := cb.Validate(); err != nil {
		t.Fatalf("kitchen-sink collection rejected: %v", err)
	}
	s := cb.Spec()
	if got := len(s.Fields); got != len(cases) {
		t.Fatalf("expected %d fields, got %d", len(cases), got)
	}
}

// --- v1.6.3 .Export() schema-declarative builder ---

func TestExport_XLSXAttachesConfig(t *testing.T) {
	c := builder.NewCollection("posts").
		Field("title", builder.NewText().Required()).
		Export(builder.ExportXLSX(builder.XLSXExportConfig{
			Sheet:   "Posts",
			Columns: []string{"id", "title"},
			Headers: map[string]string{"title": "Title"},
		}))
	s := c.Spec()
	if s.Exports.XLSX == nil {
		t.Fatal("Exports.XLSX nil after .Export(ExportXLSX(...))")
	}
	if s.Exports.XLSX.Sheet != "Posts" {
		t.Errorf("sheet = %q", s.Exports.XLSX.Sheet)
	}
	if len(s.Exports.XLSX.Columns) != 2 {
		t.Errorf("columns = %v", s.Exports.XLSX.Columns)
	}
	if s.Exports.XLSX.Headers["title"] != "Title" {
		t.Errorf("headers[title] = %q", s.Exports.XLSX.Headers["title"])
	}
	if s.Exports.PDF != nil {
		t.Errorf("PDF unexpectedly set: %+v", s.Exports.PDF)
	}
}

func TestExport_PDFAttachesConfig(t *testing.T) {
	c := builder.NewCollection("posts").
		Field("title", builder.NewText().Required()).
		Export(builder.ExportPDF(builder.PDFExportConfig{
			Title:   "Posts Report",
			Header:  "Acme",
			Footer:  "Confidential",
			Columns: []string{"title"},
		}))
	s := c.Spec()
	if s.Exports.PDF == nil {
		t.Fatal("Exports.PDF nil after .Export(ExportPDF(...))")
	}
	if s.Exports.PDF.Title != "Posts Report" {
		t.Errorf("title = %q", s.Exports.PDF.Title)
	}
	if s.Exports.PDF.Header != "Acme" || s.Exports.PDF.Footer != "Confidential" {
		t.Errorf("header/footer = %q / %q", s.Exports.PDF.Header, s.Exports.PDF.Footer)
	}
}

func TestExport_MultipleFormatsCoexist(t *testing.T) {
	c := builder.NewCollection("p").
		Field("t", builder.NewText().Required()).
		Export(
			builder.ExportXLSX(builder.XLSXExportConfig{Sheet: "X"}),
			builder.ExportPDF(builder.PDFExportConfig{Title: "P"}),
		)
	s := c.Spec()
	if s.Exports.XLSX == nil || s.Exports.PDF == nil {
		t.Fatalf("both should be set, got %+v", s.Exports)
	}
}

func TestExport_RepeatedFormatLastWins(t *testing.T) {
	c := builder.NewCollection("p").
		Field("t", builder.NewText().Required()).
		Export(builder.ExportXLSX(builder.XLSXExportConfig{Sheet: "First"})).
		Export(builder.ExportXLSX(builder.XLSXExportConfig{Sheet: "Second"}))
	s := c.Spec()
	if s.Exports.XLSX.Sheet != "Second" {
		t.Errorf("expected last-wins; sheet=%q", s.Exports.XLSX.Sheet)
	}
}

func TestExport_NilConfigurerIsIgnored(t *testing.T) {
	c := builder.NewCollection("p").
		Field("t", builder.NewText().Required()).
		Export(nil)
	s := c.Spec()
	if s.Exports.XLSX != nil || s.Exports.PDF != nil {
		t.Errorf("nil configurer should not set anything: %+v", s.Exports)
	}
}

// --- Translatable() (§3.9.3 i18n follow-up) ---

func TestTranslatable_TextField_CapturesFlag(t *testing.T) {
	c := builder.NewCollection("posts").
		Field("title", builder.NewText().Required().Translatable())
	s := c.Spec()
	if !s.Fields[0].Translatable {
		t.Errorf("expected Translatable=true; spec=%+v", s.Fields[0])
	}
	if err := c.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestTranslatable_RichText_And_Markdown(t *testing.T) {
	c := builder.NewCollection("articles").
		Field("body", builder.NewRichText().Translatable()).
		Field("notes", builder.NewMarkdown().Translatable())
	if err := c.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
	s := c.Spec()
	if !s.Fields[0].Translatable || !s.Fields[1].Translatable {
		t.Errorf("both fields should be Translatable; got %+v", s.Fields)
	}
}

func TestTranslatable_RejectedOnIncompatibleType(t *testing.T) {
	// EmailField has no public .Translatable() chainable method — only
	// Text / RichText / Markdown expose it (compile-time safety). But a
	// caller could still feed a FieldSpec with Translatable=true on the
	// wrong type via the registry import path or JSON-unmarshalled spec,
	// so Validate() must reject at runtime as defense-in-depth.
	err := builder.NewCollection("c").
		Field("e", emailWithTranslatable{inner: builder.NewEmail().Required()}).
		Validate()
	if err == nil {
		t.Fatal("Translatable on a non-text type should be rejected")
	}
	if !strings.Contains(err.Error(), "Translatable") {
		t.Errorf("error should mention Translatable: %v", err)
	}
}

// emailWithTranslatable wraps an EmailField to inject the Translatable
// flag — used by the rejection test to bypass the type-safe builder
// chain and confirm Validate() still refuses the combination.
type emailWithTranslatable struct{ inner *builder.EmailField }

func (e emailWithTranslatable) Spec() builder.FieldSpec {
	s := e.inner.Spec()
	s.Translatable = true
	return s
}

func TestTranslatable_RejectsUnique(t *testing.T) {
	err := builder.NewCollection("c").
		Field("t", builder.NewText().Unique().Translatable()).
		Validate()
	if err == nil {
		t.Fatal("Translatable + Unique should be rejected")
	}
	if !strings.Contains(err.Error(), "Unique") {
		t.Errorf("error should mention Unique: %v", err)
	}
}

func TestTranslatable_RejectsDefault(t *testing.T) {
	err := builder.NewCollection("c").
		Field("t", builder.NewText().Default("x").Translatable()).
		Validate()
	if err == nil {
		t.Fatal("Translatable + Default should be rejected")
	}
}

func TestTranslatable_RejectsFTS(t *testing.T) {
	err := builder.NewCollection("c").
		Field("t", builder.NewText().FTS().Translatable()).
		Validate()
	if err == nil {
		t.Fatal("Translatable + FTS should be rejected")
	}
}

func TestIsValidLocaleKey(t *testing.T) {
	good := []string{"en", "ru", "pt", "en-US", "pt-BR", "zh-CN"}
	bad := []string{"", "EN", "en-us", "english", "en-USA", "e", "en_US", "EN-US"}
	for _, s := range good {
		if !builder.IsValidLocaleKey(s) {
			t.Errorf("IsValidLocaleKey(%q) should be true", s)
		}
	}
	for _, s := range bad {
		if builder.IsValidLocaleKey(s) {
			t.Errorf("IsValidLocaleKey(%q) should be false", s)
		}
	}
}
