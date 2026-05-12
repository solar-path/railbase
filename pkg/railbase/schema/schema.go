// Package schema is Railbase's user-facing schema DSL.
//
// EXPERIMENTAL until v1: signatures may change. Type aliases below
// re-export the internal builder so embedders never import internal
// packages directly.
//
// Typical usage:
//
//	package schema
//
//	import "github.com/railbase/railbase/pkg/railbase/schema"
//
//	var Posts = schema.Collection("posts").
//	    Field("title",  schema.Text().Required().MinLen(3).MaxLen(120)).
//	    Field("body",   schema.Text().FTS()).
//	    Field("author", schema.Relation("users").CascadeDelete()).
//	    Field("status", schema.Select("draft", "published").Default("draft")).
//	    Field("tags",   schema.MultiSelect("news", "blog", "release").Max(3)).
//	    Index("idx_posts_status", "status").
//	    ListRule("@request.auth.id != ''").
//	    CreateRule("@request.auth.id != ''")
//
// `id`, `created`, `updated` are auto-added by the migration generator
// — don't declare them yourself. Multi-tenant collections call
// `.Tenant()` to get a `tenant_id` column with RLS policies.
package schema

import (
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// --- Builder + spec types (aliased so users see "schema.X") ---

type (
	CollectionBuilder = builder.CollectionBuilder
	Field             = builder.Field
	FieldSpec         = builder.FieldSpec
	FieldType         = builder.FieldType
	CollectionSpec    = builder.CollectionSpec
	IndexSpec         = builder.IndexSpec
	RuleSet           = builder.RuleSet

	TextField        = builder.TextField
	NumberField      = builder.NumberField
	BoolField        = builder.BoolField
	DateField        = builder.DateField
	EmailField       = builder.EmailField
	URLField         = builder.URLField
	JSONField        = builder.JSONField
	SelectField      = builder.SelectField
	MultiSelectField = builder.MultiSelectField
	FileField        = builder.FileField
	FilesField       = builder.FilesField
	RelationField    = builder.RelationField
	RelationsField   = builder.RelationsField
	PasswordField    = builder.PasswordField
	RichTextField    = builder.RichTextField
	TelField             = builder.TelField
	PersonNameField      = builder.PersonNameField
	SlugField            = builder.SlugField
	SequentialCodeField  = builder.SequentialCodeField
	ColorField           = builder.ColorField
	CronField            = builder.CronField
	MarkdownField        = builder.MarkdownField
	FinanceField         = builder.FinanceField
	PercentageField      = builder.PercentageField
	CountryField         = builder.CountryField
	TimezoneField        = builder.TimezoneField
	LanguageField        = builder.LanguageField
	LocaleField          = builder.LocaleField
	CoordinatesField     = builder.CoordinatesField
	AddressField         = builder.AddressField
	TaxIDField           = builder.TaxIDField
	BarcodeField         = builder.BarcodeField
	CurrencyField        = builder.CurrencyField
	MoneyRangeField      = builder.MoneyRangeField
	DateRangeField       = builder.DateRangeField
	TimeRangeField       = builder.TimeRangeField
	BankAccountField     = builder.BankAccountField
	QRCodeField          = builder.QRCodeField
	IBANField            = builder.IBANField
	BICField             = builder.BICField
	QuantityField        = builder.QuantityField
	DurationField        = builder.DurationField
	StatusField          = builder.StatusField
	PriorityField        = builder.PriorityField
	RatingField          = builder.RatingField
	TagsField            = builder.TagsField
	TreePathField        = builder.TreePathField
)

// --- Type-name constants (re-exported for code that introspects). ---

const (
	TypeText        = builder.TypeText
	TypeNumber      = builder.TypeNumber
	TypeBool        = builder.TypeBool
	TypeDate        = builder.TypeDate
	TypeEmail       = builder.TypeEmail
	TypeURL         = builder.TypeURL
	TypeJSON        = builder.TypeJSON
	TypeSelect      = builder.TypeSelect
	TypeMultiSelect = builder.TypeMultiSelect
	TypeFile        = builder.TypeFile
	TypeFiles       = builder.TypeFiles
	TypeRelation    = builder.TypeRelation
	TypeRelations   = builder.TypeRelations
	TypePassword    = builder.TypePassword
	TypeRichText    = builder.TypeRichText
	TypeTel            = builder.TypeTel
	TypePersonName     = builder.TypePersonName
	TypeAddress        = builder.TypeAddress
	TypeTaxID          = builder.TypeTaxID
	TypeBarcode        = builder.TypeBarcode
	TypeCurrency       = builder.TypeCurrency
	TypeMoneyRange     = builder.TypeMoneyRange
	TypeDateRange      = builder.TypeDateRange
	TypeTimeRange      = builder.TypeTimeRange
	TypeBankAccount    = builder.TypeBankAccount
	TypeQRCode         = builder.TypeQRCode
	TypeSlug           = builder.TypeSlug
	TypeSequentialCode = builder.TypeSequentialCode
	TypeColor          = builder.TypeColor
	TypeCron           = builder.TypeCron
	TypeMarkdown       = builder.TypeMarkdown
	TypeFinance        = builder.TypeFinance
	TypePercentage     = builder.TypePercentage
	TypeCountry        = builder.TypeCountry
	TypeTimezone       = builder.TypeTimezone
	TypeLanguage       = builder.TypeLanguage
	TypeLocale         = builder.TypeLocale
	TypeCoordinates    = builder.TypeCoordinates
	TypeIBAN           = builder.TypeIBAN
	TypeBIC            = builder.TypeBIC
	TypeQuantity       = builder.TypeQuantity
	TypeDuration       = builder.TypeDuration
	TypeStatus         = builder.TypeStatus
	TypePriority       = builder.TypePriority
	TypeRating         = builder.TypeRating
	TypeTags           = builder.TypeTags
	TypeTreePath       = builder.TypeTreePath
)

// --- Constructors (the verbs users write) ---

// Collection starts a new collection declaration.
func Collection(name string) *CollectionBuilder { return builder.NewCollection(name) }

// AuthCollection starts a collection that will own authenticated
// identities. The migration generator auto-injects `email`,
// `password_hash`, `verified`, `token_key`, `last_login_at`; the
// generic auth endpoints (`/api/collections/{name}/auth-with-password`,
// `/auth-refresh`, `/auth-logout`) appear automatically. Email is
// case-insensitively unique within the collection.
//
// Multiple auth collections per project are supported — `users`,
// `admins`, `sellers` each get independent signin endpoints, separate
// session rows (via `_sessions.collection_name`), and isolated email
// namespaces.
//
// Combining .AuthCollection with .Tenant() is rejected at validation
// time in v0.3.2; per-tenant signup arrives with the tenant
// resolution middleware in v0.4.
func AuthCollection(name string) *CollectionBuilder { return builder.NewAuthCollection(name) }

// Text returns a TEXT field builder.
func Text() *TextField { return builder.NewText() }

// Number returns a numeric field builder. Defaults to DOUBLE PRECISION;
// call `.Int()` for BIGINT.
func Number() *NumberField { return builder.NewNumber() }

// Bool returns a BOOLEAN field builder.
func Bool() *BoolField { return builder.NewBool() }

// Date returns a TIMESTAMPTZ field builder.
func Date() *DateField { return builder.NewDate() }

// Email returns an email-validated TEXT field builder.
func Email() *EmailField { return builder.NewEmail() }

// URL returns a URL-validated TEXT field builder.
func URL() *URLField { return builder.NewURL() }

// JSON returns a JSONB field builder.
func JSON() *JSONField { return builder.NewJSON() }

// Select returns a single-value enumeration over the given options.
// Storage: TEXT + CHECK constraint.
func Select(values ...string) *SelectField { return builder.NewSelect(values...) }

// MultiSelect returns a multi-value enumeration. Storage: TEXT[] with GIN.
func MultiSelect(values ...string) *MultiSelectField { return builder.NewMultiSelect(values...) }

// File returns a single-file reference field.
func File() *FileField { return builder.NewFile() }

// Files returns a multi-file reference field (JSONB array).
func Files() *FilesField { return builder.NewFiles() }

// Relation returns a single foreign-key field. `related` is the
// target collection name; the FK points at its `id` column.
func Relation(related string) *RelationField { return builder.NewRelation(related) }

// Relations returns a many-to-many field backed by an implicit
// junction table.
func Relations(related string) *RelationsField { return builder.NewRelations(related) }

// Password returns an Argon2id-hashed password field. Plaintext is
// never persisted or returned by the API.
func Password() *PasswordField { return builder.NewPassword() }

// RichText returns a sanitised-HTML field. Sanitisation is on by
// default; call `.NoSanitize()` to opt out for trusted content.
func RichText() *RichTextField { return builder.NewRichText() }

// --- v1.4.2 domain types ---

// Tel returns a phone-number field stored as TEXT in canonical E.164
// form ("+14155552671"). REST accepts display-form input (parens,
// dashes, spaces) and normalises on write.
func Tel() *TelField { return builder.NewTel() }

// PersonName returns a structured person-name field stored as JSONB
// with keys: first, middle, last, suffix, full. REST also accepts a
// bare string (treated as `full`).
func PersonName() *PersonNameField { return builder.NewPersonName() }

// Address returns a structured postal address field stored as JSONB.
// Components: street, street2, city, region, postal, country — all
// optional individually but at least one required. Country (when
// present) validated against ISO 3166-1 alpha-2. Per-country postal
// format validation is operator-side (hook) — too many edge cases to
// maintain in core.
func Address() *AddressField { return builder.NewAddress() }

// TaxID returns a per-country tax identifier field stored as TEXT
// in canonical compact form (uppercase, no spaces/dashes). Set
// `.Country("US")` on the field builder for non-EU-VAT identifiers
// (US EIN, RU INN, CA BN, IN GSTIN, BR CNPJ, MX RFC). EU VAT IDs
// auto-detect the country from the first two letters of the value.
// Country-specific check-digit algorithms beyond the built-in table
// belong in operator hooks where business rules also live.
func TaxID() *TaxIDField { return builder.NewTaxID() }

// Barcode returns a product barcode field stored as TEXT in
// canonical form. Default auto-detects digit-only formats by length:
// 8 = EAN-8, 12 = UPC-A, 13 = EAN-13. All three are GS1 mod-10
// check-digit verified server-side. Use `.Format("code128")` for
// alphanumeric Code-128 (no check digit, length 1-80).
func Barcode() *BarcodeField { return builder.NewBarcode() }

// Currency returns an ISO 4217 alpha-3 currency code field stored as
// uppercase TEXT ("USD", "EUR", "RUB"). REST accepts mixed-case input
// and validates membership against the embedded ~180-code ISO 4217
// list. Pairs naturally with Finance() for {amount, currency}
// accounting fields.
func Currency() *CurrencyField { return builder.NewCurrency() }

// MoneyRange returns an upper/lower money pair field stored as JSONB
// `{"min": "10.00", "max": "100.00", "currency": "USD"}`. Bounds are
// decimal strings (no float drift) under the declared precision
// (default NUMERIC(15, 4), like Finance). min ≤ max enforced both
// REST + DB. Use `.Min("0")` / `.Max("1000000")` for operator-set
// outer clamps. Typical use: price-range filters, salary bands,
// budget caps.
func MoneyRange() *MoneyRangeField { return builder.NewMoneyRange() }

// DateRange returns a date interval field stored as Postgres
// `daterange` type — built-in with operators (`@>`, `&&`, etc.).
// Wire form is the canonical `[start,end)` half-open string;
// REST also accepts `{"start": "2024-01-01", "end": "2024-12-31"}`
// object form for SDK ergonomics. Used for booking windows,
// fiscal periods, validity intervals.
func DateRange() *DateRangeField { return builder.NewDateRange() }

// TimeRange returns a time-of-day interval field stored as JSONB
// `{"start": "09:00:00", "end": "17:00:00"}`. Both bounds are
// HH:MM[:SS] in 24-hour. start ≤ end enforced REST + DB. Used for
// business hours, shift schedules, time-window features.
func TimeRange() *TimeRangeField { return builder.NewTimeRange() }

// BankAccount returns a generic per-country bank account field stored
// as JSONB. Components vary by country: US uses {routing, account};
// UK uses {sort_code, account}; CA uses {institution, transit, account};
// AU uses {bsb, account}; IN uses {ifsc, account}. Operators using a
// country WITHOUT a built-in schema (everything outside US/GB/CA/AU/IN)
// can pass arbitrary string components verbatim. Use IBAN() for
// SEPA-style accounts where the IBAN itself is the canonical identifier.
func BankAccount() *BankAccountField { return builder.NewBankAccount() }

// QRCode returns a QR-code payload field stored as TEXT. Server stores
// the payload verbatim; the operator-declared `.Format()` hint helps
// admin UI / SDK pick the right renderer. Recognised formats:
// "raw" / "url" / "vcard" / "wifi" / "epc". Empty = "raw".
func QRCode() *QRCodeField { return builder.NewQRCode() }

// --- v1.4.4 domain types ---

// Slug returns a URL-safe identifier stored as TEXT in canonical
// lowercase-with-hyphens form. The CHECK constraint enforces the
// shape; REST normalises arbitrary client input on write. Common
// pattern: `schema.Slug().From("title").Unique()` to auto-derive
// from another field when the client omits the value.
func Slug() *SlugField { return builder.NewSlug() }

// SequentialCode returns a monotonically increasing identifier formed
// from a per-collection Postgres SEQUENCE plus optional prefix and
// zero-padding. Server-owned (clients cannot set the value).
//
// Example: `schema.SequentialCode().Prefix("INV-").Pad(5)` emits
// "INV-00001", "INV-00002", ... starting at 1.
func SequentialCode() *SequentialCodeField { return builder.NewSequentialCode() }

// --- v1.4.5 domain types ---

// Color returns a hex color field stored as TEXT in canonical
// "#rrggbb" lowercase form. REST normalises shorthand ("#abc"),
// missing-# input ("ff5733"), and uppercase before insert.
func Color() *ColorField { return builder.NewColor() }

// Cron returns a 5-field crontab-expression field. The REST layer
// validates the string against the same parser the Cron scheduler
// uses, so saving implies compilability.
func Cron() *CronField { return builder.NewCron() }

// Markdown returns a Markdown content field. No write-side validation
// beyond optional MinLen/MaxLen; the type tag lets admin UI render a
// preview pane.
func Markdown() *MarkdownField { return builder.NewMarkdown() }

// --- v1.4.6 domain types ---

// Finance returns a fixed-point decimal field stored as NUMERIC(15, 4)
// by default. Call `.Precision()` / `.Scale()` to override. Wire shape
// is STRING on both ends (REST + SDK) — JSON numbers lose precision in
// IEEE 754. Consumers do math via bignumber.js / decimal.js / etc.
//
// NEVER use Number().Float() for money: "0.1 + 0.2 ≠ 0.3" is fatal in
// accounting.
func Finance() *FinanceField { return builder.NewFinance() }

// Percentage returns a NUMERIC(5, 2) field with default CHECK between
// 0 and 100 (matching "% off" UI convention). Use `.Range(0, 1)` for
// the fractional-shape convention if your domain prefers it.
func Percentage() *PercentageField { return builder.NewPercentage() }

// --- v1.4.7 domain types ---

// Country returns an ISO 3166-1 alpha-2 country code field stored as
// uppercase TEXT ("US", "RU", "DE"). REST accepts mixed-case input
// and validates membership against the embedded ISO list.
func Country() *CountryField { return builder.NewCountry() }

// Timezone returns an IANA timezone identifier field stored as TEXT
// ("Europe/Moscow", "America/New_York", "UTC"). Validated server-side
// via stdlib `time.LoadLocation` — same tz database Postgres uses,
// so `now() AT TIME ZONE <col>` Just Works downstream.
func Timezone() *TimezoneField { return builder.NewTimezone() }

// Language returns an ISO 639-1 alpha-2 language code field stored
// as lowercase TEXT ("en", "ru", "fr"). REST accepts mixed-case input
// and validates membership against the embedded 184-code ISO list.
// Sister to Country() — typical use: `user.language`, `content.lang`.
func Language() *LanguageField { return builder.NewLanguage() }

// Locale returns a BCP-47 locale field stored as TEXT in canonical
// form: lowercase language + optional uppercase region separated by
// dash ("en", "en-US", "pt-BR"). Both halves validated against
// ISO 639-1 + ISO 3166-1. Naturally feeds into the v1.5.5 i18n
// catalog — handlers can pass `record.locale` directly to i18n.T.
func Locale() *LocaleField { return builder.NewLocale() }

// Coordinates returns a geographic point field stored as JSONB
// `{"lat": <num>, "lng": <num>}`. REST validates lat ∈ [-90, 90]
// and lng ∈ [-180, 180]; SDK gen emits `{ lat: number; lng: number }`.
// Hand-rolled JSONB instead of PostGIS keeps the single-binary
// contract — operators wanting spatial joins / radius queries opt in
// to a PostGIS plugin later.
func Coordinates() *CoordinatesField { return builder.NewCoordinates() }

// --- v1.4.8 domain types ---

// IBAN returns an International Bank Account Number field stored as
// uppercase compact TEXT (no spaces). REST normalises display forms
// (with spaces / hyphens / lowercase) and validates the ISO 13616
// country length plus mod-97 check digits (ISO 7064). Unique + Indexed
// by default.
func IBAN() *IBANField { return builder.NewIBAN() }

// BIC returns a SWIFT/BIC field stored as uppercase 8- or 11-char
// TEXT. REST validates the structural shape (4 letters bank + 2
// letters country + 2 alnum location + optional 3 alnum branch).
func BIC() *BICField { return builder.NewBIC() }

// --- v1.4.9 domain types ---

// Quantity returns a value-with-unit-of-measure field stored as JSONB
// `{value: "decimal-string", unit: "code"}`. Constrain accepted units
// via `.Units("kg", "lb", "g")`; without it any non-empty unit is
// accepted. REST accepts the structured object OR string sugar
// (`"10.5 kg"`).
func Quantity() *QuantityField { return builder.NewQuantity() }

// Duration returns an ISO 8601 duration field stored as TEXT in
// canonical uppercase form ("PT5M", "P1DT2H", "P1Y6M"). REST normalises
// case and validates the grammar; consumers parse via their language's
// duration library.
func Duration() *DurationField { return builder.NewDuration() }

// --- v1.4.10 domain types ---

// Status returns a state-machine field stored as TEXT. The first
// declared state is the initial value on CREATE; CHECK enforces
// membership. Transition rules via `.Transitions()` are advisory
// metadata for admin UI and hooks; server-side membership is the
// only enforced invariant in v1.
//
// Example:
//
//	schema.Status("draft", "review", "published").
//	  Transitions(map[string][]string{
//	    "draft":  {"review"},
//	    "review": {"draft", "published"},
//	  })
func Status(states ...string) *StatusField { return builder.NewStatus(states...) }

// Priority returns an integer priority field bounded by Min/Max
// (default 0..3 for Low/Medium/High/Critical). SMALLINT storage for
// natural sorting.
func Priority() *PriorityField { return builder.NewPriority() }

// Rating returns an integer rating field (default 1..5 stars).
// Semantically distinct from Priority so admin UI can render stars
// vs dropdown.
func Rating() *RatingField { return builder.NewRating() }

// --- v1.4.11 domain types ---

// Tags returns a free-form set of labels stored as TEXT[] with a GIN
// index. REST normalises each tag (trim + lowercase) and deduplicates.
// Cap per-tag length via `.TagMaxLen()` (default 50) and total set
// size via `.MaxCount()`.
func Tags() *TagsField { return builder.NewTags() }

// TreePath returns a hierarchical-path field stored as Postgres LTREE.
// REST validates the canonical shape (`[A-Za-z0-9_]+` labels separated
// by dots). Postgres' ltree extension gives ancestor/descendant
// operators (`@>`, `<@`) and GIST-indexable queries.
func TreePath() *TreePathField { return builder.NewTreePath() }

// --- Registry: declare a collection so the rest of Railbase sees it ---

// Register adds c to the global schema registry. Call from an init()
// function in your schema package; the migration generator and the
// CRUD router (v0.3+) read from the registry on startup.
//
// Panics on duplicate name or invalid collection — both are obvious
// developer errors that should fail loud at process startup, not at
// the first runtime request.
//
// Typical use:
//
//	package schema
//
//	import railschema "github.com/railbase/railbase/pkg/railbase/schema"
//
//	var Posts = railschema.Collection("posts").
//	    Field("title", railschema.Text().Required())
//
//	func init() {
//	    railschema.Register(Posts)
//	}
func Register(c *CollectionBuilder) { registry.Register(c) }

// All returns every registered collection in alphabetical order.
// Snapshot — caller can iterate freely without holding any lock.
func All() []*CollectionBuilder { return registry.All() }

// --- v1.6.3 schema-declarative export ---

type (
	// XLSXExportConfig configures the schema-declarative XLSX export.
	XLSXExportConfig = builder.XLSXExportConfig
	// PDFExportConfig configures the schema-declarative PDF export.
	PDFExportConfig = builder.PDFExportConfig
	// ExportConfigurer is the variadic-arg shape for .Export().
	ExportConfigurer = builder.ExportConfigurer
)

// ExportXLSX wraps an XLSXExportConfig as an ExportConfigurer
// suitable for `.Export(...)`. Example:
//
//	var Posts = schema.Collection("posts").
//	  Field("title", schema.Text().Required()).
//	  Export(
//	    schema.ExportXLSX(schema.XLSXExportConfig{
//	      Sheet:   "Posts",
//	      Columns: []string{"id", "title", "status", "created"},
//	      Headers: map[string]string{"created": "Created at"},
//	    }),
//	  )
func ExportXLSX(cfg XLSXExportConfig) ExportConfigurer { return builder.ExportXLSX(cfg) }

// ExportPDF wraps a PDFExportConfig as an ExportConfigurer suitable
// for `.Export(...)`. PDF template support (markdown file body)
// lands in v1.6.4 — v1.6.3 ships the data-table layout with config
// for title / header / footer / columns / headers.
func ExportPDF(cfg PDFExportConfig) ExportConfigurer { return builder.ExportPDF(cfg) }

