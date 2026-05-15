package ts

import (
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// EmitTypes builds the contents of types.ts. One interface per
// collection, plus the shared system-field types and the file-ref
// helper. Auth collections get the additional system fields injected
// by `schema.AuthCollection` (email, verified, etc.).
//
// Naming convention:
//
//   - Collection "posts"     → interface Posts
//   - Collection "blog_post" → interface BlogPost (camel-cased)
//
// The casing helper deliberately keeps things ASCII-only — fancy
// Unicode identifiers won't survive the JS toolchain anyway.
func EmitTypes(specs []builder.CollectionSpec) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`// types.ts — interfaces matching every registered collection.

`)
	b.WriteString(`/** A reference to an uploaded file (` + "`files`" + ` field). */
export interface FileRef {
  /** Storage path or signed URL fragment. */
  path: string;
  /** Original filename as uploaded. */
  name: string;
  /** MIME type sniffed at upload time. */
  mime: string;
  /** Size in bytes. */
  size: number;
}

/** PocketBase-shape paginated list response. */
export interface ListResponse<T> {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: T[];
}

`)

	for _, spec := range specs {
		writeInterface(&b, spec)
		b.WriteString("\n")
	}

	return b.String()
}

func writeInterface(b *strings.Builder, spec builder.CollectionSpec) {
	fmt.Fprintf(b, "/** Row shape for collection `%s`. */\n", spec.Name)
	fmt.Fprintf(b, "export interface %s {\n", typeName(spec.Name))
	// System fields — present on every row regardless of type.
	b.WriteString("  /** UUIDv7. Server-generated. */\n")
	b.WriteString("  id: string;\n")
	b.WriteString("  /** Server-set on insert (RFC 3339). */\n")
	b.WriteString("  created: string;\n")
	b.WriteString("  /** Server-set on every write (RFC 3339). */\n")
	b.WriteString("  updated: string;\n")
	if spec.Tenant {
		b.WriteString("  /** Server-forced from X-Tenant header. */\n")
		b.WriteString("  tenant_id: string;\n")
	}
	if spec.Auth {
		// AuthCollection injects these via builder.NewAuthCollection.
		// We mirror them here so the generated interface reflects
		// what the user gets back from /auth-* endpoints.
		b.WriteString("  email: string;\n")
		b.WriteString("  verified: boolean;\n")
		b.WriteString("  last_login_at?: string | null;\n")
	}
	// v0.4.1 — structural system fields injected by collection-level
	// modifiers. Sentinel's FEEDBACK.md #5 caught these missing: the
	// DB columns exist but the SDK Tasks interface had neither
	// `parent` (AdjacencyList) nor `sort_index` (Ordered) nor
	// `deleted` (SoftDelete) so every read site needed `as unknown
	// as MyTask`.
	if spec.AdjacencyList {
		b.WriteString("  /** Self-FK to parent row. Null at the WBS root. */\n")
		b.WriteString("  parent?: string | null;\n")
	}
	if spec.Ordered {
		b.WriteString("  /** Per-parent ordering index. Server auto-assigns MAX+1 when omitted on insert. */\n")
		b.WriteString("  sort_index: number;\n")
	}
	if spec.SoftDelete {
		b.WriteString("  /** Tombstone timestamp. Null on live rows; non-null after DELETE. List queries hide tombstones unless `?includeDeleted=true`. */\n")
		b.WriteString("  deleted?: string | null;\n")
	}
	for _, f := range spec.Fields {
		// password is write-only — the server never returns it, so
		// we omit from the read interface entirely. Codegen elsewhere
		// (zod, collections) handles it as input-only.
		if f.Type == builder.TypePassword {
			continue
		}
		// AuthCollection's system fields are emitted above; skip
		// duplicates in the field list.
		if spec.Auth && isAuthSystemField(f.Name) {
			continue
		}
		writeFieldDoc(b, f)
		fmt.Fprintf(b, "  %s%s: %s;\n", f.Name, optionalSuffix(f), tsType(f))
	}
	b.WriteString("}\n")
}

// optionalSuffix returns "?" for nullable / optional fields. v0.7
// rule: required fields without a default are non-optional; everything
// else is optional in the read interface (the server may have NULL).
func optionalSuffix(f builder.FieldSpec) string {
	if f.Required {
		return ""
	}
	return "?"
}

