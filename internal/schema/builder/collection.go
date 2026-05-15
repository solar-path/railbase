package builder

import "fmt"

// CollectionBuilder is the entry point for declaring a collection.
// Every method returns the receiver so the call chain stays fluent
// from `NewCollection("posts")` through to the closing `.DeleteRule(...)`.
//
// CollectionBuilder is NOT goroutine-safe. Construction happens
// during package init / startup; once registered, the spec is read-only.
type CollectionBuilder struct {
	spec CollectionSpec
}

// NewCollection starts a new collection. PB-compat: name must be a
// valid SQL identifier and MUST NOT begin with `_` (those are
// reserved for system tables).
//
// Validation is deferred to Build() so we don't panic during the
// fluent chain.
func NewCollection(name string) *CollectionBuilder {
	return &CollectionBuilder{spec: CollectionSpec{Name: name}}
}

// FromSpec reconstructs a CollectionBuilder around an already-
// materialised CollectionSpec — the inverse of Spec(). The fluent
// constructors (NewCollection + .Field + …) are the path for
// compile-time schema; FromSpec is the path for runtime schema, where
// a CollectionSpec arrives as JSON (admin UI body, or a persisted
// _admin_collections row) and needs to become a registry-registrable
// builder.
//
// The slices are deep-copied so the returned builder doesn't alias the
// caller's spec. Validation is NOT run here — call .Validate() before
// handing the builder to the registry, exactly as the fluent path does.
func FromSpec(spec CollectionSpec) *CollectionBuilder {
	cp := spec
	cp.Fields = append([]FieldSpec(nil), spec.Fields...)
	cp.Indexes = append([]IndexSpec(nil), spec.Indexes...)
	return &CollectionBuilder{spec: cp}
}

// NewAuthCollection starts an auth-style collection. Compared to
// NewCollection, the materialised CollectionSpec has Auth=true; the
// SQL generator then adds the auth-specific system columns
// (email, password_hash, verified, token_key, last_login_at) plus a
// unique index on email.
//
// Multiple auth collections per project are supported — each gets
// its own row namespace and its own /api/collections/{name}/auth-*
// endpoints. Same email may exist in `users` and `admins` as
// independent identities; the session row's collection_name
// disambiguates which one is signed in.
//
// Auth + .Tenant() is rejected at validate time in v0.3.2. Per-tenant
// signup arrives with the tenant middleware in v0.4.
func NewAuthCollection(name string) *CollectionBuilder {
	return &CollectionBuilder{spec: CollectionSpec{Name: name, Auth: true}}
}

// Field appends a column. The Field argument carries its captured
// modifiers; we copy those into FieldSpec and stamp in the column
// name from this call. Caller can pass the same field-builder to
// two collections without aliasing — Spec() returns by value.
func (b *CollectionBuilder) Field(name string, f Field) *CollectionBuilder {
	if f == nil {
		// Constructive panic — this is an obvious developer error
		// at build time, not a runtime input issue.
		panic(fmt.Sprintf("schema: nil Field passed to %q.Field(%q)", b.spec.Name, name))
	}
	s := f.Spec()
	s.Name = name
	b.spec.Fields = append(b.spec.Fields, s)
	return b
}

// Tenant marks this collection as multi-tenant. The schema generator
// adds a `tenant_id UUID NOT NULL` column with FK to `tenants(id)`,
// enables RLS, and emits the standard tenant-isolation policy. See
// docs/03-data-layer.md "Multi-tenancy: PostgreSQL Row-Level Security".
func (b *CollectionBuilder) Tenant() *CollectionBuilder {
	b.spec.Tenant = true
	return b
}

// SoftDelete turns physical DELETE into a "set deleted = now()" UPDATE
// and auto-filters LIST/VIEW by `deleted IS NULL`. Adds a `deleted
// TIMESTAMPTZ NULL` system column and a partial index on
// `(deleted) WHERE deleted IS NULL` to keep filtered scans fast.
//
// Restore via `POST /api/collections/{name}/records/{id}/restore`.
// Clients passing `?includeDeleted=true` on LIST / VIEW see tombstones
// (useful for trash UI). Cron job `cleanup_trash` purges deleted rows
// older than the retention window (default 30d, settable per-collection
// via `_settings` table key `trash.retention.<collection>`).
func (b *CollectionBuilder) SoftDelete() *CollectionBuilder {
	b.spec.SoftDelete = true
	return b
}

