package builder

import (
	"fmt"
	"regexp"
)

// Identifier rules:
//   - 1-63 chars (Postgres limit)
//   - lowercase ASCII letter, digit, or underscore
//   - must start with a letter or underscore
//
// We are strict here: PB-compat strict mode REST URLs and `_collections`
// introspection both surface the names verbatim. Mixed case or hyphens
// would create off-by-quote bugs in generated SQL.
var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// alwaysReserved are columns the migration generator owns regardless
// of CollectionSpec flags. tenant_id is included even on non-tenant
// collections — flipping a collection to tenant-scoped later would
// otherwise silently overshadow the user's column.
var alwaysReserved = map[string]struct{}{
	"id":        {},
	"created":   {},
	"updated":   {},
	"tenant_id": {},
}

// authReserved are auto-injected only when CollectionSpec.Auth is true.
// Outside auth collections these names are free for users to take —
// `email` on a contacts table, `verified` on a moderation queue etc.
var authReserved = map[string]struct{}{
	"email":         {},
	"password_hash": {},
	"verified":      {},
	"token_key":     {},
	"last_login_at": {},
}

// sqlReservedKeywords is a curated subset of the Postgres-reserved
// SQL keywords most likely to collide with reasonable user field
// names. The full Postgres list is ~700 entries; users who insist on
// quoted identifiers can fork. Catches the common foot-guns: `primary`
// (PRIMARY KEY syntax), `order`, `group`, `select`, etc.
//
// Source: https://www.postgresql.org/docs/current/sql-keywords-appendix.html
// — only "reserved" + "reserved (can be function or type name)" categories.
var sqlReservedKeywords = map[string]struct{}{
	"all": {}, "analyse": {}, "analyze": {}, "and": {}, "any": {},
	"array": {}, "as": {}, "asc": {}, "asymmetric": {}, "authorization": {},
	"between": {}, "binary": {}, "both": {},
	"case": {}, "cast": {}, "check": {}, "collate": {}, "column": {},
	"concurrently": {}, "constraint": {}, "create": {}, "cross": {},
	"current_catalog": {}, "current_date": {}, "current_role": {},
	"current_schema": {}, "current_time": {}, "current_timestamp": {},
	"current_user": {},
	"default": {}, "deferrable": {}, "desc": {}, "distinct": {}, "do": {},
	"else": {}, "end": {}, "except": {},
	"false": {}, "fetch": {}, "for": {}, "foreign": {}, "freeze": {},
	"from": {}, "full": {},
	"grant": {}, "group": {},
	"having": {},
	"ilike": {}, "in": {}, "initially": {}, "inner": {}, "intersect": {},
	"into": {}, "is": {}, "isnull": {},
	"join": {},
	"lateral": {}, "leading": {}, "left": {}, "like": {}, "limit": {},
	"localtime": {}, "localtimestamp": {},
	"natural": {}, "not": {}, "notnull": {}, "null": {},
	"offset": {}, "on": {}, "only": {}, "or": {}, "order": {}, "outer": {},
	"overlaps": {},
	"placing": {}, "primary": {},
	"references": {}, "returning": {}, "right": {},
	"select": {}, "session_user": {}, "similar": {}, "some": {},
	"symmetric": {}, "system_user": {},
	"table": {}, "tablesample": {}, "then": {}, "to": {}, "trailing": {},
	"true": {},
	"union": {}, "unique": {}, "user": {}, "using": {},
	"variadic": {}, "verbose": {},
	"when": {}, "where": {}, "window": {}, "with": {},
}

// Validate checks invariants that must hold for the migration
// generator to produce sane SQL. Returns the first error found —
// callers fix one thing at a time and re-run.
func (b *CollectionBuilder) Validate() error {
	s := b.spec

	if !identRE.MatchString(s.Name) {
		return fmt.Errorf("collection name %q is not a valid identifier (lowercase, 1-63 chars, [a-z_][a-z0-9_]*)", s.Name)
	}
	if s.Name[0] == '_' {
		return fmt.Errorf("collection name %q is reserved (leading underscore is for system tables)", s.Name)
	}
	if s.Auth && s.Tenant {
		// Per-tenant auth collections need tenant resolution to be
		// available at signup/signin time. That landing in v0.4
		// (tenant middleware) — until then, refuse the combo so users
		// don't ship a half-broken multi-tenant signup flow.
		return fmt.Errorf("collection %q: AuthCollection + .Tenant() not supported until v0.4 (tenant middleware)", s.Name)
	}

	seen := make(map[string]struct{}, len(s.Fields))
	for i, f := range s.Fields {
		if !identRE.MatchString(f.Name) {
			return fmt.Errorf("collection %q: field #%d name %q is not a valid identifier", s.Name, i, f.Name)
		}
		if _, reserved := alwaysReserved[f.Name]; reserved {
			return fmt.Errorf("collection %q: field name %q is reserved (auto-injected)", s.Name, f.Name)
		}
		if _, reserved := authReserved[f.Name]; reserved && s.Auth {
			return fmt.Errorf("collection %q: field name %q is reserved on auth collections (auto-injected)", s.Name, f.Name)
		}
		if _, reserved := sqlReservedKeywords[f.Name]; reserved {
			return fmt.Errorf("collection %q: field name %q is a SQL reserved keyword — pick a different name", s.Name, f.Name)
		}
		if _, dup := seen[f.Name]; dup {
			return fmt.Errorf("collection %q: duplicate field name %q", s.Name, f.Name)
		}
		seen[f.Name] = struct{}{}

		if err := validateField(s.Name, f); err != nil {
			return err
		}
	}

	for i, idx := range s.Indexes {
		if !identRE.MatchString(idx.Name) {
			return fmt.Errorf("collection %q: index #%d name %q invalid", s.Name, i, idx.Name)
		}
		if len(idx.Columns) == 0 {
			return fmt.Errorf("collection %q: index %q has no columns", s.Name, idx.Name)
		}
		for _, col := range idx.Columns {
			if _, ok := seen[col]; !ok && !isSystemColumn(col, s.Tenant) {
				return fmt.Errorf("collection %q: index %q references unknown column %q", s.Name, idx.Name, col)
			}
		}
	}

	return nil
}

