// Phase 19 (lessons from the game factory): the effective agent cap and the
// daemon-wide one, plans with a change request, QA rounds on the board, the
// task modal's run info and the background wait state.
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, type World, agent, openCard, projectSettings, session, settingsTab, task, team } from "./daemon";
import { workflow } from "./workflow";

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

test("the effective agent cap is shown and the daemon-wide cap changes without a restart", async ({ page }) => {
  const w: World = team(4);
  const daemon = new MockDaemon(w);
  const errors = await open(page, daemon);
  const running = w.agents.filter((a) => a.state !== "exited").length;
  await expect(page.getByTestId("running-cap")).toHaveText(`${running} / 4 running`);
  await projectSettings(page, "General");
  await expect(page.getByTestId("cap-note")).toHaveText(
    "Effective: 4 (the smaller of this project's 4 and the daemon's 8). The Tech Lead, Architect and QA count too.");
  await expect(page.getByRole("region", { name: "General" })).toContainText("apply to the next agent start");
  // A setting that needs the agents stopped says so before saving.
  await page.getByRole("textbox", { name: "Model" }).fill("opus");
  await expect(page.getByTestId("needs-stop")).toHaveCount(0);
  await page.getByRole("textbox", { name: "Stack pack" }).fill("node");
  await expect(page.getByTestId("needs-stop")).toContainText("pack changes what the agents run with");

  // The daemon-wide cap, in the app's settings.
  await settingsTab(page, "Projects");
  const cap = page.getByRole("form", { name: "Daemon-wide agent cap" });
  await expect(cap).toContainText("From --max-agents");
  await cap.getByRole("textbox", { name: "Max agents (all projects)" }).fill("40");
  await cap.getByRole("button", { name: "Save" }).click();
  await expect(cap.getByRole("alert")).toContainText("between 1 and 32");
  await cap.getByRole("textbox", { name: "Max agents (all projects)" }).fill("3");
  await cap.getByRole("button", { name: "Save" }).click();
  await expect(cap.getByRole("status")).toHaveText("Saved.");
  expect(daemon.requests).toContain('PUT /api/daemon {"max_agents":3}');
  await expect(cap).toContainText("Set here; --max-agents is 8");
  await expect(page.getByTestId("running-cap")).toHaveText(`${running} / 3 running`);
  await cap.getByRole("button", { name: "Use --max-agents" }).click();
  await expect(cap).toContainText("From --max-agents");
  expect(daemon.requests).toContain('PUT /api/daemon {"max_agents":null}');
  await expect(page.getByTestId("running-cap")).toHaveText(`${running} / 4 running`);
  expect(errors).toEqual([]);
});

test("a superseded sub-task is closed and links to what replaced it", async ({ page }) => {
  const w: World = team(4);
  w.tasks.push(task("t00000000008", "Tag model, first try", "done",
    { parent_task_id: "g00000000001", superseded_by: "t00000000001", created_by: "a1ead0000001" }));
  const errors = await open(page, new MockDaemon(w));
  const card = page.locator("#task-t00000000008");
  await expect(card.getByTestId("card-status")).toHaveText("Superseded");
  await expect(card.getByTestId("card-status")).toHaveAttribute("title", "Replaced by “Tag model and migration”");
  const modal = await openCard(page, "t00000000008");
  await expect(modal.getByTestId("superseded")).toContainText("Replaced by “Tag model and migration”");
  await modal.getByTestId("superseded").getByRole("button", { name: "Open it" }).click();
  await expect(page.locator(".modal.task-detail .detail-title")).toHaveText("Tag model and migration");
  expect(errors).toEqual([]);
});

