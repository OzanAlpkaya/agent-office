// The team workflow UI (Phase 12, ADR 0029-0034) against a mocked daemon:
// the inbox, plan approval, dependencies and conflicts on task cards, QA and
// project memory.
import { type Page, expect, test } from "@playwright/test";
import type { Spec } from "../src/api";
import { MockDaemon, type World, agent, task, team, nav, settingsTab } from "./daemon";

const screens = "test-results/screens";
const at = "2026-11-08T10:05:00Z";

function workflow(): World {
  const goal = task("g00000000001", "Add tags to saved links", "in_progress");
  const sub = (id: string, title: string, status: "todo" | "in_progress" | "review", over = {}) =>
    task(id, title, status, { parent_task_id: goal.id, ...over });
  const tasks = [
    goal,
    sub("t00000000001", "Tags table", "review", { spec_key: "schema", files: ["internal/store/"], acceptance: ["tags are stored"] }),
    sub("t00000000003", "Tag API", "in_progress", { spec_key: "api" }),
    sub("t00000000002", "Tag filter", "todo", { spec_key: "filter", depends_on: ["t00000000001", "t00000000003"] }),
    sub("t00000000004", "Tag CLI", "review", {
      conflicts: [{ branch: "origin/main", files: ["cmd/main.go"] },
        { with: "t00000000001", branch: "agent-office/tags-table-b2c3d4", files: ["internal/store/store.go"] }],
    }),
  ];
  const spec: Spec = {
    id: 1, goal_id: goal.id, version: 2, status: "proposed", author: "a7c4e1000001", created_at: at, markdown: "# Add tags",
    plan: {
      summary: "Users can tag links and filter by tag.", design: "A tags table and a ?tag= filter.",
      sub_tasks: [
        { key: "schema", title: "Tags table", description: "Add the table.", files: ["internal/store/"] },
        { key: "filter", title: "Tag filter", description: "Filter by tag.", depends_on: ["schema"], acceptance: ["?tag=x filters"] },
      ],
      risks: "Migrations.",
    },
  };
  return {
    tasks,
    agents: [
      agent("a7c4e1000001", "architect", "idle", { name: "Ines", task_id: goal.id }),
      agent("a1ead0000001", "lead", "idle", { task_id: goal.id }),
      agent("b2c3d4000001", "worker", "working", { task_id: "t00000000003" }),
      agent("c3d4e5000002", "worker", "exited", { task_id: "t00000000004", exit_reason: "reported_done" }),
      agent("d4e5f6000003", "worker", "idle", { role: "docs", task_id: "t00000000001" }),
    ],
    accessRequests: [],
    messages: [],
    specs: [spec],
    inbox: [
      { id: 2, kind: "plan", body: "The architect's plan (version 2) is ready for your approval.", created_at: at, goal_id: goal.id, task_id: goal.id },
      { id: 1, kind: "question", agent_id: "a7c4e1000001", role: "architect", task_id: goal.id, goal_id: goal.id,
        body: "Should tags be case-insensitive?", created_at: at },
    ],
    qaRuns: [{
      id: 1, goal_id: goal.id, status: "failed", branch: "agent-office/qa-add-tags-e5f6a7", merged: [], created_at: at, updated_at: at,
      summary: "The filter ignores case.", bugs: [{ title: "Filter is case-sensitive", description: "?tag=Go misses go", bug_task_id: "t00000000009" }],
    }],
    memory: [{ id: 1, kind: "convention", title: "Errors are wrapped", body: "Wrap errors with %w.", status: "proposed",
      source: "f00000000001", roles: ["worker"], paths: ["internal/store/"], created_at: at, updated_at: at }],
  };
}

// card is the compact task card whose title is title; detail opens its modal (Phase 18).
const card = (page: Page, title: string) =>
  page.locator("button.task").filter({ has: page.locator(".task-title").getByText(title, { exact: true }) });
async function detail(page: Page, title: string) {
  await card(page, title).click();
  const m = page.getByRole("dialog", { name: title, exact: true });
  await expect(m).toBeVisible();
  return m;
}

async function open(page: Page, daemon: MockDaemon): Promise<string[]> {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  await daemon.install(page);
  await page.goto("/");
  return errors;
}

test("the inbox shows the team's questions and sends the answer", async ({ page }) => {
  const daemon = new MockDaemon(workflow());
  const errors = await open(page, daemon);
  const button = page.getByRole("button", { name: /^Inbox/ });
  await expect(button).toHaveAccessibleName("Inbox: 2 unread");
  await button.click();
  const inbox = page.getByRole("region", { name: "Inbox" });
  await expect(inbox).toContainText("Should tags be case-insensitive?");
  // the sender's name, with its short id as secondary text (ADR 0043)
  await expect(inbox).toContainText(/architect Ines\s*a7c4e1/);
  await inbox.getByLabel("Answer").fill("Yes, lower-case them.");
  await inbox.getByRole("button", { name: "Send" }).click();
  await expect.poll(() => daemon.requests).toContain('POST /api/inbox/1/reply {"body":"Yes, lower-case them."}');
  // A live item arrives and is counted.
  daemon.inboxItem({ id: 3, kind: "briefing", agent_id: "f00000000001", role: "assistant", body: "All quiet.", created_at: at });
  await expect(button).toHaveAccessibleName("Inbox: 3 unread");
  await expect(inbox.getByLabel("Ask the assistant")).toBeVisible();
  await page.screenshot({ path: `${screens}/team-inbox.png`, fullPage: true });
  expect(errors).toEqual([]);
});

