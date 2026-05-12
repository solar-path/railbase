package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/railbase/railbase/internal/schema/builder"
)

// addSharedSchemas registers the cross-collection types every path
// references: ListResponse wrapper, ErrorEnvelope, the auth-methods
// discovery payload, and the AuthResponse {token, record} envelope.
//
// These keys are stable so OpenAPI consumers can rely on the names
// across schema-snapshot diffs.
func addSharedSchemas(schemas map[string]*Schema) {
	schemas["ErrorEnvelope"] = &Schema{
		Type:        "object",
		Description: "Standard error envelope. Every 4xx/5xx response from Railbase uses this shape.",
		Required:    []string{"code", "message"},
		Properties: map[string]*Schema{
			"code": {
				Type: "string",
				Description: "Stable error code (e.g. `validation`, `not_found`, " +
					"`unauthorized`, `rate_limited`). Safe to switch on in clients.",
			},
			"message": {Type: "string", Description: "Human-readable explanation."},
			"details": {
				Type: "object",
				Description: "Optional structured detail. For parse errors, includes " +
					"`position` (1-indexed) so the client can underline the bad span.",
				AdditionalProperties: true,
			},
		},
	}

	schemas["ListResponse"] = &Schema{
		Type:        "object",
		Description: "PocketBase-shape paginated list response.",
		Required:    []string{"page", "perPage", "totalItems", "totalPages", "items"},
		Properties: map[string]*Schema{
			"page":       {Type: "integer", Minimum: f64(1)},
			"perPage":    {Type: "integer", Minimum: f64(1), Maximum: f64(500)},
			"totalItems": {Type: "integer", Minimum: f64(0)},
			"totalPages": {Type: "integer", Minimum: f64(0)},
			// Per-collection list responses override `items` with the
			// concrete row schema $ref. The shared definition keeps
			// `items` as a generic array so the type exists standalone.
			"items": {Type: "array", Items: &Schema{Type: "object"}},
		},
	}

	schemas["AuthResponse"] = &Schema{
		Type:        "object",
		Description: "Returned by signup / signin / refresh. Token is opaque (HMAC-signed) — treat it as a bearer credential.",
		Required:    []string{"token", "record"},
		Properties: map[string]*Schema{
			"token":  {Type: "string", Description: "Opaque session token. Pass as `Authorization: Bearer <token>` on subsequent requests."},
			"record": {Type: "object", AdditionalProperties: true, Description: "The authenticated user record (auth-collection row shape)."},
		},
	}

	// v1.7.0 — PB-compat discovery payload.
	schemas["AuthMethods"] = &Schema{
		Type: "object",
		Description: "Discovery payload returned by GET /api/collections/{name}/auth-methods. " +
			"Drives client-side login-screen rendering — front-ends call this BEFORE signin to know which paths are configured.",
		Required: []string{"password", "oauth2", "otp", "mfa", "webauthn"},
		Properties: map[string]*Schema{
			"password": {
				Type:     "object",
				Required: []string{"enabled", "identityFields"},
				Properties: map[string]*Schema{
					"enabled":        {Type: "boolean"},
					"identityFields": {Type: "array", Items: &Schema{Type: "string"}},
				},
			},
			"oauth2": {
				Type: "array",
				Items: &Schema{
					Type:     "object",
					Required: []string{"name", "displayName"},
					Properties: map[string]*Schema{
						"name":        {Type: "string", Description: "Provider key (lowercase). Pass to /auth-with-oauth2/{provider}."},
						"displayName": {Type: "string", Description: "Human-readable provider label for UI buttons."},
					},
				},
			},
			"otp": {
				Type:     "object",
				Required: []string{"enabled", "duration"},
				Properties: map[string]*Schema{
					"enabled":  {Type: "boolean"},
					"duration": {Type: "integer", Description: "OTP token lifetime in seconds."},
				},
			},
			"mfa": {
				Type:     "object",
				Required: []string{"enabled", "duration"},
				Properties: map[string]*Schema{
					"enabled":  {Type: "boolean"},
					"duration": {Type: "integer", Description: "MFA challenge lifetime in seconds."},
				},
			},
			"webauthn": {
				Type:     "object",
				Required: []string{"enabled"},
				Properties: map[string]*Schema{
					"enabled": {Type: "boolean"},
				},
			},
		},
	}

	// FileRef helper for TypeFiles fields. Mirrors the TS SDK's
	// FileRef interface exactly so consumers reading both docs see the
	// same shape.
	schemas["FileRef"] = &Schema{
		Type:        "object",
		Description: "Reference to an uploaded file (files-typed field).",
		Required:    []string{"path", "name", "mime", "size"},
		Properties: map[string]*Schema{
			"path": {Type: "string"},
			"name": {Type: "string"},
			"mime": {Type: "string"},
			"size": {Type: "integer", Minimum: f64(0)},
		},
	}

	// --- async export shapes ---
	//
	// AsyncExportRequest is the POST /api/exports body. Mirrors the
	// asyncExportRequest struct in internal/api/rest/async_export.go;
	// keep snake_case to match what the handler actually accepts.
	schemas["AsyncExportRequest"] = &Schema{
		Type:        "object",
		Description: "Body for POST /api/exports. The same query-param overrides accepted by the sync exporter live here as JSON fields.",
		Required:    []string{"format", "collection"},
		Properties: map[string]*Schema{
			"format":          {Type: "string", Enum: []string{"xlsx", "pdf"}},
			"collection":      {Type: "string", Description: "Collection name to export."},
			"filter":          {Type: "string", Description: "PB-style filter expression."},
			"sort":            {Type: "string", Description: "Comma-separated `±field`."},
			"columns":         {Type: "string", Description: "Comma-separated allow-list of column keys."},
			"sheet":           {Type: "string", Description: "Worksheet name (xlsx only)."},
			"title":           {Type: "string", Description: "Document title (pdf only)."},
			"header":          {Type: "string", Description: "Page header (pdf only)."},
			"footer":          {Type: "string", Description: "Page footer (pdf only)."},
			"include_deleted": {Type: "boolean", Description: "Surface tombstoned rows. Soft-delete collections only."},
		},
	}

	schemas["AsyncExportAccepted"] = &Schema{
		Type:        "object",
		Description: "Returned with HTTP 202 from POST /api/exports.",
		Required:    []string{"id", "status", "format", "status_url"},
		Properties: map[string]*Schema{
			"id":         {Type: "string", Format: "uuid", Description: "Export job id; also the poll URL suffix."},
			"status":     {Type: "string", Enum: []string{"pending"}, Description: "Always `pending` on enqueue; flip via the poll endpoint."},
			"format":     {Type: "string", Enum: []string{"xlsx", "pdf"}},
			"status_url": {Type: "string", Description: "Poll this URL for state updates."},
		},
	}

	schemas["AsyncExportStatus"] = &Schema{
		Type:        "object",
		Description: "Returned by GET /api/exports/{id}. `file_url` only appears once status is `completed`.",
		Required:    []string{"id", "format", "collection", "status", "created_at"},
		Properties: map[string]*Schema{
			"id":             {Type: "string", Format: "uuid"},
			"format":         {Type: "string", Enum: []string{"xlsx", "pdf"}},
			"collection":     {Type: "string"},
			"status":         {Type: "string", Enum: []string{"pending", "running", "completed", "failed"}},
			"row_count":      {Type: "integer", Minimum: f64(0), Description: "Set once the export finishes scanning."},
			"file_size":      {Type: "integer", Minimum: f64(0), Description: "Bytes on disk; present when status=completed."},
			"file_url":       {Type: "string", Description: "Signed download URL; present only when status=completed."},
			"url_expires_at": {Type: "string", Format: "date-time", Description: "When `file_url` stops verifying."},
			"error":          {Type: "string", Description: "Failure reason when status=failed."},
			"created_at":     {Type: "string", Format: "date-time"},
			"completed_at":   {Type: "string", Format: "date-time", Nullable: true},
			"expires_at":     {Type: "string", Format: "date-time", Nullable: true, Description: "When the rendered file may be swept by cleanup."},
		},
	}
}

