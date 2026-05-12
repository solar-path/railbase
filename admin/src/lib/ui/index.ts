// ─────────────────────────────────────────────────────────────────────
// Railbase UI kit — SHARED SOURCE OF TRUTH
// ─────────────────────────────────────────────────────────────────────
//
// Every file under `admin/src/lib/ui/` is part of the shareable
// shadcn-on-Preact kit that the Railbase binary serves to downstream
// frontend apps via:
//
//   GET /api/_ui/*                    — over HTTP (no auth)
//   railbase ui list / add / init     — CLI scaffolder
//
// What that means in practice:
//
//   1. Treat THIS directory as the source of truth. The admin app
//      consumes it via `import { Button } from "@/lib/ui/button.ui"`;
//      downstream apps lift the same files into their own
//      `src/lib/ui/` via `railbase ui add`.
//
//   2. NEVER reach into admin-app-specific dirs from here. Anything
//      under `admin/src/{auth,api,fields,layout,screens}/` is the
//      admin's private application code and CANNOT travel with the
//      kit. If a component needs application state, it doesn't
//      belong here.
//
//   3. App-specific composites (the kind air calls `QEditableForm`)
//      go in `admin/src/screens/` or a `_composites/` subfolder of
//      the screen that owns them — not in this directory.
//
// Layout convention:
//
//   admin/src/lib/ui/
//     ├─ *.ui.tsx          ← components (shipped to consumers)
//     ├─ _primitives/*     ← Radix-replacement primitives
//     ├─ cn.ts             ← cn() = twMerge(clsx(...)) helper
//     ├─ icons.tsx         ← hand-rolled SVG icon set
//     ├─ theme.ts          ← light/dark toggle helpers
//     └─ index.ts          ← THIS file: barrel export
//
//   admin/src/                          ← admin-app-private (do not ship)
//   admin/src/lib/ui/                   ← shared kit (do ship)
//
// Adding a new component? Place the .ui.tsx here, then `npm run
// build` — the Go embed.FS at admin/uikit.go picks it up automatically
// at next `go build`, no manifest update needed.
//
// ─────────────────────────────────────────────────────────────────────

export { cn, type ClassValue } from './cn'
export { theme, setTheme, initTheme, type Theme } from './theme'
export * from './icons'

export * from './accordion.ui'
export * from './alert.ui'
export * from './alert-dialog.ui'
export * from './aspect-ratio.ui'
export * from './avatar.ui'
export * from './badge.ui'
export * from './breadcrumb.ui'
export * from './button.ui'
export * from './calendar.ui'
export * from './card.ui'
export * from './carousel.ui'
export * from './chart.ui'
export * from './checkbox.ui'
export * from './collapsible.ui'
export * from './command.ui'
export * from './context-menu.ui'
export * from './drawer.ui'
export * from './dropdown-menu.ui'
export * from './form.ui'
export * from './hover-card.ui'
export * from './input.ui'
export * from './input-otp.ui'
export * from './item.ui'
export * from './label.ui'
export * from './password.ui'
export * from './phone.ui'
export * from './menubar.ui'
export * from './navigation-menu.ui'
export * from './pagination.ui'
export * from './popover.ui'
export * from './progress.ui'
export * from './radio-group.ui'
export * from './resizable.ui'
export * from './scroll-area.ui'
export * from './select.ui'
export * from './separator.ui'
export * from './sheet.ui'
export * from './sidebar.ui'
export * from './skeleton.ui'
export * from './slider.ui'
export { Toaster, toast, dismissToast } from './sonner.ui'
export type { ToastItem, ToastVariant, ToasterProps } from './sonner.ui'
export * from './switch.ui'
export * from './table.ui'
export * from './tabs.ui'
export * from './textarea.ui'
export * from './toggle.ui'
export * from './toggle-group.ui'
export * from './tooltip.ui'
