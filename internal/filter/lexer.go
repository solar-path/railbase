package filter

import (
	"fmt"
	"strings"
	"unicode"
)

// tokenKind enumerates lexer outputs.
type tokenKind int

const (
	tkEOF tokenKind = iota
	tkIdent
	tkMagic    // @request.auth.id, @me, @request.auth.collectionName
	tkString   // 'hello'
	tkInt      // 42
	tkFloat    // 3.14
	tkBool     // true, false
	tkNull     // null
	tkIn       // IN (case-insensitive)
	tkBetween  // BETWEEN (case-insensitive) — v1.7.21
	tkIs       // IS
	tkNot      // NOT
	tkLParen   // (
	tkRParen   // )
	tkComma    // ,
	tkAnd      // &&
	tkOr       // ||
	tkOp       // = != > < >= <= ~ !~
)

type token struct {
	kind tokenKind
	val  string
	pos  int // byte offset in source — feeds parser error messages
}

// lex returns the token stream for src. Whitespace is skipped.
// On error returns the partial slice plus an error pointing at the
// failing position; callers (parser) treat it as a 400.
func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		ch := src[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '(':
			out = append(out, token{kind: tkLParen, val: "(", pos: i})
			i++
		case ch == ')':
			out = append(out, token{kind: tkRParen, val: ")", pos: i})
			i++
		case ch == ',':
			out = append(out, token{kind: tkComma, val: ",", pos: i})
			i++
		case ch == '&':
			if i+1 < len(src) && src[i+1] == '&' {
				out = append(out, token{kind: tkAnd, val: "&&", pos: i})
				i += 2
			} else {
				return out, posErr(i, "expected `&&`")
			}
		case ch == '|':
			if i+1 < len(src) && src[i+1] == '|' {
				out = append(out, token{kind: tkOr, val: "||", pos: i})
				i += 2
			} else {
				return out, posErr(i, "expected `||`")
			}
		case ch == '=':
			out = append(out, token{kind: tkOp, val: "=", pos: i})
			i++
		case ch == '!':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: tkOp, val: "!=", pos: i})
				i += 2
			} else if i+1 < len(src) && src[i+1] == '~' {
				out = append(out, token{kind: tkOp, val: "!~", pos: i})
				i += 2
			} else {
				return out, posErr(i, "stray `!` (did you mean `!=` or `!~`?)")
			}
		case ch == '>':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: tkOp, val: ">=", pos: i})
				i += 2
			} else {
				out = append(out, token{kind: tkOp, val: ">", pos: i})
				i++
			}
		case ch == '<':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: tkOp, val: "<=", pos: i})
				i += 2
			} else {
				out = append(out, token{kind: tkOp, val: "<", pos: i})
				i++
			}
		case ch == '~':
			out = append(out, token{kind: tkOp, val: "~", pos: i})
			i++
		case ch == '\'':
			s, n, err := lexString(src[i:])
			if err != nil {
				return out, posErr(i, "%s", err.Error())
			}
			out = append(out, token{kind: tkString, val: s, pos: i})
			i += n
		case ch == '@':
			s, n, err := lexMagic(src[i:])
			if err != nil {
				return out, posErr(i, "%s", err.Error())
			}
			out = append(out, token{kind: tkMagic, val: s, pos: i})
			i += n
		case ch >= '0' && ch <= '9':
			s, kind, n := lexNumber(src[i:])
			out = append(out, token{kind: kind, val: s, pos: i})
			i += n
		case isIdentStart(ch):
			s, n := lexIdent(src[i:])
			i += n
			lower := strings.ToLower(s)
			switch lower {
			case "true", "false":
				out = append(out, token{kind: tkBool, val: lower, pos: i - n})
			case "null":
				out = append(out, token{kind: tkNull, val: "null", pos: i - n})
			case "in":
				out = append(out, token{kind: tkIn, val: "IN", pos: i - n})
			case "between":
				out = append(out, token{kind: tkBetween, val: "BETWEEN", pos: i - n})
			case "is":
				out = append(out, token{kind: tkIs, val: "IS", pos: i - n})
			case "not":
				out = append(out, token{kind: tkNot, val: "NOT", pos: i - n})
			default:
				out = append(out, token{kind: tkIdent, val: s, pos: i - n})
			}
		default:
			return out, posErr(i, "unexpected character %q", string(rune(ch)))
		}
	}
	out = append(out, token{kind: tkEOF, pos: i})
	return out, nil
}

