package rest

// Phase 3.x — REST CRUD applies field-level `DefaultRequest` defaults
// from the request context. Closes the "owner copied on the client"
// pattern that Sentinel had to live with:
//
//	// tasks.go line 22 (Sentinel)
//	// "The client copies it from the parent project on create"
//
//	// project-screen.tsx line 144 (Sentinel)
//	await rb.tasks.create({
//	    project: projectId,
//	    owner: authState.value.me.id,  // ← client-side injection
//	    ...
//	});
//
// With `Relation("users").DefaultRequest("auth.id")` declared on the
// builder side, REST CRUD now substitutes the value server-side when
// the client omits it. The CreateRule still gates the write, so the
// pattern becomes:
//
//	Field("owner", Relation("users").Required().DefaultRequest("auth.id"))
//	CreateRule("@request.auth.id = owner")
//
// Client now just sends `{project: pid, name: "..."}` — server injects
// owner = caller, CreateRule confirms caller == owner, INSERT lands.
//
// Override semantics: if the client passes `owner: <some-id>`, we do
// NOT overwrite it — the CreateRule decides whether the override is
// legal. This keeps the door open for admin endpoints that legitimately
// want to set a non-self owner.

import (
	"context"

	"github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/tenant"
)

// applyRequestDefaults walks spec.Fields and fills in any missing
// (absent from `fields`) value whose FieldSpec.DefaultRequest names a
// known expression. Mutates `fields` in place. Unknown expressions
// are silently ignored — Validate() already rejected them at boot, so
// reaching this path means we added a new expression without updating
// the resolver; tests should catch the drift before release.
//
// Why "absent from fields" instead of "zero-valued": JSON unmarshal
// distinguishes missing keys from explicit nulls. `parseInput` only
// emits keys the body carried, so `fields["owner"] = nil` means
// "client explicitly sent null" — we should NOT override that.
func applyRequestDefaults(ctx context.Context, spec builder.CollectionSpec, fields map[string]any) {
	for _, f := range spec.Fields {
		if f.DefaultRequest == "" {
			continue
		}
		if _, present := fields[f.Name]; present {
			continue // client supplied something explicit
		}
		v, ok := resolveRequestDefault(ctx, f.DefaultRequest)
		if !ok {
			continue
		}
		fields[f.Name] = v
	}
}

// resolveRequestDefault returns the value bound to one of the
// whitelisted expressions in the current request context. Returns
// (value, true) on hit; (nil, false) when the expression resolves to
// nothing (e.g. tenant.id on a non-tenant request, auth.id on an
// anonymous call). The caller leaves the field untouched on a false
// — the CreateRule then decides whether anonymous create is even
// legal.
func resolveRequestDefault(ctx context.Context, expr string) (any, bool) {
	switch expr {
	case "auth.id":
		p := middleware.PrincipalFrom(ctx)
		if !p.Authenticated() {
			return nil, false
		}
		// Return as string — Postgres UUID columns accept the canonical
		// hyphenated text form, and the rest of REST CRUD operates on
		// JSON-decoded inputs (which would round-trip uuid.UUID through
		// MarshalJSON anyway).
		return p.UserID.String(), true
	case "auth.collection":
		p := middleware.PrincipalFrom(ctx)
		if !p.Authenticated() {
			return nil, false
		}
		return p.CollectionName, true
	case "tenant.id":
		id := tenant.ID(ctx)
		if id == [16]byte{} {
			return nil, false
		}
		// Same TEXT-form as above so a `Relation("tenants")` column
		// accepts it directly.
		var s [36]byte
		hexUUID(id, &s)
		return string(s[:]), true
	case "auth.email":
		// Email isn't on the Principal struct — would require a per-
		// request lookup against the auth collection. Wire-up deferred
		// to a follow-up patch; for now we return (nil, false) so the
		// field stays missing and the CreateRule / NOT NULL constraint
		// surfaces a clear error.
		return nil, false
	}
	return nil, false
}

// hexUUID writes the canonical 8-4-4-4-12 hex form of u into out.
// Local helper to avoid pulling google/uuid here for one formatter.
func hexUUID(u [16]byte, out *[36]byte) {
	const hex = "0123456789abcdef"
	pos := 0
	for i, b := range u {
		switch i {
		case 4, 6, 8, 10:
			out[pos] = '-'
			pos++
		}
		out[pos] = hex[b>>4]
		out[pos+1] = hex[b&0xF]
		pos += 2
	}
}
