package ts

import (
	"fmt"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// EmitCollection produces one collections/<name>.ts per collection.
// The wrapper is a factory `<name>Collection(http)` returning an
// object with list/get/create/update/delete bound to the right URL
// path and types.
//
// Auth collections still get a CRUD wrapper, but the `create` and
// `update` methods route through /auth-signup and the regular CRUD
// PATCH respectively — the server's RBAC blocks generic /records POST
// on auth collections (per v0.3.2).
//
// Why one file per collection (vs one big file): readable in code
// review, tree-shakes cleanly, and the import graph in
// index.ts stays linear.
func EmitCollection(spec builder.CollectionSpec) string {
	var b strings.Builder
	b.WriteString(header)
	tName := typeName(spec.Name)

	fmt.Fprintf(&b, `// collections/%s.ts — typed CRUD for the "%s" collection.

import type { HTTPClient } from "../index.js";
import type { %s, ListResponse } from "../types.js";

/** Query options for list(). */
export interface %sListOptions {
  page?: number;
  perPage?: number;
  /** PB-style filter expression (parsed server-side). */
  filter?: string;
  /** Comma-separated signed field list, e.g. "-created,name". */
  sort?: string;
}

/** Input shape accepted by create()/update() — system fields stripped. */
export type %sInput = Partial<Omit<%s, "id" | "created" | "updated">>;

`, spec.Name, spec.Name, tName, tName, tName, tName)

	fmt.Fprintf(&b, "/** CRUD wrapper for the `%s` collection. */\n", spec.Name)
	fmt.Fprintf(&b, "export function %sCollection(http: HTTPClient) {\n", lowerFirst(tName))
	b.WriteString("  return {\n")

	fmt.Fprintf(&b, `    /** GET /api/collections/%s/records */
    list(opts: %sListOptions = {}): Promise<ListResponse<%s>> {
      const q = new URLSearchParams();
      if (opts.page != null) q.set("page", String(opts.page));
      if (opts.perPage != null) q.set("perPage", String(opts.perPage));
      if (opts.filter) q.set("filter", opts.filter);
      if (opts.sort) q.set("sort", opts.sort);
      const qs = q.toString();
      return http.request("GET", "/api/collections/%s/records" + (qs ? "?" + qs : ""));
    },

    /** GET /api/collections/%s/records/{id} */
    get(id: string): Promise<%s> {
      return http.request("GET", "/api/collections/%s/records/" + encodeURIComponent(id));
    },
`,
		spec.Name, tName, tName, spec.Name, // list
		spec.Name, tName, spec.Name, // get
	)

	if !spec.Auth {
		fmt.Fprintf(&b, `
    /** POST /api/collections/%s/records */
    create(input: %sInput): Promise<%s> {
      return http.request("POST", "/api/collections/%s/records", { body: input });
    },
`,
			spec.Name, tName, tName, spec.Name)
	} else {
		fmt.Fprintf(&b, `
    /** Auth collections do not accept generic POST. Use ` + "`%sAuth(http).signup(...)`" + ` instead. */
`, lowerFirst(tName))
	}

	fmt.Fprintf(&b, `
    /** PATCH /api/collections/%s/records/{id} */
    update(id: string, input: %sInput): Promise<%s> {
      return http.request("PATCH", "/api/collections/%s/records/" + encodeURIComponent(id), { body: input });
    },

    /** DELETE /api/collections/%s/records/{id} */
    delete(id: string): Promise<void> {
      return http.request("DELETE", "/api/collections/%s/records/" + encodeURIComponent(id));
    },
`,
		spec.Name, tName, tName, spec.Name, // update
		spec.Name, spec.Name, // delete
	)

	b.WriteString("  };\n")
	b.WriteString("}\n")
	return b.String()
}
