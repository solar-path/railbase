import { defineConfig, devices } from "@playwright/test";

// Playwright config for admin UI snapshot baseline.
//
// Locks the visual contract for screens that survived Wave 3
// migration. CI runs all specs against a binary booted in
// `embed_pg` mode; local devs use `bun run test:e2e:ui`.
//
// First-time setup (one-off per machine):
//   bunx playwright install chromium
//
// Typical workflow:
//   bun run test:e2e            # headless, validates baseline
//   bun run test:e2e:ui         # interactive — review screenshots
//   bun run test:e2e:update     # accept new snapshots after intentional UI change
//
// Snapshot policy:
//   - Baseline images live next to the spec in __snapshots__/
//     (we don't commit them in this initial scaffold — they get
//      generated on first `--update-snapshots` run on the operator's
//      machine and validated in CI from there)
//   - threshold: 0.2% pixel diff (loose enough to tolerate font
//     hinting differences across linux/mac/win, tight enough to
//     catch a layout regression)
//
// Test isolation: each spec authenticates as the bootstrap admin
// and asserts the headless DOM via `page.locator` BEFORE taking a
// screenshot — the visual diff is a backstop, not the only signal.

const PORT = Number(process.env.RAILBASE_TEST_PORT ?? 8095);

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,                    // shared backend instance
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,                              // serial — single backend
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  expect: {
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.002,            // 0.2% tolerance
      animations: "disabled",
    },
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  // We do NOT auto-start the backend here — CI scripts boot it
  // separately so they can wire env vars + seed admin data. Local
  // workflow: in one terminal `make run-embed`, in another
  // `bun run test:e2e`.
});
