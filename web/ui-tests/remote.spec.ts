// Remote access (ADR 0038-0040) against a mocked daemon: pairing, devices, the
// phone layout, the terminal key bar, copy and paste, and the PWA.
import { type BrowserContext, type BrowserContextOptions, type Page, devices, expect, test } from "@playwright/test";
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
  await expect(page.getByRole("button", { name: /^Approvals/ })).toHaveCount(0);

  const code = page.getByLabel("Pairing code");
  await code.fill("zzzz-zzzz");
  await page.getByRole("button", { name: "Pair" }).click();
  await expect(page.getByRole("alert")).toContainText("wrong or has expired");

  await code.fill("abcdefgh");
  await expect(code).toHaveValue("ABCD-EFGH");
  await page.getByLabel("Device name").fill("Test phone");
  await page.getByRole("button", { name: "Pair" }).click();
  await expect(page.getByRole("button", { name: "Approvals: 1 waiting" })).toBeVisible();
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
  await expect(page.getByRole("button", { name: "Approvals: 1 waiting" })).toBeVisible();
});

test("sign out all other devices", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  await open(page, daemon);
  await page.getByRole("button", { name: "Devices" }).click();
  const panel = page.getByRole("region", { name: "Devices" });
  await expect(panel.locator(".device")).toHaveCount(2);
  await panel.getByRole("button", { name: "Sign out all other devices" }).click();
  await expect(panel.locator(".device")).toHaveCount(1);
  expect(daemon.requests).toContain("POST /api/devices/sign-out-others");
});

test("a pairing link opened in a paired browser leaves no code in the URL", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  await open(page, daemon, `/#pair=${mockCode}`);
  await expect(page.getByRole("button", { name: "Approvals: 1 waiting" })).toBeVisible();
  expect(new URL(page.url()).hash).toBe("");
});

