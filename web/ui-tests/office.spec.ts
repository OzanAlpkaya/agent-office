// The Office tab against a mocked daemon (ADR 0024, 0025).
import { type BrowserContextOptions, type Page, devices, expect, test } from "@playwright/test";
import { MockDaemon, agent, session, team } from "./daemon";

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
  // Exited agents have left the office (ADR 0024).
  await expect(page.locator(".nameplate")).toHaveCount(daemon.world.agents.filter((a) => a.state !== "exited").length);
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

interface FrameStats {
  ticks: number; renders: number; p95Ms: number; maxMs: number; left: number; walking: number; trips: number;
  busyFrames: number; busyP95Ms: number; busyMaxMs: number; figures: Record<string, { x: number; y: number }>;
}

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
  // an agent that exited before the page opened is not in the office
  await expect(pose("d0e1f2000009")).toHaveCount(0);
  await expect(page.locator(".whiteboard-label")).toHaveAttribute("data-notes", "7");
  await expect(page.locator(".office-bar")).toContainText("2 waiting for you");
  await expect(page.locator(".office-bar")).toContainText("1 left the office");
  // nameplates show names (ADR 0043), with the role for agents other than workers
  await expect(pose("a1ead0000001")).toContainText("Kofi tech-lead");
  await expect(pose("b2c3d4000001")).toContainText("Emma");
  await expect(pose("b2c3d4000001")).toHaveAttribute("title", /Emma · worker · b2c3d4000001/);
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
  // state changes never move an agent to another desk
  expect(await plate("c3d4e5000002").boundingBox()).toEqual(before);

  daemon.removeAgent("b2c3d4000001");
  await expect(plate("b2c3d4000001")).toHaveCount(0);
  daemon.addAgent(agent("f0f0f0000099", "worker", "starting", { started_at: new Date().toISOString() }));
  await expect(plate("f0f0f0000099")).toHaveAttribute("data-pose", "booting");
  // the newcomer takes the free desk
  daemon.addAgent(agent("f0f0f0000098", "worker", "working", { started_at: new Date().toISOString() }));
  await expect(page.locator(".nameplate")).toHaveCount(10);
  expect(errorsOf(page)).toEqual([]);
});

