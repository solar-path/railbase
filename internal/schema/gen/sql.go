// Package gen turns CollectionSpec values into the artefacts the
// rest of the system consumes:
//
//   - SQL DDL (CREATE TABLE / ALTER TABLE / CREATE INDEX / RLS / triggers)
//   - JSON snapshots stored in _schema_snapshots, used by the diff
//     algorithm on the next `migrate diff` run.
//
// The package is pure — all functions are deterministic over their
// CollectionSpec inputs. No database I/O lives here; the migrate
// runner persists snapshots after applying generated SQL.
package gen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// CreateCollectionSQL emits the full DDL needed to bring a fresh
// database to the state described by spec:
//
//  1. CREATE TABLE with system fields, user fields, CHECK / FK
//     constraints inline.
//  2. CREATE INDEX statements for `.Indexed`, `.Unique` columns,
//     auto-FK indexes, FTS GIN indexes, user-declared indexes.
//  3. RLS enable + tenant-isolation policy when .Tenant() is set.
//  4. updated-trigger when any field has .AutoUpdate().
//
// The returned string can be dropped into a migration file as-is —
// each statement ends with a semicolon and a blank line.
func CreateCollectionSQL(spec builder.CollectionSpec) string {
	var b strings.Builder
	// Sequences for sequential_code fields must exist BEFORE the
	// CREATE TABLE that references them in column DEFAULTs.
	for _, f := range spec.Fields {
		if f.Type == builder.TypeSequentialCode {
			b.WriteString(createSequence(spec.Name, f))
		}
	}
	b.WriteString(createTable(spec))
	b.WriteString("\n")
	// Link sequences to their owning columns AFTER table exists so
	// DROP TABLE cascades into DROP SEQUENCE — matches SERIAL idiom.
	for _, f := range spec.Fields {
		if f.Type == builder.TypeSequentialCode {
			b.WriteString(ownSequence(spec.Name, f))
		}
	}
	for _, idx := range collectIndexes(spec) {
		b.WriteString(idx)
		b.WriteString("\n")
	}
	// v3.x — M2M junction tables. Every TypeRelations field gets a
	// dedicated junction `<owner>_<field>` so the generic CRUD layer
	// can read/write it through a normal pgx round-trip. Sentinel had
	// to use JSONB for `predecessors` because v0.3 didn't ship this —
	// see schema/tasks.go:13-15 comment.
	for _, f := range spec.Fields {
		if f.Type == builder.TypeRelations {
			b.WriteString(createJunction(spec.Name, f))
			b.WriteString("\n")
		}
	}
	if spec.Tenant {
		b.WriteString(tenantRLS(spec))
		b.WriteString("\n")
	}
	if hasAutoUpdate(spec) {
		b.WriteString(updatedTrigger(spec))
		b.WriteString("\n")
	}
	return b.String()
}

// createJunction emits the M2M junction table for one TypeRelations
// field. Pattern: `<owner>_<field>(owner_id, ref_id, sort_index)`.
// Both FKs cascade-delete so removing either side cleans up the row.
// PRIMARY KEY (owner_id, ref_id) prevents duplicate edges; sort_index
// lets clients preserve ordering when needed (e.g. ordered tag list).
func createJunction(owner string, f builder.FieldSpec) string {
	junction := junctionTableName(owner, f.Name)
	target := f.RelatedCollection
	ownerCol := "owner_id"
	refCol := "ref_id"
	return fmt.Sprintf(`CREATE TABLE %s (
    %s        UUID NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
    %s        UUID NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
    sort_index INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (%s, %s)
);
CREATE INDEX %s_ref_idx ON %s (%s);
`,
		quoteIdent(junction),
		ownerCol, quoteIdent(owner),
		refCol, quoteIdent(target),
		ownerCol, refCol,
		junction, quoteIdent(junction), refCol)
}

// JunctionTableName is the public helper for the M2M table name
// pattern. Exported for the REST CRUD layer to query without
// duplicating the convention.
func JunctionTableName(owner, field string) string {
	return junctionTableName(owner, field)
}

func junctionTableName(owner, field string) string {
	return owner + "_" + field
}

// DropCollectionSQL is the inverse of CreateCollectionSQL. Tables
// are dropped with CASCADE so any FK back-references die together;
// the trigger function dies with the table.
func DropCollectionSQL(name string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE;\n", quoteIdent(name))
}

// AddColumnSQL emits the ALTER TABLE statement that adds f to the
// existing table named coll. Constraints are inlined; CHECK clauses
// for length / range / pattern are emitted as separate ALTER TABLE
// ADD CONSTRAINT statements so they have their own names.
//
// FEEDBACK #19 — when the new field is NOT NULL but has no DEFAULT,
// emitting `ADD COLUMN x NOT NULL` against a non-empty table fails
// at apply time with `column "x" contains null values`. The diff
// generator can't know whether the target table is empty (it's a
// static-analysis tool, not a connected client), so we switch to a
// three-step pattern that ALWAYS applies:
//
//	1. ADD COLUMN nullable
//	2. UPDATE … SET x = /* TODO: backfill */ WHERE x IS NULL
//	3. ALTER COLUMN x SET NOT NULL
//
// The operator reviews the generated SQL, fills in the backfill
// expression, and runs `migrate up`. The previous single-line form is
// still emitted when the field carries a DEFAULT (Postgres backfills
// existing rows from the default) — that path is safe.
func AddColumnSQL(coll string, f builder.FieldSpec) string {
	var b strings.Builder
	// SequentialCode needs its sequence created before the ALTER TABLE
	// references it in the DEFAULT clause.
	if f.Type == builder.TypeSequentialCode {
		b.WriteString(createSequence(coll, f))
	}

	needsBackfillPattern := needsBackfillSplit(f)

	if needsBackfillPattern {
		// Step 1: ADD nullable column (clone of the spec with Required=false
		// so columnDef omits the NOT NULL clause).
		nullableSpec := f
		nullableSpec.Required = false
		b.WriteString("-- FEEDBACK #19 — NOT NULL without DEFAULT split into three steps so existing rows can be backfilled.\n")
		b.WriteString("-- REVIEW: replace the TODO backfill expression below before running `migrate up`.\n")
		b.WriteString("ALTER TABLE ")
		b.WriteString(quoteIdent(coll))
		b.WriteString(" ADD COLUMN ")
		b.WriteString(columnDef(coll, nullableSpec, false))
		b.WriteString(";\n")
		// Step 2: backfill placeholder. The expression itself is a TODO —
		// migrate up will fail on the SET NOT NULL step until the
		// operator fills in a concrete value.
		b.WriteString("UPDATE ")
		b.WriteString(quoteIdent(coll))
		b.WriteString(" SET ")
		b.WriteString(quoteIdent(f.Name))
		b.WriteString(" = /* TODO: backfill expression */ NULL WHERE ")
		b.WriteString(quoteIdent(f.Name))
		b.WriteString(" IS NULL;\n")
		// Step 3: flip to NOT NULL.
		b.WriteString("ALTER TABLE ")
		b.WriteString(quoteIdent(coll))
		b.WriteString(" ALTER COLUMN ")
		b.WriteString(quoteIdent(f.Name))
		b.WriteString(" SET NOT NULL;\n")
	} else {
		b.WriteString("ALTER TABLE ")
		b.WriteString(quoteIdent(coll))
		b.WriteString(" ADD COLUMN ")
		b.WriteString(columnDef(coll, f, false))
		b.WriteString(";\n")
	}

	if f.Type == builder.TypeSequentialCode {
		b.WriteString(ownSequence(coll, f))
	}
	for _, c := range checkConstraints(coll, f) {
		b.WriteString(c)
		b.WriteString("\n")
	}
	if f.Type == builder.TypeRelation {
		b.WriteString(foreignKey(coll, f))
		b.WriteString("\n")
	}
	for _, idx := range fieldIndexes(coll, f) {
		b.WriteString(idx)
		b.WriteString("\n")
	}
	return b.String()
}

