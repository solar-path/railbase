package builder

import "strings"

// This file groups the "single-column primitive" field types. They
// all follow the same pattern:
//
//   - Constructor returns *XxxField with FieldSpec{Type: TypeXxx}.
//   - Each modifier mutates the embedded spec and returns the receiver
//     so chains stay type-correct.
//   - Spec() returns the captured FieldSpec; CollectionBuilder.Field
//     stamps in the column name.
//
// Per-type modifiers reflect what's reasonable for that semantic
// type — e.g. Email has no FTS() because we never search emails as
// natural language, and Password has no Default() because hashes
// aren't user-configurable defaults.
//
// The duplication of the shared modifiers (Required/Unique/Index/
// Default) on each type is the price for type-correct chaining; we
// considered embedding a baseField struct but that would force users
// to break the chain to set required-on-text vs required-on-number.

// ---- Text ----

type TextField struct{ s FieldSpec }

func NewText() *TextField { return &TextField{s: FieldSpec{Type: TypeText}} }

func (f *TextField) Required() *TextField  { f.s.Required = true; return f }
func (f *TextField) Unique() *TextField    { f.s.Unique = true; return f }
func (f *TextField) Index() *TextField     { f.s.Indexed = true; return f }
func (f *TextField) Default(v string) *TextField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *TextField) MinLen(n int) *TextField     { f.s.MinLen = &n; return f }
func (f *TextField) MaxLen(n int) *TextField     { f.s.MaxLen = &n; return f }
func (f *TextField) Pattern(re string) *TextField { f.s.Pattern = re; return f }
func (f *TextField) FTS() *TextField              { f.s.FTS = true; return f }

// Translatable marks the field as a per-locale translation map. The
// column type becomes JSONB; the REST layer validates the shape on
// write (`{xx: "value", xx-XX: "value", ...}`) and picks the best
// locale on read using the request's negotiated locale + Catalog
// fallback chain. See FieldSpec.Translatable for full semantics.
//
// Pattern / MinLen / MaxLen / FTS are NOT applied per-value when
// Translatable is set — those validators are scalar-only in v1.
func (f *TextField) Translatable() *TextField { f.s.Translatable = true; return f }

// Computed makes this text field a Postgres generated-stored column
// whose value is `expr` evaluated against the row. Read-only on the
// CRUD API. See FieldSpec.Computed for the full contract.
//
//	Field("full_name", Text().Computed("first || ' ' || last"))
func (f *TextField) Computed(expr string) *TextField { f.s.Computed = expr; return f }

func (f *TextField) Spec() FieldSpec { return f.s }

// ---- Number ----

type NumberField struct{ s FieldSpec }

func NewNumber() *NumberField { return &NumberField{s: FieldSpec{Type: TypeNumber}} }

func (f *NumberField) Required() *NumberField  { f.s.Required = true; return f }
func (f *NumberField) Unique() *NumberField    { f.s.Unique = true; return f }
func (f *NumberField) Index() *NumberField     { f.s.Indexed = true; return f }
func (f *NumberField) Default(v float64) *NumberField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *NumberField) Min(v float64) *NumberField { f.s.Min = &v; return f }
func (f *NumberField) Max(v float64) *NumberField { f.s.Max = &v; return f }
// Int switches storage from DOUBLE PRECISION to BIGINT. Use for
// counters, IDs of related external systems, anything where you'd
// reach for `int64` rather than `float64`.
func (f *NumberField) Int() *NumberField  { f.s.IsInt = true; return f }

// Computed marks this number field as a Postgres generated-stored
// column. Same contract as TextField.Computed — see
// FieldSpec.Computed.
//
//	Field("total", Number().Computed("price * quantity"))
func (f *NumberField) Computed(expr string) *NumberField { f.s.Computed = expr; return f }

func (f *NumberField) Spec() FieldSpec { return f.s }

// ---- Bool ----

type BoolField struct{ s FieldSpec }

func NewBool() *BoolField { return &BoolField{s: FieldSpec{Type: TypeBool}} }

func (f *BoolField) Required() *BoolField { f.s.Required = true; return f }
func (f *BoolField) Index() *BoolField    { f.s.Indexed = true; return f }
func (f *BoolField) Default(v bool) *BoolField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
// Computed marks this bool field as a Postgres generated-stored
// column. Same contract as TextField.Computed.
//
//	Field("is_overdue", Bool().Computed("due_date < now()"))
func (f *BoolField) Computed(expr string) *BoolField { f.s.Computed = expr; return f }

func (f *BoolField) Spec() FieldSpec { return f.s }

// ---- Date (TIMESTAMPTZ) ----

type DateField struct{ s FieldSpec }

func NewDate() *DateField { return &DateField{s: FieldSpec{Type: TypeDate}} }

func (f *DateField) Required() *DateField { f.s.Required = true; return f }
func (f *DateField) Index() *DateField    { f.s.Indexed = true; return f }

// AutoCreate sets the column to `DEFAULT now()` so INSERTs without
// the field get the current time. Implies Required.
func (f *DateField) AutoCreate() *DateField {
	f.s.AutoCreate, f.s.Required = true, true
	return f
}

// AutoUpdate installs a row-level trigger that bumps the column to
// `now()` on every UPDATE. Implies Required + AutoCreate (the value
// has to start at row creation time).
func (f *DateField) AutoUpdate() *DateField {
	f.s.AutoUpdate, f.s.AutoCreate, f.s.Required = true, true, true
	return f
}
func (f *DateField) Spec() FieldSpec { return f.s }

