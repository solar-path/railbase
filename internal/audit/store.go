package audit

// Store is the v3.x unified audit surface. It owns two append-only
// hash chains backed by `_audit_log_site` (system + admin actions)
// and `_audit_log_tenant` (per-tenant actions). The legacy
// `_audit_log` (chain v1) stays read-only and is served via the
// existing Writer for backwards-compatible reads.
//
// Design rationale — see docs/19-unified-audit.md.
//
// Two-chain model. Site events form a single SHA-256 chain (process-
// wide mutex). Tenant events form ONE CHAIN PER TENANT — the
// per-tenant prev_hash lives in an in-memory map keyed by tenant_id,
// each behind its own mutex. This lets writes for different tenants
// proceed in parallel without crossing chains, and dropping rows for
// tenant T at offboard time doesn't invalidate any other tenant's
// verify.
//
// Cold start. On boot Store loads the latest hash from each chain
// (site → 1 query, tenant → 1 GROUP BY query) so subsequent writes
// link onto the existing chain. Missing rows (genesis) hash against
// 32 zero bytes — same convention as the legacy Writer.
//
// PII redaction. before/after/meta payloads run through redactJSON
// (existing) using the security.IsSensitiveKey allow-list. The same
// definition of "credential field" covers legacy + site + tenant
// chains.
//
// Bare-pool rule (inherited). Writes do not accept a *pgx.Tx — they
// always acquire a fresh connection from the pool. A handler can
// refuse a request, write `rbac.deny`, and the deny record persists
// even though the rest of the handler's work rolls back. Critical
// invariant.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ActorType is the small enum classifying who performed an action.
// Matches the SQL enum `audit_actor_type` (migration 0030).
type ActorType string

const (
	ActorSystem   ActorType = "system"
	ActorAdmin    ActorType = "admin"
	ActorAPIToken ActorType = "api_token"
	ActorJob      ActorType = "job"
	ActorUser     ActorType = "user" // only valid on _audit_log_tenant
)

// SiteEvent is one row destined for _audit_log_site. ActorType +
// Event are required; everything else may be zero. The Store fills
// id / seq / at / prev_hash / hash.
//
// Use SiteEvent for system / admin / api-token / job actions. For
// per-tenant actions use TenantEvent — the Store enforces a tenant_id
// invariant on that path.
type SiteEvent struct {
	// Actor: WHO performed this action.
	ActorType       ActorType
	ActorID         uuid.UUID // uuid.Nil for system
	ActorEmail      string    // optional; denormalised for cheap UI display
	ActorCollection string    // '_admins' | '_api_tokens' | empty

	// Action.
	Event string // dotted: "admin.backup.create", "system.migration.applied"

	// Optional entity reference. CRUD-shaped events fill both;
	// actor-only events (login, signout, cron tick) leave them empty.
	// The handler distinguishes via the Entity vs ActorOnly Store
	// method.
	EntityType string
	EntityID   string

	Outcome Outcome // success | denied | error

	// Diff payload. Redacted before persist via security.IsSensitiveKey.
	Before any
	After  any
	Meta   map[string]any

	// Failure context. ErrorCode is a short slug ("forbidden",
	// "validation"); Error carries the raw err for richer error_data.
	ErrorCode string
	Error     error

	IP        string
	UserAgent string
	RequestID string // correlates with structured logger
}

// TenantEvent is one row destined for _audit_log_tenant. TenantID is
// REQUIRED; the Store refuses writes with uuid.Nil tenant_id.
type TenantEvent struct {
	TenantID uuid.UUID // required

	ActorType       ActorType
	ActorID         uuid.UUID
	ActorEmail      string
	ActorCollection string

	Event string

	EntityType string
	EntityID   string

	Outcome Outcome

	Before any
	After  any
	Meta   map[string]any

	ErrorCode string
	Error     error

	IP        string
	UserAgent string
	RequestID string
}

// Store is the platform-wide audit handle. Constructed once on boot
// (NewStore), shared via Deps. Goroutine-safe.
type Store struct {
	pool *pgxpool.Pool

	// Site chain: process-wide single chain.
	siteMu       sync.Mutex
	sitePrevHash []byte

	// Tenant chains: one chain per tenant_id. The map is guarded by
	// tenantsMu only for map mutations (insert / lookup); per-tenant
	// chain advancement is serialised by the per-tenant mutex inside
	// each *tenantChain entry.
	tenantsMu sync.Mutex
	tenants   map[uuid.UUID]*tenantChain
}

