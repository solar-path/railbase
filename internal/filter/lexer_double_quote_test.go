package filter

// FEEDBACK loadtest #6 — lexer accepts both '...' and "..." string
// literals. Docs commonly show double-quote form; tooling that emits
// SQL-style strings was previously rejected at position 20.

import "testing"

func TestLexer_DoubleQuoteString(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"single-quote", "@request.auth.id != ''"},
		{"double-quote", `@request.auth.id != ""`},
		{"single-quoted value", "name = 'alice'"},
		{"double-quoted value", `name = "alice"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tokens, err := lex(c.in)
			if err != nil {
				t.Fatalf("lex error: %v", err)
			}
			if len(tokens) == 0 {
				t.Fatal("expected tokens, got 0")
			}
		})
	}
}

func TestLexStringQuoted_RoundTrip(t *testing.T) {
	v, n, err := lexStringQuoted(`"hello world"`, '"')
	if err != nil {
		t.Fatal(err)
	}
	if v != "hello world" {
		t.Errorf("decoded value: got %q, want %q", v, "hello world")
	}
	if n != 13 {
		t.Errorf("length: got %d, want 13", n)
	}
}
