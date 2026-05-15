package gen_test

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
)

func TestCreateCollectionSQL_BasicShape(t *testing.T) {
	spec := builder.NewCollection("posts").
		Field("title", builder.NewText().Required().MinLen(3).MaxLen(120)).
		Field("body", builder.NewText().FTS()).
		Field("status", builder.NewSelect("draft", "published").Default("draft").Required()).
		Spec()

	sql := gen.CreateCollectionSQL(spec)

	for _, want := range []string{
		`CREATE TABLE posts (`,
		`id          UUID         PRIMARY KEY DEFAULT gen_random_uuid()`,
		`created     TIMESTAMPTZ  NOT NULL    DEFAULT now()`,
		`updated     TIMESTAMPTZ  NOT NULL    DEFAULT now()`,
		`title TEXT NOT NULL`,
		`body TEXT`,
		`status TEXT NOT NULL DEFAULT 'draft'`,
		`CHECK (char_length(title) >= 3)`,
		`CHECK (char_length(title) <= 120)`,
		`CHECK (status IN ('draft', 'published'))`,
		// updated trigger always present
		`posts_updated_trg BEFORE UPDATE ON posts`,
		// FTS GIN index for body
		`gin (to_tsvector('simple', body))`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q\nfull SQL:\n%s", want, sql)
		}
	}
}

func TestCreateCollectionSQL_TenantEnablesRLS(t *testing.T) {
	spec := builder.NewCollection("posts").
		Tenant().
		Field("title", builder.NewText()).
		Spec()

	sql := gen.CreateCollectionSQL(spec)

	for _, want := range []string{
		`tenant_id   UUID         NOT NULL`,
		`CONSTRAINT posts_tenant_fk FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE`,
		`ENABLE ROW LEVEL SECURITY`,
		`FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY posts_tenant_isolation`,
		`current_setting('railbase.role', true) = 'app_admin'`,
		`tenant_id = NULLIF(current_setting('railbase.tenant', true), '')::uuid`,
		`CREATE INDEX posts_tenant_id_idx ON posts (tenant_id);`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q\nfull SQL:\n%s", want, sql)
		}
	}
}

func TestCreateCollectionSQL_RelationFK(t *testing.T) {
	spec := builder.NewCollection("posts").
		Field("author", builder.NewRelation("users").Required().CascadeDelete()).
		Spec()

	sql := gen.CreateCollectionSQL(spec)

	if !strings.Contains(sql, `author UUID NOT NULL`) {
		t.Errorf("relation column missing UUID NOT NULL:\n%s", sql)
	}
	if !strings.Contains(sql, `FOREIGN KEY (author) REFERENCES users(id) ON DELETE CASCADE`) {
		t.Errorf("FK clause missing:\n%s", sql)
	}
	if !strings.Contains(sql, `CREATE INDEX posts_author_fk_idx ON posts (author);`) {
		t.Errorf("FK index missing:\n%s", sql)
	}
}

func TestCreateCollectionSQL_NumberInt(t *testing.T) {
	spec := builder.NewCollection("counters").
		Field("n", builder.NewNumber().Int().Min(0).Max(1000)).
		Spec()

	sql := gen.CreateCollectionSQL(spec)
	if !strings.Contains(sql, `n BIGINT`) {
		t.Errorf("Int() not honoured:\n%s", sql)
	}
	if !strings.Contains(sql, `CHECK (n >= 0)`) || !strings.Contains(sql, `CHECK (n <= 1000)`) {
		t.Errorf("min/max checks missing:\n%s", sql)
	}
}

func TestCreateCollectionSQL_MultiSelectArray(t *testing.T) {
	spec := builder.NewCollection("items").
		Field("tags", builder.NewMultiSelect("a", "b", "c").Min(1).Max(2).Index()).
		Spec()

	sql := gen.CreateCollectionSQL(spec)
	if !strings.Contains(sql, `tags TEXT[]`) {
		t.Errorf("multiselect column type wrong:\n%s", sql)
	}
	if !strings.Contains(sql, `CHECK (tags <@ ARRAY['a', 'b', 'c']::TEXT[])`) {
		t.Errorf("subset check missing:\n%s", sql)
	}
	if !strings.Contains(sql, `CHECK (array_length(tags, 1) >= 1)`) {
		t.Errorf("min selections check missing:\n%s", sql)
	}
	if !strings.Contains(sql, `USING gin (tags)`) {
		t.Errorf("GIN index missing for indexed multiselect:\n%s", sql)
	}
}

