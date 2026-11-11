// Several projects in one UI (ADR 0046, 0048): the switcher, the Approvals
// badge across projects, the Projects panel, the office's floors and usage.
import { type Page, expect, test } from "@playwright/test";
import { MockDaemon, type World, agent, project, session, task, team, teamUsage } from "./daemon";

const iso = (minutes: number): string => new Date(Date.parse("2026-11-08T10:00:00Z") + minutes * 60_000).toISOString();

// webapp is a second project: a Tech Lead, a worker asking for network access.
function webapp(): World {
  const goal = task("w00000000001", "Add a login page", "in_progress");
  return {
    agents: [
      agent("aaaa00000001", "lead", "idle", { name: "Noor", task_id: goal.id }),
      agent("bbbb00000002", "worker", "working", { name: "Theo", task_id: goal.id, activity: "Edit" }),
    ],
    tasks: [goal],
    accessRequests: [{ id: 7, agent_id: "bbbb00000002", kind: "domain", value: "registry.npmjs.org", reason: "install deps",
      status: "pending", created_at: iso(3) }],
    messages: [],
    usage: [],
  };
}

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

function twoProjects(): MockDaemon {
  const daemon = new MockDaemon(teamUsage(team(10)));
  const w = webapp();
  w.usage = [session("s-web", w.agents[1], "w00000000001", { cost_micro_usd: 250_000 })];
  daemon.addProject(project("p2", "webapp", { effective: { pack: "node", model: "haiku", max_agents: 8 } }), w);
  return daemon;
}

const switcher = (page: Page) => page.getByRole("combobox", { name: "Project" });

test("the switcher shows each project's board; the choice is kept per browser", async ({ page }) => {
  const errors = await open(page, twoProjects());
  await expect(switcher(page)).toHaveValue("p1");
  await expect(page.locator(".board")).toContainText("Add tags to saved links");
  // The other project says what runs and waits there.
  await expect(switcher(page).locator('option[value="p2"]')).toHaveText("webapp · 2 running · 1 waiting");
  await switcher(page).selectOption("p2");
  await expect(page.locator(".board")).toContainText("Add a login page");
  await expect(page.locator(".board")).not.toContainText("Add tags to saved links");
  await expect(page.locator('.card[data-agent="bbbb00000002"]')).toBeVisible();
  await expect(page.locator('.card[data-agent="b2c3d4000001"]')).toHaveCount(0);
  await expect(page).toHaveTitle("(3) agent-office · webapp");
  await page.reload();
  await expect(switcher(page)).toHaveValue("p2");
  await expect(page.locator(".board")).toContainText("Add a login page");
  expect(errors).toEqual([]);
});

test("Approvals counts every project; another project's request is decided in the drawer, its agent opens there", async ({ page }) => {
  const daemon = twoProjects();
  const errors = await open(page, daemon);
  const badge = page.getByRole("button", { name: /^Approvals/ });
  // Omar's prompt and Amara's request in linkshelf, Theo's request in webapp.
  await expect(badge).toHaveAccessibleName("Approvals: 3 waiting");
  await badge.click();
  const drawer = page.getByRole("dialog", { name: "Approvals" });
  const other = drawer.locator('.other-project[data-project="p2"]');
  await expect(other.locator("h3")).toContainText("In webapp · 1");
  await expect(other).toContainText("registry.npmjs.org");
  await other.getByRole("button", { name: "Allow for this agent" }).click();
  await expect.poll(() => daemon.projectRequests).toContain("p2 POST /api/access-requests/7/approve");
  // Decided in webapp: its stream says so, and the badge drops to linkshelf's two.
  daemon.sendTo("p2", { type: "access_request", access_request: { ...webapp().accessRequests[0], status: "approved" } });
  await expect(badge).toHaveAccessibleName("Approvals: 2 waiting");
  await expect(drawer.locator('.other-project[data-project="p2"]')).toHaveCount(0);
  // A new wait in webapp: its agent's name switches project and opens the agent.
  daemon.sendTo("p2", { type: "access_request", access_request: { ...webapp().accessRequests[0], id: 8 } });
  await drawer.locator('.other-project[data-project="p2"]').getByRole("button", { name: "Theo" }).click();
  await expect(switcher(page)).toHaveValue("p2");
  await expect.poll(() => daemon.terminals).toContain("bbbb00000002");
  await expect(page.getByRole("tab", { name: /Theo/ })).toHaveAttribute("aria-selected", "true");
  expect(errors).toEqual([]);
});

