// Package openapi turns the in-memory schema registry into an
// OpenAPI 3.1 specification. Sibling of internal/sdkgen: same input
// (CollectionSpec list), different output target.
//
// v1.7.1 ships a tight MVP covering the contract surface the JS SDK
// + PB-compat tooling actually consume:
//
//   - For every registered collection: 5 CRUD paths
//     (list / view / create / update / delete) on /api/collections/{name}/records[/...].
//     Auth collections expose the 5 v0.3.2 + v1.7.0 auth endpoints
//     (signup / password signin / refresh / logout / auth-methods)
//     instead of create (which `/records` refuses with 403).
//   - Server-level endpoints: /api/auth/me, /healthz, /readyz.
//   - components/schemas: one JSON Schema per collection (row shape).
//     Plus shared types: ListResponse, ErrorEnvelope, AuthMethods,
//     AuthResponse.
//   - The same SchemaHash as sdkgen so drift detection works across
//     both targets — operators who run `generate sdk --check` and
//     `generate openapi --check` see the same hash.
//
// What's deliberately deferred:
//
//   - Realtime SSE: OpenAPI 3.1's event-stream surface is awkward;
//     deferred until a real consumer demands it.
//   - Hooks / jobs / cron / webhooks management: admin surfaces, not
//     core API contract — split into a separate "admin" OAS doc when
//     needed.
//   - File upload multipart bodies for TypeFiles fields: ships as
//     `string` placeholder + a documented `multipart/form-data`
//     content-type hint on the body. Full schema requires a separate
//     pass that walks each collection for file-typed fields.
//   - Document generation endpoints (/export.xlsx, /export.pdf): the
//     OAS would have to declare binary response schemas per
//     collection; deferred to a polish slice. Same for async exports.
//
// Output shape is the standard OpenAPI 3.1 JSON object. We do NOT
// use a third-party Go OpenAPI library — pure struct tags + json
// encode keeps the binary lean (no kin-openapi 800KB add) and the
// emitted JSON deterministic (map iteration is the only knob; we
// build sorted ordered keys via the Paths struct).
package openapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/railbase/railbase/internal/schema/builder"
)

// Spec is the root OpenAPI 3.1 document. Field ordering matches the
// canonical OAS section ordering for readable diffs.
type Spec struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Servers    []Server              `json:"servers,omitempty"`
	Paths      *Paths                `json:"paths"`
	Components *Components           `json:"components,omitempty"`
	Tags       []Tag                 `json:"tags,omitempty"`
	// X-railbase metadata for drift checks. Lives under
	// "x-railbase-*" extension keys so OAS tools that don't understand
	// extensions silently ignore them.
	XRailbase *XMeta `json:"x-railbase,omitempty"`
}

type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type Server struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// Paths is a hand-rolled ordered map. OpenAPI's JSON shape is
// `{"path1": {...}, "path2": {...}}` — we need stable iteration so
// the emitted bytes are deterministic across builds.
type Paths struct {
	order []string
	items map[string]*PathItem
}

func NewPaths() *Paths { return &Paths{items: map[string]*PathItem{}} }

func (p *Paths) Set(path string, item *PathItem) {
	if _, ok := p.items[path]; !ok {
		p.order = append(p.order, path)
	}
	p.items[path] = item
}

func (p *Paths) Get(path string) (*PathItem, bool) {
	it, ok := p.items[path]
	return it, ok
}

// Len reports how many paths are registered. Useful for assertions
// in tests + for the metadata block.
func (p *Paths) Len() int { return len(p.order) }

