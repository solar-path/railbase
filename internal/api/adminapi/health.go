package adminapi

// v1.7.x §3.11 — admin Health / metrics dashboard endpoint.
//
// Single aggregating route:
//
//	GET /api/_admin/health  → consolidated metrics envelope
//
// Backs the admin UI's Health screen (admin/src/screens/health.tsx).
// The screen polls every 5 s; every subsystem we surface is read-only
// and side-effect-free.
//
// Design rules baked into this file:
//
//   - Every subsystem is nil-guarded / try-and-recover. The dashboard
//     must still render if a particular piece is wired down (no logs
//     persistence, no realtime broker, etc.). We never return 500 just
//     because one of N sources is missing — we emit zeros/empty for
//     that subsystem and move on.
//
//   - StartedAt lazy-init: Deps.StartedAt is set on first call when
//     zero, so this slice doesn't have to widen the app.go wire-up. A
//     mutex protects the read-modify-write; the lock-free read after
//     first init is the common case via atomic load semantics on the
//     time.Time copy (Go's memory model treats the field write under
//     the mutex as the happens-before for subsequent unlocked reads
//     once we've seen non-zero).
//
//   - Counts use bounded queries with short timeouts so a slow DB
//     doesn't stall the dashboard for the entire 5 s poll window. The
//     handler-wide timeout is 5 s; subsystem failures degrade to zero
//     for that section, never block the response.

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// startedAtMu serialises the lazy-init of Deps.StartedAt. The field
// itself is written exactly once across the lifetime of a Deps; after
// that, every reader sees a non-zero copy. Package-level (not on Deps)
// to avoid widening the exported struct with sync primitives — Deps is
// constructed by value in many test sites.
var startedAtMu sync.Mutex

// mountHealth registers the consolidated health/metrics endpoint.
// Always registered: each subsystem nil-guards internally so the
// dashboard renders even when individual subsystems are wired down.
func (d *Deps) mountHealth(r chi.Router) {
	r.Get("/health", d.healthHandler)
}

// healthPool is the subset of pgxpool.Pool we need. Pulled into a
// small interface so the no-pool case is trivially zero-valued and the
// tests can pass a nil pool without panicking.
type healthPool interface {
	// (kept for forward compat — currently we just call Stat() directly
	// on the concrete pool; the interface is unused, but the design
	// space stays open.)
}

// healthResponse is the wire shape. JSON tags match the spec in the
// admin UI screen 1:1 so the React side can consume the envelope
// without reshaping.
type healthResponse struct {
	Version   string    `json:"version"`
	GoVersion string    `json:"go_version"`
	UptimeSec int64     `json:"uptime_sec"`
	StartedAt time.Time `json:"started_at"`
	Now       time.Time `json:"now"`

	Pool     healthPoolStats     `json:"pool"`
	Memory   healthMemoryStats   `json:"memory"`
	Jobs     healthJobsStats     `json:"jobs"`
	Audit    healthAuditStats    `json:"audit"`
	Logs     healthLogsStats     `json:"logs"`
	Realtime healthRealtimeStats `json:"realtime"`
	Backups  healthBackupsStats  `json:"backups"`
	Schema   healthSchemaStats   `json:"schema"`

	RequestID string `json:"request_id,omitempty"`
}

type healthPoolStats struct {
	Acquired int32 `json:"acquired"`
	Idle     int32 `json:"idle"`
	Total    int32 `json:"total"`
	Max      int32 `json:"max"`
}

type healthMemoryStats struct {
	AllocBytes      uint64 `json:"alloc_bytes"`
	TotalAllocBytes uint64 `json:"total_alloc_bytes"`
	SysBytes        uint64 `json:"sys_bytes"`
	NumGC           uint32 `json:"num_gc"`
	Goroutines      int    `json:"goroutines"`
}

type healthJobsStats struct {
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Failed    int64 `json:"failed"`
	Completed int64 `json:"completed"`
	Total     int64 `json:"total"`
}

type healthAuditStats struct {
	Total   int64 `json:"total"`
	Last24h int64 `json:"last_24h"`
}

