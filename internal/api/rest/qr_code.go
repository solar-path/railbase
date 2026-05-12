package rest

import (
	"fmt"
	"strings"
)

// validQRFormats is the set of recognised payload-format hints. The
// server stores the value verbatim — these hints exist for the
// admin UI / SDK to pick a renderer (vCard parser, URL launcher,
// WiFi-config-card, etc.).
var validQRFormats = map[string]struct{}{
	"raw":   {},
	"url":   {},
	"vcard": {},
	"wifi":  {},
	"epc":   {}, // SEPA payment
}

// normaliseQRCode validates the payload length + (when the field
// declares one) the operator-chosen format. Returns the payload
// verbatim — QR Code payload content is opaque to us.
func normaliseQRCode(in string, format string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", fmt.Errorf("qr_code payload must not be empty")
	}
	if len(s) > 4096 {
		return "", fmt.Errorf("qr_code payload too long (max 4096 chars), got %d", len(s))
	}
	// Format hint validation: if operator set one, it must be a known value.
	// Empty hint defaults to "raw" and accepts anything.
	if format != "" {
		if _, ok := validQRFormats[format]; !ok {
			return "", fmt.Errorf("qr_code format %q: must be one of raw / url / vcard / wifi / epc", format)
		}
	}
	return s, nil
}
