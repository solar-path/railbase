package ts

import (
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// EmitZod produces zod.ts: one runtime schema per collection so
// callers can validate untrusted JSON (form input, third-party
// webhooks) before handing it to the typed wrappers.
//
// We emit two schemas per collection:
//   - <Name>Schema      — full row shape (read response from server)
//   - <Name>InputSchema — write shape (no system fields, password
//     INCLUDED, plus partial coverage for PATCH)
//
// The <Name>InputSchema is what `create()` accepts; partial of it is
// what `update()` accepts. Strict() is left off so server-added
// fields don't trip clients in newer versions.
func EmitZod(specs []builder.CollectionSpec) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`// zod.ts — runtime validators. Mirror of types.ts; the two stay in
// sync because both are generated from the same FieldSpec list.

import { z } from "zod";

/** Common file reference shape (zod side). */
export const FileRefSchema = z.object({
  path: z.string(),
  name: z.string(),
  mime: z.string(),
  size: z.number(),
});

`)

	for _, spec := range specs {
		writeRowSchema(&b, spec)
		writeInputSchema(&b, spec)
		b.WriteString("\n")
	}

	return b.String()
}

func writeRowSchema(b *strings.Builder, spec builder.CollectionSpec) {
	fmt.Fprintf(b, "export const %sSchema = z.object({\n", typeName(spec.Name))
	b.WriteString("  id: z.string(),\n")
	b.WriteString("  created: z.string(),\n")
	b.WriteString("  updated: z.string(),\n")
	if spec.Tenant {
		b.WriteString("  tenant_id: z.string(),\n")
	}
	if spec.Auth {
		b.WriteString("  email: z.string().email(),\n")
		b.WriteString("  verified: z.boolean(),\n")
		b.WriteString("  last_login_at: z.string().nullable().optional(),\n")
	}
	for _, f := range spec.Fields {
		if f.Type == builder.TypePassword {
			continue
		}
		if spec.Auth && isAuthSystemField(f.Name) {
			continue
		}
		fmt.Fprintf(b, "  %s: %s,\n", f.Name, zodSchemaForRow(f))
	}
	b.WriteString("});\n")
}

func writeInputSchema(b *strings.Builder, spec builder.CollectionSpec) {
	fmt.Fprintf(b, "export const %sInputSchema = z.object({\n", typeName(spec.Name))
	if spec.Auth {
		// Auth-collection signups go through /auth-signup, but
		// expose the input schema for completeness — admins / hooks
		// may create users directly.
		b.WriteString("  email: z.string().email(),\n")
	}
	for _, f := range spec.Fields {
		if spec.Auth && isAuthSystemField(f.Name) {
			continue
		}
		fmt.Fprintf(b, "  %s: %s,\n", f.Name, zodSchemaForInput(f))
	}
	b.WriteString("});\n")
}

// zodSchemaForRow chooses the runtime validator for a field as it
// comes BACK from the server — passwords excluded, system Auth fields
// handled at the wrapper level.
func zodSchemaForRow(f builder.FieldSpec) string {
	base := zodBase(f)
	if !f.Required {
		base += ".nullable().optional()"
	}
	return base
}

// zodSchemaForInput is the WRITE variant — slightly looser:
//   - Required is honoured (the server will 400 anyway if missing)
//   - Optional fields end up `.optional()` rather than nullable+optional
//     so callers can omit them from the JSON object
func zodSchemaForInput(f builder.FieldSpec) string {
	base := zodBase(f)
	if !f.Required {
		base += ".optional()"
	}
	return base
}

