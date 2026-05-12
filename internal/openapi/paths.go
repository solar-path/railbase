package openapi

import (
	"github.com/railbase/railbase/internal/schema/builder"
)

// hasFileField reports whether any user-declared field on the
// collection is TypeFile or TypeFiles. Drives the multipart/form-data
// request-body variant on create/update — collections with no
// file-typed fields stay JSON-only.
func hasFileField(spec builder.CollectionSpec) bool {
	for _, f := range spec.Fields {
		if f.Type == builder.TypeFile || f.Type == builder.TypeFiles {
			return true
		}
	}
	return false
}

// emitCollectionPaths registers the 5 CRUD path items for a non-auth
// collection, and the 4 record paths + 5 auth-specific paths for an
// auth collection. Operation IDs follow the pattern
// `{verb}{Collection}` so SDK generators get stable names.
//
// Auth-collection /records is read-only via this surface: POST
// /records returns 403 with a "use /auth-signup" hint at runtime, so
// we document /auth-signup instead of /records POST. PATCH/DELETE
// remain available because admins curate auth-collection rows by id.
func emitCollectionPaths(paths *Paths, spec builder.CollectionSpec) {
	name := typeName(spec.Name)
	tag := spec.Name
	base := "/api/collections/" + spec.Name

	// --- list ---
	listOp := &Operation{
		OperationID: "list" + name,
		Tags:        []string{tag},
		Summary:     "List " + spec.Name + " records",
		Description: "Paginated list. Supports `filter`, `sort`, `page`, `perPage`. " +
			"For collections with `.SoftDelete()`, tombstones are hidden unless " +
			"`includeDeleted=true`.",
		Parameters: append(listQueryParams(), softDeleteParam(spec.SoftDelete)...),
		Responses: map[string]Response{
			"200": {
				Description: "Paginated list of " + spec.Name + " rows.",
				Content: map[string]MediaType{
					"application/json": {Schema: &Schema{Ref: "#/components/schemas/" + name + "List"}},
				},
			},
			"400": errorRef("Validation error in filter/sort/pagination."),
			"401": errorRef("Missing or invalid bearer token."),
			"403": errorRef("ListRule denied access."),
		},
	}

	// --- view ---
	viewOp := &Operation{
		OperationID: "view" + name,
		Tags:        []string{tag},
		Summary:     "Fetch one " + spec.Name + " record by id",
		Parameters:  []Parameter{idPathParam()},
		Responses: map[string]Response{
			"200": {
				Description: "Row.",
				Content: map[string]MediaType{
					"application/json": {Schema: &Schema{Ref: "#/components/schemas/" + name}},
				},
			},
			"401": errorRef("Missing or invalid bearer token."),
			"403": errorRef("ViewRule denied access."),
			"404": errorRef("Row not found."),
		},
	}

	// --- update ---
	updateOp := &Operation{
		OperationID: "update" + name,
		Tags:        []string{tag},
		Summary:     "Update " + spec.Name + " record by id (PATCH semantics)",
		Parameters:  []Parameter{idPathParam()},
		RequestBody: requestBodyFor(spec, name+"UpdateInput"),
		Responses: map[string]Response{
			"200": {
				Description: "Updated row.",
				Content: map[string]MediaType{
					"application/json": {Schema: &Schema{Ref: "#/components/schemas/" + name}},
				},
			},
			"400": errorRef("Validation error."),
			"401": errorRef("Missing or invalid bearer token."),
			"403": errorRef("UpdateRule denied access."),
			"404": errorRef("Row not found."),
		},
	}

	// --- delete ---
	deleteOp := &Operation{
		OperationID: "delete" + name,
		Tags:        []string{tag},
		Summary:     "Delete " + spec.Name + " record by id",
		Description: "For `.SoftDelete()` collections this writes a tombstone instead of a hard delete.",
		Parameters:  []Parameter{idPathParam()},
		Responses: map[string]Response{
			"204": {Description: "Deleted."},
			"401": errorRef("Missing or invalid bearer token."),
			"403": errorRef("DeleteRule denied access."),
			"404": errorRef("Row not found."),
		},
	}

	// --- create (non-auth only) ---
	var createOp *Operation
	if !spec.Auth {
		createOp = &Operation{
			OperationID: "create" + name,
			Tags:        []string{tag},
			Summary:     "Create " + spec.Name + " record",
			RequestBody: requestBodyFor(spec, name+"CreateInput"),
			Responses: map[string]Response{
				"201": {
					Description: "Created row.",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{Ref: "#/components/schemas/" + name}},
					},
				},
				"400": errorRef("Validation error."),
				"401": errorRef("Missing or invalid bearer token."),
				"403": errorRef("CreateRule denied access."),
			},
		}
	}

	paths.Set(base+"/records", &PathItem{
		Get:  listOp,
		Post: createOp, // nil for auth-collections — POST /records returns 403 at runtime
	})
	paths.Set(base+"/records/{id}", &PathItem{
		Get:    viewOp,
		Patch:  updateOp,
		Delete: deleteOp,
	})

	if spec.Auth {
		emitAuthPaths(paths, spec, base, name)
	}

	// Synchronous export endpoints. Emitted only when the collection
	// declared an export config; plain collections don't get them
	// because the runtime handler refuses uncategorised exports (no
	// allow-list = no /export.xlsx surface in v1.7.x semantics).
	if spec.Exports.XLSX != nil || spec.Exports.PDF != nil {
		emitExportPaths(paths, spec, base, name)
	}
}

