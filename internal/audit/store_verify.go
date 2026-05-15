package audit

// Chain-walk verifier for the v3 audit chains.
//
// Same primitive as legacy Writer.Verify (audit.go), parameterised
// over which chain to walk:
//
//   * VerifySite walks _audit_log_site as a single chain (seq ASC).
//   * VerifyTenant walks ONE tenant's rows from _audit_log_tenant
//     in tenant_seq ASC order. Per-tenant chains are independent,
//     so tampering with tenant A's rows doesn't break tenant B's
//     verify.
//   * VerifyAllTenants iterates tenant_id DISTINCT and runs
//     VerifyTenant for each. Single tenant break short-circuits and
//     returns the first failing tenant.
//
// Stability: the canonical-JSON hash input mirrors writeSite /
// writeTenant exactly — same field order, same NULL → empty-string
// COALESCE convention, same microsecond truncation. Mismatch is the
// chain-broken signal.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// VerifySite walks _audit_log_site from seq=1 forward and returns
// (rows verified, error). On chain break returns *ChainError with
// the offending seq.
func (s *Store) VerifySite(ctx context.Context) (int64, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT seq, id, at,
               actor_type::text,
               COALESCE(actor_id::text, ''),
               COALESCE(actor_email, ''),
               COALESCE(actor_collection, ''),
               event,
               COALESCE(entity_type, ''),
               COALESCE(entity_id, ''),
               outcome::text,
               COALESCE(before::text, 'null'),
               COALESCE(after::text, 'null'),
               COALESCE(meta::text, 'null'),
               COALESCE(error_code, ''),
               COALESCE(error_data::text, 'null'),
               COALESCE(ip, ''),
               COALESCE(user_agent, ''),
               COALESCE(request_id, ''),
               prev_hash, hash
          FROM _audit_log_site
         ORDER BY seq ASC
    `)
	if err != nil {
		return 0, fmt.Errorf("audit: verify site: %w", err)
	}
	defer rows.Close()

	expected := make([]byte, 32) // genesis = 32 zero bytes
	var n int64
	for rows.Next() {
		var (
			seq                                                    int64
			id                                                     uuid.UUID
			at                                                     time.Time
			actorType, actorID, actorEmail, actorCollection        string
			event, entityType, entityID, outcome                   string
			beforeStr, afterStr, metaStr                           string
			errorCode, errorDataStr, ip, userAgent, requestID      string
			prev, h                                                []byte
		)
		if err := rows.Scan(
			&seq, &id, &at,
			&actorType, &actorID, &actorEmail, &actorCollection,
			&event, &entityType, &entityID, &outcome,
			&beforeStr, &afterStr, &metaStr,
			&errorCode, &errorDataStr,
			&ip, &userAgent, &requestID,
			&prev, &h,
		); err != nil {
			return n, fmt.Errorf("audit: verify site scan: %w", err)
		}
		if !bytesEqual(prev, expected) {
			return n, &ChainError{Seq: seq, Reason: "prev_hash mismatch (site)"}
		}
		var actorUUID uuid.UUID
		if actorID != "" {
			if u, err := uuid.Parse(actorID); err == nil {
				actorUUID = u
			}
		}
		got := computeHashSite(prev, siteCanonical{
			ID:              id,
			At:              at.UTC().Truncate(time.Microsecond),
			ActorType:       actorType,
			ActorID:         actorUUID,
			ActorEmail:      actorEmail,
			ActorCollection: actorCollection,
			Event:           event,
			EntityType:      entityType,
			EntityID:        entityID,
			Outcome:         outcome,
			Before:          json.RawMessage(beforeStr),
			After:           json.RawMessage(afterStr),
			Meta:            json.RawMessage(metaStr),
			ErrorCode:       errorCode,
			ErrorData:       json.RawMessage(errorDataStr),
			IP:              ip,
			UserAgent:       userAgent,
			RequestID:       requestID,
		})
		if !bytesEqual(h, got) {
			return n, &ChainError{Seq: seq, Reason: "hash mismatch (site)"}
		}
		expected = h
		n++
	}
	return n, rows.Err()
}

// VerifyTenant walks one tenant's rows from _audit_log_tenant in
// tenant_seq ASC order. Independent per-tenant chain: tampering with
// other tenants' rows doesn't affect this verify.
func (s *Store) VerifyTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	if tenantID == uuid.Nil {
		return 0, fmt.Errorf("audit: verify tenant: tenant_id required")
	}
	rows, err := s.pool.Query(ctx, `
        SELECT tenant_seq, id, at, tenant_id,
               actor_type::text,
               COALESCE(actor_id::text, ''),
               COALESCE(actor_email, ''),
               COALESCE(actor_collection, ''),
               event,
               COALESCE(entity_type, ''),
               COALESCE(entity_id, ''),
               outcome::text,
               COALESCE(before::text, 'null'),
               COALESCE(after::text, 'null'),
               COALESCE(meta::text, 'null'),
               COALESCE(error_code, ''),
               COALESCE(error_data::text, 'null'),
               COALESCE(ip, ''),
               COALESCE(user_agent, ''),
               COALESCE(request_id, ''),
               prev_hash, hash
          FROM _audit_log_tenant
         WHERE tenant_id = $1
         ORDER BY tenant_seq ASC
    `, tenantID)
	if err != nil {
		return 0, fmt.Errorf("audit: verify tenant %s: %w", tenantID, err)
	}
	defer rows.Close()

	expected := make([]byte, 32)
	var n int64
	for rows.Next() {
		var (
			tenantSeq                                              int64
			id                                                     uuid.UUID
			at                                                     time.Time
			rowTenantID                                            uuid.UUID
			actorType, actorID, actorEmail, actorCollection        string
			event, entityType, entityID, outcome                   string
			beforeStr, afterStr, metaStr                           string
			errorCode, errorDataStr, ip, userAgent, requestID      string
			prev, h                                                []byte
		)
		if err := rows.Scan(
			&tenantSeq, &id, &at, &rowTenantID,
			&actorType, &actorID, &actorEmail, &actorCollection,
			&event, &entityType, &entityID, &outcome,
			&beforeStr, &afterStr, &metaStr,
			&errorCode, &errorDataStr,
			&ip, &userAgent, &requestID,
			&prev, &h,
		); err != nil {
			return n, fmt.Errorf("audit: verify tenant scan: %w", err)
		}
		if !bytesEqual(prev, expected) {
			return n, &ChainError{Seq: tenantSeq, Reason: fmt.Sprintf("prev_hash mismatch (tenant %s)", tenantID)}
		}
		var actorUUID uuid.UUID
		if actorID != "" {
			if u, err := uuid.Parse(actorID); err == nil {
				actorUUID = u
			}
		}
		got := computeHashTenant(prev, tenantCanonical{
			ID:              id,
			TenantID:        rowTenantID,
			At:              at.UTC().Truncate(time.Microsecond),
			ActorType:       actorType,
			ActorID:         actorUUID,
			ActorEmail:      actorEmail,
			ActorCollection: actorCollection,
			Event:           event,
			EntityType:      entityType,
			EntityID:        entityID,
			Outcome:         outcome,
			Before:          json.RawMessage(beforeStr),
			After:           json.RawMessage(afterStr),
			Meta:            json.RawMessage(metaStr),
			ErrorCode:       errorCode,
			ErrorData:       json.RawMessage(errorDataStr),
			IP:              ip,
			UserAgent:       userAgent,
			RequestID:       requestID,
		})
		if !bytesEqual(h, got) {
			return n, &ChainError{Seq: tenantSeq, Reason: fmt.Sprintf("hash mismatch (tenant %s)", tenantID)}
		}
		expected = h
		n++
	}
	return n, rows.Err()
}

// TenantVerifyResult is one tenant's verify outcome inside the
// VerifyAllTenants envelope.
type TenantVerifyResult struct {
	TenantID uuid.UUID
	Rows     int64
	Err      error // nil ⇒ OK
}

// VerifyAllTenants enumerates every distinct tenant_id in
// _audit_log_tenant and verifies its chain. Returns one
// TenantVerifyResult per tenant — the caller decides how to
// surface multiple failures (CLI prints all, monitoring alerts
// on any).
func (s *Store) VerifyAllTenants(ctx context.Context) ([]TenantVerifyResult, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT tenant_id FROM _audit_log_tenant ORDER BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("audit: verify all tenants list: %w", err)
	}
	var tenantIDs []uuid.UUID
	for rows.Next() {
		var t uuid.UUID
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, fmt.Errorf("audit: verify all tenants scan: %w", err)
		}
		tenantIDs = append(tenantIDs, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := make([]TenantVerifyResult, 0, len(tenantIDs))
	for _, t := range tenantIDs {
		n, vErr := s.VerifyTenant(ctx, t)
		results = append(results, TenantVerifyResult{
			TenantID: t,
			Rows:     n,
			Err:      vErr,
		})
	}
	return results, nil
}
