package ts

import "strings"

// EmitI18n renders i18n.ts: typed wrappers for the public translation
// endpoints (internal/i18n.BundleHandler / LocalesHandler) plus a
// pure client-side Translator that mirrors the Go catalog's lookup
// semantics — `{name}` interpolation, missing-key-returns-key, and the
// `<base>.one` / `<base>.other` plural convention.
//
// Schema-independent: the i18n endpoints are fixed, not derived from
// CollectionSpec, so EmitI18n takes no arguments.
//
// Surface (both public — translations carry no auth):
//
//   - GET /api/i18n/locales        list supported locales + text dir
//   - GET /api/i18n/{locale}       flat key→template bundle (?prefix= filters)
func EmitI18n() string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`// i18n.ts — typed wrappers for the translation endpoints + a
// client-side Translator mirroring the server catalog's semantics.

import type { HTTPClient } from "./index.js";

/** One row from GET /api/i18n/locales. */
export interface LocaleInfo {
  /** BCP-47 tag, e.g. "en", "pt-BR". */
  locale: string;
  /** "ltr" | "rtl" — text direction for the locale. */
  dir: string;
  /** True for the catalog's configured fallback locale. */
  default?: boolean;
}

/** A translation bundle: the flat key→template map for one locale. */
export interface I18nBundle {
  locale: string;
  dir: string;
  keys: Record<string, string>;
}

/** A resolved translator over a single bundle. Pure — no network. */
export interface Translator {
  locale: string;
  /** "ltr" | "rtl" — apply to <html dir>. */
  dir: string;
  /** Render ` + "`key`" + ` with ` + "`{name}`" + ` interpolation. A key that is missing
   *  everywhere renders as the key itself, so the gap is visible. */
  t(key: string, params?: Record<string, unknown>): string;
  /** Pluralised lookup: count === 1 → ` + "`key.one`" + `, else ` + "`key.other`" + `.
   *  Mirrors the server catalog's English-grade plural rule. */
  plural(key: string, count: number, params?: Record<string, unknown>): string;
  /** True when ` + "`key`" + ` is present in the bundle. */
  has(key: string): boolean;
}

// interpolate replaces {name} placeholders with params[name]. Unknown
// placeholders render literally — same as the Go side.
function interpolate(tpl: string, params?: Record<string, unknown>): string {
  if (!params || tpl.indexOf("{") === -1) return tpl;
  return tpl.replace(/\{([^{}]+)\}/g, (whole, name) => {
    const v = params[name];
    return v === undefined ? whole : String(v);
  });
}

/** Build a Translator from an already-loaded bundle. Use this when the
 *  dictionary is bundled with the app or cached client-side; use
 *  i18nClient(http).loadTranslator() to fetch one over HTTP. */
export function createTranslator(bundle: I18nBundle): Translator {
  const keys = bundle.keys ?? {};
  return {
    locale: bundle.locale,
    dir: bundle.dir || "ltr",
    has(key: string): boolean {
      return Object.prototype.hasOwnProperty.call(keys, key);
    },
    t(key: string, params?: Record<string, unknown>): string {
      const tpl = keys[key];
      if (tpl === undefined) return key;
      return interpolate(tpl, params);
    },
    plural(key: string, count: number, params?: Record<string, unknown>): string {
      return this.t(key + (count === 1 ? ".one" : ".other"), params);
    },
  };
}

/** i18n wrappers. The endpoints are public (translations carry no
 *  auth) so these work before sign-in — load the dictionary on app
 *  boot:
 *
 *    const rb = createRailbaseClient({ baseURL });
 *    const tr = await rb.i18n.loadTranslator("ru");
 *    document.documentElement.dir = tr.dir;
 *    tr.t("auth.welcome", { name });
 *    tr.plural("cart.items", n, { count: n });
 */
export function i18nClient(http: HTTPClient) {
  return {
    /** GET /api/i18n/locales — supported locales for a language picker. */
    locales(): Promise<LocaleInfo[]> {
      return http
        .request<{ items: LocaleInfo[] }>("GET", "/api/i18n/locales")
        .then((r) => r.items ?? []);
    },

    /** GET /api/i18n/{locale} — the raw bundle. ` + "`prefix`" + ` filters keys
     *  server-side (e.g. prefix "auth" → only auth.* keys). */
    bundle(locale: string, opts: { prefix?: string } = {}): Promise<I18nBundle> {
      const q = opts.prefix ? "?prefix=" + encodeURIComponent(opts.prefix) : "";
      return http.request("GET", "/api/i18n/" + encodeURIComponent(locale) + q);
    },

    /** Fetch a bundle and wrap it in a ready-to-use Translator. */
    loadTranslator(locale: string, opts: { prefix?: string } = {}): Promise<Translator> {
      return this.bundle(locale, opts).then(createTranslator);
    },
  };
}
`)
	return b.String()
}