test("an exited agent walks out, and its desk is free for the next agent", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const plate = (id: string) => page.locator(`.nameplate[data-agent="${id}"]`);
  const desk = await plate("c3d4e5000002").boundingBox();
  const neighbour = await plate("d4e5f6000003").boundingBox();
  const leftBefore = (await frameStats(page, 0)).left;

  daemon.updateAgent("c3d4e5000002", { state: "exited", exit_reason: "reported_done", exited_at: new Date().toISOString() });
  // the nameplate goes at once; the character walks to the door
  await expect(plate("c3d4e5000002")).toHaveCount(0);
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${screens}/office-walk-out.png` });
  // a wider office is a longer walk to the door
  await expect.poll(async () => (await frameStats(page, 0)).left, { timeout: 10_000 }).toBe(leftBefore + 1);
  await expect(page.locator(".office-bar")).toContainText("2 left the office");
  // nobody else moved
  expect(await plate("d4e5f6000003").boundingBox()).toEqual(neighbour);

  // the next agent sits at the free desk
  daemon.addAgent(agent("f0f0f0000097", "worker", "working", { name: "Bao", started_at: new Date().toISOString() }));
  await expect(plate("f0f0f0000097")).toBeVisible();
  // nameplates are centred on their desk; their width follows the text
  const centre = (b: { x: number; y: number; width: number; height: number } | null) =>
    b && { x: Math.round(b.x + b.width / 2), y: Math.round(b.y + b.height / 2) };
  expect(centre(await plate("f0f0f0000097").boundingBox())).toEqual(centre(desk));
  await page.waitForTimeout(1200); // the walk is over
  await page.screenshot({ path: `${screens}/office-after-walk-out.png` });

  // the exited agent is still reachable from the agent list
  await page.locator(".exited-agents summary").click();
  await expect(page.locator('.exited-agents .card[data-agent="c3d4e5000002"]')).toContainText("Yuki");
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

test("a page that re-renders for something else does not redraw the office", async ({ page }) => {
  const world = team(10);
  world.agents = world.agents.map((a) => ({ ...a, state: "exited" as const }));
  world.accessRequests = [];
  const daemon = new MockDaemon(world);
  await openOffice(page, daemon);
  await frameStats(page, 500);
  const before = (await readStats(page)).renders;
  // live usage updates re-render the whole page; the office has nothing new to draw
  for (let i = 0; i < 5; i++) {
    daemon.usage(session(`s-${i}`, world.agents[1], undefined));
    await page.waitForTimeout(150);
  }
  await page.waitForTimeout(1500);
  expect((await readStats(page)).renders - before).toBeLessThanOrEqual(1);
});

test("an idle agent at a station lets the office settle", async ({ page }) => {
  test.setTimeout(60_000);
  const daemon = new MockDaemon({ agents: [agent("c9a551000012", "assistant", "idle", { name: "Rosa" })], tasks: [], accessRequests: [], messages: [] });
  await openOffice(page, daemon);
  const rosa = plate(page, "c9a551000012");
  // its one coffee, and back to reception
  await expect(rosa).toHaveAttribute("data-place", "coffee", { timeout: 15_000 });
  await expect(rosa).toHaveAttribute("data-place", "desk", { timeout: 20_000 });
  await page.waitForTimeout(1000);
  const quiet = await frameStats(page, 2000);
  expect(quiet.renders).toBeLessThanOrEqual(1);
  expect(errorsOf(page)).toEqual([]);
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
    await expect(page.locator(".nameplate")).toHaveCount(9); // the exited agent has left
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
  await expect(page.locator(".nameplate")).toHaveCount(8);
  expect(await plate("e5f6a7000004").boundingBox()).toEqual(before);
});

test("clicks hit the right agent after a resize and a new row of desks", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const widthBefore = (await page.locator(".office canvas").boundingBox())?.width ?? 0;
  await page.setViewportSize({ width: 1920, height: 1200 });
  // the office follows the pane's width (Phase 18 review)
  await expect.poll(async () => (await page.locator(".office canvas").boundingBox())?.width ?? 0).toBeGreaterThan(widthBefore + 300);
  for (let i = 0; i < 9; i++) {
    daemon.addAgent(agent(`f1${String(i).padStart(10, "0")}`, "worker", "working", { started_at: new Date(Date.now() + i).toISOString() }));
  }
  await expect(page.locator(".nameplate")).toHaveCount(18);
  for (const id of ["f10000000008", "a1ead0000001", "d4e5f6000003"]) {
    await page.getByRole("button", { name: "Office", exact: true }).click();
    await expect(page.locator(".nameplate")).toHaveCount(18);
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

// ------------------------------------------------------------ Office v2 (ADR 0045)

const plate = (page: Page, id: string) => page.locator(`.nameplate[data-agent="${id}"]`);

async function readStats(page: Page): Promise<FrameStats> {
  await expect(page.locator(".office")).toHaveAttribute("data-stats", /ticks/);
  return JSON.parse((await page.locator(".office").getAttribute("data-stats")) ?? "null") as FrameStats;
}

// figurePoint is where an away character's head is on the page.
async function figurePoint(page: Page, id: string): Promise<{ x: number; y: number }> {
  await expect.poll(async () => (await readStats(page))?.figures?.[id] !== undefined).toBe(true);
  const f = (await readStats(page)).figures[id];
  const canvas = await page.locator(".office canvas").boundingBox();
  if (!canvas) throw new Error("no canvas");
  return { x: canvas.x + f.x, y: canvas.y + f.y };
}

test("the office has rooms with a purpose", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  await expect(page.locator(".office")).toHaveAttribute("data-plan", "wide");
  const labels = page.locator(".room-label");
  await expect(labels).toHaveText(["Planning", "Tech Lead", "Your office", "QA lab", "Review room", "Kitchen", "Lobby", "Workroom"]);
  for (const l of await labels.all()) await expect(l).toHaveAttribute("title", /\w+/);
  // the labels sit inside the drawn office
  const canvas = await page.locator(".office canvas").boundingBox();
  for (const l of await labels.all()) {
    const b = await l.boundingBox();
    expect(b && canvas && b.x >= canvas.x && b.y >= canvas.y && b.x + b.width <= canvas.x + canvas.width).toBe(true);
  }
  // the reviewer at work sits in the review room from the start, without walking there
  await expect(plate(page, "e5f6a7000004")).toHaveAttribute("data-place", "review");
  await expect(plate(page, "e5f6a7000004")).toHaveAttribute("title", /In the review room/);
  expect((await frameStats(page, 0)).trips).toBe(0);
  await page.screenshot({ path: `${screens}/office-rooms.png` });
  expect(errorsOf(page)).toEqual([]);
});

test("a report sends the worker to the whiteboard, then back to its desk", async ({ page }) => {
  test.setTimeout(45_000);
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const emma = plate(page, "b2c3d4000001");
  await expect(emma).toHaveAttribute("data-place", "desk");
  daemon.updateTask("t00000000001", { status: "review" });
  await expect(emma).toHaveAttribute("data-place", "walking");
  await page.waitForTimeout(900);
  await page.screenshot({ path: `${screens}/office-walking-to-report.png` });
  await expect(emma).toHaveAttribute("data-place", "whiteboard", { timeout: 8000 });
  await expect(emma).toHaveAttribute("title", /Reporting at the whiteboard/);
  await page.screenshot({ path: `${screens}/office-report.png` });
  // the nameplate never moved
  await expect(emma).toHaveAttribute("data-place", "desk", { timeout: 15_000 });
  expect(errorsOf(page)).toEqual([]);
});

test("a worker that reports and exits reports first, then leaves by the front door", async ({ page }) => {
  test.setTimeout(45_000);
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const before = (await frameStats(page, 0)).left;
  daemon.updateTask("t00000000002", { status: "review" });
  daemon.updateAgent("c3d4e5000002", { state: "exited", exit_reason: "reported_done", exited_at: new Date().toISOString() });
  await expect(plate(page, "c3d4e5000002")).toHaveCount(0);
  // still in the office, on its way to the whiteboard
  await figurePoint(page, "c3d4e5000002");
  await page.waitForTimeout(4000);
  expect((await readStats(page)).left).toBe(before);
  await expect.poll(async () => (await readStats(page)).left, { timeout: 20_000 }).toBe(before + 1);
  expect((await readStats(page)).figures["c3d4e5000002"]).toBeUndefined();
  expect(errorsOf(page)).toEqual([]);
});

test("an idle agent takes one coffee and comes back", async ({ page }) => {
  test.setTimeout(60_000);
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const lena = plate(page, "f6a7b8000005");
  await expect(lena).toHaveAttribute("data-place", "coffee", { timeout: 15_000 });
  await expect(lena).toHaveAttribute("title", /At the coffee machine/);
  await page.screenshot({ path: `${screens}/office-coffee.png` });
  await expect(lena).toHaveAttribute("data-place", "desk", { timeout: 20_000 });
  // one coffee per idle spell
  await page.waitForTimeout(5000);
  await expect(lena).toHaveAttribute("data-place", "desk");
  expect(errorsOf(page)).toEqual([]);
});

test("agents waiting on you queue at your door, as many as the Approvals count", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const badge = page.getByRole("button", { name: /^Approvals/ });
  const sign = page.locator(".queue-sign");
  // a permission prompt and an access request: two agents, in the queue from the start
  await expect(badge).toHaveAttribute("aria-label", "Approvals: 2 waiting");
  await expect(sign).toHaveAttribute("data-waiting", "2");
  await expect(sign).toHaveText("2 waiting");
  await expect(page.locator('.nameplate[data-place="queue"]')).toHaveCount(2);
  await expect(plate(page, "d4e5f6000003")).toHaveAttribute("title", /Waiting at your door/);
  // the reviewer hits a permission prompt: it walks from the review room to the queue
  const priya = plate(page, "e5f6a7000004");
  await expect(priya).toHaveAttribute("data-place", "review");
  daemon.updateAgent("e5f6a7000004", { state: "waiting_permission" });
  await expect(priya).toHaveAttribute("data-place", "walking");
  await expect(priya).toHaveAttribute("data-place", "queue", { timeout: 8000 });
  await expect(badge).toHaveAttribute("aria-label", "Approvals: 3 waiting");
  await expect(sign).toHaveAttribute("data-waiting", "3");
  await expect(page.locator('.nameplate[data-place="queue"]')).toHaveCount(3);
  await page.screenshot({ path: `${screens}/office-queue.png` });
  // answered: it goes back to work, and the queue moves up
  daemon.updateAgent("e5f6a7000004", { state: "working" });
  await expect(sign).toHaveAttribute("data-waiting", "2");
  await expect(priya).toHaveAttribute("data-place", "review", { timeout: 10_000 });
  await expect(page.locator('.nameplate[data-place="queue"]')).toHaveCount(2);
  expect(errorsOf(page)).toEqual([]);
});

// officeTeam is team(10) with the roles that have rooms of their own (ADR 0045)
// and two roles from role packs.
function officeTeam(): ReturnType<typeof team> {
  const w = team(10);
  w.agents.push(
    agent("a9c4e1000010", "architect", "working", { name: "Ines", task_id: "g00000000001", activity: "Read" }),
    agent("b9a000000011", "qa", "working", { name: "Tomas", task_id: "g00000000001", activity: "Bash" }),
    agent("c9a551000012", "assistant", "idle", { name: "Rosa" }),
    agent("d9d0c5000013", "worker", "working", { name: "Leila", role: "docs", task_id: "t00000000005" }),
    agent("e95ec0000014", "worker", "working", { name: "Marco", role: "security", task_id: "t00000000006" }),
  );
  return w;
}

test("the architect, QA and the assistant work at their own stations", async ({ page }) => {
  const daemon = new MockDaemon(officeTeam());
  await openOffice(page, daemon);
  for (const id of ["a9c4e1000010", "b9a000000011", "c9a551000012"]) {
    await expect(plate(page, id)).toHaveAttribute("data-place", "desk");
    // a station's agent is drawn behind its furniture, and opens on a click
    await figurePoint(page, id);
  }
  expect((await frameStats(page, 0)).trips).toBe(0);
  const { x, y } = await figurePoint(page, "a9c4e1000010");
  await page.mouse.click(x, y);
  await expect(page.locator(".terminal-wrap")).toContainText("mock terminal of a9c4e1000010");
  expect(errorsOf(page)).toEqual([]);
});

test("roles from role packs sit in the workroom in a shirt of their own", async ({ page }) => {
  const daemon = new MockDaemon(officeTeam());
  await openOffice(page, daemon);
  const worker = await plate(page, "b2c3d4000001").getAttribute("data-shirt");
  const docs = await plate(page, "d9d0c5000013").getAttribute("data-shirt");
  const security = await plate(page, "e95ec0000014").getAttribute("data-shirt");
  expect(new Set([worker, docs, security]).size).toBe(3);
  await expect(plate(page, "d9d0c5000013")).toContainText("Leila docs");
  await expect(plate(page, "e95ec0000014")).toContainText("Marco security");
  // the role name has the shirt's colour
  await expect(plate(page, "d9d0c5000013").locator(".nameplate-role")).toHaveCSS("color", hexToRgb(docs ?? ""));
  expect(errorsOf(page)).toEqual([]);
});

const hexToRgb = (h: string): string => {
  const n = parseInt(h.slice(1), 16);
  return `rgb(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255})`;
};

test("a new agent walks in through the front door to its desk", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  daemon.addAgent(agent("f0f0f0000096", "worker", "starting", { name: "Bao", started_at: new Date().toISOString() }));
  const bao = plate(page, "f0f0f0000096");
  await expect(bao).toHaveAttribute("data-place", "walking");
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${screens}/office-walk-in.png` });
  await expect(bao).toHaveAttribute("data-place", "desk", { timeout: 8000 });
  expect((await readStats(page)).trips).toBeGreaterThanOrEqual(1);
  expect(errorsOf(page)).toEqual([]);
});

