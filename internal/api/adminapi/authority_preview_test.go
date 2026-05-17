package adminapi

// Pure-function tests for the §3.3 selection-preview reason logic.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/authority"
)

func TestPreviewReason(t *testing.T) {
	tenantA := uuid.New()
	site := (*uuid.UUID)(nil)
	floor1k := int64(1000)
	floor10k := int64(10000)

	tenantMatrix := &authority.Matrix{ID: uuid.New(), TenantID: &tenantA, MinAmount: &floor10k}
	siteHi := &authority.Matrix{ID: uuid.New(), TenantID: site, MinAmount: &floor10k}
	siteLo := &authority.Matrix{ID: uuid.New(), TenantID: site, MinAmount: &floor1k}

	cases := []struct {
		name     string
		selected *authority.Matrix
		all      []authority.Matrix
		wantSub  string
	}{
		{
			name:     "no match",
			selected: nil,
			all:      nil,
			wantSub:  "no matrix matches",
		},
		{
			name:     "single candidate",
			selected: siteHi,
			all:      []authority.Matrix{*siteHi},
			wantSub:  "only one candidate",
		},
		{
			name:     "tenant + floor win",
			selected: tenantMatrix,
			all:      []authority.Matrix{*tenantMatrix, *siteLo},
			wantSub:  "tenant-specific match + highest min_amount",
		},
		{
			name:     "tenant only",
			selected: tenantMatrix,
			all:      []authority.Matrix{*tenantMatrix, *siteHi},
			wantSub:  "tenant-specific match preferred",
		},
		{
			name:     "floor only",
			selected: siteHi,
			all:      []authority.Matrix{*siteHi, *siteLo},
			wantSub:  "highest min_amount floor",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewReason(tc.selected, tc.all)
			if !containsStr(got, tc.wantSub) {
				t.Errorf("reason: got %q, want substring %q", got, tc.wantSub)
			}
		})
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
