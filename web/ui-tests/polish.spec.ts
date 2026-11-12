// Phase 18 (UI and office polish) against a mocked daemon: the navigation
// and Settings page, compact cards and their modal, office layouts per
// project, the user at their desk, and the project-wide branch clean-up.
import { type BrowserContextOptions, type Page, devices, expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { MockDaemon, type World, nav, presets, projectSettings, settingsTab, task, team } from "./daemon";

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

test("settings are pages with tabs, the project's and the app's; nothing opens over the board and pushes it down", async ({ page }) => {
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
  // Two settings (Phase 18 review): the project's behind the gear by the picker, the app's in the rail.
  await page.getByRole("button", { name: /^Project settings/ }).click();
  await expect(page.locator(".project-settings-page h1")).toHaveText("linkshelf");
  await expect(page.getByRole("tablist", { name: "Project settings" }).getByRole("tab")).toHaveText(["General", "Office", "Sandbox", "Memory"]);
  for (const [tab, region] of [["General", "Branch clean-up"], ["Office", "Office layout"], ["Sandbox", "Access grants"], ["Memory", "Project memory"]]) {
    await projectSettings(page, tab as "General");
    await expect(page.getByRole("region", { name: region })).toBeVisible();
  }
  await nav(page, "Settings");
  await expect(page.getByRole("tablist", { name: "Settings" }).getByRole("tab")).toHaveText(["Projects", "Devices", "Profile"]);
  for (const [tab, region] of [["Projects", "Projects"], ["Devices", "Devices"], ["Profile", "Profile"]]) {
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
  // Emma works on it: nothing to start or delete, only the status
  await expect(m.locator(".modal-foot button")).toHaveCount(0);
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
  await projectSettings(page, "Office");
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
  await projectSettings(page, "Office");
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
  await projectSettings(page, "General");
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
    // Every settings tab fits the phone's width (Phase 18 review).
    for (const open of [() => nav(page, "Settings"), () => page.getByRole("button", { name: /^Project settings/ }).click()]) {
      await open();
      const vp = page.viewportSize();
      for (const tab of await page.getByRole("tab").all()) {
        const b = await tab.boundingBox();
        expect(b && vp && b.x >= 0 && b.x + b.width <= vp.width, await tab.textContent() ?? "").toBe(true);
      }
      await noHorizontalScroll(page);
    }
    await page.screenshot({ path: `${screens}/phone-settings.png`, fullPage: true });
    expect(errors).toEqual([]);
  });

  test("office nameplates show the name; a tap shows the task, a second opens the agent; the whole floor in view", async ({ page }) => {
    const errors = await open(page, new MockDaemon(team(10)));
    await nav(page, "Office");
    const emma = page.locator('.nameplate[data-agent="b2c3d4000001"]');
    await expect(emma).toHaveText("Emma");
    await expect(page.locator('.nameplate[data-agent="a1ead0000001"]')).toHaveText("Kofi"); // no role either
    // name-only plates do not overlap
    const boxes = await page.locator(".nameplate").evaluateAll((els) => els.map((e) => e.getBoundingClientRect().toJSON() as DOMRect));
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i], b = boxes[j];
        expect(a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom, `nameplates ${i} and ${j}`).toBe(false);
      }
    }
    await expect(emma).toHaveAttribute("aria-expanded", "false");
    await emma.tap();
    await expect(emma).toContainText("Tag model and migration");
    await expect(page.locator(".xterm")).toHaveCount(0);
    await emma.tap();
    await expect(page.locator(".xterm")).toContainText("mock terminal of b2c3d4000001");
    await nav(page, "Office");
    // the whole floor is in view: it starts at the top, nothing scrolls, and no band is left below
    await expect(page.locator(".room-label").first()).toBeVisible();
    const pane = page.locator(".office-pane");
    expect(await pane.evaluate((e) => e.scrollHeight <= e.clientHeight && e.scrollWidth <= e.clientWidth)).toBe(true);
    const drawn = await page.locator(".office").evaluate((e) => {
      const p = e.querySelector(".office-pane")?.getBoundingClientRect();
      const scale = Number((e as HTMLElement).dataset.scale);
      const labels = [...e.querySelectorAll(".room-label")].map((l) => l.getBoundingClientRect());
      return { top: p?.top ?? 0, height: p?.height ?? 0, scale, firstLabel: Math.min(...labels.map((l) => l.top)) };
    });
    expect(drawn.firstLabel - drawn.top).toBeLessThan(6 * drawn.scale); // the first room label sits at the top
    await page.screenshot({ path: `${screens}/phone-office-names.png` });
    expect(errors).toEqual([]);
  });
});

