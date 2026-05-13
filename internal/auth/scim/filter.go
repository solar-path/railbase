package scim

// SCIM 2.0 filter language — RFC 7644 §3.4.2.2.
//
// Grammar (simplified to what real-world IdPs emit):
//
//	filter   = orExpr
//	orExpr   = andExpr ( "or" andExpr )*
//	andExpr  = notExpr ( "and" notExpr )*
//	notExpr  = "not" "(" orExpr ")" | atom
//	atom     = "(" orExpr ")" | compare
//	compare  = attrPath compareOp value
//	         | attrPath "pr"                  -- present
//	compareOp = "eq" | "ne" | "co" | "sw" | "ew" | "gt" | "ge" | "lt" | "le"
//	attrPath = identifier ("." identifier)*
//	value    = stringLit | numberLit | "true" | "false" | "null"
//
// Operators are case-INSENSITIVE per spec. Identifiers ARE case-
// insensitive for SCIM attributes (RFC says so).
//
// We parse to a tree, then a planner translates the tree to a
// parameterized SQL fragment. The translator handles only attribute
// paths the caller registers; unknown attributes return an error
// (avoids silently dropping filters the IdP intended to apply).
//
// Examples the parser accepts (taken from real-world IdP traffic):
//
//	userName eq "alice"
//	userName eq "alice" and active eq true
//	(userName eq "alice" or userName eq "bob") and active eq true
//	emails[primary eq true].value eq "alice@example.com"  -- (NOT YET)
//	displayName co "engineering"
//	id pr
//	meta.created gt "2024-01-01T00:00:00Z"
//
// We deliberately do NOT yet support:
//
//   - Complex attribute filters like `emails[primary eq true].value`
//     — that requires JOIN-shaped SQL the v1.7.51 schema doesn't model
//     (we store `emails` denormalised into `email` on the user row).
//   - Sub-attribute paths beyond 2 levels (`meta.created` works;
//     `meta.lastModified.timezone` does not, but no IdP emits that).

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Op is the parsed comparison operator.
type Op string

const (
	OpEq      Op = "eq"
	OpNe      Op = "ne"
	OpCo      Op = "co" // contains
	OpSw      Op = "sw" // starts with
	OpEw      Op = "ew" // ends with
	OpGt      Op = "gt"
	OpGe      Op = "ge"
	OpLt      Op = "lt"
	OpLe      Op = "le"
	OpPresent Op = "pr"
)

// Node is any AST node. Concrete types: AndNode, OrNode, NotNode,
// CompareNode.
type Node interface{ isFilterNode() }

// AndNode is a logical AND of two children.
type AndNode struct{ Left, Right Node }

func (AndNode) isFilterNode() {}

// OrNode is a logical OR.
type OrNode struct{ Left, Right Node }

func (OrNode) isFilterNode() {}

// NotNode negates a child.
type NotNode struct{ Inner Node }

func (NotNode) isFilterNode() {}

// CompareNode is a single attribute compare.
type CompareNode struct {
	Path  []string // ["userName"], ["meta", "created"]
	Op    Op
	Value any // string, float64, bool, or nil (for "null")
}

func (CompareNode) isFilterNode() {}

// Parse turns a SCIM filter string into a Node tree. Empty / pure-
// whitespace input returns (nil, nil) which the caller should treat
// as "no filter — return all rows".
func Parse(s string) (Node, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	p := &parser{src: s}
	p.tokens = tokenise(s)
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.tokens) {
		return nil, fmt.Errorf("scim filter: unexpected token %q at pos %d", p.tokens[p.pos].text, p.tokens[p.pos].at)
	}
	return node, nil
}

type tokenKind int

const (
	tkIdent tokenKind = iota
	tkString
	tkNumber
	tkOp
	tkAnd
	tkOr
	tkNot
	tkLParen
	tkRParen
	tkTrue
	tkFalse
	tkNull
)

type token struct {
	kind tokenKind
	text string
	at   int
}

