package rest

import (
	"fmt"
	"strings"
)

// normaliseBarcode validates and canonicalises a barcode value
// according to the format hint. Empty hint → auto-detect by length.
//
// Canonical form for digit-only formats is just the digit string
// (no separators). Check-digit verification uses the GS1 algorithm
// (sum of weighted digits ≡ 0 mod 10) for EAN-8 / UPC-A / EAN-13.
func normaliseBarcode(in string, format string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", fmt.Errorf("barcode must not be empty")
	}
	// For digit-only formats, strip operator-friendly separators.
	// Code-128 KEEPS punctuation since the encoding includes it.
	if format != "code128" {
		s = strings.NewReplacer(" ", "", "-", "").Replace(s)
	}
	switch format {
	case "code128":
		if len(s) < 1 || len(s) > 80 {
			return "", fmt.Errorf("code128 barcode length out of range (1-80), got %d", len(s))
		}
		for _, r := range s {
			if r < 32 || r > 126 {
				return "", fmt.Errorf("code128 barcode contains non-printable ASCII at rune %d", r)
			}
		}
		return s, nil
	case "ean8":
		if !allDigits(s) || len(s) != 8 {
			return "", fmt.Errorf("ean8 barcode must be 8 digits, got %q", in)
		}
		if !gs1CheckDigit(s) {
			return "", fmt.Errorf("ean8 barcode %q: check digit fails GS1 mod-10", s)
		}
		return s, nil
	case "upca":
		if !allDigits(s) || len(s) != 12 {
			return "", fmt.Errorf("upca barcode must be 12 digits, got %q", in)
		}
		if !gs1CheckDigit(s) {
			return "", fmt.Errorf("upca barcode %q: check digit fails GS1 mod-10", s)
		}
		return s, nil
	case "ean13":
		if !allDigits(s) || len(s) != 13 {
			return "", fmt.Errorf("ean13 barcode must be 13 digits, got %q", in)
		}
		if !gs1CheckDigit(s) {
			return "", fmt.Errorf("ean13 barcode %q: check digit fails GS1 mod-10", s)
		}
		return s, nil
	case "":
		// Auto-detect by length.
		if !allDigits(s) {
			return "", fmt.Errorf("barcode %q must be digit-only for auto-detect (use .Format(\"code128\") for alphanumeric)", in)
		}
		switch len(s) {
		case 8, 12, 13:
			if !gs1CheckDigit(s) {
				return "", fmt.Errorf("barcode %q: check digit fails GS1 mod-10", s)
			}
			return s, nil
		default:
			return "", fmt.Errorf("barcode %q: length %d does not match EAN-8 / UPC-A / EAN-13", s, len(s))
		}
	default:
		return "", fmt.Errorf("barcode format %q: must be one of ean8 / upca / ean13 / code128 / empty", format)
	}
}

// allDigits reports whether every byte is an ASCII digit.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// gs1CheckDigit verifies the GS1 check digit on the last position.
// Algorithm: weight each digit from the right (excluding the check
// digit) alternately by 3 and 1; sum; the check digit is whatever
// brings the total to a multiple of 10. Works for EAN-8 / UPC-A / EAN-13.
func gs1CheckDigit(s string) bool {
	if !allDigits(s) {
		return false
	}
	sum := 0
	// Iterate from right to left over all positions EXCEPT the check
	// digit (last char). Position counter starts at 1 for the digit
	// immediately left of the check digit.
	body := s[:len(s)-1]
	for i, pos := len(body)-1, 1; i >= 0; i, pos = i-1, pos+1 {
		d := int(body[i] - '0')
		if pos%2 == 1 {
			sum += d * 3
		} else {
			sum += d
		}
	}
	expected := (10 - (sum % 10)) % 10
	got := int(s[len(s)-1] - '0')
	return expected == got
}
