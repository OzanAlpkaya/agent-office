// Phase 18 (UI and office polish) against a mocked daemon: the navigation
// and Settings page, compact cards and their modal, office layouts per
// project, the user at their desk, and the project-wide branch clean-up.
import { type BrowserContextOptions, type Page, devices, expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { MockDaemon, type World, nav, presets, settingsTab, team } from "./daemon";

const screens = "test-results/screens";

async function open(page: Page, daemon: MockDaemon): Promise<string[]> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  await daemon.install(page);
  await page.goto("/");
  await expect(page.locator(".board")).toBeVisible();
  return errors;
}

const noHorizontalScroll = async (page: Page): Promise<void> => {
  const [sw, w] = await page.evaluate(() => [document.documentElement.scrollWidth, innerWidth]);
  expect(sw).toBeLessThanOrEqual(w);
};

test("settings live on one page with tabs; nothing opens over the board and pushes it down", async ({ page }) => {
  const errors = await open(page, new MockDaemon(team(10)));
  const board = await page.locator(".board").boundingBox();
  // The inbox is a drawer over the page: the board does not move.
  await page.getByRole("button", { name: /^Inbox/ }).click();
  await expect(page.getByRole("dialog", { name: "Inbox" })).toBeVisible();
  expect(await page.locator(".board").boundingBox()).toEqual(board);
  await page.keyboard.press("Escape");
  // No panel buttons in the top bar any more: Memory, Devices and Projects are Settings tabs.
  const top = page.locator("header.topbar");
  for (const name of ["Memory", "Devices", "Projects", "Briefing"]) await expect(top.getByRole("button", { name, exact: true })).toHaveCount(0);
  await nav(page, "Settings");
  const tabs = page.getByRole("tablist", { name: "Settings" }).getByRole("tab");
  await expect(tabs).toHaveText(["Project", "Projects", "Sandbox", "Memory", "Devices"]);
  await expect(page.getByRole("tab", { name: "Project", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("region", { name: "Office layout" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Branch clean-up" })).toBeVisible();
  for (const [tab, region] of [["Projects", "Projects"], ["Sandbox", "Access grants"], ["Memory", "Project memory"], ["Devices", "Devices"]]) {
    await settingsTab(page, tab as "Projects");
    await expect(page.getByRole("region", { name: region })).toBeVisible();
  }
  // Approvals and Inbox stay in the top bar on every view.
  for (const view of ["Board", "Office", "Usage", "Settings"] as const) {
    await nav(page, view);
    await expect(top.getByRole("button", { name: /^Approvals/ })).toBeVisible();
    await expect(top.getByRole("button", { name: /^Inbox/ })).toBeVisible();
  }
  expect(errors).toEqual([]);
});

test("a compact card shows only its title, status, agent and cost; the modal has the rest", async ({ page }) => {
  const errors = await open(page, new MockDaemon(team(10)));
  const card = page.locator("#task-t00000000001");
  await expect(card.locator(".task-title")).toHaveText("Tag model and migration");
  await expect(card.getByTestId("card-status")).toHaveText("Working");
  await expect(card.locator(".task-agent")).toHaveText("Emma");
  // nothing else: no buttons, selects or descriptions on the card
  await expect(card.locator("button, select, .task-desc")).toHaveCount(0);
  await expect(page.locator("#task-t00000000007 [data-testid=card-status]")).toHaveText("Starting");
  await expect(page.locator("#task-t00000000004 [data-testid=card-status]")).toHaveText("Done");
  await card.click();
  const m = page.getByRole("dialog", { name: "Tag model and migration" });
  await expect(m.getByTestId("detail-status")).toHaveText("Working");
  await expect(m.locator(".detail-meta")).toContainText("“Add tags to saved links”");
  await expect(m.locator(".detail-agents")).toContainText("Emma");
  for (const name of ["Start agent", "Delete…"]) await expect(m.getByRole("button", { name })).toBeVisible();
  await expect(m.getByLabel("Status")).toHaveValue("in_progress");
  // An agent in the modal opens its terminal, and the modal closes.
  await m.locator('.detail-agent[data-agent="b2c3d4000001"]').click();
  await expect(m).toHaveCount(0);
  await expect(page.locator(".xterm")).toContainText("mock terminal of b2c3d4000001");
  // New task is a modal too.
  await nav(page, "Board");
  await page.getByRole("button", { name: "New task" }).click();
  await page.getByRole("dialog", { name: "New task" }).getByLabel("Title").fill("Archive old links");
  await page.getByRole("dialog", { name: "New task" }).getByRole("button", { name: "Add task" }).click();
  await expect(page.getByRole("dialog", { name: "New task" })).toHaveCount(0);
  await expect(page.locator("button.task", { hasText: "Archive old links" }).getByTestId("card-status")).toHaveText("To do");
  expect(errors).toEqual([]);
});

test("the office layout is chosen from a preset, checked as you type, previewed and saved", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  const p1 = daemon.projects[0].project;
  daemon.projects[0].project = { ...p1, effective: { ...p1.effective, max_agents: 10 } };
  const errors = await open(page, daemon);
  await nav(page, "Settings");
  const layout = page.getByTestId("layout-settings");
  await expect(layout.getByRole("button", { name: "Default office" })).toHaveAttribute("aria-pressed", "true");
  await layout.getByRole("button", { name: "Own layout" }).click();
  await layout.getByRole("button", { name: "Compact" }).click();
  const yaml = layout.getByLabel("Office layout YAML");
  await expect(yaml).toHaveValue(presets[1].yaml);
  await expect(layout.getByTestId("layout-summary")).toHaveText("5 rooms · 6 desks · 15 × 25 tiles · without planning, qa, review");
  await expect(layout.getByTestId("layout-preview")).toHaveAttribute("data-valid", "true");
  await expect(layout.getByRole("img", { name: /Floor plan: Tech Lead, Your office, Kitchen, Lobby, Workroom/ })).toBeVisible();
  // 10 agents may run, 6 desks: a warning, not an error
  await expect(layout.getByRole("note")).toContainText("may be too few");
  await page.screenshot({ path: `${screens}/layout-settings.png`, fullPage: true });
  // Something invalid: its problems, no preview, no Save.
  await yaml.fill("version: 1\nrooms: []\n");
  await expect(layout.getByTestId("layout-problems")).toContainText("room lead is missing");
  await expect(layout.getByTestId("layout-preview")).toHaveAttribute("data-valid", "false");
  await expect(layout.getByRole("button", { name: "Save layout" })).toBeDisabled();
  await layout.getByRole("button", { name: "Open plan" }).click();
  await layout.getByRole("button", { name: "Save layout" }).click();
  await expect.poll(() => daemon.requests.find((r) => r.startsWith("PATCH /api/projects/p1"))).toContain('"office_layout":"# Open plan');
  await expect(layout.getByRole("status")).toHaveText("Saved: the office uses it now.");
  // The office now has the layout's rooms: no stacking, its own names.
  await nav(page, "Office");
  await expect(page.locator(".office")).toHaveAttribute("data-plan", "custom");
  await expect(page.locator(".room-label")).toHaveCount(8);
  await expect(page.locator('.room-label[data-room="work"]')).toHaveText("Open office");
  await expect(page.locator(".nameplate")).toHaveCount(9);
  await page.screenshot({ path: `${screens}/office-open-plan.png` });
  // Back to the default office.
  await nav(page, "Settings");
  await layout.getByRole("button", { name: "Default office" }).click();
  await layout.getByRole("button", { name: "Save layout" }).click();
  await expect.poll(() => daemon.requests.filter((r) => r.startsWith("PATCH /api/projects/p1")).length).toBe(2);
  expect(daemon.requests.filter((r) => r.startsWith("PATCH /api/projects/p1"))[1]).not.toContain("office_layout");
  await nav(page, "Office");
  await expect(page.locator(".office")).toHaveAttribute("data-plan", /wide|stacked/);
  expect(errors).toEqual([]);
});

test("with more agents than desks, the rest are listed in the office bar until a desk frees up", async ({ page }) => {
  const w = team(10);
  const daemon = new MockDaemon(w);
  daemon.projects[0].project = { ...daemon.projects[0].project, settings: { office_layout: presets[1].yaml },
    office: { source: "custom", layout: JSON.parse(JSON.stringify(presetLayout("compact"))) } };
  const errors = await open(page, daemon);
  await nav(page, "Office");
  await expect(page.locator(".office")).toHaveAttribute("data-plan", "custom");
  // compact: no planning room or QA lab, 6 desks; the lead and 8 others are at work, one of them exited
  const deskless = page.getByTestId("deskless");
  await expect(deskless).toContainText("2 without a desk");
  await expect(deskless.getByRole("button")).toHaveText(["Amara", "Jisoo"]);
  await page.screenshot({ path: `${screens}/office-compact-deskless.png` });
  // Emma leaves: Amara, who waited longest, gets her desk.
  daemon.updateAgent("b2c3d4000001", { state: "exited" });
  await expect(deskless.getByRole("button")).toHaveText(["Jisoo"]);
  await expect(page.locator('.nameplate[data-agent="b8c9d0000007"]')).toBeVisible();
  await deskless.getByRole("button", { name: "Jisoo" }).click();
  await expect(page.locator(".xterm")).toContainText("mock terminal of c9d0e1000008");
  expect(errors).toEqual([]);
});

// presetLayout is a preset as the daemon parses it.
function presetLayout(name: string): unknown {
  return (JSON.parse(readFileSync(new URL("../src/office/presets.gen.json", import.meta.url), "utf8")) as Record<string, unknown>)[name];
}

test("a stored layout that does not validate shows the default office with a warning", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  daemon.projects[0].project = { ...daemon.projects[0].project, settings: { office_layout: "version: 1\nrooms: []\n" },
    office: { source: "default", problems: ["room lead is missing: every office has the Tech Lead's desk and whiteboard"] } };
  const errors = await open(page, daemon);
  await nav(page, "Office");
  const warn = page.getByTestId("layout-warning");
  await expect(warn).toContainText("not valid, so the default office is shown");
  await expect(warn).toContainText("room lead is missing");
  await expect(page.locator(".office")).toHaveAttribute("data-plan", /wide|stacked/);
  await page.screenshot({ path: `${screens}/office-layout-warning.png` });
  await warn.getByRole("button", { name: "Fix it in the project settings" }).click();
  await expect(page.getByTestId("layout-settings")).toContainText("The stored layout is not valid");
  expect(errors).toEqual([]);
});

test("the user sits at their desk while a device has the UI open and visible", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  const errors = await open(page, daemon);
  await nav(page, "Office");
  // This page told the daemon it is visible: the user is at their desk.
  await expect.poll(() => daemon.visibility).toEqual([true]);
  await expect(page.locator(".office")).toHaveAttribute("data-user-present", "true");
  await expect(page.getByTestId("user-plate")).toHaveAttribute("title", "You, on Chrome on Linux");
  await page.screenshot({ path: `${screens}/office-user-present.png` });
  // The page is hidden (another tab, a locked phone): the desk is empty.
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect.poll(() => daemon.visibility).toEqual([true, false]);
  await expect(page.locator(".office")).toHaveAttribute("data-user-present", "false");
  await expect(page.getByTestId("user-plate")).toHaveCount(0);
  // Another device opens the UI: the user is back, on that device.
  daemon.presence(["Pixel 9"]);
  await expect(page.getByTestId("user-plate")).toHaveAttribute("title", "You, on Pixel 9");
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect(page.getByTestId("user-plate")).toHaveAttribute("title", "You, on Chrome on Linux and Pixel 9");
  expect(errors).toEqual([]);
});

test("old merged agent-office branches of a project are cleaned up from the settings", async ({ page }) => {
  const w: World = team(4);
  w.branches = [
    { name: "agent-office/old-login-a1b2c3", head: "1".repeat(40), class: "merged", ahead: 0 },
    { name: "agent-office/search-box-d4e5f6", head: "2".repeat(40), class: "squashed", ahead: 3 },
    { name: "agent-office/spike-cache-0a0b0c", head: "3".repeat(40), class: "unmerged", ahead: 2 },
    { name: "agent-office/tag-model-b2c3d4", head: "4".repeat(40), class: "unmerged", ahead: 1, in_use: "its agent Emma is running" },
  ];
  const daemon = new MockDaemon(w);
  const errors = await open(page, daemon);
  await nav(page, "Settings");
  const section = page.getByRole("region", { name: "Branch clean-up" });
  await section.getByRole("button", { name: "Clean up branches…" }).click();
  const clean = section.getByRole("dialog", { name: "Clean up branches" });
  await expect(clean.locator(".branch-item")).toHaveCount(4);
  await expect(clean.getByRole("checkbox", { name: "Delete agent-office/tag-model-b2c3d4" })).toBeDisabled();
  await expect(clean).toContainText("kept: its agent Emma is running");
  await clean.getByRole("checkbox", { name: "Delete agent-office/spike-cache-0a0b0c" }).check();
  await clean.getByRole("button", { name: "Delete 3 branches" }).click();
  await expect(clean.getByTestId("confirm-unmerged")).toContainText("agent-office/spike-cache-0a0b0c");
  await page.screenshot({ path: `${screens}/branch-cleanup-project.png`, fullPage: true });
  await clean.getByRole("button", { name: "Yes, delete 3 including the unmerged" }).click();
  await expect.poll(() => daemon.requests.find((r) => r.includes("/api/branches/delete"))).toContain('"unmerged":["agent-office/spike-cache-0a0b0c"]');
  await expect(section.getByTestId("branch-result")).toHaveText("Deleted 3 branches.");
  expect(errors).toEqual([]);
});

// A phone's viewport, touch and user agent, on the suite's Chromium (ADR 0025).
const { defaultBrowserType: _, ...pixel } = devices["Pixel 7"] as BrowserContextOptions & { defaultBrowserType?: string };

test.describe("on a phone", () => {
  test.use(pixel);

  test("a bottom tab bar, the badges in the top bar, no sideways scrolling, a card as a full-screen sheet", async ({ page }) => {
    const errors = await open(page, new MockDaemon(team(10)));
    const bar = page.getByRole("navigation", { name: "Main" });
    await expect(bar.getByRole("button")).toHaveText(["Board", "Office", "Agents9", "Usage", "Settings"]);
    await expect(page.getByRole("button", { name: "Approvals: 2 waiting" })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Inbox/ })).toBeVisible();
    await noHorizontalScroll(page);
    await page.locator("#task-t00000000001").click();
    const m = page.getByRole("dialog", { name: "Tag model and migration" });
    const box = await m.boundingBox();
    const vp = page.viewportSize();
    expect(box && vp && Math.round(box.width) === vp.width && Math.round(box.height) === vp.height).toBe(true);
    await noHorizontalScroll(page);
    await page.keyboard.press("Escape");
    for (const view of ["Office", "Usage", "Settings"] as const) {
      await nav(page, view);
      await noHorizontalScroll(page);
    }
    await page.screenshot({ path: `${screens}/phone-settings.png`, fullPage: true });
    expect(errors).toEqual([]);
  });
});
