import { LineChart, Line, ResponsiveContainer, Tooltip, YAxis } from "recharts";

// TrendChart — small inline line chart for "current value + last-N
// samples trend" cards on Health / Dashboard.
//
// Design constraints:
//   - Lives in a Card with ~120px height; no axis labels, no legend.
//   - Color literal hex required by Recharts API; the no-hardcoded-
//     tw-color rule has an explicit pragma escape for this pattern
//     (see admin/eslint-rules/no-hardcoded-tw-color.js).
//   - `data` is a plain array of `{ t, v }` — `t` is a unix ms or
//     monotonic counter (Recharts treats it as opaque order key);
//     `v` is the metric scalar.
//   - Lazy-loaded from screens via `lazy(() => import("./trend_chart"))`
//     so Recharts goes to its own chunk and stays out of the main
//     bundle (docs/12 §Bundle cost summary, Wave 4).
//
// Defaults pick a sensible color from the theme palette per metric
// category. The component intentionally has no Suspense fallback —
// the caller wraps in <Suspense> with its own loading placeholder.

export type TrendPoint = { t: number; v: number };

export type TrendChartProps = {
  data: TrendPoint[];
  /** Optional override; default picks from `intent`. */
  color?: string;
  /** Visual intent — drives the default color. */
  intent?: "neutral" | "primary" | "warn" | "danger" | "info";
  /** Optional formatter for the tooltip value. */
  format?: (v: number) => string;
  /** Compact mode hides the tooltip; useful for tiny sparklines. */
  compact?: boolean;
};

/* recharts: explicit hex required for series colors */
const INTENT_COLORS: Record<NonNullable<TrendChartProps["intent"]>, string> = {
  neutral: "#64748b",  // slate-500 equivalent
  primary: "#0ea5e9",  // sky-500
  warn:    "#f59e0b",  // amber-500
  danger:  "#ef4444",  // red-500
  info:    "#6366f1",  // indigo-500
};

export default function TrendChart({
  data,
  color,
  intent = "neutral",
  format,
  compact = false,
}: TrendChartProps) {
  const resolved = color ?? INTENT_COLORS[intent];
  return (
    <ResponsiveContainer width="100%" height="100%">
      <LineChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
        <YAxis hide domain={["dataMin", "dataMax"]} />
        {compact ? null : (
          <Tooltip
            cursor={false}
            contentStyle={{
              fontSize: 11,
              padding: "4px 8px",
              borderRadius: 6,
              border: "1px solid hsl(var(--border))",
              background: "hsl(var(--popover))",
            }}
            labelStyle={{ display: "none" }}
            formatter={(value: unknown) => {
              const n = typeof value === "number" ? value : Number(value);
              return [format ? format(n) : n, ""];
            }}
          />
        )}
        <Line
          type="monotone"
          dataKey="v"
          stroke={resolved}
          strokeWidth={1.5}
          dot={false}
          isAnimationActive={false}
        />
      </LineChart>
    </ResponsiveContainer>
  );
}
