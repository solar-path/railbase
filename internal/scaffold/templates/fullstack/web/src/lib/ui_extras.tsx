// Additional UI primitives layered on top of the auth-starter set.
// Keep this file ALONGSIDE ui.tsx (not replacing it) — the original
// 5 primitives (Button/Input/Label/Card/Section) are unchanged; we
// just round the surface out to 10 with the bits the fullstack pages
// use repeatedly.
//
// As with ui.tsx — swap any of these for a richer library (shadcn,
// radix, headlessui) when your design system is decided. They exist
// to make the scaffolded fullstack template look intentional out of
// the box.

import type { ComponentChildren } from "preact";
import { Link, useLocation } from "wouter-preact";

// ---- Badge -------------------------------------------------------------
// Small inline pill, used for role chips + status labels.

export function Badge({
  children,
  variant = "default",
}: {
  children: ComponentChildren;
  variant?: "default" | "success" | "warning" | "danger";
}) {
  const variants = {
    default: "bg-slate-100 text-slate-800",
    success: "bg-green-100 text-green-800",
    warning: "bg-amber-100 text-amber-800",
    danger: "bg-red-100 text-red-800",
  } as const;
  return (
    <span class={`inline-flex items-center rounded px-2 py-0.5 text-xs ${variants[variant]}`}>
      {children}
    </span>
  );
}

// ---- Alert -------------------------------------------------------------
// Inline banner. Used for form errors, "no items" callouts, etc.

export function Alert({
  children,
  variant = "info",
}: {
  children: ComponentChildren;
  variant?: "info" | "success" | "warning" | "danger";
}) {
  const variants = {
    info: "bg-slate-50 text-slate-700 border-slate-200",
    success: "bg-green-50 text-green-800 border-green-200",
    warning: "bg-amber-50 text-amber-800 border-amber-200",
    danger: "bg-red-50 text-red-800 border-red-200",
  } as const;
  return (
    <div class={`rounded border px-3 py-2 text-sm ${variants[variant]}`}>
      {children}
    </div>
  );
}

// ---- Spinner -----------------------------------------------------------
// CSS-only loading spinner. No external font / icon dependency.

export function Spinner({ size = 16 }: { size?: number }) {
  return (
    <span
      class="inline-block animate-spin rounded-full border-2 border-slate-300 border-t-slate-700"
      style={`width:${size}px;height:${size}px`}
      aria-label="Loading"
    />
  );
}

// ---- EmptyState --------------------------------------------------------
// Dashed-border placeholder for empty lists. Title + body + optional CTA.

export function EmptyState({
  title,
  body,
  cta,
}: {
  title: string;
  body?: ComponentChildren;
  cta?: ComponentChildren;
}) {
  return (
    <div class="rounded border border-dashed border-slate-300 px-4 py-10 text-center">
      <h3 class="text-sm font-medium text-slate-900">{title}</h3>
      {body ? <p class="mt-1 text-sm text-slate-500">{body}</p> : null}
      {cta ? <div class="mt-4">{cta}</div> : null}
    </div>
  );
}

// ---- Tabs --------------------------------------------------------------
// Underline-style tabs. Active tab is picked by current pathname
// matching the tab's href. Used by tenant_settings.tsx.

export type Tab = { href: string; label: string };
export function Tabs({ tabs }: { tabs: Tab[] }) {
  const [loc] = useLocation();
  return (
    <nav class="flex gap-1 border-b border-slate-200">
      {tabs.map((t) => {
        const active = loc === t.href || loc.startsWith(t.href + "/");
        return (
          <Link
            key={t.href}
            href={t.href}
            class={`-mb-px border-b-2 px-3 py-2 text-sm ${
              active ? "border-slate-900 font-medium" : "border-transparent text-slate-500 hover:text-slate-900"
            }`}
          >
            {t.label}
          </Link>
        );
      })}
    </nav>
  );
}

// ---- Table -------------------------------------------------------------
// Just enough table wrapping to get consistent header styling without
// repeating Tailwind classes everywhere. Pass <Table.Header> /
// <Table.Body> / <Table.Row> / <Table.Cell> + <Table.HeaderCell>.

export const Table = {
  Root: (props: { children: ComponentChildren; class?: string }) => (
    <table class={`w-full text-sm ${props.class ?? ""}`}>{props.children}</table>
  ),
  Header: (props: { children: ComponentChildren }) => (
    <thead><tr class="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500">{props.children}</tr></thead>
  ),
  Body: (props: { children: ComponentChildren }) => <tbody>{props.children}</tbody>,
  Row: (props: { children: ComponentChildren }) => (
    <tr class="border-b border-slate-100">{props.children}</tr>
  ),
  HeaderCell: (props: { children: ComponentChildren }) => <th class="py-2 font-medium">{props.children}</th>,
  Cell: (props: { children: ComponentChildren; mono?: boolean }) => (
    <td class={`py-2 ${props.mono ? "font-mono text-xs" : ""}`}>{props.children}</td>
  ),
};
