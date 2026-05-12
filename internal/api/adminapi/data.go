package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/schema/registry"
)

// sortStrings keeps imports tidy — direct sort.Strings would do, but
// a wrapper centralises the dependency for swapping later (e.g. if
// we adopt natural sort).
func sortStrings(s []string) { sort.Strings(s) }

// schemaHandler returns the registered collection list, materialised
// to the same JSON shape internal/schema/builder uses for diff. The
// admin UI consumes this on first load to render the sidebar (one
// entry per collection) and the schema-viewer screen.
//
// Why a separate endpoint instead of `_collections` records: the
// registry is in-memory and only the admin API has a legitimate
// reason to enumerate it; exposing it on /api/collections/* would
// leak schema details to anonymous clients.
func (d *Deps) schemaHandler(w http.ResponseWriter, r *http.Request) {
	specs := registry.Specs()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"collections": specs,
		"count":       len(specs),
	})
}

// settingsListHandler returns every key + value visible to the
// settings.Manager (defaults + persisted overrides). The shape is a
// flat list so the admin UI can sort/filter trivially.
func (d *Deps) settingsListHandler(w http.ResponseWriter, r *http.Request) {
	all, err := d.Settings.List(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list settings"))
		return
	}
	// Stable order — alphabetical key — keeps the UI table predictable.
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]map[string]any, 0, len(all))
	for _, k := range keys {
		out = append(out, map[string]any{"key": k, "value": all[k]})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
}

// settingsPatchHandler upserts a key. Body is a raw JSON value
// (object, array, scalar, anything) passed through to
// settings.Manager.Set. The Manager publishes a change event on the
// eventbus so subscribers (cache invalidators, hot-reload watchers)
// see the new value.
func (d *Deps) settingsPatchHandler(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "key is required"))
		return
	}
	body, err := readBody(r)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	// Decode the raw bytes into a Go value — Manager.Set re-encodes
	// canonically, so we hand it the parsed value rather than raw
	// bytes. Rejecting malformed JSON here keeps eventbus payloads
	// well-formed for downstream subscribers.
	var probe any
	if err := json.Unmarshal(body, &probe); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "value must be valid JSON: %s", err.Error()))
		return
	}
	if err := d.Settings.Set(r.Context(), key, probe); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "set"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "value": probe})
}

// settingsDeleteHandler clears a key. Idempotent — 204 even when the
// row doesn't exist (matches how `railbase config delete` behaves).
func (d *Deps) settingsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "key is required"))
		return
	}
	if err := d.Settings.Delete(r.Context(), key); err != nil {
		// Manager.Delete already swallows "no such row" semantics —
		// any error here is a real DB failure.
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// auditListHandler moved to audit.go in v1.7.11 when filter params
// were added. The handler reads through audit.Writer.ListFiltered +
// Count rather than directly via the pool so the SQL stays in the
// audit package.

// readBody slurps r.Body up to 1MiB. Returns the bytes verbatim so
// the caller can pass them to JSONB upserts without re-encoding.
func readBody(r *http.Request) ([]byte, error) {
	const max = 1 << 20
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	defer r.Body.Close()
	total := 0
	for {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			total += n
			if total > max {
				return nil, fmt.Errorf("body too large (>%d bytes)", max)
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	return buf, nil
}

func parseIntParam(r *http.Request, name string, defaultVal int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

func emptyAsNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