// tenantChain tracks the per-tenant prev_hash + a mutex serialising
// chain advancement for one tenant. tenant_seq is monotonic within
// the tenant — we read it back from the row we just inserted so we
// don't need to track it in memory.
type tenantChain struct {
	mu       sync.Mutex
	prevHash []byte
}

// NewStore constructs the Store. Loads the latest site chain head
// from `_audit_log_site` (if any) so writes link onto the existing
// chain. Tenant chain heads are loaded lazily on first per-tenant
// write to avoid a startup-time GROUP BY across all tenants (could
// be N tenants × M partitions on a multi-tenant SaaS).
func NewStore(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	s := &Store{
		pool:         pool,
		sitePrevHash: make([]byte, 32),
		tenants:      make(map[uuid.UUID]*tenantChain),
	}
	// Site: load the latest hash. Use seq DESC because `at` may have
	// duplicate microseconds at high write rate; seq is BIGSERIAL.
	var hash []byte
	err := pool.QueryRow(ctx,
		`SELECT hash FROM _audit_log_site ORDER BY seq DESC LIMIT 1`).Scan(&hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("audit: store: load site head: %w", err)
	}
	if err == nil {
		s.sitePrevHash = hash
	}
	return s, nil
}

// WriteSite persists e to _audit_log_site. Returns the assigned row
// id. Fire-and-forget: callers usually ignore the return value.
//
// Distinguish-by-method. Two siblings (WriteSiteEntity / WriteSiteActorOnly)
// are provided as thin wrappers that validate the entity contract at
// call-site. This raw WriteSite accepts both shapes — internal callers
// who want to bypass the entity check use it directly.
func (s *Store) WriteSite(ctx context.Context, e SiteEvent) (uuid.UUID, error) {
	if e.Event == "" {
		return uuid.Nil, fmt.Errorf("audit: site: event required")
	}
	if e.ActorType == "" {
		return uuid.Nil, fmt.Errorf("audit: site: actor_type required")
	}
	if e.ActorType == ActorUser {
		return uuid.Nil, fmt.Errorf("audit: site: actor_type=user is tenant-only — use WriteTenant")
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeSuccess
	}

	s.siteMu.Lock()
	defer s.siteMu.Unlock()

	id := uuid.Must(uuid.NewV7())
	at := time.Now().UTC().Truncate(time.Microsecond)

	beforeJSON, err := redactJSON(e.Before)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: site: encode before: %w", err)
	}
	afterJSON, err := redactJSON(e.After)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: site: encode after: %w", err)
	}
	metaJSON, err := redactJSON(e.Meta)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: site: encode meta: %w", err)
	}
	errorDataJSON, errorCode := encodeError(e.Error, e.ErrorCode)

	hash := computeHashSite(s.sitePrevHash, siteCanonical{
		ID:              id,
		At:              at,
		ActorType:       string(e.ActorType),
		ActorID:         nilToZeroUUID(e.ActorID),
		ActorEmail:      e.ActorEmail,
		ActorCollection: e.ActorCollection,
		Event:           e.Event,
		EntityType:      e.EntityType,
		EntityID:        e.EntityID,
		Outcome:         string(e.Outcome),
		Before:          json.RawMessage(beforeJSON),
		After:           json.RawMessage(afterJSON),
		Meta:            json.RawMessage(metaJSON),
		ErrorCode:       errorCode,
		ErrorData:       json.RawMessage(errorDataJSON),
		IP:              e.IP,
		UserAgent:       e.UserAgent,
		RequestID:       e.RequestID,
	})

	const q = `
        INSERT INTO _audit_log_site
            (id, at,
             actor_type, actor_id, actor_email, actor_collection,
             event, entity_type, entity_id, outcome,
             before, after, meta,
             error_code, error_data,
             ip, user_agent, request_id,
             prev_hash, hash)
        VALUES
            ($1, $2,
             $3, $4, $5, $6,
             $7, $8, $9, $10,
             $11, $12, $13,
             $14, $15,
             $16, $17, $18,
             $19, $20)
    `
	if _, err := s.pool.Exec(ctx, q,
		id, at,
		string(e.ActorType), nullableUUID(e.ActorID), nullableText(e.ActorEmail), nullableText(e.ActorCollection),
		e.Event, nullableText(e.EntityType), nullableText(e.EntityID), string(e.Outcome),
		nullableJSON(beforeJSON), nullableJSON(afterJSON), nullableJSON(metaJSON),
		nullableText(errorCode), nullableJSON(errorDataJSON),
		nullableText(e.IP), nullableText(e.UserAgent), nullableText(e.RequestID),
		s.sitePrevHash, hash,
	); err != nil {
		return uuid.Nil, fmt.Errorf("audit: site: insert: %w", err)
	}
	s.sitePrevHash = hash
	return id, nil
}

