package scim

// Sort-parameter handling for SCIM 2.0 list endpoints
// (RFC 7644 §3.4.2.3).
//
// SCIM permits `?sortBy=<attribute>&sortOrder=ascending|descending`.
// We expose a closed whitelist of sortable attributes per resource:
// unknown attributes return 400 (rather than silently falling back to
// `ORDER BY created`) — that matches the filter engine's posture and
// avoids the IdP shipping us a sortBy we silently ignored. Both Okta
// and Entra ID handle 400 gracefully.
//
// `sortOrder` without `sortBy` is ignored (spec language: "the
// default order is ascending"; without a sort attribute there's
// nothing to reverse). This matches AWS Cognito + most SCIM
// implementations.

import (
	"net/http"
	"strings"
)

// sortClause returns a `<col> ASC|DESC` fragment plus an `ok` flag.
// When `sortBy` is empty in the request, returns ("", true) — the
// caller should use its default ORDER BY. When `sortBy` is set but
// not in the whitelist, returns ("", false) — the caller MUST emit
// 400 InvalidValue.
//
// `cols` maps lowercased SCIM attribute paths → SQL column expressions.
// Caller controls the mapping so different resource types (Users vs
// Groups) can expose different sortable attributes.
func sortClause(r *http.Request, cols map[string]string) (string, bool) {
	q := r.URL.Query()
	sortBy := strings.TrimSpace(q.Get("sortBy"))
	if sortBy == "" {
		return "", true
	}
	col, ok := cols[strings.ToLower(sortBy)]
	if !ok {
		return "", false
	}
	order := strings.ToLower(strings.TrimSpace(q.Get("sortOrder")))
	dir := "ASC"
	if order == "descending" || order == "desc" {
		dir = "DESC"
	}
	return col + " " + dir, true
}

// userSortColumns is the whitelist of SCIM attribute paths sortable
// on /Users. Maps to the underlying SQL columns / expressions. Keys
// MUST be lowercased.
//
// `emails.value` is intentionally aliased to `lower(email)` — we
// store the primary email denormalised on the user row, and the SCIM
// `emails` attribute is multi-valued in the wire schema but
// effectively single-valued in our store. Sorting by `emails.value`
// at the deep level (lowest-email-of-array) is more SQL than the
// implementation latitude SCIM grants is worth.
var userSortColumns = map[string]string{
	"username":          "lower(email)",
	"emails.value":      "lower(email)",
	"id":                "id",
	"externalid":        "external_id",
	"meta.created":      "created",
	"meta.lastmodified": "updated",
}

// groupSortColumns is the same idea, scoped to Group attributes.
var groupSortColumns = map[string]string{
	"displayname":       "lower(display_name)",
	"id":                "id",
	"externalid":        "external_id",
	"meta.created":      "created_at",
	"meta.lastmodified": "updated_at",
}