// needsBackfillSplit decides whether AddColumnSQL must emit the
// three-step nullable→backfill→NOT-NULL pattern instead of a single
// ALTER TABLE … ADD COLUMN … NOT NULL line.
//
// Triggered iff:
//   - field is Required (NOT NULL), AND
//   - field has no usable default value at the SQL layer.
//
// Several field types CARRY their own server-side default even when
// the embedder didn't set `.Default(...)` — those are safe under a
// single-line ALTER and the helper returns false for them:
//
//   - TypeDate + AutoCreate → DEFAULT now()
//   - TypeSequentialCode    → DEFAULT nextval(...)
//   - TypeStatus            → DEFAULT '<first declared status>'
//   - Computed (GENERATED)  → backfill is the expression itself
//
// Everything else where Required && !HasDefault gets the safe pattern.
func needsBackfillSplit(f builder.FieldSpec) bool {
	if !f.Required {
		return false
	}
	if f.HasDefault {
		return false
	}
	if f.Computed != "" {
		return false
	}
	if f.Type == builder.TypeDate && f.AutoCreate {
		return false
	}
	if f.Type == builder.TypeSequentialCode {
		return false
	}
	if f.Type == builder.TypeStatus && len(f.StatusValues) > 0 {
		return false
	}
	return true
}

// DropColumnSQL emits ALTER TABLE ... DROP COLUMN.
// Note: drops are destructive. The migrate runner runs migrations in
// a tx; a botched diff is rolled back. But once committed, the data
// is gone — that's the user's signal to review generated SQL.
func DropColumnSQL(coll, field string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s CASCADE;\n",
		quoteIdent(coll), quoteIdent(field))
}

// authInjectedColumns returns the columns that AuthCollection adds on
// top of a regular collection (mirror of the inline emission in
// CreateCollectionSQL). The order matters for migration readability
// but not for correctness.
func authInjectedColumns() []struct{ name, baseType, defaultVal string } {
	return []struct{ name, baseType, defaultVal string }{
		{"email", "TEXT", ""},
		{"password_hash", "TEXT", ""},
		{"verified", "BOOLEAN", "FALSE"}, // has a real default — single-step
		{"token_key", "TEXT", ""},
		// last_login_at is nullable in CreateCollectionSQL — single-step ADD.
		{"last_login_at", "TIMESTAMPTZ NULL", ""},
	}
}

// AuthToggleSQL renders the ALTER TABLE statements needed when a
// collection toggles between Collection and AuthCollection.
//
// On toggle-on (newState=true) we emit a three-step ADD for each NOT
// NULL column without a default (email/password_hash/token_key), so
// the migration can be safely applied to a non-empty table:
//
//  1. ADD COLUMN ... nullable (allows ALTER to succeed against existing rows)
//  2. UPDATE ... SET col = /* TODO: backfill */ NULL  (operator fills in)
//  3. ALTER COLUMN ... SET NOT NULL  (after the backfill)
//
// `verified` has a real DEFAULT, `last_login_at` is nullable — both
// emit as a single-line ADD COLUMN.
//
// On toggle-off (newState=false) we DROP the injected columns. Cascade
// is included so any inbound FK/index gets dropped with them.
//
// FEEDBACK #B1.
func AuthToggleSQL(coll string, newState bool) string {
	q := quoteIdent(coll)
	var b strings.Builder

	if newState {
		fmt.Fprintf(&b, "-- AuthCollection toggle: adding auth-system columns to %s.\n", q)
		fmt.Fprintf(&b, "-- IMPORTANT: this migration is generated empty-backfill. After\n")
		fmt.Fprintf(&b, "-- applying steps 1+2 you MUST run the backfill UPDATEs (marked\n")
		fmt.Fprintf(&b, "-- with `TODO: backfill`) BEFORE the SET NOT NULL step succeeds.\n")
		fmt.Fprintf(&b, "-- For password_hash specifically: existing rows have no password —\n")
		fmt.Fprintf(&b, "-- operators typically email everyone to set one via the standard\n")
		fmt.Fprintf(&b, "-- /auth-with-password reset flow rather than backfilling a value.\n\n")
		for _, c := range authInjectedColumns() {
			fmt.Fprintf(&b, "-- auth column: %s\n", c.name)
			if c.defaultVal != "" {
				// `verified BOOLEAN DEFAULT FALSE` — single-step.
				fmt.Fprintf(&b, "ALTER TABLE %s ADD COLUMN %s %s NOT NULL DEFAULT %s;\n",
					q, c.name, c.baseType, c.defaultVal)
				continue
			}
			if strings.Contains(strings.ToUpper(c.baseType), "NULL") {
				// `last_login_at TIMESTAMPTZ NULL` — explicitly nullable, single-step.
				fmt.Fprintf(&b, "ALTER TABLE %s ADD COLUMN %s %s;\n", q, c.name, c.baseType)
				continue
			}
			// Three-step pattern: nullable add → backfill TODO → SET NOT NULL.
			fmt.Fprintf(&b, "ALTER TABLE %s ADD COLUMN %s %s;\n", q, c.name, c.baseType)
			fmt.Fprintf(&b, "UPDATE %s SET %s = /* TODO: backfill expression */ NULL WHERE %s IS NULL;\n",
				q, c.name, c.name)
			fmt.Fprintf(&b, "ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;\n", q, c.name)
		}
		return b.String()
	}

	// Toggle OFF — drop the injected columns. CASCADE handles auxiliary
	// objects (auth-related indexes/FKs) the application layer added.
	fmt.Fprintf(&b, "-- AuthCollection toggle: dropping auth-system columns from %s.\n", q)
	fmt.Fprintf(&b, "-- This removes the ability for users to authenticate against this collection.\n")
	for _, c := range authInjectedColumns() {
		fmt.Fprintf(&b, "ALTER TABLE %s DROP COLUMN IF EXISTS %s CASCADE;\n", q, c.name)
	}
	return b.String()
}