// ---- Email ----

// EmailField is TEXT with an RFC 5322-shaped CHECK constraint and
// PB-compat type name "email" in introspection.
type EmailField struct{ s FieldSpec }

func NewEmail() *EmailField { return &EmailField{s: FieldSpec{Type: TypeEmail}} }

func (f *EmailField) Required() *EmailField { f.s.Required = true; return f }
func (f *EmailField) Unique() *EmailField   { f.s.Unique = true; return f }
func (f *EmailField) Index() *EmailField    { f.s.Indexed = true; return f }
func (f *EmailField) Default(v string) *EmailField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *EmailField) Spec() FieldSpec { return f.s }

// ---- URL ----

type URLField struct{ s FieldSpec }

func NewURL() *URLField { return &URLField{s: FieldSpec{Type: TypeURL}} }

func (f *URLField) Required() *URLField { f.s.Required = true; return f }
func (f *URLField) Unique() *URLField   { f.s.Unique = true; return f }
func (f *URLField) Index() *URLField    { f.s.Indexed = true; return f }
func (f *URLField) Default(v string) *URLField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *URLField) Spec() FieldSpec { return f.s }

// ---- JSON ----

// JSONField stores arbitrary JSON in a JSONB column. The schema-DSL
// layer is intentionally untyped here; v0.3 will add `JSONOf[T]()`
// for compile-time typing.
type JSONField struct{ s FieldSpec }

func NewJSON() *JSONField { return &JSONField{s: FieldSpec{Type: TypeJSON}} }

func (f *JSONField) Required() *JSONField { f.s.Required = true; return f }
func (f *JSONField) Index() *JSONField    { f.s.Indexed = true; return f } // GIN
func (f *JSONField) Default(v any) *JSONField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}

// ArrayOfUUIDReferences declares the field as a JSONB array whose
// elements MUST be string UUIDs referencing `target`'s id column.
// REST CRUD enforces this on every create/update: each element gets
// looked up, missing references → 422. Closes Sentinel's
// `predecessors JSONB` papercut where FK validity was client-side-
// only ("FK validity (predecessor exists, same project) is enforced
// client-side" — tasks.go:15).
//
// Migration aid, not the recommended target shape: for proper M2M
// see TypeRelations / .Relations() with the v3 junction-table CRUD.
// ArrayOfUUIDReferences exists for JSON arrays that aren't
// edges-with-attributes (no per-edge sort_index or metadata) where a
// real M2M would be overkill.
func (f *JSONField) ArrayOfUUIDReferences(target string) *JSONField {
	f.s.JSONElementRefCollection = target
	return f
}

// SameValueAs declares that for each element of this JSON array (which
// must have been declared as `ArrayOfUUIDReferences`), the referenced
// row's `peerColumn` must equal THIS row's `peerColumn`. The
// canonical use: "predecessors must live in the same project as
// this task" — `SameValueAs("project")`.
//
// Caller's responsibility: `peerColumn` must exist on both this
// collection AND the referenced collection. Mismatch surfaces as a
// 500 at write time (we don't cross-validate the schema graph here).
func (f *JSONField) SameValueAs(peerColumn string) *JSONField {
	f.s.JSONElementPeerEqual = peerColumn
	return f
}

func (f *JSONField) Spec() FieldSpec { return f.s }

// ---- Password ----

// PasswordField stores an Argon2id hash. The plaintext is never
// persisted and never returned by the API. Default() is intentionally
// not exposed — there's no meaningful default password.
type PasswordField struct{ s FieldSpec }

func NewPassword() *PasswordField { return &PasswordField{s: FieldSpec{Type: TypePassword}} }

func (f *PasswordField) Required() *PasswordField { f.s.Required = true; return f }
func (f *PasswordField) MinLen(n int) *PasswordField {
	f.s.PasswordMinLen = &n
	return f
}
func (f *PasswordField) Spec() FieldSpec { return f.s }

// ---- RichText ----

// RichTextField stores sanitised HTML. Sanitisation is on by default
// (bluemonday); call .NoSanitize() if the source is trusted.
type RichTextField struct{ s FieldSpec }

func NewRichText() *RichTextField { return &RichTextField{s: FieldSpec{Type: TypeRichText}} }

func (f *RichTextField) Required() *RichTextField { f.s.Required = true; return f }
func (f *RichTextField) FTS() *RichTextField      { f.s.FTS = true; return f }
func (f *RichTextField) NoSanitize() *RichTextField {
	f.s.RichTextNoSanitize = true
	return f
}

// Translatable — see TextField.Translatable. Sanitisation still runs
// per-value when set: each locale's value passes through bluemonday
// before storage.
func (f *RichTextField) Translatable() *RichTextField {
	f.s.Translatable = true
	return f
}
func (f *RichTextField) Spec() FieldSpec { return f.s }

// ---- v1.4.2 Domain types: Communication ----

// TelField stores a phone number as TEXT in canonical E.164 form.
// REST + SDK accept input in display form (with separators) and the
// REST layer normalises before INSERT — the column itself is the
// validated canonical form.
type TelField struct{ s FieldSpec }

// NewTel constructs a phone number field.
//
// Storage: TEXT. CHECK: `^\+[1-9]\d{1,14}$` (E.164 RFC 5733). Display
// formatting (parens, dashes, country abbrev) is the SDK's job; the
// DB only ever sees the canonical form.
func NewTel() *TelField { return &TelField{s: FieldSpec{Type: TypeTel}} }

