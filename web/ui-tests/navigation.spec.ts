// Finding agents and deciding approvals with many agents (ADR 0043, 0044):
// names, the agent switcher, open tabs and the approvals drawer.
import { type BrowserContextOptions, type Page, devices, expect, test } from "@playwright/test";
import { MockDaemon, team } from "./daemon";

const screens = "test-results/screens";

async function open(page: Page, daemon: MockDaemon): Promise<string[]> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  await daemon.install(page);
  await page.goto("/");
  await expect(page.locator(".board")).toBeVisible();
  return errors;
}

const tabs = (page: Page) => page.getByRole("tablist", { name: "Open agents" }).getByRole("tab");
const switcher = (page: Page) => page.getByRole("dialog", { name: "Find an agent" });

test("agents have names, with the id as secondary text", async ({ page }) => {
  const errors = await open(page, new MockDaemon(team(10)));
  const card = page.locator('.card[data-agent="b2c3d4000001"]');
  await expect(card.locator(".agent-first")).toHaveText("Emma");
  await expect(card.locator(".agent-id")).toHaveText("b2c3d4");
  await expect(card.locator(".agent-name")).toHaveAttribute("title", "worker · Emma · b2c3d4000001");
  // the board's task cards name their agents too
  await expect(page.locator('.task-agents [data-agent="a1ead0000001"]')).toHaveText("tech-lead Kofi");
  expect(errors).toEqual([]);
});

