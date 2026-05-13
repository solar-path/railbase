// Package scim implements the inbound HTTP surface for SCIM 2.0
// provisioning (RFC 7644). Routes are mounted at /scim/v2/* by app.go.
//
// Authentication: every request must carry an
// `Authorization: Bearer rbsm_<token>` header. The middleware
// resolves the token against the SCIM token store and attaches the
// resolved Token to the request context. Routes pull it back out via
// TokenFromContext to know which collection to operate on.
package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
)

// scimErrorResponse is the SCIM-conformant error envelope per
// RFC 7644 §3.12. `schemas` is the constant SCIM error schema URI;
// `detail` is the human-readable message; `status` is the HTTP code
// stringified.
type scimErrorResponse struct {
	Schemas []string `json:"schemas"`
	Detail  string   `json:"detail"`
	Status  string   `json:"status"`
}

const errorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"

// WriteError emits a SCIM error response. Sets Content-Type +
// application/scim+json which some IdPs (Okta strict) check.
func WriteError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(scimErrorResponse{
		Schemas: []string{errorSchema},
		Detail:  detail,
		Status:  http.StatusText(status),
	})
}

// writeJSON emits an arbitrary SCIM resource body w/ the scim+json
// content type. Status defaults to 200; pass 201 for resource
// creation, 204 for delete.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type ctxKey int

const tokenCtxKey ctxKey = 1

// TokenFromContext recovers the authenticated SCIM token. Returns nil
// if the middleware hasn't run (test paths).
func TokenFromContext(ctx context.Context) *scimauth.Token {
	t, _ := ctx.Value(tokenCtxKey).(*scimauth.Token)
	return t
}

// AuthMiddleware validates `Authorization: Bearer rbsm_...` against
// the SCIM token store. Routes mounted under this middleware are
// guaranteed to find a non-nil Token in context.
func AuthMiddleware(tokens *scimauth.TokenStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokens == nil {
				WriteError(w, http.StatusServiceUnavailable, "SCIM not configured")
				return
			}
			authz := r.Header.Get("Authorization")
			if !strings.HasPrefix(authz, "Bearer ") {
				WriteError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
				return
			}
			raw := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
			if !strings.HasPrefix(raw, scimauth.TokenPrefix) {
				WriteError(w, http.StatusUnauthorized, "token does not look like a SCIM credential (expected rbsm_ prefix)")
				return
			}
			tok, err := tokens.Authenticate(r.Context(), raw)
			if err != nil {
				WriteError(w, http.StatusUnauthorized, "invalid SCIM credential")
				return
			}
			ctx := context.WithValue(r.Context(), tokenCtxKey, tok)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