// --- internal helpers ---

// createTable assembles the CREATE TABLE block.
func createTable(spec builder.CollectionSpec) string {
	var lines []string

	// System fields are always present and always first. The order
	// (id, created, updated, [tenant_id], [deleted]) keeps DDL diffs readable.
	lines = append(lines,
		"    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid()",
		"    created     TIMESTAMPTZ  NOT NULL    DEFAULT now()",
		"    updated     TIMESTAMPTZ  NOT NULL    DEFAULT now()",
	)
	if spec.Tenant {
		lines = append(lines,
			"    tenant_id   UUID         NOT NULL")
	}
	if spec.SoftDelete {
		// NULL = live row; non-NULL = deleted-at timestamp. We don't
		// emit a btree index — instead emit a partial index on the
		// "alive subset" so LIST queries that filter `deleted IS NULL`
		// don't scan tombstones.
		lines = append(lines,
			"    deleted     TIMESTAMPTZ")
	}
	if spec.AdjacencyList {
		// Self-referential FK with ON DELETE SET NULL — deleting a
		// parent re-roots its children rather than cascading (which
		// would wipe whole subtrees on a single parent delete, which
		// is almost never what callers want). Operators wanting CASCADE
		// can swap via raw SQL hooks.
		lines = append(lines, fmt.Sprintf(
			"    parent      UUID         NULL REFERENCES %s(id) ON DELETE SET NULL",
			quoteIdent(spec.Name)))
	}
	if spec.Ordered {
		// Default 0 lets INSERTs omit the column; REST auto-assigns
		// MAX+1 within the same parent so the column reflects the
		// intended position immediately.
		lines = append(lines,
			"    sort_index  INTEGER      NOT NULL    DEFAULT 0")
	}
	if spec.Auth {
		// Auth-collection system fields. password_hash holds the full
		// PHC string ($argon2id$v=19$m=...) so we don't need a fixed
		// width. token_key is an opaque per-record secret used for
		// signing record tokens (password-reset, email-verify, …) —
		// stored as text for the same reason.
		lines = append(lines,
			"    email         TEXT        NOT NULL",
			"    password_hash TEXT        NOT NULL",
			"    verified      BOOLEAN     NOT NULL DEFAULT FALSE",
			"    token_key     TEXT        NOT NULL",
			"    last_login_at TIMESTAMPTZ NULL",
		)
	}

	for _, f := range spec.Fields {
		// TypeRelations is M2M — it lives in a junction table created
		// by createJunction, not as a column on this row. Skipping
		// here keeps the CREATE TABLE clean (no orphan UUID[]
		// placeholder column nobody reads/writes).
		if f.Type == builder.TypeRelations {
			continue
		}
		lines = append(lines, "    "+columnDef(spec.Name, f, true))
	}

	// CHECK constraints inline (they live inside the CREATE TABLE
	// parentheses so they get the table-creation atomicity for free).
	for _, f := range spec.Fields {
		if f.Type == builder.TypeRelations {
			continue
		}
		for _, c := range checkClauses(f) {
			lines = append(lines, "    "+c)
		}
	}

	// FK constraints — relations point at the related collection's
	// id. Tenant FK points at tenants(id).
	for _, f := range spec.Fields {
		if f.Type == builder.TypeRelation {
			lines = append(lines, "    "+inlineFK(f))
		}
	}
	if spec.Tenant {
		lines = append(lines,
			"    CONSTRAINT "+spec.Name+"_tenant_fk FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE")
	}

	return fmt.Sprintf("CREATE TABLE %s (\n%s\n);\n",
		quoteIdent(spec.Name),
		strings.Join(lines, ",\n"))
}

// columnDef renders one column specification. The coll argument is
// only needed for sequence-backed defaults (sequential_code); for
// regular columns it's unused.
func columnDef(coll string, f builder.FieldSpec, inCreate bool) string {
	parts := []string{quoteIdent(f.Name), pgType(f)}

	// v3.x — Computed (generated-stored) columns shortcut the rest of
	// the column-modifier chain: NOT NULL is inherited from the
	// expression, DEFAULT is incompatible (Postgres rejects), and
	// UNIQUE / Required are still permitted. Emit the GENERATED form
	// inline so the column lands on first migration.
	if f.Computed != "" {
		// Required → NOT NULL is fine; UNIQUE → inline UNIQUE is fine.
		if f.Required {
			parts = append(parts, "NOT NULL")
		}
		if f.Unique {
			parts = append(parts, "UNIQUE")
		}
		parts = append(parts, fmt.Sprintf("GENERATED ALWAYS AS (%s) STORED", f.Computed))
		_ = inCreate
		return strings.Join(parts, " ")
	}

	if f.Required {
		parts = append(parts, "NOT NULL")
	}
	if f.Unique {
		// Single-column unique inline; multi-column unique goes
		// through CREATE UNIQUE INDEX in collectIndexes.
		parts = append(parts, "UNIQUE")
	}
	if f.HasDefault {
		parts = append(parts, "DEFAULT "+sqlLiteral(f.Default))
	} else if f.Type == builder.TypeDate && f.AutoCreate {
		parts = append(parts, "DEFAULT now()")
	} else if f.Type == builder.TypeSequentialCode && coll != "" {
		parts = append(parts, "DEFAULT "+seqDefaultExpr(coll, f))
	} else if f.Type == builder.TypeStatus && len(f.StatusValues) > 0 {
		// Initial state = first declared. Lets CREATE omit the column
		// and still produce a row that satisfies the CHECK.
		parts = append(parts, "DEFAULT "+sqlLiteral(f.StatusValues[0]))
	}
	_ = inCreate
	return strings.Join(parts, " ")
}

