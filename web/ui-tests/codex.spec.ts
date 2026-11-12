// Codex agents in the UI (ADR 0049, 0050): the agent's CLI on its card, the
// role picker naming a role's CLI, the terminal's resume note, and usage per
// vendor.
import { expect, test } from "@playwright/test";
import { MockDaemon, agent, roles, session, task, nav, openAgent } from "./daemon";

function codexTeam(): MockDaemon {
  const goal = task("g1", "Add tags", "in_progress");
  const sub = task("t1", "Tag model", "in_progress", { parent_task_id: "g1" });
  const lead = agent("a1ead0000001", "lead", "idle", { name: "Kofi", task_id: "g1" });
  const worker = agent("c0dex0000001", "worker", "working", { name: "Ada", task_id: "t1", cli: "codex", model: "" });
  const daemon = new MockDaemon({ agents: [lead, worker], tasks: [goal, sub], accessRequests: [], messages: [] });
  daemon.world.usage = [
    session("s-lead", lead, "g1", { cost_micro_usd: 12_000, tokens: { input: 100, output: 50, cache_read: 0, cache_write_5m: 0, cache_write_1h: 0 } }),
    session("s-codex", worker, "g1", { cli: "codex", models: ["gpt-6.1-sol"], unpriced_tokens: 1500,
      tokens: { input: 600, output: 100, cache_read: 800, cache_write_5m: 0, cache_write_1h: 0 } }),
  ];
  daemon.world.models = [
    { model: "claude-haiku-5-5", priced: true, messages: 1, tokens: { input: 100, output: 50, cache_read: 0, cache_write_5m: 0, cache_write_1h: 0 }, cost_micro_usd: 12_000 },
    { model: "gpt-6.1-sol", priced: false, messages: 2, tokens: { input: 600, output: 100, cache_read: 800, cache_write_5m: 0, cache_write_1h: 0 }, cost_micro_usd: 0 },
  ];
  daemon.roles = roles.map((r) => (r.name === "worker" ? { ...r, cli: "codex" as const } : r));
  return daemon;
}

test("a Codex worker shows its CLI; the role picker names a role's CLI", async ({ page }) => {
  const daemon = codexTeam();
  daemon.world.tasks.push(task("n00000000001", "Archive old links", "todo"));
  await daemon.install(page);
  await page.goto("/");
  await expect((await openAgent(page, "c0dex0000001")).locator(".cli-codex")).toHaveText("codex");
  await expect((await openAgent(page, "a1ead0000001")).locator(".cli-codex")).toHaveCount(0);
  await nav(page, "Board");
  await page.locator("#task-n00000000001").click();
  await expect(page.getByRole("combobox", { name: "Role" }).locator("option", { hasText: "worker" })).toHaveText("worker · codex");
});

test("usage shows the vendors of a team that mixes Claude Code and Codex", async ({ page }) => {
  const daemon = codexTeam();
  await daemon.install(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Usage" }).click();
  await page.getByRole("tab", { name: "Model" }).click();
  const vendors = page.getByRole("table", { name: "Usage by vendor" });
  await expect(vendors.locator('tr[data-vendor="anthropic"]')).toContainText("Anthropic (Claude Code)");
  await expect(vendors.locator('tr[data-vendor="openai"]')).toContainText("OpenAI (Codex)");
  await expect(vendors.locator('tr[data-vendor="openai"]')).toContainText("unpriced");
});