// Audit flips on automatic v3 timeline emission for every C/U/D on
// a record of this collection. See CollectionSpec.Audit for the full
// shape contract — event = "<collection>.{created,updated,deleted}",
// entity_type = <collection>, entity_id = <record.id>, actor pulled
// from ctx, before/after = the record diff.
func (b *CollectionBuilder) Audit() *CollectionBuilder {
	b.spec.Audit = true
	return b
}

// AdjacencyList adds a self-referential `parent UUID NULL` column
// with `ON DELETE SET NULL` plus an index for fast child lookups.
// Each row has at most one parent (single-parent tree). Cycle
// prevention is enforced REST-side on INSERT/UPDATE — the candidate
// parent chain is walked and rejected with 400 if it loops back, or
// if it exceeds MaxDepth (default 64; tunable via `.MaxDepth(N)`).
//
// Subtree queries (descendants/ancestors) use Postgres recursive
// CTEs — the `pkg/railbase/tree` helpers wrap the SQL.
//
// Use for: comments, file trees, org charts, geographic hierarchies
// where each node has exactly one logical parent. For multi-parent
// (BOM, dependencies), use the future DAG modifier instead.
func (b *CollectionBuilder) AdjacencyList() *CollectionBuilder {
	b.spec.AdjacencyList = true
	if b.spec.MaxDepth == 0 {
		b.spec.MaxDepth = 64
	}
	return b
}

// Ordered adds a `sort_index INTEGER NOT NULL DEFAULT 0` column for
// explicit child ordering (drag-drop reorder). The REST layer
// auto-assigns trailing sort_index on INSERT — globally if used
// standalone, or per-parent if combined with `.AdjacencyList()`.
//
// Reorder via PATCH with an explicit `sort_index` value. The server
// does NOT renumber siblings (gaps are fine and intentional — clients
// can pick midpoint values when inserting between two siblings).
// Operators wanting compact integers can run a manual renumber.
func (b *CollectionBuilder) Ordered() *CollectionBuilder {
	b.spec.Ordered = true
	return b
}

// MaxDepth caps the AdjacencyList chain length. 0 = unbounded.
// Default when AdjacencyList is set: 64. Override with `.MaxDepth(N)`.
// Trying to insert/update past MaxDepth returns 400 with
// `details.depth = <attempted>`.
func (b *CollectionBuilder) MaxDepth(n int) *CollectionBuilder {
	b.spec.MaxDepth = n
	return b
}

// ExportConfigurer is the variadic argument shape for .Export(). One
// implementation per format — see ExportXLSX / ExportPDF below.
//
// Implemented as a sealed interface (configure unexported) so the
// `.Export(...)` argument list can't be extended by external packages.
// Adding a new format = adding a new constructor in this package.
type ExportConfigurer interface {
	configure(s *ExportSet)
}

// Export attaches a schema-declarative export config to the
// collection. Subsequent calls append; passing the same format twice
// overwrites the previous entry (last wins).
//
//	var Posts = schema.Collection("posts").
//	  Field(...).
//	  Export(
//	    schema.ExportXLSX(schema.XLSXExportConfig{Sheet: "Posts", Columns: ...}),
//	    schema.ExportPDF(schema.PDFExportConfig{Title: "Posts Report"}),
//	  )
//
// `GET /api/collections/posts/export.{xlsx,pdf}` then defaults to
// these settings — request-time query params still win for one-off
// overrides.
func (b *CollectionBuilder) Export(configs ...ExportConfigurer) *CollectionBuilder {
	for _, c := range configs {
		if c == nil {
			continue
		}
		c.configure(&b.spec.Exports)
	}
	return b
}

