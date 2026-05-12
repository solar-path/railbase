import { useEffect, useState } from "react";

// QrCode — TEXT 1..4096 chars (URL, vCard, WiFi creds, EPC payment
// strings, etc.). The cell shows a 40-char monospace preview with
// ellipsis; the input is a small textarea with a character counter.
//
// We deliberately do NOT render the QR client-side: a QR library is
// another ~20 KB gzip on a 121 KB baseline, and the actual QR
// rendering happens in render contexts that support it (PDF exports,
// mobile cards) where the backend has the value anyway. A server-side
// `GET /api/_admin/qr` endpoint was considered for a popover preview
// but it isn't shipped yet and we don't want this slice to require
// backend work. A hint icon explains where the value is actually used.

const CELL_PREVIEW_LEN = 40;
const MAX_LEN = 4096;

export function QrCodeCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const preview = s.length > CELL_PREVIEW_LEN ? s.slice(0, CELL_PREVIEW_LEN) + "…" : s;
  return (
    <span className="rb-mono text-xs whitespace-nowrap text-neutral-700" title={s}>
      {preview}
    </span>
  );
}

export function QrCodeInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value : "";
  const [draft, setDraft] = useState(initial);

  useEffect(() => {
    setDraft(typeof value === "string" ? value : "");
  }, [value]);

  const over = draft.length > MAX_LEN;

  return (
    <div className="mt-1">
      <textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => onChange(draft === "" ? null : draft)}
        rows={4}
        spellCheck={false}
        className={
          "w-full rounded border px-2 py-1.5 text-sm rb-mono focus:outline-none focus:ring-1 " +
          (over
            ? "border-red-400 focus:ring-red-500"
            : "border-neutral-300 focus:ring-neutral-900")
        }
      />
      <div className="mt-0.5 flex items-center justify-between text-xs">
        <span
          className="cursor-help text-neutral-400"
          title="Use 'qr_code' field type — value is encoded as QR on render contexts that support it (PDF exports, mobile cards)"
        >
          ⓘ
        </span>
        <span className={over ? "text-red-600" : "text-neutral-500"}>
          {draft.length}/{MAX_LEN}
        </span>
      </div>
    </div>
  );
}