// seqDefaultExpr returns the SQL expression that renders the formatted
// sequential code, e.g. for INV-/Pad(5):
//
//	'INV-' || lpad(nextval('orders_code_seq')::text, 5, '0')
func seqDefaultExpr(coll string, f builder.FieldSpec) string {
	seqName := sequenceName(coll, f.Name)
	nv := fmt.Sprintf("nextval('%s')::text", seqName)
	if f.SeqPad > 0 {
		nv = fmt.Sprintf("lpad(%s, %d, '0')", nv, f.SeqPad)
	}
	if f.SeqPrefix != "" {
		nv = sqlLiteral(f.SeqPrefix) + " || " + nv
	}
	return nv
}

// sequenceName is the convention for sequential_code sequences:
// "<collection>_<field>_seq". Matches what `SERIAL` would generate.
func sequenceName(coll, field string) string {
	return coll + "_" + field + "_seq"
}

// createSequence emits the CREATE SEQUENCE statement that backs a
// sequential_code field. Emitted BEFORE the CREATE TABLE so the
// table's column DEFAULT can reference it.
func createSequence(coll string, f builder.FieldSpec) string {
	seqName := sequenceName(coll, f.Name)
	start := f.SeqStart
	if start <= 0 {
		start = 1
	}
	return fmt.Sprintf("CREATE SEQUENCE %s START WITH %d;\n", seqName, start)
}

// ownSequence links the sequence to its owning column so DROP TABLE
// cascades into DROP SEQUENCE. Standard SERIAL idiom.
func ownSequence(coll string, f builder.FieldSpec) string {
	seqName := sequenceName(coll, f.Name)
	return fmt.Sprintf("ALTER SEQUENCE %s OWNED BY %s.%s;\n",
		seqName, quoteIdent(coll), quoteIdent(f.Name))
}

// pgType maps FieldType to the Postgres column type. The mapping is
// documented in docs/03-data-layer.md "Field types"; keep it in sync.
func pgType(f builder.FieldSpec) string {
	// Translatable fields override the type-specific default: they
	// always store a JSONB locale-keyed map. The CHECK constraint
	// emitted by checkClauses enforces the object shape.
	if f.Translatable {
		return "JSONB"
	}
	switch f.Type {
	case builder.TypeText, builder.TypeEmail, builder.TypeURL,
		builder.TypePassword, builder.TypeRichText, builder.TypeFile,
		builder.TypeTel, builder.TypeSlug, builder.TypeSequentialCode,
		builder.TypeColor, builder.TypeCron, builder.TypeMarkdown,
		builder.TypeCountry, builder.TypeTimezone,
		builder.TypeLanguage, builder.TypeLocale,
		builder.TypeIBAN, builder.TypeBIC,
		builder.TypeTaxID, builder.TypeBarcode,
		builder.TypeCurrency, builder.TypeQRCode,
		builder.TypeDuration, builder.TypeStatus:
		return "TEXT"
	case builder.TypePriority, builder.TypeRating:
		return "SMALLINT"
	case builder.TypeTags:
		return "TEXT[]"
	case builder.TypeTreePath:
		return "LTREE"
	case builder.TypeNumber:
		if f.IsInt {
			return "BIGINT"
		}
		return "DOUBLE PRECISION"
	case builder.TypeFinance, builder.TypePercentage:
		p := f.NumericPrecision
		s := f.NumericScale
		if p == 0 {
			if f.Type == builder.TypePercentage {
				p = 5
			} else {
				p = 15
			}
		}
		if s == 0 {
			if f.Type == builder.TypePercentage {
				s = 2
			} else {
				s = 4
			}
		}
		return fmt.Sprintf("NUMERIC(%d,%d)", p, s)
	case builder.TypeBool:
		return "BOOLEAN"
	case builder.TypeDate:
		return "TIMESTAMPTZ"
	case builder.TypeDateRange:
		// Postgres native — built-in range type with operators (@>, &&, etc.).
		return "DATERANGE"
	case builder.TypeJSON, builder.TypeFiles, builder.TypePersonName,
		builder.TypeQuantity, builder.TypeCoordinates, builder.TypeAddress,
		builder.TypeMoneyRange, builder.TypeTimeRange, builder.TypeBankAccount:
		return "JSONB"
	case builder.TypeSelect:
		return "TEXT"
	case builder.TypeMultiSelect:
		return "TEXT[]"
	case builder.TypeRelation:
		return "UUID"
	case builder.TypeRelations:
		// Many-to-many uses a junction table created separately.
		// The column on the owner table is a no-op placeholder so
		// snapshot-shape stays uniform; we never actually emit it.
		// In practice, gen filters Relations from CREATE TABLE.
		return "UUID[]"
	}
	return "TEXT"
}

// sqlLiteral renders a Go default value as a Postgres literal.
// We escape strings minimally — single-quote-doubled. Anything more
// adventurous (jsonb_build_object etc.) would need its own emitter.
func sqlLiteral(v any) string {
	switch x := v.(type) {
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case float64:
		return fmt.Sprintf("%g", x)
	default:
		// JSON default — emit as JSONB literal via to_jsonb.
		return fmt.Sprintf("'%v'", x)
	}
}

// patternCheck wraps a single CHECK predicate to also accept the
// empty string when the field is OPTIONAL (not Required). FEEDBACK #B7:
// the blogger project hit this exactly — a JSON form sends `hero_image: ""`
// (rather than `null`), the column's URL CHECK rejects it, the embedder
// gets `400 "check constraint failed"`. NULL is already accepted by
// virtue of the column being nullable; we additionally accept ''.
//
// For Required fields the old strict regex is preserved — an empty
// string in a required field is a real validation failure.
func patternCheck(f builder.FieldSpec, predicate string) string {
	if f.Required {
		return fmt.Sprintf("CHECK (%s)", predicate)
	}
	return fmt.Sprintf("CHECK (%s = '' OR %s)",
		quoteIdent(f.Name), predicate)
}