// xlsxExportArg / pdfExportArg are sealed implementations of
// ExportConfigurer — produced by ExportXLSX / ExportPDF helpers.
type xlsxExportArg struct{ cfg XLSXExportConfig }

func (a xlsxExportArg) configure(s *ExportSet) {
	c := a.cfg
	s.XLSX = &c
}

type pdfExportArg struct{ cfg PDFExportConfig }

func (a pdfExportArg) configure(s *ExportSet) {
	c := a.cfg
	s.PDF = &c
}

// ExportXLSX wraps an XLSXExportConfig as an ExportConfigurer
// suitable for passing to .Export(). Sugar for the common case.
func ExportXLSX(cfg XLSXExportConfig) ExportConfigurer { return xlsxExportArg{cfg: cfg} }

// ExportPDF wraps a PDFExportConfig as an ExportConfigurer.
func ExportPDF(cfg PDFExportConfig) ExportConfigurer { return pdfExportArg{cfg: cfg} }

// Index declares a user index. Use for queries that the
// auto-generated indexes (PK, unique, FK-backing) don't already cover.
//
// Index name should be unique within the collection. By convention
// `idx_<collection>_<columns>` — but any non-empty identifier is fine.
func (b *CollectionBuilder) Index(name string, columns ...string) *CollectionBuilder {
	b.spec.Indexes = append(b.spec.Indexes, IndexSpec{Name: name, Columns: columns})
	return b
}

// UniqueIndex is sugar for Index(name, cols...).Unique() — i.e. a
// composite unique constraint. Single-column unique is normally
// expressed via the field's `.Unique()` modifier.
func (b *CollectionBuilder) UniqueIndex(name string, columns ...string) *CollectionBuilder {
	b.spec.Indexes = append(b.spec.Indexes, IndexSpec{Name: name, Columns: columns, Unique: true})
	return b
}

// ListRule sets the filter expression evaluated for list operations.
// v0.2 stores the string verbatim; v0.3 wires it into the CRUD path.
func (b *CollectionBuilder) ListRule(rule string) *CollectionBuilder {
	b.spec.Rules.List = rule
	return b
}

// ViewRule — single-record fetch.
func (b *CollectionBuilder) ViewRule(rule string) *CollectionBuilder {
	b.spec.Rules.View = rule
	return b
}

// CreateRule — INSERT.
func (b *CollectionBuilder) CreateRule(rule string) *CollectionBuilder {
	b.spec.Rules.Create = rule
	return b
}

// UpdateRule — PATCH/PUT.
func (b *CollectionBuilder) UpdateRule(rule string) *CollectionBuilder {
	b.spec.Rules.Update = rule
	return b
}

// DeleteRule — DELETE.
func (b *CollectionBuilder) DeleteRule(rule string) *CollectionBuilder {
	b.spec.Rules.Delete = rule
	return b
}

// PublicRules opens every CRUD operation to unauthenticated callers by
// setting all five rules to the always-true expression "true".
//
// Use this ONLY for collections that are genuinely public (and only on
// non-sensitive data). It exists because the rule engine is secure-by-
// default: an unset rule means "locked / server-only", so a truly
// public collection must say so explicitly. This is an explicit opt-in,
// not a bypass — the rules still run, they just always pass.
//
// For anything non-trivial, set the five rules individually instead
// (e.g. ListRule("@request.auth.id != ''")).
func (b *CollectionBuilder) PublicRules() *CollectionBuilder {
	b.spec.Rules = RuleSet{
		List:   "true",
		View:   "true",
		Create: "true",
		Update: "true",
		Delete: "true",
	}
	return b
}

// Spec returns a deep copy of the captured CollectionSpec. The
// registry calls this when it accepts the collection; mutating the
// returned value does not affect the builder.
func (b *CollectionBuilder) Spec() CollectionSpec {
	out := b.spec
	out.Fields = append([]FieldSpec(nil), b.spec.Fields...)
	out.Indexes = append([]IndexSpec(nil), b.spec.Indexes...)
	return out
}
