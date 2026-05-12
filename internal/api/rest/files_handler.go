package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/files"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/tenant"
)

// FilesDeps bundles the v1.3.1 storage subsystem so the file-upload /
// download routes don't have to grovel through handlerDeps for it.
//
// Driver may be nil — in that case the routes return 503 ("file
// storage not configured") rather than 500. Keeps the surface tidy
// for deployments that don't want file fields.
type FilesDeps struct {
	Driver    files.Driver
	Store     *files.Store
	Signer    []byte        // master key for SignURL
	URLTTL    time.Duration // inline-embed TTL (default 5min)
	MaxUpload int64         // per-upload byte ceiling (default 25MB)
}

// MountFiles installs the v1.3.1 file upload + download routes onto r.
// Routes:
//
//	POST   /api/collections/{name}/records/{id}/files/{field}  (multipart, part name "file")
//	DELETE /api/collections/{name}/records/{id}/files/{field}/{filename}
//	GET    /api/files/{collection}/{record_id}/{field}/{filename}  (signed URL)
//
// The upload + delete routes mount inside the authed group (caller's
// responsibility — they're collection-scoped and need the same auth
// middleware as record CRUD). The signed-URL GET mounts OUTSIDE auth
// because the HMAC token IS the auth.
func MountFiles(r chi.Router, d *handlerDeps, fd *FilesDeps) {
	if fd == nil {
		fd = &FilesDeps{}
	}
	if fd.URLTTL == 0 {
		fd.URLTTL = 5 * time.Minute
	}
	if fd.MaxUpload == 0 {
		fd.MaxUpload = 25 << 20 // 25 MiB
	}
	h := &filesHandler{deps: d, files: fd}

	r.Post("/api/collections/{name}/records/{id}/files/{field}", h.upload)
	r.Delete("/api/collections/{name}/records/{id}/files/{field}/{filename}", h.delete)
	r.Get("/api/files/{collection}/{record_id}/{field}/{filename}", h.download)
}

type filesHandler struct {
	deps  *handlerDeps
	files *FilesDeps
}