// checkClauses returns the inline-CHECK fragments for a field.
func checkClauses(f builder.FieldSpec) []string {
	var out []string
	// Translatable fields short-circuit: the column is JSONB and the
	// only invariant we enforce DB-side is "value is an object". Per-
	// key locale-shape validation runs REST-side (where the regex
	// can be friendly and the error message useful). Skipping the
	// type's normal CHECK clauses prevents nonsense like applying a
	// `~ '^https?://'` regex to a JSONB column.
	if f.Translatable {
		return []string{
			fmt.Sprintf("CHECK (jsonb_typeof(%s) = 'object')", quoteIdent(f.Name)),
		}
	}
	switch f.Type {
	case builder.TypeText, builder.TypeRichText:
		if f.MinLen != nil {
			out = append(out, fmt.Sprintf("CHECK (char_length(%s) >= %d)",
				quoteIdent(f.Name), *f.MinLen))
		}
		if f.MaxLen != nil {
			out = append(out, fmt.Sprintf("CHECK (char_length(%s) <= %d)",
				quoteIdent(f.Name), *f.MaxLen))
		}
		if f.Pattern != "" {
			out = append(out, patternCheck(f, fmt.Sprintf("%s ~ %s",
				quoteIdent(f.Name), sqlLiteral(f.Pattern))))
		}
	case builder.TypeNumber:
		if f.Min != nil {
			out = append(out, fmt.Sprintf("CHECK (%s >= %g)",
				quoteIdent(f.Name), *f.Min))
		}
		if f.Max != nil {
			out = append(out, fmt.Sprintf("CHECK (%s <= %g)",
				quoteIdent(f.Name), *f.Max))
		}
	case builder.TypeEmail:
		// Lightweight RFC5322 shape — keep it simple, avoid the
		// pathological full-grammar regex. v0.3 may upgrade to a
		// CITEXT column with a proper validator.
		out = append(out, patternCheck(f, fmt.Sprintf(
			"%s ~* '^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$'", quoteIdent(f.Name))))
	case builder.TypeURL:
		out = append(out, patternCheck(f, fmt.Sprintf(
			"%s ~* '^https?://'", quoteIdent(f.Name))))
	case builder.TypeTel:
		// E.164 canonical: leading '+', 1-15 digits, no separators.
		// REST layer normalises display forms before insert.
		out = append(out, patternCheck(f, fmt.Sprintf(
			"%s ~ '^\\+[1-9][0-9]{1,14}$'", quoteIdent(f.Name))))
	case builder.TypeSlug:
		// URL-safe canonical: lowercase ASCII + digits, hyphens only
		// between alnum runs. No leading/trailing/consecutive hyphens.
		// REST layer normalises (lowercase, strip non-ASCII, collapse
		// non-alnum to single hyphens) before insert.
		out = append(out, patternCheck(f, fmt.Sprintf(
			"%s ~ '^[a-z0-9]+(-[a-z0-9]+)*$'", quoteIdent(f.Name))))
	case builder.TypeColor:
		// Canonical hex: '#' + 6 lowercase hex digits. REST normalises
		// shorthand (#FFF → #ffffff) and uppercase before insert.
		out = append(out, patternCheck(f, fmt.Sprintf(
			"%s ~ '^#[0-9a-f]{6}$'", quoteIdent(f.Name))))
	case builder.TypeMarkdown:
		// Same min/max-length CHECK pattern as Text. Cron has no DB
		// CHECK — the parser is richer than a regex can express.
		if f.MinLen != nil {
			out = append(out, fmt.Sprintf("CHECK (char_length(%s) >= %d)",
				quoteIdent(f.Name), *f.MinLen))
		}
		if f.MaxLen != nil {
			out = append(out, fmt.Sprintf("CHECK (char_length(%s) <= %d)",
				quoteIdent(f.Name), *f.MaxLen))
		}
	case builder.TypeFinance, builder.TypePercentage:
		// Decimal-string CHECK bounds. We emit the values verbatim
		// (no float roundtripping) so what the operator declared in
		// Go is exactly what hits the DB.
		if f.MinDecimal != "" {
			out = append(out, fmt.Sprintf("CHECK (%s >= %s)",
				quoteIdent(f.Name), f.MinDecimal))
		}
		if f.MaxDecimal != "" {
			out = append(out, fmt.Sprintf("CHECK (%s <= %s)",
				quoteIdent(f.Name), f.MaxDecimal))
		}
	case builder.TypeCountry:
		// Shape only — actual membership in ISO 3166-1 is app-layer
		// so codes can be added/retired without DB migrations.
		out = append(out, fmt.Sprintf("CHECK (%s ~ '^[A-Z]{2}$')",
			quoteIdent(f.Name)))
	case builder.TypeLanguage:
		// Shape only — ISO 639-1 alpha-2, lowercase canonical.
		out = append(out, fmt.Sprintf("CHECK (%s ~ '^[a-z]{2}$')",
			quoteIdent(f.Name)))
	case builder.TypeLocale:
		// BCP-47: lowercase language (required) + optional uppercase
		// region. Examples: "en", "en-US", "pt-BR". Full validation
		// (language ∈ ISO 639-1, region ∈ ISO 3166-1) is REST-layer
		// so we don't need a 184×249 lookup in SQL.
		out = append(out, fmt.Sprintf("CHECK (%s ~ '^[a-z]{2}(-[A-Z]{2})?$')",
			quoteIdent(f.Name)))
	case builder.TypeCoordinates:
		// JSONB shape: must be an object with numeric lat ∈ [-90, 90]
		// and lng ∈ [-180, 180]. Postgres can range-check JSONB-extracted
		// numbers via `->>` cast; CHECK keeps the DB honest even when
		// callers bypass REST. NULL-safe.
		out = append(out,
			fmt.Sprintf("CHECK (jsonb_typeof(%s) = 'object')", quoteIdent(f.Name)),
			fmt.Sprintf("CHECK ((%s->>'lat')::numeric BETWEEN -90 AND 90)", quoteIdent(f.Name)),
			fmt.Sprintf("CHECK ((%s->>'lng')::numeric BETWEEN -180 AND 180)", quoteIdent(f.Name)),
		)
	case builder.TypeAddress:
		// JSONB shape only: must be a non-empty object. Component-level
		// validation (country ∈ ISO 3166-1, postal length, etc.) is
		// REST-layer so we can evolve the schema without DB migrations.
		out = append(out,
			fmt.Sprintf("CHECK (jsonb_typeof(%s) = 'object')", quoteIdent(f.Name)),
			// Cardinality > 0 — empty {} is meaningless for an address.
			// jsonb_object_keys returns one row per key; CHECK requires
			// a scalar so we use the EXISTS-on-jsonb_each idiom.
			fmt.Sprintf("CHECK (jsonb_typeof(%s) = 'object' AND %s <> '{}'::jsonb)",
				quoteIdent(f.Name), quoteIdent(f.Name)),
		)
	case builder.TypeTaxID:
		// Shape only: 4-30 chars, uppercase ASCII alphanumeric. Per-
		// country shape + check digits are REST-layer so adding a new
		// country format doesn't require a DB migration.
		out = append(out, fmt.Sprintf("CHECK (%s ~ '^[A-Z0-9]{4,30}$')",
			quoteIdent(f.Name)))
	case builder.TypeBarcode:
		// Shape depends on format: digit-only (EAN/UPC) → strict
		// length × digit regex; code128 → alphanumeric. We pick the
		// CHECK by .BarcodeFormat hint.
		switch f.BarcodeFormat {
		case "code128":
			// Code-128 supports a wide ASCII subset; mostly printable.
			// Cap at 80 chars (longest practical Code-128 string).
			out = append(out, fmt.Sprintf("CHECK (char_length(%s) BETWEEN 1 AND 80)",
				quoteIdent(f.Name)))
		case "ean8":
			out = append(out, fmt.Sprintf("CHECK (%s ~ '^[0-9]{8}$')",
				quoteIdent(f.Name)))
		case "upca":
			out = append(out, fmt.Sprintf("CHECK (%s ~ '^[0-9]{12}$')",
				quoteIdent(f.Name)))
		case "ean13":
			out = append(out, fmt.Sprintf("CHECK (%s ~ '^[0-9]{13}$')",
				quoteIdent(f.Name)))
		default:
			// Auto-detect: accept any of the three digit-only lengths.
			out = append(out, fmt.Sprintf("CHECK (%s ~ '^[0-9]{8}$' OR %s ~ '^[0-9]{12}$' OR %s ~ '^[0-9]{13}$')",
				quoteIdent(f.Name), quoteIdent(f.Name), quoteIdent(f.Name)))
		}
	case builder.TypeCurrency:
		// Shape only — actual membership in ISO 4217 is app-layer,
		// matching country's pattern (codes evolve, no migrations).
		out = append(out, fmt.Sprintf("CHECK (%s ~ '^[A-Z]{3}$')",
			quoteIdent(f.Name)))
	case builder.TypeMoneyRange:
		// JSONB shape: object with min/max decimal strings + currency
		// ISO 4217. min ≤ max enforced; both bounds inside the
		// operator-declared NumericPrecision/Scale range.
		nameQ := quoteIdent(f.Name)
		out = append(out,
			fmt.Sprintf("CHECK (jsonb_typeof(%s) = 'object')", nameQ),
			fmt.Sprintf("CHECK (%s->>'currency' ~ '^[A-Z]{3}$')", nameQ),
			fmt.Sprintf("CHECK ((%s->>'min')::numeric <= (%s->>'max')::numeric)", nameQ, nameQ),
		)
		// Operator-declared outer bounds clamp BOTH ends of the range.
		if f.MinDecimal != "" {
			out = append(out, fmt.Sprintf("CHECK ((%s->>'min')::numeric >= %s)", nameQ, f.MinDecimal))
		}
		if f.MaxDecimal != "" {
			out = append(out, fmt.Sprintf("CHECK ((%s->>'max')::numeric <= %s)", nameQ, f.MaxDecimal))
		}
	case builder.TypeTimeRange:
		// JSONB shape: object with start/end strings matching HH:MM or
		// HH:MM:SS. start ≤ end (lex compare works because the format
		// is fixed-width zero-padded). NULL-safe.
		nameQ := quoteIdent(f.Name)
		out = append(out,
			fmt.Sprintf("CHECK (jsonb_typeof(%s) = 'object')", nameQ),
			fmt.Sprintf("CHECK (%s->>'start' ~ '^[0-2][0-9]:[0-5][0-9](:[0-5][0-9])?$')", nameQ),
			fmt.Sprintf("CHECK (%s->>'end' ~ '^[0-2][0-9]:[0-5][0-9](:[0-5][0-9])?$')", nameQ),
			// Postgres TIME cast normalises HH:MM and HH:MM:SS to TIME
			// so the comparison is correct across short / long forms.
			fmt.Sprintf("CHECK ((%s->>'start')::time <= (%s->>'end')::time)", nameQ, nameQ),
		)
	case builder.TypeBankAccount:
		// JSONB shape: object with required `country` ∈ ISO 3166-1,
		// plus operator-defined component fields. Per-country shape
		// validation lives in REST so adding country support stays
		// migration-free. Note: ->>'country' returns NULL when the key
		// is missing, and `NULL ~ pattern` evaluates to NULL — which
		// CHECK treats as pass. So we explicitly require the key via
		// `?` to defeat raw-INSERT bypass missing the country.
		nameQ := quoteIdent(f.Name)
		out = append(out,
			fmt.Sprintf("CHECK (jsonb_typeof(%s) = 'object')", nameQ),
			fmt.Sprintf("CHECK (%s ? 'country' AND %s->>'country' ~ '^[A-Z]{2}$')", nameQ, nameQ),
		)
	case builder.TypeQRCode:
		// QR Code spec supports up to ~4296 alphanumeric chars at max
		// error-correction. Cap at 4096 — leaves headroom while
		// preventing pathological payloads.
		out = append(out, fmt.Sprintf("CHECK (char_length(%s) BETWEEN 1 AND 4096)",
			quoteIdent(f.Name)))
	case builder.TypeIBAN:
		// Shape only: 2 letters (country) + 2 digits (check) + up to
		// 30 alnum chars (BBAN). Mod-97 verification is REST-layer.
		out = append(out, fmt.Sprintf("CHECK (%s ~ '^[A-Z]{2}[0-9]{2}[A-Z0-9]{1,30}$')",
			quoteIdent(f.Name)))
	case builder.TypeBIC:
		// Shape: 4 letters (bank) + 2 letters (country) + 2 alnum
		// (location) + optional 3 alnum (branch). 8 or 11 chars total.
		out = append(out, fmt.Sprintf("CHECK (%s ~ '^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$')",
			quoteIdent(f.Name)))
	case builder.TypeStatus:
		// CHECK enforces membership in the declared state set.
		quoted := make([]string, len(f.StatusValues))
		for i, v := range f.StatusValues {
			quoted[i] = sqlLiteral(v)
		}
		out = append(out, fmt.Sprintf("CHECK (%s IN (%s))",
			quoteIdent(f.Name), strings.Join(quoted, ", ")))
	case builder.TypePriority, builder.TypeRating:
		if f.IntMin != nil {
			out = append(out, fmt.Sprintf("CHECK (%s >= %d)",
				quoteIdent(f.Name), *f.IntMin))
		}
		if f.IntMax != nil {
			out = append(out, fmt.Sprintf("CHECK (%s <= %d)",
				quoteIdent(f.Name), *f.IntMax))
		}
	case builder.TypeDuration:
		// ISO 8601 duration: P[nY][nM][nD][T[nH][nM][nS]]. At least
		// one component required. We allow only integer values (no
		// fractional) for shape simplicity — real-world durations
		// rarely need fractional months/years.
		out = append(out, fmt.Sprintf(
			"CHECK (%s ~ '^P(([0-9]+Y)?([0-9]+M)?([0-9]+D)?)(T([0-9]+H)?([0-9]+M)?([0-9]+S)?)?$' AND %s !~ '^PT?$')",
			quoteIdent(f.Name), quoteIdent(f.Name)))
	case builder.TypeTags:
		// Cardinality cap uses array_length — pure scalar expression
		// allowed in CHECK. Per-tag length is enforced REST-side only:
		// CHECK constraints can't contain subqueries, and `unnest(arr)`
		// in a CHECK requires either a subquery or an IMMUTABLE
		// function. Per-tag length defense in depth via app layer.
		if f.TagsMaxCount > 0 {
			out = append(out, fmt.Sprintf("CHECK (coalesce(array_length(%s, 1), 0) <= %d)",
				quoteIdent(f.Name), f.TagsMaxCount))
		}
	case builder.TypeSelect:
		quoted := make([]string, len(f.SelectValues))
		for i, v := range f.SelectValues {
			quoted[i] = sqlLiteral(v)
		}
		out = append(out, fmt.Sprintf("CHECK (%s IN (%s))",
			quoteIdent(f.Name), strings.Join(quoted, ", ")))
	case builder.TypeMultiSelect:
		quoted := make([]string, len(f.SelectValues))
		for i, v := range f.SelectValues {
			quoted[i] = sqlLiteral(v)
		}
		out = append(out, fmt.Sprintf("CHECK (%s <@ ARRAY[%s]::TEXT[])",
			quoteIdent(f.Name), strings.Join(quoted, ", ")))
		if f.MinSelections != nil {
			out = append(out, fmt.Sprintf("CHECK (array_length(%s, 1) >= %d)",
				quoteIdent(f.Name), *f.MinSelections))
		}
		if f.MaxSelections != nil {
			out = append(out, fmt.Sprintf("CHECK (array_length(%s, 1) <= %d)",
				quoteIdent(f.Name), *f.MaxSelections))
		}
	case builder.TypePassword:
		if f.PasswordMinLen != nil {
			out = append(out, fmt.Sprintf("CHECK (char_length(%s) >= %d)",
				quoteIdent(f.Name), *f.PasswordMinLen))
		}
	}
	return out
}

