// The Office tab against a mocked daemon (ADR 0024, 0025).
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, agent, team } from "./daemon";

const screens = "test-results/screens";

async function openOffice(page: Page, daemon: MockDaemon): Promise<void> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  (page as Page & { errors?: string[] }).errors = errors;
  await daemon.install(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Office", exact: true }).click();
  await expect(page.locator(".office")).toHaveAttribute("data-renderer", /webgl|canvas/);
  await expect(page.locator(".nameplate")).toHaveCount(daemon.world.agents.length);
}

const errorsOf = (page: Page): string[] => (page as Page & { errors?: string[] }).errors ?? [];

test("shows every agent at a desk with its pose", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const pose = (id: string) => page.locator(`.nameplate[data-agent="${id}"]`);
  await expect(pose("a1ead0000001")).toHaveAttribute("data-pose", "idle");
  await expect(pose("b2c3d4000001")).toHaveAttribute("data-pose", "typing");
  await expect(pose("d4e5f6000003")).toHaveAttribute("data-pose", "waiting");
  await expect(pose("e5f6a7000004")).toHaveAttribute("data-pose", "reviewing");
  await expect(pose("a7b8c9000006")).toHaveAttribute("data-pose", "booting");
  // a pending access request is waiting for the user too
  await expect(pose("b8c9d0000007")).toHaveAttribute("data-pose", "waiting");
  await expect(pose("d0e1f2000009")).toHaveAttribute("data-pose", "away");
  await expect(page.locator(".whiteboard-label")).toHaveAttribute("data-notes", "7");
  await expect(page.locator(".office-bar")).toContainText("2 waiting for you");
  // nameplates do not overlap each other
  const boxes = await page.locator(".nameplate").evaluateAll((els) => els.map((e) => e.getBoundingClientRect().toJSON() as DOMRect));
  for (let i = 0; i < boxes.length; i++) {
    for (let j = i + 1; j < boxes.length; j++) {
      const a = boxes[i], b = boxes[j];
      const overlap = a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;
      expect(overlap, `nameplates ${i} and ${j}`).toBe(false);
    }
  }
  await page.screenshot({ path: `${screens}/office-10-agents.png` });
  expect(errorsOf(page)).toEqual([]);
});

test("follows live state changes, new and removed agents", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const plate = (id: string) => page.locator(`.nameplate[data-agent="${id}"]`);
  const before = await plate("c3d4e5000002").boundingBox();

  daemon.updateAgent("c3d4e5000002", { state: "waiting_permission" });
  await expect(plate("c3d4e5000002")).toHaveAttribute("data-pose", "waiting");
  daemon.updateAgent("c3d4e5000002", { state: "idle" });
  await expect(plate("c3d4e5000002")).toHaveAttribute("data-pose", "idle");
  daemon.updateAgent("c3d4e5000002", { state: "exited", exit_reason: "reported_done" });
  await expect(plate("c3d4e5000002")).toHaveAttribute("data-pose", "away");
  // state changes never move an agent to another desk
  expect(await plate("c3d4e5000002").boundingBox()).toEqual(before);

  daemon.removeAgent("b2c3d4000001");
  await expect(plate("b2c3d4000001")).toHaveCount(0);
  daemon.addAgent(agent("f0f0f0000099", "worker", "starting", { started_at: new Date().toISOString() }));
  await expect(plate("f0f0f0000099")).toHaveAttribute("data-pose", "booting");
  // the newcomer takes the free desk
  daemon.addAgent(agent("f0f0f0000098", "worker", "working", { started_at: new Date().toISOString() }));
  await expect(page.locator(".nameplate")).toHaveCount(11);
  expect(errorsOf(page)).toEqual([]);
});

