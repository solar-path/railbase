#!/usr/bin/env bun
//
// i18n-translate.ts — machine-translate a flat key→string JSON bundle
// into the 10 most-spoken world languages.
//
// Works for BOTH:
//   • the Railbase admin SPA      → admin/src/i18n/en.json   (default)
//   • Railbase's app-facing bundle → internal/i18n/embed/en.json
//   • any downstream app's bundle  → pass its en.json path
//
// Usage:
//   bun scripts/i18n-translate.ts                         # admin SPA bundle
//   bun scripts/i18n-translate.ts internal/i18n/embed/en.json
//   bun scripts/i18n-translate.ts path/to/en.json --force # re-translate everything
//   bun scripts/i18n-translate.ts en.json --only ru,es    # subset of targets
//
// Behaviour:
//   • Source is always `en` (the file you point at must be the English
//     bundle). Output files `<locale>.json` land next to it.
//   • Incremental: an existing translation is KEPT when its key still
//     exists in en.json AND its `{placeholder}` set is unchanged. Only
//     new / changed / placeholder-drifted keys are re-translated.
//     `--force` ignores existing files and re-translates everything.
//   • `{placeholder}` tokens are shielded before translation and
//     restored after, so Google never mangles them.
//   • Key order mirrors en.json; removed keys are dropped.
//
// Translation backend: the key-less `translate.googleapis.com` web
// endpoint (same one rail/scripts/i18n-sync.ts uses) — no API key, no
// billing setup. If you'd rather use the official Cloud Translation
// API, set GOOGLE_TRANSLATE_API_KEY and the script switches to the v2
// REST endpoint automatically.

import { dirname, join, resolve } from "node:path";

// The 10 most-spoken languages (by total speakers). `en` is the source;
// the other 9 are translation targets. `gt` is the Google Translate
// language code when it differs from our locale tag.
const LANGS: Array<{ locale: string; gt?: string; name: string }> = [
  { locale: "en", name: "English" },
  { locale: "zh", gt: "zh-CN", name: "Chinese" },
  { locale: "hi", name: "Hindi" },
  { locale: "es", name: "Spanish" },
  { locale: "fr", name: "French" },
  { locale: "ar", name: "Arabic" },
  { locale: "bn", name: "Bengali" },
  { locale: "pt", name: "Portuguese" },
  { locale: "ru", name: "Russian" },
  { locale: "ur", name: "Urdu" },
];

const RATE_MS = 150; // polite delay between calls to the free endpoint
const RETRIES = 2;

type Dict = Record<string, string>;

// ── placeholder shielding ────────────────────────────────────────
// `{field}` / `{count}` placeholders must survive translation intact.
// We swap them for opaque `{x0}` tokens before sending and restore the
// original names after — Google leaves `{x0}` alone far more reliably
// than a real word.

function shield(text: string): { masked: string; restore: (s: string) => string } {
  const names: string[] = [];
  const masked = text.replace(/\{(\w+)\}/g, (_m, name: string) => {
    const i = names.push(name) - 1;
    return `{x${i}}`;
  });
  return {
    masked,
    restore: (translated) =>
      translated.replace(
        /\{\s*x\s*(\d+)\s*\}/gi,
        (_m, n: string) => `{${names[Number(n)] ?? "_"}}`,
      ),
  };
}

// placeholderSet returns the sorted, comma-joined placeholder names in a
// string — used to detect when a key's placeholders changed and the
// stored translation must be redone.
function placeholderSet(s: string): string {
  return [...s.matchAll(/\{(\w+)\}/g)]
    .map((m) => m[1]!)
    .sort()
    .join(",");
}

// ── translation backends ─────────────────────────────────────────

const API_KEY = process.env.GOOGLE_TRANSLATE_API_KEY ?? "";

async function translateOfficial(text: string, tl: string): Promise<string> {
  const res = await fetch(
    "https://translation.googleapis.com/language/translate/v2?key=" + API_KEY,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ q: text, source: "en", target: tl, format: "text" }),
    },
  );
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = (await res.json()) as {
    data?: { translations?: Array<{ translatedText?: string }> };
  };
  const out = data.data?.translations?.[0]?.translatedText;
  if (out == null) throw new Error("empty response");
  return out;
}

async function translateFree(text: string, tl: string): Promise<string> {
  const url =
    "https://translate.googleapis.com/translate_a/single" +
    `?client=gtx&sl=en&tl=${tl}&dt=t&q=${encodeURIComponent(text)}`;
  const res = await fetch(url, { headers: { "User-Agent": "Mozilla/5.0" } });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = (await res.json()) as [Array<[string, string, ...unknown[]]>, ...unknown[]];
  return data[0].map((seg) => seg[0]).join("");
}

