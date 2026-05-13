package scim

// Optimistic-concurrency support per RFC 7644 §3.7 + RFC 7232.
//
// We emit WEAK ETags (`W/"<int>"`) where the int is the
// milliseconds-since-epoch value of the resource's `updated` (User)
// or `updated_at` (Group) column. Weak is sufficient for SCIM's
// "did this row change?" question — RFC 7232 says weak ETags compare
// by exact byte-equality, which matches our value-identity goal:
// two snapshots with identical mtime are considered identical.
//
// Why milliseconds, not seconds? Real-world IdPs do round-trips fast
// enough that two PATCHes can complete in the same wall-second; a
// second-resolution ETag would erroneously claim "no change" between
// them. The Postgres TIMESTAMPTZ has microsecond resolution; rounding
// to ms balances resolution against ETag string length.
//
// We deliberately do NOT use a content-hash ETag (sha256 of the
// rendered resource): the hash would change spuriously when the IdP
// upgrades and adds a new SCIM attribute we render with a default
// value, breaking If-None-Match caching for no semantic change.

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// etagFor returns the canonical weak-ETag string for a SCIM resource
// whose mtime is `updated`. Zero time → empty string; callers should
// elide the ETag header when this returns "" (NULL `updated` in DB
// is the only case that triggers this; production rows always have
// a non-zero mtime via the DEFAULT now() on insert).
func etagFor(updated time.Time) string {
	if updated.IsZero() {
		return ""
	}
	ms := updated.UnixMilli()
	return `W/"` + strconv.FormatInt(ms, 10) + `"`
}

// setETag adds the ETag response header. No-op when `tag` is empty.
func setETag(w http.ResponseWriter, tag string) {
	if tag == "" {
		return
	}
	w.Header().Set("ETag", tag)
}

// checkIfNoneMatch returns true when the request's `If-None-Match`
// header includes `tag` (weak comparison per RFC 7232 §2.3.2 — weak
// ETags compare equal iff their opaque strings are byte-equal). A
// match means the caller already has this version; the handler
// should respond 304 Not Modified with no body.
//
// The wildcard `*` matches any current representation (RFC 7232
// §3.2): when `tag` is non-empty (resource exists), `*` means
// "match". Empty `tag` (NULL mtime) → no match for `*` either.
//
// Multiple ETags in one If-None-Match are comma-separated; any
// match → 304.
func checkIfNoneMatch(r *http.Request, tag string) bool {
	if tag == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if header == "" {
		return false
	}
	for _, candidate := range splitETagList(header) {
		if candidate == "*" {
			return true
		}
		if candidate == tag {
			return true
		}
	}
	return false
}

// checkIfMatch returns true when the request's `If-Match` precondition
// is satisfied — i.e. either the header is absent (no precondition)
// OR it lists a value matching `tag` (or wildcard `*`).
//
// When `tag` is empty (NULL mtime) we still honour `*` per RFC 7232
// — the resource exists, just without a known version. Other
// concrete ETags do not match an empty tag.
//
// SECURITY: when the header IS present and we cannot satisfy it, the
// caller MUST emit 412 Precondition Failed (RFC 7232 §4.2) — not
// silently proceed. The SCIM-error envelope is constructed by the
// handler.
func checkIfMatch(r *http.Request, tag string) (present bool, ok bool) {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	if header == "" {
		return false, true // no precondition
	}
	present = true
	for _, candidate := range splitETagList(header) {
		if candidate == "*" {
			// `*` matches "any current representation" — true iff the
			// resource exists. The handler establishes existence by
			// loading first; if we got here with `*`, the row exists.
			return true, true
		}
		if tag != "" && candidate == tag {
			return true, true
		}
	}
	return true, false
}

// splitETagList splits an If-Match / If-None-Match header into its
// individual ETag tokens. Spaces around commas are tolerated; quoted
// strings are NOT comma-split (an unescaped comma inside the opaque
// value would be malformed per the grammar, but we still preserve
// it as part of the token rather than panic).
func splitETagList(header string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(header); i++ {
		c := header[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

// writePreconditionFailed emits a 412 with the SCIM-conformant error
// envelope. The IdP retries with a fresh GET → fresh ETag → fresh PUT.
func writePreconditionFailed(w http.ResponseWriter, detail string) {
	WriteError(w, http.StatusPreconditionFailed, detail)
}