test("an agent resumed while it walks out turns back to its desk", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const left = (await frameStats(page, 0)).left;
  daemon.updateAgent("c3d4e5000002", { state: "exited", exited_at: new Date().toISOString() });
  await expect(plate(page, "c3d4e5000002")).toHaveCount(0);
  await page.waitForTimeout(400);
  daemon.updateAgent("c3d4e5000002", { state: "working", exited_at: undefined });
  await expect(plate(page, "c3d4e5000002")).toHaveAttribute("data-place", "desk", { timeout: 8000 });
  await page.waitForTimeout(1200);
  expect((await readStats(page)).left).toBe(left);
  expect(errorsOf(page)).toEqual([]);
});

test("a character away from its desk opens its terminal when clicked", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  const id = "e5f6a7000004"; // in the review room
  await expect(plate(page, id)).toHaveAttribute("data-place", "review");
  const { x, y } = await figurePoint(page, id);
  expect(await page.evaluate(([px, py]) => document.elementFromPoint(px, py)?.tagName, [x, y])).toBe("CANVAS");
  await page.mouse.move(x, y);
  await expect(plate(page, id)).toHaveClass(/hovered/);
  await page.mouse.click(x, y);
  await expect(page.locator(".terminal-wrap")).toContainText(`mock terminal of ${id}`);
});