// tsType maps a FieldSpec to its TypeScript type. Unknown types fall
// back to `unknown` rather than `any` — the user gets a compile
// error to investigate, not silent loss of typing.
func tsType(f builder.FieldSpec) string {
	switch f.Type {
	case builder.TypeText, builder.TypeRichText, builder.TypeURL:
		return "string"
	case builder.TypeEmail:
		return "string"
	case builder.TypeNumber:
		return "number"
	case builder.TypeBool:
		return "boolean"
	case builder.TypeDate:
		// RFC 3339 string. v1 will add a Date-typed wrapper option.
		return "string"
	case builder.TypeJSON:
		// `unknown` instead of `Record<string, unknown>` — JSON
		// columns are not restricted to objects, they can hold
		// arrays, strings, numbers, booleans, or null. Sentinel
		// FEEDBACK.md #6 hit this with `holidays JSONB` storing
		// `string[]` and the generator emitting Record-shape that
		// rejected array literals. `unknown` forces a type-narrow
		// at the call site, which is correct ergonomics for an
		// unconstrained JSON value. Users who want stricter typing
		// can wrap the field type at the consumer level
		// (`x.holidays as string[]`).
		return "unknown"
	case builder.TypeSelect:
		return literalUnion(f.SelectValues)
	case builder.TypeMultiSelect:
		// Wrap the union in parens — `"a" | "b"[]` parses in TS as
		// `"a" | ("b"[])`, not the array we want.
		if len(f.SelectValues) == 0 {
			return "string[]"
		}
		return "(" + literalUnion(f.SelectValues) + ")[]"
	case builder.TypeFile:
		return "string"
	case builder.TypeFiles:
		return "FileRef[]"
	case builder.TypeRelation:
		return "string"
	case builder.TypeRelations:
		return "string[]"
	case builder.TypePassword:
		// Should never be emitted on a read interface. Defensive.
		return "string"
	case builder.TypeTel:
		// E.164 canonical string on the wire. Display formatting is
		// the consumer's responsibility.
		return "string"
	case builder.TypePersonName:
		return "{ first?: string; middle?: string; last?: string; suffix?: string; full?: string }"
	case builder.TypeSlug:
		// Canonical lowercase-hyphen form on the wire. Server normalises
		// client input before storing, so what's emitted MATCHES the
		// CHECK constraint.
		return "string"
	case builder.TypeSequentialCode:
		// Server-owned. Always present on read (it's NOT NULL with a
		// sequence-backed DEFAULT), so it's a plain string.
		return "string"
	case builder.TypeColor:
		// Canonical "#rrggbb" on the wire.
		return "string"
	case builder.TypeCron:
		// 5-field crontab expression, whitespace-normalised by REST.
		return "string"
	case builder.TypeMarkdown:
		// Plain Markdown source. Consumers pick their own renderer.
		return "string"
	case builder.TypeFinance, builder.TypePercentage:
		// String on the wire — JSON numbers lose precision (IEEE 754).
		// Consumers do arithmetic via bignumber.js / decimal.js / etc.
		return "string"
	case builder.TypeCountry, builder.TypeTimezone,
		builder.TypeLanguage, builder.TypeLocale:
		// ISO 3166-1 alpha-2 / IANA tz identifier / ISO 639-1 lang /
		// BCP-47 locale. All canonical strings on the wire.
		return "string"
	case builder.TypeCoordinates:
		// JSONB {lat, lng} — numbers on the wire (range-validated).
		return "{ lat: number; lng: number }"
	case builder.TypeAddress:
		// JSONB structured address. All components optional; SDK
		// emits an open Partial-like type so callers don't have to
		// fill every key.
		return "{ street?: string; street2?: string; city?: string; region?: string; postal?: string; country?: string }"
	case builder.TypeIBAN, builder.TypeBIC:
		// Canonical compact string. Display formatting (4-char groups
		// for IBAN, separators for BIC) is the SDK consumer's job.
		return "string"
	case builder.TypeTaxID, builder.TypeBarcode:
		// Canonical compact string. Display formatting (US EIN
		// "XX-XXXXXXX", EAN-13 split, etc.) is the SDK consumer's job.
		return "string"
	case builder.TypeCurrency:
		// ISO 4217 alpha-3, uppercase. SDK consumer pairs with
		// formatNumber({ currency }) helper for display.
		return "string"
	case builder.TypeMoneyRange:
		// JSONB object — min/max are decimal STRINGS (precision-safe),
		// currency is ISO 4217.
		return "{ min: string; max: string; currency: string }"
	case builder.TypeDateRange:
		// Postgres daterange — wire form is the canonical "[start,end)"
		// string. SDK consumers parse via local date library.
		return "string"
	case builder.TypeTimeRange:
		// JSONB {start, end} — HH:MM:SS canonical.
		return "{ start: string; end: string }"
	case builder.TypeBankAccount:
		// JSONB object — country + per-country fields. Open-ended
		// shape because per-country components vary; SDK consumers
		// inspect by country.
		return "Record<string, string>"
	case builder.TypeQRCode:
		// Opaque payload string — content interpretation is the
		// SDK consumer's job (renderer picks format by .QRFormat
		// at schema declaration time).
		return "string"
	case builder.TypeQuantity:
		// JSONB object `{value: "10.5", unit: "kg"}`. Value is decimal
		// string, never number — float arithmetic precision loss.
		return "{ value: string; unit: string }"
	case builder.TypeDuration:
		// ISO 8601 duration string. Consumers parse via temporal /
		// java.time.Duration / etc.
		return "string"
	case builder.TypeStatus:
		// Literal union of declared states for full type-safety.
		return literalUnion(f.StatusValues)
	case builder.TypePriority, builder.TypeRating:
		// Integer on the wire (SMALLINT storage). Range enforcement
		// is server-side; consumer just gets a number.
		return "number"
	case builder.TypeTags:
		return "string[]"
	case builder.TypeTreePath:
		// LTREE canonical dot-path string.
		return "string"
	default:
		return "unknown"
	}
}