func (f *TelField) Required() *TelField { f.s.Required = true; return f }
func (f *TelField) Unique() *TelField   { f.s.Unique = true; return f }
func (f *TelField) Index() *TelField    { f.s.Indexed = true; return f }
func (f *TelField) Default(v string) *TelField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *TelField) Spec() FieldSpec { return f.s }

// PersonNameField stores a structured person name as JSONB.
//
// Keys (all optional): first, middle, last, suffix, full. REST input
// accepts a plain string (treated as `full`) OR an object subset; the
// REST layer validates that only known keys are present and each value
// is a string ≤ 200 chars. Storage layout (JSONB) is identical to what
// the read-side returns, so admin UI / SDK / hooks can address the
// components symmetrically.
type PersonNameField struct{ s FieldSpec }

// NewPersonName constructs a structured person name.
func NewPersonName() *PersonNameField {
	return &PersonNameField{s: FieldSpec{Type: TypePersonName}}
}

func (f *PersonNameField) Required() *PersonNameField { f.s.Required = true; return f }
func (f *PersonNameField) Index() *PersonNameField    { f.s.Indexed = true; return f }
func (f *PersonNameField) Spec() FieldSpec            { return f.s }

// AddressField stores a structured postal address as JSONB.
//
// Keys (all optional individually; at least one required): street,
// street2, city, region, postal, country. The REST layer validates
// only known keys are present, each value is a non-empty string,
// country is ISO 3166-1 alpha-2 (uppercase canonical), postal is
// 1-20 chars. No per-country postal format check — country-specific
// validation lives in operator hooks where business rules also live.
type AddressField struct{ s FieldSpec }

// NewAddress constructs a structured address field.
func NewAddress() *AddressField {
	return &AddressField{s: FieldSpec{Type: TypeAddress}}
}

func (f *AddressField) Required() *AddressField { f.s.Required = true; return f }
func (f *AddressField) Index() *AddressField    { f.s.Indexed = true; return f }
func (f *AddressField) Spec() FieldSpec         { return f.s }

// TaxIDField stores a per-country tax identifier as TEXT in canonical
// compact form (no spaces, no dashes, uppercase). The country either
// comes from the value itself (EU VAT format DE123456789) or from the
// operator-declared `.Country("US")` hint for non-prefix IDs.
type TaxIDField struct{ s FieldSpec }

// NewTaxID constructs a tax-id field. Use `.Country("US")` on the
// builder for non-EU-VAT identifiers (US EIN, RU INN, etc.).
func NewTaxID() *TaxIDField { return &TaxIDField{s: FieldSpec{Type: TypeTaxID}} }

func (f *TaxIDField) Required() *TaxIDField { f.s.Required = true; return f }
func (f *TaxIDField) Unique() *TaxIDField   { f.s.Unique = true; return f }
func (f *TaxIDField) Index() *TaxIDField    { f.s.Indexed = true; return f }

// Country fixes the country for non-EU-VAT identifiers. ISO 3166-1
// alpha-2, case-insensitive ("us" / "US" / "Us" all work).
func (f *TaxIDField) Country(cc string) *TaxIDField {
	f.s.TaxCountry = strings.ToUpper(strings.TrimSpace(cc))
	return f
}
func (f *TaxIDField) Spec() FieldSpec { return f.s }

// BarcodeField stores a product barcode in canonical form. Default
// auto-detects by length: 8 = EAN-8, 12 = UPC-A, 13 = EAN-13; all
// digit-only with mod-10 check digit. `.Format("code128")` opts out
// of the digit-only + check-digit rule for alphanumeric Code-128.
type BarcodeField struct{ s FieldSpec }

// NewBarcode constructs a barcode field with auto-detect default.
func NewBarcode() *BarcodeField { return &BarcodeField{s: FieldSpec{Type: TypeBarcode}} }

func (f *BarcodeField) Required() *BarcodeField { f.s.Required = true; return f }
func (f *BarcodeField) Unique() *BarcodeField   { f.s.Unique = true; return f }
func (f *BarcodeField) Index() *BarcodeField    { f.s.Indexed = true; return f }

// Format fixes the expected barcode format. Accepted values:
//
//	"ean13" / "ean8" / "upca" / "code128"
//
// Empty (default) = auto-detect digit-only formats by length.
func (f *BarcodeField) Format(format string) *BarcodeField {
	f.s.BarcodeFormat = strings.ToLower(strings.TrimSpace(format))
	return f
}
func (f *BarcodeField) Spec() FieldSpec { return f.s }

// CurrencyField stores an ISO 4217 alpha-3 currency code (TEXT).
// REST validates against the embedded ~180-code table; CHECK enforces
// shape ^[A-Z]{3}$.
type CurrencyField struct{ s FieldSpec }

// NewCurrency constructs a currency code field.
func NewCurrency() *CurrencyField { return &CurrencyField{s: FieldSpec{Type: TypeCurrency}} }

func (f *CurrencyField) Required() *CurrencyField { f.s.Required = true; return f }
func (f *CurrencyField) Index() *CurrencyField    { f.s.Indexed = true; return f }
func (f *CurrencyField) Default(code string) *CurrencyField {
	f.s.HasDefault, f.s.Default = true, code
	return f
}
func (f *CurrencyField) Spec() FieldSpec { return f.s }

