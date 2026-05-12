import { useEffect, useState } from "react";
import { CountryInput } from "./country";

// Address — structured postal address on the wire as a JSONB object
// `{street?, street2?, city?, region?, postal?, country?}`. Backend
// requires ≥1 field non-empty; `country` is ISO 3166-1 alpha-2 when
// set. The cell renders a single-line, comma-joined summary with a
// 50-char ellipsis to keep list columns sane. The input is a stacked
// 6-field form; on blur we strip empty fields before commit so the
// server never sees `{street: ""}` for cleared values, and commit
// `null` outright when every field is empty (matches the slice-2
// "empty → null" contract).

type Address = {
  street?: string;
  street2?: string;
  city?: string;
  region?: string;
  postal?: string;
  country?: string;
};

const FIELD_KEYS: (keyof Address)[] = [
  "street",
  "street2",
  "city",
  "region",
  "postal",
  "country",
];

function coerce(value: unknown): Address {
  if (value == null || typeof value !== "object") return {};
  const o = value as Record<string, unknown>;
  const out: Address = {};
  for (const k of FIELD_KEYS) {
    const v = o[k];
    if (typeof v === "string" && v !== "") out[k] = v;
  }
  return out;
}

function summary(a: Address): string {
  // Render order: street, street2, city, region, postal, country.
  // Skip blanks; comma-join.
  const parts: string[] = [];
  for (const k of FIELD_KEYS) {
    const v = a[k];
    if (typeof v === "string" && v.trim() !== "") parts.push(v.trim());
  }
  return parts.join(", ");
}

function strip(a: Address): Address {
  const out: Address = {};
  for (const k of FIELD_KEYS) {
    const v = a[k];
    if (typeof v === "string" && v.trim() !== "") out[k] = v.trim();
  }
  return out;
}

export function AddressCell({ value }: { value: unknown }) {
  const a = coerce(value);
  const s = summary(a);
  if (!s) return null;
  const trimmed = s.length > 50 ? s.slice(0, 49) + "…" : s;
  return (
    <span
      className="text-xs whitespace-nowrap text-neutral-800"
      title={s}
    >
      {trimmed}
    </span>
  );
}

export function AddressInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const [draft, setDraft] = useState<Address>(coerce(value));

  useEffect(() => {
    setDraft(coerce(value));
  }, [value]);

  const commit = (next: Address) => {
    const stripped = strip(next);
    onChange(Object.keys(stripped).length === 0 ? null : stripped);
  };

  const onField = (k: keyof Address, v: string) => {
    const next = { ...draft, [k]: v };
    setDraft(next);
  };

  const onBlurCommit = () => commit(draft);

  const txt =
    "mt-1 w-full rounded border border-neutral-300 px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-neutral-900";

  return (
    <div className="space-y-2">
      <label className="block">
        <span className="text-xs text-neutral-500">street</span>
        <input
          type="text"
          value={draft.street ?? ""}
          onChange={(e) => onField("street", e.target.value)}
          onBlur={onBlurCommit}
          placeholder="1600 Amphitheatre Pkwy"
          className={txt}
        />
      </label>
      <label className="block">
        <span className="text-xs text-neutral-500">street 2</span>
        <input
          type="text"
          value={draft.street2 ?? ""}
          onChange={(e) => onField("street2", e.target.value)}
          onBlur={onBlurCommit}
          placeholder="apt / suite"
          className={txt}
        />
      </label>
      <div className="flex gap-2">
        <label className="flex-1">
          <span className="text-xs text-neutral-500">city</span>
          <input
            type="text"
            value={draft.city ?? ""}
            onChange={(e) => onField("city", e.target.value)}
            onBlur={onBlurCommit}
            placeholder="Mountain View"
            className={txt}
          />
        </label>
        <label className="flex-1">
          <span className="text-xs text-neutral-500">region</span>
          <input
            type="text"
            value={draft.region ?? ""}
            onChange={(e) => onField("region", e.target.value)}
            onBlur={onBlurCommit}
            placeholder="CA"
            className={txt}
          />
        </label>
        <label className="flex-1">
          <span className="text-xs text-neutral-500">postal</span>
          <input
            type="text"
            value={draft.postal ?? ""}
            onChange={(e) => onField("postal", e.target.value)}
            onBlur={onBlurCommit}
            placeholder="94043"
            className={txt}
          />
        </label>
      </div>
      <div>
        <span className="text-xs text-neutral-500">country</span>
        <CountryInput
          value={draft.country ?? ""}
          onChange={(v) => {
            const next = {
              ...draft,
              country: typeof v === "string" ? v : "",
            };
            setDraft(next);
            // CountryInput commits on select; mirror its commit here so
            // the parent sees the change without waiting for a blur on
            // a sibling field.
            commit(next);
          }}
        />
      </div>
    </div>
  );
}
