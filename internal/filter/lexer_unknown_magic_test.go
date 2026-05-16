// Regression test for FEEDBACK #B3 — the lexer's "unknown magic var"
// error now reaches the embedder via the REST response (the rest
// handlers wrap with `%v` instead of swallowing the cause). The
// underlying lexer error includes:
//   - the bad token verbatim ("@request.auth.collection")
//   - the byte-offset position
//   - the allowed alternatives (@request.auth.id, @me,
//     @request.auth.collectionName)
//
// The blogger project hit this exactly: wrote
// `@request.auth.collection` (singular) in a ListRule, got a 500 with
// `{"error":{"code":"internal","message":"rule compile failed"}}` —
// no hint as to which token, which position, or what the right name
// was. With the FEEDBACK #B3 wrap fix the message now reads
// `rule compile failed: filter: at position N: unknown magic var
// "@request.auth.collection" (allowed: ...)`.
package filter

import (
	"errors"
	"strings"
	"testing"
)

func TestLex_UnknownMagicVar_ProducesPositionedError(t *testing.T) {
	_, err := lex(`@request.auth.collection = 'authors'`)
	if err == nil {
		t.Fatalf("expected lex error, got nil")
	}
	var pe *PositionedError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PositionedError, got %T: %v", err, err)
	}
	if pe.Position != 0 {
		t.Errorf("expected position 0 (the @), got %d", pe.Position)
	}
	if !strings.Contains(pe.Message, "@request.auth.collection") {
		t.Errorf("error must echo the bad token, got: %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "@request.auth.collectionName") {
		t.Errorf("error must list the correct alternative, got: %q", pe.Message)
	}
}

func TestLex_UnknownMagicVar_ErrorStringHasPosition(t *testing.T) {
	// Embed the bad token at offset 13 (the leading `id = 5 && `).
	src := `id = 5 && @request.auth.collection = 'x'`
	_, err := lex(src)
	if err == nil {
		t.Fatalf("expected error")
	}
	msg := err.Error()
	// The Error() string must mention "position" so an embedder reading
	// the JSON response can localise to the offending substring.
	if !strings.Contains(msg, "position") {
		t.Errorf("error string must include position info, got: %q", msg)
	}
}

func TestLex_CorrectMagicVar_NoError(t *testing.T) {
	_, err := lex(`@request.auth.collectionName = 'authors'`)
	if err != nil {
		t.Errorf("valid magic var should lex cleanly, got: %v", err)
	}
}
