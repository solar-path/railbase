package ts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/sdkgen"
)

// fixtureSpecs builds a representative set: one regular collection
// covering most field types, one auth collection. The generated
// output is asserted against expected substrings — keeps the test
// fragile in the right places (semantics) without locking us into
// exact whitespace.
func fixtureSpecs() []builder.CollectionSpec {
	min := 3
	max := 120
	return []builder.CollectionSpec{
		{
			Name: "posts",
			Fields: []builder.FieldSpec{
				{Name: "title", Type: builder.TypeText, Required: true, MinLen: &min, MaxLen: &max},
				{Name: "body", Type: builder.TypeText, FTS: true},
				{Name: "status", Type: builder.TypeSelect, Required: true, SelectValues: []string{"draft", "published"}},
				{Name: "tags", Type: builder.TypeMultiSelect, SelectValues: []string{"go", "ts", "pg"}},
			},
		},
		{
			Name: "users",
			Auth: true,
		},
	}
}

func TestEmitTypes_RowInterface(t *testing.T) {
	out := EmitTypes(fixtureSpecs())
	for _, want := range []string{
		"export interface Posts {",
		"id: string;",
		"created: string;",
		`title: string;`,
		`body?: string;`,
		`status: "draft" | "published";`,
		`tags?: ("go" | "ts" | "pg")[];`,
		"export interface Users {",
		"email: string;",
		"verified: boolean;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("EmitTypes missing %q\noutput:\n%s", want, out)
		}
	}
	if strings.Contains(out, "password_hash") {
		t.Errorf("EmitTypes leaked password_hash:\n%s", out)
	}
}

func TestEmitZod_HonoursConstraints(t *testing.T) {
	out := EmitZod(fixtureSpecs())
	for _, want := range []string{
		"PostsSchema",
		"PostsInputSchema",
		`title: z.string().min(3).max(120),`,
		`status: z.enum(["draft", "published"])`,
		`tags: z.array(z.enum(["go", "ts", "pg"])).nullable().optional()`,
		"UsersSchema",
		"email: z.string().email(),",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("EmitZod missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestEmitAuth_OnlyAuthCollections(t *testing.T) {
	out := EmitAuth(fixtureSpecs())
	if strings.Contains(out, "postsAuth") {
		t.Errorf("EmitAuth emitted wrapper for non-auth collection:\n%s", out)
	}
	for _, want := range []string{
		"export function usersAuth(http: HTTPClient)",
		"/api/collections/users/auth-signup",
		"/api/collections/users/auth-with-password",
		"/api/collections/users/auth-refresh",
		"/api/collections/users/auth-logout",
		"export async function getMe<T",
		// v1.7.0 PB-compat discovery
		"export interface AuthMethods",
		"/api/collections/users/auth-methods",
		"authMethods(): Promise<AuthMethods>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("EmitAuth missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestEmitCollection_AuthSkipsCreate(t *testing.T) {
	specs := fixtureSpecs()
	users := specs[1]
	out := EmitCollection(users)
	if strings.Contains(out, "create(input:") {
		t.Errorf("EmitCollection emitted create() for auth collection:\n%s", out)
	}
	for _, want := range []string{
		"usersCollection(http: HTTPClient)",
		"/api/collections/users/records",
		"update(id: string, input:",
		"delete(id: string)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("EmitCollection missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestGenerate_WritesFullTree(t *testing.T) {
	dir := t.TempDir()
	files, err := Generate(fixtureSpecs(), Options{
		OutDir: dir,
		Now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantFiles := []string{
		"_meta.json",
		"auth.ts",
		"collections/posts.ts",
		"collections/users.ts",
		"errors.ts",
		"i18n.ts",
		"index.ts",
		"notifications.ts",
		"realtime.ts",
		"stripe.ts",
		"types.ts",
		"zod.ts",
	}
	for _, w := range wantFiles {
		got := false
		for _, f := range files {
			if f == w {
				got = true
				break
			}
		}
		if !got {
			t.Errorf("missing generated file %s in %v", w, files)
		}
		if _, err := os.Stat(filepath.Join(dir, w)); err != nil {
			t.Errorf("not on disk: %s: %v", w, err)
		}
	}

	// Meta has the right hash.
	body, err := os.ReadFile(filepath.Join(dir, "_meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m sdkgen.Meta
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.SchemaHash, "sha256:") {
		t.Errorf("_meta.json schemaHash malformed: %q", m.SchemaHash)
	}
	if !m.GeneratedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("_meta.json generatedAt = %v, want pinned 2026-01-01", m.GeneratedAt)
	}
}

func TestSchemaHash_DeterministicAndSensitive(t *testing.T) {
	a, err := sdkgen.SchemaHash(fixtureSpecs())
	if err != nil {
		t.Fatal(err)
	}
	b, err := sdkgen.SchemaHash(fixtureSpecs())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("SchemaHash not deterministic: %s vs %s", a, b)
	}
	mut := fixtureSpecs()
	mut[0].Fields[0].Required = !mut[0].Fields[0].Required
	c, err := sdkgen.SchemaHash(mut)
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Errorf("SchemaHash insensitive to field-spec change")
	}
}
