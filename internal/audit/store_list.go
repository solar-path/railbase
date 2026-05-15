package audit

// Read-side helpers for the unified-audit Store. These back the
// admin /audit/timeline endpoint — they're NOT part of the
// integrity-critical write path.
//
// The Store's read API mirrors legacy Writer.ListFiltered: a single
// filter struct, newest-first ordering, hard-capped limit. Filters
// are AND'd; zero-value fields skip the corresponding predicate.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TimelineFilter constrains a timeline read. All fields optional.
type TimelineFilter struct {
	// ActorType filters to one actor type. Empty = any.
	ActorType ActorType
	// ActorID exact match. uuid.Nil = no filter.
	ActorID uuid.UUID
	// Event ILIKE substring. Empty = no filter.
	Event string
	// EntityType exact match. Empty = no filter.
	EntityType string
	// EntityID exact match. Empty = no filter.
	EntityID string
	// Outcome exact match. Empty = no filter.
	Outcome Outcome
	// TenantID exact match (tenant table only). uuid.Nil = no filter.
	TenantID uuid.UUID
	// RequestID exact match across both tables. Empty = no filter.
	RequestID string
	// Since lower-bounds at. Zero = no lower bound.
	Since time.Time
	// Until upper-bounds at. Zero = no upper bound.
	Until time.Time
}

// TimelineRow is the read-shape returned by ListTimeline. Mirrors the
// admin endpoint's wire JSON 1:1 to keep the handler trivial.
type TimelineRow struct {
	Source          string    // "site" | "tenant"
	ChainVersion    int       // 2 (legacy is 1 — surfaced separately)
	Seq             int64
	ID              uuid.UUID
	At              time.Time
	TenantID        uuid.UUID // uuid.Nil for site rows
	ActorType       string
	ActorID         uuid.UUID
	ActorEmail      string
	ActorCollection string
	Event           string
	EntityType      string
	EntityID        string
	Outcome         string
	Before          []byte // raw JSONB bytes; '' allowed
	After           []byte
	Meta            []byte
	ErrorCode       string
	ErrorData       []byte
	IP              string
	UserAgent       string
	RequestID       string
}

