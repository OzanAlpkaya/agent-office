// Phase 16 (autonomous team) against a mocked daemon: the grouped inbox, why
// an agent exited, and approvals for the whole project.
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, type World, agent, task, openAgent } from "./daemon";

const screens = "test-results/screens";
const t = (m: number): string => `2026-11-11T10:${String(m).padStart(2, "0")}:00Z`;

function world(): World {
  const g1 = task("g00000000001", "Add tags to saved links", "in_progress");
  const g2 = task("g00000000002", "Export links", "in_progress");
  const sub = task("t00000000001", "Tags table", "in_progress", { parent_task_id: g1.id });
  return {
    tasks: [g1, g2, sub],
    agents: [
      agent("a1ead0000001", "lead", "idle", { name: "Kofi", task_id: g1.id }),
      agent("b2c3d4000001", "worker", "exited", {
        name: "Ada", task_id: sub.id, sandbox: "podman", exit_reason: "process_exit", exit_code: 137, exit_signal: "KILL",
        oom_killed: true, resumable: true,
        exit_detail: "Out of memory: the kernel killed the agent because its worker reached its 2g memory limit (exit code 137).",
      }),
      agent("c3d4e5000002", "worker", "idle", { name: "Ben", task_id: sub.id, sandbox: "podman" }),
      agent("a1ead0000002", "lead", "idle", { name: "Lena", task_id: g2.id }),
      agent("f00000000001", "assistant", "idle", { name: "Sam" }),
    ],
    accessRequests: [{ id: 4, agent_id: "c3d4e5000002", kind: "domain", value: "storage.googleapis.com",
      reason: "module zips", status: "pending", created_at: t(1), decider: "lead" }],
    messages: [],
    inbox: [
      { id: 1, kind: "question", agent_id: "a1ead0000001", role: "tech-lead", goal_id: g1.id, task_id: g1.id,
        body: "Which tag limit?", created_at: t(1), read_at: t(2), answered_at: t(3), reply: "Ten." },
      { id: 2, kind: "question", agent_id: "a1ead0000001", role: "tech-lead", goal_id: g1.id, task_id: g1.id,
        body: "May I push the branch?", created_at: t(4) },
      { id: 3, kind: "info", goal_id: g1.id, task_id: sub.id, body: "Worker Ada died again: Out of memory.", created_at: t(5), read_at: t(6) },
      { id: 4, kind: "info", agent_id: "a1ead0000002", role: "tech-lead", goal_id: g2.id, task_id: g2.id,
        body: "Export is under way.", created_at: t(7), read_at: t(7) },
      { id: 5, kind: "briefing", agent_id: "f00000000001", role: "assistant", body: "Older briefing.", created_at: t(2), read_at: t(2) },
      { id: 6, kind: "briefing", agent_id: "f00000000001", role: "assistant", body: "Latest briefing.", created_at: t(8) },
    ],
  };
}

async function open(page: Page, daemon: MockDaemon): Promise<string[]> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  await daemon.install(page);
  await page.goto("/");
  return errors;
}

test("the inbox groups by goal and agent, folds answered items and keeps briefings apart", async ({ page }) => {
  const daemon = new MockDaemon(world());
  const errors = await open(page, daemon);
  await page.getByRole("button", { name: /^Inbox/ }).click();
  const inbox = page.getByRole("region", { name: "Inbox" });
  const groups = inbox.locator(".inbox-group");
  // The goal with an open question first; the goal with only read items last.
  await expect(groups).toHaveCount(2);
  await expect(groups.nth(0)).toContainText("Goal “Add tags to saved links”");
  await expect(groups.nth(1)).toContainText("Goal “Export links”");
  const kofi = groups.nth(0).locator('.inbox-agent[data-agent="a1ead0000001"]');
  await expect(kofi.getByText("May I push the branch?")).toBeVisible();
  // The answered question is folded under "1 earlier".
  await expect(kofi.getByText("Which tag limit?")).toBeHidden();
  await kofi.locator("summary", { hasText: "1 earlier" }).click();
  await expect(kofi.getByText("Which tag limit?")).toBeVisible();
  // agent-office's own notice sits in the goal, under agent-office.
  await expect(groups.nth(0).locator('.inbox-agent[data-agent=""]')).toContainText("agent-office");
  // Briefings in their own section, the latest shown, the older folded.
  const briefings = inbox.getByLabel("Briefings");
  await expect(briefings.getByText("Latest briefing.")).toBeVisible();
  await expect(briefings.getByText("Older briefing.")).toBeHidden();
  await expect(groups.getByText("Latest briefing.")).toHaveCount(0);
  await page.screenshot({ path: `${screens}/inbox-grouped.png`, fullPage: true });
  expect(errors).toEqual([]);
});

test("an exited agent's card says why it ended", async ({ page }) => {
  const daemon = new MockDaemon(world());
  const errors = await open(page, daemon);
  // the header above its terminal says why it ended
  const card = await openAgent(page, "b2c3d4000001");
  await expect(card.getByTestId("exit-detail")).toContainText("Out of memory");
  await expect(card.getByTestId("exit-detail")).toContainText("2g memory limit");
  await expect(card).toContainText("exit code 137 · signal KILL");
  expect(errors).toEqual([]);
});

test("an access request the Tech Lead decides can be approved for the project", async ({ page }) => {
  const daemon = new MockDaemon(world());
  const errors = await open(page, daemon);
  await page.getByRole("button", { name: /^Approvals:/ }).click();
  const drawer = page.getByRole("dialog", { name: "Approvals" });
  const item = drawer.locator(".waiting-item", { hasText: "storage.googleapis.com" });
  await expect(item).toContainText("Its Tech Lead decides this one");
  await page.screenshot({ path: `${screens}/approvals-project-scope.png` });
  await item.getByRole("button", { name: "Allow for the project" }).click();
  await expect.poll(() => daemon.requests).toContain('POST /api/access-requests/4/approve {"scope":"project"}');
  expect(errors).toEqual([]);
});