// MoneyRangeField stores an upper/lower money pair as JSONB. Reuses
// the v1.4.6 finance NumericPrecision/Scale + MinDecimal/MaxDecimal
// modifiers — bounds (min/max of the range) are decimal strings under
// the declared precision; outer bounds (operator-set Min/Max for the
// whole field) clamp both ends of every row.
type MoneyRangeField struct{ s FieldSpec }

// NewMoneyRange constructs a money-range field. Default precision +
// scale match finance: NUMERIC(15, 4). Use `.Precision(p, s)` for
// non-default monetary precision.
func NewMoneyRange() *MoneyRangeField {
	return &MoneyRangeField{s: FieldSpec{Type: TypeMoneyRange}}
}

func (f *MoneyRangeField) Required() *MoneyRangeField { f.s.Required = true; return f }
func (f *MoneyRangeField) Index() *MoneyRangeField    { f.s.Indexed = true; return f }

// Precision overrides the NUMERIC(precision, scale) used to validate
// each bound's decimal string. Default 15, 4 — matches finance.
func (f *MoneyRangeField) Precision(p, s int) *MoneyRangeField {
	f.s.NumericPrecision, f.s.NumericScale = p, s
	return f
}

// Min sets a CHECK lower bound — every row's min must be ≥ this value.
func (f *MoneyRangeField) Min(value string) *MoneyRangeField {
	f.s.MinDecimal = value
	return f
}

// Max sets a CHECK upper bound — every row's max must be ≤ this value.
func (f *MoneyRangeField) Max(value string) *MoneyRangeField {
	f.s.MaxDecimal = value
	return f
}
func (f *MoneyRangeField) Spec() FieldSpec { return f.s }

// DateRangeField stores a date interval as Postgres `daterange`.
type DateRangeField struct{ s FieldSpec }

// NewDateRange constructs a date-range field.
func NewDateRange() *DateRangeField { return &DateRangeField{s: FieldSpec{Type: TypeDateRange}} }

func (f *DateRangeField) Required() *DateRangeField { f.s.Required = true; return f }
func (f *DateRangeField) Index() *DateRangeField    { f.s.Indexed = true; return f }
func (f *DateRangeField) Spec() FieldSpec           { return f.s }

// TimeRangeField stores a time-of-day interval as JSONB.
type TimeRangeField struct{ s FieldSpec }

// NewTimeRange constructs a time-range field.
func NewTimeRange() *TimeRangeField { return &TimeRangeField{s: FieldSpec{Type: TypeTimeRange}} }

func (f *TimeRangeField) Required() *TimeRangeField { f.s.Required = true; return f }
func (f *TimeRangeField) Index() *TimeRangeField    { f.s.Indexed = true; return f }
func (f *TimeRangeField) Spec() FieldSpec           { return f.s }

// BankAccountField stores a generic per-country bank account as JSONB.
// Use IBANField for SEPA-style accounts where the IBAN is the
// canonical identifier; this is the fallback for US ABA, UK sort
// code, CA institution+transit etc.
type BankAccountField struct{ s FieldSpec }

// NewBankAccount constructs a bank-account field.
func NewBankAccount() *BankAccountField {
	return &BankAccountField{s: FieldSpec{Type: TypeBankAccount}}
}

func (f *BankAccountField) Required() *BankAccountField { f.s.Required = true; return f }
func (f *BankAccountField) Index() *BankAccountField    { f.s.Indexed = true; return f }
func (f *BankAccountField) Spec() FieldSpec             { return f.s }

// QRCodeField stores a payload string for QR-code rendering. Use
// `.Format("url" / "vcard" / "wifi" / "epc" / "raw")` to declare the
// payload kind — used by admin UI / SDK to pick the right renderer.
type QRCodeField struct{ s FieldSpec }

// NewQRCode constructs a QR-code field with default format "raw".
func NewQRCode() *QRCodeField { return &QRCodeField{s: FieldSpec{Type: TypeQRCode}} }

func (f *QRCodeField) Required() *QRCodeField { f.s.Required = true; return f }
func (f *QRCodeField) Index() *QRCodeField    { f.s.Indexed = true; return f }

// Format sets the QR payload format hint. Accepted values:
// "url" / "vcard" / "wifi" / "epc" / "raw". Empty defaults to "raw".
func (f *QRCodeField) Format(format string) *QRCodeField {
	f.s.QRFormat = strings.ToLower(strings.TrimSpace(format))
	return f
}
func (f *QRCodeField) Spec() FieldSpec { return f.s }

// ---- v1.4.4 Domain types: Identifiers ----

// SlugField stores a URL-safe identifier in canonical lowercase-hyphen
// form. The CHECK constraint `^[a-z0-9]+(-[a-z0-9]+)*$` is enforced at
// the DB level; the REST layer normalises display input (mixed case,
// spaces, punctuation) into that shape before INSERT.
//
// The common pattern is `schema.Slug().From("title").Unique()` — the
// `From` clause makes the slug auto-derive when the client omits it
// on POST, so apps don't need to ship slug logic client-side.
type SlugField struct{ s FieldSpec }

// NewSlug constructs a slug column. By default it is non-unique; most
// real-world slugs want Unique() — be explicit.
func NewSlug() *SlugField {
	return &SlugField{s: FieldSpec{Type: TypeSlug, Indexed: true}}
}

// From sets the source field name used to auto-derive the slug when
// the client omits it on INSERT. The source field must exist on the
// same collection and resolve to a string. Empty (default) = client
// must supply the slug explicitly.
func (f *SlugField) From(field string) *SlugField { f.s.SlugFrom = field; return f }

