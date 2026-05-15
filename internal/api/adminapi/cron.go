package adminapi

// Admin endpoints for the persisted cron schedules table (`_cron`).
// Mirror of the `railbase cron ...` CLI: list / upsert / enable /
// disable / run-now / delete, plus a small `/cron/kinds` helper that
// exposes the registered job kinds for the admin-UI's "kind" picker.
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /cron                       list every schedule
//	GET    /cron/kinds                 known job kinds (registry.Kinds())
//	POST   /cron                       upsert {name, expression, kind, payload, enabled}
//	POST   /cron/{name}/enable         SetEnabled(true)
//	POST   /cron/{name}/disable        SetEnabled(false)
//	POST   /cron/{name}/run-now        materialise one job now
//	DELETE /cron/{name}                delete (refused for builtins)
//
// Nil-guard discipline: mountCron skips registration when
// d.CronJobs or d.JobRegistry is nil — bare-Deps test paths get clean
// 404s rather than nil-derefs. The same shape as mountWebhooks /
// mountStripe.
//
// Builtin protection. `jobs.DefaultSchedules()` lists the rows
// Railbase re-upserts on every first boot (cleanup_*, audit_seal,
// flush_deferred_notifications, ...). Operators legitimately want to
// retune those — bumping `cleanup_logs` from daily to hourly is fair.
// What they should NOT be able to do from the admin UI is:
//
//   - delete a builtin (it would just come back on next restart, and
//     the gap is a real footgun);
//   - change its `kind` (the name is bound to a specific handler;
//     pointing `cleanup_logs` at `scheduled_backup` would be confusing
//     at best, destructive at worst).
//
// The upsert + delete handlers enforce both. Enable/disable and
// expression edits stay open for builtins.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/jobs"
)

// cronJSON is the wire-shape for a single schedule. Mirrors CronRow
// with snake_case keys + two derived booleans (is_builtin, kind_known)
// the admin UI uses for row-action gating + a warning badge when an
// old schedule references a kind the current binary doesn't register.
type cronJSON struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Expression string          `json:"expression"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	Enabled    bool            `json:"enabled"`
	LastRunAt  *time.Time      `json:"last_run_at"`
	NextRunAt  *time.Time      `json:"next_run_at"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	IsBuiltin  bool            `json:"is_builtin"`
	KindKnown  bool            `json:"kind_known"`
}

