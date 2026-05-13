package scim

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/password"
	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/settings"
)

// scimUserSchema is the canonical SCIM 2.0 User resource schema URI.
const scimUserSchema = "urn:ietf:params:scim:schemas:core:2.0:User"

// scimUser is the wire-format representation of a SCIM User resource
// per RFC 7643 §4.1. We model only the fields IdPs actually emit;
// the spec defines a much larger surface (addresses, phoneNumbers,
// photos, x509Certificates, locale, timezone, …) but real Okta /
// Azure AD traffic uses the subset below. Add fields lazily as IdP
// reports demand.
type scimUser struct {
	Schemas    []string  `json:"schemas"`
	ID         string    `json:"id"`
	ExternalID string    `json:"externalId,omitempty"`
	UserName   string    `json:"userName"`
	Name       *scimName `json:"name,omitempty"`
	Active     bool      `json:"active"`
	Emails     []scimEmail `json:"emails,omitempty"`
	Meta       scimMeta  `json:"meta"`
}

type scimName struct {
	Formatted  string `json:"formatted,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
}

type scimEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type,omitempty"`
}

type scimMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created"`
	LastModified string `json:"lastModified"`
	Location     string `json:"location,omitempty"`
	Version      string `json:"version,omitempty"`
}

// scimListResponse is the SCIM 2.0 list-results envelope.
type scimListResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []any    `json:"Resources"`
}

const listResponseSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"

// userColumns maps SCIM filter paths to SQL columns. The set is
// closed: any unmapped attribute returns a 400 from the filter
// engine. This is intentional — IdPs can't probe DB columns by
// guessing attribute names.
var userColumns = scimauth.ColumnMap{
	"username":     "lower(email)",
	"externalid":   "external_id",
	"active":       "verified",
	"emails.value": "lower(email)",
	"email":        "lower(email)",
	"id":           "id::text",
	"meta.created": "created",
}

// UsersDeps is what the Users handler needs at construction.
//
// Settings + RBAC are OPTIONAL (nil-tolerant). When non-nil they enable
// the `auth.scim.soft_delete` toggle path in deleteUser:
//
//   - Settings: read the boolean `auth.scim.soft_delete` toggle. When
//     unset or false (default), deleteUser performs a physical DELETE
//     — backward-compat with v1.7.51 wire behaviour.
//   - RBAC: when a SCIM user is deleted (soft OR hard), every role
//     they held via `_scim_group_role_map` is revoked. nil RBAC =
//     reconciliation no-ops (same contract as patchGroup).
//
// Mount.go's Mount() does NOT pass these fields today — Settings/RBAC
// wiring at the package boundary is a separate slice. Tests construct
// UsersDeps directly to exercise the toggle.
type UsersDeps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Manager
	RBAC     *rbac.Store
}

// MountUsers registers the /scim/v2/Users routes onto `r`. Caller
// MUST wrap with the AuthMiddleware first; the handlers assume a
// non-nil Token in context.
func MountUsers(r chi.Router, d *UsersDeps) {
	r.Get("/Users", d.listUsers)
	r.Post("/Users", d.createUser)
	r.Get("/Users/{id}", d.getUser)
	r.Put("/Users/{id}", d.replaceUser)
	r.Patch("/Users/{id}", d.patchUser)
	r.Delete("/Users/{id}", d.deleteUser)
}

