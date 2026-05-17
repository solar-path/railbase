package authority

import "testing"

// TestWorkflow_OnLevel verifies the level-aware accessor for all
// running + terminal workflow states.
func TestWorkflow_OnLevel(t *testing.T) {
	five := 5
	cases := []struct {
		name    string
		wf      *Workflow
		wantLv  int
		wantOk  bool
	}{
		{"nil workflow", nil, 0, false},
		{"running with level", &Workflow{
			Status: WorkflowStatusRunning, CurrentLevel: &five,
		}, 5, true},
		{"completed (level nil)", &Workflow{
			Status: WorkflowStatusCompleted, CurrentLevel: nil,
		}, 0, false},
		{"rejected", &Workflow{
			Status: WorkflowStatusRejected, CurrentLevel: nil,
		}, 0, false},
		{"cancelled", &Workflow{
			Status: WorkflowStatusCancelled, CurrentLevel: nil,
		}, 0, false},
		{"expired", &Workflow{
			Status: WorkflowStatusExpired, CurrentLevel: nil,
		}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lv, ok := tc.wf.OnLevel()
			if lv != tc.wantLv || ok != tc.wantOk {
				t.Errorf("OnLevel(): got (%d, %v), want (%d, %v)",
					lv, ok, tc.wantLv, tc.wantOk)
			}
		})
	}
}

// TestWorkflow_IsTerminal verifies the negative-form accessor.
func TestWorkflow_IsTerminal(t *testing.T) {
	cases := []struct {
		name string
		wf   *Workflow
		want bool
	}{
		{"nil treated as terminal", nil, true},
		{"running", &Workflow{Status: WorkflowStatusRunning}, false},
		{"completed", &Workflow{Status: WorkflowStatusCompleted}, true},
		{"rejected", &Workflow{Status: WorkflowStatusRejected}, true},
		{"cancelled", &Workflow{Status: WorkflowStatusCancelled}, true},
		{"expired", &Workflow{Status: WorkflowStatusExpired}, true},
		{"empty status (defensive)", &Workflow{Status: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.wf.IsTerminal(); got != tc.want {
				t.Errorf("IsTerminal(): got %v, want %v", got, tc.want)
			}
		})
	}
}