// upload streams a multipart "file" part to storage, persists metadata
// in `_files`, and updates the record column. Returns the file object
// {name, size, mime, url} on success.
func (h *filesHandler) upload(w http.ResponseWriter, r *http.Request) {
	if h.files.Driver == nil || h.files.Store == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "file storage not configured"))
		return
	}

	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	idStr := chi.URLParam(r, "id")
	recordID, parseErr := uuid.Parse(idStr)
	if parseErr != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid record id"))
		return
	}
	fieldName := chi.URLParam(r, "field")
	field, fErr := lookupFileField(spec, fieldName)
	if fErr != nil {
		rerr.WriteJSON(w, fErr)
		return
	}

	// Parse multipart with a strict ceiling. ParseMultipartForm uses
	// MAX_MEMORY as the in-memory budget; anything bigger spills to a
	// disk-backed temp file managed by net/http.
	r.Body = http.MaxBytesReader(w, r.Body, h.files.MaxUpload)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"multipart parse failed: %s", err.Error()))
		return
	}
	mpFile, mpHeader, err := r.FormFile("file")
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"missing multipart part \"file\": %s", err.Error()))
		return
	}
	defer mpFile.Close()

	mimeType := mpHeader.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if len(field.AcceptMIME) > 0 && !acceptsMIME(field.AcceptMIME, mimeType) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"mime type %q not allowed for field %q (accepts: %v)",
			mimeType, fieldName, field.AcceptMIME))
		return
	}

	// Buffer the body so we can compute SHA-256 before writing. 10MB
	// memory ceiling matches PB's default; rest spills to disk via
	// http.MaxBytesReader's internal temp file.
	hash := files.NewHashingReader(mpFile)
	body, readErr := io.ReadAll(hash)
	if readErr != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"read upload: %s", readErr.Error()))
		return
	}
	size := hash.Size()
	if field.MaxBytes > 0 && size > field.MaxBytes {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"file size %d exceeds field max %d", size, field.MaxBytes))
		return
	}
	sha := hash.Sum()
	filename := files.SanitiseFilename(mpHeader.Filename)
	storageKey := files.StorageKey(files.SHA256Hex(sha), filename)

	if _, err := h.files.Driver.Put(r.Context(), storageKey, bytes.NewReader(body)); err != nil {
		h.deps.log.Error("rest: file Put", "err", err, "collection", spec.Name, "field", fieldName)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "store upload failed"))
		return
	}

	// Insert the metadata row. Tenant + owner derived from the
	// request principal / tenant ctx.
	f := &files.File{
		Collection: spec.Name,
		RecordID:   recordID,
		Field:      fieldName,
		Filename:   filename,
		MIME:       mimeType,
		Size:       size,
		SHA256:     sha,
		StorageKey: storageKey,
	}
	if p := authmw.PrincipalFrom(r.Context()); p.Authenticated() {
		uid := p.UserID
		f.OwnerUser = &uid
	}
	if tenant.HasID(r.Context()) {
		tid := uuid.UUID(tenant.ID(r.Context()))
		f.TenantID = &tid
	}
	if err := h.files.Store.Insert(r.Context(), f); err != nil {
		_ = h.files.Driver.Delete(r.Context(), storageKey)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "persist metadata failed"))
		return
	}

	// Update the user record's file column. Single-file fields store
	// the filename; multi-file fields append to a JSONB array.
	if err := h.updateRecordColumn(r, spec, recordID, fieldName, field.Type, filename); err != nil {
		// Metadata row is now an orphan; best-effort delete.
		_ = h.files.Store.Delete(r.Context(), f.ID)
		_ = h.files.Driver.Delete(r.Context(), storageKey)
		rerr.WriteJSON(w, err)
		return
	}

	resp := map[string]any{
		"name": filename,
		"size": size,
		"mime": mimeType,
		"url":  h.signedURL(spec.Name, recordID.String(), fieldName, filename),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// delete removes the file metadata row, the underlying blob, and
// strips the filename from the record column.
func (h *filesHandler) delete(w http.ResponseWriter, r *http.Request) {
	if h.files.Driver == nil || h.files.Store == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "file storage not configured"))
		return
	}
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	recordID, parseErr := uuid.Parse(chi.URLParam(r, "id"))
	if parseErr != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid record id"))
		return
	}
	fieldName := chi.URLParam(r, "field")
	field, fErr := lookupFileField(spec, fieldName)
	if fErr != nil {
		rerr.WriteJSON(w, fErr)
		return
	}
	filename := chi.URLParam(r, "filename")
	if filename == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "filename required"))
		return
	}

	meta, err := h.files.Store.GetByKey(r.Context(), spec.Name, recordID, fieldName, filename)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "file not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup failed"))
		return
	}

	if err := h.removeFromRecord(r, spec, recordID, fieldName, field.Type, filename); err != nil {
		rerr.WriteJSON(w, err)
		return
	}
	if delErr := h.files.Store.Delete(r.Context(), meta.ID); delErr != nil && !errors.Is(delErr, files.ErrNotFound) {
		h.deps.log.Error("rest: file metadata delete", "err", delErr)
	}
	_ = h.files.Driver.Delete(r.Context(), meta.StorageKey)
	w.WriteHeader(http.StatusNoContent)
}

