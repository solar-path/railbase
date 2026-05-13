// admin/eslint.config.js — flat config (ESLint 9+).
//
// Wires the in-repo plugin `eslint-plugin-railbase` (admin/eslint-rules/)
// onto admin source. Wave 1 phase: all three rules at "warn" — IDE shows
// squiggles, CI does NOT fail. Wave 3 flips to "error".
//
// Run:
//   bun run lint:eslint        # check
//   bun run lint:eslint:fix    # (no autofixes today; placeholder)
//
// Why flat config: ESLint 9 default; lets us reference the local plugin
// by module path without publishing to npm and without polluting the
// global plugin registry.

import railbase from "./eslint-rules/index.js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default [
  {
    ignores: [
      "dist/**",
      "node_modules/**",
      "eslint-rules/**", // the rules themselves are plain JS, not subject
      "**/*.d.ts",
    ],
  },
  ...tseslint.configs.recommended.map((c) => ({
    ...c,
    files: ["src/**/*.{ts,tsx}"],
  })),
  {
    files: ["src/**/*.{ts,tsx}"],
    plugins: {
      railbase,
      "react-hooks": reactHooks,
    },
    rules: {
      // Wave 3 phase: shell + paged-list rules flipped to ERROR after
      // migration of 20 screens to <AdminPage> (Wave 3 worklog). Lock
      // the gain — any new screen that bypasses the contract fails CI.
      "railbase/no-raw-page-shell": "error",
      "railbase/no-list-when-data-is-paged": "error",
      // Wave 4 follow-up: color rule promoted to error after migration sweep.
      "railbase/no-hardcoded-tw-color": "error",
      // react-hooks: only register the rules so existing inline disables
      // resolve. Default level "off" — we don't enforce hooks-deps in
      // this phase. Wave 1.x can promote to warn / error.
      "react-hooks/rules-of-hooks": "off",
      "react-hooks/exhaustive-deps": "off",
      // Calm the noise from typescript-eslint defaults so the railbase
      // rules are visible in CI output during the Wave 1-2 ramp. These
      // are reasonable to re-enable later in a focused TS-hygiene pass.
      "@typescript-eslint/no-unused-vars": "off",
      "@typescript-eslint/no-explicit-any": "off",
      "@typescript-eslint/no-empty-object-type": "off",
      "@typescript-eslint/ban-ts-comment": "off",
      "no-empty": "off",
      "no-useless-escape": "off",
      "no-irregular-whitespace": "off",
    },
  },
];