func (p *Paths) MarshalJSON() ([]byte, error) {
	// Emit in insertion order — callers register paths in a
	// reproducible sequence (collections alphabetical, then within
	// each collection a fixed verb ordering). That gives the JSON
	// output stable byte-for-byte structure.
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range p.order {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		val, err := json.Marshal(p.items[k])
		if err != nil {
			return nil, err
		}
		b.Write(val)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

type PathItem struct {
	Summary string     `json:"summary,omitempty"`
	Get     *Operation `json:"get,omitempty"`
	Post    *Operation `json:"post,omitempty"`
	Patch   *Operation `json:"patch,omitempty"`
	Delete  *Operation `json:"delete,omitempty"`
}

type Operation struct {
	OperationID string                `json:"operationId,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Summary     string                `json:"summary,omitempty"`
	Description string                `json:"description,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"requestBody,omitempty"`
	Responses   map[string]Response   `json:"responses,omitempty"`
}

type Parameter struct {
	Name        string      `json:"name"`
	In          string      `json:"in"` // "query" / "path" / "header"
	Description string      `json:"description,omitempty"`
	Required    bool        `json:"required,omitempty"`
	Schema      *Schema     `json:"schema,omitempty"`
	Example     interface{} `json:"example,omitempty"`
}

type RequestBody struct {
	Description string                 `json:"description,omitempty"`
	Required    bool                   `json:"required,omitempty"`
	Content     map[string]MediaType   `json:"content,omitempty"`
}

type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

type MediaType struct {
	Schema  *Schema     `json:"schema,omitempty"`
	Example interface{} `json:"example,omitempty"`
}

// Schema is the JSON Schema subset that OpenAPI 3.1 uses verbatim.
// Pointers used so omitempty works correctly for the optional fields
// (a zero-valued `int` is meaningful in JSON Schema, so we can't rely
// on omitempty for primitive type values; pointers are how we say
// "absent vs explicit 0").
type Schema struct {
	Ref         string             `json:"$ref,omitempty"`
	Type        string             `json:"type,omitempty"`
	Format      string             `json:"format,omitempty"`
	Description string             `json:"description,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	MinLength   *int               `json:"minLength,omitempty"`
	MaxLength   *int               `json:"maxLength,omitempty"`
	Minimum     *float64           `json:"minimum,omitempty"`
	Maximum     *float64           `json:"maximum,omitempty"`
	Pattern     string             `json:"pattern,omitempty"`
	Nullable    bool               `json:"nullable,omitempty"`
	Example     interface{}        `json:"example,omitempty"`
	// AdditionalProperties is true / false / a schema. We model the
	// "schema" case by leaving this nil and letting the marshaller
	// pick the right shape; absent means "default to true" per JSON
	// Schema convention.
	AdditionalProperties interface{} `json:"additionalProperties,omitempty"`
}

type Components struct {
	Schemas         map[string]*Schema `json:"schemas,omitempty"`
	SecuritySchemes map[string]any     `json:"securitySchemes,omitempty"`
}

type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// XMeta carries Railbase-specific metadata. Lives under "x-railbase"
// so generic OAS tooling treats it as an extension and ignores it.
// The SchemaHash field is the same value sdkgen writes to _meta.json;
// matching values mean SDK and OpenAPI doc were generated from the
// same registry snapshot.
type XMeta struct {
	SchemaHash      string    `json:"schemaHash"`
	GeneratedAt     time.Time `json:"generatedAt"`
	RailbaseVersion string    `json:"railbaseVersion"`
}

// Options tweaks Emit's behaviour. Zero value is fine — Title defaults
// to "Railbase API", ServerURL to "http://localhost:8090".
type Options struct {
	Title       string
	Description string
	// ServerURL is the public base URL. Default "http://localhost:8090"
	// matches the dev binding so the spec is immediately runnable in
	// Swagger UI / Postman without editing.
	ServerURL string
	// SchemaHash, if set, overrides the auto-computed hash. Useful when
	// the caller already computed it (e.g. for a paired sdkgen run).
	// Empty = auto-compute.
	SchemaHash string
}

// Emit builds the OpenAPI 3.1 document from the spec list. Pure
// function — no I/O. Returns the document; callers serialise with
// json.Marshal or json.MarshalIndent.
//
// Specs are sorted alphabetically by Name before path emission so the
// output is stable regardless of registration order.
func Emit(specs []builder.CollectionSpec, opts Options) (*Spec, error) {
	if opts.Title == "" {
		opts.Title = "Railbase API"
	}
	if opts.ServerURL == "" {
		opts.ServerURL = "http://localhost:8090"
	}

	sorted := append([]builder.CollectionSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	doc := &Spec{
		OpenAPI: "3.1.0",
		Info: Info{
			Title:       opts.Title,
			Version:     buildinfo.String(),
			Description: opts.Description,
		},
		Servers: []Server{{URL: opts.ServerURL, Description: "Configured server"}},
		Paths:   NewPaths(),
		Components: &Components{
			Schemas: map[string]*Schema{},
			SecuritySchemes: map[string]any{
				"bearerAuth": map[string]any{
					"type":         "http",
					"scheme":       "bearer",
					"bearerFormat": "Railbase session token (opaque) — issued by /auth-with-password etc.",
				},
				"cookieAuth": map[string]any{
					"type": "apiKey",
					"in":   "cookie",
					"name": "railbase_session",
				},
			},
		},
	}

	// Shared component schemas (referenced by every collection's
	// paths). Emit before collection schemas so $refs resolve in
	// docs-tools that lazy-load.
	addSharedSchemas(doc.Components.Schemas)

	// Tags: cross-cutting first (system / export / realtime), then one
	// per collection. Stable ordering — the collection tags follow the
	// alphabetical spec ordering so tag-driven docs UIs render the same
	// section order across runs.
	doc.Tags = []Tag{
		{Name: "system", Description: "Health probes and metadata"},
		{Name: "export", Description: "Synchronous + asynchronous data export"},
		{Name: "realtime", Description: "Server-sent events for collection changes"},
	}

	for _, spec := range sorted {
		emitCollectionSchema(doc.Components.Schemas, spec)
		emitCollectionPaths(doc.Paths, spec)
		tagDesc := "Records of collection " + spec.Name
		if spec.Auth {
			tagDesc = "Auth collection " + spec.Name + " (records + auth endpoints)"
		}
		doc.Tags = append(doc.Tags, Tag{Name: spec.Name, Description: tagDesc})
	}

	emitSystemPaths(doc.Paths)
	emitAsyncExportPaths(doc.Paths)
	emitRealtimePath(doc.Paths)

	// Schema hash + boot metadata go in x-railbase. Same value as
	// sdkgen.SchemaHash for matched-pair drift detection.
	if opts.SchemaHash == "" {
		body, err := json.Marshal(specs)
		if err != nil {
			return nil, fmt.Errorf("openapi: marshal specs: %w", err)
		}
		opts.SchemaHash = "sha256:" + hashHex(body)
	}
	doc.XRailbase = &XMeta{
		SchemaHash:      opts.SchemaHash,
		GeneratedAt:     time.Now().UTC(),
		RailbaseVersion: buildinfo.String(),
	}

	return doc, nil
}

// EmitJSON is a convenience: Emit() + MarshalIndent. Indented so the
// generated file is human-reviewable in version control.
func EmitJSON(specs []builder.CollectionSpec, opts Options) ([]byte, error) {
	doc, err := Emit(specs, opts)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}
