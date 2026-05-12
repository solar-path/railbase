package filter

import (
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// SortKey is one parsed `?sort=` entry.
type SortKey struct {
	Field string
	Desc  bool
}

// SQL returns the column reference + direction. Quotes nothing —
// validated against the registry, so identifier injection is impossible.
func (k SortKey) SQL() string {
	dir := "ASC"
	if k.Desc {
		dir = "DESC"
	}
	return fmt.Sprintf("%s %s", k.Field, dir)
}

// ParseSort splits a comma-separated `?sort=` value into typed keys.
// Empty input returns nil — caller falls back to the default sort.
//
// Each entry may carry an optional `+` (asc, default) or `-` (desc)
// prefix:
//
//	?sort=created           → ASC
//	?sort=+created          → ASC
//	?sort=-created          → DESC
//	?sort=-status,+created  → multi-column
//
// Field names are validated against the spec; sortable types
// exclude json/files/relations — same set columnAllowed rejects in
// the WHERE compiler.
func ParseSort(raw string, spec builder.CollectionSpec) ([]SortKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var keys []SortKey
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		k := SortKey{}
		switch p[0] {
		case '+':
			p = p[1:]
		case '-':
			k.Desc = true
			p = p[1:]
		}
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("sort: empty field name")
		}
		if !columnAllowed(spec, p) {
			return nil, fmt.Errorf("sort: field %q is not sortable on collection %q", p, spec.Name)
		}
		k.Field = p
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return keys, nil
}

// JoinSQL renders a slice of SortKey as the body of an ORDER BY
// clause. Returns "" when keys is empty so the caller can splice it
// straight into a query.
func JoinSQL(keys []SortKey) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k.SQL()
	}
	return strings.Join(parts, ", ")
}