func tokenise(s string) []token {
	out := []token{}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			out = append(out, token{tkLParen, "(", i})
			i++
		case c == ')':
			out = append(out, token{tkRParen, ")", i})
			i++
		case c == '"':
			// Quoted string. Supports \" and \\.
			j := i + 1
			var sb strings.Builder
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					sb.WriteByte(s[j+1])
					j += 2
					continue
				}
				if s[j] == '"' {
					break
				}
				sb.WriteByte(s[j])
				j++
			}
			out = append(out, token{tkString, sb.String(), i})
			i = j + 1
		case c >= '0' && c <= '9', c == '-':
			j := i + 1
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			out = append(out, token{tkNumber, s[i:j], i})
			i = j
		case isIdentStart(rune(c)):
			j := i + 1
			for j < len(s) && (isIdentPart(rune(s[j])) || s[j] == '.' || s[j] == '$') {
				j++
			}
			word := s[i:j]
			low := strings.ToLower(word)
			switch low {
			case "and":
				out = append(out, token{tkAnd, low, i})
			case "or":
				out = append(out, token{tkOr, low, i})
			case "not":
				out = append(out, token{tkNot, low, i})
			case "true":
				out = append(out, token{tkTrue, low, i})
			case "false":
				out = append(out, token{tkFalse, low, i})
			case "null":
				out = append(out, token{tkNull, low, i})
			case "eq", "ne", "co", "sw", "ew", "gt", "ge", "lt", "le", "pr":
				out = append(out, token{tkOp, low, i})
			default:
				out = append(out, token{tkIdent, word, i})
			}
			i = j
		default:
			// Unknown rune — emit as identifier so parseError surfaces.
			out = append(out, token{tkIdent, string(c), i})
			i++
		}
	}
	return out
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdentPart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

type parser struct {
	src    string
	tokens []token
	pos    int
}

func (p *parser) peek() *token {
	if p.pos >= len(p.tokens) {
		return nil
	}
	return &p.tokens[p.pos]
}

func (p *parser) next() *token {
	t := p.peek()
	if t != nil {
		p.pos++
	}
	return t
}

func (p *parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != tkOr {
			return left, nil
		}
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = OrNode{Left: left, Right: right}
	}
}

func (p *parser) parseAnd() (Node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != tkAnd {
			return left, nil
		}
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = AndNode{Left: left, Right: right}
	}
}

func (p *parser) parseNot() (Node, error) {
	t := p.peek()
	if t == nil {
		return nil, errors.New("scim filter: unexpected end of input")
	}
	if t.kind == tkNot {
		p.next()
		// `not` is followed by a parenthesised expression per spec.
		l := p.peek()
		if l == nil || l.kind != tkLParen {
			return nil, errors.New("scim filter: 'not' must be followed by '('")
		}
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		r := p.peek()
		if r == nil || r.kind != tkRParen {
			return nil, errors.New("scim filter: missing ')' after 'not('")
		}
		p.next()
		return NotNode{Inner: inner}, nil
	}
	return p.parseAtom()
}

func (p *parser) parseAtom() (Node, error) {
	t := p.peek()
	if t == nil {
		return nil, errors.New("scim filter: unexpected end of input")
	}
	if t.kind == tkLParen {
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		r := p.peek()
		if r == nil || r.kind != tkRParen {
			return nil, errors.New("scim filter: missing ')'")
		}
		p.next()
		return inner, nil
	}
	return p.parseCompare()
}

func (p *parser) parseCompare() (Node, error) {
	t := p.peek()
	if t == nil || t.kind != tkIdent {
		got := "<eof>"
		if t != nil {
			got = t.text
		}
		return nil, fmt.Errorf("scim filter: expected attribute path, got %q", got)
	}
	p.next()
	path := strings.Split(t.text, ".")

	opTok := p.peek()
	if opTok == nil || opTok.kind != tkOp {
		got := "<eof>"
		if opTok != nil {
			got = opTok.text
		}
		return nil, fmt.Errorf("scim filter: expected operator after %q, got %q", t.text, got)
	}
	p.next()
	op := Op(opTok.text)

	if op == OpPresent {
		// `pr` takes no RHS.
		return CompareNode{Path: path, Op: op}, nil
	}

	valTok := p.peek()
	if valTok == nil {
		return nil, errors.New("scim filter: expected value after operator")
	}
	p.next()
	var val any
	switch valTok.kind {
	case tkString:
		val = valTok.text
	case tkNumber:
		f, err := strconv.ParseFloat(valTok.text, 64)
		if err != nil {
			return nil, fmt.Errorf("scim filter: invalid number %q", valTok.text)
		}
		val = f
	case tkTrue:
		val = true
	case tkFalse:
		val = false
	case tkNull:
		val = nil
	default:
		return nil, fmt.Errorf("scim filter: expected value literal, got %q", valTok.text)
	}
	return CompareNode{Path: path, Op: op, Value: val}, nil
}

