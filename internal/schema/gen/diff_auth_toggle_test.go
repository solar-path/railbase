// Regression tests for FEEDBACK #B1 — `migrate diff` was silently
// missing the Collection ↔ AuthCollection transition because the
// auth-injected columns (email, password_hash, …) live on the
// spec.Auth flag rather than spec.Fields. The blogger project hit
// this exactly: schema.Collection("authors") → schema.AuthCollection
// produced "schema unchanged" and the embedder had to write the
// migration by hand.
//
// These tests cover both directions of the toggle plus the unchanged
// case (toggle stays the same in both snapshots).
package gen_test

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
)

// Helper — build a CollectionSpec with the given name + auth flag,
// plus one user field so the spec is non-trivial.
func makeSpec(name string, auth bool) builder.CollectionSpec {
	return builder.CollectionSpec{
		Name: name,
		Auth: auth,
		Fields: []builder.FieldSpec{
			{Name: "display_name", Type: builder.TypeText},
		},
	}
}

// TestDiff_AuthToggle_OnDetected — the headline case. Collection
// becomes Auth → diff records it and emits the multi-step ALTER.
func TestDiff_AuthToggle_OnDetected(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{makeSpec("authors", false)})
	curr := gen.SnapshotOf([]builder.CollectionSpec{makeSpec("authors", true)})
	d := gen.Compute(prev, curr)

	if d.Empty() {
		t.Fatalf("expected non-empty diff for Auth toggle, got Empty=true")
	}
	if len(d.AuthToggles) != 1 {
		t.Fatalf("expected 1 AuthToggle, got %d: %+v", len(d.AuthToggles), d.AuthToggles)
	}
	at := d.AuthToggles[0]
	if at.Collection != "authors" {
		t.Errorf("AuthToggle collection: got %q, want \"authors\"", at.Collection)
	}
	if !at.NewState {
		t.Errorf("AuthToggle NewState: got false, want true (toggle-on)")
	}

	sql := d.SQL()
	for _, want := range []string{
		"-- AuthCollection toggle",
		"ADD COLUMN email TEXT",
		"ADD COLUMN password_hash TEXT",
		"ADD COLUMN verified BOOLEAN NOT NULL DEFAULT FALSE",
		"ADD COLUMN token_key TEXT",
		"ADD COLUMN last_login_at TIMESTAMPTZ NULL",
		// Backfill TODO must appear for the no-default columns.
		"TODO: backfill expression",
		// And the SET NOT NULL must wait until after the backfill.
		"SET NOT NULL",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("Auth-toggle SQL missing %q\nfull SQL:\n%s", want, sql)
		}
	}

	// Critically, the migration must NOT try to SET NOT NULL on
	// password_hash without the operator backfilling first — the
	// three-step pattern guarantees a separate UPDATE step.
	idxAdd := strings.Index(sql, "ADD COLUMN password_hash TEXT;")
	idxUpd := strings.Index(sql, "UPDATE authors SET password_hash =")
	idxSet := strings.Index(sql, "ALTER COLUMN password_hash SET NOT NULL")
	if idxAdd < 0 || idxUpd < 0 || idxSet < 0 {
		t.Fatalf("password_hash three-step pattern incomplete:\n%s", sql)
	}
	if !(idxAdd < idxUpd && idxUpd < idxSet) {
		t.Errorf("password_hash steps out of order: add=%d upd=%d set=%d", idxAdd, idxUpd, idxSet)
	}
}

// TestDiff_AuthToggle_OffDetected — toggle-off path drops the
// auth-injected columns. Operator who turns Auth off for an existing
// collection accepts the data loss explicitly.
func TestDiff_AuthToggle_OffDetected(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{makeSpec("authors", true)})
	curr := gen.SnapshotOf([]builder.CollectionSpec{makeSpec("authors", false)})
	d := gen.Compute(prev, curr)

	if len(d.AuthToggles) != 1 {
		t.Fatalf("expected 1 AuthToggle, got %+v", d.AuthToggles)
	}
	if d.AuthToggles[0].NewState {
		t.Errorf("toggle-off should have NewState=false, got true")
	}
	sql := d.SQL()
	for _, want := range []string{
		"DROP COLUMN IF EXISTS email CASCADE",
		"DROP COLUMN IF EXISTS password_hash CASCADE",
		"DROP COLUMN IF EXISTS verified CASCADE",
		"DROP COLUMN IF EXISTS token_key CASCADE",
		"DROP COLUMN IF EXISTS last_login_at CASCADE",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("toggle-off SQL missing %q\n%s", want, sql)
		}
	}
}

// TestDiff_AuthToggle_StableNoChange — toggle stays the same on both
// sides → no AuthToggle entry. Guards against an accidental emit on
// every diff.
func TestDiff_AuthToggle_StableNoChange(t *testing.T) {
	for _, auth := range []bool{false, true} {
		spec := makeSpec("authors", auth)
		d := gen.Compute(
			gen.SnapshotOf([]builder.CollectionSpec{spec}),
			gen.SnapshotOf([]builder.CollectionSpec{spec}),
		)
		if !d.Empty() || len(d.AuthToggles) != 0 {
			t.Errorf("auth=%v unchanged should yield empty diff, got %+v", auth, d)
		}
	}
}

// TestDiff_AuthToggle_AddedFieldComesAfter — when an Auth-toggle AND
// a new user field land in the same diff, the toggle SQL must appear
// before the ADD COLUMN for the user field. Otherwise the user field
// might reference an auth-injected column (uncommon but possible).
func TestDiff_AuthToggle_AddedFieldComesAfter(t *testing.T) {
	prev := gen.SnapshotOf([]builder.CollectionSpec{makeSpec("authors", false)})
	currSpec := builder.CollectionSpec{
		Name: "authors",
		Auth: true,
		Fields: []builder.FieldSpec{
			{Name: "display_name", Type: builder.TypeText},
			{Name: "bio", Type: builder.TypeText},
		},
	}
	curr := gen.SnapshotOf([]builder.CollectionSpec{currSpec})
	d := gen.Compute(prev, curr)
	sql := d.SQL()

	idxToggle := strings.Index(sql, "AuthCollection toggle")
	idxBio := strings.Index(sql, "add \"authors\".\"bio\"")
	if idxToggle < 0 || idxBio < 0 {
		t.Fatalf("missing toggle or field-add section:\n%s", sql)
	}
	if !(idxToggle < idxBio) {
		t.Errorf("toggle SQL must appear BEFORE user-field add (idxToggle=%d, idxBio=%d)", idxToggle, idxBio)
	}
}
