package scim

import (
	"net/http"
)

// RFC 7644 mandates three discovery endpoints. IdPs (especially Okta)
// fetch all three on first connect to learn what the SCIM service
// supports. Returning the right shape is the difference between
// "Okta connects on the first try" vs "Okta logs a vague error".

// serviceProviderConfig — RFC 7644 §5. Tells the IdP which optional
// features we support. We claim:
//
//   - patch       — supported (PATCH /Users + /Groups)
//   - bulk        — NOT supported (would require transactional
//                   handling across multiple resources; skipped for
//                   v1.7.51 since most IdPs don't emit bulk)
//   - filter      — supported (max 200 per page)
//   - changePassword — NOT supported (SCIM-managed users sign in via
//                      SAML/OAuth; no local password flow)
//   - sort        — supported (RFC 7644 §3.4.2.3; whitelist-mapped
//                   per resource type, see sort.go)
//   - etag        — supported (RFC 7644 §3.7; weak ETags derived from
//                   row mtime, optimistic concurrency via If-Match)
//
// Authentication scheme = HTTP Bearer w/ our SCIM token format.
func writeServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"schemas":          []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"documentationUri": "https://railbase.dev/docs/scim",
		"patch":            map[string]bool{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": 500},
		"changePassword":   map[string]bool{"supported": false},
		"sort":             map[string]bool{"supported": true},
		"etag":             map[string]bool{"supported": true},
		"authenticationSchemes": []map[string]any{{
			"name":        "HTTP Bearer",
			"description": "Authentication via Railbase SCIM bearer credential (rbsm_<token>)",
			"specUri":     "https://datatracker.ietf.org/doc/html/rfc6750",
			"type":        "httpbearer",
			"primary":     true,
		}},
		"meta": map[string]string{
			"resourceType": "ServiceProviderConfig",
			"location":     baseURL(r) + "/scim/v2/ServiceProviderConfig",
		},
	}
	writeJSON(w, http.StatusOK, body)
}

// resourceTypes — RFC 7644 §6. Tells the IdP which resources are
// available + their schema URIs.
func writeResourceTypes(w http.ResponseWriter, r *http.Request) {
	body := scimListResponse{
		Schemas:      []string{listResponseSchema},
		TotalResults: 2,
		StartIndex:   1,
		ItemsPerPage: 2,
		Resources: []any{
			map[string]any{
				"schemas":      []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"},
				"id":           "User",
				"name":         "User",
				"endpoint":     "/Users",
				"description":  "Railbase auth-collection users",
				"schema":       "urn:ietf:params:scim:schemas:core:2.0:User",
				"meta":         map[string]string{"resourceType": "ResourceType", "location": baseURL(r) + "/scim/v2/ResourceTypes/User"},
			},
			map[string]any{
				"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"},
				"id":          "Group",
				"name":        "Group",
				"endpoint":    "/Groups",
				"description": "Directory-style groups synced from the IdP",
				"schema":      "urn:ietf:params:scim:schemas:core:2.0:Group",
				"meta":        map[string]string{"resourceType": "ResourceType", "location": baseURL(r) + "/scim/v2/ResourceTypes/Group"},
			},
		},
	}
	writeJSON(w, http.StatusOK, body)
}

// schemas — RFC 7644 §7. Returns the full SCIM core schema documents
// for User + Group. Inlined as constants below because they're static
// per the spec — the IdP just needs them to validate its requests.
func writeSchemas(w http.ResponseWriter, r *http.Request) {
	body := scimListResponse{
		Schemas:      []string{listResponseSchema},
		TotalResults: 2,
		StartIndex:   1,
		ItemsPerPage: 2,
		Resources:    []any{userSchemaDoc(baseURL(r)), groupSchemaDoc(baseURL(r))},
	}
	writeJSON(w, http.StatusOK, body)
}

// userSchemaDoc is the SCIM core:User schema in document form. We
// only emit the attributes our handlers actually consume — IdPs
// happily ignore unmodelled attributes when they see them missing
// from the schema doc. This is the same trim list as scimUser above.
func userSchemaDoc(base string) map[string]any {
	return map[string]any{
		"id":          "urn:ietf:params:scim:schemas:core:2.0:User",
		"name":        "User",
		"description": "Railbase user resource",
		"attributes": []map[string]any{
			{"name": "userName", "type": "string", "multiValued": false, "required": true, "caseExact": false, "mutability": "readWrite", "returned": "default", "uniqueness": "server"},
			{"name": "active", "type": "boolean", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default"},
			{"name": "externalId", "type": "string", "multiValued": false, "required": false, "caseExact": true, "mutability": "readWrite", "returned": "default"},
			{"name": "emails", "type": "complex", "multiValued": true, "required": false, "mutability": "readWrite", "returned": "default",
				"subAttributes": []map[string]any{
					{"name": "value", "type": "string", "multiValued": false, "required": true},
					{"name": "primary", "type": "boolean", "multiValued": false, "required": false},
					{"name": "type", "type": "string", "multiValued": false, "required": false},
				}},
		},
		"meta": map[string]string{"resourceType": "Schema", "location": base + "/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:User"},
	}
}

func groupSchemaDoc(base string) map[string]any {
	return map[string]any{
		"id":          "urn:ietf:params:scim:schemas:core:2.0:Group",
		"name":        "Group",
		"description": "Railbase SCIM group",
		"attributes": []map[string]any{
			{"name": "displayName", "type": "string", "multiValued": false, "required": true, "mutability": "readWrite", "returned": "default"},
			{"name": "externalId", "type": "string", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default"},
			{"name": "members", "type": "complex", "multiValued": true, "required": false, "mutability": "readWrite", "returned": "default",
				"subAttributes": []map[string]any{
					{"name": "value", "type": "string", "multiValued": false, "required": true},
					{"name": "display", "type": "string", "multiValued": false, "required": false},
					{"name": "type", "type": "string", "multiValued": false, "required": false},
					{"name": "$ref", "type": "reference", "referenceTypes": []string{"User"}, "multiValued": false, "required": false},
				}},
		},
		"meta": map[string]string{"resourceType": "Schema", "location": base + "/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:Group"},
	}
}
