package builder

// This file groups field types that are not single-column primitives
// — selects (one column with CHECK or ENUM), files (one column +
// metadata table), relations (FK column or junction table).

// ---- Select (single value from a set) ----

// SelectField stores one value from a fixed set. Storage:
// `TEXT + CHECK (col IN (...))` by default. v0.3 may add `.AsEnum()`
// to upgrade to a native PG `ENUM TYPE` for tighter introspection.
type SelectField struct{ s FieldSpec }

func NewSelect(values ...string) *SelectField {
	return &SelectField{s: FieldSpec{Type: TypeSelect, SelectValues: values}}
}

func (f *SelectField) Required() *SelectField { f.s.Required = true; return f }
func (f *SelectField) Index() *SelectField    { f.s.Indexed = true; return f }
func (f *SelectField) Default(v string) *SelectField {
	f.s.HasDefault, f.s.Default = true, v
	return f
}
func (f *SelectField) Spec() FieldSpec { return f.s }

// ---- MultiSelect (subset of values) ----

// MultiSelectField stores zero-or-more values from a fixed set.
// Storage: native Postgres `TEXT[]`. We pick arrays over JSONB for
// MultiSelect because GIN-indexed array containment (`@>`) is the
// idiomatic way to filter "rows tagged with X".
type MultiSelectField struct{ s FieldSpec }

func NewMultiSelect(values ...string) *MultiSelectField {
	return &MultiSelectField{s: FieldSpec{Type: TypeMultiSelect, SelectValues: values}}
}

func (f *MultiSelectField) Required() *MultiSelectField { f.s.Required = true; return f }
func (f *MultiSelectField) Index() *MultiSelectField    { f.s.Indexed = true; return f }
func (f *MultiSelectField) Min(n int) *MultiSelectField { f.s.MinSelections = &n; return f }
func (f *MultiSelectField) Max(n int) *MultiSelectField { f.s.MaxSelections = &n; return f }
func (f *MultiSelectField) Spec() FieldSpec             { return f.s }

// ---- File (single) ----

// FileField stores a reference to one uploaded asset. Storage: TEXT
// holding the storage path / object key. File metadata (size, mime,
// thumbnails) lives in the side table that the file subsystem
// manages — out of scope for v0.2 schema gen.
type FileField struct{ s FieldSpec }

func NewFile() *FileField { return &FileField{s: FieldSpec{Type: TypeFile}} }

func (f *FileField) Required() *FileField                    { f.s.Required = true; return f }
func (f *FileField) AcceptMIME(types ...string) *FileField   { f.s.AcceptMIME = types; return f }
func (f *FileField) MaxBytes(n int64) *FileField             { f.s.MaxBytes = n; return f }
func (f *FileField) Spec() FieldSpec                         { return f.s }

// ---- Files (multiple) ----

// FilesField stores a JSONB array of file references. Multi-file is
// JSONB rather than TEXT[] because each entry carries metadata
// (filename, mime) we want addressable via JSON path.
type FilesField struct{ s FieldSpec }

func NewFiles() *FilesField { return &FilesField{s: FieldSpec{Type: TypeFiles}} }

func (f *FilesField) Required() *FilesField                  { f.s.Required = true; return f }
func (f *FilesField) AcceptMIME(types ...string) *FilesField { f.s.AcceptMIME = types; return f }
func (f *FilesField) MaxBytes(n int64) *FilesField           { f.s.MaxBytes = n; return f }

// MaxCount caps the size of the JSONB file array. FEEDBACK shopper #7 —
// previously only TagsField had a MaxCount builder; Files() inherited
// no upper bound on cardinality. The CRUD layer rejects writes that
// exceed the cap before persisting any of the files.
func (f *FilesField) MaxCount(n int) *FilesField { f.s.FilesMaxCount = n; return f }

func (f *FilesField) Spec() FieldSpec { return f.s }

// ---- Relation (single) ----

// RelationField stores a FK to another collection's id column.
// Storage: `UUID REFERENCES <related>(id)`. The default ON DELETE
// behaviour is RESTRICT — the FK blocks deleting a row that is still
// referenced. Switch with `.CascadeDelete()` or `.SetNullOnDelete()`.
type RelationField struct{ s FieldSpec }

func NewRelation(related string) *RelationField {
	return &RelationField{s: FieldSpec{Type: TypeRelation, RelatedCollection: related}}
}

func (f *RelationField) Required() *RelationField        { f.s.Required = true; return f }
func (f *RelationField) Index() *RelationField           { f.s.Indexed = true; return f }
func (f *RelationField) CascadeDelete() *RelationField {
	f.s.CascadeDelete, f.s.SetNullOnDelete = true, false
	return f
}
func (f *RelationField) SetNullOnDelete() *RelationField {
	f.s.SetNullOnDelete, f.s.CascadeDelete = true, false
	return f
}

// DefaultRequest sets a request-context-derived default value for
// this relation. The most common use is binding the FK to the
// authenticated principal:
//
//	Field("owner", Relation("users").Required().DefaultRequest("auth.id"))
//
// On create with no `owner` in the body, REST CRUD substitutes the
// auth.id of the caller. Override is allowed; combine with a CreateRule
// to forbid passing somebody else's id. See FieldSpec.DefaultRequest
// for the full list of supported expressions.
func (f *RelationField) DefaultRequest(expr string) *RelationField {
	f.s.DefaultRequest = expr
	return f
}

func (f *RelationField) Spec() FieldSpec { return f.s }

// ---- Relations (multi via junction table) ----

// RelationsField models a many-to-many relation. The schema gen
// emits a junction table `<owner>_<field>` with FKs to both sides.
// In v0.2 the junction is implicit (created during migration); we
// don't yet expose it to the user as its own collection.
type RelationsField struct{ s FieldSpec }

func NewRelations(related string) *RelationsField {
	return &RelationsField{s: FieldSpec{Type: TypeRelations, RelatedCollection: related}}
}

func (f *RelationsField) Required() *RelationsField     { f.s.Required = true; return f }
func (f *RelationsField) Index() *RelationsField        { f.s.Indexed = true; return f }
func (f *RelationsField) CascadeDelete() *RelationsField {
	f.s.CascadeDelete = true
	return f
}
func (f *RelationsField) Spec() FieldSpec { return f.s }