// cronUpsertBody is the create/update request shape. `payload` rides
// as raw JSON so the per-kind shape (e.g. scheduled_backup's
// {retention_days, out_dir}) is opaque here and validated downstream
// by the handler when it materialises a job.
type cronUpsertBody struct {
	Name       string          `json:"name"`
	Expression string          `json:"expression"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	Enabled    *bool           `json:"enabled"` // pointer: omit ⇒ keep server-side default
}

// mountCron registers the cron admin surface when both stores are
// wired. Both checks: CronJobs is the persistence dep, JobRegistry
// powers the kind-allowlist + /cron/kinds discovery.
func (d *Deps) mountCron(r chi.Router) {
	if d.CronJobs == nil || d.JobRegistry == nil {
		return
	}
	r.Get("/cron", d.cronListHandler)
	r.Get("/cron/kinds", d.cronKindsHandler)
	r.Post("/cron", d.cronUpsertHandler)
	r.Post("/cron/{name}/enable", d.cronEnableHandler(true))
	r.Post("/cron/{name}/disable", d.cronEnableHandler(false))
	r.Post("/cron/{name}/run-now", d.cronRunNowHandler)
	r.Delete("/cron/{name}", d.cronDeleteHandler)
}

// builtinNames returns the set of schedule names Railbase re-upserts
// on first boot. Recomputed per request — DefaultSchedules() is a
// hand-maintained slice (~10 entries), the cost is negligible and
// avoids a caching layer that could drift behind the source.
func builtinNames() map[string]struct{} {
	defaults := jobs.DefaultSchedules()
	out := make(map[string]struct{}, len(defaults))
	for _, s := range defaults {
		out[s.Name] = struct{}{}
	}
	return out
}

// kindKnownSet returns the registered job kinds as a set. Cheap
// helper — Registry.Kinds() returns a freshly-allocated slice.
func kindKnownSet(reg *jobs.Registry) map[string]struct{} {
	kinds := reg.Kinds()
	out := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		out[k] = struct{}{}
	}
	return out
}

// rowToJSON converts a CronRow into the wire shape. `payload` is
// passed through verbatim — the column is JSONB so it's already
// canonical JSON bytes.
func rowToJSON(r *jobs.CronRow, builtins map[string]struct{}, kinds map[string]struct{}) cronJSON {
	payload := json.RawMessage(r.Payload)
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	_, isBuiltin := builtins[r.Name]
	_, kindKnown := kinds[r.Kind]
	return cronJSON{
		ID:         r.ID.String(),
		Name:       r.Name,
		Expression: r.Expression,
		Kind:       r.Kind,
		Payload:    payload,
		Enabled:    r.Enabled,
		LastRunAt:  r.LastRunAt,
		NextRunAt:  r.NextRunAt,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
		IsBuiltin:  isBuiltin,
		KindKnown:  kindKnown,
	}
}

// cronListHandler — GET /api/_admin/cron.
//
// Returns every schedule sorted by name (CronStore.List() already
// orders). No pagination: operator-sized table tops out at ~20 rows.
func (d *Deps) cronListHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := d.CronJobs.List(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list cron schedules"))
		return
	}
	builtins := builtinNames()
	kinds := kindKnownSet(d.JobRegistry)
	out := make([]cronJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToJSON(row, builtins, kinds))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// cronKindsHandler — GET /api/_admin/cron/kinds.
//
// Returns the list of registered job kinds — the admin UI's "kind"
// picker uses this to constrain the input to handlers the running
// binary can actually execute. Order is not stable (Registry.Kinds()
// iterates a map); callers should sort on the UI side.
func (d *Deps) cronKindsHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"kinds": d.JobRegistry.Kinds()})
}

// cronNameRegex is the same superset-of-CLI rule used by the
// `railbase cron upsert` CLI: identifier-ish, no whitespace, no
// slashes (URL safety). Keep this strict — names are part of every
// path-param URL and surface in operator-facing log lines.
func validCronName(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// cronUpsertHandler — POST /api/_admin/cron.
//
// Body: {name, expression, kind, payload, enabled?}. Validates
// every field up-front:
//
//   - name: identifier-ish (validCronName)
//   - expression: jobs.ParseCron — surface form + value ranges
//   - kind: must be in the registered allowlist (Registry.Kinds())
//   - for builtin names (jobs.DefaultSchedules): kind must match the
//     existing row's kind. Expression + enabled may be retuned freely.
//
// Then upserts via CronStore.Upsert and, if `enabled` was supplied,
// calls SetEnabled in a follow-up — Upsert's INSERT path hard-codes
// enabled=TRUE and ON CONFLICT doesn't touch it, so the toggle has to
// happen separately.
func (d *Deps) cronUpsertHandler(w http.ResponseWriter, r *http.Request) {
	var body cronUpsertBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Expression = strings.TrimSpace(body.Expression)
	body.Kind = strings.TrimSpace(body.Kind)

	if !validCronName(body.Name) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"name must be 1-80 chars: letters, digits, underscore, hyphen"))
		return
	}
	if _, err := jobs.ParseCron(body.Expression); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation,
			"expression: %s", err.Error()))
		return
	}
	kinds := kindKnownSet(d.JobRegistry)
	if _, ok := kinds[body.Kind]; !ok {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"kind %q is not a registered handler — see GET /api/_admin/cron/kinds",
			body.Kind))
		return
	}

	// Builtin-protection: refuse to repoint a builtin schedule at a
	// different kind. We have to consult the existing row for this — a
	// fresh upsert for a builtin name is fine as long as the kind
	// matches.
	builtins := builtinNames()
	if _, isBuiltin := builtins[body.Name]; isBuiltin {
		existing, err := d.CronJobs.Get(r.Context(), body.Name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup builtin schedule"))
			return
		}
		if existing != nil && existing.Kind != body.Kind {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
				"%q is a builtin schedule; kind is locked to %q", body.Name, existing.Kind))
			return
		}
	}

	// Empty / nil payload → "{}". CronStore.Upsert routes RawMessage
	// through encodePayload which handles both cases, but we
	// normalise here so the round-tripped response is predictable.
	payload := body.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}

	row, err := d.CronJobs.Upsert(r.Context(), body.Name, body.Expression, body.Kind, payload)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "upsert cron schedule"))
		return
	}

	// Optional enable flag. Upsert hardcodes enabled=TRUE on the
	// INSERT branch and preserves the existing value on conflict, so
	// we only call SetEnabled when the body explicitly opts in/out.
	if body.Enabled != nil && row.Enabled != *body.Enabled {
		if err := d.CronJobs.SetEnabled(r.Context(), body.Name, *body.Enabled); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "set enabled"))
			return
		}
		row.Enabled = *body.Enabled
	}

	writeJSON(w, http.StatusOK, rowToJSON(row, builtins, kinds))
}

// cronEnableHandler returns a handler that flips `enabled` to the
// fixed value. Two-instance closure so the route registration stays
// readable (`d.cronEnableHandler(true)` vs `(false)`).
func (d *Deps) cronEnableHandler(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(chi.URLParam(r, "name"))
		if !validCronName(name) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid schedule name"))
			return
		}
		// Existence check first — SetEnabled is silently idempotent on
		// missing rows, but the operator deserves a 404 when the name
		// is wrong.
		if _, err := d.CronJobs.Get(r.Context(), name); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "schedule %q not found", name))
				return
			}
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "get schedule"))
			return
		}
		if err := d.CronJobs.SetEnabled(r.Context(), name, enabled); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "set enabled"))
			return
		}
		row, err := d.CronJobs.Get(r.Context(), name)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "reload schedule"))
			return
		}
		writeJSON(w, http.StatusOK, rowToJSON(row, builtinNames(), kindKnownSet(d.JobRegistry)))
	}
}

// cronRunNowHandler — POST /api/_admin/cron/{name}/run-now.
//
// Materialises one job from the schedule immediately without touching
// next_run_at. Returns 200 with the new job id; 404 when the name is
// missing OR disabled (RunNow's WHERE clause filters on enabled=TRUE).
// We distinguish the two cases for the UI — operators expect a useful
// "schedule disabled" message rather than a generic 404.
func (d *Deps) cronRunNowHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if !validCronName(name) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid schedule name"))
		return
	}
	existing, err := d.CronJobs.Get(r.Context(), name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "schedule %q not found", name))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "get schedule"))
		return
	}
	if !existing.Enabled {
		rerr.WriteJSON(w, rerr.New(rerr.CodePreconditionFailed,
			"schedule %q is disabled; enable it first", name))
		return
	}
	jobID, ok, err := d.CronJobs.RunNow(r.Context(), name)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "run-now"))
		return
	}
	if !ok {
		// Race: the schedule was disabled or deleted between the Get
		// above and the RunNow INSERT. Surface it cleanly.
		rerr.WriteJSON(w, rerr.New(rerr.CodeConflict,
			"schedule %q is no longer enabled", name))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": jobID.String()})
}

// cronDeleteHandler — DELETE /api/_admin/cron/{name}.
//
// Refuses to delete builtin schedules (jobs.DefaultSchedules) — they
// would just be re-upserted on the next restart, and the gap is a
// silent footgun. Operators who really want one gone use the CLI:
// `railbase cron delete <name>`.
func (d *Deps) cronDeleteHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if !validCronName(name) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid schedule name"))
		return
	}
	if _, isBuiltin := builtinNames()[name]; isBuiltin {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"%q is a builtin schedule and cannot be deleted from the admin UI", name))
		return
	}
	if err := d.CronJobs.Delete(r.Context(), name); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete schedule"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
