package adminapi

// v1.7.20 §3.14 #123 / §3.11 — admin endpoints backing the Hooks editor
// screen. The fsnotify watcher in internal/hooks already hot-reloads
// `pb_hooks/*.js` on save (150 ms debounce, <1 s end-to-end); this
// surface lets operators read + write those files from the admin UI
// without SSH'ing into the box.
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /hooks/files               list *.js under HooksDir (recursive)
//	GET    /hooks/files/{path}        return file content
//	PUT    /hooks/files/{path}        write file (creates parent dirs)
//	DELETE /hooks/files/{path}        remove a file
//
// `{path}` is the URL-encoded relative path under HooksDir. We refuse
// any request whose cleaned absolute path doesn't have HooksDir as a
// prefix — the path-traversal guard exists in both the GET and PUT
// branches because chi's pattern matching does NOT itself normalize
// `..` segments.
//
// HooksDir wired from app.go in v1.7.21+. Until then the field is empty
// in production and every handler short-circuits with a 503 "unavailable"
// envelope; tests inject a t.TempDir() via Deps directly.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
)

// mountHooksFiles registers the four hooks-editor routes on r. Unlike
// the webhooks / realtime surfaces this is NOT nil-guarded on HooksDir:
// we want the UI to be able to probe the endpoint and surface a typed
// "not configured" hint rather than a 404. The 503 fires inside each
// handler when HooksDir is empty.
func (d *Deps) mountHooksFiles(r chi.Router) {
	r.Get("/hooks/files", d.hooksFilesListHandler)
	r.Get("/hooks/files/*", d.hooksFilesGetHandler)
	r.Put("/hooks/files/*", d.hooksFilesPutHandler)
	r.Delete("/hooks/files/*", d.hooksFilesDeleteHandler)
}

// hooksFile is the wire shape for one entry in the listing + the
// envelope-less response of the per-file GET / PUT handlers. `path`
// is the slash-separated relative path under HooksDir; `modified` is
// RFC3339.
type hooksFile struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Content is only populated by the per-file GET; the list endpoint
	// omits it via the `omitempty` tag so the listing payload stays tiny.
	Content string `json:"content,omitempty"`
}

// hooksFilesListHandler — GET /api/_admin/hooks/files.
//
// Recursively walks HooksDir, returning every regular file with a `.js`
// suffix. Sorted by path so the UI renders a stable tree. Hidden files
// (those starting with a dot) and non-.js files are silently skipped —
// the editor surface only deals with hooks, not arbitrary disk state.
func (d *Deps) hooksFilesListHandler(w http.ResponseWriter, r *http.Request) {
	if d.HooksDir == "" {
		writeHooksUnavailable(w)
		return
	}
	rootAbs, err := filepath.Abs(d.HooksDir)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve hooks dir"))
		return
	}

	var rows []hooksFile
	err = filepath.WalkDir(rootAbs, func(p string, dent fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// HooksDir missing on disk is not fatal — return an empty
			// listing so the UI's "no files yet" empty state renders.
			if errors.Is(walkErr, fs.ErrNotExist) && p == rootAbs {
				return filepath.SkipDir
			}
			return walkErr
		}
		if dent.IsDir() {
			// Skip dot-directories (.git, .DS_Store etc.) but always
			// descend into the root itself.
			if p != rootAbs && strings.HasPrefix(dent.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(dent.Name(), ".js") {
			return nil
		}
		if strings.HasPrefix(dent.Name(), ".") {
			return nil
		}
		info, ierr := dent.Info()
		if ierr != nil {
			return ierr
		}
		rel, rerr2 := filepath.Rel(rootAbs, p)
		if rerr2 != nil {
			return rerr2
		}
		rows = append(rows, hooksFile{
			Path:     filepath.ToSlash(rel),
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "walk hooks dir"))
		return
	}
	if rows == nil {
		rows = []hooksFile{}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items": rows,
	})
}

// hooksFilesGetHandler — GET /api/_admin/hooks/files/{path}.
//
// Reads + returns the file content alongside size + mtime. Refuses
// directories (returns 404 with a "file not found" message — the
// listing endpoint never surfaces directories so the UI shouldn't
// reach this branch). Path traversal is rejected by resolveHooksPath.
func (d *Deps) hooksFilesGetHandler(w http.ResponseWriter, r *http.Request) {
	if d.HooksDir == "" {
		writeHooksUnavailable(w)
		return
	}
	abs, rel, ok := resolveHooksPath(w, r, d.HooksDir)
	if !ok {
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "file not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "stat file"))
		return
	}
	if info.IsDir() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "file not found"))
		return
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "read file"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(hooksFile{
		Path:     filepath.ToSlash(rel),
		Size:     info.Size(),
		Modified: info.ModTime().UTC(),
		Content:  string(body),
	})
}

// hooksFilesPutRequest is the wire shape for PUT bodies.
type hooksFilesPutRequest struct {
	Content string `json:"content"`
}

