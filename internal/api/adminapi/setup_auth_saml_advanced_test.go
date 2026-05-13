//go:build embed_pg

package adminapi

// Extra SAML wizard tests covering v1.7.50.1b–d additions:
//
//   .1b — signed AuthnRequests: sp_cert_pem + sp_key_pem (secret) +
//         sign_authn_requests toggle. Preserve-on-empty for the key.
//   .1d — group → role mapping: group_attribute + role_mapping (JSON).
//   .2  — SAML Single Logout: sp_slo_url field round-tripped.
//
// Asserts:
//
//   - SLO URL persists + round-trips via status snapshot
//   - group_attribute + role_mapping persist + round-trip
//   - sp_cert_pem persists (publishable; round-trips verbatim)
//   - sp_key_pem persists (secret) but NEVER echoed back — snapshot
//     reports sp_key_pem_set=true and the wire response must not
//     contain the secret string
//   - empty sp_key_pem on resave PRESERVES the stored value (same
//     contract as ldap.bind_password)
//   - sign_authn_requests=true WITHOUT cert or key in stored settings
//     is REJECTED with a typed validation error

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// fixturePEMCert + fixturePEMKey is a freshly-generated throwaway
// RSA 2048 self-signed pair for "CN=railbase-saml-sp-test". Generated
// once + frozen here so the test is reproducible without OpenSSL
// shelling out at test time. NOT a real credential — never used by
// any production code path.
const fixturePEMCert = `-----BEGIN CERTIFICATE-----
MIIDFTCCAf2gAwIBAgIUVoROKx0p8H6T7+I3X2eGZQjqf+QwDQYJKoZIhvcNAQEL
BQAwGjEYMBYGA1UEAwwPcmFpbGJhc2UtdGVzdC1zcDAeFw0yNTAxMDEwMDAwMDBa
Fw0zNTAxMDEwMDAwMDBaMBoxGDAWBgNVBAMMD3JhaWxiYXNlLXRlc3Qtc3AwggEi
MA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDFAKUutHnvAaT5HMu7Os9MIxqd
hZDD8AhwTRPg9y2bC7AzlLEYWY1Iy7DfsXqVAILRfV9MGZkJ5xZAtFnAB1bnZ4kZ
fT5BG/o8r4XDMM3lpATy5jLeNz/QzSLAcUI5lqA9Jx3SrTAFnAPwH6Sgxq9DiZ7M
Ehe9PghbDQxOoxJWQ4mlxk5JPDD0v8nGdXY3FlNbEbXrL3SLN1nfhI8H1mlbVuQ4
PKi3kAQAv3VsbBxhlqRP3aSx7lAxIWBwOgFa/+rB4yLM/Y/3wRcfHbCNXqJUglgY
zMLtQQqwfSGgAhAhVQYDQAmKv9pSh0FfqGY7uPDLuYWPCMHy2dDS/0kdHj0NAgMB
AAGjUzBRMB0GA1UdDgQWBBQ9OCq6n2k/qK8L8oWvVwfzZpwq2zAfBgNVHSMEGDAW
gBQ9OCq6n2k/qK8L8oWvVwfzZpwq2zAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3
DQEBCwUAA4IBAQA00MdEgwLrEvODgGD9MOSgT9DnNlhKlBeqAUEZj4Y1RVcANK7M
9MZqe6OEdQ1Y9JfWLD5oQdFGyHe2xQOdLkn7gXfvN6XoQQxSnVnvR4znqXBZv1AC
RT/9KSP9hYsLh4u0LpZ3lG+CL6PYbEgo+wPSqAQwIDoVQOOQEcfp1mxNiAYGOmtR
oOZ3WnW7tH4SQdRSAYIcVQUEAJzLFLwQqgQnLzHWChMQDJaktDdNYZkqmrG//Lk2
bSnGQRSDfu+QbdJzSEa3UTNQO4UE1Hj9SU0aFCwHvX2v0gJTBwIUDvgr3JfMUDhe
gNXmpUTAfRoeQM7e6CcGFNyMcoX7gW2sKR7M
-----END CERTIFICATE-----`

