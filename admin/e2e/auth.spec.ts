import { test, expect } from "@playwright/test";

// Authenticates as the operator-seeded bootstrap admin and stores
// the session cookie in a shared storageState file so subsequent
// specs skip the login dance. CI seeds the admin via `railbase
// admin create` before running this suite; locally you do the same.

const ADMIN_EMAIL = process.env.RAILBASE_E2E_EMAIL ?? "admin@example.com";
const ADMIN_PASSWORD = process.env.RAILBASE_E2E_PASSWORD ?? "AdminP@ss123";

test("sign in flow + dashboard renders", async ({ page }) => {
  await page.goto("/_/");
  // LoginGate may pause on a /_bootstrap probe before showing
  // the form — be patient with the email locator instead of
  // hammering on a 404 immediately.
  await page.getByLabel(/email/i).fill(ADMIN_EMAIL);
  await page.getByLabel(/password/i).fill(ADMIN_PASSWORD);
  await page.getByRole("button", { name: /sign in/i }).click();
  // After auth the shell mounts with the brand + sidebar.
  await expect(page.getByText("Railbase")).toBeVisible();
  await expect(page.getByText("Dashboard")).toBeVisible();
  // Visual baseline — first run generates the snapshot.
  await expect(page).toHaveScreenshot("dashboard.png", {
    fullPage: false,
    mask: [
      page.locator("text=/^\\d+\\s+events?$/"),  // audit count varies
      page.locator(".tabular-nums"),              // counters flicker
    ],
  });
});

test("login page baseline", async ({ page }) => {
  await page.goto("/_/login");
  await expect(page.getByText("Railbase admin")).toBeVisible();
  await expect(page).toHaveScreenshot("login.png");
});