func (f *SlugField) Required() *SlugField { f.s.Required = true; return f }
func (f *SlugField) Unique() *SlugField   { f.s.Unique = true; return f }
func (f *SlugField) Index() *SlugField    { f.s.Indexed = true; return f }
func (f *SlugField) Spec() FieldSpec      { return f.s }

// SequentialCodeField is a monotonic identifier formed from a
// per-collection Postgres SEQUENCE plus optional prefix + zero-padding.
// Generation is server-owned: the INSERT path fetches `nextval()` from
// the collection-scoped sequence and renders the formatted string.
// Clients cannot override the value (any client-supplied value is
// ignored on both INSERT and UPDATE).
//
// Builder example: `schema.SequentialCode().Prefix("INV-").Pad(5)`
// produces "INV-00001", "INV-00002", ... starting at 1.
type SequentialCodeField struct{ s FieldSpec }

// NewSequentialCode constructs a sequential identifier column.
// Unique() is implied by the underlying sequence + the column UNIQUE
// constraint emitted in SQL gen, so the modifier isn't exposed
// separately — every sequential_code field IS unique.
func NewSequentialCode() *SequentialCodeField {
	return &SequentialCodeField{s: FieldSpec{Type: TypeSequentialCode, Required: true, Unique: true, Indexed: true}}
}

// Prefix sets a literal string prepended to the rendered number.
// Common: "INV-", "ORD-", "CUST-". Empty (default) = no prefix.
func (f *SequentialCodeField) Prefix(p string) *SequentialCodeField {
	f.s.SeqPrefix = p
	return f
}

// Pad sets the minimum width of the numeric portion, zero-padded on
// the left. E.g. Pad(5) → "00001". Zero or unset = no padding.
func (f *SequentialCodeField) Pad(n int) *SequentialCodeField {
	f.s.SeqPad = n
	return f
}

// Start sets the first sequence value (Postgres default = 1). Useful
// when continuing an external numbering scheme during migration.
func (f *SequentialCodeField) Start(n int64) *SequentialCodeField {
	f.s.SeqStart = n
	return f
}

func (f *SequentialCodeField) Spec() FieldSpec { return f.s }

// ---- v1.4.5 Domain types: Content ----

// ColorField stores a hex color string in canonical lowercase 6-digit
// form (`#ff5733`). REST normalises common write forms (`#FFF`,
// `#ABC123`, `ABCDEF`) before INSERT; CHECK enforces the canonical
// shape so what's read back is always parser-ready.
type ColorField struct{ s FieldSpec }

// NewColor constructs a color field.
func NewColor() *ColorField { return &ColorField{s: FieldSpec{Type: TypeColor}} }

func (f *ColorField) Required() *ColorField { f.s.Required = true; return f }
func (f *ColorField) Index() *ColorField    { f.s.Indexed = true; return f }
func (f *ColorField) Default(v string) *ColorField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *ColorField) Spec() FieldSpec { return f.s }

// CronField stores a 5-field crontab expression as TEXT. The REST
// layer validates the string against the same parser the Cron
// scheduler uses, so saving a value implies it's compilable. No DB
// CHECK constraint — cron grammar is richer than a regex can express.
type CronField struct{ s FieldSpec }

// NewCron constructs a cron-expression field.
func NewCron() *CronField { return &CronField{s: FieldSpec{Type: TypeCron}} }

func (f *CronField) Required() *CronField { f.s.Required = true; return f }
func (f *CronField) Default(v string) *CronField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *CronField) Spec() FieldSpec { return f.s }

// MarkdownField stores raw Markdown source as TEXT. No write-side
// validation (Markdown is intentionally forgiving). The type tag lets
// admin UI surface a preview pane and SDK consumers pick a richer
// editor than plain Text.
type MarkdownField struct{ s FieldSpec }

// NewMarkdown constructs a Markdown content field.
func NewMarkdown() *MarkdownField { return &MarkdownField{s: FieldSpec{Type: TypeMarkdown}} }

func (f *MarkdownField) Required() *MarkdownField { f.s.Required = true; return f }
func (f *MarkdownField) MinLen(n int) *MarkdownField {
	f.s.MinLen = &n
	return f
}
func (f *MarkdownField) MaxLen(n int) *MarkdownField {
	f.s.MaxLen = &n
	return f
}
// FTS enables a Postgres GIN index over `to_tsvector('simple', col)`
// for full-text search on the Markdown body. Same machinery as
// `Text().FTS()`.
func (f *MarkdownField) FTS() *MarkdownField { f.s.FTS = true; return f }

// Translatable — see TextField.Translatable.
func (f *MarkdownField) Translatable() *MarkdownField {
	f.s.Translatable = true
	return f
}
func (f *MarkdownField) Spec() FieldSpec { return f.s }

// ---- v1.4.6 Domain types: Money primitives ----

// FinanceField stores a fixed-point decimal as NUMERIC(precision,
// scale). The default precision/scale of 15/4 supports values up to
// ~99 trillion with 4-decimal precision. NEVER use Number().Float()
// for monetary values — IEEE 754 loses precision at any non-trivial
// scale, and "0.1 + 0.2 = 0.30000000000000004" is unacceptable in
// accounting.
//
// On the wire (REST + SDK) the value is a STRING, not a JSON number,
// because JSON parsers in some languages silently convert to f64. The
// SDK emits `string` typing for the same reason.
type FinanceField struct{ s FieldSpec }

