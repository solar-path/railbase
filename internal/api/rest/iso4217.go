package rest

import (
	"fmt"
	"strings"
)

// iso4217 is the embedded ISO 4217 currency code list (~180 active
// alpha-3 codes as of 2024). Same compactness trick as ISO 3166-1
// and ISO 639-1: one space-separated string + per-init map build.
//
// Includes:
//   - Active circulating fiat (USD, EUR, RUB, ...)
//   - Precious metal codes (XAU gold, XAG silver, XPT platinum, XPD palladium)
//   - Special test/no-currency codes (XTS test, XXX no currency)
//   - Major crypto kept OUT (BTC / ETH are not ISO 4217; operators
//     wanting them add to a non-currency string field or a hook)
var iso4217 = map[string]struct{}{}

func init() {
	codes := strings.Fields(`
AED AFN ALL AMD ANG AOA ARS AUD AWG AZN
BAM BBD BDT BGN BHD BIF BMD BND BOB BOV BRL BSD BTN BWP BYN BZD
CAD CDF CHE CHF CHW CLF CLP CNY COP COU CRC CUC CUP CVE CZK
DJF DKK DOP DZD
EGP ERN ETB EUR
FJD FKP
GBP GEL GHS GIP GMD GNF GTQ GYD
HKD HNL HRK HTG HUF
IDR ILS INR IQD IRR ISK
JMD JOD JPY
KES KGS KHR KMF KPW KRW KWD KYD KZT
LAK LBP LKR LRD LSL LYD
MAD MDL MGA MKD MMK MNT MOP MRU MUR MVR MWK MXN MXV MYR MZN
NAD NGN NIO NOK NPR NZD
OMR
PAB PEN PGK PHP PKR PLN PYG
QAR
RON RSD RUB RWF
SAR SBD SCR SDG SEK SGD SHP SLE SOS SRD SSP STN SVC SYP SZL
THB TJS TMT TND TOP TRY TTD TWD TZS
UAH UGX USD USN UYI UYU UYW UZS
VED VES VND VUV
WST
XAF XAG XAU XBA XBB XBC XBD XCD XDR XOF XPD XPF XPT XSU XTS XUA XXX
YER
ZAR ZMW ZWG
`)
	for _, c := range codes {
		iso4217[c] = struct{}{}
	}
}

// normaliseCurrency uppercases the input and validates ISO 4217
// membership. Accepts ASCII letters in any case.
func normaliseCurrency(in string) (string, error) {
	s := strings.TrimSpace(in)
	if len(s) != 3 {
		return "", fmt.Errorf("currency code must be 3 letters (ISO 4217), got %q", in)
	}
	upper := strings.ToUpper(s)
	for i := 0; i < 3; i++ {
		c := upper[i]
		if c < 'A' || c > 'Z' {
			return "", fmt.Errorf("currency code must be ASCII letters, got %q", in)
		}
	}
	if _, ok := iso4217[upper]; !ok {
		return "", fmt.Errorf("unknown currency code %q (must be ISO 4217)", upper)
	}
	return upper, nil
}
