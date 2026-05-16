// Package builder is the in-memory schema DSL.
//
// User code calls these constructors via the public re-exports in
// pkg/railbase/schema/. Each builder is a chainable struct that
// captures intent; calling Spec() materialises a serialisable view
// the registry stores and the migration generator compares.
//
// Layering rule: this package depends only on the standard library.
// It is consumed by:
//   - internal/schema/registry — keeps the in-memory map of declared
//     collections.
//   - internal/schema/gen — emits SQL DDL and JSON snapshots from
//     CollectionSpec.
//   - pkg/railbase/schema — public re-exports for embedders.
//
// Layered design choice: each concrete field type (TextField, etc.)
// is its own struct with fluent modifiers returning *itself. This
// keeps `schema.Text().Required().MinLen(3)` chains type-correct.
// The price is per-type boilerplate for the shared modifiers
// (Required/Unique/Index/Default); we accept it as the readability
// cost of v0.2.
package builder

// FieldType discriminates FieldSpec. Wire format: lowercase ASCII,
// matches PB-compat values exactly so strict-mode `_collections`
// introspection produces the right shape.
type FieldType string

const (
	TypeText        FieldType = "text"
	TypeNumber      FieldType = "number"
	TypeBool        FieldType = "bool"
	TypeDate        FieldType = "date"
	TypeEmail       FieldType = "email"
	TypeURL         FieldType = "url"
	TypeJSON        FieldType = "json"
	TypeSelect      FieldType = "select"
	TypeMultiSelect FieldType = "multiselect"
	TypeFile        FieldType = "file"
	TypeFiles       FieldType = "files"
	TypeRelation    FieldType = "relation"
	TypeRelations   FieldType = "relations"
	TypePassword    FieldType = "password"
	TypeRichText    FieldType = "richtext"

	// --- v1.4.2 domain types (slice 1: communication) ---

	// TypeTel is a phone number stored as a single TEXT column in
	// canonical E.164 form (e.g. "+14155552671"). CHECK constraint
	// enforces the shape; richer display formatting is the client's job.
	TypeTel FieldType = "tel"

	// TypePersonName is a structured person name stored as JSONB with
	// keys: first, middle, last, suffix, full (all optional). Stored
	// structured rather than as a single TEXT so admin/exporters can
	// address components; REST accepts both shapes on write — string
	// → {full: "..."} sugar — but emits the object on read.
	TypePersonName FieldType = "person_name"

	// TypeAddress is a structured postal address stored as JSONB with
	// keys: street, street2, city, region, postal, country (all
	// optional individually, but at least one required). Country (when
	// present) validated against the ISO 3166-1 alpha-2 table — same
	// list used by the country field type. Postal code length-bounded
	// 1-20 (per-country format validation deferred — too many edge
	// cases for the value it adds; operators with strict needs add a
	// hook). Mirrors person_name's structured-JSONB pattern.
	TypeAddress FieldType = "address"

	// TypeTaxID is a per-country tax identifier stored as TEXT in
	// canonical compact form (no spaces / punctuation). The country
	// code is the FIRST 2 chars for EU VAT (DE123456789), or the
	// field's `.Country()` builder hint for non-EU identifiers.
	// REST validates per-country shape + check digits where the
	// algorithm is well-known (VAT mod-97 / EIN length / INN Luhn-
	// like). Operators with country combos beyond the built-in table
	// add a hook.
	TypeTaxID FieldType = "tax_id"

	// TypeBarcode is a product barcode stored as TEXT in canonical
	// digit-only form. Format is auto-detected by length: 13 digits
	// → EAN-13, 12 → UPC-A, 8 → EAN-8. Code-128 (alphanumeric, no
	// check digit) is accepted via `.Format("code128")` builder
	// override; default mode enforces a recognised digit-only format
	// with mod-10 check-digit verification.
	TypeBarcode FieldType = "barcode"

	// TypeCurrency is an ISO 4217 alpha-3 currency code stored as
	// TEXT in uppercase canonical form ("USD", "EUR", "RUB"). REST
	// accepts mixed case and validates against the embedded ISO 4217
	// list (~180 active codes; XAU/XAG/XPT precious metals + special
	// units included). Sister to TypeCountry; pairs naturally with
	// TypeFinance for {amount, currency} accounting fields.
	TypeCurrency FieldType = "currency"

	// TypeMoneyRange is an upper/lower money pair stored as JSONB
	// `{"min": "10.00", "max": "100.00", "currency": "USD"}`. Bounds
	// are decimal strings (no float drift) with the same precision
	// rules as TypeFinance. Currency is required; min ≤ max enforced
	// at REST + DB. Useful for price-range search filters, budget
	// caps, salary bands.
	TypeMoneyRange FieldType = "money_range"

	// TypeDateRange is a date interval stored as Postgres native
	// `daterange` type. Inclusive of start, exclusive of end (the
	// `[)` convention that's stdlib-standard). REST accepts string
	// form "[2024-01-01,2024-12-31)" or object form
	// `{"start": "2024-01-01", "end": "2024-12-31"}`; both produce
	// the same canonical Postgres daterange. start ≤ end enforced.
	TypeDateRange FieldType = "date_range"

	// TypeTimeRange is a time-of-day interval stored as JSONB
	// `{"start": "09:00", "end": "17:00"}`. Both ends are HH:MM or
	// HH:MM:SS in 24-hour. start ≤ end enforced. Used for business-
	// hours, shift schedules, time-window features. We store JSONB
	// instead of Postgres TIMERANGE (which doesn't exist as a
	// built-in type, only the broader tsrange) for predictable
	// JSON round-trip.
	TypeTimeRange FieldType = "time_range"

	// TypeBankAccount is a generic per-country bank account stored
	// as JSONB `{"country": "US", "routing": "...", "account": "..."}`.
	// Country uses ISO 3166-1; routing + account formats vary
	// (US ABA 9-digit routing + account; UK 6-digit sort code +
	// 8-digit account; CA institution + transit + account; etc.).
	// Use TypeIBAN for SEPA-style accounts where the IBAN itself is
	// the canonical identifier — this type is the fallback for
	// non-IBAN regions.
	TypeBankAccount FieldType = "bank_account"

	// TypeQRCode is a payload string stored as TEXT plus a `.Format()`
	// hint that the SDK / admin UI uses to choose the right rendering
	// library. Common formats: "url" (just a URL), "vcard" (contact
	// card), "wifi" (network credentials), "epc" (SEPA payment), "raw"
	// (any text). Server stores the payload verbatim — rendering is a
	// client concern.
	TypeQRCode FieldType = "qr_code"

	// --- v1.4.4 domain types (slice 2: identifiers) ---

	// TypeSlug is a URL-safe identifier stored as TEXT in canonical
	// lowercase-with-hyphens form. CHECK enforces `^[a-z0-9]+(-[a-z0-9]+)*$`.
	// On INSERT, an empty value auto-derives from the `SlugFrom` source
	// field (typically `title` or `name`) so clients can omit it; the
	// derivation strips non-ASCII, lowercases, and collapses non-alnum
	// runs into single hyphens. After creation the slug is stable —
	// UPDATE with empty value does NOT re-derive (URLs would break).
	TypeSlug FieldType = "slug"

	// TypeSequentialCode is a monotonically increasing identifier formed
	// from a Postgres sequence and rendered with an optional prefix +
	// zero-padding (e.g. "INV-00001"). On INSERT the column auto-fills
	// from `nextval()` of a collection-scoped sequence; clients cannot
	// override the value (UPDATE attempts are silently ignored — value
	// is server-owned). Uniqueness is enforced by the sequence's
	// monotonicity plus a column-level UNIQUE constraint as defense.
	TypeSequentialCode FieldType = "sequential_code"

	// --- v1.4.5 domain types (slice 3: content) ---

	// TypeColor is a hex color stored as TEXT in canonical lowercase
	// 6-digit form (e.g. "#ff5733"). REST accepts "#FFF" → "#ffffff",
	// "FF5733" → "#ff5733", "#FF5733" → "#ff5733"; CHECK constraint
	// enforces the canonical shape on the wire.
	TypeColor FieldType = "color"

	// TypeCron is a 5-field crontab expression stored as TEXT. The
	// REST layer validates the string via the same parser the Cron
	// scheduler uses (`internal/jobs.ParseCron`) so what reaches the
	// DB is guaranteed compilable. No CHECK constraint — cron grammar
	// is too rich for a regex; trust the app layer.
	TypeCron FieldType = "cron"

	// TypeMarkdown is plain Markdown source stored as TEXT. No special
	// validation — Markdown's grammar is intentionally forgiving. The
	// type tag exists so admin UI can render a preview pane and SDK
	// consumers can pick a syntax-highlighted editor.
	TypeMarkdown FieldType = "markdown"

	// --- v1.4.6 domain types (slice 4: money primitives) ---

	// TypeFinance is a fixed-point decimal stored as NUMERIC(precision,
	// scale). The default NUMERIC(15, 4) supports values up to 99
	// trillion with 4-decimal precision — sufficient for any monetary
	// context that isn't astrophysics-level. NEVER float (precision loss
	// at any non-trivial scale). On the wire we use strings, not
	// numbers, so JSON parsers can't silently lossily convert.
	TypeFinance FieldType = "finance"

	// TypePercentage is NUMERIC(5,2) (3 digits + 2 decimals, max 999.99)
	// with CHECK between 0 and 100 by default. The 0..100 convention
	// matches what users type into a "% off" field — fractional shape
	// (0..1) is available via `.Range(0, 1)` if your domain prefers it.
	TypePercentage FieldType = "percentage"

	// --- v1.4.7 domain types (slice 5: locale) ---

	// TypeCountry is an ISO 3166-1 alpha-2 country code stored as TEXT
	// in uppercase canonical form ("US", "RU", "DE"). REST accepts mixed
	// case and validates against the embedded ISO 3166-1 list (249 codes
	// as of 2024). CHECK constraint enforces the shape but not membership
	// — list membership is app-layer (so we can add new codes without
	// requiring a DB migration).
	TypeCountry FieldType = "country"

	// TypeTimezone is an IANA timezone identifier stored as TEXT in
	// canonical form ("Europe/Moscow", "America/New_York", "UTC"). REST
	// validates via Go's stdlib `time.LoadLocation` which uses the same
	// IANA tz database Postgres uses internally — `now() AT TIME ZONE
	// <col>` Just Works downstream.
	TypeTimezone FieldType = "timezone"

	// TypeLanguage is an ISO 639-1 alpha-2 language code stored as TEXT
	// in lowercase canonical form ("en", "ru", "fr"). REST accepts mixed
	// case and validates against the embedded ISO 639-1 list (184 codes).
	// Sister to TypeCountry — operators use this for user.language /
	// content.lang etc. CHECK constraint enforces shape only; membership
	// is REST-layer so we can add codes without DB migration.
	TypeLanguage FieldType = "language"

	// TypeLocale is a BCP-47 language tag stored as TEXT in canonical
	// form: lowercase language + optional uppercase region separated
	// by '-' ("en", "en-US", "pt-BR"). REST validates language against
	// ISO 639-1 + region against ISO 3166-1; the two halves reuse the
	// language and country embedded tables. Naturally connects to the
	// v1.5.5 i18n catalog — handlers can pass `record.locale` straight
	// into i18n.T(ctx, ...) for per-record translation.
	TypeLocale FieldType = "locale"

	// TypeCoordinates is a geographic point stored as JSONB
	// `{"lat": <num>, "lng": <num>}`. REST validates lat ∈ [-90, 90]
	// and lng ∈ [-180, 180]. Hand-rolled JSONB instead of PostGIS to
	// stay on the single-binary contract (PostGIS adds an extension
	// that's not in every PG distro). Operators wanting geospatial
	// joins / radius queries opt in to a PostGIS plugin later.
	TypeCoordinates FieldType = "coordinates"

	// --- v1.4.8 domain types (slice 6: banking) ---

	// TypeIBAN is an International Bank Account Number stored as TEXT
	// in canonical compact form (no spaces, uppercase). REST validates
	// the country code (ISO 3166-1 prefix), per-country length, and the
	// mod-97 check digits (ISO 7064). Display formatting (4-char groups
	// with spaces) is the SDK / app layer's job.
	TypeIBAN FieldType = "iban"

	// TypeBIC is a Business Identifier Code (SWIFT) stored as TEXT
	// in canonical 8- or 11-char uppercase form. Shape: 4 letters bank
	// code + 2 letters country + 2 alnum location + optional 3 alnum
	// branch. REST validates the shape; no central registry check
	// (membership lookup would require external SWIFT data).
	TypeBIC FieldType = "bic"

	// --- v1.4.9 domain types (slice 7: quantities) ---

	// TypeQuantity is a value-with-unit-of-measure stored as JSONB
	// `{value: "10.5", unit: "kg"}`. Value is a decimal string (no
	// float drift, same convention as Finance); unit is validated
	// against the per-field allow-list set via `.Units("kg", "lb")`.
	// No conversion machinery in v1 — operators do unit math in hooks
	// or pick a plugin (`railbase-uom`).
	TypeQuantity FieldType = "quantity"

	// TypeDuration is an ISO 8601 duration stored as TEXT in canonical
	// form (`PT5M`, `P1DT2H`, `P3M`). CHECK constraint enforces the
	// shape; REST normalises (uppercases P/T) and rejects malformed
	// values. Reading back gives the string unchanged — consumers parse
	// via their language's duration library.
	TypeDuration FieldType = "duration"

	// --- v1.4.10 domain types (slice 8: workflow) ---

	// TypeStatus is a state-machine value stored as TEXT. Differs from
	// `select` by adding declared transitions: REST enforces that an
	// UPDATE moves from `oldState` to `newState` only if the
	// transition is in the allow-list. CHECK constraint enforces
	// membership in the value set. Creation defaults to the first
	// declared state when no value supplied.
	TypeStatus FieldType = "status"

	// TypePriority is a SMALLINT bounded by Min/Max (defaults 0..3 for
	// "Low/Medium/High/Critical"). REST accepts integer or string
	// digits; storage is integer for natural sort.
	TypePriority FieldType = "priority"

	// TypeRating is a SMALLINT bounded by Min/Max (defaults 1..5 stars).
	// Same shape as Priority but with semantically different range
	// defaults — keeps schema introspection clean (admin UI can render
	// star widget for rating, dropdown for priority).
	TypeRating FieldType = "rating"

	// --- v1.4.11 domain types (slice 9: hierarchies) ---

	// TypeTags is a free-form set of label strings stored as TEXT[]
	// with a GIN index for `@>` / `&&` array containment / overlap
	// queries. Differs from multiselect by NOT constraining membership:
	// any tag string is acceptable. REST normalises each tag (trim +
	// lowercase) and deduplicates the resulting set.
	TypeTags FieldType = "tags"

	// TypeTreePath is an LTREE column storing dot-separated hierarchy
	// paths (`org.engineering.platform`, `cat.electronics.phones`).
	// Postgres' built-in ltree extension provides ancestor/descendant
	// operators (`<@`, `@>`), level functions, and GIST-indexable
	// queries. REST validates the canonical ltree shape on write.
	TypeTreePath FieldType = "tree_path"
)

