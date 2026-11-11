// The screenshot tour of the main screens (Phase 18): board, card detail,
// settings, office, archive, and the phone width. Writes PNGs to
// AO_TOUR_OUT (default test-results/screens/tour); docs/screenshots/phase-18
// holds the before and after sets.
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon } from "./daemon";
import { tourWorld } from "./tour";

const out = process.env.AO_TOUR_OUT ?? "test-results/screens/tour";

async function open(page: Page): Promise<MockDaemon> {
  const daemon = new MockDaemon(tourWorld());
  await daemon.install(page);
  await page.goto("/");
  await expect(page.locator(".board")).toBeVisible();
  await expect(page.locator(".usage-line").first()).toBeVisible(); // usage and roles have loaded
  await page.waitForTimeout(300);
  return daemon;
}

// The office animates; wait until its renderer is up and the first frames are drawn.
async function officeReady(page: Page): Promise<void> {
  await expect(page.locator(".office canvas")).toBeVisible();
  await expect(page.locator(".nameplate").first()).toBeVisible();
  await page.waitForTimeout(1500);
}

test("desktop tour", async ({ page }) => {
  await open(page);
  await page.screenshot({ path: `${out}/board.png` });

  // Before Phase 18 a card shows everything; the detail is the card itself.
  const card = page.locator("#task-g00000000002");
  await card.scrollIntoViewIfNeeded();
  await card.screenshot({ path: `${out}/card.png` });

  await page.getByRole("button", { name: "Projects", exact: true }).click();
  await page.getByRole("button", { name: "Settings", exact: true }).first().click();
  await page.screenshot({ path: `${out}/settings.png` });
  await page.getByRole("button", { name: "Projects", exact: true }).click();

  await page.getByRole("button", { name: /^Archive/ }).click();
  await page.getByRole("button", { name: /Sprint 3: full-text search/ }).click();
  await expect(page.getByTestId("goal-record")).toBeVisible();
  await page.screenshot({ path: `${out}/archive.png` });

  await page.getByRole("button", { name: "Office", exact: true }).click();
  await officeReady(page);
  await page.screenshot({ path: `${out}/office.png` });
});

test("phone tour", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page);
  await page.screenshot({ path: `${out}/phone-board.png` });
  await page.getByRole("button", { name: "Office", exact: true }).click();
  await officeReady(page);
  await page.screenshot({ path: `${out}/phone-office.png` });
});