// listUsers — GET /Users?filter=...&startIndex=1&count=100
//
// RFC 7644 §3.4.2 — pagination via startIndex (1-based!) + count.
func (d *UsersDeps) listUsers(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	if tok == nil {
		WriteError(w, http.StatusUnauthorized, "no auth context")
		return
	}
	q := r.URL.Query()

	// Pagination — SCIM uses 1-based startIndex.
	startIndex := 1
	if v := q.Get("startIndex"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			startIndex = n
		}
	}
	count := 100
	if v := q.Get("count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 500 {
			count = n
		}
	}

	// Filter — optional.
	filterFrag := ""
	args := []any{}
	if f := q.Get("filter"); strings.TrimSpace(f) != "" {
		node, err := scimauth.Parse(f)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		frag, fargs, err := scimauth.ToSQL(node, userColumns)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		filterFrag = frag
		args = fargs
	}

	// Always restrict to SCIM-managed + this token's collection. The
	// scim_managed column was added to `users` by migration 0026; for
	// auth-collections that haven't been retro-patched, the column
	// won't exist + the query errors. We treat the missing-column case
	// as "no SCIM users yet" and return an empty list.
	scope := "scim_managed = TRUE"
	if filterFrag != "" {
		scope = scope + " AND (" + filterFrag + ")"
	}

	// RFC 7644 §3.4.2.3 — sorting. `sortBy` whitelisted via
	// userSortColumns; unknown → 400 InvalidValue. When absent, fall
	// back to the historical default (`ORDER BY created ASC`) so list
	// pagination remains deterministic for IdPs that never set sortBy.
	orderBy, ok := sortClause(r, userSortColumns)
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalidValue: sortBy attribute is not sortable")
		return
	}
	if orderBy == "" {
		orderBy = "created ASC"
	}

	totalQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, tok.Collection, scope)
	var total int
	if err := d.Pool.QueryRow(r.Context(), totalQuery, args...).Scan(&total); err != nil {
		WriteError(w, http.StatusInternalServerError, "count users: "+err.Error())
		return
	}

	// Postgres OFFSET is 0-based; SCIM is 1-based. Translate.
	offset := startIndex - 1
	if offset < 0 {
		offset = 0
	}
	rowsArgs := append([]any{}, args...)
	rowsArgs = append(rowsArgs, count, offset)
	rowsQuery := fmt.Sprintf(`
        SELECT id, email, verified, external_id, created, updated
          FROM %s
         WHERE %s
         ORDER BY %s
         LIMIT $%d OFFSET $%d
    `, tok.Collection, scope, orderBy, len(args)+1, len(args)+2)
	rows, err := d.Pool.Query(r.Context(), rowsQuery, rowsArgs...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "list users: "+err.Error())
		return
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		u, _, err := scanUser(rows, tok.Collection, r)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, u)
	}

	writeJSON(w, http.StatusOK, scimListResponse{
		Schemas:      []string{listResponseSchema},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(out),
		Resources:    out,
	})
}

// getUser — GET /Users/{id}
//
// RFC 7644 §3.7 — emits a weak ETag for every successful read +
// honours `If-None-Match`: when the caller's snapshot matches the
// current row, respond 304 with no body so IdPs can short-circuit
// their cache refresh.
func (d *UsersDeps) getUser(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	uid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}
	u, updated, err := loadUser(r.Context(), d.Pool, tok.Collection, uid, r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tag := etagFor(updated)
	setETag(w, tag)
	if checkIfNoneMatch(r, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// createUser — POST /Users.
//
// Spec contract: SCIM client supplies userName + (typically)
// emails[primary].value + externalId + active. We map:
//
//	userName       → `email` column (auth-collections use email as
//	                  the unique identity per v0.3.2)
//	externalId     → `external_id`
//	active         → `verified` (signin requires verified=TRUE)
//	emails[primary] → `email` (overrides userName when present)
//	password       → generated random; SCIM provisioning is for
//	                  account creation by an external IdP. Local
//	                  password auth is disabled for SCIM-provisioned
//	                  users — they sign in via OAuth/SAML.
func (d *UsersDeps) createUser(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	var body scimUser
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	email := pickPrimaryEmail(body)
	if email == "" {
		WriteError(w, http.StatusBadRequest, "userName or emails[primary].value is required")
		return
	}

	// Generate placeholder password. The user will reset / set their
	// own via OAuth/SAML/magic-link.
	var rb [32]byte
	if _, err := rand.Read(rb[:]); err != nil {
		WriteError(w, http.StatusInternalServerError, "rand: "+err.Error())
		return
	}
	hash, err := password.Hash(base64.StdEncoding.EncodeToString(rb[:]))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "hash: "+err.Error())
		return
	}
	var tkb [32]byte
	if _, err := rand.Read(tkb[:]); err != nil {
		WriteError(w, http.StatusInternalServerError, "rand: "+err.Error())
		return
	}
	tokenKey := base64.RawURLEncoding.EncodeToString(tkb[:])

	// SCIM POST semantics + the auth-collection's lower(email) functional
	// UNIQUE index together preclude `ON CONFLICT (email) DO UPDATE`.
	// Instead: try INSERT; on 23505 (unique violation), look up the
	// existing row, mark it SCIM-managed, and respond 200 instead of
	// 201 to signal "found-and-claimed" vs "created".
	id := uuid.Must(uuid.NewV7())
	q := fmt.Sprintf(`
        INSERT INTO %s (id, email, password_hash, verified, token_key,
                        external_id, scim_managed)
        VALUES ($1, $2, $3, $4, $5, $6, TRUE)
        RETURNING id
    `, tok.Collection)
	var newID uuid.UUID
	status := http.StatusCreated
	err = d.Pool.QueryRow(r.Context(), q, id, email, hash, body.Active, tokenKey,
		nullIfEmpty(body.ExternalID),
	).Scan(&newID)
	if err != nil {
		// 23505 = unique violation. The user already exists — promote
		// to SCIM-managed.
		if isUniqueViolation(err) {
			upd := fmt.Sprintf(`
                UPDATE %s
                   SET external_id  = COALESCE($2, external_id),
                       scim_managed = TRUE,
                       verified     = $3,
                       updated      = now()
                 WHERE lower(email) = lower($1)
                RETURNING id
            `, tok.Collection)
			if err2 := d.Pool.QueryRow(r.Context(), upd, email,
				nullIfEmpty(body.ExternalID), body.Active).Scan(&newID); err2 != nil {
				WriteError(w, http.StatusInternalServerError, "claim existing user: "+err2.Error())
				return
			}
			status = http.StatusOK
		} else {
			WriteError(w, http.StatusInternalServerError, "create user: "+err.Error())
			return
		}
	}
	_ = status // captured in writeJSON below
	u, updated, err := loadUser(r.Context(), d.Pool, tok.Collection, newID, r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "post-create load: "+err.Error())
		return
	}
	setETag(w, etagFor(updated))
	writeJSON(w, status, u)
}

