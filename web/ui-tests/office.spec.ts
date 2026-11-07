// The Office tab against a mocked daemon (ADR 0024, 0025).
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, agent, team } from "./daemon";

const screens = "test-results/screens";

async function openOffice(page: Page, daemon: MockDaemon): Promise<void> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => {
    // Errors, and Chrome's warning when WebGL contexts leak ("Too many active WebGL contexts").
    if (m.type() === "error" || /too many active webgl contexts|context lost/i.test(m.text())) errors.push(m.text());
  });
  (page as Page & { errors?: string[] }).errors = errors;
  await daemon.install(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Office", exact: true }).click();
  await expect(page.locator(".office")).toHaveAttribute("data-renderer", /webgl|canvas/);
  await expect(page.locator(".nameplate")).toHaveCount(daemon.world.agents.length);
}

const errorsOf = (page: Page): string[] => (page as Page & { errors?: string[] }).errors ?? [];

// characterPoint is where an agent's head is on screen: 6 world pixels right
// of and 20 above the centre of its nameplate.
async function characterPoint(page: Page, id: string): Promise<{ x: number; y: number }> {
  // The office pane scrolls when it is short; bring the desk into view first.
  await page.locator(`.nameplate[data-agent="${id}"]`).scrollIntoViewIfNeeded();
  const plate = await page.locator(`.nameplate[data-agent="${id}"]`).boundingBox();
  const scale = Number(await page.locator(".office").getAttribute("data-scale"));
  if (!plate || !scale) throw new Error(`no nameplate or scale for ${id}`);
  return { x: plate.x + plate.width / 2 + 6 * scale, y: plate.y + plate.height / 2 - 20 * scale };
}

interface FrameStats { ticks: number; renders: number; p95Ms: number; maxMs: number }

// frameStats waits for the scene's statistics to cover at least ms more.
async function frameStats(page: Page, ms: number): Promise<FrameStats> {
  const read = async (): Promise<FrameStats> =>
    JSON.parse((await page.locator(".office").getAttribute("data-stats")) ?? "null") as FrameStats;
  await expect(page.locator(".office")).toHaveAttribute("data-stats", /ticks/);
  const before = await read();
  await page.waitForTimeout(ms);
  await expect.poll(async () => (await read()).ticks).toBeGreaterThan(before.ticks);
  const after = await read();
  return { ...after, ticks: after.ticks - before.ticks, renders: after.renders - before.renders };
}

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
  const { x, y } = await characterPoint(page, id);
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
    // The scene's own cost per drawn frame (tick and render), measured inside the app.
    const scene = await frameStats(page, 3000);
    // And the page as a whole: requestAnimationFrame keeps its pace.
    const pageFps = await page.evaluate(async () => {
      const start = performance.now();
      let frames = 0;
      await new Promise<void>((done) => {
        const step = (t: number): void => {
          frames++;
          if (t - start < 2000) requestAnimationFrame(step);
          else done();
        };
        requestAnimationFrame(step);
      });
      return frames / ((performance.now() - start) / 1000);
    });
    const renderer = await page.locator(".office").getAttribute("data-renderer");
    const line = `${n} agents, ${renderer}: scene drew ${scene.renders} of ${scene.ticks} ticks in ~3 s, ` +
      `p95 ${scene.p95Ms.toFixed(2)} ms, max ${scene.maxMs.toFixed(2)} ms per frame; page ${pageFps.toFixed(0)} fps`;
    test.info().annotations.push({ type: "frames", description: line });
    console.log(line);
    expect(scene.renders).toBeGreaterThan(20); // animating, at most 30 fps
    expect(scene.ticks).toBeLessThanOrEqual(3 * 30 + 15);
    expect(scene.p95Ms).toBeLessThan(8); // half a 60 Hz frame
    expect(pageFps).toBeGreaterThan(30); // smoke check, generous for loaded machines
    if (n === 20) await page.screenshot({ path: `${screens}/office-20-agents.png` });
  });
}

test("an office where nothing moves does not redraw", async ({ page }) => {
  const world = team(10);
  world.agents = world.agents.map((a) => ({ ...a, state: "exited" as const }));
  world.accessRequests = [];
  const daemon = new MockDaemon(world);
  await openOffice(page, daemon);
  const stats = await frameStats(page, 2000);
  expect(stats.ticks).toBeGreaterThan(20);
  expect(stats.renders).toBeLessThanOrEqual(1);
});

test("switching tabs does not leak canvases or log errors", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  // Chrome allows about 16 live WebGL contexts; a leak would show up as its
  // warning (collected as an error) or a lost context after 20 round trips.
  for (let i = 0; i < 20; i++) {
    await page.getByRole("button", { name: "Board", exact: true }).click();
    await expect(page.locator(".board")).toBeVisible();
    await page.getByRole("button", { name: "Office", exact: true }).click();
    await expect(page.locator(".nameplate")).toHaveCount(10);
  }
  await expect(page.locator("canvas")).toHaveCount(1);
  await expect(page.locator(".office")).toHaveAttribute("data-renderer", "webgl");
  const id = "c3d4e5000002";
  const { x, y } = await characterPoint(page, id);
  await page.mouse.click(x, y);
  await expect(page.locator(".terminal-wrap")).toContainText(`mock terminal of ${id}`);
  expect(errorsOf(page)).toEqual([]);
});

test("desks stay put when the tab is left and reopened", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const plate = (id: string) => page.locator(`.nameplate[data-agent="${id}"]`);
  const before = await plate("e5f6a7000004").boundingBox();
  daemon.removeAgent("c3d4e5000002"); // frees a desk in front of e5f6a7
  await expect(plate("c3d4e5000002")).toHaveCount(0);
  await page.getByRole("button", { name: "Board", exact: true }).click();
  await page.getByRole("button", { name: "Office", exact: true }).click();
  await expect(page.locator(".nameplate")).toHaveCount(9);
  expect(await plate("e5f6a7000004").boundingBox()).toEqual(before);
});

test("clicks hit the right agent after a resize and a new row of desks", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const scaleBefore = await page.locator(".office").getAttribute("data-scale");
  await page.setViewportSize({ width: 1100, height: 760 });
  await expect(page.locator(".office")).not.toHaveAttribute("data-scale", scaleBefore ?? "");
  for (let i = 0; i < 9; i++) {
    daemon.addAgent(agent(`f1${String(i).padStart(10, "0")}`, "worker", "working", { started_at: new Date(Date.now() + i).toISOString() }));
  }
  await expect(page.locator(".nameplate")).toHaveCount(19);
  for (const id of ["f10000000008", "a1ead0000001", "d4e5f6000003"]) {
    await page.getByRole("button", { name: "Office", exact: true }).click();
    await expect(page.locator(".nameplate")).toHaveCount(19);
    const { x, y } = await characterPoint(page, id);
    expect(await page.evaluate(([px, py]) => document.elementFromPoint(px, py)?.tagName, [x, y])).toBe("CANVAS");
    await page.mouse.click(x, y);
    await expect(page.locator(".terminal-wrap")).toContainText(`mock terminal of ${id}`);
  }
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
