// Package pbimport translates a PocketBase v0.22+ schema into Railbase
// `schema.Collection(...)` Go code. The translation is intentionally
// conservative: where PB and Railbase diverge (rule magic vars, file
// thumb sizes, geo points, hidden columns), the output carries a
// `// TODO:` comment and the operator hand-tunes.
//
// The package has no Railbase runtime dep — pure stdlib + the schema
// package's import path strings. That keeps `railbase import` callable
// from a binary that doesn't intend to embed the imported schema, e.g.
// from CI tooling that diffs upstream PB against a local snapshot.
//
// # Scope decisions
//
// MVP (v1.7.8):
//
//   - 13 most common field types (text/number/bool/email/url/date/select/
//     multiselect/json/file/files/relation/relations/editor)
//   - Auth collection options (allowEmailAuth, requireEmail, minPasswordLength)
//   - Rules copied verbatim with TODO comment
//   - Indexes ignored (Railbase derives indexes from .Unique()/.Indexed())
//
// Deferred:
//
//   - Geo / point fields (PB v0.23+ — needs PostGIS plugin path)
//   - View collections (Railbase doesn't ship views in v1)
//   - Computed columns
//   - Per-collection options that map to Railbase domain types
//     (slug normaliser etc. — operator handles)

package pbimport

// PB v0.22+ /api/collections response shape. Only the fields we
// translate are typed — everything else is `any` so unknown future
// fields don't break the import.

// CollectionsList is the top-level paginated envelope.
type CollectionsList struct {
	Page       int          `json:"page"`
	PerPage    int          `json:"perPage"`
	TotalItems int          `json:"totalItems"`
	TotalPages int          `json:"totalPages"`
	Items      []Collection `json:"items"`
}

// Collection is one PB collection's metadata. PB serialises the
// `type` field as a discriminator: "base" / "auth" / "view".
type Collection struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	System     bool      `json:"system"`
	Schema     []Field   `json:"schema"`
	ListRule   *string   `json:"listRule"`
	ViewRule   *string   `json:"viewRule"`
	CreateRule *string   `json:"createRule"`
	UpdateRule *string   `json:"updateRule"`
	DeleteRule *string   `json:"deleteRule"`
	Options    AuthOpts  `json:"options"`
	Indexes    []string  `json:"indexes"`
}

// Field is one schema field. The Options blob is type-specific — we
// hold it as a free-form map and pull fields by name in the translator
// rather than typing every variant.
type Field struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Required    bool           `json:"required"`
	Presentable bool           `json:"presentable"`
	Unique      bool           `json:"unique"` // PB v0.22- only; later versions push to indexes
	Options     map[string]any `json:"options"`
}

// AuthOpts collects the auth-collection-specific knobs PB stores in
// the `options` field. All optional; zero values mean "PB default."
type AuthOpts struct {
	AllowEmailAuth    bool     `json:"allowEmailAuth"`
	AllowOAuth2Auth   bool     `json:"allowOAuth2Auth"`
	AllowUsernameAuth bool     `json:"allowUsernameAuth"`
	ExceptEmailDomains []string `json:"exceptEmailDomains"`
	OnlyEmailDomains   []string `json:"onlyEmailDomains"`
	MinPasswordLength int      `json:"minPasswordLength"`
	OnlyVerified      bool     `json:"onlyVerified"`
	RequireEmail      bool     `json:"requireEmail"`
	ManageRule        *string  `json:"manageRule"`
}