test("with reduced motion characters change places without walking", async ({ browser }) => {
  const context = await browser.newContext({ reducedMotion: "reduce", viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const daemon = new MockDaemon(team(10));
  await openOffice(page, daemon);
  // every place an agent has had, from now on
  await page.evaluate(() => {
    const seen = new Set<string>();
    (window as unknown as { places: Set<string> }).places = seen;
    new MutationObserver((records) => {
      for (const r of records) seen.add((r.target as HTMLElement).dataset.place ?? "");
    }).observe(document.querySelector(".office-pane") as Node, { attributes: true, attributeFilter: ["data-place"], subtree: true });
  });
  daemon.updateTask("t00000000001", { status: "review" });
  await expect(plate(page, "b2c3d4000001")).toHaveAttribute("data-place", "whiteboard");
  daemon.updateAgent("c3d4e5000002", { state: "exited", exited_at: new Date().toISOString() });
  await expect(plate(page, "c3d4e5000002")).toHaveCount(0);
  await expect.poll(async () => (await readStats(page)).left).toBe(1);
  const places = await page.evaluate(() => [...(window as unknown as { places: Set<string> }).places]);
  expect(places).not.toContain("walking");
  expect((await readStats(page)).trips).toBe(0);
  expect(errorsOf(page)).toEqual([]);
  await context.close();
});

test("with reduced motion a return to the desk is drawn", async ({ browser }) => {
  test.setTimeout(60_000);
  const context = await browser.newContext({ reducedMotion: "reduce", viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  // One idle agent: with reduced motion nothing animates, so a frame is drawn only for a change.
  const daemon = new MockDaemon({ agents: [agent("f6a7b8000005", "worker", "idle", { name: "Lena" })], tasks: [], accessRequests: [], messages: [] });
  await openOffice(page, daemon);
  const lena = plate(page, "f6a7b8000005");
  await expect(lena).toHaveAttribute("data-place", "coffee", { timeout: 15_000 });
  await page.waitForTimeout(1500);
  const atCoffee = (await readStats(page)).renders;
  await expect(lena).toHaveAttribute("data-place", "desk", { timeout: 15_000 });
  await page.waitForTimeout(1500);
  expect((await readStats(page)).renders, "a frame drawn for the return to the desk").toBeGreaterThan(atCoffee);
  expect(errorsOf(page)).toEqual([]);
  await context.close();
});

test("a busy office: ten characters walking stay within the frame budget, then it settles", async ({ page }) => {
  test.setTimeout(60_000);
  const daemon = new MockDaemon(team(20));
  await openOffice(page, daemon);
  const present = daemon.world.agents.filter((x) => x.state !== "exited");
  expect(present).toHaveLength(19);
  const exit = (ids: string[]) => {
    for (const id of ids) daemon.updateAgent(id, { state: "exited", exited_at: new Date().toISOString() });
  };
  // Ten agents leave at once and walk out through the front door together.
  exit(present.slice(0, 10).map((a) => a.id));
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${screens}/office-busy.png` });
  await expect.poll(async () => (await readStats(page)).left, { timeout: 15_000 }).toBe(10);
  const s = await readStats(page);
  const line = `busy office, ${await page.locator(".office").getAttribute("data-renderer")}: ${s.busyFrames} frames drawn ` +
    `while ${10}+ walked, p95 ${s.busyP95Ms.toFixed(2)} ms, max ${s.busyMaxMs.toFixed(2)} ms; ${s.trips} trips, ${s.left} left`;
  test.info().annotations.push({ type: "frames", description: line });
  console.log(line);
  expect(s.busyFrames).toBeGreaterThan(15);
  expect(s.busyP95Ms).toBeLessThan(8);
  // Then the rest leave; once nobody is left and nothing moves, no more frames.
  exit(present.slice(10).map((a) => a.id));
  await expect(page.locator(".nameplate")).toHaveCount(0);
  await expect.poll(async () => (await readStats(page)).left, { timeout: 15_000 }).toBe(19);
  const quiet = await frameStats(page, 2000);
  expect(quiet.ticks).toBeGreaterThan(20);
  expect(quiet.renders).toBeLessThanOrEqual(1);
  expect(errorsOf(page)).toEqual([]);
});

// A phone's viewport, touch and user agent, on the suite's Chromium (ADR 0025).
const { defaultBrowserType: _, ...pixel } = devices["Pixel 7"] as BrowserContextOptions & { defaultBrowserType?: string };

test.describe("on a phone", () => {
  test.use(pixel);

  test("the office stacks its rooms and stays readable", async ({ page }) => {
    const daemon = new MockDaemon(team(10));
    await openOffice(page, daemon);
    await expect(page.locator(".office")).toHaveAttribute("data-plan", "stacked");
    await expect(page.locator(".room-label")).toHaveCount(8);
    // Fit: the whole floor in view (Phase 18 review); Zoom + for details
    const pane = page.locator(".office-pane");
    expect(await pane.evaluate((e) => e.scrollHeight <= e.clientHeight && e.scrollWidth <= e.clientWidth)).toBe(true);
    await page.getByRole("group", { name: "Office zoom" }).getByRole("button", { name: "Zoom +" }).click();
    await expect.poll(async () => Number(await page.locator(".office").getAttribute("data-scale"))).toBeGreaterThanOrEqual(1.5);
    expect(await pane.evaluate((e) => e.scrollWidth <= e.clientWidth)).toBe(true); // no sideways scrolling
    const boxes = await page.locator(".nameplate").evaluateAll((els) => els.map((e) => e.getBoundingClientRect().toJSON() as DOMRect));
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i], b = boxes[j];
        expect(a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom, `nameplates ${i} and ${j}`).toBe(false);
      }
    }
    const [sw, w] = await page.evaluate(() => [document.documentElement.scrollWidth, innerWidth]);
    expect(sw).toBeLessThanOrEqual(w);
    await page.screenshot({ path: `${screens}/phone-office.png` });
    // the workroom scrolls into view, and a tap on a nameplate opens the agent
    await plate(page, "b8c9d0000007").scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${screens}/phone-office-workroom.png` });
    // a tap shows the task, a second opens the agent (Phase 18 review)
    await plate(page, "b8c9d0000007").tap();
    await expect(plate(page, "b8c9d0000007")).toContainText("Validate tag names");
    await plate(page, "b8c9d0000007").tap();
    await expect(page.locator(".terminal-wrap")).toContainText("mock terminal of b8c9d0000007");
    expect(errorsOf(page)).toEqual([]);
  });
});

