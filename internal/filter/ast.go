// Package filter parses Railbase filter expressions and compiles
// them to parameterized PostgreSQL WHERE clauses.
//
// The grammar is a deliberate subset of PocketBase's:
//
//	expr      = orExpr
//	orExpr    = andExpr ( "||" andExpr )*
//	andExpr   = notExpr ( "&&" notExpr )*
//	notExpr   = compare           // unary `!` deferred until v1
//	compare   = primary ( cmpOp primary )*
//	          | primary "IN" "(" exprList ")"
//	          | primary "BETWEEN" primary "AND" primary    ; v1.7.21
//	          | primary "IS" [ "NOT" ] "NULL"
//	cmpOp     = "=" | "!=" | ">" | "<" | ">=" | "<=" | "~" | "!~"
//	primary   = literal | identifier | magicVar | "(" expr ")"
//	literal   = string | number | boolean | "null"
//	string    = "'" ( escape | non-quote )* "'"           ; \\ and \'
//	number    = digit+ ( "." digit+ )?
//	boolean   = "true" | "false"
//	identifier = [a-z_][a-z0-9_]*                         ; v0.4 adds dotted paths
//	magicVar  = "@request.auth.id"
//	          | "@request.auth.collectionName"
//	          | "@me"                                     ; alias for @request.auth.id
//
// What's NOT in this build (caller-visible 400 errors):
//
//   - Dotted paths (`foo.bar`) — JSONB / relation expansion
//   - `@request.body.X` — needs request-time evaluation, lands with
//     CreateRule support in v0.3.4
//   - `@now`, `@yesterday`, `@todayStart` magic times
//   - `?=` (any-of-array)
//   - Unary `!` and `xor`
//
// Note on BETWEEN: bounds are emitted inclusive (Postgres's `BETWEEN`
// is `lo <= x AND x <= hi`). The parser DOES NOT reject `lo > hi` — at
// SQL-emission time that's just a vacuous predicate matching no rows,
// matching Postgres semantics. The bounds may be any expression that
// produces a comparable value (literal, identifier, magic var). The
// keyword between bounds is `AND` (PB convention) — NOT `&&` (which is
// the logical conjunction operator and would parse differently).
//
// Operator precedence (low → high): `||`, `&&`, comparisons.
// Parentheses override.
package filter

// Node is the AST root interface. Each Compile target (SQL, future
// in-memory evaluator) walks the tree via type switches.
type Node interface {
	isNode()
}

// Or evaluates left || right.
type Or struct{ L, R Node }

// And evaluates left && right.
type And struct{ L, R Node }

// Compare is a binary comparison: left <op> right. Op is one of
// the strings in CompareOps.
type Compare struct {
	Op string
	L  Node
	R  Node
}

// In implements `field IN (a, b, c)`. List entries are Node so a
// future literal-only restriction can be enforced at compile time.
type In struct {
	Target Node
	Items  []Node
}

// Between implements `field BETWEEN lo AND hi` (v1.7.21). Inclusive on
// both ends — matches Postgres semantics (`lo <= field AND field <= hi`).
// Lo/Hi are Nodes so magic vars and identifiers work just like in
// Compare (e.g. `age BETWEEN 18 AND @request.auth.maxAge`).
type Between struct {
	Target Node
	Lo     Node
	Hi     Node
}

// IsNull / IsNotNull implement `field IS [NOT] NULL`. Distinct types
// rather than a flag so the SQL emitter doesn't need to branch on
// boolean members.
type IsNull struct{ Target Node }
type IsNotNull struct{ Target Node }

// Ident references a column on the current collection — or, since
// v3.x, a column on a related collection reached via FK navigation.
//
// DottedPath is the dot-joined dotted form for multi-segment idents
// (`project.owner`). Single-segment idents (the common case: `name`,
// `created`, `tenant_id`) leave DottedPath empty and read the column
// via Name. Why a single string instead of `[]string`: keeping Ident
// comparable lets it be a map key in AST caches without a custom
// hash/equality codec (the cache_test.go ast-equality check would
// have to handle slices specially otherwise).
//
// SQL emission:
//
//   - DottedPath == "": emit the column name directly via Name.
//   - DottedPath has 2 segments: emit a scalar subquery walking ONE
//     FK hop — `(SELECT <related>.<seg1> FROM <related> WHERE
//     <related>.id = <current>.<seg0>)`. The first segment must be a
//     TypeRelation field on the current collection.
//   - 3+ segments: rejected today; closes the dominant Sentinel use
//     case (project.owner) without committing to recursive walk
//     semantics that need cycle protection.
//
// Why scalar subquery instead of JOIN: filters compose with WHERE,
// not FROM; emitting a scalar subquery keeps the surrounding clause
// shape unchanged and avoids ambiguous-column problems when the
// related collection has a same-named column as the base.
type Ident struct {
	Name       string
	DottedPath string // empty for flat names; e.g. "project.owner" for FK walk
}

// MagicVar refers to a request-context variable: @request.auth.id,
// @me, @request.auth.collectionName.
type MagicVar struct{ Name string }

// StringLit is a quoted string literal — emitted as a parameter,
// never inlined.
type StringLit struct{ V string }

// IntLit is an integer literal. Distinct from FloatLit so the SQL
// emitter can pick the right pgtype binding.
type IntLit struct{ V int64 }

// FloatLit is a numeric literal with a fractional part.
type FloatLit struct{ V float64 }

// BoolLit is true or false.
type BoolLit struct{ V bool }

// NullLit is the literal `null`. The parser accepts it inside
// comparisons, but the SQL emitter rewrites `field = null` →
// `field IS NULL` (and `!= null` → `IS NOT NULL`) so the wire form
// stays SQL-correct. Direct use elsewhere is a compile error.
type NullLit struct{}

// Boilerplate isNode markers.
func (Or) isNode()        {}
func (And) isNode()       {}
func (Compare) isNode()   {}
func (In) isNode()        {}
func (Between) isNode()   {}
func (IsNull) isNode()    {}
func (IsNotNull) isNode() {}
func (Ident) isNode()     {}
func (MagicVar) isNode()  {}
func (StringLit) isNode() {}
func (IntLit) isNode()    {}
func (FloatLit) isNode()  {}
func (BoolLit) isNode()   {}
func (NullLit) isNode()   {}

// CompareOps lists every binary comparison operator the parser
// recognises. SQL emitter reads from this set when validating an
// AST that might have come from elsewhere (rule strings, hand-built
// trees in tests).
var CompareOps = map[string]struct{}{
	"=":  {},
	"!=": {},
	">":  {},
	"<":  {},
	">=": {},
	"<=": {},
	"~":  {}, // ILIKE %v%   — case-insensitive substring
	"!~": {},
}