// checkConstraints converts the inline CHECK fragments into ALTER
// TABLE ADD CONSTRAINT statements (used for AddColumnSQL).
func checkConstraints(coll string, f builder.FieldSpec) []string {
	clauses := checkClauses(f)
	out := make([]string, 0, len(clauses))
	for i, c := range clauses {
		// Strip the leading "CHECK " — we re-add it inside the
		// CONSTRAINT clause so the constraint can have a name.
		body := strings.TrimPrefix(c, "CHECK ")
		name := fmt.Sprintf("%s_%s_chk%d", coll, f.Name, i)
		out = append(out, fmt.Sprintf(
			"ALTER TABLE %s ADD CONSTRAINT %s CHECK %s;",
			quoteIdent(coll), quoteIdent(name), body))
	}
	return out
}

// inlineFK is the FK clause emitted inside CREATE TABLE.
func inlineFK(f builder.FieldSpec) string {
	action := ""
	switch {
	case f.CascadeDelete:
		action = " ON DELETE CASCADE"
	case f.SetNullOnDelete:
		action = " ON DELETE SET NULL"
	default:
		action = " ON DELETE RESTRICT"
	}
	return fmt.Sprintf("CONSTRAINT %s_fk FOREIGN KEY (%s) REFERENCES %s(id)%s",
		f.Name, quoteIdent(f.Name), quoteIdent(f.RelatedCollection), action)
}

