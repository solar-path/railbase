// User-facing profile + password management — Sprint 2 of the
// account-page roadmap.
//
//   PATCH /api/auth/me              — update arbitrary user-defined fields
//   POST  /api/auth/change-password — current + new (+ auto-revoke other sessions)
//
// Both endpoints are global (no /{name} segment); the principal carries
// the collection. Both are gated by the auth middleware — PrincipalFrom
// returning zero is the canonical 401 path.
//
// Field-update policy for PATCH /me:
//   - System columns (`id`, `email`, `verified`, `password_hash`,
//     `token_key`, `created`, `updated`, `last_login_at`) are NEVER
//     accepted. Email change has its own request/confirm flow with
//     email verification; password change has its own endpoint;
//     everything else is internal bookkeeping.
//   - TypePassword fields are NEVER accepted (use change-password).
//   - TypeFiles/TypeFile/TypeRelations are accepted only via their
//     own dedicated endpoints (out of scope for v0.4.3). PATCH /me
//     rejects them with a clear error.
//   - Everything else (TypeText, TypeNumber, TypeBool, TypeSelect,
//     TypeJSON, etc.) is updated verbatim — the schema-emitted CHECK
//     constraints + NOT NULL guard the database integrity.
package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/password"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// patchMeHandler updates whitelisted fields on the caller's auth-row.
// Returns the refreshed record on 200, mirroring meHandler's shape so
// the UI can swap state in one line: `setMe(await rb.account.update(...))`.
func (d *Deps) patchMeHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	coll := registry.Get(p.CollectionName)
	if coll == nil {
		// Shouldn't happen — principal collection was valid at signin.
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "auth collection missing from registry"))
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if len(body) == 0 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "request body is empty — nothing to update"))
		return
	}

	updates, err := buildProfileUpdates(coll.Spec(), body)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if len(updates) == 0 {
		// All keys were rejected — fail with a constructive error so
		// the caller knows none of their fields landed.
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"no updatable fields supplied (email/password/system fields are managed via their dedicated endpoints)"))
		return
	}

	// Build the dynamic UPDATE. Keys are pulled from the schema, not
	// the user body — no SQL injection surface even though we string-
	// concat field names.
	setClauses := make([]string, 0, len(updates))
	args := make([]any, 0, len(updates)+1)
	keys := sortedKeys(updates)
	for i, k := range keys {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", k, i+1))
		args = append(args, updates[k])
	}
	args = append(args, p.UserID)
	q := fmt.Sprintf(`UPDATE %s SET %s, updated = now() WHERE id = $%d`,
		p.CollectionName, strings.Join(setClauses, ", "), len(args))
	if _, err := d.Pool.Exec(r.Context(), q, args...); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update profile failed"))
		return
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record failed"))
		return
	}
	rec := authRecordJSON(row, p.CollectionName)
	// Surface the updated user-defined fields too — authRecordJSON only
	// emits system fields by design. We pull the fresh values from the
	// `updates` map (server-coerced) so the client always sees what got
	// stored, not what they sent.
	for k, v := range updates {
		rec[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"record": rec})
}

// buildProfileUpdates filters and type-coerces the request body against
// the auth collection's field whitelist. Returns the cleaned map
// suitable for direct UPDATE parameter binding, or a validation error
// naming the offending key.
//
// Rejection rules (any one trips a 400):
//   - Field doesn't exist on the spec
//   - Field is a system column (email/password_hash/token_key/verified/
//     last_login_at/id/created/updated)
//   - Field is TypePassword (separate endpoint)
//   - Field is TypeFiles/TypeFile/TypeRelations (separate endpoints)
//
// On accept, JSON values are passed through to pgx — pg's driver
// handles maps and slices natively for JSONB columns, time.Time for
// dates, etc. The schema's CHECK / NOT NULL constraints enforce the
// rest at the DB layer.
func buildProfileUpdates(spec builder.CollectionSpec, body map[string]any) (map[string]any, error) {
	updates := make(map[string]any, len(body))
	for key, val := range body {
		// System columns — always rejected.
		if isReservedProfileField(key, spec) {
			return nil, fmt.Errorf("field %q cannot be updated via PATCH /api/auth/me (system or credential field)", key)
		}
		// Find the field on the spec.
		var f builder.FieldSpec
		found := false
		for _, sf := range spec.Fields {
			if sf.Name == key {
				f = sf
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown field %q on collection %q", key, spec.Name)
		}
		switch f.Type {
		case builder.TypePassword:
			return nil, fmt.Errorf("field %q is a password — use POST /api/auth/change-password instead", key)
		case builder.TypeFiles, builder.TypeFile:
			return nil, fmt.Errorf("field %q is a file/files type — use the dedicated upload endpoint", key)
		case builder.TypeRelations:
			return nil, fmt.Errorf("field %q is a many-to-many relation — managed via /api/collections/%s/records/{id} with relations payload", key, spec.Name)
		}
		updates[key] = val
	}
	return updates, nil
}

// isReservedProfileField says whether `name` is a column the user must
// never set via PATCH /me. Auth-system columns are hard-coded; the
// `id`/`created`/`updated` triple is universal.
func isReservedProfileField(name string, _ builder.CollectionSpec) bool {
	switch name {
	case "id", "created", "updated":
		return true
	case "email", "verified", "password_hash", "token_key", "last_login_at":
		return true
	}
	return false
}

// sortedKeys returns map keys in alphabetical order. Deterministic key
// order keeps the UPDATE SQL stable across builds (matters for query
// plan caching + test assertions).
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- change-password ----------------------------------------------

// changePasswordHandler verifies the caller's CURRENT password, hashes
// the NEW one, and (security-critical) revokes every OTHER session so
// a stolen device can't continue using a previously-issued token.
//
// The current session is preserved — the caller who just changed
// their password shouldn't be 401'd back to login.
func (d *Deps) changePasswordHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		PasswordConfirm string `json:"passwordConfirm"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if body.CurrentPassword == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "current_password required"))
		return
	}
	if len(body.NewPassword) < 8 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "new_password must be at least 8 chars"))
		return
	}
	if body.PasswordConfirm != "" && body.PasswordConfirm != body.NewPassword {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "passwordConfirm does not match new_password"))
		return
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record failed"))
		return
	}
	if err := password.Verify(body.CurrentPassword, row.PasswordHash); err != nil {
		// 401, not 422 — same posture as signin's wrong-password.
		// Don't reveal whether the user exists; this row already does.
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "current password is incorrect"))
		return
	}
	newHash, err := password.Hash(body.NewPassword)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "hash failed"))
		return
	}
	if _, err := d.Pool.Exec(r.Context(),
		fmt.Sprintf(`UPDATE %s SET password_hash = $1, updated = now() WHERE id = $2`, p.CollectionName),
		newHash, p.UserID); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update password failed"))
		return
	}
	// Security: revoke every other live session. The hostile-actor
	// scenario this defends: phone stolen, attacker holds bearer, owner
	// changes password from laptop — without this revoke, the phone's
	// token keeps working until expiry.
	if d.Sessions != nil {
		if _, err := d.Sessions.RevokeOthers(r.Context(), p.CollectionName, p.UserID, p.SessionID); err != nil {
			// Log but don't fail the response — the password is already
			// changed; failing here would surface a confusing "did my
			// password change?" UX.
			d.Log.Warn("change-password: revoke-others failed",
				"user_id", p.UserID, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