// emitCollectionSchema adds a JSON Schema for the row shape of one
// collection. Naming convention matches the TS SDK: collection "posts"
// → schema "Posts"; "blog_post" → "BlogPost". This keeps the docs
// pairs (TS interface ↔ OpenAPI schema) discoverable.
func emitCollectionSchema(schemas map[string]*Schema, spec builder.CollectionSpec) {
	name := typeName(spec.Name)
	props := map[string]*Schema{
		"id":      {Type: "string", Format: "uuid", Description: "UUIDv7. Server-generated."},
		"created": {Type: "string", Format: "date-time", Description: "Server-set on insert."},
		"updated": {Type: "string", Format: "date-time", Description: "Server-set on every write."},
	}
	required := []string{"id", "created", "updated"}

	if spec.Tenant {
		props["tenant_id"] = &Schema{Type: "string", Format: "uuid",
			Description: "Server-forced from X-Tenant header. Cannot be set by client."}
		required = append(required, "tenant_id")
	}
	if spec.SoftDelete {
		props["deleted"] = &Schema{Type: "string", Format: "date-time", Nullable: true,
			Description: "Tombstone timestamp. NULL on live rows; non-null = soft-deleted."}
	}
	if spec.AdjacencyList {
		props["parent"] = &Schema{Type: "string", Format: "uuid", Nullable: true,
			Description: "Parent row id, NULL at root."}
	}
	if spec.Ordered {
		props["sort_index"] = &Schema{Type: "integer",
			Description: "Position within parent. Server auto-assigns on insert when omitted."}
		required = append(required, "sort_index")
	}
	if spec.Auth {
		props["email"] = &Schema{Type: "string", Format: "email"}
		props["verified"] = &Schema{Type: "boolean"}
		props["last_login_at"] = &Schema{Type: "string", Format: "date-time", Nullable: true}
		required = append(required, "email", "verified")
	}

	for _, f := range spec.Fields {
		// password is write-only — never appears on read shape.
		if f.Type == builder.TypePassword {
			continue
		}
		// AuthCollection's system fields are already emitted above.
		if spec.Auth && isAuthSystemField(f.Name) {
			continue
		}
		props[f.Name] = fieldToSchema(f)
		if f.Required {
			required = append(required, f.Name)
		}
	}

	schemas[name] = &Schema{
		Type:        "object",
		Description: "Row shape for collection `" + spec.Name + "`.",
		Properties:  props,
		Required:    required,
	}

	// Per-collection list response: same shape as ListResponse but
	// items typed to the row schema. Named "<Name>List" — clients can
	// $ref this directly without composing themselves.
	schemas[name+"List"] = &Schema{
		Type:        "object",
		Description: "Paginated list of `" + spec.Name + "` rows.",
		Required:    []string{"page", "perPage", "totalItems", "totalPages", "items"},
		Properties: map[string]*Schema{
			"page":       {Type: "integer", Minimum: f64(1)},
			"perPage":    {Type: "integer", Minimum: f64(1)},
			"totalItems": {Type: "integer", Minimum: f64(0)},
			"totalPages": {Type: "integer", Minimum: f64(0)},
			"items":      {Type: "array", Items: &Schema{Ref: "#/components/schemas/" + name}},
		},
	}

	// Create-input variant: omits server-managed fields (id/created/
	// updated/tenant_id/deleted) and includes password for auth-collections.
	createProps := map[string]*Schema{}
	var createRequired []string
	for _, f := range spec.Fields {
		if spec.Auth && isAuthSystemField(f.Name) {
			continue
		}
		createProps[f.Name] = fieldToSchema(f)
		if f.Required {
			createRequired = append(createRequired, f.Name)
		}
	}
	if len(createProps) > 0 {
		sort.Strings(createRequired)
		schemas[name+"CreateInput"] = &Schema{
			Type:        "object",
			Description: "Create body for `" + spec.Name + "`. Server fields (id/created/updated) are auto-set.",
			Properties:  createProps,
			Required:    createRequired,
		}
		// Update-input: same props but everything optional (PATCH).
		updateProps := map[string]*Schema{}
		for k, v := range createProps {
			updateProps[k] = v
		}
		schemas[name+"UpdateInput"] = &Schema{
			Type:        "object",
			Description: "Update body for `" + spec.Name + "` (PATCH — all fields optional).",
			Properties:  updateProps,
		}

		// Multipart companions: only emitted when the collection has any
		// File / Files-typed field. The shape mirrors the JSON variant
		// except File fields become `format: binary` (single part) and
		// Files fields become arrays of binaries (browsers send the same
		// field name multiple times). Non-file fields stay typed as in
		// the JSON variant — multipart/form-data accepts text parts that
		// the server parses to the target shape.
		if hasFileField(spec) {
			schemas[name+"CreateInputMultipart"] = buildMultipartSchema(spec, createProps, createRequired,
				"Multipart create body for `"+spec.Name+"`. Use when uploading File / Files fields; otherwise the JSON variant is simpler.")
			schemas[name+"UpdateInputMultipart"] = buildMultipartSchema(spec, updateProps, nil,
				"Multipart update body for `"+spec.Name+"` (PATCH — all fields optional).")
		}
	}
}