test("clicking a character opens its terminal", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const id = "c3d4e5000002";
  const plate = await page.locator(`.nameplate[data-agent="${id}"]`).boundingBox();
  const scale = Number(await page.locator(".office").getAttribute("data-scale"));
  if (!plate || !scale) throw new Error("no nameplate or scale");
  // The character's head is 6 world pixels right of and 20 above the nameplate's centre.
  const x = plate.x + plate.width / 2 + 6 * scale;
  const y = plate.y + plate.height / 2 - 20 * scale;
  // the canvas, not a nameplate, is under that point
  expect(await page.evaluate(([px, py]) => document.elementFromPoint(px, py)?.tagName, [x, y])).toBe("CANVAS");
  await page.mouse.move(x, y);
  await expect(page.locator(`.nameplate[data-agent="${id}"]`)).toHaveClass(/hovered/);
  await page.mouse.click(x, y);
  await expect(page.locator(".terminal-wrap")).toBeVisible();
  await expect(page.locator(".terminal-wrap")).toContainText(`mock terminal of ${id}`);
  expect(daemon.terminals).toEqual([id]);
  await page.screenshot({ path: `${screens}/terminal-after-click.png` });
});

test("nameplates open agents from the keyboard", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  await page.locator('.nameplate[data-agent="e5f6a7000004"]').focus();
  await page.keyboard.press("Enter");
  await expect(page.locator(".terminal-wrap")).toContainText("mock terminal of e5f6a7000004");
});

test("messages fly between desks", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  daemon.message(101, "b2c3d4000001", "a1ead0000001");
  daemon.message(102, undefined, "c3d4e5000002");
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${screens}/office-message.png` });
  expect(errorsOf(page)).toEqual([]);
});

for (const n of [10, 20]) {
  test(`stays smooth with ${n} agents`, async ({ page }) => {
    const daemon = new MockDaemon(team(n));
    await openOffice(page, daemon);
    await page.waitForTimeout(500);
    const stats = await page.evaluate(async () => {
      const times: number[] = [];
      await new Promise<void>((done) => {
        const start = performance.now();
        const step = (t: number): void => {
          times.push(t);
          if (t - start < 3000) requestAnimationFrame(step);
          else done();
        };
        requestAnimationFrame(step);
      });
      const gaps = times.slice(1).map((t, i) => t - times[i]).sort((a, b) => a - b);
      return {
        fps: (times.length - 1) / ((times[times.length - 1] - times[0]) / 1000),
        p95: gaps[Math.floor(gaps.length * 0.95)],
        worst: gaps[gaps.length - 1],
      };
    });
    const renderer = await page.locator(".office").getAttribute("data-renderer");
    test.info().annotations.push({ type: "frames", description: `${renderer}: ${stats.fps.toFixed(1)} fps, p95 ${stats.p95.toFixed(1)} ms, worst ${stats.worst.toFixed(1)} ms` });
    console.log(`${n} agents, ${renderer}: ${stats.fps.toFixed(1)} fps, p95 frame ${stats.p95.toFixed(1)} ms, worst ${stats.worst.toFixed(1)} ms`);
    expect(stats.fps).toBeGreaterThanOrEqual(50);
    expect(stats.p95).toBeLessThan(34);
    if (n === 20) await page.screenshot({ path: `${screens}/office-20-agents.png` });
  });
}

test("switching tabs does not leak canvases or log errors", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  for (let i = 0; i < 8; i++) {
    await page.getByRole("button", { name: "Board", exact: true }).click();
    await expect(page.locator(".board")).toBeVisible();
    await page.getByRole("button", { name: "Office", exact: true }).click();
    await expect(page.locator(".nameplate")).toHaveCount(10);
  }
  await expect(page.locator("canvas")).toHaveCount(1);
  expect(errorsOf(page)).toEqual([]);
});

test("reduced motion still shows every pose", async ({ browser }) => {
  const context = await browser.newContext({ reducedMotion: "reduce", viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  await expect(page.locator('.nameplate[data-pose="waiting"]')).toHaveCount(2);
  expect(errorsOf(page)).toEqual([]);
  await context.close();
});

test("an empty office explains itself", async ({ page }) => {
  const daemon = new MockDaemon({ agents: [], tasks: [], accessRequests: [], messages: [] });
  await daemon.install(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Office", exact: true }).click();
  await expect(page.locator(".office-bar")).toContainText("No agents yet");
  await expect(page.locator(".office")).toHaveAttribute("data-renderer", /webgl|canvas/);
  await page.screenshot({ path: `${screens}/office-empty.png` });
});

test("board screenshot for comparison", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await daemon.install(page);
  await page.goto("/");
  await expect(page.locator(".board")).toBeVisible();
  await page.screenshot({ path: `${screens}/board.png` });
});