// lexString consumes 'literal' from the head of src. Supports the
// two-character escapes \' and \\; everything else is taken
// verbatim. Returns (decoded value, byte length, error).
func lexString(src string) (string, int, error) {
	if len(src) == 0 || src[0] != '\'' {
		return "", 0, fmt.Errorf("expected '")
	}
	var b strings.Builder
	i := 1
	for i < len(src) {
		ch := src[i]
		if ch == '\\' {
			if i+1 >= len(src) {
				return "", 0, fmt.Errorf("dangling backslash in string")
			}
			esc := src[i+1]
			switch esc {
			case '\'':
				b.WriteByte('\'')
			case '\\':
				b.WriteByte('\\')
			default:
				return "", 0, fmt.Errorf("unknown escape \\%c", esc)
			}
			i += 2
			continue
		}
		if ch == '\'' {
			return b.String(), i + 1, nil
		}
		b.WriteByte(ch)
		i++
	}
	return "", 0, fmt.Errorf("unterminated string literal")
}

// lexMagic consumes a magic var: @ followed by `request.auth.id`,
// `request.auth.collectionName`, or `me`. The full set is
// closed-membership; anything else is a parser error so we don't
// accidentally accept `@anything`.
func lexMagic(src string) (string, int, error) {
	if len(src) == 0 || src[0] != '@' {
		return "", 0, fmt.Errorf("expected @")
	}
	// Eat all identifier/dot characters then validate against the
	// whitelist below. Lexer is strict — keeps the parser tidy.
	i := 1
	for i < len(src) {
		ch := src[i]
		if ch == '.' || isIdentCont(ch) {
			i++
			continue
		}
		break
	}
	name := src[:i]
	switch name {
	case "@request.auth.id",
		"@request.auth.collectionName",
		"@me":
		return name, i, nil
	}
	return "", 0, fmt.Errorf("unknown magic var %q (allowed: @request.auth.id, @me, @request.auth.collectionName)", name)
}

// lexNumber consumes one numeric literal. Returns kind=tkInt or tkFloat.
func lexNumber(src string) (string, tokenKind, int) {
	i := 0
	for i < len(src) && src[i] >= '0' && src[i] <= '9' {
		i++
	}
	if i < len(src) && src[i] == '.' {
		i++
		for i < len(src) && src[i] >= '0' && src[i] <= '9' {
			i++
		}
		return src[:i], tkFloat, i
	}
	return src[:i], tkInt, i
}

// lexIdent consumes one identifier. Caller decides keyword vs ident
// from the lower-cased string.
func lexIdent(src string) (string, int) {
	i := 0
	for i < len(src) && isIdentCont(src[i]) {
		i++
	}
	return src[:i], i
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentCont(b byte) bool {
	if isIdentStart(b) {
		return true
	}
	return b >= '0' && b <= '9'
}

// posErr is a small helper so error messages always carry a 0-based
// byte position. Surfaces in HTTP 400 responses as `details.position`.
func posErr(pos int, format string, args ...any) error {
	return &PositionedError{
		Position: pos,
		Message:  fmt.Sprintf(format, args...),
	}
}

// PositionedError pairs a parse error with its byte offset in the
// source string. The HTTP layer surfaces the offset to clients so
// editors can underline the bad span.
type PositionedError struct {
	Position int
	Message  string
}

func (e *PositionedError) Error() string {
	return fmt.Sprintf("filter: at position %d: %s", e.Position, e.Message)
}

// uniSafe truncates src for error messages so we never echo MB-sized
// inputs back. Used by parser when blaming a token for a problem.
func uniSafe(src string) string {
	const max = 80
	r := []rune(src)
	if len(r) <= max {
		return src
	}
	return string(r[:max]) + "…"
}

// Compile-time assertion that the input fits Postgres identifier
// rules where it matters. Lexer accepts any letters/digits/underscore;
// validation against the schema lives in the SQL emitter.
var _ = unicode.IsLetter
