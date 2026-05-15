// Pure unit tests for slug derivation + validation. No DB needed —
// these are the validators the e2e test rides on top of, kept fast
// so they run on every `go test ./...` without the embed_pg gate.
package tenants

import "testing"

func TestDeriveSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Acme Corp", "acme-corp"},
		{"  Padding  ", "padding"},
		{"Hello, World!", "hello-world"},
		{"A B  C", "a-b-c"},   // collapse runs of separators
		{"-leading-trailing-", "leading-trailing"},
		{"MiXeD CaSe", "mixed-case"},
		{"emoji-✨-only", "emoji-only"},
		{"a", "a-workspace"}, // single char padded to satisfy len>=3
	}
	for _, tc := range cases {
		got := deriveSlug(tc.in)
		if got != tc.want {
			t.Errorf("deriveSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
		// Every derived slug MUST match the regex — the whole point of
		// the helper is that the auto-form passes validation. (Truly
		// empty input is the one case operators must supply for.)
		if !slugRE.MatchString(got) {
			t.Errorf("deriveSlug(%q) = %q does not match slugRE", tc.in, got)
		}
	}
}

func TestSlugRE_Rejects(t *testing.T) {
	bad := []string{
		"",                         // empty
		"a",                        // too short
		"ab",                       // too short
		"-abc",                     // leading hyphen
		"abc-",                     // trailing hyphen
		"AbCd",                     // uppercase
		"abc!def",                  // punctuation
		"abc def",                  // space
		"abc_def",                  // underscore
	}
	for _, s := range bad {
		if slugRE.MatchString(s) {
			t.Errorf("slugRE accepted %q (should reject)", s)
		}
	}
	ok := []string{
		"abc",
		"a1b",
		"a-b-c",
		"acme-corp",
		"workspace-123",
	}
	for _, s := range ok {
		if !slugRE.MatchString(s) {
			t.Errorf("slugRE rejected %q (should accept)", s)
		}
	}
}
