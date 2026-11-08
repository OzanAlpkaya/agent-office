// Remote access (ADR 0038-0040) against a mocked daemon: pairing, devices, the
// phone layout, the terminal key bar, copy and paste, and the PWA.
import { type BrowserContextOptions, type Page, devices, expect, test } from "@playwright/test";
import { MockDaemon, mockCode, team } from "./daemon";

const screens = "test-results/screens";
const worker = "b2c3d4000001"; // a working agent of team(4) with a live terminal

async function open(page: Page, daemon: MockDaemon, path = "/"): Promise<string[]> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => {
    // A refused request logs a console error by itself; those are expected here.
    if (m.type() === "error" && !/status of 401/.test(m.text())) errors.push(m.text());
  });
  await daemon.install(page);
  await page.goto(path);
  return errors;
}

test("an unpaired browser pairs with a code", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  daemon.paired = false;
  const errors = await open(page, daemon);
  await expect(page.getByRole("heading", { name: "agent-office" })).toBeVisible();
  await expect(page.getByText("agent-office pair")).toBeVisible();
  // Nothing of the board leaks before pairing.
  await expect(page.getByText("Waiting for your approval")).toHaveCount(0);

  const code = page.getByLabel("Pairing code");
  await code.fill("zzzz-zzzz");
  await page.getByRole("button", { name: "Pair" }).click();
  await expect(page.getByRole("alert")).toContainText("wrong or has expired");

  await code.fill("abcdefgh");
  await expect(code).toHaveValue("ABCD-EFGH");
  await page.getByLabel("Device name").fill("Test phone");
  await page.getByRole("button", { name: "Pair" }).click();
  await expect(page.getByText("Waiting for your approval")).toBeVisible();
  expect(daemon.requests).toContain("POST /api/auth/pair Test phone");
  expect(errors).toEqual([]);
});

test("the link agent-office pair prints fills in the code and leaves no trace in the URL", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  daemon.paired = false;
  await open(page, daemon, `/#pair=${mockCode}`);
  await expect(page.getByLabel("Pairing code")).toHaveValue(mockCode);
  expect(new URL(page.url()).hash).toBe("");
  await page.getByRole("button", { name: "Pair" }).click();
  await expect(page.getByText("Waiting for your approval")).toBeVisible();
});

test("a browser signed out from another device returns to the pairing screen", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  await open(page, daemon);
  await expect(page.getByText("Waiting for your approval")).toBeVisible();
  daemon.signOut();
  await expect(page.getByLabel("Pairing code")).toBeVisible();
});

test("the Devices panel pairs a device and signs devices out", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  const errors = await open(page, daemon);
  await page.getByRole("button", { name: "Devices" }).click();
  const panel = page.getByRole("region", { name: "Devices" });
  await expect(panel.locator(".device")).toHaveCount(2);
  await expect(panel.locator(".device").first()).toContainText("this device");
  await expect(panel).toContainText("over Tailscale (ozan@github)");

  await panel.getByRole("button", { name: "Pair a device" }).click();
  await expect(page.getByTestId("pairing-code")).toContainText("WXYZ-2345");
  await expect(page.getByTestId("pairing-code")).toContainText("https://box.tail1234.ts.net/");
  await page.screenshot({ path: `${screens}/devices.png` });

  await panel.locator(".device").nth(1).getByRole("button", { name: "Sign out device" }).click();
  await expect(panel.locator(".device")).toHaveCount(1);
  expect(daemon.requests).toContain("DELETE /api/devices/d2");

  // Signing this browser out shows the pairing screen.
  await panel.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByLabel("Pairing code")).toBeVisible();
  expect(errors).toEqual([]);
});

// The page itself never scrolls on a phone; its panes do.
async function noHorizontalScroll(page: Page): Promise<void> {
  const [sw, w, sh, h] = await page.evaluate(() => [document.documentElement.scrollWidth, innerWidth,
    document.documentElement.scrollHeight, innerHeight]);
  expect(sw, "page scrolls sideways").toBeLessThanOrEqual(w);
  expect(sh, "page scrolls down").toBeLessThanOrEqual(h);
}