type healthLogsStats struct {
	Total   int64            `json:"total"`
	Last24h int64            `json:"last_24h"`
	ByLevel map[string]int64 `json:"by_level"`
}

type healthRealtimeStats struct {
	Subscriptions     int    `json:"subscriptions"`
	EventsDroppedTotal uint64 `json:"events_dropped_total"`
}

type healthBackupsStats struct {
	Count           int        `json:"count"`
	TotalBytes      int64      `json:"total_bytes"`
	LastCompletedAt *time.Time `json:"last_completed_at"`
}

type healthSchemaStats struct {
	Collections       int `json:"collections"`
	AuthCollections   int `json:"auth_collections"`
	TenantCollections int `json:"tenant_collections"`
}

// healthHandler — GET /api/_admin/health.
//
// Aggregates every metric we have without taking any locks the actual
// workload would. Subsystem failures degrade to zero for that section.
func (d *Deps) healthHandler(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	startedAt := d.ensureStartedAt(now)

	// Bound per-subsystem DB queries so a stuck DB doesn't stall the
	// poll loop. 3 s is generous: the dashboard polls every 5 s and we
	// want the response to be back in time for the next request even
	// when one query is misbehaving.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	resp := healthResponse{
		Version:   buildinfo.String(),
		GoVersion: runtime.Version(),
		StartedAt: startedAt.UTC(),
		Now:       now,
		UptimeSec: int64(now.Sub(startedAt).Seconds()),
		RequestID: chimw.GetReqID(r.Context()),
	}

	resp.Pool = collectPoolStats(d)
	resp.Memory = collectMemoryStats()
	resp.Jobs = collectJobsStats(ctx, d)
	resp.Audit = collectAuditStats(ctx, d)
	resp.Logs = collectLogsStats(ctx, d)
	resp.Realtime = collectRealtimeStats(d)
	resp.Backups = collectBackupsStats(ctx, d)
	resp.Schema = collectSchemaStats()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(&resp)
}

// ensureStartedAt returns the StartedAt field on Deps, initialising it
// to `now` on the first call when zero. The lock is package-level —
// see the note at the top of the file. After init the field is read
// without locking; the racy zero/non-zero check is safe because the
// only allowed transition is once-zero-to-once-set.
func (d *Deps) ensureStartedAt(now time.Time) time.Time {
	startedAtMu.Lock()
	defer startedAtMu.Unlock()
	if d.StartedAt.IsZero() {
		d.StartedAt = now
	}
	return d.StartedAt
}

// collectPoolStats reads the pgxpool's current stats. Returns zeros
// when the pool is nil (test Deps).
func collectPoolStats(d *Deps) healthPoolStats {
	if d == nil || d.Pool == nil {
		return healthPoolStats{}
	}
	s := d.Pool.Stat()
	return healthPoolStats{
		Acquired: s.AcquiredConns(),
		Idle:     s.IdleConns(),
		Total:    s.TotalConns(),
		Max:      s.MaxConns(),
	}
}

// collectMemoryStats reads runtime memory + goroutine counts. Cheap —
// readMemStats acquires a global lock for ~microseconds. We don't
// disable GC or do anything fancy; the cost is acceptable at a 5 s
// poll cadence.
func collectMemoryStats() healthMemoryStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return healthMemoryStats{
		AllocBytes:      ms.Alloc,
		TotalAllocBytes: ms.TotalAlloc,
		SysBytes:        ms.Sys,
		NumGC:           ms.NumGC,
		Goroutines:      runtime.NumGoroutine(),
	}
}

// collectJobsStats runs the GROUP BY against `_jobs`. Returns zeros
// when the pool is nil or the query fails (e.g. table missing in a
// fresh test schema). The bucket names match what the jobs.Status
// enum produces; "cancelled" is intentionally absent from the spec
// envelope (operators rarely look for it).
func collectJobsStats(ctx context.Context, d *Deps) healthJobsStats {
	out := healthJobsStats{}
	if d == nil || d.Pool == nil {
		return out
	}
	rows, err := d.Pool.Query(ctx, `SELECT status, count(*) FROM _jobs GROUP BY status`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			continue
		}
		switch status {
		case "pending":
			out.Pending = n
		case "running":
			out.Running = n
		case "failed":
			out.Failed = n
		case "completed":
			out.Completed = n
		}
		out.Total += n
	}
	return out
}

