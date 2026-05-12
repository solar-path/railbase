package gen_test

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
)

func TestDiff_NoChanges(t *testing.T) {
	specs := []builder.CollectionSpec{
		{Name: "posts", Fields: []builder.FieldSpec{
			{Name: "title", Type: builder.TypeText, Required: true},
		}},
	}
	d := gen.Compute(gen.SnapshotOf(specs), gen.SnapshotOf(specs))
	if !d.Empty() {
		t.Fatalf("expected empty diff, got %+v", d)
	}
	if got := d.SQL(); got != "" {
		t.Errorf("expected empty SQL, got %q", got)
	}
}

func TestDiff_NewCollection(t *testing.T) {
	curr := []builder.CollectionSpec{
		{Name: "posts", Fields: []builder.FieldSpec{
			{Name: "title", Type: builder.TypeText, Required: true},
		}},
	}
	d := gen.Compute(gen.Snapshot{}, gen.SnapshotOf(curr))

	if len(d.NewCollections) != 1 || d.NewCollections[0].Name != "posts" {
		t.Fatalf("expected new collection 'posts', got %+v", d.NewCollections)
	}
	sql := d.SQL()
	if !strings.Contains(sql, "CREATE TABLE posts (") {
		t.Errorf("SQL missing CREATE TABLE:\n%s", sql)
	}
}

func TestDiff_DroppedCollection(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{{
		Name: "obsolete",
		Fields: []builder.FieldSpec{
			{Name: "x", Type: builder.TypeText},
		},
	}})
	d := gen.Compute(prev, gen.Snapshot{})

	if len(d.DroppedCollections) != 1 || d.DroppedCollections[0] != "obsolete" {
		t.Fatalf("expected drop of 'obsolete', got %+v", d.DroppedCollections)
	}
	sql := d.SQL()
	if !strings.Contains(sql, "DROP TABLE IF EXISTS obsolete") {
		t.Errorf("SQL missing DROP TABLE:\n%s", sql)
	}
}

func TestDiff_AddField(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{{
		Name: "posts",
		Fields: []builder.FieldSpec{
			{Name: "title", Type: builder.TypeText, Required: true},
		},
	}})
	curr := gen.SnapshotOf([]builder.CollectionSpec{{
		Name: "posts",
		Fields: []builder.FieldSpec{
			{Name: "title", Type: builder.TypeText, Required: true},
			{Name: "body", Type: builder.TypeText},
		},
	}})
	d := gen.Compute(prev, curr)

	if len(d.FieldChanges) != 1 || len(d.FieldChanges[0].Added) != 1 {
		t.Fatalf("expected one added field; got %+v", d.FieldChanges)
	}
	if d.FieldChanges[0].Added[0].Name != "body" {
		t.Errorf("wrong field added: %s", d.FieldChanges[0].Added[0].Name)
	}
	sql := d.SQL()
	if !strings.Contains(sql, "ALTER TABLE posts ADD COLUMN body TEXT") {
		t.Errorf("ADD COLUMN missing:\n%s", sql)
	}
}

func TestDiff_DropField(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{{
		Name: "posts",
		Fields: []builder.FieldSpec{
			{Name: "title", Type: builder.TypeText},
			{Name: "body", Type: builder.TypeText},
		},
	}})
	curr := gen.SnapshotOf([]builder.CollectionSpec{{
		Name: "posts",
		Fields: []builder.FieldSpec{
			{Name: "title", Type: builder.TypeText},
		},
	}})
	d := gen.Compute(prev, curr)

	if len(d.FieldChanges) != 1 || len(d.FieldChanges[0].Dropped) != 1 ||
		d.FieldChanges[0].Dropped[0] != "body" {
		t.Fatalf("expected drop of 'body'; got %+v", d.FieldChanges)
	}
}

func TestDiff_TypeChange_Incompatible(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{{
		Name: "posts",
		Fields: []builder.FieldSpec{
			{Name: "rank", Type: builder.TypeText},
		},
	}})
	curr := gen.SnapshotOf([]builder.CollectionSpec{{
		Name: "posts",
		Fields: []builder.FieldSpec{
			{Name: "rank", Type: builder.TypeNumber},
		},
	}})
	d := gen.Compute(prev, curr)

	if !d.HasIncompatible() {
		t.Fatalf("expected incompatible change for type swap; got %+v", d)
	}
	if !strings.Contains(d.IncompatibleChanges[0], "type change") {
		t.Errorf("unexpected incompatible message: %v", d.IncompatibleChanges)
	}
}

func TestDiff_TenantToggle_Incompatible(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{{
		Name:   "posts",
		Tenant: false,
		Fields: []builder.FieldSpec{{Name: "title", Type: builder.TypeText}},
	}})
	curr := gen.SnapshotOf([]builder.CollectionSpec{{
		Name:   "posts",
		Tenant: true,
		Fields: []builder.FieldSpec{{Name: "title", Type: builder.TypeText}},
	}})
	d := gen.Compute(prev, curr)
	if !d.HasIncompatible() {
		t.Fatal("Tenant toggle should be flagged incompatible")
	}
}

func TestDiff_AddIndex(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{{
		Name:   "posts",
		Fields: []builder.FieldSpec{{Name: "title", Type: builder.TypeText}},
	}})
	curr := gen.SnapshotOf([]builder.CollectionSpec{{
		Name:   "posts",
		Fields: []builder.FieldSpec{{Name: "title", Type: builder.TypeText}},
		Indexes: []builder.IndexSpec{
			{Name: "idx_posts_title", Columns: []string{"title"}},
		},
	}})
	d := gen.Compute(prev, curr)

	if len(d.IndexChanges) != 1 || len(d.IndexChanges[0].Added) != 1 {
		t.Fatalf("expected one added index; got %+v", d.IndexChanges)
	}
	sql := d.SQL()
	if !strings.Contains(sql, "CREATE INDEX idx_posts_title ON posts (title);") {
		t.Errorf("CREATE INDEX missing:\n%s", sql)
	}
}

func TestDiff_SQL_Ordering(t *testing.T) {
	// Mixed: add a new collection, drop an old one, add a field to
	// a third. SQL must order: creates → alters → drops.
	prev := gen.SnapshotOf([]builder.CollectionSpec{
		{Name: "old", Fields: []builder.FieldSpec{{Name: "x", Type: builder.TypeText}}},
		{Name: "shared", Fields: []builder.FieldSpec{{Name: "x", Type: builder.TypeText}}},
	})
	curr := gen.SnapshotOf([]builder.CollectionSpec{
		{Name: "shared", Fields: []builder.FieldSpec{
			{Name: "x", Type: builder.TypeText},
			{Name: "y", Type: builder.TypeText}, // added
		}},
		{Name: "new", Fields: []builder.FieldSpec{{Name: "z", Type: builder.TypeText}}},
	})
	sql := gen.Compute(prev, curr).SQL()

	createIdx := strings.Index(sql, "CREATE TABLE new")
	alterIdx := strings.Index(sql, "ALTER TABLE shared ADD COLUMN y")
	dropIdx := strings.Index(sql, "DROP TABLE IF EXISTS old")

	if createIdx < 0 || alterIdx < 0 || dropIdx < 0 {
		t.Fatalf("missing one of create/alter/drop:\n%s", sql)
	}
	if !(createIdx < alterIdx && alterIdx < dropIdx) {
		t.Errorf("expected create < alter < drop; got %d < %d < %d\n%s",
			createIdx, alterIdx, dropIdx, sql)
	}
}
