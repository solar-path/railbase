package authority

// DoA gate — pre-handler check that interrupts mutations targeting
// gate-points declared via schema.Authority.
//
// *** PROTOTYPE — NOT FOR PRODUCTION USE (Slice 0). ***
//
// The gate is NOT a chi.Middleware in the conventional sense — it's
// invoked by the REST handler that owns the mutation (e.g. the
// generic CRUD UPDATE handler) AFTER RBAC and AFTER the new field
// values are parsed, but BEFORE the DB write.
//
// Flow:
//   1. Schema-side registry lists gate-points per collection.
//   2. Handler resolves which gate-points match the incoming
//      mutation (e.g. status=draft → status=published triggers
//      "articles.publish").
//   3. Handler queries the gate: is there an approved+completed
//      workflow whose requested_diff equals the attempted write?
//      → yes: GateAllow; consume the workflow in the same tx.
//      → no:  GateRequireApproval; return 409 with create_url envelope.
//
// Slice 0 simplification: gate exposes pure-function `Check` that
// works against the schema-declared AuthorityConfig list and a
// pre-loaded row (before-state). Wiring it into the REST CRUD
// handler is Slice 1 — Slice 0 stops at standalone testability of
// the matching + selection logic.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/schema/builder"
)

// GateDecision describes the gate's verdict for one mutation attempt.
type GateDecision struct {
	// Allowed = true means the mutation can proceed without DoA gating
	// (no matching gate-point) OR proceed via an approved+completed
	// workflow whose requested_diff matches.
	Allowed bool

	// ConsumeWorkflowID is set when Allowed=true via approved workflow
	// match. Handler must call Store.MarkConsumed(ctx, q, ID) in the
	// same tx as the DB write to record consume.
	ConsumeWorkflowID *uuid.UUID

	// RequiredAction is set when Allowed=false. Describes WHICH gate-
	// point triggered + how to create a workflow.
	RequiredAction *GateRequirement
}

// GateRequirement is the structured 409 envelope payload for a
// blocked mutation. SDKs marshal it into a typed error so client
// code can offer a one-click "submit for approval" UX.
type GateRequirement struct {
	ActionKey       string         `json:"action_key"`
	MatrixKey       string         `json:"matrix_key"`
	MatrixID        uuid.UUID      `json:"matrix_id"`
	LevelCount      int            `json:"level_count"`
	ProtectedFields []string       `json:"protected_fields,omitempty"`
	SuggestedDiff   map[string]any `json:"suggested_diff"`
	CreateURL       string         `json:"create_url"`
}

// CheckInput is the data the handler hands to the gate.
type CheckInput struct {
	// Op is the mutation type — "update", "insert", "delete".
	Op string

	// Collection is the collection being mutated.
	Collection string

	// RecordID identifies the row.
	RecordID uuid.UUID

	// TenantID scopes matrix selection.
	TenantID *uuid.UUID

	// BeforeFields is the row state BEFORE the attempted mutation,
	// as a map keyed by column name. Used to evaluate On.From
	// transition triggers. May be nil for Op="insert".
	BeforeFields map[string]any

	// AfterFields is the row state AFTER the attempted mutation,
	// as a map keyed by column name. Used to evaluate On.To
	// triggers and ProtectedFields consume validation.
	AfterFields map[string]any

	// Authorities is the collection's schema-declared list of
	// gate-points. Caller fetches this from the registry.
	Authorities []builder.AuthorityConfig
}

// Check evaluates whether the mutation passes the DoA gate. Returns
// a structured GateDecision the caller acts on.
//
// Logic (per Authority entry in Authorities):
//   1. Check whether the entry's On pattern matches the mutation.
//   2. If yes — try to locate an existing approved+completed
//      workflow on (collection, record_id, action_key=derived) whose
//      requested_diff matches the AfterFields for ProtectedFields.
//   3. If a match is found → GateAllow with ConsumeWorkflowID set.
//   4. If no match → GateRequireApproval with the suggested diff.
//
// Slice 0 limit: only one Authority entry is processed per call. If
// the mutation matches multiple entries, the FIRST match wins
// (collection schema order). Multi-gate orchestration is Slice 1.
func (s *Store) Check(ctx context.Context, in CheckInput) (*GateDecision, error) {
	for _, cfg := range in.Authorities {
		if !onMatchesMutation(cfg.On, in) {
			continue
		}

		actionKey := deriveActionKey(in.Collection, cfg)

		// Try to find a matching approved+completed workflow.
		wf, err := s.findApprovedWorkflowForMutation(ctx, in, cfg, actionKey)
		if err != nil && !errors.Is(err, ErrWorkflowNotFound) {
			return nil, err
		}
		if wf != nil {
			// Validate ProtectedFields against attempted write.
			if mismatch := validateProtectedFields(cfg.ProtectedFields, wf.RequestedDiff,
				in.AfterFields); mismatch != "" {
				return &GateDecision{
					Allowed: false,
					RequiredAction: &GateRequirement{
						ActionKey:       actionKey,
						MatrixID:        wf.MatrixID,
						ProtectedFields: cfg.ProtectedFields,
						SuggestedDiff:   in.AfterFields,
						CreateURL:       "/api/authority/workflows",
					},
				}, fmt.Errorf("authority: approved diff stale for protected field %q (drift detected); resubmit",
					mismatch)
			}
			return &GateDecision{
				Allowed:           true,
				ConsumeWorkflowID: &wf.ID,
			}, nil
		}

		// No approved workflow — build the 409 envelope.
		matrix, err := s.SelectActiveMatrix(ctx, SelectFilter{
			Key:      cfg.Matrix,
			TenantID: in.TenantID,
			Amount:   extractAmount(cfg.AmountField, in.AfterFields),
			Currency: extractCurrency(cfg.Currency, in.AfterFields),
		})
		if err != nil {
			if errors.Is(err, ErrMatrixNotFound) {
				if cfg.Required {
					return nil, fmt.Errorf(
						"authority: required gate point %q has no applicable matrix",
						actionKey)
				}
				// Soft pass — gate-point declared but no matrix configured
				// AND not Required. Permissive default for prototyping.
				continue
			}
			return nil, fmt.Errorf("authority: select matrix for gate: %w", err)
		}

		return &GateDecision{
			Allowed: false,
			RequiredAction: &GateRequirement{
				ActionKey:       actionKey,
				MatrixKey:       matrix.Key,
				MatrixID:        matrix.ID,
				LevelCount:      len(matrix.Levels),
				ProtectedFields: cfg.ProtectedFields,
				SuggestedDiff:   buildSuggestedDiff(cfg.ProtectedFields, in.AfterFields),
				CreateURL:       "/api/authority/workflows",
			},
		}, nil
	}

	// No Authority configs matched — mutation passes ungated.
	return &GateDecision{Allowed: true}, nil
}

