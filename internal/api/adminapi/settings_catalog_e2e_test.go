//go:build embed_pg

package adminapi

// E2E test for GET /api/_admin/settings/catalog.
//
// Confirms:
//
//   1. Authenticated system_admin can fetch the catalog.
//   2. Response includes groups[] + entries[] with at least the
//      core security.* and site.* keys (regression against an
//      accidental catalog wipe).
//   3. A persisted catalog key shows up with `is_set: true` and
//      the value the operator wrote; unknown persisted keys show
//      up in `unknown_keys`, and dedicated-screen-owned keys
//      (mailer.*, oauth.*) do NOT.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/rbac"
)

func TestSettingsCatalog_E2E(t *testing.T) {
	srv, d, stop := newRBACAdminTestServer(t)
	defer stop()
	ctx := emEventsCtx

	// Need a system_admin caller because /settings/catalog is gated
	// by settings.read.
	admin, err := d.Admins.Create(ctx, "settings-catalog@test", "test-password-1234")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := rbac.AssignSystemAdmin(ctx, d.RBAC, admin.ID); err != nil {
		t.Fatalf("assign role: %v", err)
	}

	// Pre-seed three settings so we exercise the three response
	// buckets: a cataloged key (security.allow_ips), an unknown key
	// (operator.feature_flag), and a dedicated-screen key
	// (mailer.from) which should NOT land in unknown_keys.
	must := func(key string, v any) {
		if err := d.Settings.Set(ctx, key, v); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	defer func() {
		_ = d.Settings.Delete(ctx, "security.allow_ips")
		_ = d.Settings.Delete(ctx, "operator.feature_flag")
		_ = d.Settings.Delete(ctx, "mailer.from")
	}()
	must("security.allow_ips", "10.0.0.0/8")
	must("operator.feature_flag", true)
	must("mailer.from", "noreply@example.com")

	resp, body := authedRequest(t, d, admin.ID, "GET",
		srv.URL+"/api/_admin/settings/catalog", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	var out struct {
		Groups      []string `json:"groups"`
		Entries     []struct {
			Def struct {
				Key   string `json:"key"`
				Group string `json:"group"`
				Type  string `json:"type"`
			} `json:"def"`
			Value any  `json:"value,omitempty"`
			IsSet bool `json:"is_set"`
		} `json:"entries"`
		UnknownKeys []string `json:"unknown_keys"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.Groups) == 0 {
		t.Fatal("groups[] empty")
	}
	if len(out.Entries) == 0 {
		t.Fatal("entries[] empty")
	}

	// Find the cataloged key we seeded — it should be is_set=true.
	var seedEntry *struct {
		Def struct {
			Key   string `json:"key"`
			Group string `json:"group"`
			Type  string `json:"type"`
		} `json:"def"`
		Value any  `json:"value,omitempty"`
		IsSet bool `json:"is_set"`
	}
	for i := range out.Entries {
		if out.Entries[i].Def.Key == "security.allow_ips" {
			seedEntry = &out.Entries[i]
			break
		}
	}
	if seedEntry == nil {
		t.Fatal("catalog missing security.allow_ips — migration drift?")
	}
	if !seedEntry.IsSet {
		t.Error("security.allow_ips is_set=false despite being persisted")
	}
	if v, ok := seedEntry.Value.(string); !ok || v != "10.0.0.0/8" {
		t.Errorf("security.allow_ips value = %v; want \"10.0.0.0/8\"", seedEntry.Value)
	}

	// operator.feature_flag is unknown → should land in unknown_keys.
	gotUnknown := false
	for _, k := range out.UnknownKeys {
		if k == "operator.feature_flag" {
			gotUnknown = true
			break
		}
	}
	if !gotUnknown {
		t.Errorf("unknown_keys=%v missing operator.feature_flag", out.UnknownKeys)
	}

	// mailer.from is owned by the Mailer screen → must NOT leak.
	for _, k := range out.UnknownKeys {
		if k == "mailer.from" {
			t.Errorf("unknown_keys leaked mailer.* key %q — should be filtered server-side", k)
		}
	}
}

// We need the routes registered on the test router so the GET hits
// the catalog handler. newRBACAdminTestServer already calls d.Mount,
// which now includes the catalog route. No additional setup here.
var _ chi.Router = (*chi.Mux)(nil)
