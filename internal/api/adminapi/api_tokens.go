package adminapi

// v1.7.9 — admin endpoints for managing long-lived API tokens.
//
// Companion to the v1.7.3 `railbase auth token` CLI. Admin-only
// (gated by RequireAdmin in adminapi.Mount). The endpoint shape
// follows the page/perPage convention from logs/audit/jobs; the
// response shape exposes only metadata + a short fingerprint so the
// admin UI can browse tokens without ever surfacing a raw secret.
//
// Display-once contract: the RAW token is emitted exactly once, from
// the Create and Rotate handlers. List and Revoke never include it.
// The handlers do NOT log the raw token (security-side requirement
// — leaked logs must not be enough to forge a token).
//
// Filtering:
//
//	page             1-indexed (default 1)
//	perPage          default 50, max 200
//	owner            UUID exact match on owner_id (optional)
//	owner_collection optional pair with owner; if omitted, "users"
//	include_revoked  "true" flips the default (filter out revoked)
//	kind             reserved — not used at the moment but the param
//	                 is parsed so future filters can land without a
//	                 client change

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/auth/apitoken"
	rerr "github.com/railbase/railbase/internal/errors"
)

// apiTokenJSON is the response shape for one row. Mirrors the
// apitoken.Record metadata; the raw token hash is NEVER serialised.
// Fingerprint is omitted on List paths (we don't have the raw token
// to compute it from the hash without exposing the hash itself);
// Create / Rotate compute it inline from the freshly-minted raw.
type apiTokenJSON struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	OwnerID         uuid.UUID  `json:"owner_id"`
	OwnerCollection string     `json:"owner_collection"`
	Scopes          []string   `json:"scopes"`
	Fingerprint     string     `json:"fingerprint"`
	ExpiresAt       *time.Time `json:"expires_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	CreatedAt       time.Time  `json:"created_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	RotatedFrom     *uuid.UUID `json:"rotated_from"`
}

// newAPITokenJSON shapes a Record for the wire. fingerprint is the
// already-computed short hex prefix; the caller passes "" when no
// raw token is available (List / Revoke paths).
func newAPITokenJSON(r *apitoken.Record, fingerprint string) apiTokenJSON {
	scopes := r.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return apiTokenJSON{
		ID:              r.ID,
		Name:            r.Name,
		OwnerID:         r.OwnerID,
		OwnerCollection: r.OwnerCollection,
		Scopes:          scopes,
		Fingerprint:     fingerprint,
		ExpiresAt:       r.ExpiresAt,
		LastUsedAt:      r.LastUsedAt,
		CreatedAt:       r.CreatedAt,
		RevokedAt:       r.RevokedAt,
		RotatedFrom:     r.RotatedFrom,
	}
}

// apiTokensListHandler — GET /api/_admin/api-tokens
//
// Listing uses Store.ListAll (or Store.List when owner is set) and
// post-filters in Go. We deliberately avoid extending the Store's
// SQL surface for this slice — the token table is operator-sized
// (dozens to a few hundred rows in practice), so an in-memory filter
// is cheap and keeps the CLI's wire contract stable.
func (d *Deps) apiTokensListHandler(w http.ResponseWriter, r *http.Request) {
	if d.APITokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "api tokens not configured"))
		return
	}

	const defaultPerPage = 50
	const maxPerPage = 200

	perPage := parseIntParam(r, "perPage", defaultPerPage)
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	page := parseIntParam(r, "page", 1)
	if page < 1 {
		page = 1
	}

	q := r.URL.Query()
	includeRevoked := q.Get("include_revoked") == "true"
	ownerParam := strings.TrimSpace(q.Get("owner"))
	ownerCollection := strings.TrimSpace(q.Get("owner_collection"))
	if ownerCollection == "" {
		ownerCollection = "users"
	}

	var records []*apitoken.Record
	if ownerParam != "" {
		oid, err := uuid.Parse(ownerParam)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "owner must be a valid UUID"))
			return
		}
		recs, err := d.APITokens.List(r.Context(), ownerCollection, oid)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list api tokens"))
			return
		}
		records = recs
	} else {
		recs, err := d.APITokens.ListAll(r.Context())
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list api tokens"))
			return
		}
		records = recs
	}

	// Filter revoked rows unless explicitly requested. Store.List /
	// ListAll always include revoked for audit completeness; the admin
	// UI defaults to active-only for readability.
	if !includeRevoked {
		filtered := records[:0]
		for _, rec := range records {
			if rec.RevokedAt == nil {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}

	total := int64(len(records))

	// Page slice. Same convention as logs/jobs: 1-indexed; out-of-range
	// pages return an empty items array (the UI shows "no rows").
	start := (page - 1) * perPage
	if start > len(records) {
		records = nil
	} else {
		records = records[start:]
		if len(records) > perPage {
			records = records[:perPage]
		}
	}

	items := make([]apiTokenJSON, 0, len(records))
	for _, rec := range records {
		items = append(items, newAPITokenJSON(rec, ""))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": total,
		"items":      items,
	})
}