test("a plan is changed by request, by a one-line edit, or approved in one click", async ({ page }) => {
  const daemon = new MockDaemon(workflow());
  const errors = await open(page, daemon);
  const goal = await openCard(page, "g00000000001");
  const plan = goal.locator("details.plan");
  await expect(plan.getByRole("button", { name: "Approve plan" })).toBeVisible();
  // No big form: each sub-task has its own Edit and Remove.
  await expect(plan.getByRole("textbox")).toHaveCount(1); // only the change request box
  await plan.getByRole("textbox", { name: "What to change" }).fill("Split the API sub-task in two.");
  await plan.getByRole("button", { name: "Send to the Architect" }).click();
  await expect.poll(() => daemon.requests).toContain('POST /api/specs/1/changes {"changes":"Split the API sub-task in two."}');
  await expect(plan.getByTestId("change-pending")).toContainText("Split the API sub-task in two.");
  await expect(plan.getByRole("textbox", { name: "What to change" })).toHaveValue("");
  // Remove asks once, then saves a version without it and without the dependency on it.
  await plan.getByRole("button", { name: "Remove schema" }).click();
  await plan.getByRole("button", { name: "Keep" }).click();
  await plan.getByRole("button", { name: "Remove schema" }).click();
  await plan.getByRole("button", { name: "Remove", exact: true }).click();
  await expect.poll(() => daemon.requests.find((r) => r.startsWith("PUT /api/specs/1"))).toBeDefined();
  const put = daemon.requests.find((r) => r.startsWith("PUT /api/specs/1")) ?? "";
  expect(put).not.toContain('"key":"schema"');
  expect(put).not.toContain('"depends_on":["schema"]');
  // The summary has its own small editor.
  await plan.getByRole("button", { name: "Edit the summary" }).click();
  await expect(plan.getByRole("form", { name: "Edit the summary" })).toBeVisible();
  await plan.getByRole("button", { name: "Cancel" }).click();
  await plan.getByRole("button", { name: "Approve plan" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/specs/1/approve");
  expect(errors).toEqual([]);
});

test("each QA round is a card with its result, its bug sub-tasks and its cost", async ({ page }) => {
  const w = team(4);
  const goal = "g00000000001";
  w.tasks.push(task("t00000000009", "Bug: Filter is case-sensitive", "todo", { parent_task_id: goal, created_by: "e5f6a7000099" }));
  w.agents.push(agent("e5f6a7000099", "qa", "exited", { name: "Quinn", task_id: goal, exit_reason: "reported_done" }),
    agent("e5f6a7000098", "qa", "working", { name: "Remy", task_id: goal }));
  w.usage = [session("s-qa1", w.agents[w.agents.length - 2], goal, { cost_micro_usd: 120_000 })];
  w.qaRuns = [
    { id: 2, goal_id: goal, agent_id: "e5f6a7000098", status: "running", branch: "agent-office/qa-2", merged: [], bugs: [],
      created_at: "2026-11-08T11:00:00Z", updated_at: "2026-11-08T11:00:00Z" },
    { id: 1, goal_id: goal, agent_id: "e5f6a7000099", status: "failed", branch: "agent-office/qa-1", head: "abcdef0123456789",
      merged: [{ task_id: "t00000000001", title: "Tag model and migration", branch: "agent-office/port-t00000-1234abcd", head: "1111111111",
        merge: "2222222222", port: true }],
      summary: "The filter ignores case.", bugs: [{ title: "Filter is case-sensitive", description: "?tag=Go misses go", bug_task_id: "t00000000009" }],
      created_at: "2026-11-08T10:00:00Z", updated_at: "2026-11-08T10:30:00Z" },
  ];
  const errors = await open(page, new MockDaemon(w));
  const first = page.locator("#qa-1");
  await expect(page.locator('section[data-status="review"]').locator("#qa-1")).toBeVisible();
  await expect(page.locator('section[data-status="in_progress"]').locator("#qa-2")).toContainText("QA round 2");
  await expect(first).toContainText("QA round 1");
  await expect(first.getByTestId("card-status")).toHaveText("Failed · 1 bug");
  await expect(first).toContainText("Quinn");
  await expect(first).toContainText("$0.12");
  await first.click();
  const modal = page.locator(".modal.qa-detail");
  await expect(modal).toContainText("The filter ignores case.");
  await expect(modal).toContainText("ported");
  await expect(modal).toContainText("integration branch agent-office/qa-1 @ abcdef01");
  await modal.getByTestId("bug-task").click();
  await expect(page.locator(".modal.task-detail .detail-title")).toHaveText("Bug: Filter is case-sensitive");
  expect(errors).toEqual([]);
});

test("the task modal is wider and shows each agent's run", async ({ page }) => {
  const w = team(4);
  const t1 = "t00000000001";
  const worker = w.agents.find((a) => a.task_id === t1);
  if (!worker) throw new Error("fixture: no worker on t1");
  w.usage = [session("s-w1", worker, "g00000000001", { cost_micro_usd: 340_000,
    tokens: { input: 1200, output: 3400, cache_read: 50_000, cache_write_5m: 2000, cache_write_1h: 0 } })];
  w.runs = { [t1]: [{
    agent_id: worker.id, name: worker.name, kind: "worker", role: "worker", model: "haiku", cli: "claude", state: "exited",
    started_at: "2026-11-08T10:02:00Z", exited_at: "2026-11-08T10:41:00Z", exit_reason: "reported_done",
    working_seconds: 1500, waiting_seconds: 840, branch: "agent-office/tag-model-b2c3d4", base: "1111",
    last_commit: { sha: "abcdef0123456789abcdef0123456789abcdef01", subject: "feat: tags table", at: "2026-11-08T10:40:00Z" },
    changes: { files: 3, added: 120, removed: 7 },
    sessions: [{ id: "s1", started_at: "2026-11-08T10:02:00Z", ended_at: "2026-11-08T10:20:00Z", exit_reason: "stopped", turns: 4, tool_calls: 9 },
      { id: "s2", started_at: "2026-11-08T10:25:00Z", ended_at: "2026-11-08T10:41:00Z", resumed_from: "s1", exit_reason: "reported_done", turns: 3, tool_calls: 5 }],
    reports: [{ summary: "done", verification: "make check: pass (42 tests)", at: "2026-11-08T10:40:30Z" }],
  }] };
  const errors = await open(page, new MockDaemon(w));
  const modal = await openCard(page, t1);
  const box = await modal.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThan(1000);
  const run = modal.locator(".run").first();
  await expect(run).toContainText("haiku");
  await expect(run.getByTestId("run-cost")).toHaveText("$0.3400");
  await expect(run.getByTestId("run-duration")).toContainText("25m working · 14m waiting · ended: reported done");
  await expect(run).toContainText("abcdef01 feat: tags table");
  await expect(run.getByTestId("run-changes")).toHaveText("3 files · +120 −7");
  await expect(run.getByTestId("run-tokens")).toContainText("cache read");
  await expect(run.locator(".run-sessions li")).toHaveCount(2);
  await expect(run.locator(".run-sessions li").nth(1)).toContainText("resumed");
  await expect(run.getByTestId("run-verification")).toHaveText("make check: pass (42 tests)");
  expect(errors).toEqual([]);
});

test("an agent whose background command still runs is not shown as idle", async ({ page }) => {
  const w = team(4);
  const daemon = new MockDaemon(w);
  const errors = await open(page, daemon);
  const id = "b2c3d4000001";
  daemon.updateAgent(id, { state: "waiting_background", activity: "background task" });
  await expect(page.locator(`#task-t00000000001`).getByTestId("card-status")).toHaveText("Background task");
  await expect(page.locator(`.agent-row[data-agent="${id}"]`)).toContainText(/background/i);
  daemon.updateAgent(id, { state: "idle", activity: undefined });
  await expect(page.locator(`#task-t00000000001`).getByTestId("card-status")).not.toHaveText("Background task");
  expect(errors).toEqual([]);
});

test("from a remote device the daemon-wide cap is shown but changed on this machine only", async ({ page }) => {
  const daemon = new MockDaemon(team(4));
  daemon.remote = true;
  const errors = await open(page, daemon);
  await settingsTab(page, "Projects");
  const cap = page.getByRole("form", { name: "Daemon-wide agent cap" });
  await expect(cap.getByRole("textbox", { name: "Max agents (all projects)" })).toBeDisabled();
  await expect(cap.getByTestId("cap-hint")).toContainText("Changed on this machine only");
  await expect(cap.getByRole("button", { name: "Save" })).toHaveCount(0);
  // A project's Max agents stays changeable from here.
  await projectSettings(page, "General");
  await expect(page.getByRole("textbox", { name: "Max agents" })).toBeEnabled();
  expect(errors).toEqual([]);
});
