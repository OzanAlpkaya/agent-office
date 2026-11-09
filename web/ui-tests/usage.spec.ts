// The Usage tab, usage on cards and the resume note, against a mocked daemon (ADR 0025, 0026).
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, session, team, teamUsage } from "./daemon";

const screens = "test-results/screens";

async function open(page: Page, daemon: MockDaemon): Promise<string[]> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  await daemon.install(page);
  await page.goto("/");
  return errors;
}

test("the Usage tab totals and groups sessions, money as integers", async ({ page }) => {
  const daemon = new MockDaemon(teamUsage(team(10)));
  const errors = await open(page, daemon);
  await page.getByRole("button", { name: "Usage", exact: true }).click();
  await expect(page.getByTestId("tile-cost")).toContainText("$1.32");
  await expect(page.getByTestId("tile-cost")).toContainText("1,000 tokens without a price");
  await expect(page.getByTestId("tile-sessions")).toContainText("5");
  await expect(page.getByTestId("budgets")).toContainText("$5.00 per goal");

  // By task (default): most expensive first; the agent without a task last, unpriced.
  const rows = page.locator(".usage-table tbody tr");
  await expect(rows).toHaveCount(5);
  await expect(rows.nth(0)).toContainText("Tag model and migration");
  await expect(rows.nth(0)).toContainText("$1.23");
  await expect(rows.nth(4)).toContainText("No task");
  await expect(rows.nth(4)).toContainText("unpriced");
  await page.screenshot({ path: `${screens}/usage-by-task.png`, fullPage: true });

  await page.getByRole("tab", { name: "By goal" }).click();
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("Add tags to saved links");
  await expect(rows.nth(0)).toContainText("$1.32");

  await page.getByRole("tab", { name: "By model" }).click();
  await expect(rows.filter({ hasText: "claude-local-1" })).toContainText("no price");

  await page.getByRole("tab", { name: "By session" }).click();
  await expect(rows).toHaveCount(5);
  const host = rows.filter({ hasText: "transcript" });
  await expect(host).toContainText("$0.0001"); // agent-office's estimate
  await expect(host).toContainText("$0.0001"); // and Claude Code's own (120 micro-USD)
  await page.screenshot({ path: `${screens}/usage-by-session.png`, fullPage: true });

  // A live update moves the totals without a reload.
  const w2 = daemon.world.agents.find((a) => a.id === "c3d4e5000002");
  if (!w2) throw new Error("fixture");
  daemon.usage(session("s-w2", w2, "g00000000001", { messages: 9, cost_micro_usd: 2_000_000 }));
  await expect(page.getByTestId("tile-cost")).toContainText("$3.25");
  expect(errors).toEqual([]);
});

test("agent and task cards show their usage", async ({ page }) => {
  const daemon = new MockDaemon(teamUsage(team(10)));
  const errors = await open(page, daemon);
  const card = page.locator(".card", { hasText: "b2c3d4000001" });
  await expect(card.locator(".usage-line")).toHaveText("15.0k tokens · $1.23");
  const goal = page.locator(".task").filter({ has: page.locator(".task-title", { hasText: /^Add tags to saved links$/ }) });
  await expect(goal.locator(".usage-line")).toContainText("32.0k tokens · $0.0125");
  await expect(goal.locator(".usage-line")).toContainText("team 48.2k tokens · $1.32");
  await page.screenshot({ path: `${screens}/usage-cards.png` });
  expect(errors).toEqual([]);
});

test("an exited agent's terminal explains the resume line and resumes it", async ({ page }) => {
  const daemon = new MockDaemon(team(10));
  const errors = await open(page, daemon);
  // exited agents are listed below the running ones, collapsed (ADR 0044)
  await page.locator(".exited-agents summary").click();
  await page.locator('.card[data-agent="d0e1f2000009"]').click();
  await expect(page.getByTestId("resume-note")).toContainText("does not apply here");
  await expect(page.getByTestId("resume-note")).toContainText("Use Resume to continue it in agent-office");
  await page.screenshot({ path: `${screens}/resume-note.png` });
  await page.locator(".terminal-banner").getByRole("button", { name: "Resume" }).click();
  await expect.poll(() => daemon.requests).toContain('POST /api/agents/d0e1f2000009/resume {"prompt":""}');
  await page.locator('.card[data-agent="d0e1f2000009"]').getByRole("button", { name: "Resume" }).click();
  await expect.poll(() => daemon.requests.filter((r) => r.startsWith("POST /api/agents/d0e1f2000009/resume")).length).toBe(2);
  expect(errors).toEqual([]);
});

test("an agent from an earlier daemon run shows its saved sessions", async ({ page }) => {
  const world = team(10);
  const id = "d0e1f2000009";
  world.agents = world.agents.map((a) => a.id === id
    ? { ...a, state: "exited", has_terminal: false, exit_reason: "daemon_restart", session_id: `${id}-s2`, resumable: true }
    : a);
  const daemon = new MockDaemon(world);
  const errors = await open(page, daemon);
  await page.locator(".exited-agents summary").click();
  await page.locator(`.card[data-agent="${id}"]`).click();
  const saved = page.getByTestId("saved-terminal");
  await expect(saved.locator(".xterm-rows")).toContainText(`saved output of ${id}-s2`);
  const picker = page.getByLabel("Session");
  await expect(picker.locator("option")).toHaveCount(2);
  await picker.selectOption(`${id}-s1`);
  await expect(saved.locator(".xterm-rows")).toContainText(`saved output of ${id}-s1`);
  await page.screenshot({ path: `${screens}/saved-terminal.png` });
  await saved.getByRole("button", { name: "Resume" }).click();
  await expect.poll(() => daemon.requests).toContain(`POST /api/agents/${id}/resume {"prompt":""}`);
  expect(errors).toEqual([]);
});
