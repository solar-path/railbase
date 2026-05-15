package filter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// equalFoldASCII is a tight case-insensitive ASCII compare used for the
// `AND` keyword inside BETWEEN. strings.EqualFold works but allocates
// less than a regex; we sidestep Unicode folding entirely here because
// `AND` is by definition ASCII.
func equalFoldASCII(a, b string) bool { return strings.EqualFold(a, b) }

// Parse turns a filter expression source string into an AST.
// Returns a *PositionedError on bad input — callers map that to
// HTTP 400 with `details.position`.
//
// Empty source returns (nil, nil) — the rest of the system treats
// nil filter as "no constraint" (open ListRule).
//
// Concurrency / caching: identical source strings share a single parsed
// AST via the package-private astCache (see cache.go). The AST is
// immutable downstream — Compile walks the tree read-only and the SQL
// emitter builds its own buffer — so sharing the *Node across requests
// is safe. Parse errors are NOT cached (GetOrLoad contract); each
// malformed filter re-parses on the next call.
func Parse(src string) (Node, error) {
	if src == "" {
		return nil, nil
	}
	return parseCached(context.Background(), src)
}

// parseUncached is the actual recursive-descent entry point. Kept
// separate from Parse so the cache wrapper (cache.go) can plug in
// transparently and so tests can exercise the parser directly without
// touching the package-global cache.
func parseUncached(src string) (Node, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tkEOF {
		return nil, p.errAt(p.peek().pos, "unexpected token %q after expression", p.peek().val)
	}
	return n, nil
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token {
	if p.i >= len(p.toks) {
		return token{kind: tkEOF}
	}
	return p.toks[p.i]
}

func (p *parser) advance() token {
	t := p.peek()
	if p.i < len(p.toks) {
		p.i++
	}
	return t
}

func (p *parser) errAt(pos int, format string, args ...any) error {
	return &PositionedError{Position: pos, Message: fmt.Sprintf(format, args...)}
}

// expr → orExpr
func (p *parser) parseExpr() (Node, error) { return p.parseOr() }

// orExpr → andExpr (`||` andExpr)*
func (p *parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tkOr {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = Or{L: left, R: right}
	}
	return left, nil
}

// andExpr → compare (`&&` compare)*
func (p *parser) parseAnd() (Node, error) {
	left, err := p.parseCompare()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tkAnd {
		p.advance()
		right, err := p.parseCompare()
		if err != nil {
			return nil, err
		}
		left = And{L: left, R: right}
	}
	return left, nil
}

// compare → primary ( (cmpOp primary)
//                    | ("IN" "(" exprList ")")
//                    | ("BETWEEN" primary "AND" primary)
//                    | ("IS" ["NOT"] "NULL") )?
//
// PB-style: comparisons don't chain (`a < b < c` is rejected), so
// we either consume one comparison or fall through.
func (p *parser) parseCompare() (Node, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	switch p.peek().kind {
	case tkOp:
		op := p.advance()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return Compare{Op: op.val, L: left, R: right}, nil
	case tkBetween:
		// v1.7.21 — `target BETWEEN lo AND hi`. We require the literal
		// keyword "AND" between bounds (NOT `&&` — that's logical
		// conjunction and would create grammar ambiguity). The lexer
		// leaves `AND` as a regular tkIdent; the parser disambiguates
		// here via case-insensitive comparison.
		betweenTok := p.advance()
		lo, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		andTok := p.peek()
		if andTok.kind != tkIdent || !equalFoldASCII(andTok.val, "AND") {
			return nil, p.errAt(andTok.pos, "expected `AND` after BETWEEN bound (got %q)", andTok.val)
		}
		p.advance()
		hi, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		_ = betweenTok // kept for symmetry / future error blame
		return Between{Target: left, Lo: lo, Hi: hi}, nil
	case tkIn:
		p.advance()
		if p.peek().kind != tkLParen {
			return nil, p.errAt(p.peek().pos, "expected `(` after IN")
		}
		p.advance()
		var items []Node
		if p.peek().kind != tkRParen {
			for {
				item, err := p.parsePrimary()
				if err != nil {
					return nil, err
				}
				items = append(items, item)
				if p.peek().kind == tkComma {
					p.advance()
					continue
				}
				break
			}
		}
		if p.peek().kind != tkRParen {
			return nil, p.errAt(p.peek().pos, "expected `,` or `)` in IN list")
		}
		p.advance()
		if len(items) == 0 {
			return nil, p.errAt(p.peek().pos, "IN list cannot be empty")
		}
		return In{Target: left, Items: items}, nil
	case tkIs:
		p.advance()
		negate := false
		if p.peek().kind == tkNot {
			p.advance()
			negate = true
		}
		if p.peek().kind != tkNull {
			return nil, p.errAt(p.peek().pos, "expected NULL after IS [NOT]")
		}
		p.advance()
		if negate {
			return IsNotNull{Target: left}, nil
		}
		return IsNull{Target: left}, nil
	}
	return left, nil
}

// primary → literal | identifier | magicVar | "(" expr ")"
func (p *parser) parsePrimary() (Node, error) {
	t := p.peek()
	switch t.kind {
	case tkLParen:
		p.advance()
		n, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tkRParen {
			return nil, p.errAt(p.peek().pos, "expected `)`")
		}
		p.advance()
		return n, nil
	case tkIdent:
		p.advance()
		// Dotted path: consume `.<ident>` greedily so `project.owner`
		// becomes Ident{Name: "owner", DottedPath: "project.owner"}.
		// Stop at the first non-dot or non-ident — the parser's
		// recursive descent picks up the comparison operator that
		// follows.
		if p.peek().kind != tkDot {
			return Ident{Name: t.val}, nil
		}
		path := []string{t.val}
		for p.peek().kind == tkDot {
			p.advance() // consume dot
			seg := p.peek()
			if seg.kind != tkIdent {
				return nil, p.errAt(seg.pos, "expected identifier after '.'")
			}
			p.advance()
			path = append(path, seg.val)
		}
		return Ident{Name: path[len(path)-1], DottedPath: strings.Join(path, ".")}, nil
	case tkMagic:
		p.advance()
		return MagicVar{Name: t.val}, nil
	case tkString:
		p.advance()
		return StringLit{V: t.val}, nil
	case tkInt:
		p.advance()
		v, err := strconv.ParseInt(t.val, 10, 64)
		if err != nil {
			return nil, p.errAt(t.pos, "bad integer literal %q", t.val)
		}
		return IntLit{V: v}, nil
	case tkFloat:
		p.advance()
		v, err := strconv.ParseFloat(t.val, 64)
		if err != nil {
			return nil, p.errAt(t.pos, "bad float literal %q", t.val)
		}
		return FloatLit{V: v}, nil
	case tkBool:
		p.advance()
		return BoolLit{V: t.val == "true"}, nil
	case tkNull:
		p.advance()
		return NullLit{}, nil
	case tkEOF:
		return nil, p.errAt(t.pos, "unexpected end of expression")
	}
	return nil, p.errAt(t.pos, "unexpected token %q", t.val)
}