// hooksFilesPutHandler — PUT /api/_admin/hooks/files/{path}.
//
// Writes content to the file, creating any missing parent directories.
// Refuses paths that escape HooksDir or that don't end in `.js` — the
// editor is hooks-only by design; arbitrary file write would surprise
// operators who don't expect this surface to be a general FS API.
//
// We write through a temp file in the same directory, then rename, so a
// failed write doesn't truncate the existing hook (the fsnotify watcher
// would otherwise hot-load a half-written file).
func (d *Deps) hooksFilesPutHandler(w http.ResponseWriter, r *http.Request) {
	if d.HooksDir == "" {
		writeHooksUnavailable(w)
		return
	}
	abs, rel, ok := resolveHooksPath(w, r, d.HooksDir)
	if !ok {
		return
	}
	if !strings.HasSuffix(abs, ".js") {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "only .js files are allowed"))
		return
	}
	var req hooksFilesPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create parent dir"))
		return
	}
	// Atomic-ish write: temp file in the same dir + rename, so the
	// fsnotify watcher doesn't observe a half-written file mid-save.
	dir := filepath.Dir(abs)
	tmp, err := os.CreateTemp(dir, ".rb-hook-*.tmp")
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create temp file"))
		return
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.WriteString(req.Content); err != nil {
		cleanup()
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "write temp file"))
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "close temp file"))
		return
	}
	if err := os.Rename(tmpPath, abs); err != nil {
		_ = os.Remove(tmpPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rename temp file"))
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "stat after write"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(hooksFile{
		Path:     filepath.ToSlash(rel),
		Size:     info.Size(),
		Modified: info.ModTime().UTC(),
	})
}

// hooksFilesDeleteHandler — DELETE /api/_admin/hooks/files/{path}.
//
// Removes the file. 204 on success, 404 if the file didn't exist (we
// don't treat delete-of-missing as idempotent here — the UI should
// reflect the truth: the file the operator asked to delete wasn't
// there). Refuses traversal via resolveHooksPath.
func (d *Deps) hooksFilesDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if d.HooksDir == "" {
		writeHooksUnavailable(w)
		return
	}
	abs, _, ok := resolveHooksPath(w, r, d.HooksDir)
	if !ok {
		return
	}
	err := os.Remove(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "file not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete file"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveHooksPath validates + resolves the {path} URL-param into an
// absolute on-disk path under HooksDir. Returns (abs, rel, ok). On
// failure it writes the typed 400 envelope and returns ok=false so
// the caller short-circuits.
//
// Guard chain:
//  1. URL-decode (chi delivers the raw path segment unescaped from the
//     glob match, but we run url.PathUnescape defensively for the
//     percent-encoded case that some clients send anyway).
//  2. Reject absolute paths (`/etc/passwd`).
//  3. Reject any segment containing `..` literally (defence in depth;
//     filepath.Clean would normalize them but we want a hard reject
//     so the error message is obvious to operators).
//  4. filepath.Clean + Join under HooksDir, then verify the result
//     still has HooksDir as a prefix (handles platform-specific edge
//     cases the literal `..` check might miss).
func resolveHooksPath(w http.ResponseWriter, r *http.Request, hooksDir string) (abs, rel string, ok bool) {
	raw := chi.URLParam(r, "*")
	if raw == "" {
		writeBadRequest(w, "path is required")
		return "", "", false
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		writeBadRequest(w, "invalid url-encoded path")
		return "", "", false
	}
	if strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, `\`) {
		writeBadRequest(w, "path escapes hooks dir")
		return "", "", false
	}
	// Hard reject any literal `..` segment — defence in depth on top of
	// the prefix check below.
	for _, seg := range strings.FieldsFunc(decoded, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if seg == ".." {
			writeBadRequest(w, "path escapes hooks dir")
			return "", "", false
		}
	}
	rootAbs, err := filepath.Abs(hooksDir)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve hooks dir"))
		return "", "", false
	}
	cleanedRel := filepath.Clean(decoded)
	joined := filepath.Join(rootAbs, cleanedRel)
	// Post-join prefix check: the joined path must live under HooksDir.
	// We compare with a trailing separator so /foo/bar doesn't match a
	// HooksDir of /foo/ba.
	rootWithSep := rootAbs + string(filepath.Separator)
	if joined != rootAbs && !strings.HasPrefix(joined, rootWithSep) {
		writeBadRequest(w, "path escapes hooks dir")
		return "", "", false
	}
	if joined == rootAbs {
		writeBadRequest(w, "path is required")
		return "", "", false
	}
	relOut, err := filepath.Rel(rootAbs, joined)
	if err != nil {
		writeBadRequest(w, "path escapes hooks dir")
		return "", "", false
	}
	return joined, relOut, true
}

// writeBadRequest emits the spec's `bad_request` envelope at HTTP 400.
// We can't use rerr.WriteJSON for this case because the internal
// errors package's Code enum doesn't include `bad_request` — Railbase
// uses `validation` for client-input failures. We hand-roll the JSON
// here to match the spec literally.
func writeBadRequest(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    "bad_request",
			"message": message,
		},
	})
}

// writeHooksUnavailable emits a 503 with the `unavailable` code,
// matching the realtime / webhooks nil-guard pattern. The admin UI
// detects this status to render a "Hooks directory not configured —
// set RAILBASE_HOOKS_DIR" hint.
func writeHooksUnavailable(w http.ResponseWriter) {
	rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "hooks dir not configured"))
}