test("showcase: a moment in the office, for the README", async ({ page }) => {
  test.setTimeout(60_000);
  const world = officeTeam();
  // the architect's plan waits for approval: it takes the plan to the user's door
  world.agents = world.agents.map((a) => (a.id === "a9c4e1000010" ? { ...a, state: "idle" as const } : a));
  world.specs = [{
    id: 1, goal_id: "g00000000001", version: 1, status: "proposed", author: "a9c4e1000010", markdown: "", created_at: new Date().toISOString(),
    plan: { summary: "Tags", design: "", sub_tasks: [] },
  }];
  const daemon = new MockDaemon(world);
  await openOffice(page, daemon);
  // Idle agents go for their coffee; then a worker reports while they are at the counter.
  await expect(plate(page, "f6a7b8000005")).toHaveAttribute("data-place", "coffee", { timeout: 15_000 });
  daemon.updateTask("t00000000001", { status: "review" });
  await expect(plate(page, "b2c3d4000001")).toHaveAttribute("data-place", "whiteboard", { timeout: 8000 });
  daemon.addAgent(agent("f0f0f0000095", "worker", "starting", { name: "Bao", started_at: new Date().toISOString() }));
  daemon.message(201, "c3d4e5000002", "a1ead0000001");
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${screens}/office-showcase.png` });
  const canvas = await page.locator(".office canvas").boundingBox();
  if (canvas) {
    await page.screenshot({ path: `${screens}/office-rooms-closeup.png`, clip: { x: canvas.x, y: canvas.y, width: canvas.width, height: canvas.height * 0.42 } });
  }
  expect(errorsOf(page)).toEqual([]);
});
