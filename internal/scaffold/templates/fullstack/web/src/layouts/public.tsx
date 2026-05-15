import { Link } from "wouter-preact";
import type { ComponentChildren } from "preact";

// PublicLayout — marketing-site shell: top nav with brand + 4 links
// + "Sign in" CTA. Keep it ascetic on purpose; the user's brand
// designer replaces this with their look-and-feel.

export function PublicLayout({ children }: { children: ComponentChildren }) {
  return (
    <div class="min-h-screen bg-white text-slate-900">
      <header class="border-b border-slate-200">
        <nav class="mx-auto flex max-w-6xl items-center justify-between px-6 py-4">
          <Link href="/" class="text-lg font-semibold tracking-tight">Acme</Link>
          <div class="flex items-center gap-6 text-sm">
            <Link href="/pricing" class="text-slate-600 hover:text-slate-900">Pricing</Link>
            <Link href="/docs" class="text-slate-600 hover:text-slate-900">Docs</Link>
            <Link href="/contact" class="text-slate-600 hover:text-slate-900">Contact</Link>
            <Link href="/login" class="rounded bg-slate-900 px-3 py-1.5 text-white hover:bg-slate-800">
              Sign in
            </Link>
          </div>
        </nav>
      </header>
      <main>{children}</main>
      <footer class="border-t border-slate-200">
        <div class="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-3 px-6 py-6 text-xs text-slate-500">
          <span>© {new Date().getFullYear()} Acme. Built with Railbase.</span>
          <div class="flex gap-4">
            <Link href="/docs/privacy" class="hover:text-slate-700">Privacy</Link>
            <Link href="/docs/terms" class="hover:text-slate-700">Terms</Link>
            <Link href="/docs/cookies" class="hover:text-slate-700">Cookies</Link>
          </div>
        </div>
      </footer>
    </div>
  );
}
