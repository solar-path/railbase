import { useEffect, useMemo, useState } from "react";

// Country — ISO 3166-1 alpha-2 code. Wire form is uppercase 2 letters.
// The cell renders a small uppercase badge; the input is the same
// searchable flat-select pattern as Currency, but with the alpha-2
// codes. We ship the top-40 most likely countries (an admin's record
// browser overwhelmingly hits a small set in practice); the backend
// validator (249 entries) is authoritative — anything outside this
// curated list still renders verbatim in the cell.

export const COUNTRY_CODES: ReadonlyArray<{ code: string; name: string }> = [
  { code: "US", name: "United States" },
  { code: "CA", name: "Canada" },
  { code: "MX", name: "Mexico" },
  { code: "BR", name: "Brazil" },
  { code: "AR", name: "Argentina" },
  { code: "GB", name: "United Kingdom" },
  { code: "IE", name: "Ireland" },
  { code: "FR", name: "France" },
  { code: "DE", name: "Germany" },
  { code: "ES", name: "Spain" },
  { code: "PT", name: "Portugal" },
  { code: "IT", name: "Italy" },
  { code: "NL", name: "Netherlands" },
  { code: "BE", name: "Belgium" },
  { code: "CH", name: "Switzerland" },
  { code: "AT", name: "Austria" },
  { code: "SE", name: "Sweden" },
  { code: "NO", name: "Norway" },
  { code: "DK", name: "Denmark" },
  { code: "FI", name: "Finland" },
  { code: "PL", name: "Poland" },
  { code: "CZ", name: "Czechia" },
  { code: "HU", name: "Hungary" },
  { code: "RO", name: "Romania" },
  { code: "GR", name: "Greece" },
  { code: "TR", name: "Turkey" },
  { code: "RU", name: "Russia" },
  { code: "UA", name: "Ukraine" },
  { code: "BY", name: "Belarus" },
  { code: "KZ", name: "Kazakhstan" },
  { code: "GE", name: "Georgia" },
  { code: "CN", name: "China" },
  { code: "JP", name: "Japan" },
  { code: "KR", name: "South Korea" },
  { code: "IN", name: "India" },
  { code: "ID", name: "Indonesia" },
  { code: "PH", name: "Philippines" },
  { code: "TH", name: "Thailand" },
  { code: "VN", name: "Vietnam" },
  { code: "SG", name: "Singapore" },
  { code: "AU", name: "Australia" },
  { code: "NZ", name: "New Zealand" },
  { code: "ZA", name: "South Africa" },
  { code: "EG", name: "Egypt" },
  { code: "AE", name: "United Arab Emirates" },
  { code: "SA", name: "Saudi Arabia" },
  { code: "IL", name: "Israel" },
];

export function CountryCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const code = String(value).toUpperCase();
  return (
    <span
      className="inline-block rounded bg-muted text-foreground font-mono text-xs px-1.5 py-0.5 tracking-wider"
      title={COUNTRY_CODES.find((c) => c.code === code)?.name ?? code}
    >
      {code}
    </span>
  );
}

export function CountryInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value.toUpperCase() : "";
  const [selected, setSelected] = useState(initial);
  const [query, setQuery] = useState("");

  useEffect(() => {
    setSelected(typeof value === "string" ? value.toUpperCase() : "");
  }, [value]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return COUNTRY_CODES;
    return COUNTRY_CODES.filter(
      (c) => c.code.toLowerCase().includes(q) || c.name.toLowerCase().includes(q),
    );
  }, [query]);

  return (
    <div className="mt-1 space-y-1">
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.currentTarget.value)}
        placeholder="Search code or name…"
        className="w-full rounded border border-input px-2 py-1 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
      />
      <select
        value={selected}
        onChange={(e) => {
          const v = e.currentTarget.value;
          setSelected(v);
          onChange(v || null);
        }}
        size={6}
        className="w-full rounded border border-input px-2 py-1 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-ring"
      >
        <option value="">— none —</option>
        {filtered.map((c) => (
          <option key={c.code} value={c.code}>
            {c.code} — {c.name}
          </option>
        ))}
      </select>
    </div>
  );
}
