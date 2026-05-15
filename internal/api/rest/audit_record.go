package rest

// Per-record CRUD audit emission (v3.x unified audit).
//
// Enabled per-collection via CollectionSpec.Audit. When the flag is
// off the helpers below are zero-cost short-circuits. When on every
// Create / Update / Delete fires one event into the audit chain
// AFTER commit, so a rolled-back transaction doesn't leave a phantom
// audit row.
//
// Routing site vs tenant happens INSIDE the audit Writer — since
// the legacy Writer has the v3 Store attached, a non-nil TenantID
// here lands in `_audit_log_tenant` (per-tenant chain), an empty one
// lands in `_audit_log_site`. From the call-site's perspective both
// look like a single Audit.Write call.
//
// Actor extraction reads the request's auth Principal — ctx-aware,
// graceful when anonymous (audit event still emits, actor_id=Nil,
// user_collection="" → ActorSystem on the v3 side). The legacy
// Audit.Write redaction allow-list (security.IsSensitiveKey) covers
// password_hash / token_key / *_token fields before persist.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/tenant"
)

// recordAuditVerb is the small enum of CRUD verbs the rest layer
// emits. The v3 event name becomes "<collection>.<verb>".
type recordAuditVerb string

const (
	recordVerbCreated recordAuditVerb = "created"
	recordVerbUpdated recordAuditVerb = "updated"
	recordVerbDeleted recordAuditVerb = "deleted"
)

// emitRecordAudit emits one v3 audit event for the given CRUD verb
// on a record of spec. Short-circuits when spec.Audit is off or the
// Writer is unwired. Called POST-commit so a rolled-back tx never
// produces a phantom audit row.
//
// before / after carry the record diff:
//
//	create: before = nil,        after = inserted row
//	update: before = pre-image,  after = post-image
//	delete: before = pre-image,  after = nil
//
// recordID is the canonical PK — pulled from the row map by the
// caller so this helper doesn't have to know about composite keys
// or non-`id` PKs (future-proofing).
func emitRecordAudit(
	r *http.Request,
	w *audit.Writer,
	spec builder.CollectionSpec,
	verb recordAuditVerb,
	recordID string,
	before, after any,
) {
	if w == nil || !spec.Audit {
		return
	}
	ctx := r.Context()
	event := spec.Name + "." + string(verb)

	// Tenant routing. Tenant collections always carry a tenant_id in
	// ctx (middleware enforces it); we use it both as the Event field
	// and as the bare-pool tenant scope. Non-tenant collections leave
	// TenantID at uuid.Nil so the Writer forwards to _audit_log_site.
	var tenantID uuid.UUID
	if spec.Tenant && tenant.HasID(ctx) {
		tenantID = tenant.ID(ctx)
	}

	// Actor extraction. authmw.PrincipalFrom is nil-safe and returns
	// the zero Principal when unauthenticated; we map that to an
	// empty UserID / UserCollection so the Writer's transparent
	// forward classifies it as system (rare — public-rule CRUD).
	userID := uuid.Nil
	userCollection := ""
	if p := authmw.PrincipalFrom(ctx); p.Authenticated() {
		userID = p.UserID
		userCollection = p.CollectionName
	}

	// Tag entity in the after payload so the v3 Store's forward path
	// picks up entity_type + entity_id without extending the legacy
	// Event struct. The Writer's forwardToStore reads these from a
	// dedicated convention key — see internal/audit/audit.go.
	_, _ = w.Write(ctx, audit.Event{
		UserID:         userID,
		UserCollection: userCollection,
		TenantID:       tenantID,
		Event:          event,
		Outcome:        audit.OutcomeSuccess,
		Before:         before,
		After:          after,
		IP:             clientIPFromRequest(ctx, r),
		UserAgent:      r.Header.Get("User-Agent"),
	})

	// Also stamp entity_type/entity_id by an explicit Store write
	// when the Store reference is reachable through the Writer. We
	// reach for it via a small adapter to avoid plumbing Store into
	// handlerDeps separately — the Writer already has it.
	if store := writerStore(w); store != nil {
		entityWrite(ctx, store, spec, verb, recordID, userID, userCollection, tenantID, before, after, r)
	}
}

// writerStore reaches into audit.Writer for its attached Store.
// Defined in the audit package as a public accessor.
func writerStore(w *audit.Writer) *audit.Store { return w.Store() }