// buildMultipartSchema clones the property map from the JSON input
// variant and rewrites File / Files fields to their multipart-friendly
// shape (binary string or array of binary strings). Other fields stay
// as text parts the server parses to the declared type — same shape as
// the JSON variant gives clients a clean type to encode against.
func buildMultipartSchema(spec builder.CollectionSpec, base map[string]*Schema, required []string, desc string) *Schema {
	props := make(map[string]*Schema, len(base))
	for k, v := range base {
		props[k] = v
	}
	for _, f := range spec.Fields {
		switch f.Type {
		case builder.TypeFile:
			props[f.Name] = &Schema{
				Type:        "string",
				Format:      "binary",
				Description: "Uploaded file (single part).",
			}
		case builder.TypeFiles:
			props[f.Name] = &Schema{
				Type:        "array",
				Items:       &Schema{Type: "string", Format: "binary"},
				Description: "Uploaded files (repeat the field name per file).",
			}
		}
	}
	return &Schema{
		Type:        "object",
		Description: desc,
		Properties:  props,
		Required:    required,
	}
}

// fieldToSchema is the OpenAPI counterpart of sdkgen/ts.tsType.
// Maps every FieldType to its JSON Schema shape. Unknown types fall
// back to a permissive `object` with additionalProperties:true so
// the spec stays valid even if a future type lands without being
// added here — better than silently dropping the field.
func fieldToSchema(f builder.FieldSpec) *Schema {
	s := &Schema{}
	switch f.Type {
	case builder.TypeText, builder.TypeRichText:
		s.Type = "string"
	case builder.TypeURL:
		s.Type = "string"
		s.Format = "uri"
	case builder.TypeEmail:
		s.Type = "string"
		s.Format = "email"
	case builder.TypeNumber:
		if f.IsInt {
			s.Type = "integer"
		} else {
			s.Type = "number"
		}
	case builder.TypeBool:
		s.Type = "boolean"
	case builder.TypeDate:
		s.Type = "string"
		s.Format = "date-time"
	case builder.TypeJSON:
		s.Type = "object"
		s.AdditionalProperties = true
	case builder.TypeSelect:
		s.Type = "string"
		s.Enum = append([]string(nil), f.SelectValues...)
	case builder.TypeMultiSelect:
		s.Type = "array"
		if len(f.SelectValues) > 0 {
			s.Items = &Schema{Type: "string", Enum: append([]string(nil), f.SelectValues...)}
		} else {
			s.Items = &Schema{Type: "string"}
		}
	case builder.TypeFile:
		s.Type = "string"
		s.Description = "File path / signed URL. Upload via multipart POST."
	case builder.TypeFiles:
		s.Type = "array"
		s.Items = &Schema{Ref: "#/components/schemas/FileRef"}
	case builder.TypeRelation:
		s.Type = "string"
		s.Format = "uuid"
		if f.RelatedCollection != "" {
			s.Description = "→ " + f.RelatedCollection + ".id"
		}
	case builder.TypeRelations:
		s.Type = "array"
		s.Items = &Schema{Type: "string", Format: "uuid"}
		if f.RelatedCollection != "" {
			s.Description = "→ " + f.RelatedCollection + ".id (junction)"
		}
	case builder.TypePassword:
		s.Type = "string"
		s.Description = "Write-only. Server stores Argon2id hash."
	case builder.TypeTel:
		s.Type = "string"
		s.Description = "E.164 canonical phone number."
	case builder.TypePersonName:
		s.Type = "object"
		s.Properties = map[string]*Schema{
			"first":  {Type: "string"},
			"middle": {Type: "string"},
			"last":   {Type: "string"},
			"suffix": {Type: "string"},
			"full":   {Type: "string"},
		}
	case builder.TypeSlug:
		s.Type = "string"
		s.Pattern = `^[a-z0-9]+(?:-[a-z0-9]+)*$`
	case builder.TypeSequentialCode:
		s.Type = "string"
		s.Description = "Server-owned auto-increment code. Read-only."
	case builder.TypeColor:
		s.Type = "string"
		s.Pattern = `^#[0-9a-f]{6}$`
	case builder.TypeCron:
		s.Type = "string"
		s.Description = "5-field crontab expression."
	case builder.TypeMarkdown:
		s.Type = "string"
	case builder.TypeFinance, builder.TypePercentage:
		// Decimal as STRING on the wire to preserve precision.
		s.Type = "string"
		s.Pattern = `^-?\d+(\.\d+)?$`
		s.Description = "Decimal string. Float-arithmetic precision loss not possible on the wire."
	case builder.TypeCountry:
		s.Type = "string"
		s.Pattern = `^[A-Z]{2}$`
		s.Description = "ISO 3166-1 alpha-2."
	case builder.TypeTimezone:
		s.Type = "string"
		s.Description = "IANA timezone identifier (e.g. `Europe/Berlin`)."
	case builder.TypeLanguage:
		s.Type = "string"
		s.Pattern = `^[a-z]{2}$`
		s.Description = "ISO 639-1 alpha-2."
	case builder.TypeLocale:
		s.Type = "string"
		s.Pattern = `^[a-z]{2}(?:-[A-Z]{2})?$`
		s.Description = "BCP-47 `lang[-REGION]`."
	case builder.TypeCoordinates:
		s.Type = "object"
		s.Required = []string{"lat", "lng"}
		s.Properties = map[string]*Schema{
			"lat": {Type: "number", Minimum: f64(-90), Maximum: f64(90)},
			"lng": {Type: "number", Minimum: f64(-180), Maximum: f64(180)},
		}
	case builder.TypeAddress:
		s.Type = "object"
		s.Properties = map[string]*Schema{
			"street":  {Type: "string"},
			"street2": {Type: "string"},
			"city":    {Type: "string"},
			"region":  {Type: "string"},
			"postal":  {Type: "string"},
			"country": {Type: "string", Pattern: `^[A-Z]{2}$`},
		}
	case builder.TypeIBAN:
		s.Type = "string"
		s.Description = "ISO 13616 IBAN, compact (no spaces)."
	case builder.TypeBIC:
		s.Type = "string"
		s.Pattern = `^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$`
		s.Description = "SWIFT/BIC code."
	case builder.TypeTaxID:
		s.Type = "string"
		s.Pattern = `^[A-Z0-9]{4,30}$`
	case builder.TypeBarcode:
		s.Type = "string"
	case builder.TypeCurrency:
		s.Type = "string"
		s.Pattern = `^[A-Z]{3}$`
		s.Description = "ISO 4217 alpha-3."
	case builder.TypeMoneyRange:
		s.Type = "object"
		s.Required = []string{"min", "max", "currency"}
		s.Properties = map[string]*Schema{
			"min":      {Type: "string", Description: "Decimal string."},
			"max":      {Type: "string", Description: "Decimal string."},
			"currency": {Type: "string", Pattern: `^[A-Z]{3}$`},
		}
	case builder.TypeDateRange:
		s.Type = "string"
		s.Description = "Postgres daterange canonical `[start,end)` form."
	case builder.TypeTimeRange:
		s.Type = "object"
		s.Required = []string{"start", "end"}
		s.Properties = map[string]*Schema{
			"start": {Type: "string"},
			"end":   {Type: "string"},
		}
	case builder.TypeBankAccount:
		s.Type = "object"
		s.Required = []string{"country"}
		s.Properties = map[string]*Schema{
			"country": {Type: "string", Pattern: `^[A-Z]{2}$`},
		}
		s.AdditionalProperties = map[string]any{"type": "string"}
		s.Description = "Per-country bank-account object. Country is required; other components depend on country code."
	case builder.TypeQRCode:
		s.Type = "string"
		s.Description = "QR payload (≤4096 chars)."
	case builder.TypeQuantity:
		s.Type = "object"
		s.Required = []string{"value", "unit"}
		s.Properties = map[string]*Schema{
			"value": {Type: "string", Description: "Decimal string."},
			"unit":  {Type: "string"},
		}
	case builder.TypeDuration:
		s.Type = "string"
		s.Description = "ISO 8601 duration (`P[nY][nM][nD][T[nH][nM][nS]]`)."
	case builder.TypeStatus:
		s.Type = "string"
		s.Enum = append([]string(nil), f.StatusValues...)
	case builder.TypePriority:
		s.Type = "integer"
		s.Minimum = f64(0)
		s.Maximum = f64(3)
	case builder.TypeRating:
		s.Type = "integer"
		s.Minimum = f64(1)
		s.Maximum = f64(5)
	case builder.TypeTags:
		s.Type = "array"
		s.Items = &Schema{Type: "string"}
	case builder.TypeTreePath:
		s.Type = "string"
		s.Description = "LTREE dot-path."
	default:
		// Unknown FieldType — emit a permissive shape so the spec
		// stays valid. Logged separately as a "new field type needs an
		// OpenAPI mapping" reminder.
		s.Type = "object"
		s.AdditionalProperties = true
	}

	// Text-family constraints surface as JSON Schema modifiers when
	// they're meaningful for the spec consumer.
	if f.MinLen != nil {
		s.MinLength = f.MinLen
	}
	if f.MaxLen != nil {
		s.MaxLength = f.MaxLen
	}
	if f.Min != nil && s.Minimum == nil {
		// Don't overwrite explicit min set above (e.g. coordinates).
		s.Minimum = f.Min
	}
	if f.Max != nil && s.Maximum == nil {
		s.Maximum = f.Max
	}
	if f.Pattern != "" && s.Pattern == "" {
		s.Pattern = f.Pattern
	}

	return s
}