// NewFinance constructs a fixed-point decimal field with default
// NUMERIC(15, 4). Call .Precision() / .Scale() to override.
func NewFinance() *FinanceField {
	return &FinanceField{s: FieldSpec{Type: TypeFinance, NumericPrecision: 15, NumericScale: 4}}
}

func (f *FinanceField) Required() *FinanceField { f.s.Required = true; return f }
func (f *FinanceField) Index() *FinanceField    { f.s.Indexed = true; return f }
func (f *FinanceField) Default(decimalString string) *FinanceField {
	f.s.HasDefault, f.s.Default = true, decimalString
	return f
}

// Precision sets the total number of digits (NUMERIC(p, s) — p).
// Must satisfy 1 ≤ p ≤ 1000, p ≥ scale. Validate() enforces.
func (f *FinanceField) Precision(p int) *FinanceField { f.s.NumericPrecision = p; return f }

// Scale sets the digits after the decimal point (NUMERIC(p, s) — s).
// Must satisfy 0 ≤ s ≤ precision.
func (f *FinanceField) Scale(s int) *FinanceField { f.s.NumericScale = s; return f }

// Min/Max set CHECK bounds. Decimal strings — pass exactly what
// you want in the constraint (no float conversion involved).
func (f *FinanceField) Min(decimal string) *FinanceField { f.s.MinDecimal = decimal; return f }
func (f *FinanceField) Max(decimal string) *FinanceField { f.s.MaxDecimal = decimal; return f }
func (f *FinanceField) Spec() FieldSpec                  { return f.s }

// PercentageField stores a percentage as NUMERIC(5, 2). Default range
// is 0..100 with hundredth-precision (matches "% off" UI expectation);
// call `.Range(0, 1)` if your domain uses the fractional 0..1 shape.
type PercentageField struct{ s FieldSpec }

// NewPercentage constructs a percentage field with default
// NUMERIC(5, 2) and CHECK between 0 and 100.
func NewPercentage() *PercentageField {
	return &PercentageField{s: FieldSpec{
		Type:             TypePercentage,
		NumericPrecision: 5, NumericScale: 2,
		MinDecimal: "0", MaxDecimal: "100",
	}}
}

func (f *PercentageField) Required() *PercentageField { f.s.Required = true; return f }
func (f *PercentageField) Index() *PercentageField    { f.s.Indexed = true; return f }
func (f *PercentageField) Default(decimal string) *PercentageField {
	f.s.HasDefault, f.s.Default = true, decimal
	return f
}

// Range overrides the default 0..100 CHECK bounds. Decimal strings
// avoid float-to-string drift in the emitted SQL.
func (f *PercentageField) Range(minDec, maxDec string) *PercentageField {
	f.s.MinDecimal, f.s.MaxDecimal = minDec, maxDec
	return f
}

// Precision/Scale tune the NUMERIC(p, s) for callers needing more
// decimals (e.g. 6-decimal interest rates).
func (f *PercentageField) Precision(p int) *PercentageField { f.s.NumericPrecision = p; return f }
func (f *PercentageField) Scale(s int) *PercentageField     { f.s.NumericScale = s; return f }
func (f *PercentageField) Spec() FieldSpec                  { return f.s }

// ---- v1.4.7 Domain types: Locale ----

// CountryField stores an ISO 3166-1 alpha-2 country code (TEXT,
// 2 uppercase letters). REST lower→upper-cases and validates
// membership against the embedded ISO list. CHECK constraint enforces
// the shape (`^[A-Z]{2}$`) but not list membership — codes get
// added/retired without DB migrations.
type CountryField struct{ s FieldSpec }

// NewCountry constructs a country code field.
func NewCountry() *CountryField { return &CountryField{s: FieldSpec{Type: TypeCountry}} }

func (f *CountryField) Required() *CountryField { f.s.Required = true; return f }
func (f *CountryField) Index() *CountryField    { f.s.Indexed = true; return f }
func (f *CountryField) Default(code string) *CountryField {
	f.s.HasDefault, f.s.Default = true, code
	return f
}
func (f *CountryField) Spec() FieldSpec { return f.s }

// TimezoneField stores an IANA timezone identifier (TEXT). REST
// validates via Go's stdlib `time.LoadLocation` — same tz database
// Postgres uses internally, so `now() AT TIME ZONE <col>` works.
type TimezoneField struct{ s FieldSpec }

// NewTimezone constructs a timezone field.
func NewTimezone() *TimezoneField { return &TimezoneField{s: FieldSpec{Type: TypeTimezone}} }

func (f *TimezoneField) Required() *TimezoneField { f.s.Required = true; return f }
func (f *TimezoneField) Index() *TimezoneField    { f.s.Indexed = true; return f }
func (f *TimezoneField) Default(zone string) *TimezoneField {
	f.s.HasDefault, f.s.Default = true, zone
	return f
}
func (f *TimezoneField) Spec() FieldSpec { return f.s }

// LanguageField stores an ISO 639-1 alpha-2 language code (TEXT). REST
// validates against the embedded 184-code table; CHECK enforces shape.
type LanguageField struct{ s FieldSpec }

// NewLanguage constructs a language code field.
func NewLanguage() *LanguageField { return &LanguageField{s: FieldSpec{Type: TypeLanguage}} }

func (f *LanguageField) Required() *LanguageField { f.s.Required = true; return f }
func (f *LanguageField) Index() *LanguageField    { f.s.Indexed = true; return f }
func (f *LanguageField) Default(code string) *LanguageField {
	f.s.HasDefault, f.s.Default = true, code
	return f
}
func (f *LanguageField) Spec() FieldSpec { return f.s }

