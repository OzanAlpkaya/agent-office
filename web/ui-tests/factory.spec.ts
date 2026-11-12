// Phase 19 (lessons from the game factory): the effective agent cap and the
// daemon-wide one, plans with a change request, QA rounds on the board, the
// task modal's run info and the background wait state.
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, type World, openCard, projectSettings, settingsTab, task, team } from "./daemon";

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