async function gtranslate(text: string, tl: string): Promise<string> {
  let lastErr: unknown;
  for (let attempt = 0; attempt <= RETRIES; attempt++) {
    try {
      return API_KEY ? await translateOfficial(text, tl) : await translateFree(text, tl);
    } catch (e) {
      lastErr = e;
      await Bun.sleep(300 * (attempt + 1));
    }
  }
  throw lastErr instanceof Error ? lastErr : new Error(String(lastErr));
}

// ── bundle IO ────────────────────────────────────────────────────

async function loadDict(abs: string): Promise<Dict> {
  const f = Bun.file(abs);
  if (!(await f.exists())) return {};
  try {
    const parsed = JSON.parse(await f.text());
    return parsed && typeof parsed === "object" ? (parsed as Dict) : {};
  } catch {
    return {};
  }
}

// serialize writes the dict in en.json key order, 2-space indented, with
// a trailing newline — stable diffs across re-runs.
function serialize(dict: Dict, keyOrder: string[]): string {
  const ordered: Dict = {};
  for (const k of keyOrder) ordered[k] = dict[k] ?? "";
  return JSON.stringify(ordered, null, 2) + "\n";
}

// ── per-locale sync ──────────────────────────────────────────────

async function syncLocale(
  en: Dict,
  keyOrder: string[],
  outDir: string,
  lang: { locale: string; gt?: string; name: string },
  force: boolean,
): Promise<{ locale: string; translated: number; kept: number; removed: number }> {
  const targetPath = join(outDir, `${lang.locale}.json`);
  const existing = force ? {} : await loadDict(targetPath);
  const next: Dict = {};
  const missing: string[] = [];

  for (const k of keyOrder) {
    const enVal = en[k] ?? "";
    const prev = existing[k];
    const usable =
      !force &&
      prev !== undefined &&
      prev !== "" &&
      placeholderSet(prev) === placeholderSet(enVal);
    if (usable) {
      next[k] = prev;
    } else {
      next[k] = enVal; // fall back to English until translated
      if (enVal.trim() !== "") missing.push(k);
    }
  }

  const removed = Object.keys(existing).filter((k) => !(k in en)).length;

  for (const k of missing) {
    const { masked, restore } = shield(en[k]!);
    try {
      const translated = await gtranslate(masked, lang.gt ?? lang.locale);
      next[k] = restore(translated);
    } catch (e) {
      console.warn(`  [${lang.locale}] ${k} → kept English (${(e as Error).message})`);
    }
    await Bun.sleep(RATE_MS);
  }

  await Bun.write(targetPath, serialize(next, keyOrder));
  return {
    locale: lang.locale,
    translated: missing.length,
    kept: keyOrder.length - missing.length,
    removed,
  };
}

// ── main ─────────────────────────────────────────────────────────

async function main() {
  const args = Bun.argv.slice(2);
  const force = args.includes("--force");
  const onlyIdx = args.indexOf("--only");
  const onlyArg = onlyIdx >= 0 ? args[onlyIdx + 1] : undefined;
  const only = onlyArg
    ? new Set(onlyArg.split(",").map((s) => s.trim()))
    : null;

  // First non-flag arg is the source en.json; default to the admin SPA.
  // Note: when --only isn't passed, onlyArg is undefined — don't let the
  // filter accidentally drop a positional path equal to args[0].
  const positional = args.filter(
    (a, i) => !a.startsWith("--") && (onlyArg === undefined || a !== onlyArg || i !== onlyIdx + 1),
  );
  const repoRoot = resolve(import.meta.dir, "..");
  const sourcePath = resolve(
    positional[0] ?? join(repoRoot, "admin/src/i18n/locales/en.json"),
  );

  const en = await loadDict(sourcePath);
  const keyOrder = Object.keys(en);
  if (keyOrder.length === 0) {
    console.error(`no keys found in ${sourcePath} — is it a flat key→string JSON bundle?`);
    process.exit(1);
  }

  const outDir = dirname(sourcePath);
  const targets = LANGS.filter(
    (l) => l.locale !== "en" && (!only || only.has(l.locale)),
  );

  console.log(`source: ${sourcePath} (${keyOrder.length} keys)`);
  console.log(
    `backend: ${API_KEY ? "Cloud Translation API v2" : "translate.googleapis.com (key-less)"}`,
  );
  console.log(`targets: ${targets.map((t) => t.locale).join(", ")}\n`);

  // Sequential, not parallel — the free endpoint rate-limits hard when
  // hit from many concurrent connections.
  for (const lang of targets) {
    const r = await syncLocale(en, keyOrder, outDir, lang, force);
    console.log(
      `  ${r.locale.padEnd(3)} ${lang.name.padEnd(11)} translated=${r.translated}  kept=${r.kept}  removed=${r.removed}`,
    );
  }
  console.log("\ndone.");
}

await main();
