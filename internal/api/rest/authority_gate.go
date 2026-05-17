package rest

// REST integration of the v2.0-alpha DoA gate (internal/authority).
//
// *** PROTOTYPE — Slice 0 wiring ***
//
// The gate is opt-in: it only runs when the CRUD handler's handlerDeps
// has a non-nil authority.Store AND the active CollectionSpec has at
// least one Authority() declaration. Otherwise this is a no-op and
// non-DoA collections pay zero overhead.
//
// Behaviour summary (full spec: docs/26-authority.md):
//   - permissive pass: gate returned Allowed=true with no consume id
//     → handler proceeds as before.
//   - in-tx consume: gate returned Allowed=true with consume_workflow_id
//     → handler wraps UPDATE + MarkConsumed in a tx; if either fails
//     both roll back.
//   - blocked: gate returned Allowed=false → 409 with GateRequirement
//     envelope written to ResponseWriter, handler returns (gateBlocked=true).

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/authority"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/tenant"
)

// runAuthorityGate invokes gate.Check against the UPDATE mutation when
// DoA wiring is configured. Returns (consumeWorkflowID, gateBlocked).
// When gateBlocked is true, the caller MUST return immediately — the
// 409 envelope has already been written.
func (d *handlerDeps) runAuthorityGate(
	ctx context.Context,
	w http.ResponseWriter,
	spec builder.CollectionSpec,
	idStr string,
	beforeFields map[string]any,
	afterFields map[string]any,
) (consumeWorkflowID *uuid.UUID, gateBlocked bool) {
	if d.authority == nil || len(spec.Authorities) == 0 {
		return nil, false
	}

	recordID, err := uuid.Parse(idStr)
	if err != nil {
		// Non-UUID id will 404 in the regular path anyway — let the
		// gate skip so the existing 404 short-circuits cleanly.
		return nil, false
	}

	var tenantID *uuid.UUID
	if spec.Tenant && tenant.HasID(ctx) {
		t := tenant.ID(ctx)
		tenantID = &t
	}

	dec, err := d.authority.Check(ctx, authority.CheckInput{
		Op:           "update",
		Collection:   spec.Name,
		RecordID:     recordID,
		TenantID:     tenantID,
		BeforeFields: beforeFields,
		AfterFields:  afterFields,
		Authorities:  spec.Authorities,
	})
	if err != nil {
		// Drift error from validateProtectedFields surfaces as
		// non-nil err + dec.Allowed=false. Treat any other error as
		// internal so we don't silently weaken the gate.
		if dec != nil && !dec.Allowed && dec.RequiredAction != nil {
			writeGateEnvelope(w, http.StatusConflict, err.Error(), dec.RequiredAction)
			return nil, true
		}
		d.log.Error("rest: doa gate check failed",
			"collection", spec.Name, "record", recordID, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "doa gate"))
		return nil, true
	}

	if dec.Allowed {
		return dec.ConsumeWorkflowID, false
	}

	// Blocked with envelope.
	msg := "approval required for this mutation"
	if dec.RequiredAction != nil && dec.RequiredAction.ActionKey != "" {
		msg = "approval required for action " + dec.RequiredAction.ActionKey
	}
	writeGateEnvelope(w, http.StatusConflict, msg, dec.RequiredAction)
	return nil, true
}

// fetchRowForGate selects the pre-image of the row for the gate when
// auditBefore is nil (spec.Audit off). Returns nil on any error — the
// gate treats nil pre-image as "any source value", which is the same
// degraded behaviour as when On.From is unspecified.
func fetchRowForGate(ctx context.Context, q pgQuerier, spec builder.CollectionSpec, id string) map[string]any {
	sql, args := buildViewOpts(spec, id, "", nil, true)
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

// mergeForGate returns the effective after-state visible to the DoA
// gate by overlaying the PATCH fields on top of the pre-image. This
// is what the gate needs: a PATCH that omits a ProtectedField
// shouldn't appear to "blank out" that field — its existing value
// carries through unchanged.
//
// Falls back gracefully when pre-image is nil (audit off + DoA gate
// has no other pre-image source): returns a copy of fields. The gate
// will then over-gate for partial PATCHes touching ProtectedFields
// that aren't in the patch — acceptable fail-safe for Slice 0.
func mergeForGate(before, fields map[string]any) map[string]any {
	out := make(map[string]any, len(before)+len(fields))
	for k, v := range before {
		out[k] = v
	}
	for k, v := range fields {
		out[k] = v
	}
	return out
}

// writeGateEnvelope writes a 409 with a structured GateRequirement
// payload. The shape matches docs/26 §5 — clients with the typed-SDK
// can pattern-match on the envelope and one-click submit the workflow.
func writeGateEnvelope(w http.ResponseWriter, status int, msg string,
	req *authority.GateRequirement) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	payload := map[string]any{
		"code":    "approval_required",
		"message": msg,
	}
	if req != nil {
		payload["authority"] = req
	}
	_ = json.NewEncoder(w).Encode(payload)
}
