package ts

// EmitContact renders contact.ts: typed wrapper for the public
// POST /api/contact endpoint (v0.4.3 Sprint 5).
//
// Schema-independent. The endpoint is anonymous — no auth required —
// so the wrapper is callable from public marketing pages BEFORE
// signin. Rate limit (5/min/IP) is enforced server-side; the SDK
// surfaces 429-equivalent errors as RailbaseAPIError with code
// "rate_limit" which the form can render as "please try again
// shortly".
func EmitContact() string {
	return header + `// contact.ts — typed wrapper for POST /api/contact (public).
//
// The endpoint is public; no token is required. Submissions go to
// the operator's configured contact.recipient address via the
// Railbase mailer. When mailer/recipient isn't configured, the
// server returns 503 — present that as "contact form not yet set
// up" rather than a generic error.

import type { HTTPClient } from "./index.js";

/** The wire shape of a contact-form submission. ` + "`website`" + ` is the
 *  honeypot field — DON'T set it from your form code. Render the
 *  input element hidden + autocomplete="off"; legitimate users
 *  leave it empty and bots fill it. */
export interface ContactSubmission {
  name: string;
  email: string;
  message: string;
  /** Optional. Surfaces in the operator's email. */
  company?: string;
  /** Optional. */
  phone?: string;
  /** Optional. Subject line override; defaults to "Sales inquiry". */
  subject?: string;
  /** Honeypot. Leave empty in legitimate UIs. */
  website?: string;
}

/** Public-form helper. Returns void on success — the server's email
 *  send is fire-and-forget from the caller's perspective.
 *
 *      await rb.contact.submit({
 *        name: "Alice",
 *        email: "alice@example.com",
 *        company: "Acme",
 *        message: "Tell me more about your enterprise plan.",
 *      });
 *
 *  On rate-limit (5/min per IP) the call rejects with
 *  RailbaseAPIError, code="rate_limit". On unconfigured mailer it
 *  rejects with code="unavailable" — UIs should branch on those
 *  rather than showing the generic "Submission failed" string. */
export function contactClient(http: HTTPClient) {
  return {
    submit(input: ContactSubmission): Promise<void> {
      return http.request("POST", "/api/contact", { body: input });
    },
  };
}
`
}