// zodBase returns the inner schema (no .optional()/.nullable() wrap).
func zodBase(f builder.FieldSpec) string {
	switch f.Type {
	case builder.TypeText, builder.TypeRichText:
		return zodString(f)
	case builder.TypeEmail:
		return "z.string().email()"
	case builder.TypeURL:
		return "z.string().url()"
	case builder.TypeNumber:
		return zodNumber(f)
	case builder.TypeBool:
		return "z.boolean()"
	case builder.TypeDate:
		// PB returns trailing-Z timestamps; zod's .datetime() wants ISO 8601.
		return "z.string()"
	case builder.TypeJSON:
		return "z.record(z.unknown())"
	case builder.TypeSelect:
		if len(f.SelectValues) == 0 {
			return "z.string()"
		}
		return "z.enum([" + zodStringTuple(f.SelectValues) + "])"
	case builder.TypeMultiSelect:
		inner := "z.string()"
		if len(f.SelectValues) > 0 {
			inner = "z.enum([" + zodStringTuple(f.SelectValues) + "])"
		}
		arr := "z.array(" + inner + ")"
		if f.MinSelections != nil {
			arr += fmt.Sprintf(".min(%d)", *f.MinSelections)
		}
		if f.MaxSelections != nil {
			arr += fmt.Sprintf(".max(%d)", *f.MaxSelections)
		}
		return arr
	case builder.TypeFile:
		return "z.string()"
	case builder.TypeFiles:
		return "z.array(FileRefSchema)"
	case builder.TypeRelation:
		return "z.string()"
	case builder.TypeRelations:
		return "z.array(z.string())"
	case builder.TypePassword:
		min := 8
		if f.PasswordMinLen != nil {
			min = *f.PasswordMinLen
		}
		return fmt.Sprintf("z.string().min(%d)", min)
	case builder.TypeTel:
		// E.164: leading '+', 1-15 digits. Server also accepts display
		// forms (spaces/dashes/parens) and normalises; zod here enforces
		// the strict shape because that's what the SERVER returns on
		// read. Input validation on the form is the app's job.
		return `z.string().regex(/^\+[1-9][0-9]{1,14}$/, "must be E.164: +<country><number>")`
	case builder.TypePersonName:
		return "z.object({ first: z.string().max(200).optional(), middle: z.string().max(200).optional(), last: z.string().max(200).optional(), suffix: z.string().max(200).optional(), full: z.string().max(200).optional() })"
	case builder.TypeSlug:
		// Server normalises on write; on READ the value MUST match the
		// CHECK constraint exactly. Zod enforces the strict shape so
		// client-side parsing surfaces drift early.
		return `z.string().regex(/^[a-z0-9]+(-[a-z0-9]+)*$/, "must be lowercase-with-hyphens")`
	case builder.TypeSequentialCode:
		// Server-owned, always present on read. We don't constrain the
		// shape further (prefix + padding format is operator's choice).
		return "z.string()"
	case builder.TypeColor:
		// Server normalises on write; on read the value MATCHES the
		// CHECK exactly. Enforce strict shape so client-side parsing
		// surfaces drift early.
		return `z.string().regex(/^#[0-9a-f]{6}$/, "must be #RRGGBB lowercase hex")`
	case builder.TypeCron:
		// 5 whitespace-separated fields. The full cron grammar is too
		// rich for a regex, so we settle for "5 non-empty fields";
		// server-side compile gives the real check.
		return `z.string().regex(/^\S+ \S+ \S+ \S+ \S+$/, "must be a 5-field cron expression")`
	case builder.TypeMarkdown:
		return zodString(f)
	case builder.TypeFinance, builder.TypePercentage:
		// Decimal-string shape: optional sign, digits, optional fractional.
		return `z.string().regex(/^-?\d+(\.\d+)?$/, "must be a decimal string")`
	case builder.TypeCountry:
		// ISO 3166-1 alpha-2: 2 uppercase ASCII letters. Membership
		// check is server-authoritative; client-side zod enforces shape.
		return `z.string().regex(/^[A-Z]{2}$/, "must be ISO 3166-1 alpha-2 (e.g. US, RU, DE)")`
	case builder.TypeTimezone:
		// IANA tz: "Region/City" or "UTC". Server-side LoadLocation
		// is authoritative; client zod is loose (acceptable shape).
		return `z.string().min(1, "timezone required")`
	case builder.TypeLanguage:
		// ISO 639-1: 2 lowercase ASCII letters. Membership check is
		// server-authoritative; client zod enforces shape only.
		return `z.string().regex(/^[a-z]{2}$/, "must be ISO 639-1 alpha-2 (e.g. en, ru, fr)")`
	case builder.TypeLocale:
		// BCP-47: lowercase lang, optional dash + uppercase region.
		// Both halves validated server-side; client enforces shape.
		return `z.string().regex(/^[a-z]{2}(-[A-Z]{2})?$/, "must be BCP-47 (e.g. en, en-US, pt-BR)")`
	case builder.TypeCoordinates:
		// Geographic point — server checks range; client checks shape +
		// range so a typo surfaces before round-trip.
		return `z.object({ lat: z.number().min(-90).max(90), lng: z.number().min(-180).max(180) })`
	case builder.TypeAddress:
		// Structured address; all components optional individually.
		// Country shape enforced client-side; membership server-side.
		return `z.object({ street: z.string().max(200).optional(), street2: z.string().max(200).optional(), city: z.string().max(200).optional(), region: z.string().max(200).optional(), postal: z.string().max(20).optional(), country: z.string().regex(/^[A-Z]{2}$/, "ISO 3166-1 alpha-2").optional() })`
	case builder.TypeIBAN:
		// Loose shape — server does mod-97. 2 letters + 2 digits + up
		// to 30 alnum.
		return `z.string().regex(/^[A-Z]{2}[0-9]{2}[A-Z0-9]{1,30}$/, "must be canonical IBAN (no spaces, uppercase)")`
	case builder.TypeBIC:
		return `z.string().regex(/^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$/, "must be 8- or 11-char SWIFT/BIC code")`
	case builder.TypeTaxID:
		// Loose shape — per-country format + check digits are server-side.
		// 4-30 uppercase alphanumeric covers every supported country.
		return `z.string().regex(/^[A-Z0-9]{4,30}$/, "tax id must be 4-30 uppercase alphanumeric (no separators)")`
	case builder.TypeBarcode:
		// Auto-detect or per-format — shapes vary. Server enforces
		// the specific format + GS1 check digit; client just ensures
		// non-empty.
		return `z.string().min(1, "barcode required").max(80)`
	case builder.TypeCurrency:
		// ISO 4217 alpha-3: 3 uppercase ASCII letters. Membership
		// server-side; client checks shape.
		return `z.string().regex(/^[A-Z]{3}$/, "must be ISO 4217 alpha-3 (e.g. USD, EUR, RUB)")`
	case builder.TypeMoneyRange:
		// {min, max, currency} — bounds are decimal strings.
		return `z.object({ min: z.string().regex(/^-?\d+(\.\d+)?$/), max: z.string().regex(/^-?\d+(\.\d+)?$/), currency: z.string().regex(/^[A-Z]{3}$/) })`
	case builder.TypeDateRange:
		// Postgres "[start,end)" canonical wire form.
		return `z.string().regex(/^[\[\(](\d{4}-\d{2}-\d{2})?,(\d{4}-\d{2}-\d{2})?[\]\)]$/, "must be Postgres daterange like [2024-01-01,2024-12-31)")`
	case builder.TypeTimeRange:
		// {start, end} — HH:MM or HH:MM:SS.
		return `z.object({ start: z.string().regex(/^[0-2]\d:[0-5]\d(:[0-5]\d)?$/), end: z.string().regex(/^[0-2]\d:[0-5]\d(:[0-5]\d)?$/) })`
	case builder.TypeBankAccount:
		// Country required + ISO 3166-1 shape. Per-country fields
		// are open (different countries have different components);
		// server enforces strict shape per country.
		return `z.object({ country: z.string().regex(/^[A-Z]{2}$/, "ISO 3166-1 alpha-2") }).catchall(z.string())`
	case builder.TypeQRCode:
		// Payload bounded to QR Code max-data envelope.
		return `z.string().min(1).max(4096)`
	case builder.TypeQuantity:
		// Structured object — value is decimal string, unit string.
		// Membership of unit (when allow-list set) is server-side.
		return `z.object({ value: z.string().regex(/^-?\d+(\.\d+)?$/, "decimal string"), unit: z.string().min(1) })`
	case builder.TypeDuration:
		// ISO 8601 duration: P then optional date components, optional
		// T then time components. At least one component required.
		return `z.string().regex(/^P(\d+Y)?(\d+M)?(\d+D)?(T(\d+H)?(\d+M)?(\d+S)?)?$/, "must be ISO 8601 duration like PT5M or P1DT2H").refine(s => s !== "P" && s !== "PT", { message: "must contain at least one component" })`
	case builder.TypeStatus:
		// Literal enum of declared states.
		if len(f.StatusValues) == 0 {
			return "z.string()"
		}
		quoted := make([]string, len(f.StatusValues))
		for i, v := range f.StatusValues {
			quoted[i] = quote(v)
		}
		return fmt.Sprintf("z.enum([%s])", strings.Join(quoted, ", "))
	case builder.TypePriority, builder.TypeRating:
		// Integer with optional Min/Max.
		s := "z.number().int()"
		if f.IntMin != nil {
			s += fmt.Sprintf(".min(%d)", *f.IntMin)
		}
		if f.IntMax != nil {
			s += fmt.Sprintf(".max(%d)", *f.IntMax)
		}
		return s
	case builder.TypeTags:
		// Array of lowercase non-empty strings. Server dedupes + sorts.
		s := "z.array(z.string().min(1)"
		if f.TagMaxLen > 0 {
			s += fmt.Sprintf(".max(%d)", f.TagMaxLen)
		}
		s += ")"
		if f.TagsMaxCount > 0 {
			s += fmt.Sprintf(".max(%d)", f.TagsMaxCount)
		}
		return s
	case builder.TypeTreePath:
		// LTREE: dot-separated labels, each [A-Za-z0-9_]+, total length capped.
		return `z.string().regex(/^([A-Za-z0-9_]+)(\.[A-Za-z0-9_]+)*$|^$/, "must be ltree path (labels separated by dots)")`
	default:
		return "z.unknown()"
	}
}

func zodString(f builder.FieldSpec) string {
	s := "z.string()"
	if f.MinLen != nil {
		s += fmt.Sprintf(".min(%d)", *f.MinLen)
	}
	if f.MaxLen != nil {
		s += fmt.Sprintf(".max(%d)", *f.MaxLen)
	}
	return s
}

func zodNumber(f builder.FieldSpec) string {
	s := "z.number()"
	if f.IsInt {
		s += ".int()"
	}
	if f.Min != nil {
		s += fmt.Sprintf(".min(%v)", *f.Min)
	}
	if f.Max != nil {
		s += fmt.Sprintf(".max(%v)", *f.Max)
	}
	return s
}

func zodStringTuple(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = quote(v)
	}
	return strings.Join(parts, ", ")
}
