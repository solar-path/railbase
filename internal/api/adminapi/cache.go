package adminapi

// v1.7.x §3.11 — admin endpoints exposing the cache.Registry built in
// v1.5.1. Read-only listing + a manual Clear action per instance.
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /cache                  list every registered cache + Stats()
//	POST   /cache/{name}/clear     drop entries + zero counters
//
// Why server-side hit_rate_pct: javascript's number type can drift on
// integer division when the denominator gets large (Hits+Misses well
// past 2^53). Computing in Go on int64 and emitting a pre-rounded
// percentage avoids the floating-point surprise and keeps the wire
// format stable for future Prometheus scrapers.
//
// No filtering / paging: the registry tops out at the handful of
// caches a single binary spins up (roles resolver, settings, jobs,
// schema, etc.). A flat sorted list keeps the UI shape predictable.
//
// Always-registered: unlike webhooks/realtime/api-tokens, the cache
// surface doesn't require any Deps wiring — the registry is a package-
// global. Tests just Register/Unregister directly. Empty registry
// returns an empty array, not a 503.

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/cache"
	rerr "github.com/railbase/railbase/internal/errors"
)

// mountCache registers the cache inspector routes. Mirrors the
// mountWebhooks / mountRealtime sibling shape — handler + tests live
// together in this file. No nil-guard on a Deps field: the cache
// registry is a package-global, not a Deps-borne handle.
func (d *Deps) mountCache(r chi.Router) {
	r.Get("/cache", d.cacheListHandler)
	r.Post("/cache/{name}/clear", d.cacheClearHandler)
}

// cacheStatsJSON is the wire shape per instance. Mirrors cache.Stats
// field-for-field plus the server-side-computed HitRatePct and the
// MaxApprox derivation from the registry-known Capacity (when we
// know it; today we don't surface Options through the StatsProvider
// interface, so MaxApprox is omitted — the UI shows Size/— for the
// max column).
//
// hit_rate_pct: percentage rounded to one decimal. Zero requests
// (hits+misses == 0) reports 0.0 so the UI doesn't render NaN.
type cacheStatsJSON struct {
	Hits        int64   `json:"hits"`
	Misses      int64   `json:"misses"`
	HitRatePct  float64 `json:"hit_rate_pct"`
	Loads       int64   `json:"loads"`
	LoadFails   int64   `json:"load_fails"`
	Evictions   int64   `json:"evictions"`
	Size        int     `json:"size"`
}

type cacheInstanceJSON struct {
	Name  string         `json:"name"`
	Stats cacheStatsJSON `json:"stats"`
}

// cacheListResponse is the GET envelope. Flat `instances` array;
// sorted by name for stable UI rendering across polls.
type cacheListResponse struct {
	Instances []cacheInstanceJSON `json:"instances"`
}

// cacheListHandler — GET /api/_admin/cache.
//
// Snapshots every entry in the registry, computes the server-side
// hit rate, and emits the flat array. Empty registry → empty array
// (no 503): operators expect "no caches yet" to be a normal state
// during the gradual per-subsystem wire-up that follows the v1.5.1
// primitive ship.
func (d *Deps) cacheListHandler(w http.ResponseWriter, _ *http.Request) {
	snapshot := cache.All()

	// Stable order: sort by name. UI columns stay put across polls,
	// and a deterministic shape makes the response cacheable downstream
	// by anything that hashes the bytes.
	names := make([]string, 0, len(snapshot))
	for n := range snapshot {
		names = append(names, n)
	}
	sort.Strings(names)

	items := make([]cacheInstanceJSON, 0, len(names))
	for _, n := range names {
		provider := snapshot[n]
		s := provider.Stats()
		items = append(items, cacheInstanceJSON{
			Name:  n,
			Stats: shapeCacheStats(s),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(cacheListResponse{Instances: items})
}

// shapeCacheStats maps cache.Stats → wire shape, including the
// server-side hit-rate computation. Extracted so the unit tests can
// exercise the rounding behaviour without going through the handler.
func shapeCacheStats(s cache.Stats) cacheStatsJSON {
	total := s.Hits + s.Misses
	var rate float64
	if total > 0 {
		// Compute in float64 — int64 division would zero out. Round
		// to one decimal so the UI shows e.g. "92.3%" not "92.30000001%".
		rate = float64(s.Hits) / float64(total) * 100
		rate = float64(int64(rate*10+0.5)) / 10
	}
	return cacheStatsJSON{
		Hits:       s.Hits,
		Misses:     s.Misses,
		HitRatePct: rate,
		Loads:      s.Loads,
		LoadFails:  s.LoadFails,
		Evictions:  s.Evictions,
		Size:       s.Size,
	}
}

// cacheClearHandler — POST /api/_admin/cache/{name}/clear.
//
// 204 on success, 404 if the name isn't registered. Emits an
// audit event (`cache.cleared`) so the action is replayable; we
// stash the cleared name in Before for the audit timeline since the
// per-instance stats snapshot is irrelevant once we've zeroed it.
//
// No request body: there's nothing to configure on a clear. The
// route's path param IS the action target.
func (d *Deps) cacheClearHandler(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name is required"))
		return
	}
	provider, ok := cache.Get(name)
	if !ok {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "cache not registered"))
		return
	}
	provider.Clear()

	// v3.x — entity_type="cache", entity_id=<name>. Timeline filter
	// «всё про эту cache namespace» хитит индекс.
	writeAuditEntity(r.Context(), d, EntityAuditInput{
		Event:      "cache.cleared",
		EntityType: "cache",
		EntityID:   name,
		Outcome:    audit.OutcomeSuccess,
	}, r)

	w.WriteHeader(http.StatusNoContent)
}
