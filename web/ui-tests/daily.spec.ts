// Phase 17 (daily use) against a mocked daemon: the goal picker and archive,
// closing a goal, Merge and Push with their confirmations, revoking access
// grants, and Claude Code's background requests shown quietly.
import { type Page, expect, test } from "@playwright/test";
import type { GoalRecord, MergePreview, QARun } from "../src/api";
import { MockDaemon, type World, agent, task } from "./daemon";

const screens = "test-results/screens";
const t = (m: number): string => `2026-11-11T10:${String(m).padStart(2, "0")}:00Z`;
const qaHead = "1111111111111111111111111111111111111111";
const mainHead = "2222222222222222222222222222222222222222";

function world(): World {
  const active = task("g00000000001", "Add tags to saved links", "review");
  const other = task("g00000000002", "Export links", "in_progress");
  const old = task("g00000000003", "Sprint 3: search", "done", { updated_at: t(5) });
  const older = task("g00000000004", "Sprint 2: login", "done", { updated_at: t(1) });
  const qa: QARun = { id: 1, goal_id: active.id, status: "passed", branch: "agent-office/qa-tags-a1b2c3", head: qaHead,
    merged: [], summary: "all green", bugs: [], created_at: t(2), updated_at: t(3) };
  const oldQA: QARun = { ...qa, id: 2, goal_id: old.id, branch: "agent-office/qa-search-c3d4e5", summary: "search works" };
  const preview: MergePreview = {
    goal_id: active.id, qa_run_id: 1, branch: qa.branch, source_head: qaHead, target: "main", target_head: mainHead,
    message: "feat: tags for links\n\nAdds a tags table and a filter.", files: 4, untested: 2, checked_out: true, ready: true,
    paths: [".github/workflows/ci.yml", "internal/tags/tags.go", "internal/tags/tags_test.go", "migrations/0003_tags.sql"],
    sensitive: [".github/workflows/ci.yml"],
    remote: "origin", remote_url: "github-personal:OzanAlpkaya/pulsego.git",
  };
  const record: GoalRecord = {
    goal: old,
    sub_tasks: [task("t00000000031", "Search index", "done", { parent_task_id: old.id })],
    agents: [
      { id: "a1ead0000003", name: "Kofi", role: "tech-lead", kind: "lead", cli: "claude", model: "haiku", task_id: old.id,
        branch: "agent-office/a1ead0000003", state: "exited", exit_reason: "goal_done", started_at: t(0), removed: true },
      { id: "b2c3d4000003", name: "Ada", role: "worker", kind: "worker", cli: "codex", task_id: "t00000000031",
        branch: "agent-office/search-index-b2c3d4", state: "exited",
        exit_detail: "Out of memory: the kernel killed the agent because its worker reached 2g.", started_at: t(0), removed: false },
    ],
    qa_runs: [oldQA],
    report: { summary: "Search shipped.", commit_message: "feat: search\n\nFull-text search over links.", created_at: t(4) },
    merges: [],
  };
  return {
    tasks: [active, other, old, older,
      task("t00000000001", "Tags table", "review", { parent_task_id: active.id }),
      task("t00000000002", "Export CSV", "in_progress", { parent_task_id: other.id }),
      task("t00000000031", "Search index", "done", { parent_task_id: old.id })],
    agents: [
      agent("a1ead0000001", "lead", "idle", { name: "Lena", task_id: active.id }),
      agent("a1ead0000002", "lead", "working", { name: "Omar", task_id: other.id }),
      agent("c3d4e5000002", "worker", "working", { name: "Ben", task_id: "t00000000002", sandbox: "podman" }),
      agent("b2c3d4000003", "worker", "exited", { name: "Ada", task_id: "t00000000031" }),
    ],
    accessRequests: [], messages: [],
    qaRuns: [qa, oldQA],
    previews: { [active.id]: preview },
    records: { [old.id]: record },
    grants: [
      { id: 1, host: "storage.googleapis.com", scope: "project", request_id: 4, created_at: t(1), decided_by: "user" },
      { id: 2, host: "docs.example.com", scope: "agent", agent_id: "c3d4e5000002", request_id: 5, created_at: t(2),
        decided_by: "lead", decider_agent_id: "a1ead0000002", decision_reason: "API docs" },
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

const card = (page: Page, id: string) => page.locator(`#task-${id}`);

test("the board shows active goals, one goal on request, and done goals in the archive", async ({ page }) => {
  const daemon = new MockDaemon(world());
  const errors = await open(page, daemon);
  await expect(card(page, "g00000000001")).toBeVisible();
  await expect(card(page, "g00000000002")).toBeVisible();
  await expect(card(page, "t00000000002")).toBeVisible();
  await expect(card(page, "g00000000003")).toHaveCount(0); // done: archived
  await expect(card(page, "t00000000031")).toHaveCount(0);
  const picker = page.getByLabel("Goal");
  await expect(picker).toContainText("All active goals (2)");
  await picker.selectOption({ label: "Export links" });
  await expect(card(page, "g00000000001")).toHaveCount(0);
  await expect(card(page, "t00000000002")).toBeVisible();
  await page.reload(); // remembered per project in this browser
  await expect(page.getByLabel("Goal")).toHaveValue("g00000000002");
  await page.getByLabel("Goal").selectOption("all");

  await page.getByRole("button", { name: "Archive (2)" }).click();
  const archive = page.getByTestId("archive");
  await expect(archive.locator("li")).toHaveCount(2);
  await expect(archive.locator("li").first()).toContainText("Sprint 3: search"); // most recently closed first
  await expect(archive.locator("li").first()).toContainText("QA passed");
  await archive.getByRole("button", { name: /Sprint 3: search/ }).click();
  const rec = page.getByTestId("goal-record");
  await expect(rec.getByText("Search shipped.")).toBeVisible();
  await expect(rec.getByText("Full-text search over links.", { exact: false })).toBeVisible();
  await expect(rec.getByText("Search index")).toBeVisible();
  await expect(rec.getByText("agent-office/search-index-b2c3d4")).toBeVisible();
  await expect(rec.locator("tr", { hasText: "Kofi" })).toContainText("removed");
  await expect(rec.locator("tr", { hasText: "Ada" })).toContainText("Out of memory");
  await expect(rec.locator("tr", { hasText: "Ada" })).toContainText("codex");
  await expect(rec.getByText("search works")).toBeVisible();
  await page.screenshot({ path: `${screens}/goal-archive.png`, fullPage: true });

  // One action removes the done goal's exited agents.
  await rec.getByRole("button", { name: "Remove 1 exited agent" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/tasks/g00000000003/remove-agents");
  await rec.getByRole("button", { name: "Reopen" }).click();
  await expect.poll(() => daemon.requests).toContain('PATCH /api/tasks/g00000000003 {"status":"review"}');
  expect(errors).toEqual([]);
});

test("marking a goal done asks first while its agents run", async ({ page }) => {
  const daemon = new MockDaemon(world());
  const errors = await open(page, daemon);
  const c = card(page, "g00000000002");
  await c.locator("select").first().selectOption("done");
  const confirm = c.getByRole("dialog", { name: "Mark the goal done" });
  await expect(confirm).toContainText("2 agents of this goal are still working");
  await expect(confirm).toContainText("sub-tasks are marked done too");
  await confirm.getByRole("button", { name: "Cancel" }).click();
  expect(daemon.requests.filter((r) => r.startsWith("PATCH"))).toEqual([]);
  await c.locator("select").first().selectOption("done");
  await c.getByRole("button", { name: "Mark done and stop them" }).click();
  await expect.poll(() => daemon.requests).toContain('PATCH /api/tasks/g00000000002 {"status":"done"}');
  // A goal whose only agent is its idle Tech Lead is marked done at once.
  await card(page, "g00000000001").locator("select").first().selectOption("done");
  await expect.poll(() => daemon.requests).toContain('PATCH /api/tasks/g00000000001 {"status":"done"}');
  expect(errors).toEqual([]);
});

test("Merge and Push run after a confirmation that shows the branch, the target and the message", async ({ page }) => {
  const daemon = new MockDaemon(world());
  const errors = await open(page, daemon);
  const c = card(page, "g00000000001");
  await expect(card(page, "g00000000002").getByRole("button", { name: "Merge…" })).toHaveCount(0); // not reported
  await c.getByRole("button", { name: "Merge…" }).click();
  const dlg = c.getByRole("dialog", { name: "Confirm the merge" });
  await expect(dlg).toContainText("agent-office/qa-tags-a1b2c3");
  await expect(dlg).toContainText("at 11111111 (QA passed)");
  await expect(dlg).toContainText("main at 22222222");
  await expect(dlg).toContainText("4 files; main is checked out");
  await expect(dlg.getByText("main has 2 commits QA did not test with", { exact: false })).toBeVisible();
  await expect(dlg.getByTestId("merge-sensitive")).toContainText(".github/workflows/ci.yml");
  await dlg.getByText("Changed files").click();
  await expect(dlg.getByText("migrations/0003_tags.sql")).toBeVisible();
  const msg = dlg.getByLabel("Commit message");
  await expect(msg).toHaveValue("feat: tags for links\n\nAdds a tags table and a filter.");
  await page.screenshot({ path: `${screens}/merge-confirm.png`, fullPage: true });
  expect(daemon.requests.filter((r) => r.includes("/merge"))).toEqual([]); // nothing until confirmed
  await msg.fill("feat: tags\n\nEdited.");
  await dlg.getByRole("button", { name: "Merge into main" }).click();
  await expect.poll(() => daemon.requests).toContain(
    `POST /api/tasks/g00000000001/merge {"target_head":"${mainHead}","source_head":"${qaHead}","message":"feat: tags\\n\\nEdited."}`);
  await expect(c).toContainText("Merged into main as abcdef01");
  await expect(c).toContainText("not pushed");
  await expect(c.getByRole("button", { name: "Merge…" })).toHaveCount(0);

  await c.getByRole("button", { name: "Push…" }).click();
  const push = c.getByRole("dialog", { name: "Confirm the push" });
  await expect(push).toContainText("origin (github-personal:OzanAlpkaya/pulsego.git)");
  await expect(push).toContainText("abcdef01 to main (the merge only");
  await expect(push).toContainText("feat: tags");
  expect(daemon.requests.filter((r) => r.includes("/push"))).toEqual([]);
  await push.getByRole("button", { name: "Push to origin" }).click();
  await expect.poll(() => daemon.requests).toContain("POST /api/tasks/g00000000001/push");
  await expect(c).toContainText("pushed to origin");
  await expect(c.getByRole("button", { name: "Push…" })).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("a refused merge says why", async ({ page }) => {
  const w = world();
  const pv = w.previews?.g00000000001;
  if (!pv) throw new Error("fixture");
  const daemon = new MockDaemon(w);
  const errors = await open(page, daemon);
  const c = card(page, "g00000000001");
  // Refused in the preview: no Merge button in the confirmation.
  w.previews = { g00000000001: { ...pv, ready: false, reason: "main is checked out with uncommitted changes: commit or stash them first" } };
  await c.getByRole("button", { name: "Merge…" }).click();
  await expect(c.getByTestId("merge-refused")).toContainText("uncommitted changes");
  await expect(c.getByRole("button", { name: "Merge into main" })).toHaveCount(0);
  await c.getByRole("button", { name: "Close" }).click();
  // Refused at the click: the target moved since the confirmation.
  w.previews = { g00000000001: pv };
  daemon.mergeRefusal = "merge refused: main moved since the confirmation (now at 33333333): review it again";
  await c.getByRole("button", { name: "Merge…" }).click();
  await c.getByRole("button", { name: "Merge into main" }).click();
  await expect(c.getByRole("alert")).toContainText("main moved since the confirmation");
  await expect(c.getByRole("dialog", { name: "Confirm the merge" })).toBeVisible();
  // The failed request is the only error in the console.
  expect(errors.filter((e) => !e.includes("409"))).toEqual([]);
});

test("access grants can be revoked, and background requests are shown quietly", async ({ page }) => {
  const w = world();
  const daemon = new MockDaemon(w);
  const errors = await open(page, daemon);
  w.events = {}; // set before the page loads the recorded events
  await page.locator(".badge.pack").click();
  const grants = page.getByTestId("grants");
  await expect(grants.locator("li")).toHaveCount(2);
  await expect(grants.locator("li").first()).toContainText("storage.googleapis.com");
  await expect(grants.locator("li").first()).toContainText("every agent of the project");
  await expect(grants.locator("li").nth(1)).toContainText("approved by the Tech Lead");
  await grants.locator("li").first().getByRole("button", { name: "Revoke" }).click();
  await expect.poll(() => daemon.requests).toContain("DELETE /api/access-grants/1");
  await expect(grants.locator("li")).toHaveCount(1);
  await page.screenshot({ path: `${screens}/grants.png`, fullPage: true });
  expect(errors).toEqual([]);
});

test("Claude Code's background requests are apart from sandbox problems, and quiet", async ({ page }) => {
  const w = world();
  w.events = { c3d4e5000002: [
    { kind: "egress_background", detail: "downloads.claude.ai:443", at: t(9) },
    { kind: "egress_denied", detail: "github.com:443", at: t(8) },
    { kind: "api_background", detail: "POST /v1/messages/count_tokens", at: t(7) },
  ] };
  const daemon = new MockDaemon(w);
  const errors = await open(page, daemon);
  const ben = page.locator(".card", { hasText: "Ben" }).first();
  await expect(ben.getByText("1 sandbox event · latest: Network blocked")).toBeVisible();
  const quiet = ben.getByTestId("background-events");
  await expect(quiet).toHaveText("2 background requests of Claude Code refused (expected)");
  await expect(quiet).toHaveClass(/muted/);
  await quiet.click();
  await expect(ben.getByText("downloads.claude.ai:443")).toBeVisible();
  expect(errors).toEqual([]);
});