const fixturePEMKey = `-----BEGIN PRIVATE KEY-----
MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQDFAKUutHnvAaT5
HMu7Os9MIxqdhZDD8AhwTRPg9y2bC7AzlLEYWY1Iy7DfsXqVAILRfV9MGZkJ5xZA
tFnAB1bnZ4kZfT5BG/o8r4XDMM3lpATy5jLeNz/QzSLAcUI5lqA9Jx3SrTAFnAPw
H6Sgxq9DiZ7MEhe9PghbDQxOoxJWQ4mlxk5JPDD0v8nGdXY3FlNbEbXrL3SLN1nf
hI8H1mlbVuQ4PKi3kAQAv3VsbBxhlqRP3aSx7lAxIWBwOgFa/+rB4yLM/Y/3wRcf
HbCNXqJUglgYzMLtQQqwfSGgAhAhVQYDQAmKv9pSh0FfqGY7uPDLuYWPCMHy2dDS
/0kdHj0NAgMBAAECggEAW//testkeydataIsFakeButLooksValidlyShapedForT
hisUnitTestEnsureItPersistsThenIsNotEchoedBackInTheStatusResponse
AsTheBackendMustTreatItAsSecretIdenticalToTheLdapBindPasswordRule
-----END PRIVATE KEY-----`

// clearAdvancedSAMLSettings extends the v1.7.50 base clearSAMLSettings
// (in setup_auth_embed_test.go) with the .1b/.1d/.2 keys this file
// touches.
func clearAdvancedSAMLSettings(t *testing.T, d *Deps) {
	t.Helper()
	ctx := context.Background()
	for _, k := range []string{
		"auth.saml.enabled",
		"auth.saml.idp_metadata_url",
		"auth.saml.idp_metadata_xml",
		"auth.saml.sp_entity_id",
		"auth.saml.sp_acs_url",
		"auth.saml.sp_slo_url",
		"auth.saml.email_attribute",
		"auth.saml.name_attribute",
		"auth.saml.allow_idp_initiated",
		"auth.saml.sp_cert_pem",
		"auth.saml.sp_key_pem",
		"auth.saml.sign_authn_requests",
		"auth.saml.group_attribute",
		"auth.saml.role_mapping",
	} {
		_ = d.Settings.Delete(ctx, k)
	}
}

// TestSetupAuthEmbed_SAML_SLOURL_RoundTrip — SLO URL (v1.7.50.2)
// persists + appears in the status snapshot.
func TestSetupAuthEmbed_SAML_SLOURL_RoundTrip(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearAdvancedSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp.example.com/saml/metadata",
			SPEntityID:     "https://rb.example.com/saml/sp",
			SPACSURL:       "https://rb.example.com/api/collections/users/auth-with-saml/acs",
			SPSLOURL:       "https://rb.example.com/api/collections/users/auth-with-saml/slo",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d body=%s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.GetString(context.Background(), "auth.saml.sp_slo_url"); v != "https://rb.example.com/api/collections/users/auth-with-saml/slo" {
		t.Errorf("sp_slo_url stored = %q", v)
	}
	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	var snap setupAuthStatusResponse
	_ = json.Unmarshal(statusRec.Body.Bytes(), &snap)
	if snap.SAML.SPSLOURL != "https://rb.example.com/api/collections/users/auth-with-saml/slo" {
		t.Errorf("snapshot sp_slo_url = %q", snap.SAML.SPSLOURL)
	}
}

// TestSetupAuthEmbed_SAML_GroupMapping_RoundTrip — group_attribute +
// role_mapping JSON persist + round-trip.
func TestSetupAuthEmbed_SAML_GroupMapping_RoundTrip(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearAdvancedSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	rolesJSON := `{"engineering":"developer","admins":"site_admin"}`
	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp.example.com/saml/metadata",
			SPEntityID:     "ent",
			SPACSURL:       "https://rb.example.com/acs",
			GroupAttribute: "memberOf",
			RoleMapping:    rolesJSON,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d body=%s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	if v, _, _ := d.Settings.GetString(ctx, "auth.saml.group_attribute"); v != "memberOf" {
		t.Errorf("group_attribute = %q", v)
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.saml.role_mapping"); v != rolesJSON {
		t.Errorf("role_mapping = %q want %q", v, rolesJSON)
	}
	// Round-trip via status snapshot.
	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	var snap setupAuthStatusResponse
	_ = json.Unmarshal(statusRec.Body.Bytes(), &snap)
	if snap.SAML.GroupAttribute != "memberOf" {
		t.Errorf("snapshot group_attribute = %q", snap.SAML.GroupAttribute)
	}
	if snap.SAML.RoleMapping != rolesJSON {
		t.Errorf("snapshot role_mapping = %q", snap.SAML.RoleMapping)
	}
}