// WriteTenant persists e to _audit_log_tenant. TenantID required.
// Per-tenant chain advancement: writes for tenant T serialise behind
// T's mutex, but writes for different tenants run in parallel.
func (s *Store) WriteTenant(ctx context.Context, e TenantEvent) (uuid.UUID, error) {
	if e.TenantID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("audit: tenant: tenant_id required")
	}
	if e.Event == "" {
		return uuid.Nil, fmt.Errorf("audit: tenant: event required")
	}
	if e.ActorType == "" {
		return uuid.Nil, fmt.Errorf("audit: tenant: actor_type required")
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeSuccess
	}

	chain, err := s.tenantChainFor(ctx, e.TenantID)
	if err != nil {
		return uuid.Nil, err
	}

	chain.mu.Lock()
	defer chain.mu.Unlock()

	id := uuid.Must(uuid.NewV7())
	at := time.Now().UTC().Truncate(time.Microsecond)

	beforeJSON, err := redactJSON(e.Before)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: tenant: encode before: %w", err)
	}
	afterJSON, err := redactJSON(e.After)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: tenant: encode after: %w", err)
	}
	metaJSON, err := redactJSON(e.Meta)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: tenant: encode meta: %w", err)
	}
	errorDataJSON, errorCode := encodeError(e.Error, e.ErrorCode)

	hash := computeHashTenant(chain.prevHash, tenantCanonical{
		ID:              id,
		TenantID:        e.TenantID,
		At:              at,
		ActorType:       string(e.ActorType),
		ActorID:         nilToZeroUUID(e.ActorID),
		ActorEmail:      e.ActorEmail,
		ActorCollection: e.ActorCollection,
		Event:           e.Event,
		EntityType:      e.EntityType,
		EntityID:        e.EntityID,
		Outcome:         string(e.Outcome),
		Before:          json.RawMessage(beforeJSON),
		After:           json.RawMessage(afterJSON),
		Meta:            json.RawMessage(metaJSON),
		ErrorCode:       errorCode,
		ErrorData:       json.RawMessage(errorDataJSON),
		IP:              e.IP,
		UserAgent:       e.UserAgent,
		RequestID:       e.RequestID,
	})

	// tenant_seq: monotonic within tenant. Compute as
	// COALESCE(max(tenant_seq), 0)+1 INSIDE the insert so concurrent
	// inserts for the same tenant collide on the unique chain (the
	// per-tenant mutex above already serialises us, so this is
	// belt-and-braces — but it prevents a forgotten-mutex bug from
	// silently producing duplicate tenant_seq values).
	const q = `
        WITH next AS (
            SELECT COALESCE(MAX(tenant_seq), 0) + 1 AS s
              FROM _audit_log_tenant
             WHERE tenant_id = $3
        )
        INSERT INTO _audit_log_tenant
            (id, at, tenant_id, tenant_seq,
             actor_type, actor_id, actor_email, actor_collection,
             event, entity_type, entity_id, outcome,
             before, after, meta,
             error_code, error_data,
             ip, user_agent, request_id,
             prev_hash, hash)
        SELECT
            $1, $2, $3, next.s,
            $4, $5, $6, $7,
            $8, $9, $10, $11,
            $12, $13, $14,
            $15, $16,
            $17, $18, $19,
            $20, $21
          FROM next
    `
	if _, err := s.pool.Exec(ctx, q,
		id, at, e.TenantID,
		string(e.ActorType), nullableUUID(e.ActorID), nullableText(e.ActorEmail), nullableText(e.ActorCollection),
		e.Event, nullableText(e.EntityType), nullableText(e.EntityID), string(e.Outcome),
		nullableJSON(beforeJSON), nullableJSON(afterJSON), nullableJSON(metaJSON),
		nullableText(errorCode), nullableJSON(errorDataJSON),
		nullableText(e.IP), nullableText(e.UserAgent), nullableText(e.RequestID),
		chain.prevHash, hash,
	); err != nil {
		return uuid.Nil, fmt.Errorf("audit: tenant: insert: %w", err)
	}
	chain.prevHash = hash
	return id, nil
}