// download streams the blob to the client. Auth is the signed URL
// (token + expires query params); record/tenant rules are NOT
// reapplied here — the assumption is that whoever minted the URL
// already passed them. This matches PB and is what most SPA fetches
// do (load an <img src> without re-authenticating).
func (h *filesHandler) download(w http.ResponseWriter, r *http.Request) {
	if h.files.Driver == nil || h.files.Store == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "file storage not configured"))
		return
	}
	collection := chi.URLParam(r, "collection")
	recordIDStr := chi.URLParam(r, "record_id")
	field := chi.URLParam(r, "field")
	filename := chi.URLParam(r, "filename")
	token := r.URL.Query().Get("token")
	expires := r.URL.Query().Get("expires")

	if token == "" || expires == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "missing signature"))
		return
	}
	if err := files.VerifySignature(h.files.Signer, collection, recordIDStr, field, filename, token, expires); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden, "invalid or expired signature"))
		return
	}

	recordID, parseErr := uuid.Parse(recordIDStr)
	if parseErr != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid record id"))
		return
	}
	meta, err := h.files.Store.GetByKey(r.Context(), collection, recordID, field, filename)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "file not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup failed"))
		return
	}
	rc, err := h.files.Driver.Open(r.Context(), meta.StorageKey)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "file blob missing"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "open failed"))
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", meta.MIME)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, meta.Filename))
	http.ServeContent(w, r, meta.Filename, meta.CreatedAt, rc)
}

// signedURL constructs the canonical download URL for a (collection,
// record, field, filename) tuple. Used by upload responses + record
// marshalling.
func (h *filesHandler) signedURL(collection, recordID, field, filename string) string {
	return signedFileURL(h.files.Signer, collection, recordID, field, filename, h.files.URLTTL)
}

// signedFileURL is the free-function form of filesHandler.signedURL —
// callable from marshalRecord without needing the handler instance.
func signedFileURL(signer []byte, collection, recordID, field, filename string, ttl time.Duration) string {
	tok, exp := files.SignURL(signer, collection, recordID, field, filename, ttl)
	return fmt.Sprintf("/api/files/%s/%s/%s/%s?token=%s&expires=%s",
		collection, recordID, field, filename, tok, exp)
}

// lookupFileField pulls the file/files field spec by name. Returns a
// 400 envelope if the field is missing or wrong type.
func lookupFileField(spec builder.CollectionSpec, name string) (*builder.FieldSpec, *rerr.Error) {
	for i := range spec.Fields {
		if spec.Fields[i].Name == name {
			f := spec.Fields[i]
			if f.Type != builder.TypeFile && f.Type != builder.TypeFiles {
				return nil, rerr.New(rerr.CodeValidation,
					"field %q is not a file/files field (type=%s)", name, f.Type)
			}
			return &f, nil
		}
	}
	return nil, rerr.New(rerr.CodeNotFound,
		"field %q not found on collection %q", name, spec.Name)
}

// acceptsMIME reports whether `mt` matches any pattern in `accept`.
// Wildcards: `image/*` matches any `image/...`, `*/*` matches any.
func acceptsMIME(accept []string, mt string) bool {
	if mt == "" {
		return false
	}
	// Strip "; charset=..." so the major/minor matcher works.
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	parts := strings.SplitN(mt, "/", 2)
	if len(parts) != 2 {
		return false
	}
	major, minor := parts[0], parts[1]
	for _, p := range accept {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pp := strings.SplitN(p, "/", 2)
		if len(pp) != 2 {
			// Bare extension like "pdf" — let mime.TypeByExtension
			// catch the canonical form via the request's MIME, but
			// reject here since we don't know what to compare to.
			if t := mime.TypeByExtension("." + p); t != "" && strings.HasPrefix(t, mt) {
				return true
			}
			continue
		}
		if (pp[0] == "*" || pp[0] == major) && (pp[1] == "*" || pp[1] == minor) {
			return true
		}
	}
	return false
}

