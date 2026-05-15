package ts

import (
	"strings"
	"testing"
)

func TestEmitContact_Surface(t *testing.T) {
	out := EmitContact()
	for _, want := range []string{
		"export interface ContactSubmission {",
		"name: string;",
		"email: string;",
		"message: string;",
		"company?: string;",
		"phone?: string;",
		"subject?: string;",
		"website?: string;",
		"export function contactClient(http: HTTPClient)",
		"submit(input: ContactSubmission): Promise<void>",
		`"POST", "/api/contact"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("contact.ts missing %q\n---\n%s", want, out)
		}
	}
}

func TestEmitIndex_WiresContact(t *testing.T) {
	out := EmitIndex(nil)
	for _, want := range []string{
		`import { contactClient } from "./contact.js";`,
		"contact: contactClient(http),",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("index.ts missing %q (contact wiring)\n---\n%s", want, out)
		}
	}
}
