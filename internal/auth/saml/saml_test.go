package saml

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestConfig_Validate(t *testing.T) {
	base := Config{
		IdPMetadataURL: "https://idp.example.com/saml/metadata",
		SPEntityID:     "https://railbase.example.com/saml/sp",
		SPACSURL:       "https://railbase.example.com/saml/acs",
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no metadata", func(c *Config) { c.IdPMetadataURL = ""; c.IdPMetadataXML = "" }, "idp_metadata_url"},
		{"no sp_entity_id", func(c *Config) { c.SPEntityID = "" }, "sp_entity_id"},
		{"no sp_acs_url", func(c *Config) { c.SPACSURL = "" }, "sp_acs_url"},
		{"sp_acs_url no scheme", func(c *Config) { c.SPACSURL = "railbase.example.com/acs" }, "scheme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q doesn't contain %q", err.Error(), tc.want)
			}
		})
	}
	// Inline XML satisfies the metadata requirement (no URL needed).
	c := base
	c.IdPMetadataURL = ""
	c.IdPMetadataXML = "<EntityDescriptor xmlns=\"urn:oasis:names:tc:SAML:2.0:metadata\" entityID=\"x\"/>"
	if err := c.Validate(); err != nil {
		t.Errorf("inline XML should be valid alone: %v", err)
	}
}

func TestParseIdPMetadata_RejectsEmpty(t *testing.T) {
	_, _, _, _, err := parseIdPMetadata([]byte("<EntityDescriptor xmlns=\"urn:oasis:names:tc:SAML:2.0:metadata\" entityID=\"x\"></EntityDescriptor>"))
	if err == nil {
		t.Fatal("expected error for metadata without IDPSSODescriptor")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty(nil) != "" {
		t.Error("nil slice")
	}
	if firstNonEmpty([]string{"", "  ", "found", "other"}) != "found" {
		t.Error("should skip empty/whitespace")
	}
}

func TestLooksLikeEmail(t *testing.T) {
	cases := map[string]bool{
		"alice@example.com": true,
		"not-an-email":      false,
		"@example.com":      false,
		"alice@":            false,
		"alice@example":     false, // no TLD dot
		"":                  false,
	}
	for in, want := range cases {
		if got := looksLikeEmail(in); got != want {
			t.Errorf("looksLikeEmail(%q) = %v, want %v", in, got, want)
		}
	}
}

// v1.7.50.1a — InResponseTo enforcement unit tests.
//
// We don't construct a full SAMLResponse (would require RSA-sign a
// minimal XML doc — that's library-level). Instead we directly
// exercise the validateInResponseTo helper which is the
// security-critical bit.

func TestValidateInResponseTo_MatchingIDPasses(t *testing.T) {
	xml := `<?xml version="1.0"?>
<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol"
    xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion"
    ID="resp1" InResponseTo="req-abc-123" Version="2.0">
  <saml:Assertion ID="a1">
    <saml:Subject>
      <saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer">
        <saml:SubjectConfirmationData InResponseTo="req-abc-123" NotOnOrAfter="2030-01-01T00:00:00Z"/>
      </saml:SubjectConfirmation>
    </saml:Subject>
  </saml:Assertion>
</samlp:Response>`
	b64 := base64.StdEncoding.EncodeToString([]byte(xml))
	if err := validateInResponseTo(b64, "req-abc-123"); err != nil {
		t.Errorf("matching IDs should pass: %v", err)
	}
}

func TestValidateInResponseTo_MismatchedIDRejected(t *testing.T) {
	xml := `<?xml version="1.0"?>
<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol"
    ID="resp1" InResponseTo="req-WRONG" Version="2.0">
</samlp:Response>`
	b64 := base64.StdEncoding.EncodeToString([]byte(xml))
	err := validateInResponseTo(b64, "req-abc-123")
	if err == nil {
		t.Fatal("mismatched IDs should be rejected")
	}
	if !strings.Contains(err.Error(), "InResponseTo") {
		t.Errorf("error should mention InResponseTo, got %q", err.Error())
	}
}

func TestValidateInResponseTo_MissingAttributeRejected(t *testing.T) {
	// Response with NO InResponseTo at all — could be a replayed
	// IdP-initiated assertion the attacker re-purposed.
	xml := `<?xml version="1.0"?>
<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol"
    ID="resp1" Version="2.0">
</samlp:Response>`
	b64 := base64.StdEncoding.EncodeToString([]byte(xml))
	err := validateInResponseTo(b64, "req-abc-123")
	if err == nil {
		t.Fatal("missing InResponseTo should be rejected when expected ID is set")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should call out missing attribute, got %q", err.Error())
	}
}

func TestValidateInResponseTo_SubjectConfirmationDataMismatchRejected(t *testing.T) {
	// Outer InResponseTo matches but a SubjectConfirmationData inside
	// the Assertion disagrees — pathological / probably a wrapping
	// attack attempt.
	xml := `<?xml version="1.0"?>
<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol"
    xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion"
    ID="resp1" InResponseTo="req-abc-123" Version="2.0">
  <saml:Assertion ID="a1">
    <saml:Subject>
      <saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer">
        <saml:SubjectConfirmationData InResponseTo="req-DIFFERENT" NotOnOrAfter="2030-01-01T00:00:00Z"/>
      </saml:SubjectConfirmation>
    </saml:Subject>
  </saml:Assertion>
</samlp:Response>`
	b64 := base64.StdEncoding.EncodeToString([]byte(xml))
	err := validateInResponseTo(b64, "req-abc-123")
	if err == nil {
		t.Fatal("SubjectConfirmationData InResponseTo mismatch should be rejected")
	}
}

func TestValidateInResponseTo_MalformedXMLRejected(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("<<<not xml>>>"))
	err := validateInResponseTo(b64, "anything")
	if err == nil {
		t.Fatal("malformed XML should be rejected")
	}
}

func TestValidateInResponseTo_MalformedBase64Rejected(t *testing.T) {
	err := validateInResponseTo("not-base64!@#", "anything")
	if err == nil {
		t.Fatal("malformed base64 should be rejected")
	}
}
