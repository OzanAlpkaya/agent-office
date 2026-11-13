// The Phase 20 screenshots (docs/screenshots/phase-20): the office with the
// board summary in its bar and on the whiteboard, windows only on outside
// walls in the default office and every preset, the phone office, and the
// cap warning in a project's settings. Writes PNGs to AO_SHOTS_OUT (default
// test-results/screens/phase-20). The checks are in office.spec.ts and
// windows.test.ts; this only takes pictures.
import { type BrowserContextOptions, type Page, devices, expect, test } from "@playwright/test";
import { MockDaemon, layoutOf, presets } from "./daemon";
import { tourWorld } from "./tour";

const out = process.env.AO_SHOTS_OUT ?? "test-results/screens/phase-20";

async function office(page: Page, daemon: MockDaemon): Promise<void> {
  await daemon.install(page);
  await page.goto("/");
  await expect(page.locator(".board")).toBeVisible();
  await page.getByRole("navigation", { name: "Main" }).getByRole("button", { name: /^Office/ }).click();
  await expect(page.locator(".office canvas")).toBeVisible();
  await expect(page.locator(".nameplate").first()).toBeVisible();
  await page.waitForTimeout(1500);
}

const shot = (page: Page, name: string): Promise<Buffer> => page.screenshot({ path: `${out}/${name}.png`, animations: "disabled" });

test("the default office: the board summary and windows on outside walls only", async ({ page }) => {
  await office(page, new MockDaemon(tourWorld()));
  await shot(page, "office");
  await page.getByRole("group", { name: "Office zoom" }).getByRole("button", { name: "Zoom +" }).click();
  await page.waitForTimeout(800);
  const wb = await page.locator(".whiteboard-label").boundingBox();
  if (!wb) throw new Error("no whiteboard");
  await page.screenshot({ path: `${out}/office-whiteboard.png`, animations: "disabled",
    clip: { x: Math.max(0, wb.x - 120), y: Math.max(0, wb.y - 40), width: wb.width + 240, height: wb.height + 160 } });
});

for (const p of presets) {
  test(`the ${p.name} preset`, async ({ page }) => {
    const daemon = new MockDaemon(tourWorld());
    daemon.projects[0].project = { ...daemon.projects[0].project, settings: { office_layout: p.yaml },
      office: { source: "custom", layout: layoutOf(p.yaml) } };
    await office(page, daemon);
    await expect(page.locator(".office")).toHaveAttribute("data-plan", "custom");
    await shot(page, `office-${p.name}`);
  });
}

const { defaultBrowserType: _, ...pixel } = devices["Pixel 7"] as BrowserContextOptions & { defaultBrowserType?: string };
test.describe("phone", () => {
  test.use(pixel);
  test("the phone office", async ({ page }) => {
    await office(page, new MockDaemon(tourWorld()));
    await shot(page, "phone-office");
  });
});

test("a project's Max agents above the daemon-wide cap", async ({ page }) => {
  const daemon = new MockDaemon(tourWorld());
  await daemon.install(page);
  await page.goto("/");
  await page.getByRole("button", { name: /^Project settings/ }).click();
  await page.getByRole("textbox", { name: "Max agents", exact: true }).fill("12");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("cap-saved")).toBeVisible();
  await page.getByTestId("cap-saved").scrollIntoViewIfNeeded();
  await shot(page, "project-settings-cap");
});