// ColumnMap tells the SQL planner how each SCIM path resolves to a
// database column. Keys are lowercased + dotted (e.g. "username",
// "meta.created"). Values name the SQL column directly — the planner
// inlines them verbatim, so callers MUST sanitise (use only the closed
// allow-list of names registered here, never user input).
type ColumnMap map[string]string

// ToSQL renders an AST plus a ColumnMap into a parameterized SQL WHERE
// fragment + args. Returns ("", nil, nil) for a nil node (caller skips
// adding `AND (...)` to the outer query).
func ToSQL(node Node, cols ColumnMap) (string, []any, error) {
	if node == nil {
		return "", nil, nil
	}
	g := &sqlGen{cols: cols}
	frag, err := g.gen(node)
	if err != nil {
		return "", nil, err
	}
	return frag, g.args, nil
}

type sqlGen struct {
	cols ColumnMap
	args []any
}

func (g *sqlGen) addArg(v any) string {
	g.args = append(g.args, v)
	return fmt.Sprintf("$%d", len(g.args))
}

func (g *sqlGen) gen(n Node) (string, error) {
	switch x := n.(type) {
	case AndNode:
		l, err := g.gen(x.Left)
		if err != nil {
			return "", err
		}
		r, err := g.gen(x.Right)
		if err != nil {
			return "", err
		}
		return "(" + l + " AND " + r + ")", nil
	case OrNode:
		l, err := g.gen(x.Left)
		if err != nil {
			return "", err
		}
		r, err := g.gen(x.Right)
		if err != nil {
			return "", err
		}
		return "(" + l + " OR " + r + ")", nil
	case NotNode:
		inner, err := g.gen(x.Inner)
		if err != nil {
			return "", err
		}
		return "NOT (" + inner + ")", nil
	case CompareNode:
		key := strings.ToLower(strings.Join(x.Path, "."))
		col, ok := g.cols[key]
		if !ok {
			return "", fmt.Errorf("scim filter: attribute %q is not filterable", key)
		}
		return g.compareSQL(col, x.Op, x.Value)
	}
	return "", fmt.Errorf("scim filter: unknown node type %T", n)
}

func (g *sqlGen) compareSQL(col string, op Op, val any) (string, error) {
	switch op {
	case OpPresent:
		return "(" + col + " IS NOT NULL AND " + col + "::text <> '')", nil
	case OpEq:
		if val == nil {
			return col + " IS NULL", nil
		}
		return col + " = " + g.addArg(val), nil
	case OpNe:
		if val == nil {
			return col + " IS NOT NULL", nil
		}
		return "(" + col + " IS NULL OR " + col + " <> " + g.addArg(val) + ")", nil
	case OpCo:
		s, ok := val.(string)
		if !ok {
			return "", fmt.Errorf("scim filter: 'co' expects string, got %T", val)
		}
		return col + " ILIKE " + g.addArg("%"+s+"%"), nil
	case OpSw:
		s, ok := val.(string)
		if !ok {
			return "", fmt.Errorf("scim filter: 'sw' expects string, got %T", val)
		}
		return col + " ILIKE " + g.addArg(s+"%"), nil
	case OpEw:
		s, ok := val.(string)
		if !ok {
			return "", fmt.Errorf("scim filter: 'ew' expects string, got %T", val)
		}
		return col + " ILIKE " + g.addArg("%"+s), nil
	case OpGt:
		return col + " > " + g.addArg(val), nil
	case OpGe:
		return col + " >= " + g.addArg(val), nil
	case OpLt:
		return col + " < " + g.addArg(val), nil
	case OpLe:
		return col + " <= " + g.addArg(val), nil
	}
	return "", fmt.Errorf("scim filter: unsupported operator %q", op)
}