// FieldSpec is the materialised, serialisable description of one
// column. Most fields carry a small subset of the union; the rest
// stay zero-valued. Order is stable so JSON marshalling produces a
// deterministic snapshot for diffing.
type FieldSpec struct {
	Name string    `json:"name"`
	Type FieldType `json:"type"`

	Required bool `json:"required,omitempty"`
	Unique   bool `json:"unique,omitempty"`
	Indexed  bool `json:"indexed,omitempty"`

	// Translatable, when true, makes this field store a per-locale
	// translation map instead of a flat scalar. Storage becomes JSONB
	// `{"en":"Hello","ru":"Привет",...}` with locale-keyed values; the
	// REST layer validates the shape on write (each value a string;
	// each key a BCP-47 tag of the form `xx` or `xx-XX`) and picks the
	// best locale on read using the request's resolved locale +
	// Catalog fallback chain. Admin UI surfaces a per-locale tab editor.
	//
	// Only text-shaped field types (Text / RichText / Markdown) honour
	// this flag in v1. Setting it on incompatible types is rejected at
	// Validate() time so a wrong-typed mistake fails fast.
	Translatable bool `json:"translatable,omitempty"`

	HasDefault bool `json:"has_default,omitempty"`
	Default    any  `json:"default,omitempty"`

	// Computed, when non-empty, makes this field a Postgres
	// generated-stored column whose value is `Computed` evaluated
	// against the row at INSERT / UPDATE time. Read-only: REST CRUD
	// strips it from create / update bodies (clients can't set it),
	// and the SQL generator emits `GENERATED ALWAYS AS (<expr>) STORED`
	// instead of a regular column.
	//
	// Use cases:
	//
	//   - Full-name fields: `Computed("first_name || ' ' || last_name")`
	//   - Aggregated counts: `Computed("jsonb_array_length(predecessors)")`
	//   - Slug derivation server-side: `Computed("lower(replace(title, ' ', '-'))")`
	//
	// Constraints (validated by Validate()):
	//
	//   - Required/Unique/Indexed are honoured (the generated column
	//     can have indexes and unique constraints normally).
	//   - HasDefault is mutually exclusive — generated columns can't
	//     have DEFAULTs (Postgres rejects this combo).
	//   - The expression is NOT parsed for safety — operators MUST
	//     audit it manually. A Bad Idea is `Computed("delete from
	//     other_table where id=...")`, but Postgres rejects DML in
	//     generated expressions, so the worst case is a CREATE TABLE
	//     that fails loudly at migrate-apply time.
	Computed string `json:"computed,omitempty"`

	// DefaultRequest, when non-empty, instructs the REST CRUD layer to
	// substitute a value derived from the request context whenever the
	// caller omits this field on INSERT. Closes the «owner copied on
	// the client» pattern Sentinel had to live with
	// (`tasks.go: owner: authState.value.me.id` at every create call).
	//
	// Supported expressions (more land in later patches as needs surface):
	//
	//   - "auth.id"          → authenticated principal's user/admin id
	//   - "auth.email"       → principal's email (if applicable)
	//   - "auth.collection"  → "_admins" | "<users>" | "_api_tokens"
	//   - "tenant.id"        → resolved tenant id (errors if no tenant)
	//
	// Override posture: the value is applied ONLY when the field is
	// missing from the request body — clients that explicitly pass a
	// value get that value (then the CreateRule decides whether to
	// allow it). Combined with `.CreateRule("@request.auth.id = owner")`
	// you get «server-injected default + RBAC guard» as a single line:
	//
	//   Field("owner", Relation("users").Required().
	//        DefaultRequest("auth.id")).
	//   CreateRule("@request.auth.id = owner")
	//
	// Empty string ⇒ no request-default behaviour (current default).
	// Must be one of the supported expressions or Validate() rejects.
	DefaultRequest string `json:"default_request,omitempty"`

	// --- text-family modifiers (Text, Email, URL, RichText) ---
	MinLen  *int   `json:"min_len,omitempty"`
	MaxLen  *int   `json:"max_len,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	FTS     bool   `json:"fts,omitempty"`

	// --- number modifiers ---
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	IsInt bool     `json:"is_int,omitempty"` // BIGINT instead of DOUBLE PRECISION

	// --- date modifiers ---
	AutoCreate bool `json:"auto_create,omitempty"` // DEFAULT now()
	AutoUpdate bool `json:"auto_update,omitempty"` // trigger sets on UPDATE

	// --- select modifiers ---
	SelectValues  []string `json:"select_values,omitempty"`
	MinSelections *int     `json:"min_selections,omitempty"` // multiselect only
	MaxSelections *int     `json:"max_selections,omitempty"` // multiselect only

	// --- file modifiers ---
	AcceptMIME []string `json:"accept_mime,omitempty"`
	MaxBytes   int64    `json:"max_bytes,omitempty"`

	// --- relation modifiers ---
	RelatedCollection string `json:"related_collection,omitempty"`
	CascadeDelete     bool   `json:"cascade_delete,omitempty"`
	SetNullOnDelete   bool   `json:"set_null_on_delete,omitempty"`

	// --- JSON-array validation modifiers (TypeJSON only) ---
	// JSONElementRefCollection, when non-empty, instructs the REST
	// CRUD layer to validate every UUID-shaped string element of a
	// JSONB array against `<collection>.id`. See JSONField
	// .ArrayOfUUIDReferences for the operator-facing builder.
	JSONElementRefCollection string `json:"json_element_ref_collection,omitempty"`
	// JSONElementPeerEqual names a column that must match (same value)
	// between this row and every JSONB-element-referenced row. Used
	// for Sentinel-style "predecessor must be in same project"
	// invariants. See JSONField.SameValueAs.
	JSONElementPeerEqual string `json:"json_element_peer_equal,omitempty"`

	// --- password modifiers ---
	PasswordMinLen *int `json:"password_min_len,omitempty"`

	// --- richtext modifiers ---
	RichTextNoSanitize bool `json:"richtext_no_sanitize,omitempty"` // default = sanitize

	// --- slug modifiers (TypeSlug) ---
	// SlugFrom is the source field column name. When set, an empty
	// slug value on INSERT auto-derives by lowercasing + transliterating
	// the source's value. Empty SlugFrom means "client must always
	// supply the slug explicitly".
	SlugFrom string `json:"slug_from,omitempty"`

	// --- sequential_code modifiers (TypeSequentialCode) ---
	// SeqPrefix is prepended literally to the formatted number, e.g.
	// "INV-" produces "INV-00001". Empty = no prefix.
	SeqPrefix string `json:"seq_prefix,omitempty"`
	// SeqPad is the minimum width of the numeric portion. Zero-padded.
	// E.g. SeqPad=5 → "00001", "00042". Zero or unset = no padding.
	SeqPad int `json:"seq_pad,omitempty"`
	// SeqStart is the first value the sequence emits. Defaults to 1
	// (Postgres default). Useful when migrating from external systems.
	SeqStart int64 `json:"seq_start,omitempty"`

	// --- finance / percentage modifiers ---
	// NumericPrecision is the total number of digits NUMERIC(p,s).
	// Zero means "use the type's default" (15 for finance, 5 for
	// percentage).
	NumericPrecision int `json:"numeric_precision,omitempty"`
	// NumericScale is the digits after the decimal point. Zero or
	// unset means the type's default (4 for finance, 2 for percentage).
	NumericScale int `json:"numeric_scale,omitempty"`
	// MinDecimal / MaxDecimal are CHECK bounds for finance/percentage.
	// Stored as the canonical decimal string so the SQL gen can emit
	// them verbatim without float-to-string drift.
	MinDecimal string `json:"min_decimal,omitempty"`
	MaxDecimal string `json:"max_decimal,omitempty"`

	// --- quantity modifiers ---
	// QuantityUnits is the allow-list of acceptable unit codes for a
	// quantity field. Empty (default) = accept any non-empty unit
	// string (operator declared "value-with-unit" pattern but doesn't
	// constrain the unit).
	QuantityUnits []string `json:"quantity_units,omitempty"`

	// --- status / priority / rating modifiers ---
	// StatusValues is the allow-list of state names. First entry is
	// the initial state when no value is supplied on CREATE.
	StatusValues []string `json:"status_values,omitempty"`
	// StatusTransitions maps from-state to allowed to-states. Empty
	// (default) = any state can transition to any other state. When
	// set, REST refuses transitions not in the map.
	StatusTransitions map[string][]string `json:"status_transitions,omitempty"`
	// IntMin / IntMax are CHECK bounds for priority / rating fields.
	// Both are inclusive. Zero means "unset".
	IntMin *int `json:"int_min,omitempty"`
	IntMax *int `json:"int_max,omitempty"`

	// --- tags modifiers ---
	// TagsMaxCount caps the cardinality of the tag set (default
	// unlimited). Useful to prevent a single record from accumulating
	// thousands of tags via an open API.
	TagsMaxCount int `json:"tags_max_count,omitempty"`
	// TagMaxLen caps each individual tag's char_length. Default 50 —
	// long enough for any reasonable label, short enough to keep the
	// GIN index efficient.
	TagMaxLen int `json:"tag_max_len,omitempty"`

	// --- tax_id modifier ---
	// TaxCountry is the operator-declared country for tax ID validation.
	// When empty, REST attempts to AUTO-DETECT from the first 2 chars
	// for EU VAT (DE / FR / IT / ...). For non-prefix IDs (US EIN, RU INN,
	// etc.) the operator MUST set TaxCountry on the field builder.
	TaxCountry string `json:"tax_country,omitempty"`

	// --- barcode modifier ---
	// BarcodeFormat is the explicit format hint. Empty = auto-detect
	// by length (8 → EAN-8, 12 → UPC-A, 13 → EAN-13). Non-empty values:
	// "ean13" / "ean8" / "upca" force the format; "code128" disables
	// the digit-only + check-digit rule and accepts alphanumeric.
	BarcodeFormat string `json:"barcode_format,omitempty"`

	// --- qr_code modifier ---
	// QRFormat is the operator-declared payload format hint. Stored
	// alongside the payload so admin UI / SDK can pick the right
	// rendering library. Valid values: "url", "vcard", "wifi", "epc",
	// "raw" (default "raw"). The server does NOT enforce per-format
	// payload structure — that's a rendering concern.
	QRFormat string `json:"qr_format,omitempty"`
}

// CollectionSpec is what the registry stores per declared collection.
type CollectionSpec struct {
	Name    string      `json:"name"`
	Tenant  bool        `json:"tenant,omitempty"`
	Auth    bool        `json:"auth,omitempty"` // schema.AuthCollection() — injects email/password_hash/verified/token_key
	// PublicProfile, when true on an auth collection, exposes a
	// read-only `/api/collections/{name}/profiles[/{id}]` endpoint
	// returning the non-secret user-declared fields (no email,
	// password_hash, token_key, verified, last_login_at). FEEDBACK
	// #B2 — closes the byline-without-auth gap for editorial/CMS
	// projects (blogger's `authors.{name, slug, title, bio, avatar_url}`
	// for article authorship rendering). No-op on non-auth collections.
	PublicProfile bool `json:"public_profile,omitempty"`
	// SoftDelete, when true, replaces DELETE-row with `UPDATE … SET
	// deleted = now()` and auto-filters LIST/VIEW by `deleted IS NULL`.
	// Callers can pass `?includeDeleted=true` to opt-in to seeing tombstones
	// (admin / trash UI). A nightly job purges rows whose `deleted`
	// is older than the retention window (configurable per-collection;
	// default 30 days).
	SoftDelete bool        `json:"soft_delete,omitempty"`

	// AdjacencyList, when true, adds a `parent UUID NULL` self-referential
	// FK column with `ON DELETE SET NULL`, plus a backing index. Cycle
	// prevention is REST-layer: on UPDATE the candidate parent chain is
	// walked, depth-bounded by MaxDepth (default 64, unbounded if 0).
	// Use for comments, file trees, org charts where each child has at
	// most one parent. See docs/03-data-layer.md "Hierarchical data".
	AdjacencyList bool `json:"adjacency_list,omitempty"`

	// Ordered, when true, adds a `sort_index INTEGER NOT NULL DEFAULT 0`
	// column for explicit child ordering (drag-drop). The REST layer
	// auto-assigns a trailing sort_index on INSERT (MAX(sort_index)+1
	// within the same parent — or globally if AdjacencyList is off).
	// Reorder via PATCH with explicit `sort_index`. Combine with
	// AdjacencyList for per-parent ordering, or use standalone for a
	// flat ordered list.
	Ordered bool `json:"ordered,omitempty"`

	// MaxDepth caps the AdjacencyList chain length. 0 = unbounded.
	// Validated at INSERT/UPDATE: walking parent chain past MaxDepth
	// returns 400. A reasonable default is 64 (deeper trees are almost
	// always bugs — runaway recursion or cycle that the cycle-check
	// would have caught with smaller bound).
	MaxDepth int `json:"max_depth,omitempty"`

	// Audit, when true, auto-writes a v3 timeline event for every
	// Create / Update / Delete on a record of this collection. The
	// event shape: actor=ctx principal, entity_type=<collection>,
	// entity_id=<record.id>, before/after=record diff,
	// event=<collection>.{created,updated,deleted}. Off by default —
	// audit-heavy collections (sessions, ephemerals) shouldn't pay
	// the per-write hash-chain cost.
	//
	// When Tenant=true the event lands in _audit_log_tenant (RLS-
	// scoped to the request's tenant); otherwise it lands in
	// _audit_log_site (admin / system actions). See
	// docs/19-unified-audit.md.
	Audit bool `json:"audit,omitempty"`

	Fields  []FieldSpec `json:"fields"`
	Indexes []IndexSpec `json:"indexes,omitempty"`
	Rules   RuleSet     `json:"rules,omitempty"`

	// Exports holds v1.6.3 schema-declarative export configs — one
	// entry per format the collection wants pre-configured (XLSX
	// and/or PDF). Empty → handlers use their auto-inferred defaults
	// (all readable columns, collection name as sheet/title, etc.).
	// Request-time query params (?columns, ?sort, ?filter, ?sheet,
	// ?title, ?header, ?footer) override anything in the config so a
	// one-off request always wins.
	Exports ExportSet `json:"exports,omitempty"`

	// EntityDocs holds per-entity PDF documents — invoices, statements,
	// summaries that combine one parent row with related child rows.
	// Each entry registers a route:
	//
	//   GET /api/collections/{name}/{id}/<EntityDocConfig.Name>.pdf
	//
	// The template receives `.Record` (the parent row), `.Related`
	// (map of related-collection slices), `.Now`, and `.Tenant`. The
	// regular ViewRule for the parent collection gates access — owner
	// checks via @request.auth.id = customer still apply.
	//
	// FEEDBACK #29 — `.Export()` was flat-table-only. Per-entity
	// documents (an invoice with line items + totals) had no path
	// other than 250 lines of hand-rolled gopdf in the embedder.
	EntityDocs []EntityDocConfig `json:"entity_docs,omitempty"`
}

