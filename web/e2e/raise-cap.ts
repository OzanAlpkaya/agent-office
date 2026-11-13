// Phase 20 (item 8): raises the daemon-wide agent cap from the real UI, for
// test/e2e's TestDaemonCapFromTheUIEndToEnd. Not a Playwright test file: the
// Go test starts the daemon and runs this with the daemon's URL, a pairing
// code, the cap a project has and the daemon-wide cap to set.
//
//   node e2e/raise-cap.ts <base> <pairing-code> <project-cap> <daemon-cap-now> <new-daemon-cap>
import { chromium, expect } from "@playwright/test";

const [base, code, projectCap, capNow, newCap] = process.argv.slice(2);
if (!base || !code || !projectCap || !capNow || !newCap) {
  console.error("usage: raise-cap.ts <base> <pairing-code> <project-cap> <daemon-cap-now> <new-daemon-cap>");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(`${base}/#pair=${code}`);
  await page.getByRole("button", { name: "Pair" }).click();
  await expect(page.locator(".board")).toBeVisible({ timeout: 15_000 });
  // The project's settings say its Max agents is above the daemon-wide cap, and link to it.
  await page.getByRole("button", { name: /^Project settings/ }).click();
  const note = page.getByTestId("cap-note");
  await expect(note).toContainText(`The daemon-wide cap of ${capNow} is below this project's ${projectCap}`);
  await note.getByRole("button", { name: "Change the daemon-wide cap" }).click();
  const field = page.getByRole("textbox", { name: "Max agents (all projects)" });
  await expect(field).toBeFocused();
  await field.fill(newCap);
  await page.getByRole("form", { name: "Daemon-wide agent cap" }).getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("form", { name: "Daemon-wide agent cap" }).getByRole("status")).toHaveText("Saved.");
  await expect(page.getByRole("form", { name: "Daemon-wide agent cap" })).toContainText("Set here");
  if (errors.length > 0) throw new Error("page errors: " + errors.join("; "));
  console.log("raised the daemon-wide cap to " + newCap);
} finally {
  await browser.close();
}
