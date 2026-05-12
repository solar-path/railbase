package rest

import (
	"fmt"
	"strings"
)

// iso3166Alpha2 is the embedded ISO 3166-1 alpha-2 country code list
// (2024 publication). Encoded as a single string for compactness —
// memberCheck is a slice over it, indexed by code. We keep the list
// here and not in `internal/schema/builder` so adding/retiring codes
// doesn't require a schema rebuild.
//
// Source: ISO 3166-1 official register, alpha-2 column.
// Includes user-assigned (XK Kosovo) commonly accepted in practice.
var iso3166Alpha2 = map[string]struct{}{}

func init() {
	codes := strings.Fields(`
AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ
BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ
CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ
DE DJ DK DM DO DZ
EC EE EG EH ER ES ET
FI FJ FK FM FO FR
GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY
HK HM HN HR HT HU
ID IE IL IM IN IO IQ IR IS IT
JE JM JO JP
KE KG KH KI KM KN KP KR KW KY KZ
LA LB LC LI LK LR LS LT LU LV LY
MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ
NA NC NE NF NG NI NL NO NP NR NU NZ
OM
PA PE PF PG PH PK PL PM PN PR PS PT PW PY
QA
RE RO RS RU RW
SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ
TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ
UA UG UM US UY UZ
VA VC VE VG VI VN VU
WF WS
XK
YE YT
ZA ZM ZW
`)
	for _, c := range codes {
		iso3166Alpha2[c] = struct{}{}
	}
}

// normaliseCountry uppercases the input and validates membership.
// Accepts ASCII letters in any case ("us" / "Us" / "US"); rejects
// codes that don't appear in the embedded list.
func normaliseCountry(in string) (string, error) {
	s := strings.TrimSpace(in)
	if len(s) != 2 {
		return "", fmt.Errorf("country code must be 2 letters (ISO 3166-1 alpha-2), got %q", in)
	}
	upper := strings.ToUpper(s)
	for i := 0; i < 2; i++ {
		c := upper[i]
		if c < 'A' || c > 'Z' {
			return "", fmt.Errorf("country code must be ASCII letters, got %q", in)
		}
	}
	if _, ok := iso3166Alpha2[upper]; !ok {
		return "", fmt.Errorf("unknown country code %q (must be ISO 3166-1 alpha-2)", upper)
	}
	return upper, nil
}