// EntityDocConfig declares one per-entity PDF document on a
// collection. FEEDBACK #29.
type EntityDocConfig struct {
	// Name is the URL slug for the document (no extension). The
	// resulting route is /api/collections/{collection}/{id}/{Name}.pdf.
	// Must be [a-z0-9_-]+; the registry validates this.
	Name string `json:"name"`

	// Template is the Markdown template under pb_data/pdf_templates/
	// to render. Same template engine + helpers as .Export() PDFs.
	// The template's `.` is a struct with .Record, .Related,
	// .Now, .Tenant.
	Template string `json:"template"`

	// Title is the document title rendered on the first page when the
	// template doesn't include its own header. Empty → "{collection}
	// {id-short}" (e.g. "orders fec43944").
	Title string `json:"title,omitempty"`

	// Related declares the child queries to run alongside the parent
	// row. Each entry expands to:
	//
	//   SELECT * FROM <Collection>
	//   WHERE <ChildColumn> = <parent row>.<ParentColumn>
	//
	// In the template these land at `.Related["<key>"]` as
	// []map[string]any. Example wiring for order items:
	//
	//   Related: map[string]builder.RelatedSpec{
	//       "items": {Collection: "order_items", ChildColumn: "order_ref", ParentColumn: "id"},
	//   }
	//
	// Then in invoice.md: `{{ range .Related.items }}{{ .product }} — {{ .qty }}{{ end }}`.
	Related map[string]RelatedSpec `json:"related,omitempty"`
}