// emitExportPaths registers the sync export endpoints for a single
// collection. Only emits xlsx / pdf when the collection actually
// declared the corresponding config — schemas with .Export(ExportXLSX(...))
// but no PDF block get just the .xlsx path, mirroring runtime behaviour.
func emitExportPaths(paths *Paths, spec builder.CollectionSpec, base, name string) {
	tag := spec.Name
	commonParams := append([]Parameter{
		{Name: "filter", In: "query", Schema: &Schema{Type: "string"}, Description: "PB-style filter expression."},
		{Name: "sort", In: "query", Schema: &Schema{Type: "string"}, Description: "Comma-separated `±field`."},
		{Name: "columns", In: "query", Schema: &Schema{Type: "string"}, Description: "Comma-separated allow-list of column keys."},
	}, softDeleteParam(spec.SoftDelete)...)

	if spec.Exports.XLSX != nil {
		xlsxParams := append([]Parameter(nil), commonParams...)
		xlsxParams = append(xlsxParams, Parameter{
			Name: "sheet", In: "query", Schema: &Schema{Type: "string"},
			Description: "Worksheet name. Defaults to the collection name.",
		})
		paths.Set(base+"/export.xlsx", &PathItem{
			Get: &Operation{
				OperationID: "exportXlsx" + name,
				Tags:        []string{tag, "export"},
				Summary:     "Synchronous XLSX export of " + spec.Name,
				Description: "Returns the rendered workbook as a binary stream. " +
					"For large datasets use POST /api/exports (async) instead.",
				Parameters: xlsxParams,
				Responses: map[string]Response{
					"200": {
						Description: "Workbook bytes.",
						Content: map[string]MediaType{
							"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": {
								Schema: &Schema{Type: "string", Format: "binary"},
							},
						},
					},
					"400": errorRef("Validation error in filter/sort/columns/sheet."),
					"401": errorRef("Missing or invalid bearer token."),
					"403": errorRef("ListRule denied access."),
					"404": errorRef("Unknown collection."),
				},
			},
		})
	}

	if spec.Exports.PDF != nil {
		pdfParams := append([]Parameter(nil), commonParams...)
		pdfParams = append(pdfParams,
			Parameter{Name: "title", In: "query", Schema: &Schema{Type: "string"}, Description: "Document title."},
			Parameter{Name: "header", In: "query", Schema: &Schema{Type: "string"}, Description: "Page header text."},
			Parameter{Name: "footer", In: "query", Schema: &Schema{Type: "string"}, Description: "Page footer text."},
		)
		paths.Set(base+"/export.pdf", &PathItem{
			Get: &Operation{
				OperationID: "exportPdf" + name,
				Tags:        []string{tag, "export"},
				Summary:     "Synchronous PDF export of " + spec.Name,
				Description: "Returns the rendered PDF as a binary stream. " +
					"For large datasets use POST /api/exports (async) instead.",
				Parameters: pdfParams,
				Responses: map[string]Response{
					"200": {
						Description: "PDF bytes.",
						Content: map[string]MediaType{
							"application/pdf": {
								Schema: &Schema{Type: "string", Format: "binary"},
							},
						},
					},
					"400": errorRef("Validation error."),
					"401": errorRef("Missing or invalid bearer token."),
					"403": errorRef("ListRule denied access."),
					"404": errorRef("Unknown collection."),
				},
			},
		})
	}
}

