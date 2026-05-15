package security

import (
	neturl "net/url"
	"strings"
)

// RedactDSN strips the password from a postgres:// (or any URL-form)
// DSN, replacing it with "***". Best-effort — falls back to the raw
// string when the URL parser can't make sense of it. Never panics.
//
// Used at every site that logs a DSN OR echoes one back to the
// operator (setup-wizard probe response, error messages). Centralised
// here so adding a new logging site doesn't risk re-leaking the
// password — `import "internal/security".RedactDSN` is the single
// point of trust.
func RedactDSN(dsn string) string {
	u, err := neturl.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, hasPw := u.User.Password(); !hasPw {
		return dsn
	}
	u.User = neturl.UserPassword(u.User.Username(), "***")
	// neturl.URL.String() percent-encodes "*" in the userinfo segment
	// to "%2A". That's correct for a real URL, but operators reading
	// a log line want the literal asterisks. Reverse the encoding for
	// our sentinel only — the rest of the URL keeps its escaping.
	return strings.Replace(u.String(), "%2A%2A%2A", "***", 1)
}

// sensitiveKeys is the exact (lowercased) JSON field names whose value
// is treated as a credential and replaced with [REDACTED] by callers
// like audit's redactJSON. The IsSensitiveKey suffix rules below catch
// the broader credential families (`*_password`, `*_secret`, `*_token`)
// so this list only needs to enumerate the names that don't fit those
// patterns plus any whose family suffix would over-match.
var sensitiveKeys = map[string]struct{}{
	// password family — bare names; "*_password" handled by suffix
	"password":      {},
	"password_hash": {},
	// secret family — bare names; "*_secret" handled by suffix
	"secret":     {},
	"secret_key": {},
	// token family — bare names; "*_token" handled by suffix
	"token":     {},
	"token_key": {},
	// API + transport credentials
	"api_key":           {},
	"apikey":            {},
	"private_key":       {},
	"signing_key":       {},
	"encryption_key":    {},
	"secret_access_key": {},
	"authorization":     {},
	"auth_header":       {},
	"cookie":            {},
	"set_cookie":        {},
	"bearer":            {},
	// Whole DSN / connection strings are sensitive
	"dsn":               {},
	"database_url":      {},
	"connection_string": {},
	"conn_string":       {},
	// Session-derived secrets
	"session_id":    {},
	"session":       {},
}

// IsSensitiveKey reports whether `key` (a JSON object field name)
// names a value that must NEVER be persisted or logged in clear.
// Used by audit's payload redaction; safe to reuse anywhere a generic
// key/value structure needs filtering before serialisation.
//
// The match is case-insensitive and combines two rules:
//
//  1. The exact-key allow-list in `sensitiveKeys` above — the names
//     that don't fit any broader pattern.
//
//  2. Suffix rules — anything ending in `password`, `secret`, or
//     `token` is treated as a credential. This catches both
//     snake_case (`client_secret`, `bind_password`, `csrf_token`)
//     and camelCase (`clientSecret`, `accessToken`) variants without
//     having to enumerate every spelling.
//
// The suffixes are picked so they DON'T match metadata fields like
// `password_reset_at` (timestamp — ends in `_at`, not `password`),
// `token_id` (ID of a token row — ends in `_id`), or `secret_metadata`
// (ends in `_metadata`). False positives don't leak; false negatives
// do — so when in doubt, add to the exact list.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if _, ok := sensitiveKeys[k]; ok {
		return true
	}
	return strings.HasSuffix(k, "password") ||
		strings.HasSuffix(k, "secret") ||
		strings.HasSuffix(k, "token")
}
