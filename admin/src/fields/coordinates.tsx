import { useEffect, useState } from "react";

// Coordinates — geographic lat/lng pair. Wire shape is a JSONB object
// `{lat: number, lng: number}`; degrees, signed (positive = N/E,
// negative = S/W). The cell renders the compass-suffix form
// ("40.7128°N, 74.0060°W") rounded to 4 decimals (~11m at the
// equator — enough for any admin-grade glance). The input is two
// side-by-side <input type="number"> boxes; we keep an internal
// string-draft per side so the operator can clear / retype without
// the field collapsing to NaN, and only commit when both values
// parse as numbers inside their respective bounds (lat ±90, lng
// ±180).

type Coord = { lat: number; lng: number };

function coerce(value: unknown): Coord | null {
  if (value == null || typeof value !== "object") return null;
  const o = value as { lat?: unknown; lng?: unknown };
  if (typeof o.lat !== "number" || typeof o.lng !== "number") return null;
  if (!Number.isFinite(o.lat) || !Number.isFinite(o.lng)) return null;
  return { lat: o.lat, lng: o.lng };
}

function fmtLat(lat: number): string {
  const suffix = lat >= 0 ? "N" : "S";
  return `${Math.abs(lat).toFixed(4)}°${suffix}`;
}

function fmtLng(lng: number): string {
  const suffix = lng >= 0 ? "E" : "W";
  return `${Math.abs(lng).toFixed(4)}°${suffix}`;
}

export function CoordinatesCell({ value }: { value: unknown }) {
  const c = coerce(value);
  if (!c) return null;
  return (
    <span className="rb-mono text-xs whitespace-nowrap">
      {fmtLat(c.lat)}, {fmtLng(c.lng)}
    </span>
  );
}

export function CoordinatesInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = coerce(value);
  const [lat, setLat] = useState(initial ? String(initial.lat) : "");
  const [lng, setLng] = useState(initial ? String(initial.lng) : "");
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const c = coerce(value);
    setLat(c ? String(c.lat) : "");
    setLng(c ? String(c.lng) : "");
  }, [value]);

  const validateAndCommit = () => {
    if (lat === "" && lng === "") {
      setErr(null);
      onChange(null);
      return;
    }
    const fLat = parseFloat(lat);
    const fLng = parseFloat(lng);
    if (!Number.isFinite(fLat) || !Number.isFinite(fLng)) {
      setErr("lat and lng must both be numbers");
      return;
    }
    if (fLat < -90 || fLat > 90) {
      setErr("lat must be between −90 and 90");
      return;
    }
    if (fLng < -180 || fLng > 180) {
      setErr("lng must be between −180 and 180");
      return;
    }
    setErr(null);
    onChange({ lat: fLat, lng: fLng });
  };

  const inputCls =
    "mt-1 w-full rounded border px-2 py-1.5 text-sm rb-mono focus:outline-none focus:ring-1 " +
    (err
      ? "border-red-400 focus:ring-red-500"
      : "border-neutral-300 focus:ring-neutral-900");

  return (
    <div>
      <div className="flex gap-2">
        <label className="flex-1">
          <span className="text-xs text-neutral-500">lat</span>
          <input
            type="number"
            value={lat}
            step="any"
            min={-90}
            max={90}
            onChange={(e) => {
              setLat(e.currentTarget.value);
              setErr(null);
            }}
            onBlur={validateAndCommit}
            placeholder="40.7128"
            className={inputCls}
          />
        </label>
        <label className="flex-1">
          <span className="text-xs text-neutral-500">lng</span>
          <input
            type="number"
            value={lng}
            step="any"
            min={-180}
            max={180}
            onChange={(e) => {
              setLng(e.currentTarget.value);
              setErr(null);
            }}
            onBlur={validateAndCommit}
            placeholder="−74.0060"
            className={inputCls}
          />
        </label>
      </div>
      {err ? <p className="mt-0.5 text-xs text-red-600">{err}</p> : null}
    </div>
  );
}