// emitAsyncExportPaths registers the cross-collection async export
// surface: enqueue, poll, download. Mounted once per spec (not per
// collection) because the same routes serve every collection's exports.
func emitAsyncExportPaths(paths *Paths) {
	paths.Set("/api/exports", &PathItem{
		Post: &Operation{
			OperationID: "enqueueExport",
			Tags:        []string{"export"},
			Summary:     "Enqueue an async export job",
			Description: "Returns 202 immediately. Poll GET /api/exports/{id} for status; " +
				"once status is `completed`, the response carries a signed `file_url` to download.",
			RequestBody: &RequestBody{
				Required: true,
				Content: map[string]MediaType{
					"application/json": {Schema: &Schema{Ref: "#/components/schemas/AsyncExportRequest"}},
				},
			},
			Responses: map[string]Response{
				"202": {
					Description: "Job accepted.",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{Ref: "#/components/schemas/AsyncExportAccepted"}},
					},
				},
				"400": errorRef("Validation error (bad format, unknown collection, etc.)."),
				"401": errorRef("Missing or invalid bearer token."),
				"403": errorRef("ListRule denied access or auth-collection exports."),
			},
		},
	})

	paths.Set("/api/exports/{id}", &PathItem{
		Get: &Operation{
			OperationID: "getExport",
			Tags:        []string{"export"},
			Summary:     "Poll the status of an async export",
			Parameters: []Parameter{{
				Name: "id", In: "path", Required: true,
				Schema:      &Schema{Type: "string", Format: "uuid"},
				Description: "Export job id (returned by POST /api/exports).",
			}},
			Responses: map[string]Response{
				"200": {
					Description: "Current export state.",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{Ref: "#/components/schemas/AsyncExportStatus"}},
					},
				},
				"401": errorRef("Missing or invalid bearer token."),
				"404": errorRef("Export not found."),
			},
		},
	})

	paths.Set("/api/exports/{id}/file", &PathItem{
		Get: &Operation{
			OperationID: "downloadExport",
			Tags:        []string{"export"},
			Summary:     "Download a completed async export file",
			Description: "Signed-URL endpoint — the `token`+`expires` pair returned by GET /api/exports/{id} " +
				"is the auth. No bearer token required (the HMAC IS the credential).",
			Parameters: []Parameter{
				{Name: "id", In: "path", Required: true,
					Schema:      &Schema{Type: "string", Format: "uuid"},
					Description: "Export job id."},
				{Name: "token", In: "query", Required: true,
					Schema: &Schema{Type: "string"}, Description: "HMAC signature from status response."},
				{Name: "expires", In: "query", Required: true,
					Schema: &Schema{Type: "string"}, Description: "Unix-seconds expiry from status response."},
			},
			Responses: map[string]Response{
				"200": {
					Description: "Rendered file bytes.",
					Content: map[string]MediaType{
						"application/pdf": {
							Schema: &Schema{Type: "string", Format: "binary"},
						},
						"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": {
							Schema: &Schema{Type: "string", Format: "binary"},
						},
					},
				},
				"401": errorRef("Invalid or expired signature."),
				"404": errorRef("Export not found, not completed, or file expired."),
			},
		},
	})
}