// entityWrite emits the Entity-shaped v3 row (with entity_type +
// entity_id) ALONGSIDE the legacy forward. Duplication is
// intentional and minimal: the legacy forward gives us continuity
// across the chain even when the Entity write fails; the Entity
// write gives the Timeline UI the indexed entity filter. Phase 1.5
// removes the legacy forward.
func entityWrite(
	ctx context.Context,
	s *audit.Store,
	spec builder.CollectionSpec,
	verb recordAuditVerb,
	recordID string,
	userID uuid.UUID,
	userCollection string,
	tenantID uuid.UUID,
	before, after any,
	r *http.Request,
) {
	event := spec.Name + "." + string(verb)
	if tenantID != uuid.Nil {
		actor := audit.ActorUser
		switch userCollection {
		case "_admins":
			actor = audit.ActorAdmin
		case "_api_tokens":
			actor = audit.ActorAPIToken
		case "":
			actor = audit.ActorSystem
		}
		_, _ = s.WriteTenantEntity(ctx, audit.TenantEvent{
			TenantID:        tenantID,
			ActorType:       actor,
			ActorID:         userID,
			ActorCollection: userCollection,
			Event:           event,
			EntityType:      spec.Name,
			EntityID:        recordID,
			Outcome:         audit.OutcomeSuccess,
			Before:          before,
			After:           after,
			IP:              clientIPFromRequest(ctx, r),
			UserAgent:       r.Header.Get("User-Agent"),
		})
		return
	}
	// Site write (no tenant context). Map user-collection actors to
	// ActorAdmin as a safe fallback so the row still surfaces in the
	// site timeline; the actor_collection column preserves the
	// original collection name for forensics.
	actor := audit.ActorSystem
	switch userCollection {
	case "_admins":
		actor = audit.ActorAdmin
	case "_api_tokens":
		actor = audit.ActorAPIToken
	default:
		if userID != uuid.Nil {
			actor = audit.ActorAdmin
		}
	}
	_, _ = s.WriteSiteEntity(ctx, audit.SiteEvent{
		ActorType:       actor,
		ActorID:         userID,
		ActorCollection: userCollection,
		Event:           event,
		EntityType:      spec.Name,
		EntityID:        recordID,
		Outcome:         audit.OutcomeSuccess,
		Before:          before,
		After:           after,
		IP:              clientIPFromRequest(ctx, r),
		UserAgent:       r.Header.Get("User-Agent"),
	})
}

// fetchPreImage SELECTs the current row state for an UPDATE / DELETE
// pre-image audit capture. Returns nil + nil on (a) audit disabled,
// (b) row not found (handler will 404 next anyway), or (c) SELECT
// failure (audit is best-effort — failed pre-image just means the
// resulting audit row gets before=nil instead of crashing the
// request).
//
// Uses the same buildView path as the public read endpoint, scoped
// through the supplied querier (which carries tenant RLS context
// when the collection is tenant-scoped). Soft-deleted rows are
// fetched even for tenant-scoped collections because UPDATE/DELETE
// of tombstones is a valid admin path and we still want the
// pre-image for audit.
func fetchPreImage(
	ctx context.Context,
	q pgQuerier,
	spec builder.CollectionSpec,
	id string,
) map[string]any {
	if !spec.Audit {
		return nil
	}
	sql, args := buildViewOpts(spec, id, "", nil, true /* includeDeleted */)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	if !rows.Next() {
		return nil
	}
	row, err := scanRow(rows, spec)
	if err != nil {
		return nil
	}
	return row
}

// recordIDFromRow extracts the canonical `id` field from a record
// map. Returns "" if absent — the caller short-circuits the audit
// emission in that case.
func recordIDFromRow(row map[string]any) string {
	if row == nil {
		return ""
	}
	v, ok := row["id"]
	if !ok {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case uuid.UUID:
		return s.String()
	default:
		return ""
	}
}

// clientIPFromRequest pulls the request's client IP. Tries
// X-Forwarded-For first (RemoteAddr inside an LB will be the LB's
// IP) then falls back to RemoteAddr. The audit IP column is
// best-effort — failing to extract it isn't fatal.
func clientIPFromRequest(_ context.Context, r *http.Request) string {
	if r == nil {
		return ""
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// First entry is the originating client per RFC 7239.
		for i := 0; i < len(v); i++ {
			if v[i] == ',' {
				return trimSpace(v[:i])
			}
		}
		return trimSpace(v)
	}
	return r.RemoteAddr
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
