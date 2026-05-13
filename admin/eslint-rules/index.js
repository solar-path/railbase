/**
 * @fileoverview eslint-plugin-railbase — in-repo plugin aggregating the
 * three admin-UI guardrails. Lives under admin/eslint-rules/ and is
 * referenced from admin/eslint.config.js as a local plugin (not a
 * published npm package).
 *
 * The three rules:
 *   - no-raw-page-shell      — screens must mount inside <AdminPage>
 *   - no-hardcoded-tw-color  — colors must come from theme tokens
 *   - no-list-when-data-is-paged — paginated shape needs Pager/QDataTable
 *
 * Phasing (per docs/23 §Lint enforcement):
 *   - Wave 1: all three at "warn" — CI green, IDE shows squiggles.
 *   - Wave 3: flip to "error" once 9 RHF-migrated screens shift to
 *     <AdminPage>, blocking regression in CI.
 */

import noRawPageShell from "./no-raw-page-shell.js";
import noHardcodedTwColor from "./no-hardcoded-tw-color.js";
import noListWhenDataIsPaged from "./no-list-when-data-is-paged.js";

export default {
  meta: {
    name: "eslint-plugin-railbase",
    version: "0.1.0",
  },
  rules: {
    "no-raw-page-shell": noRawPageShell,
    "no-hardcoded-tw-color": noHardcodedTwColor,
    "no-list-when-data-is-paged": noListWhenDataIsPaged,
  },
};