// isUniqueViolation reports whether `err` wraps a Postgres 23505 (a
// unique_violation). Used by the SCIM Users POST to detect the
// "this email is already taken" path so we can promote-to-managed.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// pgconn.PgError exposes Code; we string-match to avoid an import
	// cycle through the api package boundary.
	type sqlStateError interface{ SQLState() string }
	if se, ok := err.(sqlStateError); ok {
		return se.SQLState() == "23505"
	}
	// Walk the unwrap chain.
	for inner := err; inner != nil; {
		if se, ok := inner.(sqlStateError); ok {
			return se.SQLState() == "23505"
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := inner.(unwrapper)
		if !ok {
			break
		}
		inner = u.Unwrap()
	}
	return strings.Contains(err.Error(), "23505")
}

// replaceUser — PUT /Users/{id}. Full replacement; the IdP sends the
// new desired state.
//
// RFC 7644 §3.7 — when the caller sends `If-Match: W/"<version>"`, we
// load the current row first + compare ETags; mismatch → 412
// Precondition Failed with a SCIM error envelope. The IdP retries
// with a fresh GET → fresh ETag → fresh PUT. `If-Match: *` always
// matches (means "must exist"). Absent header → no precondition.
func (d *UsersDeps) replaceUser(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	uid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}
	var body scimUser
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	email := pickPrimaryEmail(body)
	if email == "" {
		WriteError(w, http.StatusBadRequest, "userName or emails[primary].value is required")
		return
	}
	// If-Match precondition. We load the row to compute current ETag;
	// the row also doubles as our existence-check (so missing rows
	// surface as 404 not 412, matching RFC 7232 §6).
	if r.Header.Get("If-Match") != "" {
		_, currentMtime, err := loadUser(r.Context(), d.Pool, tok.Collection, uid, r)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				WriteError(w, http.StatusNotFound, "user not found")
				return
			}
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, ok := checkIfMatch(r, etagFor(currentMtime)); !ok {
			writePreconditionFailed(w, "If-Match precondition failed: resource version has changed")
			return
		}
	}
	q := fmt.Sprintf(`
        UPDATE %s
           SET email = $2,
               verified = $3,
               external_id = $4,
               updated = now()
         WHERE id = $1 AND scim_managed = TRUE
        RETURNING id
    `, tok.Collection)
	var got uuid.UUID
	err = d.Pool.QueryRow(r.Context(), q, uid, email, body.Active,
		nullIfEmpty(body.ExternalID),
	).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "replace user: "+err.Error())
		return
	}
	u, updated, err := loadUser(r.Context(), d.Pool, tok.Collection, got, r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setETag(w, etagFor(updated))
	writeJSON(w, http.StatusOK, u)
}