test("a project is added from the Projects panel with its own pack, and removed after a confirmation", async ({ page }) => {
  const daemon = twoProjects();
  const errors = await open(page, daemon);
  await page.getByRole("button", { name: "Projects", exact: true }).click();
  const panel = page.getByRole("region", { name: "Projects" });
  await expect(panel.locator(".project-item")).toHaveCount(2);
  await expect(panel.getByLabel("Stack pack").first()).toHaveAttribute("placeholder", "daemon: go");
  const add = panel.locator(".project-add");
  await add.getByLabel("Repository path").fill("/home/dev/api-server");
  await add.getByLabel("Stack pack").fill("node");
  await add.getByLabel("Max agents").fill("2");
  await add.getByRole("button", { name: "Add project" }).click();
  await expect.poll(() => daemon.requests.find((r) => r.startsWith("POST /api/projects"))).toContain(
    '{"repo":"/home/dev/api-server","name":"","settings":{"pack":"node","max_agents":2}}');
  await expect(switcher(page)).toHaveValue("p3");
  await expect(switcher(page).locator("option")).toHaveText(["linkshelf · 9 running · 2 waiting", "webapp · 2 running · 1 waiting", "api-server"]);

  await page.getByRole("button", { name: "Projects", exact: true }).click();
  const item = page.getByRole("region", { name: "Projects" }).locator(".project-item").filter({ hasText: "webapp" });
  await item.getByRole("button", { name: "Remove…" }).click();
  await expect(item).toContainText("Its history, branches and worktrees are kept.");
  await item.getByRole("button", { name: "Remove", exact: true }).click();
  await expect.poll(() => daemon.requests).toContain("DELETE /api/projects/p2");
  await expect(switcher(page).locator("option")).toHaveText(["linkshelf · 9 running · 2 waiting", "api-server"]);
  expect(errors).toEqual([]);
});

test("a project's settings are changed in the panel; empty fields inherit the daemon's", async ({ page }) => {
  const daemon = twoProjects();
  await open(page, daemon);
  await page.getByRole("button", { name: "Projects", exact: true }).click();
  const item = page.getByRole("region", { name: "Projects" }).locator(".project-item").filter({ hasText: "webapp" });
  await item.getByRole("button", { name: "Settings" }).click();
  await item.getByLabel("Model").fill("sonnet");
  await item.getByLabel("Goal budget ($)").fill("1.5");
  await item.getByLabel("GitHub").selectOption("off");
  await item.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => daemon.requests.find((r) => r.startsWith("PATCH /api/projects/p2"))).toContain(
    '"settings":{"model":"sonnet","github":false,"budget_goal_micro_usd":1500000}');
});

test("the office has a floor per project; choosing a floor shows that project's team", async ({ page }) => {
  const daemon = twoProjects();
  const errors = await open(page, daemon);
  await page.getByRole("button", { name: "Office" }).click();
  const floors = page.getByRole("navigation", { name: "Floors" });
  await expect(floors.locator(".floor")).toHaveCount(2);
  await expect(floors.locator('.floor[data-floor="p1"]')).toHaveAttribute("aria-current", "true");
  await expect(floors.locator('.floor[data-floor="p2"] .count')).toHaveText("1");
  const linkshelf = daemon.world.agents.filter((a) => a.state !== "exited").length;
  await expect(page.locator(".nameplate")).toHaveCount(linkshelf);
  await floors.locator('.floor[data-floor="p2"]').click();
  await expect(switcher(page)).toHaveValue("p2");
  await page.getByRole("button", { name: "Office" }).click();
  await expect(page.getByRole("navigation", { name: "Floors" }).locator('.floor[data-floor="p2"]')).toHaveAttribute("aria-current", "true");
  await expect(page.locator(".nameplate")).toHaveCount(2);
  await expect(page.locator('.nameplate[data-agent="bbbb00000002"]')).toBeVisible();
  expect(errors).toEqual([]);
});

test("usage shows this project's sessions and every project's totals", async ({ page }) => {
  const errors = await open(page, twoProjects());
  await page.getByRole("button", { name: "Usage" }).click();
  const all = page.getByRole("region", { name: "All projects" });
  await expect(all.locator('tr[data-project="p1"]')).toContainText("linkshelf");
  await expect(all.locator('tr[data-project="p2"]')).toContainText("$0.25");
  await expect(all.getByTestId("all-projects-total")).toContainText("6"); // five linkshelf sessions and one webapp session
  expect(errors).toEqual([]);
});

test("with no project yet, the UI asks for the first one", async ({ page }) => {
  const daemon = new MockDaemon({ agents: [], tasks: [], accessRequests: [], messages: [] });
  daemon.projects.splice(0);
  await daemon.install(page);
  await page.goto("/");
  await expect(page.getByText("No project yet.")).toBeVisible();
  await page.getByLabel("Repository path").fill("/home/dev/linkshelf");
  await page.getByRole("button", { name: "Add project" }).click();
  await expect(page.locator(".board")).toBeVisible();
  await expect(switcher(page)).toHaveValue("p1");
});