func TestAddColumnSQL_WithCheck(t *testing.T) {
	// Required NOT NULL field WITHOUT default → FEEDBACK #19 emits
	// the three-step backfill pattern (nullable → UPDATE TODO →
	// SET NOT NULL) so existing rows don't trip 23502 at apply.
	f := builder.NewText().Required().MinLen(5).Spec()
	f.Name = "tagline"

	sql := gen.AddColumnSQL("posts", f)

	if !strings.Contains(sql, `ALTER TABLE posts ADD COLUMN tagline TEXT;`) {
		t.Errorf("step 1 (nullable ADD COLUMN) missing:\n%s", sql)
	}
	if !strings.Contains(sql, `UPDATE posts SET tagline = /* TODO: backfill expression */ NULL WHERE tagline IS NULL;`) {
		t.Errorf("step 2 (UPDATE backfill TODO) missing:\n%s", sql)
	}
	if !strings.Contains(sql, `ALTER TABLE posts ALTER COLUMN tagline SET NOT NULL;`) {
		t.Errorf("step 3 (SET NOT NULL) missing:\n%s", sql)
	}
	// The single-shot "ADD COLUMN … NOT NULL" form must NOT appear —
	// that's the pre-fix path which fails against non-empty tables.
	if strings.Contains(sql, `ADD COLUMN tagline TEXT NOT NULL`) {
		t.Errorf("unsafe single-shot NOT NULL ADD COLUMN regressed:\n%s", sql)
	}
	if !strings.Contains(sql, `ALTER TABLE posts ADD CONSTRAINT posts_tagline_chk0 CHECK`) {
		t.Errorf("CHECK constraint missing:\n%s", sql)
	}
}

// FEEDBACK #19 happy-path: fields that DO carry a usable server-side
// default keep the single-line ALTER TABLE … ADD COLUMN … NOT NULL
// form. Postgres backfills existing rows from the default at apply
// time, which is safe.
func TestAddColumnSQL_WithDefault_NoSplit(t *testing.T) {
	f := builder.NewText().Required().Default("untitled").Spec()
	f.Name = "title"

	sql := gen.AddColumnSQL("posts", f)

	if !strings.Contains(sql, `ALTER TABLE posts ADD COLUMN title TEXT NOT NULL UNIQUE DEFAULT 'untitled';`) &&
		!strings.Contains(sql, `ALTER TABLE posts ADD COLUMN title TEXT NOT NULL DEFAULT 'untitled';`) {
		t.Errorf("single-line ALTER with DEFAULT missing:\n%s", sql)
	}
	if strings.Contains(sql, `UPDATE posts SET title`) {
		t.Errorf("backfill UPDATE should NOT appear when DEFAULT is set:\n%s", sql)
	}
	if strings.Contains(sql, `ALTER COLUMN title SET NOT NULL`) {
		t.Errorf("redundant SET NOT NULL emitted when single-line form was sufficient:\n%s", sql)
	}
}

// Nullable fields (Required = false) keep the trivial form — no
// backfill needed since NULL is a valid value.
func TestAddColumnSQL_Nullable_NoSplit(t *testing.T) {
	f := builder.NewText().Spec()
	f.Name = "subtitle"

	sql := gen.AddColumnSQL("posts", f)

	if !strings.Contains(sql, `ALTER TABLE posts ADD COLUMN subtitle TEXT;`) {
		t.Errorf("trivial ALTER missing:\n%s", sql)
	}
	if strings.Contains(sql, `UPDATE posts`) {
		t.Errorf("backfill UPDATE leaked into nullable ADD:\n%s", sql)
	}
}