// localeKeyRE matches the keys allowed in a Translatable field's
// JSONB map. BCP-47 `lang` or `lang-REGION` with the conventions
// enforced everywhere else in Railbase (lowercase language, optional
// uppercase region). Compiled once at package init.
var localeKeyRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

// IsValidLocaleKey is the regex predicate that REST + admin use to
// validate a single Translatable field's locale key on write. Exposed
// so the REST coercion layer can share the rule without re-compiling
// the regex.
func IsValidLocaleKey(s string) bool { return localeKeyRE.MatchString(s) }

// validateField runs per-type sanity checks. The constraints here
// catch obvious DSL mistakes (MinLen > MaxLen, empty enum, relation
// to itself's not-yet-declared partner, etc.) before they become
// confusing SQL errors.
func validateField(coll string, f FieldSpec) error {
	// Translatable is restricted to the text-shaped types in v1.
	// Validate early so the operator gets a clear DSL error instead of
	// surprising-on-write JSONB shape failures.
	if f.Translatable {
		switch f.Type {
		case TypeText, TypeRichText, TypeMarkdown:
			// fine
		default:
			return fmt.Errorf("collection %q field %q: Translatable() is only supported on Text / RichText / Markdown fields (got %s)",
				coll, f.Name, f.Type)
		}
		if f.Unique {
			return fmt.Errorf("collection %q field %q: Translatable() is incompatible with Unique() — uniqueness across translation maps is not well-defined",
				coll, f.Name)
		}
		if f.HasDefault {
			// JSONB defaults on Translatable would need shape-validated
			// per-locale defaults; not supported in v1.
			return fmt.Errorf("collection %q field %q: Translatable() does not support Default() in v1",
				coll, f.Name)
		}
		if f.FTS {
			// FTS on a JSONB column requires per-locale tsvector
			// generation, which we haven't designed yet. Reject for v1.
			return fmt.Errorf("collection %q field %q: Translatable() is incompatible with FTS() in v1",
				coll, f.Name)
		}
	}
	switch f.Type {
	case TypeText, TypeRichText:
		if f.MinLen != nil && f.MaxLen != nil && *f.MinLen > *f.MaxLen {
			return fmt.Errorf("collection %q field %q: MinLen=%d > MaxLen=%d",
				coll, f.Name, *f.MinLen, *f.MaxLen)
		}
	case TypeNumber:
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return fmt.Errorf("collection %q field %q: Min=%g > Max=%g",
				coll, f.Name, *f.Min, *f.Max)
		}
	case TypeSelect, TypeMultiSelect:
		if len(f.SelectValues) == 0 {
			return fmt.Errorf("collection %q field %q: select with no values",
				coll, f.Name)
		}
		if f.HasDefault {
			d, ok := f.Default.(string)
			if !ok {
				return fmt.Errorf("collection %q field %q: select default must be string",
					coll, f.Name)
			}
			found := false
			for _, v := range f.SelectValues {
				if v == d {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("collection %q field %q: default %q not in allowed values %v",
					coll, f.Name, d, f.SelectValues)
			}
		}
		if f.Type == TypeMultiSelect && f.MinSelections != nil && f.MaxSelections != nil &&
			*f.MinSelections > *f.MaxSelections {
			return fmt.Errorf("collection %q field %q: Min=%d > Max=%d selections",
				coll, f.Name, *f.MinSelections, *f.MaxSelections)
		}
	case TypeRelation, TypeRelations:
		if !identRE.MatchString(f.RelatedCollection) {
			return fmt.Errorf("collection %q field %q: invalid related collection %q",
				coll, f.Name, f.RelatedCollection)
		}
		if f.CascadeDelete && f.SetNullOnDelete {
			return fmt.Errorf("collection %q field %q: CascadeDelete and SetNullOnDelete are mutually exclusive",
				coll, f.Name)
		}
	}
	return nil
}

// isSystemColumn lets index validation reference auto-added columns.
func isSystemColumn(name string, tenant bool) bool {
	switch name {
	case "id", "created", "updated":
		return true
	case "tenant_id":
		return tenant
	}
	return false
}

// IsSystemColumnFor reports whether name is auto-injected for spec.
// Used by the SQL generator and CRUD handlers — a single source of
// truth so "what columns does the generator own" stays consistent.
func IsSystemColumnFor(spec CollectionSpec, name string) bool {
	switch name {
	case "id", "created", "updated":
		return true
	case "tenant_id":
		return spec.Tenant
	case "email", "password_hash", "verified", "token_key", "last_login_at":
		return spec.Auth
	}
	return false
}