// A phone's viewport, touch and user agent, without its default browser:
// the suite runs Chromium only (ADR 0025).
function phone(name: string): BrowserContextOptions {
  const { defaultBrowserType: _, ...opts } = devices[name];
  return opts;
}

const phones: [string, BrowserContextOptions][] = [["Pixel 7", phone("Pixel 7")], ["iPhone 14", phone("iPhone 14")]];

for (const [name, device] of phones) {
  test.describe(`on a phone (${name})`, () => {
    test.use(device);

    test("board, approvals, agents and a terminal with its key bar", async ({ page }) => {
      const daemon = new MockDaemon(team(6));
      const errors = await open(page, daemon);
      const slug = name.replace(/\s+/g, "-").toLowerCase();

      // Approvals first, with thumb-sized buttons.
      const allow = page.getByRole("button", { name: "Allow for this agent" });
      await expect(allow).toBeVisible();
      expect((await allow.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(44);
      await expect(page.locator(".board")).toBeVisible();
      await noHorizontalScroll(page);
      await page.screenshot({ path: `${screens}/phone-${slug}-board.png` });

      // The board's columns snap sideways inside the board, not the page.
      const board = page.locator(".board");
      const [inner, outer] = await board.evaluate((el) => [el.scrollWidth, el.clientWidth]);
      expect(inner).toBeGreaterThan(outer);

      // The agent list is a tab.
      await expect(page.locator("aside")).toHaveCount(0);
      await page.getByRole("button", { name: /^Agents · \d+$/ }).click();
      await expect(page.locator("aside.agents-pane")).toBeVisible();
      await noHorizontalScroll(page);
      await page.screenshot({ path: `${screens}/phone-${slug}-agents.png` });
      await page.locator("aside .card", { hasText: worker }).click();

      // The terminal, with the keys a phone keyboard lacks.
      await expect(page.locator(".xterm")).toContainText(`mock terminal of ${worker}`);
      const bar = page.getByRole("toolbar", { name: "Terminal keys" });
      await expect(bar).toBeVisible();
      await bar.getByRole("button", { name: "Esc" }).click();
      await bar.getByRole("button", { name: "↑" }).click();
      await bar.getByRole("button", { name: "⏎" }).click();
      await expect.poll(() => daemon.terminalInput.filter((m) => m.includes('"input"'))).toEqual([
        JSON.stringify({ type: "input", data: "\x1b" }),
        JSON.stringify({ type: "input", data: "\x1b[A" }),
        JSON.stringify({ type: "input", data: "\r" }),
      ]);
      await noHorizontalScroll(page);
      await page.screenshot({ path: `${screens}/phone-${slug}-terminal.png` });
      expect(errors).toEqual([]);
    });
  });
}

test("the desktop layout keeps the sidebar and hides the key bar", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  await open(page, daemon);
  await expect(page.locator("aside")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Agents · / })).toHaveCount(0);
  await page.locator("aside .card", { hasText: worker }).click();
  await expect(page.locator(".xterm")).toContainText(`mock terminal of ${worker}`);
  await expect(page.getByRole("toolbar", { name: "Terminal keys" })).toBeHidden();
});

test("the terminal in use sets the size", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  await open(page, daemon);
  await page.locator("aside .card", { hasText: worker }).click();
  await expect(page.locator(".xterm")).toContainText(`mock terminal of ${worker}`);
  const resizes = (): number => daemon.terminalInput.filter((m) => m.includes('"resize"')).length;
  await expect.poll(resizes).toBeGreaterThan(0);
  const before = resizes();
  // Another viewer may have resized the PTY meanwhile: focusing this terminal claims it back.
  await page.locator("header h1").click();
  await page.locator(".xterm").click();
  await expect.poll(resizes).toBeGreaterThan(before);
});