// RelatedSpec describes one child-table lookup for an EntityDoc.
type RelatedSpec struct {
	// Collection is the related collection name.
	Collection string `json:"collection"`

	// ChildColumn is the foreign-key column on the child collection.
	ChildColumn string `json:"child_column"`

	// ParentColumn is the column on the parent row supplying the FK
	// value. Empty → "id" (the common case).
	ParentColumn string `json:"parent_column,omitempty"`

	// OrderBy is an optional ORDER BY clause (just the column +
	// direction, e.g. "created ASC"). Empty → no explicit ordering.
	OrderBy string `json:"order_by,omitempty"`

	// Limit caps the related row count to keep PDF sizes sane.
	// 0 → 1000 (server-side default; embedders raise via a custom
	// handler if they need more).
	Limit int `json:"limit,omitempty"`
}

// ExportSet groups per-format export configs. Each format has at
// most one entry; pointers so callers can tell "not configured" from
// "configured with all-zero defaults".
type ExportSet struct {
	XLSX *XLSXExportConfig `json:"xlsx,omitempty"`
	PDF  *PDFExportConfig  `json:"pdf,omitempty"`
}

// XLSXExportConfig is the schema-declarative XLSX export shape from
// docs/08 §1. All fields are optional — empty XLSXExportConfig{}
// behaves exactly like no config (handler-inferred defaults).
//
// Field precedence at request time: query param > config > default.
type XLSXExportConfig struct {
	// Sheet is the worksheet name in the workbook. Empty → collection
	// name. Overridden by `?sheet=` on the request.
	Sheet string `json:"sheet,omitempty"`

	// Columns is an ordered allow-list of column keys (a subset of
	// the collection's readable columns). Empty → handler uses the
	// auto-inferred set (id + created + updated + system flags +
	// user fields in declaration order). Overridden by `?columns=`.
	Columns []string `json:"columns,omitempty"`

	// Headers maps a column key to its display label. Missing key
	// → header falls back to the column key itself. Lets schema
	// authors set "Author" instead of "author_id" without renaming
	// the column.
	Headers map[string]string `json:"headers,omitempty"`

	// Format maps a column key to an Excel number-format code as a
	// string ("yyyy-mm-dd", "#,##0.00", "$#,##0.00", "0.00%", etc.).
	//
	// **Current status (v1.6.3)**: the field is STORED on the spec
	// but the XLSX writer ignores it — cells render verbatim. Per-column
	// styling lands in a v1.6.x follow-up; until then, embedders who
	// need formatted money/dates in Excel should:
	//   - For dates: pre-format in the row data (`{"created":
	//     "2026-05-16"}` instead of an RFC3339 string).
	//   - For currency in cents: use a custom export handler atop
	//     `pkg/railbase/export.NewXLSXWriter` and write a string cell.
	//
	// FEEDBACK #37 — the shopper's `CurrencyFormat(...)` / `DateFormat(...)`
	// mini-DSL referenced in docs/08-generation.md doesn't exist. Use
	// plain Excel number-format-code strings here when the writer
	// honours them. The map shape is fixed (string→string).
	Format map[string]string `json:"format,omitempty"`
}