// findApprovedWorkflowForMutation searches for a completed workflow
// on this (collection, record_id, action_key) that hasn't been
// consumed yet AND whose snapshot ProtectedFields match the
// attempted after-state.
func (s *Store) findApprovedWorkflowForMutation(ctx context.Context, in CheckInput,
	cfg builder.AuthorityConfig, actionKey string) (*Workflow, error) {
	const stmt = `
		SELECT id
		FROM _doa_workflows
		WHERE collection = $1
		  AND record_id = $2
		  AND action_key = $3
		  AND status = 'completed'
		  AND consumed_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1`

	var id uuid.UUID
	err := s.pool.QueryRow(ctx, stmt, in.Collection, in.RecordID, actionKey).Scan(&id)
	if err != nil {
		return nil, ErrWorkflowNotFound
	}
	return s.GetWorkflow(ctx, id)
}

// onMatchesMutation returns true when the schema-declared AuthorityOn
// pattern matches the attempted mutation.
func onMatchesMutation(on builder.AuthorityOn, in CheckInput) bool {
	op := on.Op
	if op == "" {
		op = "update"
	}
	if op != in.Op {
		return false
	}

	switch op {
	case "delete":
		return true // no field/from/to for delete
	case "insert":
		if on.Field == "" {
			return true // insert any
		}
		if !containsString(on.To, fmt.Sprint(in.AfterFields[on.Field])) {
			return false
		}
		return true
	case "update":
		if on.Field == "" {
			return false // update needs a field to gate on
		}
		afterVal := fmt.Sprint(in.AfterFields[on.Field])
		if !containsString(on.To, afterVal) {
			return false
		}
		if len(on.From) > 0 {
			beforeVal := ""
			if in.BeforeFields != nil {
				beforeVal = fmt.Sprint(in.BeforeFields[on.Field])
			}
			if !containsString(on.From, beforeVal) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// deriveActionKey constructs the action_key from collection + Authority.Name.
// When Name is set: "<collection>.<Name>" (e.g. "articles.publish").
// When Name is empty (single Authority on the collection):
// "<collection>.<Op>" (e.g. "articles.update").
func deriveActionKey(collection string, cfg builder.AuthorityConfig) string {
	if cfg.Name != "" {
		return collection + "." + cfg.Name
	}
	op := cfg.On.Op
	if op == "" {
		op = "update"
	}
	return collection + "." + op
}

// validateProtectedFields compares the approved workflow's
// requested_diff with the attempted after-state for the
// fields listed in ProtectedFields. Returns the first drifting
// field name (or empty string on full match).
func validateProtectedFields(protected []string, approvedDiffRaw []byte,
	attempted map[string]any) string {
	if len(protected) == 0 {
		return ""
	}
	var approvedDiff map[string]any
	if err := json.Unmarshal(approvedDiffRaw, &approvedDiff); err != nil {
		// Unmarshal failure = treat as drift (defensive).
		return protected[0]
	}
	for _, field := range protected {
		approvedVal, ok := approvedDiff[field]
		if !ok {
			// Approved diff didn't include this protected field —
			// any non-zero attempted value is a drift.
			if attempted[field] != nil {
				return field
			}
			continue
		}
		attemptedVal := attempted[field]
		if !reflect.DeepEqual(approvedVal, attemptedVal) {
			return field
		}
	}
	return ""
}

// buildSuggestedDiff filters the attempted fields down to the
// ProtectedFields set (plus the gate-trigger field if not already
// in ProtectedFields). This is the suggested_diff payload returned
// in the 409 envelope so client code can immediately POST a
// workflow create with the right shape.
func buildSuggestedDiff(protected []string, attempted map[string]any) map[string]any {
	out := make(map[string]any, len(protected))
	for _, field := range protected {
		if v, ok := attempted[field]; ok {
			out[field] = v
		}
	}
	return out
}

func extractAmount(field string, attempted map[string]any) *int64 {
	if field == "" {
		return nil
	}
	v, ok := attempted[field]
	if !ok || v == nil {
		return nil
	}
	switch x := v.(type) {
	case int64:
		return &x
	case int:
		i := int64(x)
		return &i
	case float64:
		i := int64(x)
		return &i
	}
	return nil
}

func extractCurrency(configured string, attempted map[string]any) string {
	if configured != "" {
		return configured
	}
	if v, ok := attempted["currency"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func containsString(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}
