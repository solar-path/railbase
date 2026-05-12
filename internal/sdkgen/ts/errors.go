package ts

// errorsTS is static — the codes mirror internal/errors. Keeping the
// list out of the header lets future error additions land as a
// single-file change in this package.
//
// Why a discriminated union and not a class hierarchy:
//   - Pattern matching with switch(err.code) is the idiomatic TS way
//     and tree-shakes cleanly.
//   - Subclasses lose their identity through the .json() round-trip
//     the fetch layer performs.
//
// `details` is typed loosely on purpose. v0.7 carries a free-form
// shape; v1 will tighten per-code (e.g. `code: "validation"` carries
// `{ field, rule }[]`).
func errorsTS() string {
	return header + `// errors.ts — discriminated union mirroring internal/errors codes.

export type RailbaseError =
  | { code: "not_found"; message: string }
  | { code: "unauthorized"; message: string }
  | { code: "forbidden"; message: string }
  | { code: "validation"; message: string; details?: unknown }
  | { code: "conflict"; message: string }
  | { code: "rate_limit"; message: string; retryAfter?: number }
  | { code: "internal"; message: string };

/** Thrown by every fetch wrapper on non-2xx response. */
export class RailbaseAPIError extends Error {
  readonly code: RailbaseError["code"];
  readonly status: number;
  readonly body: RailbaseError;

  constructor(status: number, body: RailbaseError) {
    super(body.message);
    this.name = "RailbaseAPIError";
    this.code = body.code;
    this.status = status;
    this.body = body;
  }
}

export function isRailbaseError(err: unknown): err is RailbaseAPIError {
  return err instanceof RailbaseAPIError;
}
`
}