// emitRealtimePath registers the SSE subscription endpoint. The
// response content type is `text/event-stream` and the schema is a
// permissive string — OpenAPI 3.1 cannot natively describe SSE frames,
// so we document the wire shape in the description and let clients use
// EventSource (or an SSE library) to consume.
func emitRealtimePath(paths *Paths) {
	paths.Set("/api/realtime", &PathItem{
		Get: &Operation{
			OperationID: "subscribeRealtime",
			Tags:        []string{"realtime"},
			Summary:     "Subscribe to realtime collection events (SSE)",
			Description: "Opens a `text/event-stream` connection. Each event is an SSE frame:\n\n" +
				"```\nevent: <topic>\nid: <monotonic-id>\ndata: {\"verb\":\"create|update|delete\",\"record\":{...}}\n\n```\n\n" +
				"Pass `Last-Event-ID` (preferred) or `?since=<id>` to resume from a known point. " +
				"Use EventSource in browsers; any SSE-aware HTTP client elsewhere.",
			Parameters: []Parameter{
				{Name: "topics", In: "query", Required: true,
					Schema:      &Schema{Type: "string"},
					Description: "Comma-separated topic patterns. Each is `<collection>/<id>` or `<collection>/*` for all rows."},
				{Name: "since", In: "query",
					Schema:      &Schema{Type: "string"},
					Description: "Resume from this monotonic event id. Ignored when `Last-Event-ID` header is present."},
				{Name: "Last-Event-ID", In: "header",
					Schema:      &Schema{Type: "string"},
					Description: "Standard SSE resume token; takes precedence over `since`."},
			},
			Responses: map[string]Response{
				"200": {
					Description: "Open SSE stream. Connection stays open until the client disconnects.",
					Content: map[string]MediaType{
						"text/event-stream": {
							Schema: &Schema{Type: "string", Description: "SSE frames; see operation description for shape."},
						},
					},
				},
				"400": errorRef("Missing or invalid topics."),
				"401": errorRef("Missing or invalid bearer token."),
			},
		},
	})
}

// emitAuthPaths adds the auth-collection-specific endpoints: signup,
// signin, refresh, logout, and the v1.7.0 discovery endpoint.
func emitAuthPaths(paths *Paths, spec builder.CollectionSpec, base, name string) {
	tag := spec.Name

	paths.Set(base+"/auth-signup", &PathItem{
		Post: &Operation{
			OperationID: "signup" + name,
			Tags:        []string{tag},
			Summary:     "Create account + auto-signin (returns session token)",
			RequestBody: &RequestBody{
				Required: true,
				Content: map[string]MediaType{
					"application/json": {Schema: &Schema{
						Type:     "object",
						Required: []string{"email", "password"},
						Properties: map[string]*Schema{
							"email":           {Type: "string", Format: "email"},
							"password":        {Type: "string", MinLength: intp(8)},
							"passwordConfirm": {Type: "string"},
						},
					}},
				},
			},
			Responses: map[string]Response{
				"200": authResponseOK(),
				"400": errorRef("Validation error (bad email, password too short, etc.)."),
				"409": errorRef("Email already in use."),
			},
		},
	})

	paths.Set(base+"/auth-with-password", &PathItem{
		Post: &Operation{
			OperationID: "signinWithPassword" + name,
			Tags:        []string{tag},
			Summary:     "Sign in with email + password",
			RequestBody: &RequestBody{
				Required: true,
				Content: map[string]MediaType{
					"application/json": {Schema: &Schema{
						Type:     "object",
						Required: []string{"password"},
						Properties: map[string]*Schema{
							"identity": {Type: "string", Description: "Email (or username when configured)."},
							"email":    {Type: "string", Format: "email", Description: "Alias of `identity` for PB-compat."},
							"password": {Type: "string"},
						},
					}},
				},
			},
			Responses: map[string]Response{
				"200": authResponseOK(),
				"400": errorRef("Validation error."),
				"401": errorRef("Wrong password or no such user (response is uniform — timing-safe)."),
				"429": errorRef("Account temporarily locked due to repeated failures."),
			},
		},
	})

	paths.Set(base+"/auth-refresh", &PathItem{
		Post: &Operation{
			OperationID: "refresh" + name,
			Tags:        []string{tag},
			Summary:     "Rotate session token",
			Responses: map[string]Response{
				"200": authResponseOK(),
				"401": errorRef("Token missing or revoked."),
			},
		},
	})

	paths.Set(base+"/auth-logout", &PathItem{
		Post: &Operation{
			OperationID: "logout" + name,
			Tags:        []string{tag},
			Summary:     "Revoke current session",
			Responses: map[string]Response{
				"204": {Description: "Logged out."},
				"401": errorRef("Not authenticated."),
			},
		},
	})

	paths.Set(base+"/auth-methods", &PathItem{
		Get: &Operation{
			OperationID: "authMethods" + name,
			Tags:        []string{tag},
			Summary:     "Discover configured authentication methods",
			Description: "Public — no auth required. Front-ends call this BEFORE signin to render the login screen.",
			Responses: map[string]Response{
				"200": {
					Description: "Discovery payload.",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{Ref: "#/components/schemas/AuthMethods"}},
					},
				},
				"404": errorRef("Collection not found or not an auth-collection."),
			},
		},
	})
}

