package adminapi

// v3.x — unified audit Timeline endpoint. The single-screen UI in
// admin/src/screens/logs.tsx fetches from here; the response merges
// _audit_log_site + _audit_log_tenant (chain v2) with a future
// extension point to layer legacy _audit_log (chain v1) on top for
// installations that still have legacy rows.
//
// Design — docs/19-unified-audit.md §6.
//
// Filters (all optional, AND'd):
//
//	actor_type    'system'|'admin'|'api_token'|'job'|'user'
//	actor_id      UUID exact match
//	event         ILIKE substring on the `event` column
//	entity_type   exact match
//	entity_id     exact match
//	outcome       'success'|'denied'|'error'
//	tenant_id     UUID exact match (tenant scope; site rows always
//	              included unless source=tenant)
//	request_id    exact match
//	since, until  RFC3339 bounds on `at`
//	source        'all'|'site'|'tenant' — default 'all'
//	page, perPage standard pagination (perPage default 50, max 500)
//
// Result is a UNION ORDER BY at DESC across the selected sources.
// Site rows have tenant_id=null; tenant rows carry the tenant_id.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	rerr "github.com/railbase/railbase/internal/errors"
)

// timelineWireRow mirrors audit.TimelineRow with JSON-friendly types.
// Notable shape choices:
//
//   - tenant_id is a string (UUID) OR null — never the zero UUID, so
//     the UI can branch on (row.tenant_id == null).
//   - before/after/meta/error_data ride as RawMessage so JSONB lands
//     as native JSON (not double-encoded strings) in the response.
//   - actor.id is null when the actor is system (uuid.Nil).
type timelineWireRow struct {
	Source       string `json:"source"`
	ChainVersion int    `json:"chain_version"`
	Seq          int64  `json:"seq"`
	ID           string `json:"id"`
	At           string `json:"at"`

	TenantID *string `json:"tenant_id"`

	Actor struct {
		Type       string  `json:"type"`
		ID         *string `json:"id"`
		Email      string  `json:"email"`
		Collection string  `json:"collection"`
	} `json:"actor"`

	Event string `json:"event"`

	Entity struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"entity"`

	Outcome string `json:"outcome"`

	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
	Meta      json.RawMessage `json:"meta"`
	ErrorCode string          `json:"error_code"`
	ErrorData json.RawMessage `json:"error_data"`

	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	RequestID string `json:"request_id"`
}

type auditTimelineResponse struct {
	Items      []timelineWireRow `json:"items"`
	Page       int               `json:"page"`
	PerPage    int               `json:"perPage"`
	TotalItems int64             `json:"totalItems"`
}

// auditTimelineHandler — GET /api/_admin/audit/timeline.
//
// Strategy: fetch up to `perPage * page` rows from each requested
// source, merge by `at DESC`, then slice the result. This is O(page)
// DB work per source — same approach as the existing audit list
// endpoint. Heavy paging is bounded by the per-source cap inside
// audit.Store.ListSite/ListTenant (1000).
func (d *Deps) auditTimelineHandler(w http.ResponseWriter, r *http.Request) {
	if d.AuditStore == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "audit store not configured"))
		return
	}

	const defaultPerPage = 50
	const maxPerPage = 500

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
	limit := page * perPage
	if limit > maxPerPage {
		limit = maxPerPage
	}

	f := parseTimelineFilter(r)
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	if source == "" {
		source = "all"
	}

	wantSite := source == "all" || source == "site"
	wantTenant := source == "all" || source == "tenant"

	var (
		merged     []*audit.TimelineRow
		totalItems int64
	)

	if wantSite {
		rows, err := d.AuditStore.ListSite(r.Context(), f, limit)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list site audit"))
			return
		}
		merged = append(merged, rows...)
		c, err := d.AuditStore.CountSite(r.Context(), f)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count site audit"))
			return
		}
		totalItems += c
	}
	if wantTenant {
		rows, err := d.AuditStore.ListTenant(r.Context(), f, limit)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list tenant audit"))
			return
		}
		merged = append(merged, rows...)
		c, err := d.AuditStore.CountTenant(r.Context(), f)
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count tenant audit"))
			return
		}
		totalItems += c
	}

	// Merge sort by at DESC; ties broken by seq DESC (newer seq within
	// the same microsecond wins).
	sort.SliceStable(merged, func(i, j int) bool {
		if !merged[i].At.Equal(merged[j].At) {
			return merged[i].At.After(merged[j].At)
		}
		return merged[i].Seq > merged[j].Seq
	})

	// Slice to the page.
	start := (page - 1) * perPage
	if start > len(merged) {
		merged = nil
	} else {
		end := start + perPage
		if end > len(merged) {
			end = len(merged)
		}
		merged = merged[start:end]
	}

	out := make([]timelineWireRow, 0, len(merged))
	for _, row := range merged {
		out = append(out, encodeTimelineRow(row))
	}

	writeJSON(w, http.StatusOK, auditTimelineResponse{
		Items:      out,
		Page:       page,
		PerPage:    perPage,
		TotalItems: totalItems,
	})
}

// parseTimelineFilter pulls the audit.TimelineFilter out of the
// request query string. Permissive: unknown filter values are
// ignored, not 400'd, so the UI can pass a freeform `event` string
// without worrying about server-side validation rules.
func parseTimelineFilter(r *http.Request) audit.TimelineFilter {
	q := r.URL.Query()
	f := audit.TimelineFilter{
		ActorType:  audit.ActorType(strings.TrimSpace(q.Get("actor_type"))),
		Event:      strings.TrimSpace(q.Get("event")),
		EntityType: strings.TrimSpace(q.Get("entity_type")),
		EntityID:   strings.TrimSpace(q.Get("entity_id")),
		Outcome:    audit.Outcome(strings.TrimSpace(q.Get("outcome"))),
		RequestID:  strings.TrimSpace(q.Get("request_id")),
	}
	if v := strings.TrimSpace(q.Get("actor_id")); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			f.ActorID = u
		}
	}
	if v := strings.TrimSpace(q.Get("tenant_id")); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			f.TenantID = u
		}
	}
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		}
	}
	if v := strings.TrimSpace(q.Get("until")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		}
	}
	return f
}

// encodeTimelineRow turns a TimelineRow into the wire shape. JSONB
// bytes (before/after/meta/error_data) ride as RawMessage so the
// response is a native JSON tree rather than a base64'd blob.
func encodeTimelineRow(r *audit.TimelineRow) timelineWireRow {
	out := timelineWireRow{
		Source:       r.Source,
		ChainVersion: r.ChainVersion,
		Seq:          r.Seq,
		ID:           r.ID.String(),
		At:           r.At.UTC().Format(time.RFC3339Nano),
		Event:        r.Event,
		Outcome:      r.Outcome,
		ErrorCode:    r.ErrorCode,
		IP:           r.IP,
		UserAgent:    r.UserAgent,
		RequestID:    r.RequestID,
	}
	if r.TenantID != uuid.Nil {
		s := r.TenantID.String()
		out.TenantID = &s
	}
	out.Actor.Type = r.ActorType
	if r.ActorID != uuid.Nil {
		s := r.ActorID.String()
		out.Actor.ID = &s
	}
	out.Actor.Email = r.ActorEmail
	out.Actor.Collection = r.ActorCollection
	out.Entity.Type = r.EntityType
	out.Entity.ID = r.EntityID
	out.Before = jsonOrNull(r.Before)
	out.After = jsonOrNull(r.After)
	out.Meta = jsonOrNull(r.Meta)
	out.ErrorData = jsonOrNull(r.ErrorData)
	return out
}

// jsonOrNull returns the raw JSONB bytes as RawMessage, or the JSON
// literal `null` when empty. Without this the empty-string case
// would serialise as `""` (invalid JSON for a structured field).
func jsonOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}
