import { useEffect, useRef, useState } from "react";

// useMetricBuffer — keep a rolling ring buffer of metric samples
// keyed off a polling source.
//
// Why client-side: the v1 backend `/api/_admin/health` endpoint
// returns point-in-time snapshots, not time-series. Until the
// metrics endpoint materializes server-side history, this hook
// provides a "live trend last N polls" window. Cheap, ephemeral
// (lost on page refresh), and good enough for "is the graph
// going up or down right now" UX.
//
// Args:
//   - `source`     — the current sample value (or null/undefined if
//                    not yet available; sample is skipped in that case)
//   - `pollKey`    — a value that changes every poll (typically the
//                    response's `now` field, or `dataUpdatedAt` from
//                    TanStack Query). Used to deduplicate so that
//                    re-renders within the same poll don't double-push.
//   - `capacity`   — max samples to retain. Default 24 (~2 min at 5s poll).
//
// Returns: array of {t, v} samples in insertion order.
//
// Re-render policy: pushes a new tuple onto a useRef-backed ring and
// triggers a setState so consumers re-render. Trimming happens on each
// push so the returned array never exceeds capacity.

export type Sample = { t: number; v: number };

export function useMetricBuffer(
  source: number | null | undefined,
  pollKey: unknown,
  capacity: number = 24,
): Sample[] {
  const ref = useRef<Sample[]>([]);
  const lastKeyRef = useRef<unknown>(undefined);
  const [samples, setSamples] = useState<Sample[]>([]);

  useEffect(() => {
    if (source == null || !Number.isFinite(source)) return;
    if (lastKeyRef.current === pollKey) return;
    lastKeyRef.current = pollKey;
    const next: Sample = { t: Date.now(), v: source };
    const buf = ref.current.concat(next);
    if (buf.length > capacity) buf.splice(0, buf.length - capacity);
    ref.current = buf;
    setSamples(buf);
  }, [source, pollKey, capacity]);

  return samples;
}

// useMetricRate — derivative of an absolute, monotonic counter.
//
// The /api/_admin/metrics endpoint returns counters as cumulative
// uint64s (e.g. http.requests_total = 12345). For a "requests per
// second" sparkline the chart needs the DELTA between consecutive
// polls divided by the elapsed wall time. This hook does that
// conversion: each new (source, pollKey) tuple computes
//   rate = max(0, (latest - previous) / dtSeconds)
// and pushes {t, v=rate} onto a ring buffer of the last `capacity`
// rates. Returns `{ rate, samples }` so consumers can render the
// current rate as a headline value AND the rolling series as a chart
// data prop.
//
// Edge cases handled:
//   - First sample: no previous → no rate emitted; returns rate=null
//     and an empty samples array. Caller renders "warming up…".
//   - Counter reset (e.g. process restart): if latest < previous, we
//     emit 0 instead of a negative spike. The next poll picks up the
//     new monotonic series cleanly.
//   - dt <= 0 (pollKey changed but wall clock didn't advance):
//     skipped — guards against division blowups.
//
// `unit` is the multiplier applied to the per-second rate before
// storing. Pass 1 for /sec, 60 for /min — the hook keeps the math
// at one site instead of letting every caller bake the conversion in.

export function useMetricRate(
  source: number | null | undefined,
  pollKey: unknown,
  capacity: number = 24,
  unit: number = 1,
): { rate: number | null; samples: Sample[] } {
  const samplesRef = useRef<Sample[]>([]);
  const prevRef = useRef<{ t: number; v: number } | null>(null);
  const lastKeyRef = useRef<unknown>(undefined);
  const [state, setState] = useState<{ rate: number | null; samples: Sample[] }>(
    { rate: null, samples: [] },
  );

  useEffect(() => {
    if (source == null || !Number.isFinite(source)) return;
    if (lastKeyRef.current === pollKey) return;
    lastKeyRef.current = pollKey;

    const now = Date.now();
    const prev = prevRef.current;
    prevRef.current = { t: now, v: source };

    if (prev == null) {
      // First sample — record but don't emit a rate.
      return;
    }
    const dtSec = (now - prev.t) / 1000;
    if (dtSec <= 0) return;
    let delta = source - prev.v;
    if (delta < 0) delta = 0; // counter reset / wraparound
    const rate = (delta / dtSec) * unit;

    const next: Sample = { t: now, v: rate };
    const buf = samplesRef.current.concat(next);
    if (buf.length > capacity) buf.splice(0, buf.length - capacity);
    samplesRef.current = buf;
    setState({ rate, samples: buf });
  }, [source, pollKey, capacity, unit]);

  return state;
}
