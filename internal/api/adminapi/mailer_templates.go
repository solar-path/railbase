package adminapi

// v1.7.x §3.11 deferred — admin endpoints for browsing the Mailer's
// markdown email templates. Companion to internal/mailer.
//
// Scope: READ-ONLY viewer. The Mailer renders an embedded built-in
// template (`internal/mailer/builtin/<kind>.md`) unless an operator
// has dropped an override file at `<DataDir>/email_templates/<kind>.md`,
// in which case that wins. This admin surface lets the operator
// SEE both states without leaving the UI:
//
//   - GET /api/_admin/mailer-templates           — list 8 kinds with
//                                                  override status
//   - GET /api/_admin/mailer-templates/{kind}    — view one kind:
//                                                  raw markdown + HTML
//
// Editing / save endpoints are intentionally deferred to v1.1.x
// (needs Monaco + content-type detection + preview render +
// validation). Operators override by writing to disk; on next send
// the Mailer's resolver picks the file up.
//
// DataDir resolution mirrors backups.go — read RAILBASE_DATA_DIR
// (fallback `pb_data`) so we don't widen the Deps surface for this
// single read-only slice.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/mailer"
)

// mailerTemplateMeta is the per-kind row in the list response.
// override_exists tells the UI whether `<DataDir>/email_templates/
// <kind>.md` is a regular file; when false, the Mailer falls through
// to the embedded built-in and override_size_bytes/override_modified
// are zero/nil. Mtime carries the RFC3339 string the frontend can
// hand to relativeTime() — null when no override.
type mailerTemplateMeta struct {
	Kind             string     `json:"kind"`
	OverrideExists   bool       `json:"override_exists"`
	OverrideSize     int64      `json:"override_size_bytes"`
	OverrideModified *time.Time `json:"override_modified"`
}

// mailerTemplateView is the response shape for the per-kind viewer.
// `source` is the markdown text the Mailer would render today (override
// wins, else built-in). `html` is the same text piped through
// mailer.RenderMarkdownForCLI — the trusted built-in markdown
// renderer. The two together let the UI swap between Raw and Preview
// tabs without a second round trip.
type mailerTemplateView struct {
	Kind             string     `json:"kind"`
	Source           string     `json:"source"`
	HTML             string     `json:"html"`
	OverrideExists   bool       `json:"override_exists"`
	OverrideSize     int64      `json:"override_size_bytes"`
	OverrideModified *time.Time `json:"override_modified"`
}

// mailerKinds returns the canonical list of kinds the admin UI
// exposes. We delegate to mailer.BuiltinKinds() so the embed.FS in
// `internal/mailer/builtin/` is the single source of truth — if a
// v1.1 slice adds or renames a builtin, this list updates by
// recompiling, no parallel slice to maintain.
//
// Why a function and not a package-level var: BuiltinKinds() walks
// the embed.FS on each call, which is microseconds. Caching here
// would only matter if the admin list endpoint hit was a hot path
// (it isn't).
func mailerKinds() []string {
	return mailer.BuiltinKinds()
}

// mailerTemplatesListHandler — GET /api/_admin/mailer-templates.
//
// Walks the canonical kind list and stats each override file in
// `<DataDir>/email_templates/`. Returns 200 with an envelope shaped
// like {templates: [...]} regardless of whether the override
// directory exists — a fresh deploy without overrides is a normal,
// empty state, not 404.
func (d *Deps) mailerTemplatesListHandler(w http.ResponseWriter, r *http.Request) {
	dataDir, err := dataDirFromEnv()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve data dir"))
		return
	}
	overrideDir := filepath.Join(dataDir, "email_templates")

	kinds := mailerKinds()
	items := make([]mailerTemplateMeta, 0, len(kinds))
	for _, k := range kinds {
		items = append(items, statOverride(overrideDir, k))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"templates": items,
	})
}

// mailerTemplatesViewHandler — GET /api/_admin/mailer-templates/{kind}.
//
// kind must be one of mailerKinds() — anything else → 404 (typed
// envelope, not a bare HTTP code). When the override file is
// present, source is the file contents; otherwise it's the embedded
// built-in. html is the rendered HTML — safe to dangerously-set on
// the frontend because the markdown renderer is a fixed allowlist
// (see internal/mailer/markdown.go).
func (d *Deps) mailerTemplatesViewHandler(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if !isKnownKind(kind) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "unknown mailer template kind: %s", kind))
		return
	}

	dataDir, err := dataDirFromEnv()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve data dir"))
		return
	}
	overrideDir := filepath.Join(dataDir, "email_templates")
	meta := statOverride(overrideDir, kind)

	var source string
	if meta.OverrideExists {
		body, readErr := os.ReadFile(filepath.Join(overrideDir, kind+".md"))
		if readErr != nil {
			// Race: file vanished between stat and read. Fall back to
			// the built-in so the viewer still has something useful.
			meta.OverrideExists = false
			meta.OverrideSize = 0
			meta.OverrideModified = nil
		} else {
			source = string(body)
		}
	}
	if source == "" {
		if builtin, ok := mailer.BuiltinSource(kind); ok {
			source = builtin
		}
	}

	view := mailerTemplateView{
		Kind:             kind,
		Source:           source,
		HTML:             mailer.RenderMarkdownForCLI(source),
		OverrideExists:   meta.OverrideExists,
		OverrideSize:     meta.OverrideSize,
		OverrideModified: meta.OverrideModified,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(view)
}

// statOverride returns the meta row for one kind. A missing override
// is the empty struct (OverrideExists=false, zero size, nil mtime) —
// the frontend renders the "(built-in default)" affordance from
// that. We deliberately skip non-regular files (directories,
// symlinks-to-directories) so an operator can't accidentally point
// the override slot at a directory.
func statOverride(overrideDir, kind string) mailerTemplateMeta {
	out := mailerTemplateMeta{Kind: kind}
	info, err := os.Stat(filepath.Join(overrideDir, kind+".md"))
	if err != nil || !info.Mode().IsRegular() {
		return out
	}
	out.OverrideExists = true
	out.OverrideSize = info.Size()
	t := info.ModTime().UTC()
	out.OverrideModified = &t
	return out
}

// isKnownKind reports whether kind appears in the canonical builtin
// list. We re-read the embed list each call (cheap) so the kind set
// stays in lockstep with the embed.FS without a cache layer.
func isKnownKind(kind string) bool {
	for _, k := range mailerKinds() {
		if k == kind {
			return true
		}
	}
	return false
}