func literalUnion(values []string) string {
	if len(values) == 0 {
		return "string"
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = quote(v)
	}
	return strings.Join(parts, " | ")
}

// writeFieldDoc renders a /** ... */ JSDoc block above each property
// when there's metadata worth surfacing (constraints, FTS, default).
// Skipped if the field is plain — keeping the output readable.
func writeFieldDoc(b *strings.Builder, f builder.FieldSpec) {
	notes := fieldNotes(f)
	if len(notes) == 0 {
		return
	}
	b.WriteString("  /** ")
	b.WriteString(strings.Join(notes, "; "))
	b.WriteString(" */\n")
}

func fieldNotes(f builder.FieldSpec) []string {
	var notes []string
	if f.MinLen != nil && f.MaxLen != nil {
		notes = append(notes, fmt.Sprintf("len %d..%d", *f.MinLen, *f.MaxLen))
	} else if f.MinLen != nil {
		notes = append(notes, fmt.Sprintf("len ≥ %d", *f.MinLen))
	} else if f.MaxLen != nil {
		notes = append(notes, fmt.Sprintf("len ≤ %d", *f.MaxLen))
	}
	if f.Min != nil && f.Max != nil {
		notes = append(notes, fmt.Sprintf("range %v..%v", *f.Min, *f.Max))
	} else if f.Min != nil {
		notes = append(notes, fmt.Sprintf("≥ %v", *f.Min))
	} else if f.Max != nil {
		notes = append(notes, fmt.Sprintf("≤ %v", *f.Max))
	}
	if f.FTS {
		notes = append(notes, "full-text indexed")
	}
	if f.Unique {
		notes = append(notes, "unique")
	}
	if f.HasDefault {
		notes = append(notes, fmt.Sprintf("default %v", f.Default))
	}
	if f.Type == builder.TypeRelation && f.RelatedCollection != "" {
		notes = append(notes, fmt.Sprintf("→ %s.id", f.RelatedCollection))
	}
	if f.Type == builder.TypeRelations && f.RelatedCollection != "" {
		notes = append(notes, fmt.Sprintf("→ %s.id (junction)", f.RelatedCollection))
	}
	return notes
}

// isAuthSystemField reports whether name collides with the fields
// AuthCollection injects automatically. Mirrors the list in
// internal/schema/gen/sql.go so the read interface doesn't double
// up.
func isAuthSystemField(name string) bool {
	switch name {
	case "email", "password_hash", "verified", "token_key", "last_login_at":
		return true
	}
	return false
}

// typeName converts a snake_case collection name to a PascalCase TS
// identifier. Examples:
//   - "posts"      → "Posts"
//   - "blog_post"  → "BlogPost"
//   - "user_2fa"   → "User2fa"
//
// Why no special-casing for plurals: predictability beats English.
// User wants `Post` instead of `Posts`? Rename the collection.
func typeName(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// quote wraps s in double quotes, escaping the way TS string literals
// expect. Used in literal unions and Zod enums.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