// foreignKey emits the FK as a standalone ALTER TABLE statement,
// for use in AddColumnSQL.
func foreignKey(coll string, f builder.FieldSpec) string {
	action := ""
	switch {
	case f.CascadeDelete:
		action = " ON DELETE CASCADE"
	case f.SetNullOnDelete:
		action = " ON DELETE SET NULL"
	default:
		action = " ON DELETE RESTRICT"
	}
	name := fmt.Sprintf("%s_%s_fk", coll, f.Name)
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(id)%s;",
		quoteIdent(coll), quoteIdent(name),
		quoteIdent(f.Name), quoteIdent(f.RelatedCollection), action)
}

// collectIndexes returns the CREATE INDEX statements for one table:
// auto-indexes (FK columns, tenant_id, .Indexed fields), FTS GIN
// indexes, and any user-declared composite indexes.
func collectIndexes(spec builder.CollectionSpec) []string {
	var out []string

	if spec.Tenant {
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_tenant_id_idx ON %s (tenant_id);",
			spec.Name, quoteIdent(spec.Name)))
	}
	if spec.SoftDelete {
		// Partial index on the "alive subset". Queries like
		// `SELECT … FROM t WHERE deleted IS NULL AND <user filter>`
		// can use this index for the IS-NULL predicate at near-zero
		// cost, then narrow by the user filter via heap scan.
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_alive_idx ON %s (created) WHERE deleted IS NULL;",
			spec.Name, quoteIdent(spec.Name)))
	}
	if spec.Auth {
		// Email is unique per auth collection — two `users` rows
		// can't share an email, but `users` and `admins` can have
		// the same email (different identities). Lower-case index so
		// signin is case-insensitive without dragging in CITEXT.
		out = append(out, fmt.Sprintf(
			"CREATE UNIQUE INDEX %s_email_idx ON %s (lower(email));",
			spec.Name, quoteIdent(spec.Name)))
	}
	if spec.AdjacencyList {
		// FK-backing index — Postgres does NOT auto-index FK columns.
		// Without it every parent-DELETE triggers a full table scan to
		// SET NULL the children, and every "find my children" query
		// is a seq scan.
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_parent_idx ON %s (parent);",
			spec.Name, quoteIdent(spec.Name)))
	}
	if spec.Ordered {
		// Composite index supporting "siblings of X in display order".
		// With AdjacencyList: `WHERE parent = $1 ORDER BY sort_index`
		// hits the index in order. Standalone (no parent): the index
		// is still useful for `ORDER BY sort_index` over the whole set.
		if spec.AdjacencyList {
			out = append(out, fmt.Sprintf(
				"CREATE INDEX %s_siblings_idx ON %s (parent, sort_index);",
				spec.Name, quoteIdent(spec.Name)))
		} else {
			out = append(out, fmt.Sprintf(
				"CREATE INDEX %s_sort_idx ON %s (sort_index);",
				spec.Name, quoteIdent(spec.Name)))
		}
	}

	for _, f := range spec.Fields {
		out = append(out, fieldIndexes(spec.Name, f)...)
	}

	// User-declared indexes last so their explicit names show up
	// after the auto-generated ones in `\d` output.
	for _, idx := range spec.Indexes {
		out = append(out, indexStmt(spec.Name, idx))
	}
	return out
}