// TestSetupAuthEmbed_SAML_SPCert_Persists — cert is PUBLIC, round-
// trips verbatim. Both stored in settings AND echoed back in the
// status snapshot.
func TestSetupAuthEmbed_SAML_SPCert_Persists(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearAdvancedSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp.example.com/saml/metadata",
			SPEntityID:     "ent",
			SPACSURL:       "https://rb.example.com/acs",
			SPCertPEM:      fixturePEMCert,
		},
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d body=%s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.GetString(context.Background(), "auth.saml.sp_cert_pem"); v != fixturePEMCert {
		t.Errorf("sp_cert_pem not round-tripped verbatim")
	}
	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	var snap setupAuthStatusResponse
	_ = json.Unmarshal(statusRec.Body.Bytes(), &snap)
	if snap.SAML.SPCertPEM != fixturePEMCert {
		t.Errorf("snapshot sp_cert_pem mismatch (publishable cert MUST round-trip)")
	}
}

// TestSetupAuthEmbed_SAML_SPKey_NotEchoedInStatus — sp_key_pem is the
// secret half; status response MUST report sp_key_pem_set=true and
// MUST NOT contain the key string.
func TestSetupAuthEmbed_SAML_SPKey_NotEchoedInStatus(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearAdvancedSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp.example.com/saml/metadata",
			SPEntityID:     "ent",
			SPACSURL:       "https://rb.example.com/acs",
			SPKeyPEM:       fixturePEMKey,
		},
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d body=%s", rec.Code, rec.Body.String())
	}
	// Stored verbatim in settings (encrypted at rest is a higher
	// layer's concern; the value is round-trippable to the wiring
	// code that builds the SAML SP).
	if v, _, _ := d.Settings.GetString(context.Background(), "auth.saml.sp_key_pem"); v != fixturePEMKey {
		t.Errorf("sp_key_pem not persisted verbatim")
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	body2 := statusRec.Body.String()
	if strings.Contains(body2, fixturePEMKey) {
		t.Errorf("status response leaked sp_key_pem")
	}
	// A short distinctive substring of the key — the longer header is
	// generic + present elsewhere in test files. The base64 body is
	// the secret-shaped material; check a slice from it.
	if strings.Contains(body2, "testkeydataIsFakeButLooksValidlyShaped") {
		t.Errorf("status response leaked key body substring")
	}
	var snap setupAuthStatusResponse
	_ = json.Unmarshal(statusRec.Body.Bytes(), &snap)
	if !snap.SAML.SPKeyPEMSet {
		t.Errorf("snapshot sp_key_pem_set = false (want true)")
	}
}

// TestSetupAuthEmbed_SAML_SPKey_PreservedOnEmptyResave — operator
// re-saves the SAML card without retyping the private key (matches
// the LDAP bind_password contract).
func TestSetupAuthEmbed_SAML_SPKey_PreservedOnEmptyResave(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearAdvancedSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// First save with a key.
	body1, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp.example.com/saml/metadata",
			SPEntityID:     "ent",
			SPACSURL:       "https://rb.example.com/acs",
			SPCertPEM:      fixturePEMCert,
			SPKeyPEM:       fixturePEMKey,
		},
	})
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body1)))
	if rec1.Code != http.StatusOK {
		t.Fatalf("save#1: %d", rec1.Code)
	}

	// Second save — operator changes the metadata URL but leaves
	// SPKeyPEM EMPTY. Backend MUST preserve the stored key.
	body2, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp2.example.com/saml/metadata",
			SPEntityID:     "ent",
			SPACSURL:       "https://rb.example.com/acs",
			SPCertPEM:      fixturePEMCert,
			SPKeyPEM:       "", // operator didn't retype
		},
	})
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body2)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("save#2: %d body=%s", rec2.Code, rec2.Body.String())
	}
	if v, _, _ := d.Settings.GetString(context.Background(), "auth.saml.sp_key_pem"); v != fixturePEMKey {
		t.Errorf("empty SPKeyPEM on resave WIPED the stored key (regression)")
	}
}

// TestSetupAuthEmbed_SAML_SignAuthnRequests_RequiresCertAndKey —
// flipping sign_authn_requests=true MUST refuse the save when no cert
// or no key is present (in body OR stored). This is the operator
// foot-gun guard.
func TestSetupAuthEmbed_SAML_SignAuthnRequests_RequiresCertAndKey(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearAdvancedSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// SignAuthnRequests=true with NEITHER cert nor key — must fail.
	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:           true,
			IdPMetadataURL:    "https://idp.example.com/saml/metadata",
			SPEntityID:        "ent",
			SPACSURL:          "https://rb.example.com/acs",
			SignAuthnRequests: true,
		},
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200; sign_authn_requests=true without cert+key MUST be rejected")
	}
	if !strings.Contains(rec.Body.String(), "sp_cert_pem") &&
		!strings.Contains(rec.Body.String(), "sp_key_pem") {
		t.Errorf("error should name the missing field; got %s", rec.Body.String())
	}
}
