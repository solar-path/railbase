import { test, expect } from "@playwright/test";

// Visual baseline for the 8 most-trafficked admin screens. After
// Wave 3 every screen mounts inside <AdminPage>, so a single
// header-card-toolbar-body shape is enforced across the set.
// Snapshots catch regressions to that contract; pure-text content
// (counts, IDs) is masked so the baseline survives normal data flux.

const ADMIN_EMAIL = process.env.RAILBASE_E2E_EMAIL ?? "admin@example.com";
const ADMIN_PASSWORD = process.env.RAILBASE_E2E_PASSWORD ?? "AdminP@ss123";

test.beforeEach(async ({ page }) => {
  await page.goto("/_/login");
  await page.getByLabel(/email/i).fill(ADMIN_EMAIL);
  await page.getByLabel(/password/i).fill(ADMIN_PASSWORD);
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByText("Dashboard")).toBeVisible();
});

const screens: Array<{ path: string; name: string; landmark: RegExp }> = [
  { path: "/_/schema",              name: "schema.png",      landmark: /Schema/i },
  // Audit + App logs now share the unified Logs screen; the active
  // category surfaces as a tab, the page title is "Logs".
  { path: "/_/logs/audit",          name: "audit.png",       landmark: /Audit/i },
  { path: "/_/logs/app",            name: "logs.png",        landmark: /Logs/i },
  { path: "/_/data/_jobs",          name: "jobs.png",        landmark: /Jobs/i },
  { path: "/_/logs/health",         name: "health.png",      landmark: /Health/i },
  { path: "/_/settings",            name: "settings.png",    landmark: /Settings/i },
  { path: "/_/data/_api_tokens",    name: "api-tokens.png",  landmark: /API tokens/i },
  { path: "/_/settings/mailer",     name: "mailer.png",      landmark: /Mailer/i },
];

for (const s of screens) {
  test(`${s.name} renders + baseline`, async ({ page }) => {
    await page.goto(s.path);
    await expect(page.getByText(s.landmark).first()).toBeVisible();
    // Per-screen visual baseline; counts / dates / hex ids masked.
    await expect(page).toHaveScreenshot(s.name, {
      fullPage: false,
      mask: [
        page.locator(".tabular-nums"),
        page.locator(".rb-mono"),              // ids, timestamps, hex
        page.locator("[data-testid='live-pulse']"),
      ],
    });
  });
}