// WriteSiteEntity is the safety-wrapper for entity-bound site events.
// entity_type + entity_id MUST be set; misuse trips an explicit
// error instead of silently writing an actor-only row with NULL
// entity columns (which then breaks the «show everything for X»
// filter).
func (s *Store) WriteSiteEntity(ctx context.Context, e SiteEvent) (uuid.UUID, error) {
	if e.EntityType == "" || e.EntityID == "" {
		return uuid.Nil, fmt.Errorf("audit: site: entity_type + entity_id required for WriteSiteEntity (use WriteSiteActorOnly for actor-only events)")
	}
	return s.WriteSite(ctx, e)
}

// WriteSiteActorOnly is the safety-wrapper for actor-only events
// (login, signout, cron tick). Refuses to write if entity_type /
// entity_id are populated — if you have an entity, use the Entity
// wrapper so the entity filter index hits properly.
func (s *Store) WriteSiteActorOnly(ctx context.Context, e SiteEvent) (uuid.UUID, error) {
	if e.EntityType != "" || e.EntityID != "" {
		return uuid.Nil, fmt.Errorf("audit: site: WriteSiteActorOnly rejects entity_type/entity_id — use WriteSiteEntity instead")
	}
	return s.WriteSite(ctx, e)
}

// WriteTenantEntity / WriteTenantActorOnly — same contract as site.
func (s *Store) WriteTenantEntity(ctx context.Context, e TenantEvent) (uuid.UUID, error) {
	if e.EntityType == "" || e.EntityID == "" {
		return uuid.Nil, fmt.Errorf("audit: tenant: entity_type + entity_id required for WriteTenantEntity (use WriteTenantActorOnly for actor-only events)")
	}
	return s.WriteTenant(ctx, e)
}

func (s *Store) WriteTenantActorOnly(ctx context.Context, e TenantEvent) (uuid.UUID, error) {
	if e.EntityType != "" || e.EntityID != "" {
		return uuid.Nil, fmt.Errorf("audit: tenant: WriteTenantActorOnly rejects entity_type/entity_id — use WriteTenantEntity instead")
	}
	return s.WriteTenant(ctx, e)
}

// tenantChainFor returns the per-tenant chain head, lazily loading
// it from `_audit_log_tenant` if this is the first write for the
// tenant since process boot.
func (s *Store) tenantChainFor(ctx context.Context, tenantID uuid.UUID) (*tenantChain, error) {
	s.tenantsMu.Lock()
	chain, ok := s.tenants[tenantID]
	if ok {
		s.tenantsMu.Unlock()
		return chain, nil
	}
	// Reserve the slot so concurrent first-writes for the same tenant
	// don't both run the SELECT. The under-construction chain has its
	// prev_hash zeroed; we fill it from the DB before releasing the
	// per-tenant mutex.
	chain = &tenantChain{prevHash: make([]byte, 32)}
	chain.mu.Lock() // held until we've loaded the head
	s.tenants[tenantID] = chain
	s.tenantsMu.Unlock()

	defer chain.mu.Unlock()

	var hash []byte
	err := s.pool.QueryRow(ctx,
		`SELECT hash FROM _audit_log_tenant
		  WHERE tenant_id = $1
		  ORDER BY tenant_seq DESC
		  LIMIT 1`, tenantID).Scan(&hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// Drop the half-built chain so a retry re-loads.
		s.tenantsMu.Lock()
		delete(s.tenants, tenantID)
		s.tenantsMu.Unlock()
		return nil, fmt.Errorf("audit: tenant: load chain head for %s: %w", tenantID, err)
	}
	if err == nil {
		chain.prevHash = hash
	}
	return chain, nil
}

// encodeError extracts a JSON-serialisable error_data payload from
// err. errorCode falls back to err.Error()'s class if not explicitly
// provided. Returns ([]byte, code) pair; either may be empty.
func encodeError(err error, code string) ([]byte, string) {
	if err == nil {
		return nil, code
	}
	if code == "" {
		code = "internal"
	}
	payload := map[string]any{"message": err.Error()}
	b, mErr := json.Marshal(payload)
	if mErr != nil {
		// Programming error — error message produced invalid JSON.
		// Fall back to a bare-minimum payload so the audit row still
		// lands.
		return []byte(`{"message":"audit: error encode failed"}`), code
	}
	return b, code
}