// ListSite returns the most-recent N site rows matching f, newest
// first. limit clamped to [1, 1000].
func (s *Store) ListSite(ctx context.Context, f TimelineFilter, limit int) ([]*TimelineRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	where, args := buildSiteWhere(f)
	q := `SELECT seq, id, at,
                 actor_type,
                 COALESCE(actor_id::text, ''),
                 COALESCE(actor_email, ''),
                 COALESCE(actor_collection, ''),
                 event,
                 COALESCE(entity_type, ''),
                 COALESCE(entity_id, ''),
                 outcome::text,
                 COALESCE(before::text, ''),
                 COALESCE(after::text, ''),
                 COALESCE(meta::text, ''),
                 COALESCE(error_code, ''),
                 COALESCE(error_data::text, ''),
                 COALESCE(ip, ''),
                 COALESCE(user_agent, ''),
                 COALESCE(request_id, '')
            FROM _audit_log_site` + where + fmt.Sprintf(" ORDER BY seq DESC LIMIT %d", limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list site: %w", err)
	}
	defer rows.Close()
	out := make([]*TimelineRow, 0, limit)
	for rows.Next() {
		r := &TimelineRow{Source: "site", ChainVersion: 2}
		var actorID, before, after, meta, errorData string
		if err := rows.Scan(
			&r.Seq, &r.ID, &r.At,
			&r.ActorType,
			&actorID,
			&r.ActorEmail, &r.ActorCollection,
			&r.Event,
			&r.EntityType, &r.EntityID,
			&r.Outcome,
			&before, &after, &meta,
			&r.ErrorCode, &errorData,
			&r.IP, &r.UserAgent, &r.RequestID,
		); err != nil {
			return nil, fmt.Errorf("audit: list site scan: %w", err)
		}
		if actorID != "" {
			if u, err := uuid.Parse(actorID); err == nil {
				r.ActorID = u
			}
		}
		r.Before = []byte(before)
		r.After = []byte(after)
		r.Meta = []byte(meta)
		r.ErrorData = []byte(errorData)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListTenant returns the most-recent N tenant rows matching f. If
// f.TenantID is set, scoped to that tenant; otherwise reads across
// every tenant (caller is responsible for setting up RLS scope on
// the connection if isolation is required).
func (s *Store) ListTenant(ctx context.Context, f TimelineFilter, limit int) ([]*TimelineRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	where, args := buildTenantWhere(f)
	q := `SELECT seq, id, at, tenant_id,
                 actor_type,
                 COALESCE(actor_id::text, ''),
                 COALESCE(actor_email, ''),
                 COALESCE(actor_collection, ''),
                 event,
                 COALESCE(entity_type, ''),
                 COALESCE(entity_id, ''),
                 outcome::text,
                 COALESCE(before::text, ''),
                 COALESCE(after::text, ''),
                 COALESCE(meta::text, ''),
                 COALESCE(error_code, ''),
                 COALESCE(error_data::text, ''),
                 COALESCE(ip, ''),
                 COALESCE(user_agent, ''),
                 COALESCE(request_id, '')
            FROM _audit_log_tenant` + where + fmt.Sprintf(" ORDER BY seq DESC LIMIT %d", limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list tenant: %w", err)
	}
	defer rows.Close()
	out := make([]*TimelineRow, 0, limit)
	for rows.Next() {
		r := &TimelineRow{Source: "tenant", ChainVersion: 2}
		var actorID, before, after, meta, errorData string
		if err := rows.Scan(
			&r.Seq, &r.ID, &r.At, &r.TenantID,
			&r.ActorType,
			&actorID,
			&r.ActorEmail, &r.ActorCollection,
			&r.Event,
			&r.EntityType, &r.EntityID,
			&r.Outcome,
			&before, &after, &meta,
			&r.ErrorCode, &errorData,
			&r.IP, &r.UserAgent, &r.RequestID,
		); err != nil {
			return nil, fmt.Errorf("audit: list tenant scan: %w", err)
		}
		if actorID != "" {
			if u, err := uuid.Parse(actorID); err == nil {
				r.ActorID = u
			}
		}
		r.Before = []byte(before)
		r.After = []byte(after)
		r.Meta = []byte(meta)
		r.ErrorData = []byte(errorData)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountSite returns total rows matching f (for pagination).
func (s *Store) CountSite(ctx context.Context, f TimelineFilter) (int64, error) {
	where, args := buildSiteWhere(f)
	var c int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM _audit_log_site`+where, args...).Scan(&c); err != nil {
		return 0, fmt.Errorf("audit: count site: %w", err)
	}
	return c, nil
}

// CountTenant returns total rows matching f (for pagination).
func (s *Store) CountTenant(ctx context.Context, f TimelineFilter) (int64, error) {
	where, args := buildTenantWhere(f)
	var c int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM _audit_log_tenant`+where, args...).Scan(&c); err != nil {
		return 0, fmt.Errorf("audit: count tenant: %w", err)
	}
	return c, nil
}

// buildSiteWhere builds the WHERE clause for _audit_log_site. Returns
// the leading " WHERE ..." or empty when no predicates apply.
func buildSiteWhere(f TimelineFilter) (string, []any) {
	var clauses []string
	var args []any
	add := func(c string, v any) {
		args = append(args, v)
		clauses = append(clauses, strings.Replace(c, "?", fmt.Sprintf("$%d", len(args)), 1))
	}
	if f.ActorType != "" {
		add("actor_type = ?", string(f.ActorType))
	}
	if f.ActorID != uuid.Nil {
		add("actor_id = ?", f.ActorID)
	}
	if f.Event != "" {
		add("event ILIKE ?", "%"+f.Event+"%")
	}
	if f.EntityType != "" {
		add("entity_type = ?", f.EntityType)
	}
	if f.EntityID != "" {
		add("entity_id = ?", f.EntityID)
	}
	if f.Outcome != "" {
		add("outcome = ?", string(f.Outcome))
	}
	if f.RequestID != "" {
		add("request_id = ?", f.RequestID)
	}
	if !f.Since.IsZero() {
		add("at >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		add("at <= ?", f.Until)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// buildTenantWhere builds the WHERE clause for _audit_log_tenant.
// Same predicates as site plus tenant_id.
func buildTenantWhere(f TimelineFilter) (string, []any) {
	var clauses []string
	var args []any
	add := func(c string, v any) {
		args = append(args, v)
		clauses = append(clauses, strings.Replace(c, "?", fmt.Sprintf("$%d", len(args)), 1))
	}
	if f.TenantID != uuid.Nil {
		add("tenant_id = ?", f.TenantID)
	}
	if f.ActorType != "" {
		add("actor_type = ?", string(f.ActorType))
	}
	if f.ActorID != uuid.Nil {
		add("actor_id = ?", f.ActorID)
	}
	if f.Event != "" {
		add("event ILIKE ?", "%"+f.Event+"%")
	}
	if f.EntityType != "" {
		add("entity_type = ?", f.EntityType)
	}
	if f.EntityID != "" {
		add("entity_id = ?", f.EntityID)
	}
	if f.Outcome != "" {
		add("outcome = ?", string(f.Outcome))
	}
	if f.RequestID != "" {
		add("request_id = ?", f.RequestID)
	}
	if !f.Since.IsZero() {
		add("at >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		add("at <= ?", f.Until)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