// patchUser — PATCH /Users/{id}. Operation-based partial update
// (RFC 7644 §3.5.2). The IdP sends `Operations: [{op, path, value}]`
// where `op` is add/replace/remove. We support the subset Okta /
// Azure AD actually emit: `replace` for active + email + name,
// `add` for emails, `remove` for emails.
//
// RFC 7644 §3.7 — when the caller sends `If-Match`, we load the row
// first + verify ETag matches before applying the PATCH (412 on
// mismatch, same as PUT).
func (d *UsersDeps) patchUser(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	uid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}
	// If-Match precondition. Same flow as replaceUser — load to
	// compute the current ETag, 404 if missing, 412 if mismatched.
	if r.Header.Get("If-Match") != "" {
		_, currentMtime, err := loadUser(r.Context(), d.Pool, tok.Collection, uid, r)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				WriteError(w, http.StatusNotFound, "user not found")
				return
			}
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, ok := checkIfMatch(r, etagFor(currentMtime)); !ok {
			writePreconditionFailed(w, "If-Match precondition failed: resource version has changed")
			return
		}
	}
	var body struct {
		Schemas    []string `json:"schemas"`
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path,omitempty"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(body.Operations) == 0 {
		WriteError(w, http.StatusBadRequest, "no Operations provided")
		return
	}
	// Build the SET clauses lazily — we only UPDATE columns the patch
	// actually touched.
	sets := []string{}
	args := []any{uid}
	for _, op := range body.Operations {
		opName := strings.ToLower(op.Op)
		path := strings.TrimSpace(op.Path)
		switch {
		case opName == "replace" && (strings.EqualFold(path, "active")):
			var v bool
			if err := json.Unmarshal(op.Value, &v); err != nil {
				WriteError(w, http.StatusBadRequest, "active value must be bool")
				return
			}
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("verified = $%d", len(args)))
		case opName == "replace" && (strings.EqualFold(path, "userName") || strings.EqualFold(path, "emails[primary eq true].value")):
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				WriteError(w, http.StatusBadRequest, "value must be string")
				return
			}
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("email = $%d", len(args)))
		case opName == "replace" && strings.EqualFold(path, "externalId"):
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				WriteError(w, http.StatusBadRequest, "value must be string")
				return
			}
			args = append(args, nullIfEmpty(v))
			sets = append(sets, fmt.Sprintf("external_id = $%d", len(args)))
		case opName == "replace" && path == "":
			// Bulk replace — value is a partial scimUser object.
			var partial scimUser
			if err := json.Unmarshal(op.Value, &partial); err != nil {
				WriteError(w, http.StatusBadRequest, "bulk replace value must be a User object")
				return
			}
			if partial.UserName != "" || len(partial.Emails) > 0 {
				email := pickPrimaryEmail(partial)
				if email != "" {
					args = append(args, email)
					sets = append(sets, fmt.Sprintf("email = $%d", len(args)))
				}
			}
			if partial.ExternalID != "" {
				args = append(args, partial.ExternalID)
				sets = append(sets, fmt.Sprintf("external_id = $%d", len(args)))
			}
			// `active` is a boolean — we use a separate raw json
			// unmarshal to distinguish "absent" from "false".
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(op.Value, &raw)
			if a, ok := raw["active"]; ok {
				var v bool
				if json.Unmarshal(a, &v) == nil {
					args = append(args, v)
					sets = append(sets, fmt.Sprintf("verified = $%d", len(args)))
				}
			}
		case opName == "remove" && strings.EqualFold(path, "externalId"):
			sets = append(sets, "external_id = NULL")
		default:
			// Silently accept other paths — IdPs send fields we don't
			// model (phoneNumbers, addresses, …); returning 400 would
			// break their sync. RFC 7644 §3.5.2.3 allows clients to
			// patch unknown paths if the server supports it.
		}
	}
	if len(sets) == 0 {
		// All ops were no-ops (paths we don't model). Return current
		// state.
		u, updated, err := loadUser(r.Context(), d.Pool, tok.Collection, uid, r)
		if err != nil {
			WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		setETag(w, etagFor(updated))
		writeJSON(w, http.StatusOK, u)
		return
	}
	q := fmt.Sprintf(`UPDATE %s SET %s, updated = now() WHERE id = $1 AND scim_managed = TRUE RETURNING id`,
		tok.Collection, strings.Join(sets, ", "))
	var got uuid.UUID
	err = d.Pool.QueryRow(r.Context(), q, args...).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "patch user: "+err.Error())
		return
	}
	u, updated, err := loadUser(r.Context(), d.Pool, tok.Collection, got, r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setETag(w, etagFor(updated))
	writeJSON(w, http.StatusOK, u)
}