test("the switcher finds any of 20 agents, grouped by goal, exited ones last", async ({ page }) => {
  const daemon = new MockDaemon(team(20));
  const errors = await open(page, daemon);
  await page.keyboard.press("/");
  const dialog = switcher(page);
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("searchbox", { name: "Search agents" })).toBeFocused();
  await expect(dialog.getByRole("option")).toHaveCount(20);
  const groups = await dialog.locator(".switcher-group").allTextContents();
  expect(groups[0]).toMatch(/^Goal: Add tags to saved links · \d+$/);
  expect(groups.at(-1)).toBe("Exited · 1");
  await page.screenshot({ path: `${screens}/switcher-20.png` });

  // search by task: one match, Enter opens it
  await page.keyboard.type("filter");
  await expect(dialog.getByRole("option")).toHaveCount(1);
  await expect(dialog.getByRole("option")).toContainText("Yuki");
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(page.locator(".terminal-wrap")).toContainText("mock terminal of c3d4e5000002");
  await expect(tabs(page)).toHaveCount(1);
  await expect(tabs(page).first()).toContainText("Yuki");

  // the arrow keys move through the list; an exited agent can be opened
  await page.getByRole("button", { name: /^Find an agent/ }).click();
  await page.keyboard.type("ravi");
  await page.keyboard.press("Enter");
  await expect(page.getByTestId("resume-note")).toBeVisible();

  // "/" typed into a terminal goes to the terminal, not the switcher
  await page.locator(".xterm").click();
  await page.keyboard.press("/");
  await expect(switcher(page)).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("tabs hold the agents you opened: at most six, closable, movable, kept on reload", async ({ page }) => {
  const daemon = new MockDaemon(team(20));
  const errors = await open(page, daemon);
  const pick = async (name: string): Promise<void> => {
    await page.getByRole("button", { name: /^Find an agent/ }).click();
    await page.keyboard.type(name);
    await page.keyboard.press("Enter");
    await expect(page.getByRole("tab", { name: new RegExp(`^${name}`), selected: true })).toBeVisible();
  };
  // Nothing open yet: no agent tabs, however many agents run.
  await expect(tabs(page)).toHaveCount(0);
  for (const name of ["Kofi", "Emma", "Yuki", "Omar", "Priya", "Lena"]) await pick(name);
  await expect(tabs(page)).toHaveCount(6);
  await pick("Kofi"); // used again
  await pick("Mateo"); // a seventh: Emma, the least recently used, closes
  await expect(tabs(page)).toHaveCount(6);
  expect(await tabs(page).locator(".agent-tab-name").allTextContents()).toEqual(["Kofi", "Yuki", "Omar", "Priya", "Lena", "Mateo"]);
  await page.screenshot({ path: `${screens}/tabs-6.png` });

  // Close the open tab: its right-hand neighbour opens (none: the left one).
  await page.getByRole("button", { name: "Close Mateo" }).click();
  await expect(page.getByRole("tab", { name: /^Lena/, selected: true })).toBeVisible();
  // Middle-click closes too.
  await page.getByRole("tab", { name: /^Omar/ }).click({ button: "middle" });
  await expect(tabs(page)).toHaveCount(4);

  // Move with Alt+arrow keys, and by dragging.
  await page.getByRole("tab", { name: /^Kofi/ }).focus();
  await page.keyboard.press("Alt+ArrowRight");
  expect(await tabs(page).locator(".agent-tab-name").allTextContents()).toEqual(["Yuki", "Kofi", "Priya", "Lena"]);
  await page.getByRole("tab", { name: /^Lena/ }).dragTo(page.getByRole("tab", { name: /^Yuki/ }));
  expect(await tabs(page).locator(".agent-tab-name").allTextContents()).toEqual(["Lena", "Yuki", "Kofi", "Priya"]);

  // The tabs survive a reload; a removed agent's tab closes.
  await page.reload();
  await expect(page.locator(".board")).toBeVisible();
  expect(await tabs(page).locator(".agent-tab-name").allTextContents()).toEqual(["Lena", "Yuki", "Kofi", "Priya"]);
  daemon.removeAgent("e5f6a7000004"); // Priya
  await expect(tabs(page)).toHaveCount(3);
  expect(errors).toEqual([]);
});

test("an approval request arriving moves nothing; the drawer decides it", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  const errors = await open(page, daemon);
  const badge = page.getByRole("button", { name: /^Approvals:/ });
  await expect(badge).toHaveAccessibleName("Approvals: 1 waiting");
  await expect(page).toHaveTitle("(1) agent-office");
  const before = await page.locator(".board").boundingBox();
  const header = await page.locator("header").boundingBox();

  daemon.accessRequest({ id: 2, agent_id: "c3d4e5000002", kind: "domain", value: "registry.npmjs.org",
    reason: "install the markdown linter", status: "pending", created_at: new Date().toISOString() });
  await expect(badge).toHaveAccessibleName("Approvals: 2 waiting");
  await expect(page).toHaveTitle("(2) agent-office");
  expect(await page.locator(".board").boundingBox()).toEqual(before);
  expect(await page.locator("header").boundingBox()).toEqual(header);
  await page.screenshot({ path: `${screens}/approvals-badge.png` });

  // The waiting agent's card links to the drawer.
  await page.locator('.card[data-agent="c3d4e5000002"]').getByRole("button", { name: "1 request waits for your approval" }).click();
  const drawer = page.getByRole("dialog", { name: "Approvals" });
  await expect(drawer.locator(".approvals li")).toHaveCount(2);
  await expect(drawer).toContainText("Yuki");
  await expect(drawer).toContainText("registry.npmjs.org");
  // The drawer lies over the page: the board did not move either.
  expect(await page.locator(".board").boundingBox()).toEqual(before);
  await page.screenshot({ path: `${screens}/approvals-drawer.png` });

  await drawer.locator(".approvals li", { hasText: "registry.npmjs.org" }).getByRole("button", { name: "Allow for this agent" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/access-requests/2/approve");
  daemon.accessRequest({ ...daemon.world.accessRequests.find((r) => r.id === 2)!, status: "approved" });
  await expect(drawer.locator(".approvals li")).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);
  await expect(badge).toHaveAccessibleName("Approvals: 1 waiting");
  expect(errors).toEqual([]);
});

// A phone's viewport, touch and user agent, on the suite's Chromium (ADR 0025).
const { defaultBrowserType: _, ...pixel } = devices["Pixel 7"] as BrowserContextOptions & { defaultBrowserType?: string };

test.describe("on a phone", () => {
  test.use(pixel);

  test("the switcher is a full-screen sheet and the strip shows the open agent only", async ({ page }) => {
    const daemon = new MockDaemon(team(20));
    const errors = await open(page, daemon);
    await page.getByRole("button", { name: /^Find an agent/ }).click();
    const dialog = switcher(page);
    const box = await dialog.boundingBox();
    const vp = page.viewportSize();
    expect(box && vp && Math.round(box.width) === vp.width && Math.round(box.height) === vp.height).toBe(true);
    await page.screenshot({ path: `${screens}/phone-switcher.png` });
    await dialog.getByRole("searchbox").fill("amara");
    await dialog.getByRole("option", { name: /Amara/ }).click();
    await expect(page.locator(".xterm")).toContainText("mock terminal of b8c9d0000007");
    await page.getByRole("button", { name: /^Find an agent/ }).click();
    await switcher(page).getByRole("searchbox").fill("emma");
    await switcher(page).getByRole("option", { name: /Emma/ }).click();
    await expect(tabs(page)).toHaveCount(1);
    await expect(tabs(page).first()).toContainText("Emma");
    const [sw, w] = await page.evaluate(() => [document.documentElement.scrollWidth, innerWidth]);
    expect(sw).toBeLessThanOrEqual(w);
    await page.screenshot({ path: `${screens}/phone-terminal-tab.png` });

    // Approvals open as a bottom sheet over the page.
    await page.getByRole("button", { name: "Approvals: 1 waiting" }).click();
    await expect(page.getByRole("dialog", { name: "Approvals" })).toBeVisible();
    await page.screenshot({ path: `${screens}/phone-approvals.png` });
    expect(errors).toEqual([]);
  });
});