// PDFExportConfig is the schema-declarative PDF export shape. Same
// precedence rules as XLSXExportConfig.
type PDFExportConfig struct {
	// Title is the document title rendered on the first page.
	// Empty → collection name. Overridden by `?title=`. Ignored when
	// Template is set — the template's own frontmatter wins for chrome.
	Title string `json:"title,omitempty"`

	// Header is the repeating page header drawn at the top of every
	// page. Empty → no header. Overridden by `?header=`. Ignored
	// when Template is set.
	Header string `json:"header,omitempty"`

	// Footer is the document footer (currently last-page only —
	// multi-page footer support deferred). Empty → no footer.
	// Overridden by `?footer=`. Ignored when Template is set.
	Footer string `json:"footer,omitempty"`

	// Columns is the same allow-list semantics as XLSXExportConfig.
	// Ignored when Template is set — templates author their own layout.
	Columns []string `json:"columns,omitempty"`

	// Headers maps column key → display label. Same as XLSXExportConfig.
	// Ignored when Template is set.
	Headers map[string]string `json:"headers,omitempty"`

	// Format reserved; same caveat as XLSXExportConfig.
	Format map[string]string `json:"format,omitempty"`

	// Template, when non-empty, names a Markdown template (relative
	// path inside the operator-configured `pb_data/pdf_templates`
	// directory) that drives the PDF body. When unset, the handler
	// renders the data-table layout introduced in v1.6.1.
	//
	// The template is processed via `text/template` with these helpers:
	//   - `date "layout" v` — format time.Time using a Go layout string
	//   - `default fallback v` — fallback when v is zero
	//   - `truncate N s` — rune-aware truncate with ellipsis
	//   - `money v` — USD-defaulted shortcut for `currency v "USD"`
	//   - `currency v "USD"` — integer minor-units → "$1,234.56" with
	//     symbols for USD/EUR/GBP/RUB/JPY/CNY/INR (FEEDBACK #34)
	//   - `str v` — coerce any value to its string form, so
	//     `{{ slice (str .id) 0 8 }}` works without a printf dance
	//     (FEEDBACK #33)
	//   - `each v` — alias of stdlib `range`
	// plus all text/template built-ins (`if`, `range`, `with`, ...).
	//
	// Template context (the `.` dot):
	//   - .Records   — []map[string]any of filter-matched rows
	//   - .Tenant    — tenant ID string ("" when not tenant-scoped)
	//   - .Now       — time.Time at request time
	//   - .Filter    — raw filter expression ("" when none)
	//
	// Example: `pb_data/pdf_templates/posts-report.md`:
	//
	//   ---
	//   title: Posts Report — {{ .Now | date "2006-01-02" }}
	//   ---
	//   # Posts
	//   {{ range .Records }}
	//   - **{{ .title }}** ({{ .status }}) — {{ .created | date "Jan 2" }}
	//   {{ end }}
	Template string `json:"template,omitempty"`
}

// IndexSpec is one user-declared index. System indexes (the implicit
// per-tenant index, FK-backing index, etc.) are emitted by gen
// without appearing here.
type IndexSpec struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

// RuleSet captures per-CRUD-action filter rules. v0.2 stores only
// the raw filter expression; the parser/evaluator land in v0.3 with
// CRUD endpoints. Empty string = no rule (server-only).
type RuleSet struct {
	List   string `json:"list,omitempty"`
	View   string `json:"view,omitempty"`
	Create string `json:"create,omitempty"`
	Update string `json:"update,omitempty"`
	Delete string `json:"delete,omitempty"`
}

// Field is implemented by every field type. Spec() returns the
// captured options; CollectionBuilder.Field stamps in the column
// name and appends the result.
type Field interface {
	Spec() FieldSpec
}
