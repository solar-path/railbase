// Admin SPA internationalisation.
//
// Same signals-based approach as auth/context.tsx — no React Context,
// no Provider fan-out. A module-level `localeSignal` + `dictSignal`
// hold the state; `useT()` subscribes a component to both so it
// re-renders on a language switch.
//
// Translations ship WITH the SPA build (not fetched from the backend):
// `locales/en.json` is the source of truth, the other 9 are produced
// by `scripts/i18n-translate.ts`. English is imported statically (it's
// the always-available fallback); the rest are lazy-loaded on demand
// via import.meta.glob — a locale whose JSON hasn't been generated yet
// simply falls back to English, key by key.
//
// The public `/api/i18n/*` endpoints + the generated SDK's i18n.ts are
// a SEPARATE surface — those serve downstream apps. This module is the
// admin UI's own dictionary.

import { signal } from "@preact/signals";
import enDict from "./locales/en.json";

type Dict = Record<string, string>;

// The 10 most-spoken world languages — kept in sync with the LANGS
// table in scripts/i18n-translate.ts. `name` is the endonym (shown in
// the switcher as-is); `dir` drives <html dir> for RTL scripts.
export const SUPPORTED_LOCALES = [
  { code: "en", name: "English", dir: "ltr" },
  { code: "zh", name: "中文", dir: "ltr" },
  { code: "hi", name: "हिन्दी", dir: "ltr" },
  { code: "es", name: "Español", dir: "ltr" },
  { code: "fr", name: "Français", dir: "ltr" },
  { code: "ar", name: "العربية", dir: "rtl" },
  { code: "bn", name: "বাংলা", dir: "ltr" },
  { code: "pt", name: "Português", dir: "ltr" },
  { code: "ru", name: "Русский", dir: "ltr" },
  { code: "ur", name: "اردو", dir: "rtl" },
] as const;

export type LocaleCode = (typeof SUPPORTED_LOCALES)[number]["code"];

const STORAGE_KEY = "rb_admin_locale";
const DEFAULT_LOCALE: LocaleCode = "en";

// Lazy loaders for every non-English bundle. import.meta.glob returns
// only the files that actually exist, so a not-yet-generated locale is
// simply absent here and loadDict falls back to English. en.json is
// excluded — it ships statically (see enDict above) as the always-
// present fallback, so globbing it too would only confuse the bundler.
const localeModules = import.meta.glob<{ default: Dict }>([
  "./locales/*.json",
  "!./locales/en.json",
]);

// ── module state ─────────────────────────────────────────────────

export const localeSignal = signal<LocaleCode>(DEFAULT_LOCALE);
// dictSignal always holds a fully-usable dictionary — English at boot,
// swapped for the active locale once its bundle resolves.
const dictSignal = signal<Dict>(enDict as Dict);

function isSupported(code: string): code is LocaleCode {
  return SUPPORTED_LOCALES.some((l) => l.code === code);
}

export function dirOf(code: string): "ltr" | "rtl" {
  const l = SUPPORTED_LOCALES.find((x) => x.code === code);
  return l?.dir === "rtl" ? "rtl" : "ltr";
}

// detectInitialLocale: stored choice → browser language → English.
function detectInitialLocale(): LocaleCode {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && isSupported(stored)) return stored;
  } catch {
    /* localStorage unavailable (private mode etc) — fall through */
  }
  const nav = (navigator.language || "").toLowerCase().split("-")[0];
  if (nav && isSupported(nav)) return nav;
  return DEFAULT_LOCALE;
}

async function loadDict(code: LocaleCode): Promise<Dict> {
  if (code === "en") return enDict as Dict;
  const loader = localeModules[`./locales/${code}.json`];
  if (!loader) return {}; // bundle not generated yet → translate() falls back to en
  try {
    const mod = await loader();
    return mod.default ?? {};
  } catch {
    return {};
  }
}

function applyHtmlAttrs(code: LocaleCode) {
  if (typeof document === "undefined") return;
  document.documentElement.lang = code;
  document.documentElement.dir = dirOf(code);
}

// ── public API ───────────────────────────────────────────────────

// setLocale swaps the active language: loads the bundle, updates the
// signals (re-rendering every useT() consumer), persists the choice,
// and updates <html lang/dir>.
export async function setLocale(code: LocaleCode): Promise<void> {
  if (!isSupported(code)) code = DEFAULT_LOCALE;
  const dict = await loadDict(code);
  dictSignal.value = dict;
  localeSignal.value = code;
  try {
    localStorage.setItem(STORAGE_KEY, code);
  } catch {
    /* ignore */
  }
  applyHtmlAttrs(code);
}

// initI18n runs once at boot (main.tsx awaits it before first render)
// so a stored non-English choice doesn't flash English first.
export async function initI18n(): Promise<void> {
  const code = detectInitialLocale();
  applyHtmlAttrs(code);
  if (code === "en") {
    localeSignal.value = "en";
    return;
  }
  await setLocale(code);
}

// translate resolves a key against the active dict, then English, then
// the key itself (so a missing translation is visible, not blank).
// `{name}` placeholders are filled from params; unknown placeholders
// render literally.
function translate(dict: Dict, key: string, params?: Record<string, unknown>): string {
  const tpl = dict[key] ?? (enDict as Dict)[key] ?? key;
  if (!params || tpl.indexOf("{") === -1) return tpl;
  return tpl.replace(/\{(\w+)\}/g, (whole, name: string) => {
    const v = params[name];
    return v === undefined ? whole : String(v);
  });
}

export interface Translator {
  /** Current locale code. */
  locale: LocaleCode;
  /** "ltr" | "rtl" for the current locale. */
  dir: "ltr" | "rtl";
  /** Translate `key`, filling `{name}` placeholders from `params`. */
  t: (key: string, params?: Record<string, unknown>) => string;
  /** Pluralised lookup: count === 1 → `key.one`, else `key.other`. */
  tp: (key: string, count: number, params?: Record<string, unknown>) => string;
  /** Switch the admin UI language. */
  setLocale: typeof setLocale;
}

// useT is the component-facing hook. Reading the two signals here
// subscribes the calling component, so it re-renders when the language
// changes. `t` / `tp` close over the current dict.
export function useT(): Translator {
  const locale = localeSignal.value; // subscribe
  const dict = dictSignal.value; // subscribe
  return {
    locale,
    dir: dirOf(locale),
    t: (key, params) => translate(dict, key, params),
    tp: (key, count, params) =>
      translate(dict, key + (count === 1 ? ".one" : ".other"), params),
    setLocale,
  };
}
