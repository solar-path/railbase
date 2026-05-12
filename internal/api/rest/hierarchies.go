package rest

import (
	"fmt"
	"sort"
	"strings"
)

// normaliseTags accepts an array of strings and returns the canonical
// tag set: each tag trimmed + lowercased, deduplicated, optionally
// length-capped per tag, optionally count-capped overall. Returns
// `[]string` so pgx encodes as TEXT[].
//
// Accepted inputs:
//
//   - `[]any{"foo", "bar"}` from JSON array decode.
//   - `[]string{"foo", "bar"}` from typed callers.
//
// Empty array allowed (results in an empty TEXT[]). Per-tag rules:
//   - Trim leading/trailing whitespace.
//   - Lowercase ASCII (A-Z → a-z).
//   - Reject empty-after-trim.
//   - Reject tags exceeding `perTagMax` (when > 0).
//   - Reject if total cardinality exceeds `totalMax` (when > 0).
func normaliseTags(v any, perTagMax, totalMax int) ([]string, error) {
	var raw []any
	switch t := v.(type) {
	case []any:
		raw = t
	case []string:
		raw = make([]any, len(t))
		for i, s := range t {
			raw[i] = s
		}
	case nil:
		return []string{}, nil
	default:
		return nil, fmt.Errorf("expected array of strings, got %T", v)
	}

	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for i, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("tag at index %d: expected string, got %T", i, item)
		}
		s = strings.TrimSpace(s)
		// Lowercase ASCII (cheap, no allocs beyond the input).
		b := []byte(s)
		for j, c := range b {
			if c >= 'A' && c <= 'Z' {
				b[j] = c + 32
			}
		}
		s = string(b)
		if s == "" {
			return nil, fmt.Errorf("tag at index %d: empty after trim", i)
		}
		if perTagMax > 0 && len(s) > perTagMax {
			return nil, fmt.Errorf("tag %q exceeds max length %d", s, perTagMax)
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if totalMax > 0 && len(out) > totalMax {
		return nil, fmt.Errorf("tag set has %d items, max is %d", len(out), totalMax)
	}
	// Stable sort so equal inputs yield equal column values — easier
	// snapshot testing, deduplication in caches.
	sort.Strings(out)
	return out, nil
}

// normaliseTreePath validates an LTREE-compatible path string. Postgres
// ltree accepts labels matching `[A-Za-z0-9_]+` (up to 256 chars each)
// separated by dots, total path up to 65535 chars. Empty path ("")
// is the root and is allowed.
//
// Returns the string unchanged when valid — case is significant in
// ltree so we don't lowercase. Operators who want case-insensitive
// hierarchies should normalise client-side or via hooks.
func normaliseTreePath(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		// Empty path = ltree root. Postgres accepts it.
		return s, nil
	}
	if len(s) > 65535 {
		return "", fmt.Errorf("ltree path too long (%d chars, max 65535)", len(s))
	}
	// Split on dots and validate each label.
	labels := strings.Split(s, ".")
	for i, label := range labels {
		if label == "" {
			return "", fmt.Errorf("ltree path has empty label at position %d (consecutive dots or leading/trailing dot)", i)
		}
		if len(label) > 256 {
			return "", fmt.Errorf("ltree label %q at position %d exceeds 256 chars", label, i)
		}
		for j := 0; j < len(label); j++ {
			c := label[j]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '_') {
				return "", fmt.Errorf("ltree label %q has invalid character %q (only [A-Za-z0-9_] allowed)", label, c)
			}
		}
	}
	return s, nil
}