// collectAuditStats counts `_audit_log` rows total + in the trailing
// 24h. Nil-guarded against d.Audit (the writer is the canonical signal
// for "audit is wired"; the pool alone isn't enough since some tests
// run the pool without the writer).
func collectAuditStats(ctx context.Context, d *Deps) healthAuditStats {
	out := healthAuditStats{}
	if d == nil || d.Audit == nil || d.Pool == nil {
		return out
	}
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM _audit_log`).Scan(&out.Total)
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM _audit_log WHERE at > now() - interval '24 hours'`).Scan(&out.Last24h)
	return out
}

// collectLogsStats counts `_logs` rows total + last 24h + the by-level
// breakdown. The level column is lowercase ("debug" / "info" / "warn"
// / "error") per the migration; we emit those keys verbatim. Returns
// an empty map (never nil) so the React side can `.values()` without
// guarding.
func collectLogsStats(ctx context.Context, d *Deps) healthLogsStats {
	out := healthLogsStats{
		ByLevel: map[string]int64{},
	}
	if d == nil || d.Pool == nil {
		return out
	}
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM _logs`).Scan(&out.Total)
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM _logs WHERE created > now() - interval '24 hours'`).Scan(&out.Last24h)
	rows, err := d.Pool.Query(ctx, `SELECT level, count(*) FROM _logs GROUP BY level`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var level string
		var n int64
		if err := rows.Scan(&level, &n); err != nil {
			continue
		}
		out.ByLevel[level] = n
	}
	return out
}

// collectRealtimeStats reads the broker's Snapshot() and sums per-sub
// drop counts. The Snapshot is cheap (single mutex acquire); we don't
// need a separate counter on the broker.
func collectRealtimeStats(d *Deps) healthRealtimeStats {
	out := healthRealtimeStats{}
	if d == nil || d.Realtime == nil {
		return out
	}
	stats := d.Realtime.Snapshot()
	out.Subscriptions = stats.SubscriptionCount
	for _, s := range stats.Subscriptions {
		out.EventsDroppedTotal += s.Dropped
	}
	return out
}

// collectBackupsStats walks the on-disk backups directory. The spec
// references a `_backups` table but Railbase keeps backups on the
// filesystem (see internal/api/adminapi/backups.go); we mirror that
// model so the dashboard reflects what `railbase backup list` shows.
// Returns zeros when DataDir resolution fails or the directory is
// missing — both are normal on fresh deploys.
func collectBackupsStats(_ context.Context, _ *Deps) healthBackupsStats {
	out := healthBackupsStats{}
	dataDir, err := dataDirFromEnv()
	if err != nil {
		return out
	}
	items, err := listBackupItems(dataDir)
	if err != nil {
		return out
	}
	out.Count = len(items)
	for i := range items {
		out.TotalBytes += items[i].SizeBytes
		t := items[i].Created
		if out.LastCompletedAt == nil || t.After(*out.LastCompletedAt) {
			c := t
			out.LastCompletedAt = &c
		}
	}
	return out
}

// collectSchemaStats walks the registry counting collections by kind.
// Always available — the registry is in-memory and populated at
// init() time. Returns zeros when the registry is empty (e.g. tests
// that haven't registered any collections).
func collectSchemaStats() healthSchemaStats {
	cols := registry.All()
	out := healthSchemaStats{Collections: len(cols)}
	for _, c := range cols {
		spec := c.Spec()
		if spec.Auth {
			out.AuthCollections++
		}
		if spec.Tenant {
			out.TenantCollections++
		}
	}
	return out
}

// silence so a future refactor that swaps to an interface-typed pool
// doesn't break the import graph.
var _ = builder.CollectionSpec{}