test("the office fits the whole floor in the pane; Zoom + is one step larger; the choice is remembered", async ({ page }) => {
  const errors = await open(page, new MockDaemon(team(10)));
  await nav(page, "Office");
  await expect(page.locator(".office")).toHaveAttribute("data-renderer", /webgl|canvas/);
  const zoom = page.getByRole("group", { name: "Office zoom" });
  await expect(zoom.getByRole("button", { name: "Fit" })).toHaveAttribute("aria-pressed", "true");
  const pane = page.locator(".office-pane");
  // Fit: nothing to scroll, and the drawing uses the pane's width
  const noScroll = (): Promise<boolean> => pane.evaluate((e) => e.scrollHeight <= e.clientHeight && e.scrollWidth <= e.clientWidth);
  await expect.poll(noScroll).toBe(true);
  const fit = Number(await page.locator(".office").getAttribute("data-scale"));
  expect(fit).toBeGreaterThanOrEqual(2);
  await page.screenshot({ path: `${screens}/office-fit.png` });
  // Zoom +: one step larger, and the pane scrolls
  await zoom.getByRole("button", { name: "Zoom +" }).click();
  await expect(page.locator(".office")).toHaveAttribute("data-scale", String(fit + 1));
  await expect.poll(noScroll).toBe(false);
  await page.screenshot({ path: `${screens}/office-zoom.png` });
  // remembered in this browser
  await page.reload();
  await nav(page, "Office");
  await expect(page.getByRole("group", { name: "Office zoom" }).getByRole("button", { name: "Zoom +" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".office")).toHaveAttribute("data-scale", String(fit + 1));
  await page.getByRole("group", { name: "Office zoom" }).getByRole("button", { name: "Fit" }).click();
  await expect(page.locator(".office")).toHaveAttribute("data-scale", String(fit));
  await expect.poll(noScroll).toBe(true);
  expect(errors).toEqual([]);
});

test("the avatar creator: a name, a live preview, Randomize, and Save for every floor", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  const errors = await open(page, daemon);
  await settingsTab(page, "Profile");
  const creator = page.getByTestId("avatar-creator");
  const preview = creator.getByTestId("avatar-preview");
  await expect(preview).toContainText("You"); // the default name until one is given
  // each click gives a new look
  for (let i = 0; i < 5; i++) {
    const before = await preview.getAttribute("data-look");
    await creator.getByRole("button", { name: "Randomize" }).click();
    await expect(preview).not.toHaveAttribute("data-look", before ?? "");
  }
  await creator.getByLabel("Name on your nameplate").fill("Ozan");
  await expect(preview).toContainText("Ozan");
  const look = await preview.getAttribute("data-look");
  // the preview is drawn: not an empty canvas
  const painted = await preview.locator("canvas").evaluate((c: HTMLCanvasElement) =>
    Array.from(c.getContext("2d")?.getImageData(0, 0, c.width, c.height).data ?? []).some((v, i) => i % 4 === 3 && v > 0));
  expect(painted).toBe(true);
  await page.screenshot({ path: `${screens}/avatar-creator.png` });
  expect(daemon.requests.filter((r) => r.startsWith("PUT /api/profile"))).toEqual([]); // nothing until Save
  await creator.getByRole("button", { name: "Save" }).click();
  const [skin, hair, style, shirt, pants] = (look ?? "").split("-").map(Number);
  await expect.poll(() => daemon.requests.find((r) => r.startsWith("PUT /api/profile")))
    .toBe(`PUT /api/profile ${JSON.stringify({ name: "Ozan", look: { skin, hair, style, shirt, pants } })}`);
  await expect(creator.getByRole("status")).toHaveText("Saved: you look like this on every floor.");
  // The office: the user's nameplate says the name.
  await nav(page, "Office");
  await expect(page.getByTestId("user-plate")).toHaveText("Ozan");
  await expect(page.getByTestId("user-plate")).toHaveAttribute("title", "Ozan, on Chrome on Linux");
  expect(errors).toEqual([]);
});

test("+ New project in the picker opens the add form", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  const errors = await open(page, daemon);
  await page.getByRole("combobox", { name: "Project" }).selectOption({ label: "+ New project…" });
  const dlg = page.getByRole("dialog", { name: "New project" });
  await expect(dlg.getByLabel("Repository path")).toBeFocused();
  await expect(page.getByRole("combobox", { name: "Project" })).toHaveValue("p1"); // the entry is never selected
  await dlg.getByLabel("Repository path").fill("/home/dev/api-server");
  await dlg.getByRole("button", { name: "Add project" }).click();
  await expect(dlg).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "Project" })).toHaveValue("p2");
  expect(errors).toEqual([]);
});

test("a card's modal shows only the actions that fit its state", async ({ page }) => {
  const w = team(4);
  w.tasks.push(task("g00000000002", "Add search", "todo"), task("g00000000003", "Export", "review"));
  const errors = await open(page, new MockDaemon(w));
  const actions = (name: string) => page.getByRole("dialog", { name }).locator(".modal-foot button");
  // To do, while another goal's Tech Lead runs: the starts that can run, nothing greyed out
  await page.locator("#task-g00000000002").click();
  await expect(actions("Add search")).toHaveText(["Start agent", "Start architect", "Delete…"]);
  await expect(page.getByRole("dialog", { name: "Add search" }).getByRole("note")).toContainText("One goal at a time");
  await page.keyboard.press("Escape");
  // Review: Mark done, no start buttons
  await page.locator("#task-g00000000003").click();
  await expect(actions("Export")).toHaveText(["Mark done", "Delete…"]);
  await page.keyboard.press("Escape");
  // A sub-task being worked on: nothing to start, no Delete
  await page.locator("#task-t00000000001").click();
  await expect(actions("Tag model and migration")).toHaveCount(0);
  for (const b of await page.locator(".modal-foot button").all()) await expect(b).toBeEnabled();
  expect(errors).toEqual([]);
});