test("the plan notice in the inbox reviews or approves the plan directly", async ({ page }) => {
  const daemon = new MockDaemon(workflow());
  const errors = await open(page, daemon);
  await page.getByRole("button", { name: /^Inbox/ }).click();
  const notice = page.getByRole("region", { name: "Inbox" }).locator("li.inbox-item.kind-plan");
  await expect(notice).toContainText("ready for your approval");
  // Review plan opens the goal's card, its plan open.
  await notice.getByRole("button", { name: "Review plan" }).click();
  const goal = page.getByRole("dialog", { name: "Add tags to saved links" });
  await expect(goal).toBeVisible();
  await expect(goal.locator("details.plan")).toHaveAttribute("open", "");
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: /^Inbox/ }).click();
  await notice.getByRole("button", { name: "Approve plan v2" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/specs/1/approve");
  await expect.poll(() => daemon.requests).toContain("POST /api/inbox/2/read");
  await page.screenshot({ path: `${screens}/team-inbox-plan.png`, fullPage: true });
  expect(errors).toEqual([]);
});

test("a proposed plan is shown on the goal and approved by the user", async ({ page }) => {
  const daemon = new MockDaemon(workflow());
  const errors = await open(page, daemon);
  await expect(card(page, "Add tags to saved links").getByTestId("card-status")).toHaveText("Plan to approve");
  const goal = await detail(page, "Add tags to saved links");
  const plan = goal.locator("details.plan");
  await expect(plan).toContainText(/Plan v2 · waiting for your approval · 2 sub-tasks · by architect Ines\s*a7c4e1/);
  await expect(plan).toContainText("Users can tag links and filter by tag.");
  await expect(plan).toContainText("after schema");
  await expect(plan).toContainText("?tag=x filters");
  await expect(goal.getByRole("button", { name: "Start architect" })).toBeDisabled(); // one is running
  await page.screenshot({ path: `${screens}/team-plan.png`, fullPage: true });
  await plan.getByRole("button", { name: "Edit" }).click();
  await plan.getByLabel("Title").first().fill("Tags table and index");
  await plan.getByRole("button", { name: "Save as a new version" }).click();
  await expect.poll(() => daemon.requests.find((r) => r.startsWith("PUT /api/specs/1"))).toContain('"title":"Tags table and index"');
  await plan.getByRole("button", { name: "Approve plan" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/specs/1/approve");
  expect(errors).toEqual([]);
});

test("task cards show dependencies, conflicts, QA and their roles", async ({ page }) => {
  const daemon = new MockDaemon(workflow());
  const errors = await open(page, daemon);
  // The cards say it in one chip; the modal has the detail.
  await expect(card(page, "Tag filter").getByTestId("card-status")).toHaveText("Waits on 1");
  await expect(card(page, "Add tags to saved links").getByTestId("card-status")).toHaveText("Plan to approve");
  await page.screenshot({ path: `${screens}/team-board.png`, fullPage: true });
  let m = await detail(page, "Tag filter");
  await expect(m).toContainText("Waits for “Tag API”");
  // Roles: the worker roles are offered when starting an agent.
  await expect(m.getByLabel("Role")).toContainText("docs");
  await page.keyboard.press("Escape");
  m = await detail(page, "Tag CLI");
  await expect(m).toContainText("Conflicts with the base branch origin/main in cmd/main.go");
  await expect(m).toContainText("Conflicts with “Tags table” (agent-office/tags-table-b2c3d4) in internal/store/store.go");
  await m.getByRole("button", { name: "Resolve conflict" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/tasks/t00000000004/resolve-conflict");
  await page.keyboard.press("Escape");
  m = await detail(page, "Tags table");
  await expect(m).toContainText("internal/store/");
  await expect(m).toContainText("1 acceptance criterion");
  await page.keyboard.press("Escape");
  m = await detail(page, "Add tags to saved links");
  await expect(m.locator("details.qa")).toContainText("QA failed run 1");
  await expect(m.locator("details.qa")).toContainText("1 bug");
  await page.keyboard.press("Escape");
  // names and roles in the rail
  await expect(page.locator('.agent-row[data-agent="d4e5f6000003"]')).toContainText("docs");
  await expect(page.locator('.agent-row[data-agent="a1ead0000001"]')).toContainText("tech-lead");
  expect(errors).toEqual([]);
});

test("project memory proposals wait for the user", async ({ page }) => {
  const daemon = new MockDaemon(workflow());
  const errors = await open(page, daemon);
  // A proposal is counted on Settings; the memory is a tab there (Phase 18).
  await expect(page.getByRole("navigation", { name: "Main" }).getByRole("button", { name: /^Settings/ }))
    .toHaveAccessibleName("Settings (1 memory note proposed)");
  await settingsTab(page, "Memory");
  const panel = page.getByRole("region", { name: "Project memory" });
  await expect(panel).toContainText("Errors are wrapped");
  await expect(panel).toContainText("proposed by f00000");
  await expect(panel.getByTestId("memory-scope")).toHaveText("For roles worker · files internal/store/");
  await expect(panel.getByRole("link", { name: "Export as Markdown" })).toHaveAttribute("href", "/api/memory/export");
  await panel.getByRole("button", { name: "Edit" }).click();
  await panel.getByLabel("Only for roles").fill("worker, qa");
  await panel.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => daemon.requests.find((r) => r.startsWith("PATCH /api/memory/1")))
    .toContain('"roles":["worker","qa"],"paths":["internal/store/"]');
  await panel.getByRole("button", { name: "Cancel" }).click();
  await panel.getByRole("button", { name: "Accept" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/memory/1/accept");
  daemon.memoryEntry({ ...workflow().memory![0], status: "accepted" });
  await expect(page.getByRole("navigation", { name: "Main" }).getByRole("button", { name: /^Settings/ })).toHaveAccessibleName("Settings");
  await expect(panel.getByRole("button", { name: "Accept" })).toHaveCount(0);
  // Briefing is asked for at the top of the inbox.
  await page.getByRole("button", { name: /^Inbox/ }).click();
  await page.getByRole("dialog", { name: "Inbox" }).getByRole("button", { name: "Briefing" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/assistant/briefing");
  expect(errors.filter((e) => !e.includes("404"))).toEqual([]);
});

test("a question and a plan waiting for you are in Approvals, and the architect queues at your door with its plan", async ({ page }) => {
  const daemon = new MockDaemon(workflow());
  const errors = await open(page, daemon);
  // one agent, the architect, waits on two things: its question and its plan (ADR 0044)
  const badge = page.getByRole("button", { name: /^Approvals/ });
  await expect(badge).toHaveAttribute("aria-label", "Approvals: 1 waiting");
  await expect(page).toHaveTitle("(1) agent-office");
  await badge.click();
  const drawer = page.getByRole("dialog", { name: "Approvals" });
  const entry = drawer.locator('.waiting-entry[data-agent="a7c4e1000001"]');
  await expect(entry).toContainText("Asks you: Should tags be case-insensitive?");
  await expect(entry).toContainText("Its plan for “Add tags to saved links” waits for your approval (version 2).");
  await page.screenshot({ path: `${screens}/approvals-question-plan.png` });
  await entry.getByRole("button", { name: "Answer in the inbox" }).click();
  await expect(page.getByRole("region", { name: "Inbox" })).toContainText("Should tags be case-insensitive?");
  await page.keyboard.press("Escape"); // the inbox drawer lies over the page
  await badge.click();
  await page.getByRole("dialog", { name: "Approvals" }).getByRole("button", { name: "Review plan" }).click();
  await expect(page.getByRole("dialog", { name: "Add tags to saved links" }).locator("details.plan")).toHaveAttribute("open", "");
  await page.keyboard.press("Escape");
  // the Office: the architect stands in the queue at the user's door
  await page.getByRole("button", { name: "Office", exact: true }).click();
  await expect(page.locator('.nameplate[data-agent="a7c4e1000001"]')).toHaveAttribute("data-place", "queue");
  await expect(page.locator(".queue-sign")).toHaveAttribute("data-waiting", "1");
  expect(errors).toEqual([]);
});

test("a second goal says why its Tech Lead cannot start yet, and opens the running one", async ({ page }) => {
  const world = team(10);
  world.tasks.push(task("g00000000002", "Add search", "todo"));
  const daemon = new MockDaemon(world);
  await daemon.install(page);
  await page.goto("/");
  await page.locator("#task-g00000000002").click();
  const card = page.getByRole("dialog", { name: "Add search" });
  await expect(card.getByRole("button", { name: "Start Tech Lead" })).toBeDisabled();
  const note = card.getByRole("note");
  await expect(note).toContainText("One goal at a time in this project: the Tech Lead of “Add tags to saved links” is still running");
  await expect(note).toContainText("mark that goal done or stop its Tech Lead");
  await note.getByRole("button", { name: "Open it" }).click();
  await expect(page.getByRole("tab", { name: /Kofi/ })).toHaveAttribute("aria-selected", "true");
  // The goal with the running lead has no such note: its own lead is on it.
  await nav(page, "Board");
  await page.locator("#task-g00000000001").click();
  await expect(page.getByRole("dialog", { name: "Add tags to saved links" }).getByRole("note")).toHaveCount(0);
});