// apiTokensCreateRequest is the wire shape for POST. Field names
// match the CLI's flag names so admins switching between the two
// surfaces don't have to relearn anything.
type apiTokensCreateRequest struct {
	Name            string   `json:"name"`
	OwnerID         string   `json:"owner_id"`
	OwnerCollection string   `json:"owner_collection"`
	Scopes          []string `json:"scopes"`
	TTLSeconds      int64    `json:"ttl_seconds"`
}

// apiTokensCreateHandler — POST /api/_admin/api-tokens.
//
// Returns the RAW token in the response body (display-once). The
// handler MUST NOT log the raw — even at debug level.
func (d *Deps) apiTokensCreateHandler(w http.ResponseWriter, r *http.Request) {
	if d.APITokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "api tokens not configured"))
		return
	}

	var req apiTokensCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name is required"))
		return
	}
	if strings.TrimSpace(req.OwnerID) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "owner_id is required"))
		return
	}
	oid, err := uuid.Parse(req.OwnerID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "owner_id must be a valid UUID"))
		return
	}
	if strings.TrimSpace(req.OwnerCollection) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "owner_collection is required"))
		return
	}

	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	raw, rec, err := d.APITokens.Create(r.Context(), apitoken.CreateInput{
		Name:            req.Name,
		OwnerID:         oid,
		OwnerCollection: req.OwnerCollection,
		Scopes:          req.Scopes,
		TTL:             ttl,
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create api token"))
		return
	}

	// Compute fingerprint inline from the raw token. We can't go back
	// to compute it later — the raw is destroyed after this response.
	fp := fingerprintFromDeps(d, raw)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":  raw,
		"record": newAPITokenJSON(rec, fp),
	})
}

// apiTokensRevokeHandler — POST /api/_admin/api-tokens/{id}/revoke.
//
// Idempotent per the Store contract: revoking an already-revoked
// token returns the same envelope as the first call.
func (d *Deps) apiTokensRevokeHandler(w http.ResponseWriter, r *http.Request) {
	if d.APITokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "api tokens not configured"))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "id must be a valid UUID"))
		return
	}

	if err := d.APITokens.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, apitoken.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "api token not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "revoke api token"))
		return
	}

	// Re-read so the response carries the now-revoked record. Store.Get
	// includes revoked rows (CLI/audit reuse the call path).
	rec, err := d.APITokens.Get(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load revoked api token"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"record": newAPITokenJSON(rec, ""),
	})
}

// apiTokensRotateRequest carries the optional TTL override; if zero,
// the Store inherits the predecessor's remaining lifetime.
type apiTokensRotateRequest struct {
	TTLSeconds int64 `json:"ttl_seconds"`
}

// apiTokensRotateHandler — POST /api/_admin/api-tokens/{id}/rotate.
//
// Same display-once contract as Create: the raw successor token is
// in the response exactly once.
func (d *Deps) apiTokensRotateHandler(w http.ResponseWriter, r *http.Request) {
	if d.APITokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "api tokens not configured"))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "id must be a valid UUID"))
		return
	}

	// Body is optional — a rotate with no body is "inherit TTL from
	// predecessor". Decode tolerantly: an empty body must not 400.
	var req apiTokensRotateRequest
	if r.ContentLength != 0 && r.Body != nil {
		// Best-effort decode: ignore EOF / empty-body errors so the
		// "no body at all" shorthand stays valid.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	raw, rec, err := d.APITokens.Rotate(r.Context(), id, ttl)
	if err != nil {
		if errors.Is(err, apitoken.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "api token not found or already revoked"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rotate api token"))
		return
	}

	fp := fingerprintFromDeps(d, raw)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":  raw,
		"record": newAPITokenJSON(rec, fp),
	})
}

// fingerprintFromDeps wraps Store.Fingerprint so the Create / Rotate
// handlers can emit a parity label with the CLI. Nil-Store falls
// through to "" — the UI degrades to the id-prefix display.
func fingerprintFromDeps(d *Deps, raw string) string {
	if d == nil || d.APITokens == nil {
		return ""
	}
	return d.APITokens.Fingerprint(raw)
}
