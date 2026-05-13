/**
 * @fileoverview no-hardcoded-tw-color — forbid literal Tailwind color
 * utilities; all colors must come through oklch tokens in
 * lib/ui/theme.ts (e.g. text-foreground / bg-card / border-destructive).
 *
 * Why: shadcn-on-Preact kit (docs/12 §Shareable UI kit) is theme-driven;
 * literal colors break dark mode, hi-contrast mode, and any future
 * theming plugin. Once even one screen hardcodes `text-red-500`, the
 * audit becomes intractable.
 *
 * Detection: scans string literals + template literals containing
 * className-like content for Tailwind color utilities matching
 *   {text,bg,border,ring,fill,stroke,from,to,via,outline,decoration,
 *    accent,divide,placeholder,caret}-{red,blue,green,...,slate,zinc,
 *    neutral,stone,gray,…}-{50..950}
 * or arbitrary-value bracket colors `bg-[#abc]` / `text-[rgb(...)]`.
 *
 * Escape hatch — a same-line or preceding-line comment containing
 * either `recharts:` or `shadcn:` silences the rule for that AST node.
 *  - `recharts:` — Recharts axis/series props take literal color
 *    strings, not Tailwind classes; the literal must be a theme
 *    token's hex equivalent (validation lives in the chart wrapper).
 *  - `shadcn:` — canonical shadcn/ui components specify literal palette
 *    colors that intentionally do not theme (e.g. modal overlay scrims
 *    fixed at `bg-black/50`, sonner status icons at emerald/amber/sky/
 *    red-500, password-strength bars at amber/yellow/emerald-500). Use
 *    only inside `lib/ui/*.ui.tsx` and only where the canonical shadcn
 *    source uses the same literal.
 *
 * False-positive policy: comment-pragma per-occurrence. We deliberately
 * do NOT have a globally-disabled mode — if you find yourself disabling
 * this rule a lot, add a missing theme token instead.
 */

// Tailwind v4 default palette names. List is intentionally explicit
// (not regex `[a-z]+`) so we don't false-positive on `text-balance`
// or `border-collapse` (utility classes that happen to start with
// allowed prefixes).
const PALETTE = [
  "slate", "gray", "zinc", "neutral", "stone",
  "red", "orange", "amber", "yellow", "lime",
  "green", "emerald", "teal", "cyan", "sky",
  "blue", "indigo", "violet", "purple", "fuchsia",
  "pink", "rose",
  "black", "white",
].join("|");

const PREFIX = [
  "text", "bg", "border", "ring", "fill", "stroke",
  "from", "to", "via", "outline", "decoration",
  "accent", "divide", "placeholder", "caret",
].join("|");

// Match `text-red-500`, `bg-blue-900/50`, `border-zinc-200`, etc.
// Also matches `text-black` / `bg-white` (no shade suffix).
const COLOR_UTILITY = new RegExp(
  `\\b(?:${PREFIX})-(?:${PALETTE})(?:-(?:[0-9]{2,3}))?(?:/[0-9]+)?\\b`,
  "g",
);

// Match arbitrary-value brackets containing a color: `bg-[#abc]`,
// `text-[rgb(...)]`, `border-[hsl(...)]`. The bracket value is a hex
// or color-function call.
const BRACKET_COLOR = new RegExp(
  `\\b(?:${PREFIX})-\\[(?:#[0-9a-fA-F]{3,8}|(?:rgb|rgba|hsl|hsla|oklch|color)\\([^\\]]*\\))\\]`,
  "g",
);

/** @type {import('eslint').Rule.RuleModule} */
export default {
  meta: {
    type: "problem",
    docs: {
      description:
        "Forbid literal Tailwind color utilities + bracket colors in className strings. Use theme tokens.",
    },
    schema: [],
    messages: {
      paletteUtility:
        "Hardcoded Tailwind color `{{match}}`. Use a theme token (text-foreground / bg-card / border-destructive / …) from lib/ui/theme.ts. To intentionally use a literal color (Recharts chart series, or a canonical shadcn component), add a `/* recharts: ... */` or `/* shadcn: ... */` pragma on the same or preceding line.",
      bracketColor:
        "Bracket-color `{{match}}` in className. Promote to a theme token in lib/ui/theme.ts. Pragma `/* recharts: ... */` or `/* shadcn: ... */` silences this rule.",
    },
  },

  create(context) {
    const sourceCode = context.sourceCode || context.getSourceCode();

    const PRAGMA_RE = /(?:recharts|shadcn):/i;

    function hasEscapePragma(node) {
      // Walk up: a pragma on the literal itself, or on any enclosing
      // statement/expression up to the program root, silences this
      // literal. A single `/* shadcn: … */` block-comment above a
      // const declaration thus covers every string literal inside it.
      let cur = node;
      while (cur && cur.type !== 'Program') {
        const before = sourceCode.getCommentsBefore
          ? sourceCode.getCommentsBefore(cur)
          : [];
        for (const c of before) {
          if (PRAGMA_RE.test(c.value)) return true;
        }
        const trailing = sourceCode.getCommentsAfter
          ? sourceCode.getCommentsAfter(cur)
          : [];
        for (const c of trailing) {
          if (c.loc.start.line === cur.loc.end.line && PRAGMA_RE.test(c.value)) {
            return true;
          }
        }
        // Inner-leading comments (e.g. `cn(/* shadcn: */ 'bg-amber-500', …)`).
        const inside = sourceCode.getCommentsInside
          ? sourceCode.getCommentsInside(cur)
          : [];
        for (const c of inside) {
          if (
            PRAGMA_RE.test(c.value) &&
            c.range[1] <= node.range[0]
          ) {
            return true;
          }
        }
        cur = cur.parent;
      }
      return false;
    }

    function reportMatches(node, text) {
      if (hasEscapePragma(node)) return;
      let m;
      while ((m = COLOR_UTILITY.exec(text)) !== null) {
        context.report({
          node,
          messageId: "paletteUtility",
          data: { match: m[0] },
        });
      }
      COLOR_UTILITY.lastIndex = 0;
      while ((m = BRACKET_COLOR.exec(text)) !== null) {
        context.report({
          node,
          messageId: "bracketColor",
          data: { match: m[0] },
        });
      }
      BRACKET_COLOR.lastIndex = 0;
    }

    return {
      Literal(node) {
        if (typeof node.value !== "string") return;
        // Only inspect strings long enough to plausibly carry a class
        // list; skip trivial enum strings.
        if (node.value.length < 4) return;
        reportMatches(node, node.value);
      },
      TemplateLiteral(node) {
        for (const q of node.quasis) {
          if (q.value.cooked) reportMatches(node, q.value.cooked);
        }
      },
    };
  },
};