// deleteUser — DELETE /Users/{id}. Default policy is hard-delete (RFC
// 7644 §3.6 — "The server MAY remove or mark the resource as inactive";
// we pick the destructive default to match IdP deprovisioning intent
// out of the box).
//
// Operators who want soft-delete (so the row stays for audit + can
// be re-activated) opt in by:
//
//  1. Declaring the SCIM-mapped collection with `.SoftDelete()` in
//     their schema package (gives the `deleted TIMESTAMPTZ` column).
//  2. Setting `auth.scim.soft_delete=true` via `railbase config set`
//     or the admin Settings screen.
//
// BOTH must be true; either missing falls back to hard-delete (graceful
// degradation — a setting flipped on a collection without the column
// would otherwise 500). The collection lookup uses the schema registry;
// when the SCIM-mapped collection isn't registered (atypical), the
// fallback is hard-delete too.
//
// Regardless of branch, we revoke role-grants the user held via SCIM
// group memberships. A soft-deleted user is logically inactive — they
// can no longer sign in — so they MUST lose the role-grants. If the
// operator restores the row later, re-adding them to the group will
// re-grant via the standard PATCH path.
//
// Group-membership rows in `_scim_group_members` are wiped in both
// branches: leaving them dangling on hard-delete creates orphans (no
// FK to the user collection); keeping them on soft-delete would make
// the user "rejoin" their old groups silently on restore. Operators
// who want the latter behaviour can re-add via PATCH after restore.
//
// Response: 204 No Content (RFC 7644 §3.6) in both branches.
func (d *UsersDeps) deleteUser(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	uid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}

	// Resolve soft-delete policy. Both prerequisites must hold; either
	// missing → physical DELETE (backward-compat).
	useSoft := scimSoftDeleteEnabled(r.Context(), d.Settings, tok.Collection)

	// Snapshot group memberships BEFORE the delete so we can reconcile
	// role grants AFTER the row is gone. The reconciliation runs
	// regardless of branch — a soft-deleted user is inactive and must
	// not retain group-granted roles.
	memberships, _ := userGroupMemberships(r.Context(), d.Pool, uid)

	var affected int64
	if useSoft {
		q := fmt.Sprintf(`UPDATE %s SET deleted = now(), updated = now()
                            WHERE id = $1 AND scim_managed = TRUE AND deleted IS NULL`, tok.Collection)
		tag, execErr := d.Pool.Exec(r.Context(), q, uid)
		if execErr != nil {
			WriteError(w, http.StatusInternalServerError, "soft-delete user: "+execErr.Error())
			return
		}
		affected = tag.RowsAffected()
	} else {
		q := fmt.Sprintf(`DELETE FROM %s WHERE id = $1 AND scim_managed = TRUE`, tok.Collection)
		tag, execErr := d.Pool.Exec(r.Context(), q, uid)
		if execErr != nil {
			WriteError(w, http.StatusInternalServerError, "delete user: "+execErr.Error())
			return
		}
		affected = tag.RowsAffected()
	}
	if affected == 0 {
		WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	// Reconcile role-grants for every group the user was a member of.
	// reconcileGroupGrants is nil-tolerant on RBAC + idempotent on the
	// "still owed via another group" path, so this is safe to fan out
	// without per-role diffing.
	if d.RBAC != nil {
		for _, gid := range memberships {
			_ = reconcileGroupGrants(r.Context(), d.Pool, d.RBAC, tok.Collection, gid, uid, false)
		}
	}

	// Clear the group-membership rows. On hard-delete this prevents
	// orphan rows in _scim_group_members (no FK to the user collection).
	// On soft-delete this matches the "user is inactive" semantics —
	// restore won't silently re-join old groups.
	if _, err := d.Pool.Exec(r.Context(),
		`DELETE FROM _scim_group_members WHERE user_id = $1 AND user_collection = $2`,
		uid, tok.Collection); err != nil {
		// Best-effort cleanup. Returning 5xx here would mask the actual
		// delete (which already succeeded). Operators notice via the
		// `_scim_group_members` row count drifting from the user count.
		_ = err
	}

	w.WriteHeader(http.StatusNoContent)
}

