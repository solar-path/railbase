package filter

import (
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// Context is the request-time data the SQL compiler needs to resolve
// magic variables. Every magic var resolves to a parameter binding —
// never inlined — so a hostile filter literal can't smuggle SQL.
type Context struct {
	// AuthID is "" for anonymous requests. Magic var
	// `@request.auth.id` / `@me` resolves to AuthID; in NULL contexts
	// (no signed-in user) callers should rewrite the rule to a static
	// `false` upstream rather than pass an empty string here.
	AuthID string

	// AuthCollection is the auth collection the principal belongs to,
	// e.g. "users". Empty for anonymous.
	AuthCollection string

	// Schema resolves a collection name to its spec. Required only
	// when filters use dotted paths (`project.owner`); nil is fine
	// for flat-field filters. The resolver lets the SQL compiler
	// walk a relation FK chain without the filter package importing
	// the global schema registry (preserves the inward-pointing
	// dependency arrow).
	//
	// Typical wiring: pass `schema.ResolveCollection` from the
	// schema registry package. Returns (spec, true) on hit, (zero,
	// false) on unknown name — the compiler turns the latter into
	// an explicit "unknown collection" filter error rather than
	// SQL drift.
	Schema func(name string) (builder.CollectionSpec, bool)
}

// Compile turns an AST into a parameterized SQL fragment plus the
// argument list. Returned SQL is safe to splice into a larger query
// because every literal/magic var is bound through $N placeholders.
//
// startParam is the next $N to use; the returned `nextParam` lets the
// caller chain Compile with surrounding clauses (e.g., LIMIT/OFFSET).
//
// fieldExists checks whether an Ident is a real column — it lets the
// CRUD handler reject `?filter=mystery_column='x'` with a 400 before
// the query reaches Postgres. Pass nil to skip validation (used by
// rule compilation, where the rule is known-good at registration).
func Compile(n Node, spec builder.CollectionSpec, ctx Context, startParam int) (sql string, args []any, nextParam int, err error) {
	c := &compiler{spec: spec, ctx: ctx, next: startParam}
	if err := c.emit(n); err != nil {
		return "", nil, startParam, err
	}
	return c.b.String(), c.args, c.next, nil
}

type compiler struct {
	spec builder.CollectionSpec
	ctx  Context
	b    strings.Builder
	args []any
	next int
}

// param appends v to args and writes a $N placeholder. Centralised
// so the next-param counter is impossible to desync.
func (c *compiler) param(v any) {
	c.args = append(c.args, v)
	fmt.Fprintf(&c.b, "$%d", c.next)
	c.next++
}

// emit walks the AST, writing SQL into c.b and binding parameters.
func (c *compiler) emit(n Node) error {
	switch v := n.(type) {
	case Or:
		c.b.WriteString("(")
		if err := c.emit(v.L); err != nil {
			return err
		}
		c.b.WriteString(" OR ")
		if err := c.emit(v.R); err != nil {
			return err
		}
		c.b.WriteString(")")
	case And:
		c.b.WriteString("(")
		if err := c.emit(v.L); err != nil {
			return err
		}
		c.b.WriteString(" AND ")
		if err := c.emit(v.R); err != nil {
			return err
		}
		c.b.WriteString(")")
	case Compare:
		return c.emitCompare(v)
	case In:
		return c.emitIn(v)
	case Between:
		return c.emitBetween(v)
	case IsNull:
		c.b.WriteString("(")
		if err := c.emit(v.Target); err != nil {
			return err
		}
		c.b.WriteString(" IS NULL)")
	case IsNotNull:
		c.b.WriteString("(")
		if err := c.emit(v.Target); err != nil {
			return err
		}
		c.b.WriteString(" IS NOT NULL)")
	case Ident:
		if err := c.emitIdent(v); err != nil {
			return err
		}
	case MagicVar:
		c.emitMagic(v)
	case StringLit:
		c.param(v.V)
	case IntLit:
		c.param(v.V)
	case FloatLit:
		c.param(v.V)
	case BoolLit:
		c.param(v.V)
	case NullLit:
		// Bare `null` outside a comparison is not a valid SQL value —
		// the parser produces this only when the user wrote `null` as
		// a primary, which makes no semantic sense alone.
		return fmt.Errorf("`null` literal is only valid as part of a comparison (e.g. `field = null`)")
	default:
		return fmt.Errorf("filter: unknown AST node %T", n)
	}
	return nil
}

func (c *compiler) emitCompare(v Compare) error {
	// Rewrite `field = null` / `field != null` into IS / IS NOT NULL,
	// otherwise PG silently returns no rows for `= NULL`.
	if _, lNull := v.L.(NullLit); lNull {
		switch v.Op {
		case "=":
			return c.emit(IsNull{Target: v.R})
		case "!=":
			return c.emit(IsNotNull{Target: v.R})
		}
	}
	if _, rNull := v.R.(NullLit); rNull {
		switch v.Op {
		case "=":
			return c.emit(IsNull{Target: v.L})
		case "!=":
			return c.emit(IsNotNull{Target: v.L})
		}
	}

	// v0.4.2 — short-circuit anonymous comparisons against UUID-typed
	// columns. Closes Sentinel FEEDBACK.md G4.
	//
	// The bug: `ListRule("@request.auth.id = owner")` with `owner` a
	// Relation field (UUID FK). An anonymous request has AuthID = ""
	// so the parameter binds the empty string, and Postgres tries to
	// coerce '' to uuid for the equality, panicking with
	//   ERROR: invalid input syntax for type uuid: "" (SQLSTATE 22P02)
	// The REST layer surfaces this as a 500 "count failed" instead of
	// the operator-expected "rule denies → empty list" outcome.
	//
	// Fix: detect "anonymous magic auth-id compared against UUID
	// column" before emitting the comparison, and replace the whole
	// node with a constant SQL false. Semantically correct — an
	// anonymous principal cannot own any row keyed by a UUID FK, so
	// the rule must deny.
	//
	// We do NOT short-circuit against text-typed columns (which
	// happily accept ''); the existing `@request.auth.id != ''`
	// idiom for "any authenticated user" keeps working unchanged.
	if c.ctx.AuthID == "" {
		if c.compareIsAnonUUID(v.L, v.R) || c.compareIsAnonUUID(v.R, v.L) {
			// `=` against an empty principal never matches; `!=` against
			// it could theoretically match every row, but treating a
			// missing principal as "matches nothing" is the safer
			// deny-by-default posture for rules. Both operators
			// collapse to FALSE.
			c.b.WriteString("(false)")
			return nil
		}
	}

	switch v.Op {
	case "~":
		return c.emitLike(v.L, v.R, false)
	case "!~":
		return c.emitLike(v.L, v.R, true)
	case "=", "!=", ">", "<", ">=", "<=":
		c.b.WriteString("(")
		if err := c.emit(v.L); err != nil {
			return err
		}
		c.b.WriteString(" " + v.Op + " ")
		if err := c.emit(v.R); err != nil {
			return err
		}
		c.b.WriteString(")")
	default:
		return fmt.Errorf("unsupported operator %q", v.Op)
	}
	return nil
}

// emitLike produces an ILIKE expression with %v% wrapping. The
// pattern is parameterized via the cleaner ` LIKE '%' || $N || '%'`
// form so we don't hand-build the wildcard inside Go code (and avoid
// double-encoding when v already contains `%`).
func (c *compiler) emitLike(left, right Node, negate bool) error {
	c.b.WriteString("(")
	if err := c.emit(left); err != nil {
		return err
	}
	if negate {
		c.b.WriteString(" NOT ILIKE ('%' || ")
	} else {
		c.b.WriteString(" ILIKE ('%' || ")
	}
	if err := c.emit(right); err != nil {
		return err
	}
	c.b.WriteString(" || '%'))")
	return nil
}

// emitBetween renders `(target BETWEEN lo AND hi)`. Postgres treats
// BETWEEN as inclusive; we keep that contract verbatim. Bounds with
// `lo > hi` produce a vacuous predicate (matches Postgres / standard SQL)
// rather than an error — operators may rely on this behaviour to model
// "open" ranges by binding the low side to a known sentinel.
func (c *compiler) emitBetween(v Between) error {
	c.b.WriteString("(")
	if err := c.emit(v.Target); err != nil {
		return err
	}
	c.b.WriteString(" BETWEEN ")
	if err := c.emit(v.Lo); err != nil {
		return err
	}
	c.b.WriteString(" AND ")
	if err := c.emit(v.Hi); err != nil {
		return err
	}
	c.b.WriteString(")")
	return nil
}

func (c *compiler) emitIn(v In) error {
	c.b.WriteString("(")
	if err := c.emit(v.Target); err != nil {
		return err
	}
	c.b.WriteString(" IN (")
	for i, item := range v.Items {
		if i > 0 {
			c.b.WriteString(", ")
		}
		if err := c.emit(item); err != nil {
			return err
		}
	}
	c.b.WriteString("))")
	return nil
}

func (c *compiler) emitIdent(v Ident) error {
	// Single-segment ident — fast path, validates against current
	// collection's columns.
	if v.DottedPath == "" {
		if !columnAllowed(c.spec, v.Name) {
			return fmt.Errorf("unknown field %q on collection %q", v.Name, c.spec.Name)
		}
		c.b.WriteString(v.Name)
		return nil
	}
	// Dotted-path ident — one FK hop. Sentinel's "project.owner"
	// shape: `project` is a relation FK on `tasks`, we want
	// `projects.owner`. Emit as a scalar subquery so the surrounding
	// comparison clause stays well-formed:
	//
	//   (SELECT projects.owner FROM projects WHERE projects.id = tasks.project)
	//
	// FK semantics guarantee at most one matching row, so the
	// subquery is single-valued. Returns NULL if `tasks.project` is
	// NULL — comparisons against NULL evaluate to UNKNOWN, which is
	// the correct posture (the rule shouldn't accidentally match).
	path := strings.Split(v.DottedPath, ".")
	if len(path) > 2 {
		return fmt.Errorf("dotted path %q: only one FK hop is supported (v3.x); split into multiple filters or denormalise",
			v.DottedPath)
	}
	relName := path[0]
	targetCol := path[1]

	// First segment must be a Relation field on the current spec.
	var relField builder.FieldSpec
	found := false
	for _, f := range c.spec.Fields {
		if f.Name == relName && f.Type == builder.TypeRelation {
			relField = f
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("dotted path %q: %q is not a relation field on %q",
			v.DottedPath, relName, c.spec.Name)
	}
	if c.ctx.Schema == nil {
		return fmt.Errorf("dotted path %q: filter Context.Schema resolver not wired — cannot resolve related collection %q",
			v.DottedPath, relField.RelatedCollection)
	}
	relSpec, ok := c.ctx.Schema(relField.RelatedCollection)
	if !ok {
		return fmt.Errorf("dotted path %q: related collection %q not in schema registry",
			v.DottedPath, relField.RelatedCollection)
	}
	// Validate the second segment against the related collection's
	// columns — same column-allowed posture as flat idents.
	if !columnAllowed(relSpec, targetCol) {
		return fmt.Errorf("dotted path %q: unknown field %q on related collection %q",
			v.DottedPath, targetCol, relSpec.Name)
	}
	fmt.Fprintf(&c.b, "(SELECT %s.%s FROM %s WHERE %s.id = %s.%s)",
		relSpec.Name, targetCol,
		relSpec.Name,
		relSpec.Name, c.spec.Name, relName)
	return nil
}

// compareIsAnonUUID reports whether `magicSide` is the auth-id magic
// var AND `columnSide` is an Ident that resolves to a UUID-typed column
// on the current spec. Used by emitCompare to short-circuit the
// anonymous-empty-string-into-UUID-column crash (FEEDBACK G4).
//
// Only flat idents are considered — dotted paths resolve through a
// scalar subquery whose result is the related column's type; we leave
// those alone because the related-collection schema isn't always
// available at this layer (Schema resolver returning false on miss
// would push the diagnostic upstream).
func (c *compiler) compareIsAnonUUID(magicSide, columnSide Node) bool {
	mv, ok := magicSide.(MagicVar)
	if !ok {
		return false
	}
	if mv.Name != "@request.auth.id" && mv.Name != "@me" {
		return false
	}
	id, ok := columnSide.(Ident)
	if !ok || id.DottedPath != "" {
		return false
	}
	return c.identIsUUIDColumn(id.Name)
}

// identIsUUIDColumn says whether `name` is a UUID-typed column on the
// current spec. System columns (`id`, `tenant_id`, `parent`) are
// hard-coded; user fields are UUID only when declared as Relation.
//
// `parent` is uuid only when AdjacencyList is enabled — the column
// doesn't exist otherwise, so a filter naming it would have been
// rejected upstream by columnAllowed.
func (c *compiler) identIsUUIDColumn(name string) bool {
	switch name {
	case "id":
		return true
	case "tenant_id":
		return c.spec.Tenant
	case "parent":
		return c.spec.AdjacencyList
	}
	for _, f := range c.spec.Fields {
		if f.Name == name {
			return f.Type == builder.TypeRelation
		}
	}
	return false
}

func (c *compiler) emitMagic(v MagicVar) {
	switch v.Name {
	case "@request.auth.id", "@me":
		// Bound as a parameter even when empty — that way the rule
		// `@request.auth.id != ''` evaluates correctly for anonymous.
		c.param(c.ctx.AuthID)
	case "@request.auth.collectionName":
		c.param(c.ctx.AuthCollection)
	default:
		// Lexer should already have rejected anything else; a panic
		// here would mean a programming error in the lexer/parser.
		c.param(nil)
	}
}

// columnAllowed says whether name is a valid filter target on spec.
// User fields, plus the system fields appropriate to the spec
// (always: id/created/updated; tenant: tenant_id; auth: email/
// verified/last_login_at — but never password_hash/token_key).
func columnAllowed(spec builder.CollectionSpec, name string) bool {
	switch name {
	case "id", "created", "updated":
		return true
	case "tenant_id":
		return spec.Tenant
	case "email", "verified", "last_login_at":
		return spec.Auth
	case "parent":
		return spec.AdjacencyList
	case "sort_index":
		return spec.Ordered
	case "password_hash", "token_key":
		// Never let a filter touch credential material — even reading
		// the hash via `~` could leak bits via timing.
		return false
	}
	for _, f := range spec.Fields {
		if f.Name == name {
			// JSON / files / relations get filtered out for v0.3.3 —
			// they need dotted paths or array operators we don't have
			// yet. Sortable/filterable types are the simple scalars.
			switch f.Type {
			case builder.TypeJSON, builder.TypeFiles, builder.TypeFile,
				builder.TypeMultiSelect, builder.TypeRelations,
				builder.TypePassword,
				builder.TypePersonName, builder.TypeQuantity,
				builder.TypeCoordinates,                      // JSONB {lat,lng} — needs ST_* ops (deferred to PostGIS plugin)
				builder.TypeAddress,                          // JSONB structured — filter by dotted path (deferred)
				builder.TypeMoneyRange,                       // JSONB {min,max,currency} — needs range overlap ops (deferred)
				builder.TypeDateRange,                        // Postgres daterange — needs @> && etc. (deferred)
				builder.TypeTimeRange,                        // JSONB {start,end} — needs range overlap (deferred)
				builder.TypeBankAccount,                      // JSONB structured — filter by dotted path (deferred)
				builder.TypeTags,                            // TEXT[] — needs array ops (deferred)
				builder.TypeTreePath: // LTREE — needs ancestor/descendant ops (deferred)
				return false
			}
			return true
		}
	}
	return false
}