// isAuthSystemField mirrors the same predicate in sdkgen/ts so the
// two outputs agree on what counts as an auth-injected system field.
// Skipped fields appear once (in the system properties block at the
// top) and never duplicated when walking the user-declared field list.
func isAuthSystemField(name string) bool {
	switch name {
	case "email", "password_hash", "verified", "token_key", "last_login_at":
		return true
	}
	return false
}

// typeName converts snake_case collection names to PascalCase, same
// rule as sdkgen/ts. Keeps the OpenAPI schema names visually
// matched with TS interfaces — operators reading both side by side
// see "Posts" / "BlogPost" / "User2fa" in both surfaces.
func typeName(s string) string {
	parts := []rune{}
	upNext := true
	for _, r := range s {
		if r == '_' {
			upNext = true
			continue
		}
		if upNext {
			if r >= 'a' && r <= 'z' {
				r = r - 32
			}
			upNext = false
		}
		parts = append(parts, r)
	}
	return string(parts)
}

// f64 returns a pointer to f. Used to give Schema.Minimum / Maximum
// a non-nil value distinct from "absent". Inlined helpers like this
// keep the schema-building call sites tight.
func f64(v float64) *float64 { return &v }

// hashHex hashes bytes with SHA-256 and returns lowercase hex. Same
// algorithm sdkgen uses so paired drift checks compare apples to
// apples.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