// LocaleField stores a BCP-47 locale (language[-REGION]) as TEXT. REST
// validates both halves against ISO 639-1 + ISO 3166-1.
type LocaleField struct{ s FieldSpec }

// NewLocale constructs a locale field.
func NewLocale() *LocaleField { return &LocaleField{s: FieldSpec{Type: TypeLocale}} }

func (f *LocaleField) Required() *LocaleField { f.s.Required = true; return f }
func (f *LocaleField) Index() *LocaleField    { f.s.Indexed = true; return f }
func (f *LocaleField) Default(tag string) *LocaleField {
	f.s.HasDefault, f.s.Default = true, tag
	return f
}
func (f *LocaleField) Spec() FieldSpec { return f.s }

// CoordinatesField stores a {lat, lng} geographic point as JSONB. REST
// validates lat ∈ [-90, 90], lng ∈ [-180, 180].
type CoordinatesField struct{ s FieldSpec }

// NewCoordinates constructs a coordinates field.
func NewCoordinates() *CoordinatesField { return &CoordinatesField{s: FieldSpec{Type: TypeCoordinates}} }

func (f *CoordinatesField) Required() *CoordinatesField { f.s.Required = true; return f }
func (f *CoordinatesField) Index() *CoordinatesField    { f.s.Indexed = true; return f }
func (f *CoordinatesField) Spec() FieldSpec             { return f.s }

// ---- v1.4.8 Domain types: Banking ----

// IBANField stores an International Bank Account Number in canonical
// compact form (no spaces, uppercase). REST validates the ISO 3166-1
// country prefix, per-country length, and mod-97 check digits. CHECK
// constraint enforces shape only — membership / check-digit is
// app-layer (the algorithm shouldn't be replicated in SQL).
type IBANField struct{ s FieldSpec }

// NewIBAN constructs an IBAN field. Defaults to Unique=true + Index
// because IBANs are typically queried by value (account lookup).
func NewIBAN() *IBANField {
	return &IBANField{s: FieldSpec{Type: TypeIBAN, Unique: true, Indexed: true}}
}

func (f *IBANField) Required() *IBANField { f.s.Required = true; return f }
func (f *IBANField) Default(v string) *IBANField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *IBANField) Spec() FieldSpec { return f.s }

// BICField stores a SWIFT/BIC code in canonical 8- or 11-char
// uppercase form. REST validates the shape; no DB CHECK because the
// regex would be a leap (4 letters + 2 letters + 2 alnum + optional
// 3 alnum) and Postgres regex syntax differs from app side.
type BICField struct{ s FieldSpec }

// NewBIC constructs a BIC field.
func NewBIC() *BICField { return &BICField{s: FieldSpec{Type: TypeBIC, Indexed: true}} }

func (f *BICField) Required() *BICField { f.s.Required = true; return f }
func (f *BICField) Unique() *BICField   { f.s.Unique = true; return f }
func (f *BICField) Default(v string) *BICField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *BICField) Spec() FieldSpec { return f.s }

// ---- v1.4.9 Domain types: Quantities ----

// QuantityField stores a value-with-unit-of-measure as JSONB:
// `{value: "10.5", unit: "kg"}`. The value is a decimal string (same
// convention as Finance — no float precision loss); the unit is
// validated against the allow-list set via `.Units(...)`.
//
// No conversion machinery built-in. Operators who need kg↔lb math
// implement it via hooks or pick a UOM plugin — keeping the type
// itself small and predictable.
type QuantityField struct{ s FieldSpec }

// NewQuantity constructs a quantity field with no unit restriction.
// Call `.Units("kg", "lb", "g")` to constrain.
func NewQuantity() *QuantityField {
	return &QuantityField{s: FieldSpec{Type: TypeQuantity}}
}

// Units restricts the field's `unit` component to the given values.
// Validation happens at REST time, not in DB CHECK (unit lists change
// without DDL migrations — same reasoning as Country membership).
func (f *QuantityField) Units(units ...string) *QuantityField {
	f.s.QuantityUnits = append(f.s.QuantityUnits, units...)
	return f
}

func (f *QuantityField) Required() *QuantityField { f.s.Required = true; return f }
func (f *QuantityField) Spec() FieldSpec          { return f.s }

// DurationField stores an ISO 8601 duration string as TEXT in
// canonical form (`PT5M`, `P1DT2H`, `P1Y6M`). CHECK enforces the shape;
// the REST layer normalises (uppercases the P/T/unit chars) before
// insert. Consumers parse via their language's duration library
// (`temporal` in JS, `Duration` in Java/Kotlin/Swift, etc).
type DurationField struct{ s FieldSpec }

// NewDuration constructs an ISO 8601 duration field.
func NewDuration() *DurationField { return &DurationField{s: FieldSpec{Type: TypeDuration}} }

func (f *DurationField) Required() *DurationField { f.s.Required = true; return f }
func (f *DurationField) Index() *DurationField    { f.s.Indexed = true; return f }
func (f *DurationField) Default(v string) *DurationField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *DurationField) Spec() FieldSpec { return f.s }

// ---- v1.4.10 Domain types: Workflow ----

