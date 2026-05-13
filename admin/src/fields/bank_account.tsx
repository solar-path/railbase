import { useEffect, useState } from "react";
import type { FieldSpec } from "../api/types";

// Bank account — structured JSONB record whose populated sub-fields
// depend on the jurisdiction. The full shape on the wire is
// `{iban?, bic?, accountNumber?, sortCode?, routingNumber?}`; in
// practice EU rows populate iban (+ optional bic), GB rows tend to
// use accountNumber + sortCode, US rows use accountNumber +
// routingNumber, etc. The FieldSpec may carry a `country` hint (same
// defensive-cast pattern as slice-2 tax_id, since the TS shape lags
// the runtime spec). The cell prefers the IBAN if present (rendered
// in the same 4-char-grouped form as slice-2 iban.tsx) and otherwise
// shows the first non-empty sub-field with a short label so the
// operator knows what they're looking at ("RTN: 021000021"). The
// input shows all five sub-fields stacked; the operator fills the
// ones relevant to the jurisdiction.

type BankAccount = {
  iban?: string;
  bic?: string;
  accountNumber?: string;
  sortCode?: string;
  routingNumber?: string;
};

const FIELD_KEYS: (keyof BankAccount)[] = [
  "iban",
  "bic",
  "accountNumber",
  "sortCode",
  "routingNumber",
];

// Mirror slice-2 iban grouping inline — guardrail forbids touching
// iban.tsx so we duplicate the (3-line) helper rather than promoting
// it to a shared util.
function groupIban(s: string): string {
  const n = s.replace(/[\s-]+/g, "").toUpperCase();
  const out: string[] = [];
  for (let i = 0; i < n.length; i += 4) out.push(n.slice(i, i + 4));
  return out.join(" ");
}

function coerce(value: unknown): BankAccount {
  if (value == null || typeof value !== "object") return {};
  const o = value as Record<string, unknown>;
  const out: BankAccount = {};
  for (const k of FIELD_KEYS) {
    const v = o[k];
    if (typeof v === "string" && v !== "") out[k] = v;
  }
  return out;
}

function strip(b: BankAccount): BankAccount {
  const out: BankAccount = {};
  for (const k of FIELD_KEYS) {
    const v = b[k];
    if (typeof v === "string" && v.trim() !== "") out[k] = v.trim();
  }
  return out;
}

// countryHint — same defensive cast pattern as tax_id.tsx (slice 2).
// The TS FieldSpec union doesn't yet carry `country`; the runtime
// spec does.
function countryHint(field: FieldSpec): string | null {
  const c = (field as unknown as { country?: unknown }).country;
  if (typeof c === "string" && /^[A-Z]{2}$/.test(c.toUpperCase())) {
    return c.toUpperCase();
  }
  return null;
}

const LABEL: Record<keyof BankAccount, string> = {
  iban: "IBAN",
  bic: "BIC",
  accountNumber: "A/C",
  sortCode: "Sort",
  routingNumber: "RTN",
};

export function BankAccountCell({ value }: { value: unknown }) {
  const b = coerce(value);
  if (b.iban) {
    return (
      <span className="font-mono text-xs whitespace-nowrap" title={b.iban}>
        {groupIban(b.iban)}
      </span>
    );
  }
  for (const k of FIELD_KEYS) {
    const v = b[k];
    if (typeof v === "string" && v !== "") {
      return (
        <span className="font-mono text-xs whitespace-nowrap">
          <span className="text-muted-foreground">{LABEL[k]}:</span> {v}
        </span>
      );
    }
  }
  return null;
}

export function BankAccountInput({
  field,
  value,
  onChange,
}: {
  field: FieldSpec;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const [draft, setDraft] = useState<BankAccount>(coerce(value));
  const hint = countryHint(field);

  useEffect(() => {
    setDraft(coerce(value));
  }, [value]);

  const onField = (k: keyof BankAccount, v: string) => {
    setDraft({ ...draft, [k]: v });
  };

  const onBlurCommit = () => {
    const stripped = strip(draft);
    onChange(Object.keys(stripped).length === 0 ? null : stripped);
  };

  const inputCls =
    "mt-1 w-full rounded border border-input px-2 py-1.5 text-sm font-mono uppercase focus:outline-none focus:ring-1 focus:ring-ring";

  return (
    <div className="space-y-2">
      {hint ? (
        <p className="text-xs text-muted-foreground">
          country hint: <span className="font-mono">{hint}</span>
        </p>
      ) : null}
      <label className="block">
        <span className="text-xs text-muted-foreground">IBAN</span>
        <input
          type="text"
          value={draft.iban ?? ""}
          onChange={(e) => onField("iban", e.currentTarget.value)}
          onBlur={onBlurCommit}
          spellcheck={false}
          autoCorrect="off"
          placeholder="DE89370400440532013000"
          className={inputCls}
        />
      </label>
      <label className="block">
        <span className="text-xs text-muted-foreground">BIC / SWIFT</span>
        <input
          type="text"
          value={draft.bic ?? ""}
          onChange={(e) => onField("bic", e.currentTarget.value)}
          onBlur={onBlurCommit}
          spellcheck={false}
          autoCorrect="off"
          placeholder="DEUTDEFF"
          className={inputCls}
        />
      </label>
      <label className="block">
        <span className="text-xs text-muted-foreground">account number</span>
        <input
          type="text"
          value={draft.accountNumber ?? ""}
          onChange={(e) => onField("accountNumber", e.currentTarget.value)}
          onBlur={onBlurCommit}
          spellcheck={false}
          autoCorrect="off"
          placeholder="12345678"
          className={inputCls}
        />
      </label>
      <div className="flex gap-2">
        <label className="flex-1">
          <span className="text-xs text-muted-foreground">sort code (GB)</span>
          <input
            type="text"
            value={draft.sortCode ?? ""}
            onChange={(e) => onField("sortCode", e.currentTarget.value)}
            onBlur={onBlurCommit}
            spellcheck={false}
            autoCorrect="off"
            placeholder="20-00-00"
            className={inputCls}
          />
        </label>
        <label className="flex-1">
          <span className="text-xs text-muted-foreground">routing # (US)</span>
          <input
            type="text"
            value={draft.routingNumber ?? ""}
            onChange={(e) => onField("routingNumber", e.currentTarget.value)}
            onBlur={onBlurCommit}
            spellcheck={false}
            autoCorrect="off"
            placeholder="021000021"
            className={inputCls}
          />
        </label>
      </div>
    </div>
  );
}