test("a browser signed out from another device returns to the pairing screen", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  await open(page, daemon);
  await expect(page.getByRole("button", { name: "Approvals: 1 waiting" })).toBeVisible();
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
  await expect(panel.getByRole("button", { name: "Sign out all other devices" })).toHaveCount(0);

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
      const daemon = new MockDaemon(team(8)); // a host agent at a permission prompt, and a network request
      const errors = await open(page, daemon);
      const slug = name.replace(/\s+/g, "-").toLowerCase();

      // Approvals wait behind the header badge, in a sheet with thumb-sized buttons (ADR 0044).
      await expect(page.locator(".board")).toBeVisible();
      await noHorizontalScroll(page);
      await page.screenshot({ path: `${screens}/phone-${slug}-board.png` });
      await page.getByRole("button", { name: "Approvals: 2 waiting" }).click();
      const allow = page.getByRole("dialog", { name: "Approvals" }).getByRole("button", { name: "Allow for this agent" });
      await expect(allow).toBeVisible();
      expect((await allow.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(44);
      await noHorizontalScroll(page);
      await page.screenshot({ path: `${screens}/phone-${slug}-approvals.png` });
      await page.getByRole("dialog", { name: "Approvals" }).getByRole("button", { name: "Close" }).click();
      await expect(page.getByRole("dialog", { name: "Approvals" })).toHaveCount(0);

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

// The clipboard as in a browser profile that denies the async write (Ozan's
// manual test: copying silently did nothing). Only clipboard-read is granted,
// to read back what landed there; setClipboard grants write for a moment.
async function clipboardPage(page: Page, context: BrowserContext, setup?: (d: MockDaemon) => void): Promise<{
  clip: () => Promise<string>;
  setClipboard: (t: string) => Promise<void>;
  inputs: () => string[];
  daemon: MockDaemon;
}> {
  await context.grantPermissions(["clipboard-read"]);
  const daemon = new MockDaemon(team(4));
  setup?.(daemon);
  await open(page, daemon);
  await page.locator("aside .card", { hasText: worker }).click();
  await expect(page.locator(".xterm")).toContainText(`mock terminal of ${worker}`);
  return {
    daemon,
    clip: () => page.evaluate(() => navigator.clipboard.readText()),
    setClipboard: async (t: string) => {
      await context.grantPermissions(["clipboard-read", "clipboard-write"]);
      await page.evaluate((x) => navigator.clipboard.writeText(x), t);
      await context.clearPermissions();
      await context.grantPermissions(["clipboard-read"]);
    },
    inputs: () => daemon.terminalInput.filter((m) => m.includes('"input"')),
  };
}

async function selectFirstRow(page: Page): Promise<void> {
  const box = (await page.locator(".xterm-screen").boundingBox())!;
  await page.mouse.move(box.x + 1, box.y + 6);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width - 4, box.y + 6, { steps: 6 });
  await page.mouse.up();
}

const firstRow = `mock terminal of ${worker}`;

test("copied text lands in the clipboard without the clipboard-write permission", async ({ page, context }) => {
  const { clip, setClipboard, inputs } = await clipboardPage(page, context);
  // The condition of the bug: the async Clipboard API is refused.
  const refused = await page.evaluate(() => navigator.clipboard.writeText("x").then(() => "", (e: Error) => e.message));
  expect(refused).toContain("Write permission denied");

  // A mouse selection is copied when it ends.
  await selectFirstRow(page);
  await expect.poll(async () => (await clip()).trim()).toBe(firstRow);

  // Ctrl+Shift+C copies the selection and sends nothing to the agent.
  await setClipboard("old");
  await page.keyboard.press("Control+Shift+C");
  await expect.poll(async () => (await clip()).trim()).toBe(firstRow);
  expect(inputs(), "Ctrl+Shift+C reached the agent").toEqual([]);

  // The right-click menu copies too.
  await setClipboard("old");
  const box = (await page.locator(".xterm-screen").boundingBox())!;
  await page.mouse.click(box.x + 40, box.y + 6, { button: "right" });
  const menu = page.getByRole("menu", { name: "Terminal" });
  await expect(menu).toBeVisible();
  await menu.getByRole("menuitem", { name: "Copy" }).click();
  await expect(menu).toBeHidden();
  await expect.poll(async () => (await clip()).trim()).toBe(firstRow);
  await expect(page.getByRole("status")).toHaveCount(0); // no failure notice
  expect(inputs()).toEqual([]);
});

test("paste from the menu and Ctrl+Shift+V; Ctrl+C interrupts; F12 is the browser's", async ({ page, context }) => {
  const { setClipboard, inputs } = await clipboardPage(page, context);
  const box = (await page.locator(".xterm-screen").boundingBox())!;

  await setClipboard("pasted-by-menu");
  await page.mouse.click(box.x + 40, box.y + 30, { button: "right" });
  const menu = page.getByRole("menu", { name: "Terminal" });
  await expect(menu.getByRole("menuitem", { name: "Copy" })).toBeDisabled(); // nothing selected
  await menu.getByRole("menuitem", { name: "Paste" }).click();
  await expect.poll(inputs).toEqual([JSON.stringify({ type: "input", data: "pasted-by-menu" })]);

  // Ctrl+Shift+V pastes through the browser's paste event (not ^V).
  await setClipboard("pasted-text");
  await page.locator(".xterm").click();
  await page.keyboard.press("Control+Shift+V");
  await expect.poll(inputs).toContain(JSON.stringify({ type: "input", data: "pasted-text" }));

  // F12 and Ctrl+Shift+I go to the browser, not to the agent (no \x1b[24~ or Tab).
  const before = inputs().length;
  await page.keyboard.press("F12");
  await page.keyboard.press("Control+Shift+I");
  // Ctrl+C still interrupts, and is the only thing sent.
  await page.keyboard.press("Control+c");
  await expect.poll(() => inputs().slice(before)).toEqual([JSON.stringify({ type: "input", data: "\x03" })]);
});

test("a copy that fails says why", async ({ page, context }) => {
  // A browser that refuses both ways of copying.
  await context.addInitScript(() => {
    document.execCommand = () => false;
    navigator.clipboard.writeText = () => Promise.reject(new DOMException(
      "Failed to execute 'writeText' on 'Clipboard': Write permission denied.", "NotAllowedError"));
  });
  const { inputs } = await clipboardPage(page, context);
  await page.locator(".xterm").click();
  await page.keyboard.press("Control+Shift+C");
  await expect(page.getByRole("status")).toHaveText("Copy failed: nothing is selected");
  // The drag ends under the notice and outside the terminal: it is still copied.
  const box = (await page.locator(".xterm-screen").boundingBox())!;
  await page.mouse.move(box.x + 1, box.y + 6);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width + 30, box.y + 6, { steps: 6 });
  await page.mouse.up();
  await expect(page.getByRole("status")).toHaveText("Copy failed: Write permission denied.");
  await page.screenshot({ path: `${screens}/copy-failed.png` });
  expect(inputs()).toEqual([]);
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
    await expect(page.getByRole("button", { name: "Approvals: 1 waiting" })).toBeVisible();
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

// Claude Code's fullscreen mode (seen in Ozan's sessions): it turns on mouse
// tracking, draws its own selection and sends it as OSC 52; xterm.js then has
// no selection of its own ("Copy failed: nothing is selected" before the fix).
const fullscreen = "\x1b[?1000h\x1b[?1002h\x1b[?1006h";
const osc52 = (text: string): string => `\x1b]52;c;${Buffer.from(text, "utf8").toString("base64")}\x07`;
const answer = "● What can I help you with?";

function claudeFullscreen(d: MockDaemon): void {
  // An old request in the scrollback must not be applied when it is replayed.
  d.terminalPreamble = `${fullscreen}\r\n${answer}\r\n${osc52("old request from the scrollback")}`;
  // On the release of a drag (SGR mouse report "...m"), copy the selection, as Claude Code does.
  d.onTerminalInput = (data, reply) => { if (/\x1b\[<\d+;\d+;\d+m/.test(data)) reply(osc52(answer)); };
}

test("a selection Claude Code makes itself (OSC 52) lands in the clipboard", async ({ page, context }) => {
  const { clip, inputs } = await clipboardPage(page, context, claudeFullscreen);
  // (The browser's clipboard is shared by the tests, so only "not the old request" can be checked.)
  expect(await clip(), "the replayed scrollback set the clipboard").not.toBe("old request from the scrollback");
  const rowsEl = page.locator(".xterm-rows > div");
  const idx = (await rowsEl.allTextContents()).findIndex((l) => l.includes(answer));
  const rb = (await rowsEl.nth(idx).boundingBox())!;
  await page.mouse.move(rb.x + 2, rb.y + rb.height / 2);
  await page.mouse.down();
  await page.mouse.move(rb.x + 200, rb.y + rb.height / 2, { steps: 6 });
  await page.mouse.up();
  // The mouse went to the program (xterm.js made no selection) and its answer reached the clipboard.
  expect(inputs().some((m) => m.includes("\\u001b[<"))).toBe(true);
  await expect.poll(clip).toBe(answer);
  await expect(page.getByRole("status")).toHaveCount(0);
});

test("an agent cannot set or read the clipboard on its own", async ({ page, context }) => {
  const { clip, setClipboard, inputs, daemon } = await clipboardPage(page, context, claudeFullscreen);
  await setClipboard("mine");
  // No gesture in this terminal: an unprompted write is refused and said so.
  daemon.terminalOutput(worker, osc52("text the agent picked"));
  await expect(page.getByRole("status")).toContainText("Ignored a clipboard write from the agent");
  // A read request is never answered.
  daemon.terminalOutput(worker, "\x1b]52;c;?\x07");
  await page.waitForTimeout(500);
  expect(await clip()).toBe("mine");
  expect(inputs()).toEqual([]);
});

test("with Claude Code handling the mouse, Shift+drag selects and Ctrl+Shift+C explains", async ({ page, context }) => {
  const { clip } = await clipboardPage(page, context, (d) => { d.terminalPreamble = fullscreen; });
  await page.locator(".xterm").click();
  await page.keyboard.press("Control+Shift+C");
  await expect(page.getByRole("status")).toContainText("Claude Code handles the mouse in this terminal");
  await page.keyboard.down("Shift");
  await selectFirstRow(page);
  await page.keyboard.up("Shift");
  await expect.poll(async () => (await clip()).trim()).toBe(firstRow);
});