// StatusField is a state-machine value stored as TEXT. Unlike `select`,
// the field optionally carries declared transitions used by admin UI
// and hooks for graph rendering and rule enforcement.
//
// In v1.4.10 the server enforces MEMBERSHIP only (DB CHECK constraint
// + REST validation). Transition rules declared via `.Transitions()`
// are introspectable metadata — operators wire transition enforcement
// via hooks (`onRecordBeforeUpdate`) when they want server-side rejection.
// Future v1.5+ may add per-field triggers when production usage shows
// hook-based enforcement is friction.
//
// On CREATE, an omitted value defaults to the first declared state.
type StatusField struct{ s FieldSpec }

// NewStatus constructs a status field with the declared states. First
// state is the initial value when CREATE omits the column.
func NewStatus(states ...string) *StatusField {
	return &StatusField{s: FieldSpec{
		Type:         TypeStatus,
		StatusValues: states,
		Indexed:      true, // status is a typical filter target
	}}
}

// Transitions declares the allowed state transitions. Pass a map
// from-state → list of to-states. Empty (don't call) = any-to-any
// (no transition enforcement).
//
// Example:
//
//	schema.Status("draft", "review", "published").
//	  Transitions(map[string][]string{
//	    "draft":     {"review"},
//	    "review":    {"draft", "published"},
//	    "published": {},
//	  })
func (f *StatusField) Transitions(m map[string][]string) *StatusField {
	f.s.StatusTransitions = m
	return f
}

func (f *StatusField) Required() *StatusField { f.s.Required = true; return f }
func (f *StatusField) Index() *StatusField    { f.s.Indexed = true; return f }
func (f *StatusField) Default(v string) *StatusField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *StatusField) Spec() FieldSpec { return f.s }

// PriorityField stores an integer priority bounded by Min/Max (defaults
// 0..3 for Low/Medium/High/Critical). REST accepts integer or digit
// string; storage is SMALLINT for natural sorting.
type PriorityField struct{ s FieldSpec }

// NewPriority constructs a priority field with default range 0..3.
func NewPriority() *PriorityField {
	zero, three := 0, 3
	return &PriorityField{s: FieldSpec{
		Type:    TypePriority,
		IntMin:  &zero,
		IntMax:  &three,
		Indexed: true, // priority is a typical filter / sort target
	}}
}

// Range overrides the default 0..3 bounds.
func (f *PriorityField) Range(min, max int) *PriorityField {
	f.s.IntMin, f.s.IntMax = &min, &max
	return f
}

func (f *PriorityField) Required() *PriorityField { f.s.Required = true; return f }
func (f *PriorityField) Default(v int) *PriorityField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *PriorityField) Spec() FieldSpec { return f.s }

// RatingField stores a 1..5-star rating (or whatever range you set).
// Semantically distinct from Priority so the admin UI can render
// stars vs dropdown.
type RatingField struct{ s FieldSpec }

// NewRating constructs a rating field with default range 1..5.
func NewRating() *RatingField {
	one, five := 1, 5
	return &RatingField{s: FieldSpec{
		Type:    TypeRating,
		IntMin:  &one,
		IntMax:  &five,
		Indexed: true,
	}}
}

func (f *RatingField) Range(min, max int) *RatingField {
	f.s.IntMin, f.s.IntMax = &min, &max
	return f
}

func (f *RatingField) Required() *RatingField { f.s.Required = true; return f }
func (f *RatingField) Default(v int) *RatingField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *RatingField) Spec() FieldSpec { return f.s }

// ---- v1.4.11 Domain types: Hierarchies ----

// TagsField stores a free-form set of label strings as TEXT[] with a
// GIN index. REST normalises each tag (trim + lowercase ASCII) and
// deduplicates the resulting set so repeated POSTs of the same tags
// don't grow the array. Use `.MaxCount()` / `.TagMaxLen()` to bound
// the cardinality and per-tag length.
type TagsField struct{ s FieldSpec }

// NewTags constructs a tags field. Default per-tag max length is 50
// (long enough for any sensible label, short enough to keep the GIN
// index efficient).
func NewTags() *TagsField {
	return &TagsField{s: FieldSpec{Type: TypeTags, TagMaxLen: 50, Indexed: true}}
}

// MaxCount caps the size of the tag set per record. 0 = unlimited.
func (f *TagsField) MaxCount(n int) *TagsField { f.s.TagsMaxCount = n; return f }

// TagMaxLen overrides the default 50-char per-tag length cap.
func (f *TagsField) TagMaxLen(n int) *TagsField { f.s.TagMaxLen = n; return f }

func (f *TagsField) Required() *TagsField { f.s.Required = true; return f }
func (f *TagsField) Spec() FieldSpec      { return f.s }

// TreePathField stores a hierarchical path as Postgres LTREE. REST
// validates the canonical ltree shape on write: dot-separated labels,
// each label = [A-Za-z0-9_]+ up to 256 chars, total path up to 65535
// chars (Postgres ltree limits).
//
// Postgres exposes operators (`@>` ancestor, `<@` descendant, `||`
// concat, `nlevel()` depth) and GIST/GIN indexes — building category
// trees, comment threads, org charts, and forum sections is direct.
type TreePathField struct{ s FieldSpec }

// NewTreePath constructs an LTREE-backed hierarchy path field.
func NewTreePath() *TreePathField {
	// LTREE benefits from a GIST index for ancestor/descendant queries.
	// We mark Indexed=true; the SQL gen emits GIST instead of btree
	// when it sees TypeTreePath.
	return &TreePathField{s: FieldSpec{Type: TypeTreePath, Indexed: true}}
}

func (f *TreePathField) Required() *TreePathField { f.s.Required = true; return f }
func (f *TreePathField) Spec() FieldSpec          { return f.s }