func TestDropColumnSQL(t *testing.T) {
	got := gen.DropColumnSQL("posts", "deprecated")
	want := "ALTER TABLE posts DROP COLUMN deprecated CASCADE;\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDropCollectionSQL(t *testing.T) {
	got := gen.DropCollectionSQL("posts")
	want := "DROP TABLE IF EXISTS posts CASCADE;\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTranslatable_EmitsJSONBWithObjectCheck(t *testing.T) {
	spec := builder.NewCollection("articles").
		Field("title", builder.NewText().Required().Translatable()).
		Spec()
	sql := gen.CreateCollectionSQL(spec)
	if !strings.Contains(sql, "title JSONB") {
		t.Errorf("title should be JSONB:\n%s", sql)
	}
	if !strings.Contains(sql, "CHECK (jsonb_typeof(title) = 'object')") {
		t.Errorf("jsonb_typeof CHECK missing:\n%s", sql)
	}
}

func TestTranslatable_IndexedEmitsGIN(t *testing.T) {
	spec := builder.NewCollection("articles").
		Field("title", builder.NewText().Required().Index().Translatable()).
		Spec()
	sql := gen.CreateCollectionSQL(spec)
	if !strings.Contains(sql, "USING gin (title)") {
		t.Errorf("translatable Indexed() should emit GIN, not btree:\n%s", sql)
	}
}

func TestEmail_CheckIncluded(t *testing.T) {
	spec := builder.NewCollection("users").
		Field("email", builder.NewEmail().Required().Unique()).
		Spec()
	sql := gen.CreateCollectionSQL(spec)
	if !strings.Contains(sql, `CHECK (email ~* '^[^@\s]+@[^@\s]+\.[^@\s]+$')`) {
		t.Errorf("email CHECK missing:\n%s", sql)
	}
}

func TestSnapshot_DeterministicOrdering(t *testing.T) {
	specs := []builder.CollectionSpec{
		{Name: "zebra", Fields: []builder.FieldSpec{
			{Name: "z", Type: builder.TypeText},
			{Name: "a", Type: builder.TypeText},
		}},
		{Name: "alpha", Fields: []builder.FieldSpec{
			{Name: "y", Type: builder.TypeText},
			{Name: "b", Type: builder.TypeText},
		}},
	}
	snap := gen.SnapshotOf(specs)
	if snap.Collections[0].Name != "alpha" || snap.Collections[1].Name != "zebra" {
		t.Fatalf("collections not sorted by name: %+v", snap.Collections)
	}
	if snap.Collections[0].Fields[0].Name != "b" {
		t.Errorf("alpha fields not sorted: %+v", snap.Collections[0].Fields)
	}
}

func TestSnapshot_RoundTrip(t *testing.T) {
	specs := []builder.CollectionSpec{
		{Name: "posts", Fields: []builder.FieldSpec{
			{Name: "title", Type: builder.TypeText, Required: true},
		}},
	}
	snap := gen.SnapshotOf(specs)
	data, err := snap.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gen.ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Collections[0].Name; got != "posts" {
		t.Errorf("round-trip lost name: %q", got)
	}
	if !parsed.Collections[0].Fields[0].Required {
		t.Errorf("round-trip lost Required")
	}
}

func TestCreateCollectionSQL_AuthCollection(t *testing.T) {
	c := builder.NewAuthCollection("users").
		Field("display_name", builder.NewText())
	got := gen.CreateCollectionSQL(c.Spec())
	for _, want := range []string{
		"email         TEXT        NOT NULL",
		"password_hash TEXT        NOT NULL",
		"verified      BOOLEAN     NOT NULL DEFAULT FALSE",
		"token_key     TEXT        NOT NULL",
		"last_login_at TIMESTAMPTZ NULL",
		"display_name",
		"users_email_idx ON users (lower(email))",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in DDL:\n%s", want, got)
		}
	}
}

func TestValidate_RefusesAuthPlusTenant(t *testing.T) {
	err := builder.NewAuthCollection("users").Tenant().Validate()
	if err == nil {
		t.Fatal("AuthCollection + .Tenant() must be rejected")
	}
	if !strings.Contains(err.Error(), "v0.4") {
		t.Errorf("error should reference v0.4 milestone: %v", err)
	}
}
