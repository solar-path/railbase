import { useEffect, useMemo, useState } from "react";

// Currency — ISO 4217 alpha-3. The wire form is uppercase 3 letters.
// The cell renders a small neutral-bg badge so the column reads as
// "this is a code, not free text". The input is a searchable flat
// select (the search box filters CURRENCY_CODES on every keystroke,
// no autocomplete dependency); we ship the ~50 most common active
// codes — that covers the long tail well enough for admin use, and
// any unknown value the schema allows still renders verbatim in the
// cell.

// 50 common ISO 4217 codes (alpha-3, name). Curated to over-index on
// what an admin in EMEA/US/Asia-Pacific will see; the validator on
// the backend (~180 codes) is authoritative — this list is just the
// dropdown content.
export const CURRENCY_CODES: ReadonlyArray<{ code: string; name: string }> = [
  { code: "USD", name: "US Dollar" },
  { code: "EUR", name: "Euro" },
  { code: "GBP", name: "British Pound" },
  { code: "JPY", name: "Japanese Yen" },
  { code: "CNY", name: "Chinese Yuan" },
  { code: "CHF", name: "Swiss Franc" },
  { code: "CAD", name: "Canadian Dollar" },
  { code: "AUD", name: "Australian Dollar" },
  { code: "NZD", name: "New Zealand Dollar" },
  { code: "RUB", name: "Russian Ruble" },
  { code: "INR", name: "Indian Rupee" },
  { code: "BRL", name: "Brazilian Real" },
  { code: "MXN", name: "Mexican Peso" },
  { code: "ZAR", name: "South African Rand" },
  { code: "SEK", name: "Swedish Krona" },
  { code: "NOK", name: "Norwegian Krone" },
  { code: "DKK", name: "Danish Krone" },
  { code: "PLN", name: "Polish Zloty" },
  { code: "CZK", name: "Czech Koruna" },
  { code: "HUF", name: "Hungarian Forint" },
  { code: "RON", name: "Romanian Leu" },
  { code: "TRY", name: "Turkish Lira" },
  { code: "UAH", name: "Ukrainian Hryvnia" },
  { code: "BYN", name: "Belarusian Ruble" },
  { code: "KZT", name: "Kazakhstani Tenge" },
  { code: "GEL", name: "Georgian Lari" },
  { code: "AED", name: "UAE Dirham" },
  { code: "SAR", name: "Saudi Riyal" },
  { code: "ILS", name: "Israeli Shekel" },
  { code: "EGP", name: "Egyptian Pound" },
  { code: "QAR", name: "Qatari Riyal" },
  { code: "KWD", name: "Kuwaiti Dinar" },
  { code: "HKD", name: "Hong Kong Dollar" },
  { code: "SGD", name: "Singapore Dollar" },
  { code: "TWD", name: "Taiwan Dollar" },
  { code: "KRW", name: "South Korean Won" },
  { code: "THB", name: "Thai Baht" },
  { code: "MYR", name: "Malaysian Ringgit" },
  { code: "IDR", name: "Indonesian Rupiah" },
  { code: "PHP", name: "Philippine Peso" },
  { code: "VND", name: "Vietnamese Dong" },
  { code: "PKR", name: "Pakistani Rupee" },
  { code: "BDT", name: "Bangladeshi Taka" },
  { code: "ARS", name: "Argentine Peso" },
  { code: "CLP", name: "Chilean Peso" },
  { code: "COP", name: "Colombian Peso" },
  { code: "PEN", name: "Peruvian Sol" },
  { code: "NGN", name: "Nigerian Naira" },
  { code: "KES", name: "Kenyan Shilling" },
  { code: "MAD", name: "Moroccan Dirham" },
];

export function CurrencyCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const code = String(value).toUpperCase();
  return (
    <span
      className="inline-block rounded bg-neutral-100 text-neutral-700 rb-mono text-xs px-1.5 py-0.5 tracking-wider"
      title={CURRENCY_CODES.find((c) => c.code === code)?.name ?? code}
    >
      {code}
    </span>
  );
}

export function CurrencyInput({
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
    if (!q) return CURRENCY_CODES;
    return CURRENCY_CODES.filter(
      (c) => c.code.toLowerCase().includes(q) || c.name.toLowerCase().includes(q),
    );
  }, [query]);

  return (
    <div className="mt-1 space-y-1">
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Search code or name…"
        className="w-full rounded border border-neutral-300 px-2 py-1 text-xs focus:outline-none focus:ring-1 focus:ring-neutral-900"
      />
      <select
        value={selected}
        onChange={(e) => {
          const v = e.target.value;
          setSelected(v);
          onChange(v || null);
        }}
        size={6}
        className="w-full rounded border border-neutral-300 px-2 py-1 text-sm rb-mono focus:outline-none focus:ring-1 focus:ring-neutral-900"
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