// scimSoftDeleteEnabled returns true iff BOTH the operator toggle
// `auth.scim.soft_delete` is true AND the SCIM-mapped collection was
// declared with `.SoftDelete()` in the schema. Either missing → false.
//
// Lookups are best-effort: a settings read error, a missing-from-
// registry collection, or a nil Settings manager all default to false
// (the safe, backward-compat answer). This avoids surfacing a 500 for
// what is effectively a configuration question.
func scimSoftDeleteEnabled(ctx context.Context, mgr *settings.Manager, collection string) bool {
	if mgr == nil {
		return false
	}
	v, ok, err := mgr.GetBool(ctx, "auth.scim.soft_delete")
	if err != nil || !ok || !v {
		return false
	}
	c := registry.Get(collection)
	if c == nil {
		return false
	}
	return c.Spec().SoftDelete
}

// userGroupMemberships returns every SCIM group_id the user currently
// belongs to. Used by deleteUser to fan reconcileGroupGrants out so
// role-grants get revoked across every mapped group.
func userGroupMemberships(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx,
		`SELECT group_id FROM _scim_group_members WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var g uuid.UUID
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// --- helpers ---

func pickPrimaryEmail(u scimUser) string {
	for _, e := range u.Emails {
		if e.Primary && e.Value != "" {
			return strings.TrimSpace(strings.ToLower(e.Value))
		}
	}
	if len(u.Emails) > 0 && u.Emails[0].Value != "" {
		return strings.TrimSpace(strings.ToLower(u.Emails[0].Value))
	}
	if u.UserName != "" {
		return strings.TrimSpace(strings.ToLower(u.UserName))
	}
	return ""
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// loadUser fetches one user and renders it as a scimUser. Always uses
// scim_managed=TRUE — non-SCIM users aren't exposed via the SCIM API.
// Returns the row's `updated` time alongside the resource so callers
// can compute its weak ETag without re-querying.
func loadUser(ctx context.Context, pool *pgxpool.Pool, coll string, id uuid.UUID, r *http.Request) (*scimUser, time.Time, error) {
	q := fmt.Sprintf(`SELECT id, email, verified, external_id, created, updated
	                    FROM %s WHERE id = $1 AND scim_managed = TRUE`, coll)
	row := pool.QueryRow(ctx, q, id)
	return scanUser(row, coll, r)
}

type scanner interface {
	Scan(dest ...any) error
}

// scanUser materialises a `_users`-shaped row as a SCIM resource +
// returns the row's `updated` mtime. The mtime is needed for ETag
// emission; the resource's `Meta.Version` field is also populated
// inline so JSON-only callers see the version too.
func scanUser(s scanner, coll string, r *http.Request) (*scimUser, time.Time, error) {
	var (
		id       uuid.UUID
		email    string
		verified bool
		extID    *string
		created  time.Time
		updated  time.Time
	)
	if err := s.Scan(&id, &email, &verified, &extID, &created, &updated); err != nil {
		return nil, time.Time{}, err
	}
	u := &scimUser{
		Schemas:  []string{scimUserSchema},
		ID:       id.String(),
		UserName: email,
		Active:   verified,
		Emails: []scimEmail{{
			Value:   email,
			Primary: true,
			Type:    "work",
		}},
		Meta: scimMeta{
			ResourceType: "User",
			Created:      created.UTC().Format(time.RFC3339),
			LastModified: updated.UTC().Format(time.RFC3339),
			Location:     fmt.Sprintf("%s/scim/v2/Users/%s", baseURL(r), id),
			Version:      etagFor(updated),
		},
	}
	if extID != nil {
		u.ExternalID = *extID
	}
	return u, updated, nil
}

func baseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}