// emitSystemPaths registers the cross-cutting endpoints that don't
// belong to a single collection.
func emitSystemPaths(paths *Paths) {
	paths.Set("/api/auth/me", &PathItem{
		Get: &Operation{
			OperationID: "getMe",
			Tags:        []string{"system"},
			Summary:     "Get the currently-authenticated record",
			Responses: map[string]Response{
				"200": {
					Description: "Authenticated record.",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{
							Type:     "object",
							Required: []string{"record"},
							Properties: map[string]*Schema{
								"record": {Type: "object", AdditionalProperties: true},
							},
						}},
					},
				},
				"401": errorRef("Not authenticated."),
			},
		},
	})

	paths.Set("/healthz", &PathItem{
		Get: &Operation{
			OperationID: "healthz",
			Tags:        []string{"system"},
			Summary:     "Liveness probe",
			Description: "Always 200 if the process is up. Use as Kubernetes liveness; doesn't check Postgres.",
			Responses: map[string]Response{
				"200": {Description: "Process is up."},
			},
		},
	})

	paths.Set("/readyz", &PathItem{
		Get: &Operation{
			OperationID: "readyz",
			Tags:        []string{"system"},
			Summary:     "Readiness probe",
			Description: "200 when the server is ready to accept traffic (Postgres reachable, migrations applied).",
			Responses: map[string]Response{
				"200": {Description: "Ready."},
				"503": errorRef("Not ready (DB unreachable, migrations pending, etc.)."),
			},
		},
	})
}

// --- shared parameter + response helpers ---

func listQueryParams() []Parameter {
	return []Parameter{
		{Name: "page", In: "query", Schema: &Schema{Type: "integer", Minimum: f64(1)}, Description: "1-indexed page."},
		{Name: "perPage", In: "query", Schema: &Schema{Type: "integer", Minimum: f64(1), Maximum: f64(500)}, Description: "Rows per page (max 500)."},
		{Name: "filter", In: "query", Schema: &Schema{Type: "string"}, Description: "PB-style filter expression (e.g. `status='published' && created>'2024-01-01'`)."},
		{Name: "sort", In: "query", Schema: &Schema{Type: "string"}, Description: "Comma-separated `±field` (default `-created,-id`)."},
	}
}

func softDeleteParam(softDelete bool) []Parameter {
	if !softDelete {
		return nil
	}
	return []Parameter{{
		Name: "includeDeleted", In: "query", Schema: &Schema{Type: "boolean"},
		Description: "Expose tombstoned rows. Default false.",
	}}
}

func idPathParam() Parameter {
	return Parameter{
		Name: "id", In: "path", Required: true,
		Schema:      &Schema{Type: "string", Format: "uuid"},
		Description: "Row id (UUIDv7).",
	}
}

func errorRef(desc string) Response {
	return Response{
		Description: desc,
		Content: map[string]MediaType{
			"application/json": {Schema: &Schema{Ref: "#/components/schemas/ErrorEnvelope"}},
		},
	}
}

func authResponseOK() Response {
	return Response{
		Description: "Session issued.",
		Content: map[string]MediaType{
			"application/json": {Schema: &Schema{Ref: "#/components/schemas/AuthResponse"}},
		},
	}
}

func requestBodyRef(schema string) *RequestBody {
	return &RequestBody{
		Required: true,
		Content: map[string]MediaType{
			"application/json": {Schema: &Schema{Ref: "#/components/schemas/" + schema}},
		},
	}
}

// requestBodyFor is the collection-aware variant of requestBodyRef.
// Always emits the `application/json` variant; when the collection has
// any File/Files-typed field, also emits a `multipart/form-data`
// variant pointing at the `<schema>Multipart` companion schema (which
// the schema builder defines so the field walks live in one place).
//
// Clients that don't need to upload binary blobs keep posting JSON;
// only callers attaching real files need the multipart path.
func requestBodyFor(spec builder.CollectionSpec, schema string) *RequestBody {
	rb := requestBodyRef(schema)
	if hasFileField(spec) {
		rb.Content["multipart/form-data"] = MediaType{
			Schema: &Schema{Ref: "#/components/schemas/" + schema + "Multipart"},
		}
	}
	return rb
}

func intp(v int) *int { return &v }
