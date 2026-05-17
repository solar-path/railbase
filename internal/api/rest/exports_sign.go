package rest

// POST /api/exports/sign — issue a short-lived path-scoped download
// token (dltoken) for browser-side export downloads.
//
// FEEDBACK shopper N5 — the dltoken package exists, but until this
// handler shipped, embedders had to hand-roll the signing endpoint
// themselves (parse path, RBAC check, sign, return). This consolidates
// the pattern into one secure default.
//
// Body shape (JSON):
//
//	{ "path": "/api/collections/orders/export.xlsx",
//	  "ttl_seconds": 60   // optional, capped at dltoken.MaxTTL
//	}
//
// Response:
//
//	{ "download_url": "/api/collections/orders/export.xlsx?dt=<token>",
//	  "expires_at":   "2026-05-16T01:14:58Z" }
//
// RBAC: the handler currently accepts any authenticated principal.
// Path-level RBAC (does this user have list/view access to the target
// collection?) is enforced by the underlying export route itself —
// the token only lets the request through the dltoken-aware middleware
// query-param check; the route handler still runs its full rule
// machinery.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/dltoken"
	rerr "github.com/railbase/railbase/internal/errors"
)

// MountExportsSign registers POST /api/exports/sign. Pass nil
// masterKey to skip mounting entirely (test environments without a
// secret).
func MountExportsSign(r chiRouter, masterKey []byte) {
	if masterKey == nil {
		return
	}
	h := &exportsSignHandler{masterKey: masterKey}
	r.Post("/api/exports/sign", h.ServeHTTP)
}

// chiRouter narrows the chi.Router surface to just the Post method
// this file uses, so we don't pull a fresh chi import just for one
// type assertion.
type chiRouter interface {
	Post(pattern string, handler http.HandlerFunc) // matches chi.Router
}

type exportsSignHandler struct {
	masterKey []byte
}

type exportsSignRequest struct {
	Path       string `json:"path"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type exportsSignResponse struct {
	DownloadURL string    `json:"download_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// allowedExportPathPrefixes whitelists the path patterns we'll sign.
// Refuses to sign arbitrary URLs to limit blast radius if a token
// leaks. Mirrors the prefixes the existing export handlers register.
var allowedExportPathPrefixes = []string{
	"/api/collections/", // matches both /records/export.xlsx and entity-doc PDFs
	"/api/files/",       // signed file downloads
}

func (h *exportsSignHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "authentication required"))
		return
	}
	var req exportsSignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if req.Path == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "path is required"))
		return
	}
	if !isAllowedExportPath(req.Path) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"path %q is not eligible for download-token signing (allowed prefixes: %v)",
			req.Path, allowedExportPathPrefixes))
		return
	}
	// Clamp TTL.
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = dltoken.DefaultTTL
	}
	if ttl > dltoken.MaxTTL {
		ttl = dltoken.MaxTTL
	}

	token, expiresAt, err := dltoken.Sign(h.masterKey, req.Path, dltoken.SignOptions{TTL: ttl})
	if err != nil {
		if errors.Is(err, dltoken.ErrInvalid) {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "sign token"))
		return
	}

	sep := "?"
	if strings.Contains(req.Path, "?") {
		sep = "&"
	}
	resp := exportsSignResponse{
		DownloadURL: req.Path + sep + "dt=" + token,
		ExpiresAt:   expiresAt,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func isAllowedExportPath(p string) bool {
	// Reject path traversal + scheme injection. The path must be
	// rooted at /api/ and not contain ".." or schema markers.
	if strings.Contains(p, "..") || strings.Contains(p, "://") || !strings.HasPrefix(p, "/") {
		return false
	}
	for _, prefix := range allowedExportPathPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
