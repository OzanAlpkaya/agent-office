// Phase 19 (lessons from the game factory): the effective agent cap and the
// daemon-wide one, plans with a change request, QA rounds on the board, the
// task modal's run info and the background wait state.
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, type World, openCard, projectSettings, settingsTab, task, team } from "./daemon";
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
