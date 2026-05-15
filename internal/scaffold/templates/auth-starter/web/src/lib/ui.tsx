// Minimal UI primitives. Intentionally tiny — the goal is "works out
// of the box with Tailwind", not "ships a UI kit". Swap for shadcn /
// radix / @railbase/ui-preact / whatever your team prefers; the four
// page components below only depend on these four primitives.

import type { ComponentChildren, JSX } from "preact";

export function Button(
  props: JSX.IntrinsicElements["button"] & { variant?: "primary" | "outline" | "ghost" | "destructive" }
) {
  const { variant = "primary", class: extra, ...rest } = props;
  const base = "inline-flex items-center justify-center rounded-md px-3 py-1.5 text-sm font-medium transition disabled:opacity-50 disabled:cursor-not-allowed";
  const variants = {
    primary: "bg-slate-900 text-white hover:bg-slate-800",
    outline: "border border-slate-300 bg-white text-slate-900 hover:bg-slate-50",
    ghost: "text-slate-700 hover:bg-slate-100",
    destructive: "bg-red-600 text-white hover:bg-red-700",
  } as const;
  return <button {...rest} class={`${base} ${variants[variant]} ${extra ?? ""}`} />;
}

export function Input(props: JSX.IntrinsicElements["input"]) {
  const { class: extra, ...rest } = props;
  return (
    <input
      {...rest}
      class={`w-full rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm focus:border-slate-500 focus:outline-none ${extra ?? ""}`}
    />
  );
}

export function Label({ children, htmlFor }: { children: ComponentChildren; htmlFor?: string }) {
  return (
    <label htmlFor={htmlFor} class="block text-xs font-medium text-slate-600">
      {children}
    </label>
  );
}

export function Card({ children, class: extra }: { children: ComponentChildren; class?: string }) {
  return (
    <div class={`rounded-lg border border-slate-200 bg-white p-4 shadow-sm ${extra ?? ""}`}>
      {children}
    </div>
  );
}

export function Section({ title, children }: { title: string; children: ComponentChildren }) {
  return (
    <Card>
      <h2 class="mb-3 text-base font-semibold text-slate-900">{title}</h2>
      {children}
    </Card>
  );
}