// updateRecordColumn sets the user record's file column to the
// just-uploaded filename. For single-file fields this is a simple
// UPDATE; for multi-file fields we append to a JSONB array.
func (h *filesHandler) updateRecordColumn(r *http.Request, spec builder.CollectionSpec, recordID uuid.UUID, fieldName string, fieldType builder.FieldType, filename string) *rerr.Error {
	q, qErr := h.deps.queryFor(r.Context(), spec)
	if qErr != nil {
		return qErr
	}
	var sql string
	switch fieldType {
	case builder.TypeFile:
		sql = fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE id = $2`,
			pgIdent(spec.Name), pgIdent(fieldName))
		tag, err := q.Exec(r.Context(), sql, filename, recordID)
		if err != nil {
			return rerr.Wrap(err, rerr.CodeInternal, "update record column failed")
		}
		if tag.RowsAffected() == 0 {
			return rerr.New(rerr.CodeNotFound, "record %s not found", recordID)
		}
	case builder.TypeFiles:
		// JSONB array append; coalesce NULL → [].
		sql = fmt.Sprintf(`UPDATE %s
		    SET %s = COALESCE(%s, '[]'::jsonb) || $1::jsonb
		    WHERE id = $2`,
			pgIdent(spec.Name), pgIdent(fieldName), pgIdent(fieldName))
		payload := fmt.Sprintf("[%q]", filename)
		tag, err := q.Exec(r.Context(), sql, payload, recordID)
		if err != nil {
			return rerr.Wrap(err, rerr.CodeInternal, "update record column failed")
		}
		if tag.RowsAffected() == 0 {
			return rerr.New(rerr.CodeNotFound, "record %s not found", recordID)
		}
	}
	return nil
}

// removeFromRecord strips a filename from the record's file column.
// For single-file fields → NULL; for multi-file → remove array entry.
func (h *filesHandler) removeFromRecord(r *http.Request, spec builder.CollectionSpec, recordID uuid.UUID, fieldName string, fieldType builder.FieldType, filename string) *rerr.Error {
	q, qErr := h.deps.queryFor(r.Context(), spec)
	if qErr != nil {
		return qErr
	}
	var sql string
	var args []any
	switch fieldType {
	case builder.TypeFile:
		sql = fmt.Sprintf(`UPDATE %s SET %s = NULL WHERE id = $1 AND %s = $2`,
			pgIdent(spec.Name), pgIdent(fieldName), pgIdent(fieldName))
		args = []any{recordID, filename}
	case builder.TypeFiles:
		// JSONB minus: filter the array to exclude `filename`.
		sql = fmt.Sprintf(`UPDATE %s SET %s = (
		    SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb)
		    FROM jsonb_array_elements_text(COALESCE(%s, '[]'::jsonb)) AS elem
		    WHERE elem <> $1
		) WHERE id = $2`,
			pgIdent(spec.Name), pgIdent(fieldName), pgIdent(fieldName))
		args = []any{filename, recordID}
	}
	if _, err := q.Exec(r.Context(), sql, args...); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "strip record column failed")
	}
	return nil
}

// pgIdent quotes an identifier conservatively. Schema validators
// already restrict names to [a-zA-Z_][a-zA-Z0-9_]*, so quoting is
// purely belt-and-braces — never reaches Postgres unless someone
// circumvents the registry validator.
func pgIdent(s string) string {
	// Strip any embedded quote bytes that shouldn't exist anyway.
	return `"` + strings.ReplaceAll(s, `"`, "") + `"`
}

// fileURLBuilder returns a closure that produces signed URLs for the
// given collection. Used by marshalRecord at JSON-emit time.
//
// (Defined here so the marshal path imports `files` only through this
// package; record.go doesn't need to know about file storage.)
func (h *filesHandler) fileURLBuilder(collection, recordID string) func(field, filename string) string {
	return func(field, filename string) string {
		return h.signedURL(collection, recordID, field, filename)
	}
}

// ext returns the lower-case filename extension WITHOUT the leading
// dot. "image.PNG" → "png". Used by acceptsMIME's bare-extension
// branch.
func ext(filename string) string {
	e := path.Ext(filename)
	if e == "" {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(e, "."))
}

// suppress unused-import linter complaints in the partial-state
// commits during v1.3.1 development.
var _ = ext