// fieldIndexes returns the indexes implied by a single field:
//   - .Indexed → btree
//   - .FTS → GIN on tsvector(generated col)
//   - relation FK → btree (FKs don't auto-index in Postgres)
//
// Redundancy rule: a TypeRelation column gets ONE btree index, named
// `<coll>_<field>_fk_idx`. When the user also calls `.Index()` on a
// relation field (a common idiom — Sentinel's `Field("owner",
// Relation("users").Required().Index())` did exactly this), we used
// to emit BOTH `_idx` and `_fk_idx` on the same column. That doubles
// write overhead with zero query benefit. v3.x emits only the FK
// index for relation columns regardless of the `.Index()` toggle.
// Same applies to `.Unique` — the unique index already covers reads.
func fieldIndexes(coll string, f builder.FieldSpec) []string {
	var out []string
	// btree for .Indexed — but only when no more specific index will
	// already cover the column (FK index for relations, Unique for
	// .Unique fields).
	if f.Indexed && !f.Unique && f.Type != builder.TypeRelation {
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_idx ON %s (%s);",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	if f.Type == builder.TypeRelation {
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_fk_idx ON %s (%s);",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	// FTS and Translatable both mutate the column away from plain text,
	// so the FTS GIN expression must use ->>'<base>' or similar. v1
	// punts: FTS on a Translatable field is rejected at builder Validate
	// time. Skip the FTS index emission here when Translatable is set.
	if (f.Type == builder.TypeText || f.Type == builder.TypeRichText || f.Type == builder.TypeMarkdown) && f.FTS && !f.Translatable {
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_fts ON %s USING gin (to_tsvector('simple', %s));",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	// Translatable always stores JSONB; override the default btree with
	// GIN so containment / key-existence operators work efficiently.
	if f.Translatable && f.Indexed {
		out = out[:0]
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_gin ON %s USING gin (%s);",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	if f.Type == builder.TypeJSON && f.Indexed {
		// Override the default btree with GIN for JSONB.
		out = out[:0]
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_gin ON %s USING gin (%s);",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	if f.Type == builder.TypeMultiSelect && f.Indexed {
		out = out[:0]
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_gin ON %s USING gin (%s);",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	if f.Type == builder.TypeTags && f.Indexed {
		// Tags is TEXT[] — same GIN-for-array trick.
		out = out[:0]
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_gin ON %s USING gin (%s);",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	if f.Type == builder.TypeTreePath && f.Indexed {
		// LTREE benefits from GIST for ancestor/descendant operators
		// (`@>`, `<@`, `~`). Default btree is much less useful.
		out = out[:0]
		out = append(out, fmt.Sprintf(
			"CREATE INDEX %s_%s_gist ON %s USING gist (%s);",
			coll, f.Name, quoteIdent(coll), quoteIdent(f.Name)))
	}
	return out
}

// indexStmt renders one user-declared IndexSpec.
func indexStmt(coll string, idx builder.IndexSpec) string {
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = quoteIdent(c)
	}
	uniq := ""
	if idx.Unique {
		uniq = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);",
		uniq, quoteIdent(idx.Name), quoteIdent(coll), strings.Join(cols, ", "))
}

// tenantRLS enables RLS and installs the tenant-isolation policy.
// Admin role bypass mirrors the `WithSiteScope` pattern: setting
// `railbase.role = 'app_admin'` lets management code see every row.
func tenantRLS(spec builder.CollectionSpec) string {
	return fmt.Sprintf(`ALTER TABLE %s ENABLE ROW LEVEL SECURITY;
ALTER TABLE %s FORCE ROW LEVEL SECURITY;
CREATE POLICY %s_tenant_isolation ON %s
    USING (
        current_setting('railbase.role', true) = 'app_admin'
        OR tenant_id = NULLIF(current_setting('railbase.tenant', true), '')::uuid
    )
    WITH CHECK (
        current_setting('railbase.role', true) = 'app_admin'
        OR tenant_id = NULLIF(current_setting('railbase.tenant', true), '')::uuid
    );
`, quoteIdent(spec.Name), quoteIdent(spec.Name), spec.Name, quoteIdent(spec.Name))
}

// hasAutoUpdate is true if any field carries .AutoUpdate().
func hasAutoUpdate(spec builder.CollectionSpec) bool {
	for _, f := range spec.Fields {
		if f.AutoUpdate {
			return true
		}
	}
	// `updated` system column is implicitly AutoUpdate — we always
	// install the trigger so updates bump it.
	return true
}

// updatedTrigger installs a row-level BEFORE UPDATE trigger that
// bumps the `updated` column to now() on every UPDATE.
func updatedTrigger(spec builder.CollectionSpec) string {
	fn := spec.Name + "_set_updated"
	trg := spec.Name + "_updated_trg"
	return fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$
BEGIN
    NEW.updated = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER %s BEFORE UPDATE ON %s
    FOR EACH ROW EXECUTE FUNCTION %s();
`, fn, trg, quoteIdent(spec.Name), fn)
}

// quoteIdent wraps an identifier in double quotes only when needed.
// All Railbase-generated names are guaranteed safe by the validator,
// so we keep DDL un-quoted for readability — much easier to grep.
func quoteIdent(name string) string {
	// We trust validate.go to reject anything that requires quoting.
	return name
}

// SortedFields returns the fields of spec sorted by name. Useful for
// deterministic snapshot serialization independent of declaration order.
func SortedFields(spec builder.CollectionSpec) []builder.FieldSpec {
	out := append([]builder.FieldSpec(nil), spec.Fields...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
