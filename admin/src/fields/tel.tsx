import { useEffect, useState } from "react";

// Tel — E.164 phone number ("+14155552671"). The wire shape is the
// canonical compact form; the cell renders with country-code +
// triplet spacing for readability, the input validates the E.164
// shape on blur. We deliberately do not implement per-country
// formatting rules — that's ~10 KB of libphonenumber data for very
// little real value in an admin UI. The cell formatter splits on
// digit count: 1-3 char CC, then groups of 3-4.
//
// Country-flag rendering: ISO 3166-1 alpha-2 → regional indicator
// surrogate pair. Only the 1- and 7-key country codes shipped here;
// the long tail falls back to no flag (just the formatted number).

const E164_RE = /^\+[1-9]\d{6,14}$/;

// A small mapping of E.164 country dial codes → ISO 3166-1 alpha-2.
// This is the top ~30 most populous CCs — enough to cover the cases
// an admin will see in practice; misses just skip the flag and still
// render the formatted number.
const CC_TO_ISO: Array<[string, string]> = [
  ["1", "US"], // also CA — defaults to US for the flag
  ["7", "RU"],
  ["20", "EG"], ["27", "ZA"], ["30", "GR"], ["31", "NL"], ["32", "BE"],
  ["33", "FR"], ["34", "ES"], ["36", "HU"], ["39", "IT"], ["40", "RO"],
  ["41", "CH"], ["43", "AT"], ["44", "GB"], ["45", "DK"], ["46", "SE"],
  ["47", "NO"], ["48", "PL"], ["49", "DE"],
  ["51", "PE"], ["52", "MX"], ["53", "CU"], ["54", "AR"], ["55", "BR"],
  ["56", "CL"], ["57", "CO"], ["58", "VE"],
  ["60", "MY"], ["61", "AU"], ["62", "ID"], ["63", "PH"], ["64", "NZ"],
  ["65", "SG"], ["66", "TH"],
  ["81", "JP"], ["82", "KR"], ["84", "VN"], ["86", "CN"],
  ["90", "TR"], ["91", "IN"], ["92", "PK"], ["93", "AF"], ["94", "LK"],
  ["95", "MM"], ["98", "IR"],
  ["212", "MA"], ["213", "DZ"], ["216", "TN"], ["218", "LY"],
  ["234", "NG"], ["254", "KE"], ["255", "TZ"], ["256", "UG"],
  ["351", "PT"], ["352", "LU"], ["353", "IE"], ["354", "IS"],
  ["358", "FI"], ["359", "BG"],
  ["370", "LT"], ["371", "LV"], ["372", "EE"], ["375", "BY"],
  ["380", "UA"], ["385", "HR"], ["386", "SI"], ["387", "BA"],
  ["420", "CZ"], ["421", "SK"], ["852", "HK"], ["855", "KH"],
  ["880", "BD"], ["886", "TW"], ["962", "JO"], ["963", "SY"],
  ["964", "IQ"], ["965", "KW"], ["966", "SA"], ["971", "AE"],
  ["972", "IL"], ["973", "BH"], ["974", "QA"], ["977", "NP"],
  ["994", "AZ"], ["995", "GE"], ["996", "KG"], ["998", "UZ"],
];

// Resolve an E.164 string to its ISO 3166-1 alpha-2 country code, or
// null if the prefix doesn't match any of the known CCs. Greedy match
// on the longest prefix wins (so "+1 …" → US, "+12 …" still → US).
function isoForE164(s: string): string | null {
  if (!s.startsWith("+")) return null;
  const digits = s.slice(1);
  // Try 3-digit, then 2-digit, then 1-digit.
  for (const len of [3, 2, 1]) {
    if (digits.length < len) continue;
    const prefix = digits.slice(0, len);
    for (const [cc, iso] of CC_TO_ISO) {
      if (cc === prefix) return iso;
    }
  }
  return null;
}

// Convert ISO 3166-1 alpha-2 to the corresponding regional-indicator
// flag emoji. "US" → 🇺🇸. Two-char ASCII assumed (uppercased).
function flagFor(iso: string | null): string {
  if (!iso || iso.length !== 2) return "";
  const A = 0x1f1e6; // regional indicator A
  const a = "A".charCodeAt(0);
  const codePoints = [...iso.toUpperCase()].map((c) => A + (c.charCodeAt(0) - a));
  return String.fromCodePoint(...codePoints);
}

// Format an E.164 string for display: "+CC XXX XXX XXXX" approx.
// We pick the country code via longest-prefix match on the known set;
// for unknown CCs we treat the first 1-3 digits as the CC heuristically.
function formatE164(s: string): string {
  if (!s.startsWith("+")) return s;
  const digits = s.slice(1);
  // Use the matched ISO to pick the CC length; fall back to 1-digit CC.
  let ccLen = 1;
  for (const len of [3, 2, 1]) {
    if (digits.length < len) continue;
    const prefix = digits.slice(0, len);
    if (CC_TO_ISO.some(([cc]) => cc === prefix)) {
      ccLen = len;
      break;
    }
  }
  const cc = digits.slice(0, ccLen);
  const rest = digits.slice(ccLen);
  // Group the remainder into 3-4 char chunks from the left.
  const groups: string[] = [];
  let i = 0;
  while (i < rest.length) {
    // Use 3-char groups except the trailing one which can run 3-4.
    const take = rest.length - i <= 4 ? rest.length - i : 3;
    groups.push(rest.slice(i, i + take));
    i += take;
  }
  return `+${cc} ${groups.join(" ")}`.trimEnd();
}

export function TelCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const iso = isoForE164(s);
  const flag = flagFor(iso);
  const formatted = formatE164(s);
  return (
    <span className="font-mono text-xs whitespace-nowrap">
      {flag ? <span className="mr-1" aria-label={iso ?? ""}>{flag}</span> : null}
      {formatted}
    </span>
  );
}

export function TelInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value : "";
  const [draft, setDraft] = useState(initial);
  const [err, setErr] = useState<string | null>(null);

  // Keep draft synced to upstream value when the caller resets it
  // (e.g. switching records in the editor).
  useEffect(() => {
    setDraft(typeof value === "string" ? value : "");
  }, [value]);

  const iso = isoForE164(draft);
  const flag = flagFor(iso);

  const validate = (s: string): string | null => {
    if (s === "") return null; // empty is fine — required-ness is the schema's call
    if (!E164_RE.test(s)) return "E.164 shape: +CC followed by 7-15 digits";
    return null;
  };

  return (
    <div>
      <div className="relative">
        <input
          type="tel"
          value={draft}
          onChange={(e) => {
            setDraft(e.currentTarget.value);
            setErr(null);
          }}
          onBlur={() => {
            const e = validate(draft);
            setErr(e);
            if (!e) onChange(draft === "" ? null : draft);
          }}
          placeholder="+14155552671"
          className={
            "mt-1 w-full rounded border px-2 py-1.5 pl-8 text-sm font-mono focus:outline-none focus:ring-1 " +
            (err
              ? "border-destructive/40 focus:ring-destructive"
              : "border-input focus:ring-ring")
          }
        />
        {flag ? (
          <span
            className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-sm"
            aria-hidden="true"
          >
            {flag}
          </span>
        ) : null}
      </div>
      {err ? <p className="mt-0.5 text-xs text-destructive">{err}</p> : null}
    </div>
  );
}
