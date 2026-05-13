/**
 * @fileoverview no-list-when-data-is-paged — if the file uses paginated
 * data shape (`totalItems` / `totalPages` / `perPage`), then `.map()`
 * over the items should be inside a <QDataTable> / <Pager> / <Table>
 * wrap, not a raw .map render.
 *
 * Heuristic (intentionally coarse to keep the rule cheap):
 *   1. File mentions `totalItems` OR `totalPages` OR `perPage`.
 *   2. File contains at least one `.map(...)` call expression.
 *   3. File does NOT mention `QDataTable`, `Pager`, `Table` import, or
 *      `TableBody` (kit table primitive).
 *
 * If all three: report a one-shot warning at the top-level Program node
 * with a hint to wire pagination through <Pager> or hoist the list to
 * <QDataTable>. We do NOT try to pinpoint the specific .map — false
 * positives would be too noisy. The current shape teaches the pattern
 * once per file.
 *
 * Escape hatch: same-file `// eslint-disable-next-line railbase/no-list-when-data-is-paged`
 * at the offending .map, OR fix the architecture (wrap the list).
 *
 * This rule is intentionally weak: it's a smoke detector, not a proof
 * system. It catches the regression where a new screen ships an
 * `items.slice(0, perPage).map(...)` direct render without backend
 * pagination, which is what screens degrade into when reviewers don't
 * notice.
 */

const PAGINATION_MARKERS = [
  "totalItems",
  "totalPages",
  "perPage",
];

const WRAPPER_MARKERS = [
  "QDataTable",
  "Pager",
  "TableBody",
  "@/lib/ui/table.ui",
  "../layout/pager",
];

/** @type {import('eslint').Rule.RuleModule} */
export default {
  meta: {
    type: "suggestion",
    docs: {
      description:
        "Paginated data shape detected without QDataTable / Pager / Table wrap. Wire pagination through the canonical helpers.",
    },
    schema: [],
    messages: {
      pagedRawMap:
        "File uses paginated data ({{markers}}) but renders rows without QDataTable / Pager / Table wrap. Wire pagination through `<Pager>` from layout/pager.tsx or hoist to `<QDataTable>`.",
    },
  },

  create(context) {
    // Scope: only screens/ + layout/ TSX files. API clients, types, and
    // generic utility modules can mention `perPage` in fetch-arg
    // signatures without rendering anything — false-positive territory.
    const filename = context.filename || context.getFilename();
    if (!/[/\\]src[/\\](?:screens|layout)[/\\][^/\\]+\.tsx$/.test(filename)) {
      return {};
    }

    const sourceCode = context.sourceCode || context.getSourceCode();
    const text = sourceCode.getText();

    const presentPaginationMarkers = PAGINATION_MARKERS.filter((m) =>
      text.includes(m),
    );
    if (presentPaginationMarkers.length === 0) return {};

    const presentWrapperMarkers = WRAPPER_MARKERS.filter((m) =>
      text.includes(m),
    );
    if (presentWrapperMarkers.length > 0) return {};

    // Final check: any .map() at all? `.map(` substring is good enough
    // — we already know the file is paginated and has no wrapper.
    if (!/\.map\s*\(/.test(text)) return {};

    return {
      Program(node) {
        context.report({
          node,
          messageId: "pagedRawMap",
          data: {
            markers: presentPaginationMarkers.join(", "),
          },
        });
      },
    };
  },
};
