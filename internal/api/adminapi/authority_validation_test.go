package adminapi

// v2.0-alpha — pure-function validation tests for the DoA matrix
// admin request shape. DB round-trip lives in the Slice 0 e2e test;
// these cases pin the validation contract without spinning up PG.

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/authority"
)

func TestBuildMatrixFromRequest_RejectsEmptyKey(t *testing.T) {
	req := &matrixRequest{Name: "x", Levels: []matrixLevelRequest{validLevel()}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "key is required") {
		t.Errorf("expected 'key is required' validation error, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsEmptyName(t *testing.T) {
	req := &matrixRequest{Key: "k", Levels: []matrixLevelRequest{validLevel()}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "name is required") {
		t.Errorf("expected 'name is required' validation error, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsEmptyLevels(t *testing.T) {
	req := &matrixRequest{Key: "k", Name: "n"}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "at least one level") {
		t.Errorf("expected 'at least one level' validation error, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsZeroLevelN(t *testing.T) {
	lvl := validLevel()
	lvl.LevelN = 0
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "level_n must be >= 1") {
		t.Errorf("expected level_n bound check, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsInvalidMode(t *testing.T) {
	lvl := validLevel()
	lvl.Mode = "majority"
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "mode must be one of any|all|threshold") {
		t.Errorf("expected mode validation error, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_ThresholdRequiresMinApprovals(t *testing.T) {
	lvl := validLevel()
	lvl.Mode = authority.ModeThreshold
	// MinApprovals unset
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "min_approvals required") {
		t.Errorf("expected min_approvals required error, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsPositionApproverType(t *testing.T) {
	// Slice 0 boundary — position approver type is forward-compat in
	// the DB schema but rejected by admin REST until v2.x org-chart.
	lvl := validLevel()
	lvl.Approvers = []matrixApproverRequest{
		{ApproverType: authority.ApproverTypePosition, ApproverRef: "cfo"},
	}
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "org-chart primitive (v2.x)") {
		t.Errorf("expected position rejected with v2.x hint, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsDepartmentHeadApproverType(t *testing.T) {
	lvl := validLevel()
	lvl.Approvers = []matrixApproverRequest{
		{ApproverType: authority.ApproverTypeDepartmentHead, ApproverRef: "finance"},
	}
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "org-chart primitive (v2.x)") {
		t.Errorf("expected department_head rejected with v2.x hint, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsUnknownApproverType(t *testing.T) {
	lvl := validLevel()
	lvl.Approvers = []matrixApproverRequest{
		{ApproverType: "anyone", ApproverRef: "x"},
	}
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "must be role|user") {
		t.Errorf("expected approver_type bounds error, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_RejectsEmptyApproverRef(t *testing.T) {
	lvl := validLevel()
	lvl.Approvers = []matrixApproverRequest{
		{ApproverType: authority.ApproverTypeRole, ApproverRef: ""},
	}
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "approver_ref is required") {
		t.Errorf("expected approver_ref required error, got %+v", err)
	}
}

func TestBuildMatrixFromRequest_AcceptsValidRequest(t *testing.T) {
	req := &matrixRequest{
		Key:               "articles.publish",
		Name:              "Article publish workflow",
		Description:       "Newsroom DoA",
		OnFinalEscalation: authority.FinalEscalationExpire,
		Levels: []matrixLevelRequest{
			{
				LevelN: 1,
				Name:   "Editorial review",
				Mode:   authority.ModeAny,
				Approvers: []matrixApproverRequest{
					{ApproverType: authority.ApproverTypeRole, ApproverRef: "editor"},
				},
			},
			{
				LevelN: 2,
				Name:   "Chief sign-off",
				Mode:   authority.ModeAny,
				Approvers: []matrixApproverRequest{
					{ApproverType: authority.ApproverTypeRole, ApproverRef: "chief"},
				},
			},
		},
	}
	m, err := buildMatrixFromRequest(req)
	if err != nil {
		t.Fatalf("valid request rejected: %+v", err)
	}
	if m.Key != "articles.publish" {
		t.Errorf("Key not preserved: got %q", m.Key)
	}
	if len(m.Levels) != 2 {
		t.Fatalf("Levels not preserved: got %d, want 2", len(m.Levels))
	}
	if m.Levels[0].Name != "Editorial review" || m.Levels[1].Name != "Chief sign-off" {
		t.Errorf("Level order not preserved: %+v", m.Levels)
	}
	if len(m.Levels[0].Approvers) != 1 ||
		m.Levels[0].Approvers[0].ApproverType != authority.ApproverTypeRole ||
		m.Levels[0].Approvers[0].ApproverRef != "editor" {
		t.Errorf("Approver not preserved: %+v", m.Levels[0].Approvers)
	}
}

func TestBuildMatrixFromRequest_AcceptsThresholdWithMinApprovals(t *testing.T) {
	minN := 2
	lvl := validLevel()
	lvl.Mode = authority.ModeThreshold
	lvl.MinApprovals = &minN
	lvl.Approvers = []matrixApproverRequest{
		{ApproverType: authority.ApproverTypeRole, ApproverRef: "editor"},
		{ApproverType: authority.ApproverTypeRole, ApproverRef: "chief"},
		{ApproverType: authority.ApproverTypeRole, ApproverRef: "legal"},
	}
	req := &matrixRequest{Key: "k", Name: "n", Levels: []matrixLevelRequest{lvl}}
	m, err := buildMatrixFromRequest(req)
	if err != nil {
		t.Fatalf("valid threshold request rejected: %+v", err)
	}
	if m.Levels[0].Mode != authority.ModeThreshold {
		t.Errorf("Mode lost: got %q", m.Levels[0].Mode)
	}
	if m.Levels[0].MinApprovals == nil || *m.Levels[0].MinApprovals != 2 {
		t.Errorf("MinApprovals not preserved: %v", m.Levels[0].MinApprovals)
	}
}

func TestBuildMatrixFromRequest_AcceptsAmountRangeMateriality(t *testing.T) {
	minAmt := int64(0)
	maxAmt := int64(100000)
	req := &matrixRequest{
		Key:       "expenses.approve",
		Name:      "Small expense",
		MinAmount: &minAmt,
		MaxAmount: &maxAmt,
		Currency:  "USD",
		Levels:    []matrixLevelRequest{validLevel()},
	}
	m, err := buildMatrixFromRequest(req)
	if err != nil {
		t.Fatalf("materiality request rejected: %+v", err)
	}
	if m.MinAmount == nil || *m.MinAmount != 0 {
		t.Errorf("MinAmount not preserved: %v", m.MinAmount)
	}
	if m.MaxAmount == nil || *m.MaxAmount != 100000 {
		t.Errorf("MaxAmount not preserved: %v", m.MaxAmount)
	}
	if m.Currency != "USD" {
		t.Errorf("Currency not preserved: %q", m.Currency)
	}
}

func TestBuildMatrixFromRequest_RejectsInvalidEffectiveFromTimestamp(t *testing.T) {
	bad := "not-a-timestamp"
	req := &matrixRequest{
		Key:           "k",
		Name:          "n",
		EffectiveFrom: &bad,
		Levels:        []matrixLevelRequest{validLevel()},
	}
	_, err := buildMatrixFromRequest(req)
	if err == nil || !strings.Contains(err.Message, "invalid ISO 8601") {
		t.Errorf("expected ISO 8601 validation error, got %+v", err)
	}
}

// validLevel returns a minimal-valid level used by every test that
// just wants ONE field invalid.
func validLevel() matrixLevelRequest {
	return matrixLevelRequest{
		LevelN: 1,
		Name:   "Level 1",
		Mode:   authority.ModeAny,
		Approvers: []matrixApproverRequest{
			{ApproverType: authority.ApproverTypeRole, ApproverRef: "editor"},
		},
	}
}