test("copy and paste in the terminal", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const daemon = new MockDaemon(team(4));
  await open(page, daemon);
  await page.locator("aside .card", { hasText: worker }).click();
  const screen = page.locator(".xterm-screen");
  await expect(page.locator(".xterm")).toContainText(`mock terminal of ${worker}`);
  const clip = (): Promise<string> => page.evaluate(() => navigator.clipboard.readText());
  const inputs = (): string[] => daemon.terminalInput.filter((m) => m.includes('"input"'));

  // Selecting copies.
  const box = (await screen.boundingBox())!;
  await page.mouse.move(box.x + 2, box.y + 8);
  await page.mouse.down();
  await page.mouse.move(box.x + 150, box.y + 8, { steps: 5 });
  await page.mouse.up();
  await expect.poll(clip).toContain("mock terminal");

  // Ctrl+Shift+C copies and sends nothing to the agent.
  await page.evaluate(() => navigator.clipboard.writeText("old"));
  await page.keyboard.press("Control+Shift+C");
  await expect.poll(clip).toContain("mock terminal");
  expect(inputs(), "Ctrl+Shift+C reached the agent").toEqual([]);
  // Ctrl+Shift+V pastes through the browser's paste event (not ^V).
  await page.evaluate(() => navigator.clipboard.writeText("pasted-text"));
  await page.keyboard.press("Control+Shift+V");
  await expect.poll(inputs).toEqual([JSON.stringify({ type: "input", data: "pasted-text" })]);
  // Ctrl+C still interrupts.
  await page.keyboard.press("Control+c");
  await expect.poll(inputs).toEqual([
    JSON.stringify({ type: "input", data: "pasted-text" }), JSON.stringify({ type: "input", data: "\x03" }),
  ]);
});

test("Paste on the phone key bar pastes the clipboard", async ({ browser }) => {
  const context = await browser.newContext({ ...phone("Pixel 7"), baseURL: "http://127.0.0.1:4173", serviceWorkers: "block" });
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const page = await context.newPage();
  const daemon = new MockDaemon(team(4));
  await open(page, daemon);
  await page.getByRole("button", { name: /^Agents · / }).click();
  await page.locator("aside .card", { hasText: worker }).click();
  await expect(page.locator(".xterm")).toContainText(`mock terminal of ${worker}`);
  await page.evaluate(() => navigator.clipboard.writeText("hello-from-the-clipboard"));
  await page.getByRole("toolbar", { name: "Terminal keys" }).getByRole("button", { name: "Paste" }).click();
  await expect.poll(() => daemon.terminalInput.join("\n")).toContain("hello-from-the-clipboard");
  await context.close();
});

test.describe("as an installed app", () => {
  test("Chromium finds no installability errors, and offline shows the offline page", async ({ browser }) => {
    const context = await browser.newContext({ serviceWorkers: "allow", baseURL: "http://127.0.0.1:4173" });
    const page = await context.newPage();
    const daemon = new MockDaemon(team(4));
    await open(page, daemon);
    await expect(page.getByText("Waiting for your approval")).toBeVisible();
    await page.evaluate(() => navigator.serviceWorker.ready.then(() => undefined));

    const manifest = await page.evaluate(async () => {
      const href = document.querySelector<HTMLLinkElement>('link[rel="manifest"]')?.href ?? "";
      const res = await fetch(href);
      return { type: res.headers.get("content-type"), body: (await res.json()) as { display: string; icons: { purpose: string }[] } };
    });
    expect(manifest.body.display).toBe("standalone");
    expect(manifest.body.icons.map((i) => i.purpose)).toContain("maskable");

    const cdp = await context.newCDPSession(page);
    await expect.poll(async () => {
      const r = (await cdp.send("Page.getInstallabilityErrors")) as { installabilityErrors: { errorId: string }[] };
      return r.installabilityErrors.map((e) => e.errorId);
    }, { timeout: 10_000 }).toEqual([]);

    // Once the worker controls the page, a page load without a network shows the offline page.
    await page.reload();
    await page.evaluate(() => navigator.serviceWorker.controller !== null);
    await context.setOffline(true);
    await page.reload();
    await expect(page.getByRole("heading", { name: "agent-office is not reachable" })).toBeVisible();
    await page.screenshot({ path: `${screens}/offline.png` });
    await context.setOffline(false);
    await context.close();
  });
});
