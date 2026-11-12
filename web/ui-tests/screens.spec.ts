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
  await expect(page.locator(".task-cost").first()).toBeVisible(); // usage has loaded
  await page.waitForTimeout(300);
  return daemon;
}

// The office animates; wait until its renderer is up and the first frames are drawn.
async function officeReady(page: Page): Promise<void> {
  await expect(page.locator(".office canvas")).toBeVisible();
  await expect(page.locator(".nameplate").first()).toBeVisible();
  await page.waitForTimeout(1500);
}

// Animations (a modal popping in) are finished first, so nothing is caught half-drawn.
const shot = (page: Page, name: string): Promise<Buffer> => page.screenshot({ path: `${out}/${name}.png`, animations: "disabled" });

const nav = (page: Page, name: string) => page.getByRole("navigation", { name: "Main" }).getByRole("button", { name: new RegExp(`^${name}`) });

test("desktop tour", async ({ page }) => {
  await open(page);
  await shot(page, "board");

  await page.locator("#task-g00000000002").click();
  const modal = page.getByRole("dialog", { name: "Export links as CSV" });
  await expect(modal.getByText("Tech Lead's report")).toBeVisible();
  await shot(page, "card");
  await modal.getByRole("button", { name: "Close" }).click();

  // The project's settings (the gear by the picker) and the app's (the rail).
  await page.getByRole("button", { name: /^Project settings/ }).click();
  await expect(page.getByRole("tabpanel", { name: "General" })).toBeVisible();
  await page.waitForTimeout(300);
  await shot(page, "settings");
  await page.getByRole("tab", { name: "Office" }).click();
  const layout = page.getByTestId("layout-settings");
  await layout.getByRole("button", { name: "Own layout" }).click();
  await layout.getByRole("button", { name: "Open plan" }).click();
  await expect(layout.getByTestId("layout-summary")).toBeVisible();
  await shot(page, "settings-layout");
  await nav(page, "Settings").click();
  await expect(page.getByRole("tabpanel", { name: "Projects" })).toBeVisible();
  await shot(page, "app-settings");
  await page.getByRole("tab", { name: "Profile" }).click();
  const creator = page.getByTestId("avatar-creator");
  await creator.getByLabel("Name on your nameplate").fill("Ozan");
  await creator.getByRole("button", { name: "Randomize" }).click();
  await shot(page, "avatar-creator");

  await nav(page, "Board").click();
  await page.getByRole("button", { name: /^Archive/ }).click();
  await page.getByRole("button", { name: /Sprint 3: full-text search/ }).click();
  await expect(page.getByTestId("goal-record").getByText("Tech Lead's report")).toBeVisible();
  await shot(page, "archive");

  await nav(page, "Office").click();
  await officeReady(page);
  await shot(page, "office");

  await page.locator('.agent-row[data-agent="b2c3d4000001"] button').click();
  await expect(page.locator(".agent-header")).toBeVisible();
  await page.waitForTimeout(500);
  await shot(page, "agent");
});

test("phone tour", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page);
  await shot(page, "phone-board");
  await page.locator("#task-g00000000002").click();
  await expect(page.getByRole("dialog", { name: "Export links as CSV" })).toBeVisible();
  await shot(page, "phone-card");
  await page.getByRole("dialog", { name: "Export links as CSV" }).getByRole("button", { name: "Close" }).click();
  await nav(page, "Office").click();
  await officeReady(page);
  await shot(page, "phone-office");
  await page.getByRole("button", { name: /^Agents · / }).click();
  await shot(page, "phone-agents");
  await nav(page, "Settings").click();
  await page.waitForTimeout(300);
  await shot(page, "phone-settings");
  await page.getByRole("button", { name: /^Project settings/ }).click();
  await page.waitForTimeout(300);
  await shot(page, "phone-project-settings");
});
